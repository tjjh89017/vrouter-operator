# vRouter-Operator Design Spec

> CRD Architecture & Controller Design

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
   → render template
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

Each provider implements a common interface for router discovery and command execution:

```go
type Provider interface {
    // GetRouter returns router info for the given exact ref
    GetRouter(ctx context.Context, ref RouterRef) (*RouterInfo, error)
    // WriteFile writes content to a file on the target router via guest agent
    WriteFile(ctx context.Context, ref RouterRef, path string, content []byte) error
    // ExecScript executes a script file on the target router, returns PID for async tracking
    ExecScript(ctx context.Context, ref RouterRef, path string) (pid int64, err error)
    // GetExecStatus checks the execution status of a previously started script
    GetExecStatus(ctx context.Context, ref RouterRef, pid int64) (*ExecStatus, error)
}

type ExecStatus struct {
    Exited   bool
    ExitCode int
    Stdout   string
    Stderr   string
}
```

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

### 2.4 Proxmox VE Provider (Future)

Uses Proxmox REST API to execute QGA commands on remote Proxmox nodes. Requires API credentials stored in a Kubernetes Secret.

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
    #   endpoint: "https://pve.example.com:8006"
    #   credentialsRef:
    #     name: proxmox-credentials    # Secret containing api-token-id + api-token-secret
    #   node: "pve-node-1"             # optional, limit to specific node
    #   insecureSkipTLSVerify: false
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
- For Proxmox, `proxmox.vmid` identifies the VM; `endpoint` and `credentialsRef` are required
- `params` uses `x-kubernetes-preserve-unknown-fields: true`; stored as `apiextensionsv1.JSON` in Go

---

### 3.3 VRouterBinding

Combines a template with multiple targets, providing common params (lowest priority).

```yaml
apiVersion: vrouter.kojuro.date/v1
kind: VRouterBinding
metadata:
  name: bgp-binding
spec:
  templateRef:
    name: bgp-router
  save: true              # persist config after commit, default: true
  params:                 # common, lowest priority
    ntpServer: "10.0.0.1"
    dnsServer: "8.8.8.8"
  targetRefs:
    - name: site-a-routers
    - name: site-b-routers
```

---

### 3.4 VRouterConfig (Generated / Direct)

The final artifact. Can be auto-generated by BindingController (ownerRef + labels → Binding) or created directly by the user (inline config).

```yaml
apiVersion: vrouter.kojuro.date/v1
kind: VRouterConfig
metadata:
  name: bgp-binding.vyos-router-a   # {binding-name}-{router-name}
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
  provider:                         # inherited from VRouterTarget
    type: kubevirt
    kubevirt:
      name: vyos-router-a
      namespace: production
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
  message: ""
```

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
| VRouterTarget | `provider.kubevirt` must be set when `type=kubevirt`; `provider.proxmox` must be set when `type=proxmox` |
| VRouterConfig | Same provider cross-field validation as VRouterTarget |
| VRouterBinding | `spec.targetRefs` must not be empty |

---

## 7. Controllers

### 7.1 BindingController

| Item | Description |
|------|-------------|
| Watch | VRouterBinding, VRouterTarget, VRouterTemplate |
| Output | VRouterConfig (ownerRef + labels → VRouterBinding) |
| Naming | `{binding-name}.{router-name}` |
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

1. Lookup `templateRef` → get VRouterTemplate
2. Lookup each `targetRef` → get VRouterTarget
3. Read `target.provider` — identify router via provider-specific config (KubeVirt: name/namespace, Proxmox: vmid)
4. Merge params: `binding.params` → `target.params`
5. Render template with merged params
6. Create/update VRouterConfig with:
   - `ownerReference` → binding (for cascade delete via K8s GC)
   - `vrouter.kojuro.date/binding` label → binding name (for listing)
   - `vrouter.kojuro.date/target` label → target name (for listing)
   - provider info carried over from VRouterTarget
7. **Orphan cleanup**: list existing VRouterConfigs by label `vrouter.kojuro.date/binding={name}`, diff against desired set, delete orphans

> **Note on ownerReference**: Uses `controllerutil.SetControllerReference()` with `blockOwnerDeletion: true`. This ensures the binding stays in "Deleting" state (foreground delete) until all dependent VRouterConfigs are cleaned up, making it easier to debug deletion order and verify cleanup completion.

**reconcileDelete (onDelete)**:

VRouterConfigs are owned by the binding via ownerRef, so K8s GC handles cascade deletion automatically. The controller simply removes the finalizer to unblock deletion:

```go
controllerutil.RemoveFinalizer(binding, FinalizerName)
return ctrl.Result{}, r.Update(ctx, binding)
```

```go
// Orphan cleanup in BindingController.Reconcile()
desired := map[string]bool{}
for _, targetRef := range binding.Spec.TargetRefs {
    target := getTarget(targetRef.Name)
    configName := fmt.Sprintf("%s.%s", binding.Name, routerName(target))
    desired[configName] = true
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
| Apply | Render internal script template → write file → execute |
| Status | Pending → Applying → Applied / Failed |

Reconcile flow (state machine):

```
Pending ──→ Applying ──→ Applied
               │
               └──→ Failed
```

1. **phase=Pending**: Read `spec.provider.type`, instantiate provider, render internal script template, write script via `Provider.WriteFile()`, execute via `Provider.ExecScript()`, save PID to `status.execPID`, set phase → `Applying`, return `RequeueAfter(3s)`
2. **phase=Applying**: Check exec status via `Provider.GetExecStatus(status.execPID)`
   - If still running → return `RequeueAfter(3s)`
   - If exited with success → set phase → `Applied`, clear `execPID`, set `lastAppliedTime`
   - If exited with error → set phase → `Failed`, record error in `message`
3. **phase=Applied**: Compare `spec` hash with last applied hash — if changed, reset to `Pending`
4. **phase=Failed**: No auto-retry; user must update spec or annotate to trigger re-apply

### 7.3 Internal Script Template

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

### 7.4 QGA Script Execution Flow

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

**Proxmox VE** (REST API, future):

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
              "vroutertargets", "vrouterbindings"]
  verbs: ["get", "list", "watch", "create", "update", "patch", "delete"]
- apiGroups: ["vrouter.kojuro.date"]
  resources: ["vrouterconfigs/status", "vrouterbindings/status"]
  verbs: ["update", "patch"]
```

---

## 9. Go Types (kubebuilder)

CRD schemas are generated from Go structs via kubebuilder markers. Run `make manifests` to produce CRD YAML.

### 9.1 VRouterTemplate

```go
//+kubebuilder:object:root=true
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
type VRouterTarget struct {
    metav1.TypeMeta   `json:",inline"`
    metav1.ObjectMeta `json:"metadata,omitempty"`
    Spec VRouterTargetSpec `json:"spec,omitempty"`
}

type VRouterTargetSpec struct {
    Provider ProviderConfig `json:"provider"`
    // +kubebuilder:pruning:PreserveUnknownFields
    // +kubebuilder:validation:Schemaless
    // +optional
    Params apiextensionsv1.JSON `json:"params,omitempty"`
}
```

### 9.3 VRouterBinding

```go
//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
type VRouterBinding struct {
    metav1.TypeMeta   `json:",inline"`
    metav1.ObjectMeta `json:"metadata,omitempty"`
    Spec   VRouterBindingSpec   `json:"spec,omitempty"`
    Status VRouterBindingStatus `json:"status,omitempty"`
}

type VRouterBindingSpec struct {
    TemplateRef NameRef `json:"templateRef"`
    // +kubebuilder:default=true
    Save bool `json:"save,omitempty"`
    // +kubebuilder:pruning:PreserveUnknownFields
    // +kubebuilder:validation:Schemaless
    // +optional
    Params     apiextensionsv1.JSON `json:"params,omitempty"`
    TargetRefs []NameRef            `json:"targetRefs"`
}

type VRouterBindingStatus struct {
    // +optional
    Conditions []metav1.Condition `json:"conditions,omitempty"`
}

type NameRef struct {
    Name string `json:"name"`
}
```

### 9.4 VRouterConfig

```go
//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
//+kubebuilder:printcolumn:name="Provider",type=string,JSONPath=`.spec.provider.type`
//+kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type VRouterConfig struct {
    metav1.TypeMeta   `json:",inline"`
    metav1.ObjectMeta `json:"metadata,omitempty"`
    Spec   VRouterConfigSpec   `json:"spec,omitempty"`
    Status VRouterConfigStatus `json:"status,omitempty"`
}

type VRouterConfigSpec struct {
    Provider ProviderConfig `json:"provider"`
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
}
```

### 9.5 Shared Types

```go
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

type NameRef struct {
    Name string `json:"name"`
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
type ProxmoxConfig struct {
    VMID           int             `json:"vmid"`
    Endpoint       string          `json:"endpoint"`
    CredentialsRef SecretReference `json:"credentialsRef"`
    // +optional
    Node string `json:"node,omitempty"`
    // +kubebuilder:default=false
    // +optional
    InsecureSkipTLSVerify bool `json:"insecureSkipTLSVerify,omitempty"`
}
```

---

## 10. CRD Relationship Diagram

```
VRouterTemplate  ←templateRef──  VRouterBinding  ──targetRefs──→  VRouterTarget
  (template logic)      (combine + common params)                       (provider + params)
                               │
                        BindingController
                        render per router
                               │ ownerRef
                               ▼
                          VRouterConfig (per router)
                          ├── spec.provider (from Target)
                          ├── spec.config   (rendered config.boot)
                          └── spec.commands (rendered set commands)
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
  provider:
    type: kubevirt            # optional, defaults to kubevirt
    kubevirt:
      name: vyos-router-a
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
