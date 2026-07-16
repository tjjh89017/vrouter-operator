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
VRouterTemplate + VRouterParams + VRouterTarget + VRouterBinding
         │
   BindingController
   → resolve targetRefs
   → merge params (paramsRefs → binding → target)
   → render and concatenate templates in order
   → create/update VRouterConfig per router (ownerRef → Binding)
         │
   VRouterController
   → dispatch to provider: ExecScript(config, commands, save)
   → provider renders the internal script template, writes it to the
     router, and executes it, all via QGA (see §7.4/§7.5)
   → poll GetExecStatus, update VRouterConfig status
```

### 2.2 Provider Abstraction

Each provider instance is bound to a specific target router at construction time:

```go
type Provider interface {
    // IsVMRunning checks whether the underlying VM is in a running (non-stopped) state.
    IsVMRunning(ctx context.Context) (bool, error)
    // CheckReady verifies the router is reachable and ready to accept config.
    // KubeVirt/Proxmox: QGA responds to ping, then polls
    // `systemctl show -p SubState vyos-router.service` until it exits and
    // reports substate "exited" — vyos-router is a oneshot unit that runs to
    // completion, so readiness is "has finished", not "is-active/running".
    // vrouter-daemon: no-op (a successful IsVMRunning already implies the
    // agent connection is up and ready).
    CheckReady(ctx context.Context) error
    // ExecScript renders and applies the given VyOS config asynchronously.
    // config is a VyOS config block, commands is a block of configure-mode
    // commands, save controls whether the running config is persisted to
    // disk after commit. Returns a provider-specific handle (e.g. PID) for
    // polling via GetExecStatus.
    ExecScript(ctx context.Context, config, commands string, save bool) (handle int64, err error)
    // GetExecStatus polls the result of a previously started ExecScript call.
    GetExecStatus(ctx context.Context, handle int64) (*ExecStatus, error)
}

type ExecStatus struct {
    Exited   bool
    ExitCode int
    Stdout   string
    Stderr   string
}
```

There is no separate `WriteFile` step on the interface: each provider's `ExecScript` renders the internal script (§7.4) from the raw `config`/`commands`/`save` it is given, then writes and executes it via QGA (KubeVirt, Proxmox) or hands the raw config to the remote agent to render and run (vrouter-daemon, §9.8) — script rendering is a provider-internal concern, not something the controller does upfront. The script destination path (`/tmp/vrouter-apply.sh`) and execution command (`/bin/vbash`) are fixed inside the KubeVirt/Proxmox providers.

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
- Template engine uses `text/template` + sprig, supporting `range`, `default`, `has`, etc.

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
  paramsRefs:               # ordered list; later entries override earlier ones; see §3.6/§4.1
    - name: site-defaults
    - name: subnet-params
  save: true              # persist config after commit, default: true
  params:                 # common, overrides all paramsRefs; still overridden by target.params
    ntpServer: "10.0.0.1"
    dnsServer: "8.8.8.8"
  targetRefs:
    - name: site-a-routers
    - name: site-b-routers
```

> **Backward compatibility**: The deprecated `templateRef` field (singular, optional) is still accepted. If set, it is prepended to `templateRefs` as the highest-priority (first) template.

- `paramsRefs` is an ordered, optional list of `VRouterParams` objects (§3.6) to merge as the lowest-priority params layer: later entries override earlier ones, and the fully-merged `paramsRefs` result is itself overridden by this binding's own `params`, which in turn is overridden by the target's `params` (§4.1). An empty or absent `paramsRefs` list is valid and simply contributes no extra layer.

**Same-namespace-only references**: every entry in `templateRef`, `templateRefs`, `paramsRefs`, and `targetRefs` must resolve to an object in the VRouterBinding's own namespace. A `namespace` field left empty defaults to the binding's namespace (as usual); a `namespace` field set to any *other* namespace is rejected by the validating webhook. See §6.2 for the full policy.

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

### 3.6 VRouterParams

Holds a reusable, schemaless block of `params` that one or more `VRouterBinding` objects can merge in via `spec.paramsRefs` (§3.3). Has no status subresource — it is a pure data object.

```yaml
apiVersion: vrouter.kojuro.date/v1
kind: VRouterParams
metadata:
  name: subnet-params
spec:
  params:                 # arbitrary structure (x-kubernetes-preserve-unknown-fields), same shape as target.params
    subnetGateway: "192.168.1.1"
```

- `params` uses `x-kubernetes-preserve-unknown-fields: true` (schemaless), stored as `apiextensionsv1.JSON` in Go — identical treatment to `VRouterTarget.spec.params` and `VRouterBinding.spec.params`
- Referenced only by `VRouterBinding.spec.paramsRefs`, same-namespace only (§3.3, §6.2); no other CRD references a VRouterParams
- Cannot be deleted while any VRouterBinding in the same namespace still references it via `paramsRefs` — see §6.2
- Short names: `vrtp`, `vrouterparams`

---

## 4. Params Merge Design

### 4.1 Priority Order

| Priority | Source | Description |
|----------|--------|-------------|
| 1 (lowest) | `paramsRefs` (in list order) | Ordered list of `VRouterParams` (§3.6) resolved by BindingController; later entries override earlier ones |
| 2 | `binding.params` | Common base, shared across all targets; overrides the merged `paramsRefs` result |
| 3 (highest) | `target.params` | Overrides both `binding.params` and `paramsRefs` |

BindingController's `onChange` builds the layer list in exactly this order — `paramsRefs` (as resolved, in `spec.paramsRefs` list order), then `binding.Params`, then `target.Params` — and folds them with `MergeParamsLayers` (§4.2), so `target.params` always wins overall, matching the pre-`paramsRefs` behavior when `paramsRefs` is empty.

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

`MergeParamsLayers(layers ...apiextensionsv1.JSON)` (`internal/template/render.go`) extends this to more than two layers: it folds an ordered list into a single map by repeatedly calling `MergeParams` — the first layer is the base, and each subsequent layer overrides the accumulated result so far, so the last layer given has the highest priority overall (an empty layer list returns an empty map). It does not change `MergeParams`' own two-arg signature or semantics; each fold step re-encodes the running result as JSON and is just another `MergeParams` call. BindingController's `onChange` calls it with `paramsRefs` (in list order) followed by `binding.Params` then `target.Params`, producing the 3-layer priority order in §4.1.

### 4.3 Merge Semantics

`mergo.WithOverride` is a *presence*-based merge, not a *truthiness*-based one: whether a key wins is decided by whether it is present in the override JSON, not by whether its decoded value is Go's zero value.

| Case | Behavior |
|------|----------|
| scalar override scalar | Override replaces base, **including when the override value is a zero value** (`false`, `""`, `0`). `mergo.WithOverride` only special-cases *nil* interface values, not JSON zero values — once `override.params` is decoded into a `map[string]interface{}`, `false`/`""`/`0` are ordinary non-nil values and always win. |
| map override map | mergo recursive merge |
| slice override slice, non-empty | Direct replacement (no append) |
| slice override slice, override is `[]` (present but empty) | Also replaces the base slice — `mergo.WithOverrideEmptySlice` is set specifically so an empty override list clears the base list instead of being ignored. |
| key entirely absent from the override JSON | Keeps base value — only keys that are present in the decoded override map participate in the merge. |

**Practical consequence**: there is no way to distinguish "the override explicitly set this key to false/empty/zero" from "the override didn't mention this key" once the override JSON has been decoded — both look the same to mergo except for the "is the key present at all" check above. A `target.params` (or `binding.params`) block that merely *mentions* a key, even with a zero value, silently overrides a truthy base default (e.g. a template's `firewall.enabled: true` can be turned off by a target that sets `firewall.enabled: false`, but also by any override value that happens to decode to `false`/`""`/`0`). This is intentional, pinned behavior — not a bug — and is covered by characterization tests. Template and target authors who need "leave this key alone" semantics must omit the key entirely from `params`, not set it to its zero value. These per-pair semantics apply at every fold step of `MergeParamsLayers` (§4.2), not only to a single two-layer merge — e.g. a `paramsRefs` entry that merely mentions a key with a zero value can silently override an earlier `paramsRefs` entry's truthy value, exactly as `target.params` can override `binding.params`.

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

**Function denylist**: templates render inside the operator process, so the full sprig function map is not exposed as-is. `env`, `expandenv`, and `getHostByName` are removed before parsing — an unrestricted template could otherwise read the operator pod's environment (potentially including injected secrets/tokens) via `{{ env "X" }}`/`{{ expandenv "$X" }}`, or trigger arbitrary DNS lookups via `getHostByName`. All other sprig helpers (`default`, `has`, `range`, etc.) are unaffected. This filtered function map is the single source of truth for both controller-side rendering and the VRouterTemplate validating webhook's admission-time syntax check (§6.2), so a template calling a denied function is rejected at admission with "function ... not defined" instead of only failing later at reconcile.

### 5.2 Common sprig Functions

The function set is sprig v3's `TxtFuncMap()` minus the denylist in §5.1 (`env`, `expandenv`, `getHostByName`). Note that `required` and `toYaml` are **not** available — those are Helm-specific additions, not sprig functions, and are not implemented by this operator.

| Function | Purpose | Example |
|----------|---------|---------|
| `default` | Default value | `{{ default "8.8.8.8" .dnsServer }}` |
| `has` | Check if list contains a value | `{{ has "connected" .redistribute }}` |
| `range` | Go template built-in | `{{ range .neighbors }}...{{ end }}` |

---

## 6. Webhooks

### 6.1 Defaulting Webhook

No defaulting logic is currently needed; field-level defaults are handled by kubebuilder markers (`+kubebuilder:default=...`).

### 6.2 Validating Webhook

Performs cross-field validation and reference checks that kubebuilder JSON schema markers cannot express:

| Resource | Validation |
|----------|------------|
| VRouterTarget | `provider.kubevirt` (with non-empty `name`) must be set when `type=kubevirt`; `provider.proxmox` must be set when `type=proxmox`; `provider.daemon` (with non-empty `address` and `agentID`) must be set when `type=vrouter-daemon`; for Proxmox, `clusterRef.name` is mandatory and `clusterRef.namespace`, if set, must equal the VRouterTarget's own namespace (cross-namespace references are rejected). On **delete**, rejected if any VRouterBinding's `targetRefs` or any VRouterConfig's `targetRef` in the same namespace still resolves to this VRouterTarget (error names one referencing object and, if more exist, how many others) — VRouterTarget and VRouterParams (below) are the only two CRDs in this table whose webhooks register the `delete` verb. This check fails closed: a `List` error on either kind blocks the deletion rather than letting it through, and a referencing object that is itself already terminating (non-nil `deletionTimestamp`) still counts as a blocker until its own finalizer is removed |
| VRouterConfig | `spec.targetRef` must resolve to an existing VRouterTarget; `spec.targetRef.namespace`, if set, must equal the VRouterConfig's own namespace (cross-namespace references are rejected). VRouterConfig does **not** carry its own `provider` block, so there is no provider cross-field validation here — only the targetRef checks above. |
| VRouterBinding | `spec.targetRefs` must not be empty; at least one of `spec.templateRef` (deprecated) or `spec.templateRefs` must be set; every entry in `spec.templateRef`, `spec.templateRefs[]`, `spec.paramsRefs[]`, and `spec.targetRefs[]` must resolve to an existing object, and each entry's `namespace`, if set, must equal the VRouterBinding's own namespace (cross-namespace references are rejected); `spec.paramsRefs` is optional — an empty or absent list is valid and simply skips this check |
| VRouterTemplate | `spec.config` and `spec.commands`, when set, must each parse as valid Go `text/template` syntax (parsed with the same sprig FuncMap used at render time — see §5.1); a syntax error is rejected at admission instead of surfacing later as a render failure |
| ProxmoxCluster | `spec.endpoints` must be non-empty, and no entry may be an empty string; `spec.syncInterval` must be greater than zero; `spec.credentialsRef.name` must be non-empty |
| VRouterParams | `ValidateCreate`/`ValidateUpdate` are no-ops — the schemaless `params` block has no cross-field constraints to check. On **delete**, rejected if any VRouterBinding's `spec.paramsRefs` in the same namespace still resolves to this VRouterParams (error names one referencing binding and, if more exist, how many others); unlike VRouterTarget, no other CRD references a VRouterParams, so only bindings are checked. This check fails closed the same way as VRouterTarget's delete check above (a `List` error blocks the deletion) |

For VRouterTarget, VRouterConfig, VRouterBinding, and ProxmoxCluster, the entire table of checks above (not just the reference-existence and cross-namespace checks) is skipped during `ValidateUpdate` once the object already has a non-nil `deletionTimestamp`. This matters for the finalizer-removal update that every controller's `onDelete` performs (see §7): without the skip, a spec that is now stale — a deleted reference, a pre-existing cross-namespace reference (see the upgrade caveat below), or even a plain presence check such as `kubevirt.name` or `credentialsRef.name` being empty — would make that final update fail admission and deadlock deletion.

Webhooks are disabled when `ENABLE_WEBHOOKS=false` (env var, checked in `cmd/main.go`).

#### Same-namespace-only references (standing policy)

`VRouterBinding.spec.templateRef` / `templateRefs` / `paramsRefs` / `targetRefs`, `VRouterConfig.spec.targetRef`, and `VRouterTarget.spec.provider.proxmox.clusterRef` may only reference objects in the same namespace as the referencing object. If a reference's `namespace` field is left empty it resolves to the referencing object's own namespace (via `ResolveNamespace`, unchanged); if it is set to any *other* namespace, the validating webhook rejects the create/update.

This is a **deliberate, standing design decision**, not a temporary limitation to be lifted later. Allowing an object in one namespace to reference credentials or targets in another namespace — with no authorization check beyond "the name resolves to something" — lets a tenant who can only create objects in their own namespace borrow another namespace's Proxmox API credentials or bind to another namespace's routers, and QGA `guest-exec` reaches the guest as root. Rejecting cross-namespace references at admission removes this confused-deputy path entirely, at the cost of not supporting shared credentials across namespaces.

If multiple namespaces need to target VMs on the same Proxmox cluster, co-locate a `ProxmoxCluster` (and the `Secret` it references via `credentialsRef`) in **each** namespace that needs it, rather than sharing one `ProxmoxCluster` across namespaces. This duplicates the endpoint/credential object but keeps every reference same-namespace and keeps the authorization boundary aligned with Kubernetes RBAC namespace boundaries.

> **Upgrade caveat**: enabling this webhook on an existing installation does not retroactively fix objects created before the webhook existed. A `VRouterBinding`, `VRouterConfig`, or `VRouterTarget` that already has a cross-namespace reference keeps working with its old (now inconsistent) behavior — the webhook only evaluates `ValidateCreate` and `ValidateUpdate`, so an existing object is not re-validated until the next time it is edited (or deleted and recreated). Operators upgrading to a version with this webhook enabled should audit existing objects for cross-namespace references (non-empty `namespace` fields in `templateRef`/`templateRefs`/`paramsRefs`/`targetRefs`/`targetRef`/`clusterRef` that differ from the referencing object's own namespace) before relying on the same-namespace guarantee.

---

## 7. Controllers

### 7.1 BindingController

| Item | Description |
|------|-------------|
| Watch | VRouterBinding, VRouterTarget, VRouterTemplate, VRouterParams |
| Output | VRouterConfig (ownerRef + labels → VRouterBinding) |
| Naming | `{binding-name}.{targetRef.name}` |
| Cleanup | Binding deleted → ownerRef cascade delete (K8s GC, background); targetRef removed → label-based orphan cleanup |

> **Note on VRouterTemplate/VRouterParams reconcilers**: both are pure data objects consumed only via BindingController's watches above — their own reconcilers (`VRouterTemplateReconciler`, `VRouterParamsReconciler`) are no-ops that exist solely to satisfy manager registration (`SetupWithManager`) and unconditionally return `ctrl.Result{}, nil`; all actual reconciliation of their content happens here in BindingController.

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
3. Lookup each `paramsRefs` entry, in list order → get VRouterParams (optional; an empty or absent list is valid and contributes no extra params layer)
4. Lookup each `targetRef` → get VRouterTarget
5. Merge params: `paramsRefs` (in list order) → `binding.params` → `target.params` (highest priority), via `MergeParamsLayers` (§4.1/§4.2)
6. Render and concatenate config/commands from templates in order
7. Create/update VRouterConfig with:
   - `ownerReference` → binding (for cascade delete via K8s GC)
   - `vrouter.kojuro.date/binding` label → binding name (for listing)
   - `vrouter.kojuro.date/target` label → target name (for listing)
   - `targetRef` set to the VRouterTarget name
8. **Orphan cleanup**: list existing VRouterConfigs by label `vrouter.kojuro.date/binding={name}`, diff against desired set, delete orphans
9. **Update status condition**: always update `Ready` condition before returning
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
| Apply | Pre-check → dispatch `ExecScript(config, commands, save)` (provider renders/writes/executes internally) → poll |
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
     → fail → if an exec is in flight (execPID > 0) and it has been running
       longer than the apply timeout (measured from execStartedTime, backfilled
       to "now" on first observation if a config left mid-apply by an older
       controller version has execPID > 0 but no recorded execStartedTime),
       the router being unreachable does not excuse the apply from its
       timeout: clear execPID, set phase=Failed, condition Applied=False
       (reason=ApplyTimeout), return nil (no retry) — the same outcome as
       step 4's own apply-timeout handling below, so a persistently failing
       CheckReady cannot keep the config in Applying forever just because
       step 4 is never reached. Otherwise: if phase was Applied, reset
       phase=Pending and set condition Applied=False (reason=RouterNotReady,
       message includes the CheckReady error) in the same status patch, since
       a previously-Applied result can no longer be trusted once the router
       stops answering ready checks (commonly a mid-reboot window). If phase
       was not Applied, status is left untouched (nothing to invalidate).
       Either way, requeue(10s); this is not treated as a reconcile error.

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
     pid = ExecScript(cfg.Spec.Config, cfg.Spec.Commands, save)
       — the provider renders the internal script template (§7.4) from these
       raw values and writes + executes it via QGA; the controller does not
       render or write the script itself
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
- A dispatched apply that never exits is bounded by a fixed apply timeout; once exceeded, the config is marked Failed instead of being polled forever. There is currently no CRD field to tune this timeout per config. The timeout bounds "GetExecStatus keeps succeeding but reports still running", "GetExecStatus keeps failing with a non-lost error" (a provider that cannot reliably tell a stale/unknown-pid error apart from a transient one, see KubeVirt/Proxmox GetExecStatus doc comments, would otherwise retry the error indefinitely without ever reaching Applied, Failed, or a fresh dispatch), and — as of the CheckReady-failure fix above — "CheckReady itself keeps failing", so a router that goes unreachable while an exec is in flight cannot pin the config in Applying forever just because step 4 (which normally evaluates this timeout) is never reached.
- If the previously dispatched exec's result can no longer be retrieved, the controller does not treat this as a normal error to retry indefinitely — it clears the exec handle and re-dispatches a fresh `ExecScript` call, since retrying the same (now-meaningless) handle can never succeed.
- `Applied` is set to `False` the moment a new generation (or a reboot-forced re-apply) is dispatched — not only on failure — so `kubectl wait --for=condition=Applied` cannot return early against a stale `True` left over from a previous generation while a new apply is in flight. The condition's `observedGeneration` reflects the generation the exec was actually dispatched for, not necessarily the object's current `.metadata.generation` (which may have moved on while the script was still running).
- `Applied` is also flipped to `False` (reason=`RouterNotReady`) the moment step 3's `CheckReady` fails on a config that was previously `Applied` — otherwise a stale `True` would let `kubectl wait --for=condition=Applied` report success for the entire window the router is unreachable, even though the controller no longer trusts that result. If the config was not previously `Applied` (already `Pending`/`Applying`/`Failed`), a `CheckReady` failure leaves status untouched — there is no stale `True` to clear.

**Condition (for `kubectl wait`):**

| Condition type | Status=True | Status=False |
|----------------|-------------|--------------|
| `Applied` | phase=Applied (for the generation the exec was dispatched for) | phase=Failed (including apply-timeout), an apply for a new generation/reboot has just been dispatched (phase=Applying), or CheckReady failed on a config that was previously Applied (phase resets to Pending, reason=RouterNotReady) |

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

The `internal/provider/qga` package (shared by the KubeVirt and Proxmox providers) maintains a hardcoded script template. The `config` and `commands` fields from VRouterConfig are passed straight through the provider's `ExecScript(config, commands, save)` and injected into this template to produce a complete vbash script, which is then written to the router and executed. This rendering happens inside the provider, not the controller — see §2.2.

```bash
#!/bin/vbash
if [ "$(id -g -n)" != 'vyattacfg' ] ; then
    exec sg vyattacfg -c "/bin/vbash $(readlink -f $0) $@"
fi
source /opt/vyatta/etc/functions/script-template
configure

# --- config section ---
{{- if .Config }}
load /dev/stdin <<'{{ .Delimiter }}'
{{ .Config }}
{{ .Delimiter }}
{{- else }}
load /opt/vyatta/etc/config.boot.default
{{- end }}

# --- commands section (optional) ---
{{- if .Commands }}
{{ .Commands }}
{{- end }}

commit
{{- if .Save }}
save
{{- end }}
```

- The leading `sg vyattacfg` re-exec guard ensures the script runs in the group required to use the `configure`/`commit` config-mode commands even when the QGA `guest-exec` session that launched it does not already carry that group membership.
- `config`: loaded via `load /dev/stdin` as a config.boot fragment, terminated by a heredoc delimiter (`.Delimiter`)
- **Heredoc delimiter randomization**: `.Delimiter` is not a fixed string. `config` is built from lower-privileged user `params`, so a fixed delimiter (e.g. a literal `VYOS_CONFIG_EOF`) would let a config line equal to that marker close the heredoc early and run the remainder of the file as arbitrary shell, as root, inside the router VM. Each render generates an unpredictable random token (`VROUTER_CONFIG_EOF_<hex>`, `crypto/rand`-backed) and checks it against every line of `config`, growing the random suffix and retrying (up to a bounded number of attempts) on the astronomically unlikely chance of a collision. Rendering fails outright if a collision-free delimiter cannot be produced within that bound, rather than falling back to an unsafe fixed marker.
- `commands`: a single pre-joined block of `set` commands (already newline-joined by BindingController from one or more templates, or supplied directly on a hand-authored VRouterConfig) executed in configure mode — not a list iterated by the script template itself
- `commit` is always appended; `save` is conditional (default: true), controlled by `spec.save`
- The script is written to a fixed path `/tmp/vrouter-apply.sh` (overwritten each time) then executed

### 7.5 QGA Script Execution Flow

KubeVirt and Proxmox both implement `ExecScript` as the same two-step pattern internally — render the script (§7.4), write it, then execute it — rather than exposing write and execute as separate interface calls; the caller only ever calls `ExecScript(config, commands, save)` followed by polling `GetExecStatus`.

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

### 7.6 VRouterTargetController

| Item | Description |
|------|-------------|
| Watch | VRouterTarget (primary); VirtualMachineInstance (KubeVirt only, mapped by name+namespace) |
| Output | Updates `VRouterTarget.status.vmRunning` |
| Requeue | `RequeueAfter(60s)` on every path (poll interval), regardless of outcome |
| Finalizer | None — this controller only reports status on an object other controllers reference; it does not own external resources to clean up |

**Reconcile flow (onChange only; there is no delete-time branch):**

```
1. Get VRouterTarget; NotFound → return (nothing to do)

2. Build provider via provider.New(ctx, target, client, restConfig) (§2.2)
   → error (e.g. referenced Secret/ProxmoxCluster not yet available) →
     log + requeue(60s); status.vmRunning is left at its last value

3. running, err := prov.IsVMRunning(ctx)
   → error → log + requeue(60s); status.vmRunning is left at its last value
   → success → each provider treats "VM not found" as a clean stopped
     result rather than an error: KubeVirt reports false when the
     VirtualMachineInstance is missing, and Proxmox reports false when the
     VMID is absent from the cluster (VM destroyed) or the node returns 404

4. if running != status.vmRunning:
     patch status.vmRunning = running (client.MergeFrom on a deep copy)
     — VRouterConfigController watches VRouterTarget via configsForTarget,
       so this status patch alone re-triggers reconciles of every
       VRouterConfig referencing this target; no explicit enqueue needed

5. return requeue(60s)
```

**VMI watch mapping** (`vmiToVRouterTargets`, KubeVirt only): on any VirtualMachineInstance event, lists all VRouterTargets and enqueues the ones whose `spec.provider.kubevirt.name` and resolved namespace (defaulting to the target's own namespace, the same rule the provider factory applies, §2.2) match the VMI's name and namespace. This makes a VM start/stop transition visible immediately instead of waiting up to 60s for the next poll. Proxmox targets have no equivalent watch and rely solely on the 60s interval here; `status.proxmoxNode` and reboot detection for Proxmox are instead driven by ProxmoxClusterController's own poll (§7.3).

**Error handling and staleness**: a persistent `IsVMRunning` error (as distinct from a clean "not found") leaves `status.vmRunning` at its last observed value with no signal that the value may now be stale — there is no `lastProbeTime`/`Unknown` state today. `status.vmRunning` is display-only (a printer column, §9.2); no controller gates behavior on it, so this is a documented gap rather than a live bug. See the "VRouterTarget Health Probe" item in `docs/TODO.md` for the proposed fix and why it has not been implemented yet.

**Relationship to the provider factory**: `provider.New` (§2.2) is called fresh on every reconcile rather than caching a provider instance across reconciles, so a change to `target.spec.provider` or the underlying Secret/ProxmoxCluster takes effect on the very next poll with no separate rebuild step.

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
  resources: ["vrouterconfigs", "vroutertemplates", "vrouterparams",
              "vroutertargets", "vrouterbindings", "proxmoxclusters"]
  verbs: ["get", "list", "watch", "create", "update", "patch", "delete"]
- apiGroups: ["vrouter.kojuro.date"]
  resources: ["vrouterconfigs/status", "vrouterbindings/status",
              "proxmoxclusters/status", "vroutertargets/status"]
  verbs: ["get", "update", "patch"]
- apiGroups: ["vrouter.kojuro.date"]
  resources: ["vrouterconfigs/finalizers", "vrouterbindings/finalizers",
              "proxmoxclusters/finalizers"]
  verbs: ["update"]
```

The `/finalizers` update rule is only needed for the three CRDs whose controllers add/remove a finalizer (VRouterConfig, VRouterBinding, ProxmoxCluster — see §7). VRouterTarget, VRouterTemplate, and VRouterParams have no finalizer logic (nothing owns cascade-deletable children through them), so they carry no `/finalizers` rule. VRouterParams also has no status subresource, so it carries no `/status` rule either.

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
    // VMRunning reflects whether the target VM was observed as running during
    // the most recent poll by VRouterTargetController (§7.6; polls every 60s
    // via the provider's IsVMRunning, for any provider type), and is exposed
    // as a printer column.
    // +optional
    VMRunning bool `json:"vmRunning,omitempty"`
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
    // ParamsRefs is an ordered list of VRouterParams to merge, same-namespace only.
    // Later entries override earlier ones; the merged result is applied below
    // (i.e. is overridden by) this binding's own Params.
    // +optional
    ParamsRefs []NameRef `json:"paramsRefs,omitempty"`
    TargetRefs []NameRef `json:"targetRefs"`
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

### 9.12 VRouterParams Types

```go
//+kubebuilder:object:root=true
//+kubebuilder:resource:shortName={vrtp,vrouterparams}
type VRouterParams struct {
    metav1.TypeMeta   `json:",inline"`
    metav1.ObjectMeta `json:"metadata,omitempty"`
    Spec VRouterParamsSpec `json:"spec,omitempty"`
}

type VRouterParamsSpec struct {
    // +kubebuilder:pruning:PreserveUnknownFields
    // +kubebuilder:validation:Schemaless
    // +optional
    Params apiextensionsv1.JSON `json:"params,omitempty"`
}
```

VRouterParams has no `Status` type and no `+kubebuilder:subresource:status` marker — it is a pure data object, consumed only by BindingController (§7.1) via `VRouterBindingSpec.ParamsRefs` (§9.3) and merged with `MergeParamsLayers` (§4.1/§4.2).

---

## 10. CRD Relationship Diagram

```
ProxmoxCluster ──clusterRef──→ VRouterTarget ←──targetRefs──  VRouterBinding ──templateRefs──→ VRouterTemplate(s)
(endpoints +                   (provider + params)                    (combine + common params)    (template logic)
 credentials)                        │                                │
      │                              │                          paramsRefs
      │                              │                                │
      │                              │                                ▼
      │                              │                          VRouterParams(s)
      │                              │                          (lowest-priority params
      │                              │                           layer, §4.1)
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
                                  dispatch ExecScript(config, commands, save)
                                  (provider renders script, writes + execs it)
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
