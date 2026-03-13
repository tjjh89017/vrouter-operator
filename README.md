# vrouter-operator

A Kubernetes Operator that manages VyOS virtual router configuration running on KubeVirt or Proxmox VE. Configuration is delivered via QEMU Guest Agent (QGA) over a virtio channel — no network reachability or sidecar injection required.

## Demo

YouTube: [https://www.youtube.com/watch?v=pvdPgob3jAE](https://www.youtube.com/watch?v=pvdPgob3jAE)

[![vRouter-Operator-Demo](http://img.youtube.com/vi/pvdPgob3jAE/0.jpg)](https://www.youtube.com/watch?v=pvdPgob3jAE "vRouter-Operator Demo")

Full guide: https://hackmd.io/@TJibejKyT3OH1YR5f1duzg/ryMdLbvigx

---

## How It Works

```
ProxmoxCluster ──clusterRef──→ VRouterTarget ←──── VRouterBinding ──── VRouterTemplate
(endpoints +                   (provider +          (bind template       (config/commands
 credentials)                   params)              to targets)          template text)
      │                              │
ProxmoxCluster                BindingController
Controller                    → merge params (binding → target)
(polls node,                  → render template
 reboot detect)               → create VRouterConfig per target (targetRef → VRouterTarget)
      │                              │
      └─ status.proxmoxNode   VRouterController
         status.lastRebootTime → resolve VRouterTarget (provider info)
                              → wait for VM running
                              → wait for vyos-router.service active
                              → write rendered script via QGA
                              → execute script via QGA
                              → update status (phase, conditions)
```

### CRDs

| CRD | Short name | Purpose |
|-----|-----------|---------|
| `VRouterTemplate` | `vrtt` | Go `text/template` config/command template |
| `VRouterTarget` | `vrt` | Target VM + provider config + default params |
| `VRouterBinding` | `vrb` | Binds a template to one or more targets |
| `VRouterConfig` | `vrc` | Final rendered config applied to one router (auto-generated or direct) |
| `ProxmoxCluster` | `pxc` | Centralised Proxmox endpoint + credentials (Proxmox provider only) |

---

## Prerequisites

- Kubernetes cluster
  - KubeVirt installed for the `kubevirt` provider (tested on Harvester)
  - Proxmox VE cluster accessible for the `proxmox` provider
- cert-manager (for webhook TLS)
- VyOS VM image with `qemu-guest-agent` installed (no `cloud-init` needed)

### Build VyOS image

```toml
# VyOS image build flavor
image_format = "qcow2"
image_opts = "-c"
disk_size = 4
packages = ["qemu-guest-agent"]

[boot_settings]
    console_type = "tty"
    console_num = '0'
```

---

## Installation

### Prerequisites

- Kubernetes 1.24+
- [cert-manager](https://cert-manager.io/docs/installation/) installed (required for webhook TLS)
  ```bash
  kubectl apply -f https://github.com/cert-manager/cert-manager/releases/latest/download/cert-manager.yaml
  kubectl wait --for=condition=Available deployment --all -n cert-manager --timeout=120s
  ```

### Helm (recommended)

```bash
helm install vrouter-operator oci://ghcr.io/tjjh89017/charts/vrouter-operator \
  --namespace vrouter-system --create-namespace
```

Or install from the local chart:

```bash
helm install vrouter-operator ./charts/vrouter-operator \
  --namespace vrouter-system --create-namespace
```

#### Common values overrides

```bash
# Pin to a specific image tag
helm install vrouter-operator ./charts/vrouter-operator \
  --namespace vrouter-system --create-namespace \
  --set controllerManager.manager.image.tag=v0.1.0

# Disable webhooks (dev/testing only — no validation)
helm install vrouter-operator ./charts/vrouter-operator \
  --namespace vrouter-system --create-namespace \
  --set controllerManager.manager.args="{--metrics-bind-address=:8443,--leader-elect,--health-probe-bind-address=:8081,--enable-webhooks=false}"
```

#### Upgrade

```bash
helm upgrade vrouter-operator ./charts/vrouter-operator \
  --namespace vrouter-system
```

#### Uninstall

```bash
helm uninstall vrouter-operator --namespace vrouter-system
```

### Kustomize / raw manifests

```bash
make install          # install CRDs into current cluster
make deploy IMG=<your-image>
```

---

## Quick Start

### 1. Install the operator

```bash
helm install vrouter-operator ./charts/vrouter-operator \
  --namespace vrouter-system --create-namespace
```

### 2. Create a VRouterTemplate

Define config/commands using Go `text/template` syntax with [sprig](https://masterminds.github.io/sprig/) functions:

```yaml
apiVersion: vrouter.kojuro.date/v1
kind: VRouterTemplate
metadata:
  name: hostname-template
  namespace: default
spec:
  commands: |
    set system host-name '{{ .hostname }}'
```

### 3. Create a VRouterTarget

**KubeVirt:**

```yaml
apiVersion: vrouter.kojuro.date/v1
kind: VRouterTarget
metadata:
  name: vyos-kubevirt
  namespace: default
spec:
  provider:
    type: kubevirt
    kubevirt:
      name: vyos-kubevirt   # VirtualMachine name
      namespace: default
  params:
    hostname: "my-vyos-router"
```

**Proxmox VE** (requires a `ProxmoxCluster`, see below):

```yaml
apiVersion: vrouter.kojuro.date/v1
kind: VRouterTarget
metadata:
  name: vyos-proxmox
  namespace: default
spec:
  provider:
    type: proxmox
    proxmox:
      vmid: 121
      clusterRef:
        name: pve-cluster
        namespace: default
  params:
    hostname: "my-vyos-router"
```

### 4. Create a VRouterBinding

Bind the template to one or more targets:

```yaml
apiVersion: vrouter.kojuro.date/v1
kind: VRouterBinding
metadata:
  name: hostname-binding
  namespace: default
spec:
  templateRef:
    name: hostname-template
  save: true          # persist config after commit (default: true)
  targetRefs:
    - name: vyos-kubevirt
```

The operator will automatically create a `VRouterConfig` for each target and apply it to the VM via QGA.

### 5. Check status

```bash
kubectl get vrc                  # or: kubectl get vrouterconfig
kubectl get vrb                  # or: kubectl get vrouterbinding
kubectl wait vrc/hostname-binding.vyos-kubevirt --for=condition=Applied
```

---

## Proxmox Provider

The Proxmox provider uses the Proxmox REST API to communicate with VMs via QEMU Guest Agent.

### ProxmoxCluster

`ProxmoxCluster` centralises endpoint and credential configuration for a Proxmox VE cluster. All `VRouterTarget` resources on the same cluster share one `ProxmoxCluster` instead of repeating credentials per target.

The controller polls `/cluster/resources` on `syncInterval` and writes the resolved node name into `VRouterTarget.status.proxmoxNode`, eliminating per-operation node-lookup API calls.

```yaml
apiVersion: vrouter.kojuro.date/v1
kind: ProxmoxCluster
metadata:
  name: pve-cluster
  namespace: default
spec:
  endpoints:
    - "https://192.168.1.10:8006"
  credentialsRef:
    name: proxmox-credentials   # Secret with api-token-id and api-token-secret
  insecureSkipTLSVerify: false
  syncInterval: "60s"
  checkGuestUptime: false       # set true to detect guest-initiated reboots via QGA
```

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: proxmox-credentials
  namespace: default
stringData:
  api-token-id: "user@pam!token-name"
  api-token-secret: "<token-secret>"
```

### Reboot detection

| Scenario | Detection |
|----------|-----------|
| Proxmox stop → start (hard restart) | Proxmox uptime resets → detected automatically |
| Guest `reboot` command (soft reboot) | Requires `checkGuestUptime: true`; reads `/proc/uptime` via QGA |

When a reboot is detected, `VRouterTarget.status.lastRebootTime` is updated and the `VRouterConfig` controller forces a re-apply on the next reconcile.

### Check sync status

```bash
kubectl wait --for=condition=Synced proxmoxcluster/pve-cluster --timeout=120s
kubectl get vrt vyos-proxmox -o jsonpath='{.status.proxmoxNode}'
```

---

## Direct VRouterConfig (without Binding)

For simple cases or direct management, create a `VRouterConfig` directly:

```yaml
apiVersion: vrouter.kojuro.date/v1
kind: VRouterConfig
metadata:
  name: my-router-config
  namespace: default
spec:
  targetRef:
    name: vyos-kubevirt
  save: true
  commands: |
    set system host-name 'my-vyos-router'
```

---

## Params Merge

When a `VRouterBinding` has both `params` and its `VRouterTarget` has `params`, they are merged with **target params taking priority**:

```
final params = binding.params ← target.params
```

---

## VRouterConfig Status

| Phase | Meaning |
|-------|---------|
| `Pending` | Waiting for VM to be ready |
| `Applying` | Script dispatched, waiting for completion |
| `Applied` | Config applied successfully |
| `Failed` | Script exited with non-zero code (no auto-retry; edit spec to retry) |

The `Applied` condition is set for `kubectl wait` support:

```bash
kubectl wait vrc/<name> --for=condition=Applied --timeout=120s
```

---

## Provider Support

| Provider | Status | Notes |
|----------|--------|-------|
| KubeVirt | ✅ Supported | Same-cluster KubeVirt; SPDY exec into virt-launcher pod |
| Proxmox VE | ✅ Supported | REST API + QGA; credentials via ProxmoxCluster |

---

## Development

```bash
# Build
make build

# Run locally (webhooks disabled)
ENABLE_WEBHOOKS=false go run ./cmd/main.go

# Run tests
make test

# After modifying api/v1/ types
make generate   # regenerate zz_generated.deepcopy.go
make manifests  # regenerate CRDs and RBAC
```

> All commits require DCO sign-off: `git commit -s`
