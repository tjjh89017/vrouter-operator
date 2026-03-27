# vRouter-Operator Design Spec

> CRD Architecture & Controller Design
>
> This document reflects the **implemented** state of the codebase.
> For planned features, see [TODO.md](TODO.md). For proposals, see [proposals/](proposals/).

---

## 1. Overview

vRouter-Operator is a Kubernetes Operator that manages VyOS virtual router configuration running on virtualization platforms. Through a provider abstraction layer, it supports multiple virtualization backends (KubeVirt, Proxmox VE, etc.). The default provider is KubeVirt (same cluster), which delivers configuration via qemu-guest-agent (QGA) over a virtio channel — no network reachability or sidecar container injection required.

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
    type: kubevirt          # kubevirt (default) | proxmox
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
    #     namespace: default           # optional, defaults to same namespace
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
- For KubeVirt, `kubevirt.name` identifies the VM; defaults to same cluster; optionally specify a remote kubeconfig via Secret
- For Proxmox, `proxmox.vmid` identifies the VM; `clusterRef` references a ProxmoxCluster resource
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

- `endpoints`: multiple endpoints supported for HA Proxmox clusters; each request tries them in order
- `credentialsRef`: references a Secret with keys `api-token-id` and `api-token-secret`
- `syncInterval`: controls poll frequency for node tracking (default: 60s)
- `checkGuestUptime`: when true, queries `/proc/uptime` via QGA to detect guest-initiated reboots that don't restart the QEMU process (default: true)
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

| Case | Behavior |
|------|----------|
| scalar override scalar | Override replaces |
| map override map | mergo recursive merge |
| slice override slice | Direct replacement (no append) |
| override sets nil | Keeps base value (mergo default does not override with nil) |
| override missing key | Keeps base value |

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

Performs cross-field validation that kubebuilder JSON schema markers cannot express:

| Resource | Validation |
|----------|------------|
| VRouterTarget | `provider.kubevirt` must be set when `type=kubevirt`; `provider.proxmox` must be set when `type=proxmox`; for Proxmox, `clusterRef.name` is mandatory |
| VRouterConfig | Same provider cross-field validation as VRouterTarget |
| VRouterBinding | `spec.targetRefs` must not be empty |

Webhooks are disabled when `ENABLE_WEBHOOKS=false` (env var, checked in `cmd/main.go`).

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
       → still running → requeue(3s)
       → exitCode == 0 → clear PID, set phase=Applied, condition Applied=True, return nil
       → exitCode != 0 → clear PID, set phase=Failed, condition Applied=False, return nil

  5. Check reboot: if target.status.lastRebootTime > status.lastAppliedTime → force re-apply

  6. if generation == observedGeneration and phase is Applied/Failed:
       return nil  ← already done for this generation

  7. (new spec or reboot detected)
     render internal script template
     WriteFile(script)
     pid = ExecScript()
     set execPID=pid, observedGeneration=generation, phase=Applying
     return requeue(3s)

onDelete():
  remove finalizer → unblock deletion
```

**Key semantics:**
- `generation == observedGeneration` → exec for this spec version has been dispatched (running or finished)
- `generation > observedGeneration` → new spec available, dispatch exec
- `target.status.lastRebootTime > status.lastAppliedTime` → VM rebooted, force re-apply regardless of generation
- Failed phase has no auto-retry; user updates spec (increments generation) to re-trigger

**Condition (for `kubectl wait`):**

| Condition type | Status=True | Status=False |
|----------------|-------------|--------------|
| `Applied` | phase=Applied | phase=Failed or Applying |

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
     b. Detect reboot from Proxmox uptime: if uptime ≤ 1.5 × syncInterval → set lastRebootTime = now
     c. If checkGuestUptime enabled:
          Async exec `cat /proc/uptime` via QGA, poll with 10s timeout
          If guest uptime ≤ 1.5 × syncInterval → set lastRebootTime = now
     d. Patch VRouterTarget.status

5. Set status.lastSyncTime = now, update Synced condition
   Return RequeueAfter(syncInterval)
```

**Purpose of `proxmoxNode` and `lastRebootTime`:**
- `proxmoxNode`: The provider factory reads this to build the Proxmox provider with the correct node name, since Proxmox QGA commands are node-scoped.
- `lastRebootTime`: VRouterConfig controller compares this against `lastAppliedTime` to force re-apply after a reboot.

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
    // +kubebuilder:default=true
    Save bool `json:"save,omitempty"`
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
    // +kubebuilder:default=true
    Save bool `json:"save,omitempty"`
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
    // +optional
    LastAppliedTime *metav1.Time `json:"lastAppliedTime,omitempty"`
    // +optional
    Message string `json:"message,omitempty"`
    // ObservedGeneration is the generation for which exec was last dispatched.
    // +optional
    ObservedGeneration int64 `json:"observedGeneration,omitempty"`
    // Conditions for kubectl wait support. The "Applied" condition is True
    // when phase=Applied and False when phase=Failed or Applying.
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

// +kubebuilder:validation:Enum=kubevirt;proxmox
type ProviderType string

const (
    ProviderKubeVirt ProviderType = "kubevirt"
    ProviderProxmox  ProviderType = "proxmox"
)

type ProviderConfig struct {
    // +kubebuilder:default=kubevirt
    // +optional
    Type     ProviderType    `json:"type,omitempty"`
    // +optional
    KubeVirt *KubeVirtConfig `json:"kubevirt,omitempty"`
    // +optional
    Proxmox  *ProxmoxConfig  `json:"proxmox,omitempty"`
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

### 9.8 ProxmoxCluster Types

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
    Endpoints []string `json:"endpoints"`
    // Reference to a Secret containing api-token-id and api-token-secret.
    CredentialsRef SecretReference `json:"credentialsRef"`
    // +kubebuilder:default=false
    // +optional
    InsecureSkipTLSVerify bool `json:"insecureSkipTLSVerify,omitempty"`
    // +kubebuilder:default="60s"
    // +optional
    SyncInterval metav1.Duration `json:"syncInterval,omitempty"`
    // +kubebuilder:default=true
    // +optional
    CheckGuestUptime bool `json:"checkGuestUptime,omitempty"`
}

type ProxmoxClusterStatus struct {
    // +optional
    LastSyncTime *metav1.Time `json:"lastSyncTime,omitempty"`
    // +optional
    Conditions []metav1.Condition `json:"conditions,omitempty"`
}
```

### 9.9 Constants

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

### 9.10 QGA Constants

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
