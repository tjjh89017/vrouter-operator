# TODO (Backlog)

Items here are design proposals and planned features. Do not implement unless explicitly instructed.

For detailed proposals, see [proposals/](proposals/).

---

## VRouterTarget Health Probe

**Goal**: Detect when a target VM goes down and recovers, then automatically re-trigger reconciliation of related VRouterConfigs so config is re-applied after recovery.

> Note: `VRouterTarget` already has `+kubebuilder:subresource:status` and a status struct with `ProxmoxNode` and `LastRebootTime`. The Health Probe would add `Health`, `Message`, `LastProbeTime`, and `LastTransitionTime` fields to the existing status.

**Verified gap this would close**: `VRouterTargetReconciler.Reconcile` (`internal/controller/vroutertarget_controller.go`) currently collapses "provider query failed" and "not yet polled since a transient error" into silence — on a `prov.IsVMRunning` error it only logs and returns `RequeueAfter: vmRunningPollInterval` (60s) for backoff, leaving `status.VMRunning` at its last-known value with no signal that the value is stale (checked 2026-07: a destroyed Proxmox VM now cleanly returns `false` via `errVMNotFound` in `internal/provider/proxmox/provider.go`, so this is specifically about persistent errors — expired credentials, network partition to the API, kubeconfig expiry for a remote KubeVirt cluster — not the "VM genuinely gone" case). A sustained failure (e.g. an hour-long credential outage) is invisible to anyone reading `kubectl get vroutertarget`, which only shows the boolean `VM Running` printer column.

This is judged **not worth an isolated fix** right now: `status.VMRunning` is purely informational (a printer column, per SPEC §9.2) — no controller gates behavior on it. `VRouterConfigReconciler.onChange` (`internal/controller/vrouterconfig_controller.go`) calls `prov.IsVMRunning` itself, independently of this field, so it has its own (already-correct) error handling and does not inherit the staleness. The only other reader, `ProxmoxClusterReconciler.markTargetsStopped`, only reads it to skip a redundant patch — reading a stale value there just means one extra no-op write next cycle, not an observable bug. Fixing the staleness in isolation (e.g. a bare `lastProbeError` field, or a condition with no consumer) would add CRD/status API surface — `make generate`/`make manifests`, a SPEC §9.2 update, and a Helm chart CRD resync — for a display-only field, which is disproportionate on its own. The `Health`/`Unknown` fields proposed below are the right place to fold this in (a query failure naturally maps to `Health: Unknown`, distinct from `Healthy`/`Unhealthy`), so this note documents the gap rather than special-casing a separate fix.

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

**Problem**: `VRouterTemplate.spec.config` and `spec.commands` are both Go templates rendered against the same merged params (`MergeParamsLayers`: `paramsRefs` in list order, then `binding.Spec.Params`, then `target.Spec.Params`, later layers override — SPEC §4.1), but the two rendered outputs are embedded very differently in the generated apply script (SPEC §7.4, `internal/provider/qga/script.go`):

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

Net effect: **anyone who can set `binding.spec.params`, `target.spec.params`, or the params of a `VRouterParams` referenced via `binding.spec.paramsRefs`** (not just `VRouterTemplate` authors) can inject arbitrary vbash if any `commands` template in the chain interpolates that param, even though `VRouterTemplate` is typically the more privileged/reviewed resource and `VRouterBinding`/`VRouterTarget` are typically edited more freely per-deployment.

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

## Abandoned Exec on Apply Timeout / Lost Exec Result

**Problem**: VRouterController (SPEC §7.2, `internal/controller/vrouterconfig_controller.go`) dispatches the apply script via `prov.ExecScript` and tracks it purely by an opaque handle (`status.execPID`), polling `prov.GetExecStatus` until it reports exit. Two paths give up on that handle **without ever telling the guest to stop the script**:

1. **Apply timeout (`ApplyTimeout` reason, `failApplyTimeout`)** — fires when `pollExecStatus` sees no confirmed exit within `execApplyTimeout` (5 min, a fixed constant), and also — since the router-unreachable case was folded in — when `handleCheckReadyFailure` sees the same in-flight exec has outlived `execApplyTimeout` while `CheckReady` is failing. Either way, the controller clears `status.execPID`/`execStartedTime` and marks the config `Failed`. The script may still be running (or hung) inside the guest; nothing was sent to stop it.
2. **Lost exec result (`providertypes.ErrExecResultLost`)** — the guest agent's exec registry no longer knows the PID (operator restart lost its in-memory map for the daemon provider; a VM/guest-agent restart forgets in-flight `guest-exec` PIDs for KubeVirt/Proxmox — see the "Known limitation" comments on `GetExecStatus` in `internal/provider/kubevirt/provider.go` and `internal/provider/proxmox/provider.go`). `pollExecStatus` resets the config to `Pending` and the next reconcile re-dispatches a fresh `ExecScript`. A guest-agent (or operator-process) restart does not kill the child process tree it was tracking, so the **original script can still be executing** when the replacement is dispatched — the two now run concurrently against the same router.

**Consequences**:

- Two concurrent `configure`/`commit` sessions against the same running-config session-lock: depending on how the virtual router's config subsystem serializes them, the second `commit` may block waiting for a lock the first (abandoned) session still holds — reintroducing the same "stuck in Applying forever" failure mode `execApplyTimeout` exists to bound, just one layer down, in a place the controller cannot see or time out — or the two may interleave and produce a config that is a partial merge of both attempts.
- Double `save` (writing `config.boot`) from two overlapping scripts is at best redundant, at worst a torn write if both `save` calls land close together.
- The abandoned session may itself later run to completion (or time out on its own) and issue a final `commit`/`save` *after* the replacement exec's result has already been recorded — silently overwriting a config the controller believes is current with a stale one from the abandoned attempt.
- For the vrouter-daemon provider (`internal/provider/daemon/provider.go`), the same class of problem applies through a different mechanism: `ExecScript` starts a goroutine that calls the remote `ApplyConfig` RPC and stores the result in a package-level `execMap`. If the **operator process** restarts, `execMap` is lost and `GetExecStatus` returns `ErrExecResultLost` (see the `"no pending exec for pid %d (operator may have restarted)"` error), but the goroutine's own `context.Background()`-rooted call was scoped to the old process and is gone with it — whether the *remote agent* also aborts the in-progress apply when the underlying gRPC call is dropped, or keeps running it to completion server-side, is up to the daemon agent's implementation and is not visible or controlled from this repo. Re-dispatch after `ErrExecResultLost` risks the same concurrent-apply scenario as KubeVirt/Proxmox, with even less visibility into what the abandoned attempt is doing.

**Current behavior**: nothing in `internal/controller/vrouterconfig_controller.go` or the `Provider` interface (`internal/provider/types/types.go`) attempts to terminate, fence, or even check on an abandoned script before or after re-dispatching. `failApplyTimeout` and the `ErrExecResultLost` branch of `pollExecStatus` both only mutate `VRouterConfig.Status` — the guest-side process is left to run to whatever completion it reaches on its own.

**A third, cheaper-to-trigger path into the same window**: `applyConfig` calls `prov.ExecScript` (dispatching the script into the guest) and only afterwards persists `status.execPID`/`execStartedTime`/`observedGeneration` via `r.Status().Patch`. If that patch itself fails — after the script has already started running — the reconcile returns an error, `status.execPID` is left at its old value (0, if this was a fresh dispatch), and the next reconcile (controller-runtime's own error-driven requeue, or a later watch event) sees no exec in flight and calls `ExecScript` again, running a second script concurrently with the first: the same `configure`/`commit`-session and stale-overwrite consequences described above, just reached without either `execApplyTimeout` or `ErrExecResultLost` firing first. This is judged a narrow, low-likelihood window rather than a separate problem to solve on its own: `VRouterConfig.Status` has exactly one writer (this reconciler; no other controller or webhook patches it), and controller-runtime serializes reconciles per object key, so there is no *other actor* racing this patch — the realistic trigger is a generic transient API-server/etcd error, not a `resourceVersion` conflict. In fact a real conflict is close to impossible here regardless: `applyConfig` builds its patch with plain `client.MergeFrom(cfg.DeepCopy())` (no `client.MergeFromWithOptimisticLock`), so the patch carries no `resourceVersion` precondition and cannot itself fail with `IsConflict` the way an `Update` could. That also means `client-go`'s `retry.RetryOnConflict` would not help: it retries only on `apierrors.IsConflict`, which this call cannot produce, so it would add complexity without covering the actual (non-conflict, transient) failure mode. A more general "retry any error before returning" wrapper would cover it, but is a heavier, less local change than it looks — it would need its own bounded-retry/backoff policy distinct from the reconcile-level backoff controller-runtime already applies on the returned error today (the status quo already retries, just via a full extra dispatch rather than a same-reconcile in-place retry) — so it is left undone here and folded into the mitigation options below rather than special-cased.

**Mitigation options**:

| # | Option | Trade-offs |
|---|--------|------------|
| (a) | Advisory lock (`flock`) around the generated apply script (`internal/provider/qga/script.go`'s `applyScriptTmpl`) on a fixed path (e.g. `/tmp/vrouter-apply.lock`), so a second concurrent instance blocks or exits immediately instead of racing the first into `configure`/`commit` | Cheap, self-contained in the script template; prevents *concurrent* execution but does not stop the abandoned process from eventually running its now-stale config to completion after the flock is released — only serializes, does not cancel |
| (b) | Kill-before-redispatch: before calling `ExecScript` again after `ErrExecResultLost` or `ApplyTimeout`, dispatch a new exec that kills the previous PID (e.g. `guest-exec` of `kill -TERM <pid>` for KubeVirt/Proxmox, since standard QGA has no `guest-exec-kill`/cancel verb — only `guest-exec`, `guest-exec-status`, per `internal/provider/qga/constants.go`) | Directly addresses the root cause; but is unreliable exactly when it matters most — `ErrExecResultLost` and `ApplyTimeout` are both triggered by agent/connectivity trouble, i.e. the same conditions that make a *new* exec (the kill itself) unreliable; also cannot address the daemon provider, which has no PID-level primitive over the `ControlService` gRPC API (`ApplyConfig`/`IsConnected` only, SPEC §9.8) |
| (c) | Generation fencing: stamp a per-dispatch nonce/generation into the rendered script (e.g. an env var or a marker file written before `configure`) and have the script check-and-abort (`commit` refuses / script exits before `commit`) if a newer nonce has since been written by a later dispatch | Detects staleness from *inside* the guest without needing a kill primitive, so it also covers the daemon provider; requires the script to re-read shared state at exactly the right point (immediately before `commit`) to close the race, and still does not stop a script already past that check-point mid-`commit` |
| (d) | Accept and document: keep `execApplyTimeout` as a best-effort bound on the *controller's* view, explicitly document that an abandoned exec is not fenced or killed, and rely on operators noticing repeated `ApplyTimeout`/lost-result cycles (both already logged and surfaced in `status.message`) as a signal to intervene manually (e.g. reboot the target or inspect the router directly) | No implementation cost; leaves the concurrent-session and stale-overwrite consequences above live in production, with no automated defense |

**Recommendation (needs maintainer decision)**: (a) as a low-cost first layer — a flock in the script template is a small, self-contained change that at least turns "two config sessions interleave unpredictably" into "the second instance waits or exits cleanly," and it benefits the KubeVirt/Proxmox path immediately. Pair it with (c) if the maintainer wants the daemon provider covered too and is willing to accept the script-side complexity of a staleness check. (b) is likely not worth pursuing on its own — it is least reliable in precisely the failure modes (`ErrExecResultLost`, `ApplyTimeout`) it is meant to fix, and does not extend to the daemon provider at all. (d) should be written down regardless of which of (a)/(b)/(c) is also implemented, since none of them fully close the window between "script starts running" and "lock/fence takes effect."

**Cross-references**: SPEC §7.2 (VRouterController reconcile flow, `execApplyTimeout`, phase/condition semantics), SPEC §9.4 (`VRouterConfigStatus.ExecPID`/`ExecStartedTime`/`LastRebootHandledTime`), SPEC §9.8 (vrouter-daemon `ControlService` API surface), SPEC §7.4 (apply script template); `internal/controller/vrouterconfig_controller.go` (`failApplyTimeout`, `pollExecStatus`, `handleCheckReadyFailure`, `applyConfig`); `internal/provider/types/types.go` (`ErrExecResultLost`, `Provider.ExecScript`/`GetExecStatus`); `internal/provider/qga/script.go` and `internal/provider/qga/constants.go` (apply script template, QGA command set — no kill/cancel verb); `internal/provider/daemon/provider.go` (`execMap`, `ExecScript`/`GetExecStatus`).

---

## Open Design Questions

1. **QGA ping vs VMI phase**: VMI Running does not guarantee QGA is responsive (boot window). Option: use VMI phase as proxy; let VRouterController's natural failure → Failed handle the transient window. QGA-level ping adds complexity.

2. **Direct VRouterConfigs (no Binding)**: Configs created without a Binding have no `vrouter.kojuro.date/target` label. TargetController cannot find them. Option: VRouterController stamps the target label when `spec.provider` can be matched to a VRouterTarget.

3. **RBAC**: TargetController needs `get/list/watch` on `virtualmachineinstances` (already in §8) and `update/patch` on `vrouterconfigs/status` and `vrouterconfigs`.
