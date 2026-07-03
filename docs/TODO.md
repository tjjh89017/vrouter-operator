# TODO (Backlog)

Items here are design proposals and planned features. Do not implement unless explicitly instructed.

For detailed proposals, see [proposals/](proposals/).

---

## VRouterTarget Health Probe

**Goal**: Detect when a target VM goes down and recovers, then automatically re-trigger reconciliation of related VRouterConfigs so config is re-applied after recovery.

> Note: `VRouterTarget` already has `+kubebuilder:subresource:status` and a status struct with `ProxmoxNode` and `LastRebootTime`. The Health Probe would add `Health`, `Message`, `LastProbeTime`, and `LastTransitionTime` fields to the existing status.

### New Fields on VRouterTargetStatus

```go
type VRouterTargetStatus struct {
    // existing fields
    ProxmoxNode    string       `json:"proxmoxNode,omitempty"`
    LastRebootTime *metav1.Time `json:"lastRebootTime,omitempty"`

    // new fields for Health Probe
    // +kubebuilder:validation:Enum=Healthy;Unhealthy;Unknown
    // +kubebuilder:default=Unknown
    Health string `json:"health,omitempty"`
    // +optional
    Message string `json:"message,omitempty"`
    // +optional
    LastProbeTime *metav1.Time `json:"lastProbeTime,omitempty"`
    // +optional
    LastTransitionTime *metav1.Time `json:"lastTransitionTime,omitempty"`
}
```

### New: TargetController

| Item | Description |
|------|-------------|
| Watch | VRouterTarget, VMI (KubeVirt); Proxmox: poll |
| Output | Updates VRouterTarget.Status.Health |
| Side effect | On Unhealthy→Healthy transition, resets related VRouterConfig phases |

**Reconcile flow:**

```
1. Query VM state by provider type:
   KubeVirt: check VMI.status.phase == Running
   Proxmox:  GET /api2/json/nodes/{node}/qemu/{vmid}/status/current

2. Update VRouterTarget.Status.Health:
   VM Running   → Healthy
   VM other     → Unhealthy
   Query failed → Unknown

3. If Health transitions non-Healthy → Healthy:
   → List all VRouterConfigs with label vrouter.kojuro.date/target={name}
   → Patch phase: Applied → Pending for each
   → VRouterController picks up the change and re-applies
```

**Watch setup (KubeVirt):**

```go
ctrl.NewControllerManagedBy(mgr).
    For(&vrouterv1.VRouterTarget{}).
    Watches(
        &kubevirtv1.VirtualMachineInstance{},
        handler.EnqueueRequestsFromMapFunc(mapVMIToTarget),
    ).
    Complete(r)
```

Use a field index on `.spec.provider.kubevirt.name` + `.spec.provider.kubevirt.namespace` to efficiently map VMI → VRouterTarget.

### Trigger Summary

| Event | Action |
|-------|--------|
| VM goes down | Health: Healthy → Unhealthy. No VRouterConfig changes (wait for recovery). |
| VM recovers | Health: Unhealthy → Healthy. Batch patch VRouterConfigs phase: Applied → Pending. |
| VRouterConfig re-queued | VRouterController re-applies config via QGA. |

BindingController already watches VRouterTarget and will re-reconcile on health changes, but Binding re-reconcile only re-renders spec — it does **not** reset VRouterConfig phase. Phase reset must be done by TargetController.

---

## VRouterConfig: loadDefaultConfigOnDelete

**Goal**: Option to load a default/baseline config on the router when a VRouterConfig is deleted, so the router doesn't retain stale configuration.

### Considerations

- Semantics of "default config" need clear definition (VyOS startup config? user-specified baseline?)
- Deletion path becomes slow and can block if VM/QGA is unreachable (finalizer stuck)
- Interaction with `save: true` — after loading default, should it save again?
- Orphan cleanup in BindingController already deletes VRouterConfigs; triggering load-default on every orphan delete may cause unintended rollbacks when multiple configs target the same router
- Consider placing the policy at VRouterBinding level (`cleanupPolicy: LoadDefault | Retain`, similar to PV reclaimPolicy) rather than per-VRouterConfig
- Need a timeout + force-delete escape hatch to avoid permanently stuck finalizers

---

## `commands` Interpolation Trust Boundary

**Problem**: `VRouterTemplate.spec.config` and `spec.commands` are both Go templates rendered against the same merged params (`MergeParams(binding.Spec.Params, target.Spec.Params)`, target overrides binding — SPEC §4.1), but the two rendered outputs are embedded very differently in the generated apply script (SPEC §7.4, `internal/provider/qga/script.go`):

- `config` is written inside `load /dev/stdin <<'{{ .Delimiter }}'` — a quoted heredoc with a randomized per-render delimiter (`crypto/rand`-backed, SPEC §7.4). It is always treated as **data**, never as shell syntax, regardless of what a param value contains.
- `commands` is spliced into the script **verbatim** as executable vbash statements, with no quoting or escaping. This is by design — `commands` exists precisely so template authors can emit `set ...`/`delete ...` (and arbitrary vbash) lines. But it means any param value interpolated into a `commands` template becomes literal shell/vbash text: a param such as `foo; rm -rf /` or one containing backticks/`$( )` executes as written, as root, inside the router (the `sg vyattacfg` re-exec in the script template runs with the config-mode group, not as an unprivileged user).

Example: a template with `spec.commands: "set interfaces ethernet eth0 description '{{ .desc }}'"` and a `target.params.desc` of:

```
x'; delete interfaces ethernet eth0; commit; #
```

renders to a `commands` block that deletes the interface and commits, instead of setting a description — with no syntax error and no admission-time signal that this happened. The same applies to the vrouter-daemon provider (`internal/provider/daemon/provider.go`): `config`/`commands` are shipped as-is over gRPC to the remote agent, which renders/executes its own vbash script from them (SPEC §9.8) — the trust boundary is the same, just enforced (or not) in a different process.

**Current mitigations, and why they don't cover this**:

| Mitigation | Covers | Does not cover |
|---|---|---|
| Admission-time Go-template syntax check on `spec.config`/`spec.commands` (SPEC §6.2, VRouterTemplate webhook) | Malformed `{{ }}` syntax | Well-formed templates whose *rendered output*, after param substitution, is unsafe vbash — syntax validation runs on the template text, not on a rendered sample |
| sprig function denylist (`env`, `expandenv`, `getHostByName` — SPEC §5.1/§5.2, `internal/template/render.go`) | Operator-process secret/DNS exfiltration via template functions | Anything expressible without those functions, including plain `{{ .param }}` interpolation of attacker-controlled param values |
| Heredoc randomized delimiter (`internal/provider/qga/script.go`) | `config` breaking out of its heredoc | `commands`, which was never inside a heredoc — it is meant to run as shell, so there is no data/code boundary to protect |
| Same-namespace-only reference policy (SPEC §6.2 "Same-namespace-only references") | Confused-deputy access to another namespace's targets/credentials | Params supplied by a same-namespace, lower-privileged `VRouterBinding`/`VRouterTarget` author who is not trusted to run arbitrary vbash on the router |

Net effect: **anyone who can set `binding.spec.params` or `target.spec.params`** (not just `VRouterTemplate` authors) can inject arbitrary vbash if any `commands` template in the chain interpolates that param, even though `VRouterTemplate` is typically the more privileged/reviewed resource and `VRouterBinding`/`VRouterTarget` are typically edited more freely per-deployment.

### Mitigation Options

| # | Option | Trade-offs |
|---|--------|------------|
| (a) | Document `commands` templates as trusted-author-only; lint/reject param interpolation (`{{ .param... }}` or any non-literal expression) inside `spec.commands` at admission time | Cheapest to implement (webhook-side AST check on parsed template nodes); may be too strict for legitimate uses like `set interfaces ethernet {{ .ifName }}` where the value is expected to vary safely |
| (b) | Quote/escape param values when interpolated into `commands` (e.g. a `shellQuote` template func) | Doesn't compose with vbash's own syntax rules (`set` paths, config-mode grammar) the way POSIX shell quoting does; a single escaping scheme can't safely cover every place a param might land in a `set`/`delete`/arbitrary vbash line |
| (c) | Restrict `commands` templates to a whitelisted interpolation grammar (e.g. only allow params to fill specific `set`/`delete` argument positions via a structured helper, not raw template text) | Most robust, but a significant redesign of the `commands` field's contract and likely a breaking change for existing templates |
| (d) | Accept and document the risk: RBAC on who may create/edit `VRouterTemplate`, `VRouterBinding`, and `VRouterTarget` is the real trust boundary, not the template engine | No implementation cost; requires operators to treat write access to *any* of these three CRDs (not just VRouterTemplate) as router-root-equivalent, and to document this clearly in SPEC/README |

**Recommendation (needs maintainer decision)**: (d) as the documented baseline — the operator already relies on Kubernetes RBAC as the authorization boundary elsewhere (see SPEC §6.2's same-namespace-only rationale), and `commands` running arbitrary vbash is an intentional feature, not a bug to engineer away. Combine with (a) as a low-cost admission-time guard for the common case (template authors who did not intend `commands` to take untrusted param input at all). (b) and (c) are likely not worth the complexity given (d) already draws the RBAC line correctly, once it is actually written down.

**Open question for maintainer**: should `VRouterBinding.spec.params` / `VRouterTarget.spec.params` be documented as equally privileged as `VRouterTemplate.spec.commands` (i.e., "anyone who can set params on any of these three CRDs can run arbitrary vbash on the router"), or should (a)/(c) be pursued so that params stay a lower trust tier than templates?

---

## Open Design Questions

1. **QGA ping vs VMI phase**: VMI Running does not guarantee QGA is responsive (boot window). Option: use VMI phase as proxy; let VRouterController's natural failure → Failed handle the transient window. QGA-level ping adds complexity.

2. **Direct VRouterConfigs (no Binding)**: Configs created without a Binding have no `vrouter.kojuro.date/target` label. TargetController cannot find them. Option: VRouterController stamps the target label when `spec.provider` can be matched to a VRouterTarget.

3. **RBAC**: TargetController needs `get/list/watch` on `virtualmachineinstances` (already in §8) and `update/patch` on `vrouterconfigs/status` and `vrouterconfigs`.
