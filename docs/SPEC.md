# vRouter-Operator Design Spec

> CRD Architecture & Controller Design
>
> This document reflects the **implemented** state of the codebase.
> For planned features, see [TODO.md](TODO.md). For proposals, see [proposals/](proposals/).

---

## 1. Overview

vRouter-Operator is a Kubernetes Operator that manages VyOS virtual router configuration running on virtualization platforms. Through a provider abstraction layer, it supports multiple virtualization backends (KubeVirt, Proxmox VE, vrouter-daemon for bare metal or any VM, etc.). The default provider is KubeVirt (same cluster), which delivers configuration via qemu-guest-agent (QGA) over a virtio channel — no network reachability or sidecar container injection required.

---

## 2. Architecture

### 2.1 Controller Flow

```
VRouterTemplate + VRouterTarget + VRouterBinding
         │
   BindingController
   → resolve targetRefs
   → merge params (binding → target)
   → render and concatenate templates in order
   → create/update VRouterConfig per router (ownerRef → Binding)
         │
   VRouterController
   → dispatch to provider
   → render internal script template (config + commands → script)
   → write script to router via QGA
   → execute script via QGA
   → update VRouterConfig status
```

### 2.2 Provider Abstraction

Each provider instance is bound to a specific target router at construction time:

```go
type Provider interface {
    // IsVMRunning checks whether the underlying VM is in a running state.
    IsVMRunning(ctx context.Context) (bool, error)
    // CheckReady verifies the router is reachable and ready to accept config
    // (QGA ping + vyos-router.service is-active).
    CheckReady(ctx context.Context) error
    // WriteFile writes the apply script content to the router via guest agent.
    WriteFile(ctx context.Context, content []byte) error
    // ExecScript executes the apply script asynchronously, returns PID for tracking.
    ExecScript(ctx context.Context) (pid int64, err error)
    // GetExecStatus polls the result of a previously started script.
    GetExecStatus(ctx context.Context, pid int64) (*ExecStatus, error)
}

type ExecStatus struct {
    Exited   bool
    ExitCode int
    Stdout   string
    Stderr   string
}
```

The script destination path (`/tmp/vrouter-apply.sh`) and execution command (`/bin/vbash`) are fixed inside the provider. The controller only provides the script content.

### 2.3 KubeVirt Provider (Default)

Uses client-go SPDY exec into the virt-launcher pod to run QGA commands. No libvirt TCP API required.

```go
req := clientset.CoreV1().RESTClient().Post().
    Resource("pods").
    Name(virtLauncherPod).
    Namespace(namespace).
    SubResource("exec").
    VersionedParams(&corev1.PodExecOptions{
        Container: "compute",
        Command: []string{"virsh", "qemu-agent-command", domain, agentCmd},
    }, scheme.ParameterCodec)
```

### 2.4 Proxmox VE Provider

Uses Proxmox REST API to execute QGA commands on remote Proxmox nodes. API credentials and endpoints are managed by a `ProxmoxCluster` CRD; `VRouterTarget` references the cluster via `spec.provider.proxmox.clusterRef`. Supports endpoint failover (tries endpoints in order).

---

## 3. CRD Design

### 3.1 VRouterTemplate

Defines config generation logic using Go `text/template` syntax with sprig FuncMap.

```yaml
apiVersion: vrouter.kojuro.date/v1
kind: VRouterTemplate
metadata:
  name: bgp-router
spec:
  config: |               # hierarchical config, optional
    protocols {
      bgp {{ .asn }} {
        parameters {
          router-id {{ .routerId }}
        }
      }
    }
  commands: |             # set commands, optional
    set protocols bgp {{ .asn }} parameters router-id {{ .routerId }}
    set protocols bgp {{ .asn }} neighbor {{ .peerIp }} remote-as {{ .peerAsn }}
    {{ range .neighbors }}
    set protocols bgp {{ $.asn }} neighbor {{ .ip }} remote-as {{ .remoteAs }}
    {{ end }}
```

- `config` and `commands` can coexist; during apply, config is applied first, then commands
- Template engine uses `text/template` + sprig, supporting `range`, `default`, `required`, etc.

---

### 3.2 VRouterTarget

Defines target VMs by exact name, provider configuration, and associated params. Can be reused across Bindings.

```yaml
apiVersion: vrouter.kojuro.date/v1
kind: VRouterTarget
metadata:
  name: site-a-routers
spec:
  provider:
    type: kubevirt          # kubevirt (default) | proxmox | vrouter-daemon
    kubevirt:
      name: vyos-router-a
      namespace: production
      # kubeconfig:           # optional, defaults to same cluster
      #   secretRef:
      #     name: remote-cluster-kubeconfig
      #     key: kubeconfig
    # --- Proxmox VE example ---
    # type: proxmox
    # proxmox:
    #   vmid: 100
    #   clusterRef:                    # references a ProxmoxCluster resource
    #     name: pve-cluster
    #     namespace: default           # optional; if set, must equal this VRouterTarget's own
    #                                  # namespace — cross-namespace references are rejected
    # --- vrouter-daemon example (bare metal or any VM) ---
    # type: vrouter-daemon
    # daemon:
    #   address: "vrouter-daemon.vrouter-system.svc:50052"  # gRPC ControlService endpoint
    #   agentID: "router-a"                                  # target router agent identifier
  params:                 # arbitrary structure (x-kubernetes-preserve-unknown-fields)
    asn: 65001
    routerId: "10.0.0.1"
    neighbors:
      - ip: "10.0.0.2"
        remoteAs: 65002
        description: "upstream-a"
      - ip: "10.0.0.3"
        remoteAs: 65003
        description: "upstream-b"
    addressFamilies:
      ipv4:
        unicast: true
        redistribute:
          - connected
          - static
```

- `provider.type` defaults to `kubevirt` if omitted
- For KubeVirt, `kubevirt.name` identifies the VM; `kubevirt.namespace` defaults to this VRouterTarget's own namespace if omitted (an explicit different namespace is allowed, see §9.6); defaults to same cluster; optionally specify a remote kubeconfig via Secret
- For Proxmox, `proxmox.vmid` identifies the VM; `clusterRef` references a ProxmoxCluster resource
- For vrouter-daemon, `daemon.address` (gRPC endpoint) and `daemon.agentID` identify the target router agent; see README.md for deployment details
- **Same-namespace-only references**: `proxmox.clusterRef.namespace`, if set, must equal the VRouterTarget's own namespace. The validating webhook rejects a `clusterRef` pointing at a different namespace — see §6.2 for the full same-namespace policy and why it exists.
- `params` uses `x-kubernetes-preserve-unknown-fields: true`; stored as `apiextensionsv1.JSON` in Go
- Short names: `vrt`, `vroutertarget`

---

### 3.3 VRouterBinding

Combines an ordered list of templates with multiple targets, providing common params (lowest priority).

```yaml
apiVersion: vrouter.kojuro.date/v1
kind: VRouterBinding
metadata:
  name: bgp-binding
spec:
  templateRefs:             # ordered list; later templates' config/commands appended after earlier ones
    - name: base-config
    - name: bgp-router
  save: true              # persist config after commit, default: true
  params:                 # common, lowest priority
    ntpServer: "10.0.0.1"
    dnsServer: "8.8.8.8"
  targetRefs:
    - name: site-a-routers
    - name: site-b-routers
```

> **Backward compatibility**: The deprecated `templateRef` field (singular, optional) is still accepted. If set, it is prepended to `templateRefs` as the highest-priority (first) template.

**Same-namespace-only references**: every entry in `templateRef`, `templateRefs`, and `targetRefs` must resolve to an object in the VRouterBinding's own namespace. A `namespace` field left empty defaults to the binding's namespace (as usual); a `namespace` field set to any *other* namespace is rejected by the validating webhook. See §6.2 for the full policy.

Short names: `vrb`, `vrouterbinding`

---

### 3.4 ProxmoxCluster

Holds Proxmox VE cluster credentials and endpoint configuration. Shared across multiple `VRouterTarget` resources that reference the same cluster.

```yaml
apiVersion: vrouter.kojuro.date/v1
kind: ProxmoxCluster
metadata:
  name: pve-cluster
spec:
  endpoints:                         # tried in order until one responds
    - "https://pve1.example.com:8006"
    - "https://pve2.example.com:8006"
  credentialsRef:
    name: proxmox-credentials        # Secret containing api-token-id + api-token-secret
  insecureSkipTLSVerify: false
  syncInterval: 60s                  # how often to poll /cluster/resources
  checkGuestUptime: true             # enable guest-agent uptime check for soft-reboot detection (default: true)
status:
  lastSyncTime: "2026-01-01T00:00:00Z"
  conditions: []
```

- `endpoints`: multiple endpoints supported for HA Proxmox clusters; each request tries them in order. Must be non-empty; the validating webhook rejects `endpoints: []` (see §6.2).
- `credentialsRef`: references a Secret with keys `api-token-id` and `api-token-secret`. `credentialsRef.name` must be non-empty.
- `syncInterval`: controls poll frequency for node tracking (default: 60s). Must be greater than zero — a non-positive value would otherwise make the controller hot-poll the Proxmox API every reconcile.
- `checkGuestUptime`: when true (default), queries `/proc/uptime` via QGA to detect guest-initiated reboots that don't restart the QEMU process. This is a pointer bool (`*bool`): leaving the field unset defaults to `true`, and an explicit `checkGuestUptime: false` is honored and disables the guest-agent uptime check (see §9.9 ProxmoxCluster Types).
- Short names: `pxc`, `proxmoxcluster`

---

### 3.5 VRouterConfig (Generated / Direct)

The final artifact. Can be auto-generated by BindingController (ownerRef + labels → Binding) or created directly by the user (inline config).

```yaml
apiVersion: vrouter.kojuro.date/v1
kind: VRouterConfig
metadata:
  name: bgp-binding.site-a-routers  # {binding-name}.{targetRef.name}
  labels:                             # for listing/filtering by controller
    vrouter.kojuro.date/binding: bgp-binding
    vrouter.kojuro.date/target: site-a-routers
  ownerReferences:                    # for cascade delete when binding is removed
    - apiVersion: vrouter.kojuro.date/v1
      kind: VRouterBinding
      name: bgp-binding
      uid: xxxxxxxx-xxxx-xxxx-xxxx   # auto-filled by controllerutil.SetControllerReference()
      controller: true
      blockOwnerDeletion: true
spec:
  targetRef:                        # references the VRouterTarget
    name: site-a-routers
  save: true              # persist config after commit, default: true
  config: |               # rendered, optional
    protocols {
      bgp 65001 {
        parameters {
          router-id 10.0.0.1
        }
      }
    }
  commands: |             # rendered, optional
    set protocols bgp 65001 parameters router-id 10.0.0.1
    set protocols bgp 65001 neighbor 10.0.0.2 remote-as 65002
status:
  phase: Applied          # Pending / Applying / Applied / Failed
  execPID: 0              # QGA guest-exec PID, tracked during Applying phase
  lastAppliedTime: "2026-01-01T00:00:00Z"
  observedGeneration: 1
  message: ""
```

Short names: `vrc`, `vrouterconfig`

---

## 4. Params Merge Design

### 4.1 Priority Order

| Priority | Source | Description |
|----------|--------|-------------|
| 1 (lowest) | `binding.params` | Common base, shared across all targets |
| 2 (highest) | `target.params` | Overrides binding.params |

### 4.2 Deep Merge Implementation

Uses `dario.cat/mergo` for deep merge.

```go
func mergeParams(base, override apiextensionsv1.JSON) (map[string]interface{}, error) {
    var baseMap, overMap map[string]interface{}

    d1 := json.NewDecoder(bytes.NewReader(base.Raw))
    d1.UseNumber()   // avoid numbers being converted to float64
    d1.Decode(&baseMap)

    d2 := json.NewDecoder(bytes.NewReader(override.Raw))
    d2.UseNumber()
    d2.Decode(&overMap)

    mergo.Merge(&baseMap, overMap,
        mergo.WithOverride,
        mergo.WithOverrideEmptySlice,
    )
    return baseMap, nil
}
```

### 4.3 Merge Semantics

`mergo.WithOverride` is a *presence*-based merge, not a *truthiness*-based one: whether a key wins is decided by whether it is present in the override JSON, not by whether its decoded value is Go's zero value.

| Case | Behavior |
|------|----------|
| scalar override scalar | Override replaces base, **including when the override value is a zero value** (`false`, `""`, `0`). `mergo.WithOverride` only special-cases *nil* interface values, not JSON zero values — once `override.params` is decoded into a `map[string]interface{}`, `false`/`""`/`0` are ordinary non-nil values and always win. |
| map override map | mergo recursive merge |
| slice override slice, non-empty | Direct replacement (no append) |
| slice override slice, override is `[]` (present but empty) | Also replaces the base slice — `mergo.WithOverrideEmptySlice` is set specifically so an empty override list clears the base list instead of being ignored. |
| key entirely absent from the override JSON | Keeps base value — only keys that are present in the decoded override map participate in the merge. |

**Practical consequence**: there is no way to distinguish "the override explicitly set this key to false/empty/zero" from "the override didn't mention this key" once the override JSON has been decoded — both look the same to mergo except for the "is the key present at all" check above. A `target.params` (or `binding.params`) block that merely *mentions* a key, even with a zero value, silently overrides a truthy base default (e.g. a template's `firewall.enabled: true` can be turned off by a target that sets `firewall.enabled: false`, but also by any override value that happens to decode to `false`/`""`/`0`). This is intentional, pinned behavior — not a bug — and is covered by characterization tests. Template and target authors who need "leave this key alone" semantics must omit the key entirely from `params`, not set it to its zero value.

---

## 5. Template Engine

### 5.1 Configuration

```go
import "github.com/Masterminds/sprig/v3"

func renderTemplate(templateStr string, data map[string]interface{}) (string, error) {
    tmpl, err := template.New("config").
        Funcs(sprig.TxtFuncMap()).
        Parse(templateStr)
    if err != nil {
        return "", err
    }
    var buf bytes.Buffer
    err = tmpl.Execute(&buf, data)
    return buf.String(), err
}
```

### 5.2 Common sprig Functions

| Function | Purpose | Example |
|----------|---------|---------|
| `default` | Default value | `{{ default "8.8.8.8" .dnsServer }}` |
| `required` | Required check, errors if missing | `{{ required "asn is required" .asn }}` |
| `has` | Check if list contains a value | `{{ has "connected" .redistribute }}` |
| `toYaml` | Convert nested struct to yaml | `{{ .config \| toYaml }}` |
| `range` | Go template built-in | `{{ range .neighbors }}...{{ end }}` |

---

## 6. Webhooks

### 6.1 Defaulting Webhook

No defaulting logic is currently needed; field-level defaults are handled by kubebuilder markers (`+kubebuilder:default=...`).

### 6.2 Validating Webhook

Performs cross-field validation and reference checks that kubebuilder JSON schema markers cannot express:

| Resource | Validation |
|----------|------------|
| VRouterTarget | `provider.kubevirt` must be set when `type=kubevirt`; `provider.proxmox` must be set when `type=proxmox`; `provider.daemon` (with non-empty `address` and `agentID`) must be set when `type=vrouter-daemon`; for Proxmox, `clusterRef.name` is mandatory and `clusterRef.namespace`, if set, must equal the VRouterTarget's own namespace (cross-namespace references are rejected) |
| VRouterConfig | `spec.targetRef` must resolve to an existing VRouterTarget; `spec.targetRef.namespace`, if set, must equal the VRouterConfig's own namespace (cross-namespace references are rejected). VRouterConfig does **not** carry its own `provider` block, so there is no provider cross-field validation here — only the targetRef checks above. |
| VRouterBinding | `spec.targetRefs` must not be empty; at least one of `spec.templateRef` (deprecated) or `spec.templateRefs` must be set; every entry in `spec.templateRef`, `spec.templateRefs[]`, and `spec.targetRefs[]` must resolve to an existing object, and each entry's `namespace`, if set, must equal the VRouterBinding's own namespace (cross-namespace references are rejected) |
| VRouterTemplate | `spec.config` and `spec.commands`, when set, must each parse as valid Go `text/template` syntax (parsed with the same sprig FuncMap used at render time — see §5.1); a syntax error is rejected at admission instead of surfacing later as a render failure |
| ProxmoxCluster | `spec.endpoints` must be non-empty, and no entry may be an empty string; `spec.syncInterval` must be greater than zero; `spec.credentialsRef.name` must be non-empty |

All reference-existence and cross-namespace checks above are skipped during `ValidateUpdate` when the object already has a non-nil `deletionTimestamp`. This matters for the finalizer-removal update that every controller's `onDelete` performs (see §7): without the skip, a reference that has already been deleted (or a pre-existing cross-namespace reference — see the upgrade caveat below) would make that final update fail admission and deadlock deletion.

Webhooks are disabled when `ENABLE_WEBHOOKS=false` (env var, checked in `cmd/main.go`).

#### Same-namespace-only references (standing policy)

`VRouterBinding.spec.templateRef` / `templateRefs` / `targetRefs`, `VRouterConfig.spec.targetRef`, and `VRouterTarget.spec.provider.proxmox.clusterRef` may only reference objects in the same namespace as the referencing object. If a reference's `namespace` field is left empty it resolves to the referencing object's own namespace (via `ResolveNamespace`, unchanged); if it is set to any *other* namespace, the validating webhook rejects the create/update.

This is a **deliberate, standing design decision**, not a temporary limitation to be lifted later. Allowing an object in one namespace to reference credentials or targets in another namespace — with no authorization check beyond "the name resolves to something" — lets a tenant who can only create objects in their own namespace borrow another namespace's Proxmox API credentials or bind to another namespace's routers, and QGA `guest-exec` reaches the guest as root. Rejecting cross-namespace references at admission removes this confused-deputy path entirely, at the cost of not supporting shared credentials across namespaces.

If multiple namespaces need to target VMs on the same Proxmox cluster, co-locate a `ProxmoxCluster` (and the `Secret` it references via `credentialsRef`) in **each** namespace that needs it, rather than sharing one `ProxmoxCluster` across namespaces. This duplicates the endpoint/credential object but keeps every reference same-namespace and keeps the authorization boundary aligned with Kubernetes RBAC namespace boundaries.

> **Upgrade caveat**: enabling this webhook on an existing installation does not retroactively fix objects created before the webhook existed. A `VRouterBinding`, `VRouterConfig`, or `VRouterTarget` that already has a cross-namespace reference keeps working with its old (now inconsistent) behavior — the webhook only evaluates `ValidateCreate` and `ValidateUpdate`, so an existing object is not re-validated until the next time it is edited (or deleted and recreated). Operators upgrading to a version with this webhook enabled should audit existing objects for cross-namespace references (non-empty `namespace` fields in `templateRef`/`templateRefs`/`targetRefs`/`targetRef`/`clusterRef` that differ from the referencing object's own namespace) before relying on the same-namespace guarantee.

---

## 7. Controllers

### 7.1 BindingController

| Item | Description |
|------|-------------|
| Watch | VRouterBinding, VRouterTarget, VRouterTemplate |
| Output | VRouterConfig (ownerRef + labels → VRouterBinding) |
| Naming | `{binding-name}.{targetRef.name}` |
| Cleanup | Binding deleted → ownerRef cascade delete (K8s GC, background); targetRef removed → label-based orphan cleanup |

Reconcile flow:

```
if DeletionTimestamp.IsZero():
    → ensure finalizer present (add if missing, requeue)
    → reconcileNormal (onChange)
else:
    → reconcileDelete (onDelete)
```

**reconcileNormal (onChange)**:

1. Resolve effective template list via `effectiveTemplateRefs()` (deprecated `templateRef` prepended to `templateRefs`)
2. Lookup each template → get VRouterTemplate
3. Lookup each `targetRef` → get VRouterTarget
4. Merge params: `binding.params` → `target.params`
5. Render and concatenate config/commands from templates in order
6. Create/update VRouterConfig with:
   - `ownerReference` → binding (for cascade delete via K8s GC)
   - `vrouter.kojuro.date/binding` label → binding name (for listing)
   - `vrouter.kojuro.date/target` label → target name (for listing)
   - `targetRef` set to the VRouterTarget name
7. **Orphan cleanup**: list existing VRouterConfigs by label `vrouter.kojuro.date/binding={name}`, diff against desired set, delete orphans
8. **Update status condition**: always update `Ready` condition before returning
   - success → `Ready=True`, reason=`ReconcileSucceeded`
   - any error → `Ready=False`, reason=`ReconcileError`, message=error string

> **Note on ownerReference**: Uses `controllerutil.SetControllerReference()` with `blockOwnerDeletion: true`. This ensures the binding stays in "Deleting" state (foreground delete) until all dependent VRouterConfigs are cleaned up, making it easier to debug deletion order and verify cleanup completion.

**reconcileDelete (onDelete)**:

VRouterConfigs are owned by the binding via ownerRef, so K8s GC handles cascade deletion automatically. The controller simply removes the finalizer to unblock deletion:

```go
controllerutil.RemoveFinalizer(binding, FinalizerName)
return ctrl.Result{}, r.Update(ctx, binding)
```

**Condition (for `kubectl wait`):**

| Condition type | Status=True | Status=False |
|----------------|-------------|--------------|
| `Ready` | all VRouterConfigs reconciled successfully | any reconcile error |

```bash
kubectl wait --for=condition=Ready vrouterbinding/mybinding --timeout=60s
```

```go
// Orphan cleanup in BindingController.Reconcile()
desired := map[string]bool{}
for _, targetRef := range binding.Spec.TargetRefs {
    cfgName := fmt.Sprintf("%s.%s", binding.Name, targetRef.Name)
    desired[cfgName] = true
}

var existing v1.VRouterConfigList
client.List(ctx, &existing, client.MatchingLabels{
    "vrouter.kojuro.date/binding": binding.Name,
})

for _, cfg := range existing.Items {
    if !desired[cfg.Name] {
        client.Delete(ctx, &cfg)
    }
}
```

### 7.2 VRouterController

| Item | Description |
|------|-------------|
| Watch | VRouterConfig |
| Dependency | Provider-specific (KubeVirt: VMI + virt-launcher pod; Proxmox: API endpoint) |
| Apply | Pre-check → render script → write file → execute → poll |
| Status | `phase` is display-only; `observedGeneration` + `execPID` drive control flow |

**Reconcile flow:**

```
Reconcile():
  if DeletionTimestamp set → onDelete (remove finalizer)
  if finalizer missing    → add finalizer, requeue
  → onChange

onChange():
  1. Fetch VRouterTarget, create provider
  2. IsVMRunning()
     → false → skip (VM stopped, no action)
  3. CheckReady()
     → fail → return error (controller requeues with backoff)

  4. if execPID > 0:
       GetExecStatus(execPID)
       → still running, within the apply timeout → requeue(3s)
       → still running, past the apply timeout → clear execPID, set phase=Failed,
         condition Applied=False (reason=ApplyTimeout), return nil (no retry)
       → exec result lost (operator restarted and lost its in-memory exec
         tracking, or the guest agent that owned the PID restarted and no
         longer recognizes it) → clear execPID, set phase=Pending,
         requeue(3s) so the next reconcile re-dispatches a fresh exec
       → any other GetExecStatus error (not recognized as lost — e.g. the
         KubeVirt/Proxmox "unknown pid after guest agent restart" case that
         cannot yet be classified as lost, or a genuinely transient network
         error), within the apply timeout → return the error (controller
         requeues with backoff), execPID untouched
       → any other GetExecStatus error, past the apply timeout → same
         ApplyTimeout handling as "still running, past the apply timeout"
         above: clear execPID, set phase=Failed, condition Applied=False
         (reason=ApplyTimeout), return nil (no retry). This is what actually
         bounds the KubeVirt/Proxmox post-reboot "unknown pid" case today:
         since it cannot be classified as lost, it would otherwise return an
         error forever and — because execPID > 0 short-circuits the
         generation check in step 6 before it ever runs — deadlock the
         config exactly like the "exec result lost" case, immune even to a
         spec update.
       → exitCode == 0 → clear execPID, set phase=Applied, condition Applied=True
         (stamped with the generation this exec was dispatched for), return nil
       → exitCode != 0 → clear execPID, set phase=Failed, condition Applied=False
         (stamped with the generation this exec was dispatched for), return nil

  5. Check reboot: force re-apply only if target.status.lastRebootTime is newer
     than the last reboot this config has already dispatched an attempt for.
     A single reboot forces at most one re-apply attempt — even if that
     attempt fails — instead of re-dispatching on every reconcile.

  6. if generation == observedGeneration and phase is Applied/Failed and no
     reboot force-apply is pending (step 5):
       return nil  ← already done for this generation

  7. (new spec, or a reboot not yet handled)
     set condition Applied=False (reason=Applying) for the generation about
     to be dispatched, before the script starts running
     render internal script template
     WriteFile(script)
     pid = ExecScript()
     record dispatch time; set execPID=pid, observedGeneration=generation,
     "last reboot handled" = target.status.lastRebootTime, phase=Applying
     return requeue(3s)

onDelete():
  remove finalizer → unblock deletion
```

**Key semantics:**
- `generation == observedGeneration` → exec for this spec version has been dispatched (running or finished)
- `generation > observedGeneration` → new spec available, dispatch exec
- A target reboot newer than the last reboot this config has already attempted forces exactly one re-apply for the current generation, regardless of `observedGeneration`
- Failed phase has no auto-retry; the config only re-dispatches when the user updates spec (new generation) or the target reboots again. A reboot-forced apply that fails does **not** get re-dispatched again for the same reboot — this closes the hot-loop that a permanently-failing render (e.g. a bad `set` command) combined with a stale reboot timestamp used to cause.
- A dispatched apply that never exits is bounded by a fixed apply timeout; once exceeded, the config is marked Failed instead of being polled forever. There is currently no CRD field to tune this timeout per config. The timeout bounds both "GetExecStatus keeps succeeding but reports still running" and "GetExecStatus keeps failing with a non-lost error" — a provider that cannot reliably tell a stale/unknown-pid error apart from a transient one (see KubeVirt/Proxmox GetExecStatus doc comments) would otherwise retry the error indefinitely without ever reaching Applied, Failed, or a fresh dispatch.
- If the previously dispatched exec's result can no longer be retrieved, the controller does not treat this as a normal error to retry indefinitely — it clears the exec handle and re-dispatches a fresh `ExecScript` call, since retrying the same (now-meaningless) handle can never succeed.
- `Applied` is set to `False` the moment a new generation (or a reboot-forced re-apply) is dispatched — not only on failure — so `kubectl wait --for=condition=Applied` cannot return early against a stale `True` left over from a previous generation while a new apply is in flight. The condition's `observedGeneration` reflects the generation the exec was actually dispatched for, not necessarily the object's current `.metadata.generation` (which may have moved on while the script was still running).

**Condition (for `kubectl wait`):**

| Condition type | Status=True | Status=False |
|----------------|-------------|--------------|
| `Applied` | phase=Applied (for the generation the exec was dispatched for) | phase=Failed (including apply-timeout), or an apply for a new generation/reboot has just been dispatched (phase=Applying) |

```bash
kubectl wait --for=condition=Applied vrouterconfig/myconfig --timeout=60s
```

### 7.3 ProxmoxClusterController

| Item | Description |
|------|-------------|
| Watch | ProxmoxCluster (primary), VRouterTarget (informer for mapping) |
| Output | Updates `VRouterTarget.status.proxmoxNode` and `VRouterTarget.status.lastRebootTime` |
| Requeue | `RequeueAfter(spec.syncInterval)` on success |

**Reconcile flow (onChange):**

```
1. Check if syncInterval has elapsed since lastSyncTime; if not, requeue(remaining)

2. Read credentials from Secret (api-token-id + api-token-secret)
   Build HTTP client (respect insecureSkipTLSVerify)

3. GET /api2/json/cluster/resources?type=vm  (tries endpoints in order)
   → Build map: vmid → {node, uptime}

4. List all VRouterTargets referencing this ProxmoxCluster (clusterRef.name == cluster.Name)
   For each target:
     a. Update status.proxmoxNode = map[vmid].node (or "" if not found)
     b. Detect reboot from Proxmox uptime: if 0 < uptime ≤ 1.5 × syncInterval →
        derive bootTime = (time uptime was observed) - uptime, and set
        lastRebootTime = bootTime only if that is more than a small tolerance
        (5s, absorbs uptime rounding and measurement skew) newer than the
        currently stored lastRebootTime
     c. If checkGuestUptime enabled:
          Async exec `cat /proc/uptime` via QGA, poll with 10s timeout
          If 0 < guest uptime ≤ 1.5 × syncInterval → derive bootTime and update
          lastRebootTime using the same rule as 4b (using the time the guest
          uptime value was actually read back, not the time the sync started,
          since the QGA poll can itself take up to the full 10s timeout)
     d. Patch VRouterTarget.status

5. Set status.lastSyncTime = now, update Synced condition
   Return RequeueAfter(syncInterval)
```

**Purpose of `proxmoxNode` and `lastRebootTime`:**
- `proxmoxNode`: The provider factory reads this to build the Proxmox provider with the correct node name, since Proxmox QGA commands are node-scoped. Step 4a clears it to `""` (rather than leaving the previous value) when the VMID is absent from `/cluster/resources` — e.g. the VM was destroyed. Leaving a stale node cached here would otherwise let the provider factory's cached-node fallback target the wrong host if the same VMID is later reused on a different node. When `proxmoxNode` is `""`, the Proxmox provider falls back to a live `/cluster/resources` lookup on the next operation; if that lookup still cannot find the VMID (VM genuinely gone, not just moved), `IsVMRunning` reports "not running" rather than surfacing an error, so VRouterTargetController and VRouterConfigController take the same clean stopped path as an ordinary 404 from `/status/current` instead of looping on a reconcile error.
- `lastRebootTime`: VRouterController (§7.2) compares this against the last reboot it has already dispatched a forced re-apply for, to force re-apply after a reboot at most once per reboot. Deriving it as `bootTime = observedAt - uptime` (rather than stamping the sync's wall-clock time directly) and only advancing it when the new bootTime is meaningfully newer than the stored one means repeated syncs during the same reboot's low-uptime window settle on one stable value instead of continuously advancing lastRebootTime — which would otherwise make VRouterController force a redundant re-apply on every sync until uptime crossed the threshold.

---

### 7.4 Internal Script Template

The controller maintains a hardcoded script template. The `config` and `commands` fields from VRouterConfig are injected into this template to produce a complete vbash script, which is then written to the router and executed.

```bash
#!/bin/vbash
source /opt/vyatta/etc/functions/script-template
configure

# --- config section ---
{{- if .Config }}
load /dev/stdin <<'VYOS_CONFIG_EOF'
{{ .Config }}
VYOS_CONFIG_EOF
{{- else }}
load /opt/vyatta/etc/config.boot.default
{{- end }}

# --- commands section (optional) ---
{{- range .Commands }}
{{ . }}
{{- end }}

commit
{{- if .Save }}
save
{{- end }}
```

- `config`: loaded via `load /dev/stdin` as a config.boot fragment
- `commands`: individual `set` commands executed in configure mode
- `commit` is always appended; `save` is conditional (default: true), controlled by `spec.save`
- The script is written to a fixed path `/tmp/vrouter-apply.sh` (overwritten each time) then executed

### 7.5 QGA Script Execution Flow

All providers follow the same two-step pattern: write file → execute file.

**KubeVirt** (SPDY exec into virt-launcher pod):

```go
// WriteFile: guest-file-open → guest-file-write → guest-file-close
openCmd := `{"execute":"guest-file-open","arguments":{"path":"/tmp/vrouter-apply.sh","mode":"w"}}`
writeCmd := fmt.Sprintf(`{"execute":"guest-file-write","arguments":{"handle":%d,"buf-b64":"%s"}}`, handle, b64Script)
closeCmd := fmt.Sprintf(`{"execute":"guest-file-close","arguments":{"handle":%d}}`, handle)

// ExecScript: guest-exec → returns PID
execCmd := `{"execute":"guest-exec","arguments":{
  "path":"/bin/vbash",
  "arg":["/tmp/vrouter-apply.sh"],
  "capture-output":true}}`
// → save PID to status.execPID, return RequeueAfter(3s)

// GetExecStatus: guest-exec-status (called on next reconcile)
statusCmd := fmt.Sprintf(`{"execute":"guest-exec-status","arguments":{"pid":%d}}`, pid)
// → check exited field, update phase accordingly
```

**Proxmox VE** (REST API):

```
# WriteFile
POST /api2/json/nodes/{node}/qemu/{vmid}/agent/file-write
  { "file": "/tmp/vrouter-apply.sh", "content": "<base64>" }

# ExecScript → returns PID
POST /api2/json/nodes/{node}/qemu/{vmid}/agent/exec
  { "command": "/bin/vbash /tmp/vrouter-apply.sh" }

# GetExecStatus (called on next reconcile)
GET /api2/json/nodes/{node}/qemu/{vmid}/agent/exec-status?pid={pid}
```

---

## 8. RBAC Requirements

```yaml
rules:
- apiGroups: [""]
  resources: ["pods/exec"]
  verbs: ["create"]
- apiGroups: [""]
  resources: ["pods"]
  verbs: ["get", "list", "watch"]
- apiGroups: [""]
  resources: ["secrets"]         # for reading provider credentials
  verbs: ["get", "list", "watch"]
- apiGroups: ["kubevirt.io"]
  resources: ["virtualmachines", "virtualmachineinstances"]
  verbs: ["get", "list", "watch"]
- apiGroups: ["vrouter.kojuro.date"]
  resources: ["vrouterconfigs", "vroutertemplates",
              "vroutertargets", "vrouterbindings", "proxmoxclusters"]
  verbs: ["get", "list", "watch", "create", "update", "patch", "delete"]
- apiGroups: ["vrouter.kojuro.date"]
  resources: ["vrouterconfigs/status", "vrouterbindings/status",
              "proxmoxclusters/status", "vroutertargets/status"]
  verbs: ["get", "update", "patch"]
```

---

## 9. Go Types (kubebuilder)

CRD schemas are generated from Go structs via kubebuilder markers. Run `make manifests` to produce CRD YAML.

### 9.1 VRouterTemplate

```go
//+kubebuilder:object:root=true
//+kubebuilder:resource:shortName={vrtt,vroutertemplate}
type VRouterTemplate struct {
    metav1.TypeMeta   `json:",inline"`
    metav1.ObjectMeta `json:"metadata,omitempty"`
    Spec VRouterTemplateSpec `json:"spec,omitempty"`
}

type VRouterTemplateSpec struct {
    // +optional
    Config string `json:"config,omitempty"`
    // +optional
    Commands string `json:"commands,omitempty"`
}
```

### 9.2 VRouterTarget

```go
//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:resource:shortName={vrt,vroutertarget}
type VRouterTarget struct {
    metav1.TypeMeta   `json:",inline"`
    metav1.ObjectMeta `json:"metadata,omitempty"`
    Spec   VRouterTargetSpec   `json:"spec,omitempty"`
    Status VRouterTargetStatus `json:"status,omitempty"`
}

type VRouterTargetSpec struct {
    Provider ProviderConfig `json:"provider"`
    // +kubebuilder:pruning:PreserveUnknownFields
    // +kubebuilder:validation:Schemaless
    // +optional
    Params apiextensionsv1.JSON `json:"params,omitempty"`
}

type VRouterTargetStatus struct {
    // ProxmoxNode is the Proxmox cluster node currently hosting the VM.
    // Populated and kept up-to-date by ProxmoxClusterController.
    // +optional
    ProxmoxNode string `json:"proxmoxNode,omitempty"`
    // LastRebootTime is set when ProxmoxClusterController detects guest uptime
    // is within 1.5× syncInterval (recently rebooted).
    // +optional
    LastRebootTime *metav1.Time `json:"lastRebootTime,omitempty"`
}
```

### 9.3 VRouterBinding

```go
//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:resource:shortName={vrb,vrouterbinding}
type VRouterBinding struct {
    metav1.TypeMeta   `json:",inline"`
    metav1.ObjectMeta `json:"metadata,omitempty"`
    Spec   VRouterBindingSpec   `json:"spec,omitempty"`
    Status VRouterBindingStatus `json:"status,omitempty"`
}

type VRouterBindingSpec struct {
    // Deprecated: use TemplateRefs instead. If non-nil, prepended to TemplateRefs as highest priority.
    // +optional
    TemplateRef *NameRef `json:"templateRef,omitempty"`
    // TemplateRefs is an ordered list of templates to merge. Templates are applied in order;
    // later templates' config and commands are appended after earlier ones.
    // +optional
    TemplateRefs []NameRef `json:"templateRefs,omitempty"`
    TargetRefs   []NameRef `json:"targetRefs"`
    // Save controls whether the applied configuration is persisted, and is
    // propagated to the generated VRouterConfig.spec.save. Pointer bool so
    // an explicit `false` is honored — see VRouterConfigSpec.Save (§9.4).
    // +kubebuilder:default=true
    // +optional
    Save *bool `json:"save,omitempty"`
    // +kubebuilder:pruning:PreserveUnknownFields
    // +kubebuilder:validation:Schemaless
    // +optional
    Params apiextensionsv1.JSON `json:"params,omitempty"`
}

type VRouterBindingStatus struct {
    // +optional
    Conditions []metav1.Condition `json:"conditions,omitempty"`
}
```

### 9.4 VRouterConfig

```go
//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:resource:shortName={vrc,vrouterconfig}
//+kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
//+kubebuilder:printcolumn:name="Target",type=string,JSONPath=`.spec.targetRef.name`
//+kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type VRouterConfig struct {
    metav1.TypeMeta   `json:",inline"`
    metav1.ObjectMeta `json:"metadata,omitempty"`
    Spec   VRouterConfigSpec   `json:"spec,omitempty"`
    Status VRouterConfigStatus `json:"status,omitempty"`
}

type VRouterConfigSpec struct {
    // TargetRef references the VRouterTarget that describes the provider and router.
    TargetRef NameRef `json:"targetRef"`
    // Save controls whether the applied configuration is persisted so it survives
    // a reboot. Save is a pointer bool: nil (field absent) defaults to true via
    // +kubebuilder:default=true, but an explicit `false` is honored. A plain
    // `bool` cannot represent this — Go's JSON encoder omits a `false` value
    // when the field has `omitempty`, so the API server would re-apply the
    // default and silently turn an explicit `save: false` back into `true`.
    // +kubebuilder:default=true
    // +optional
    Save *bool `json:"save,omitempty"`
    // +optional
    Config string `json:"config,omitempty"`
    // +optional
    Commands string `json:"commands,omitempty"`
}

type VRouterConfigStatus struct {
    // +kubebuilder:validation:Enum=Pending;Applying;Applied;Failed
    // +kubebuilder:default=Pending
    Phase string `json:"phase,omitempty"`
    // +optional
    ExecPID int64 `json:"execPID,omitempty"`
    // ExecStartedTime records when the current execPID was dispatched, so the
    // controller can bound how long it polls before failing the config
    // instead of waiting forever (§7.2).
    // +optional
    ExecStartedTime *metav1.Time `json:"execStartedTime,omitempty"`
    // +optional
    LastAppliedTime *metav1.Time `json:"lastAppliedTime,omitempty"`
    // LastRebootHandledTime records the VRouterTarget.Status.LastRebootTime
    // value this config has already dispatched a reboot-forced apply attempt
    // for, so a single reboot forces at most one re-apply (§7.2) instead of
    // hot-looping when that attempt fails.
    // +optional
    LastRebootHandledTime *metav1.Time `json:"lastRebootHandledTime,omitempty"`
    // +optional
    Message string `json:"message,omitempty"`
    // ObservedGeneration is the generation for which exec was last dispatched.
    // +optional
    ObservedGeneration int64 `json:"observedGeneration,omitempty"`
    // Conditions for kubectl wait support. The "Applied" condition is True
    // when phase=Applied for the dispatched generation, and False both when
    // phase=Failed and while a new generation/reboot-forced apply is in
    // flight (phase=Applying) — see §7.2.
    // +optional
    Conditions []metav1.Condition `json:"conditions,omitempty"`
}
```

### 9.5 Shared Types

```go
// NameRef is a reference to a resource by name, optionally in another namespace.
// When Namespace is empty, the namespace of the referencing resource is used.
type NameRef struct {
    // +optional
    Namespace string `json:"namespace,omitempty"`
    Name      string `json:"name"`
}

// +kubebuilder:validation:Enum=kubevirt;proxmox;vrouter-daemon
type ProviderType string

const (
    ProviderKubeVirt      ProviderType = "kubevirt"
    ProviderProxmox       ProviderType = "proxmox"
    ProviderVRouterDaemon ProviderType = "vrouter-daemon"
)

type ProviderConfig struct {
    // +kubebuilder:default=kubevirt
    // +optional
    Type     ProviderType    `json:"type,omitempty"`
    // +optional
    KubeVirt *KubeVirtConfig `json:"kubevirt,omitempty"`
    // +optional
    Proxmox  *ProxmoxConfig  `json:"proxmox,omitempty"`
    // +optional
    Daemon   *DaemonConfig   `json:"daemon,omitempty"`
}

type SecretKeyRef struct {
    Name string `json:"name"`
    Key  string `json:"key"`
}

type SecretReference struct {
    Name string `json:"name"`
}
```

### 9.6 KubeVirt Types

```go
type KubeVirtConfig struct {
    Name string `json:"name"`
    // +optional
    Namespace string `json:"namespace,omitempty"`
    // +optional
    Kubeconfig *KubeconfigRef `json:"kubeconfig,omitempty"`
}

type KubeconfigRef struct {
    SecretRef SecretKeyRef `json:"secretRef"`
}
```

`kubevirt.namespace` is optional and defaults to the VRouterTarget's own namespace when omitted (applied consistently by `provider.New`, and by the VMI-watch mapping functions in VRouterTargetController and VRouterConfigController). Unlike `proxmox.clusterRef` (§9.7), this is not a same-namespace-restricted reference to another CRD in this cluster — it identifies where the remote VMI itself lives, optionally in a different cluster via `kubeconfig` — so an explicit cross-namespace value is accepted and not rejected by the validating webhook.

### 9.7 Proxmox Types

```go
// ProxmoxConfig identifies a VM on a Proxmox cluster.
// Credentials and endpoint live in the referenced ProxmoxCluster resource.
type ProxmoxConfig struct {
    // VMID of the virtual machine on Proxmox.
    VMID int `json:"vmid"`
    // ClusterRef references a ProxmoxCluster resource that holds endpoint and credentials.
    // Namespace may be omitted to use the same namespace as the VRouterTarget.
    ClusterRef NameRef `json:"clusterRef"`
}
```

### 9.8 Daemon Types

```go
const ProviderVRouterDaemon ProviderType = "vrouter-daemon"

// DaemonConfig holds vrouter-daemon ControlService connection parameters.
type DaemonConfig struct {
    // Address is the gRPC endpoint of the vrouter-daemon ControlService
    // (e.g., "vrouter-server.default.svc:9090").
    Address string `json:"address"`
    // AgentID is the identifier of the target router agent.
    AgentID string `json:"agentID"`
    // TimeoutSeconds is the maximum time to wait for a config apply to complete.
    // +optional
    // +kubebuilder:default=60
    TimeoutSeconds int32 `json:"timeoutSeconds,omitempty"`
}
```

The vrouter-daemon provider targets bare metal or any VM without KubeVirt or Proxmox VE, via a gRPC agent that connects back to a server deployed in the cluster. See README.md for deployment and the required Redis-backed server component; it is otherwise out of scope for this design doc, which focuses on the operator's controller/webhook/CRD layer.

### 9.9 ProxmoxCluster Types

```go
//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:resource:shortName={pxc,proxmoxcluster}
//+kubebuilder:printcolumn:name="Endpoint",type=string,JSONPath=`.spec.endpoints[0]`
//+kubebuilder:printcolumn:name="LastSync",type=date,JSONPath=`.status.lastSyncTime`
//+kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type ProxmoxCluster struct {
    metav1.TypeMeta   `json:",inline"`
    metav1.ObjectMeta `json:"metadata,omitempty"`
    Spec   ProxmoxClusterSpec   `json:"spec,omitempty"`
    Status ProxmoxClusterStatus `json:"status,omitempty"`
}

type ProxmoxClusterSpec struct {
    // Endpoints tried in order until one responds (HA cluster support).
    // Validated non-empty by the validating webhook (§6.2).
    Endpoints []string `json:"endpoints"`
    // Reference to a Secret containing api-token-id and api-token-secret.
    // CredentialsRef.Name validated non-empty by the validating webhook (§6.2).
    CredentialsRef SecretReference `json:"credentialsRef"`
    // +kubebuilder:default=false
    // +optional
    InsecureSkipTLSVerify bool `json:"insecureSkipTLSVerify,omitempty"`
    // SyncInterval controls poll frequency. Validated > 0 by the validating
    // webhook (§6.2); a non-positive value would otherwise make the
    // controller re-sync on every reconcile instead of waiting.
    // +kubebuilder:default="60s"
    // +optional
    SyncInterval metav1.Duration `json:"syncInterval,omitempty"`
    // CheckGuestUptime is a pointer bool so an explicit `false` is honored
    // (nil defaults to true) — same rationale as VRouterConfigSpec.Save (§9.4).
    // +kubebuilder:default=true
    // +optional
    CheckGuestUptime *bool `json:"checkGuestUptime,omitempty"`
}

type ProxmoxClusterStatus struct {
    // +optional
    LastSyncTime *metav1.Time `json:"lastSyncTime,omitempty"`
    // +optional
    Conditions []metav1.Condition `json:"conditions,omitempty"`
}
```

### 9.10 Constants

```go
const (
    FinalizerName = "vrouter.kojuro.date/finalizer"
    LabelBinding  = "vrouter.kojuro.date/binding"
    LabelTarget   = "vrouter.kojuro.date/target"
)

// VRouterConfig phase constants.
const (
    PhasePending  = "Pending"
    PhaseApplying = "Applying"
    PhaseApplied  = "Applied"
    PhaseFailed   = "Failed"
)

const ConditionApplied = "Applied"  // VRouterConfig
const ConditionReady   = "Ready"    // VRouterBinding
```

### 9.11 QGA Constants

```go
const (
    VyOSService = "vyos-router.service"
    ScriptPath  = "/tmp/vrouter-apply.sh"
)
```

QGA command templates: `CmdPing`, `CmdFileOpen`, `CmdFileWrite`, `CmdFileClose`, `CmdExecServiceSubState`, `CmdExecScript`, `CmdExecStatus`.

---

## 10. CRD Relationship Diagram

```
ProxmoxCluster ──clusterRef──→ VRouterTarget ←──targetRefs──  VRouterBinding ──templateRefs──→ VRouterTemplate(s)
(endpoints +                   (provider + params)                    (combine + common params)    (template logic)
 credentials)                        │
      │                              │
ProxmoxCluster                BindingController
Controller                    render per router
(polls node                          │ ownerRef + targetRef label
 & reboot)                           ▼
      │                       VRouterConfig (per router)
      └─ status.proxmoxNode   ├── spec.targetRef → VRouterTarget
         status.lastRebootTime├── spec.config     (rendered config.boot)
                              └── spec.commands   (rendered set commands)
                                         │
                                  VRouterController
                                  render internal script template
                                  (config + commands → vbash script)
                                         │
                                  write file → exec script
                                         │
                              ┌──────────┴──────────┐
                              ▼                     ▼
                        KubeVirt Provider     Proxmox Provider
                        SPDY exec → QGA      REST API → QGA
                              │                     │
                              ▼                     ▼
                        VirtualMachine        Proxmox VM
                        (VyOS applied)        (VyOS applied)
```

---

## 11. Direct VRouterConfig Usage (Without Template)

Template/Binding is optional. A single router can be configured directly by creating a VRouterConfig:

```yaml
apiVersion: vrouter.kojuro.date/v1
kind: VRouterConfig
metadata:
  name: vyos-router-a-config
spec:
  targetRef:
    name: vyos-router-a-target     # references a VRouterTarget
  save: true              # optional, defaults to true
  commands: |
    set interfaces ethernet eth0 address 10.0.0.1/24
    set protocols bgp 65001 parameters router-id 10.0.0.1
```

---

## 12. Dependencies

| Package | Purpose |
|---------|---------|
| `k8s.io/client-go` | SPDY exec, Kubernetes API |
| `kubevirt.io/api` | VMI type definitions (KubeVirt provider) |
| `k8s.io/apimachinery` | ObjectMeta |
| `k8s.io/apiextensions-apiserver` | apiextensionsv1.JSON |
| `github.com/Masterminds/sprig/v3` | Template functions |
| `dario.cat/mergo` | Params deep merge |
| `sigs.k8s.io/controller-runtime` | Controller framework |
