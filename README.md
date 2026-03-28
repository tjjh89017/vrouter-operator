# vRouter-Operator

A Kubernetes Operator that manages VyOS virtual router configuration running on KubeVirt, Proxmox VE, or bare-metal via the vrouter-daemon gRPC agent. Configuration is delivered via QEMU Guest Agent (QGA) over a virtio channel or gRPC — no network reachability or sidecar injection required.

## Demo

YouTube: [https://www.youtube.com/watch?v=RsieH9gFU4I](https://www.youtube.com/watch?v=RsieH9gFU4I)

[![vRouter-Operator-Demo](http://img.youtube.com/vi/RsieH9gFU4I/0.jpg)](https://www.youtube.com/watch?v=RsieH9gFU4I "vRouter-Operator Demo")

---

## How It Works

```
ProxmoxCluster ──clusterRef──→ VRouterTarget ←──── VRouterBinding ──── VRouterTemplate
(endpoints +                   (provider +          (bind template       (config/commands
 credentials)                   params)              to targets)          template text)
      │                              │
ProxmoxCluster                VRouterTarget
Controller                    Controller
(polls node,                  (polls IsVMRunning every 60s,
 reboot detect,                updates status.vmRunning,
 unreachable →                 VMI watch for KubeVirt instant detect)
 vmRunning=false)                    │
      │                        status.vmRunning change
      └─ status.proxmoxNode    → triggers VRouterConfig reconcile
         status.lastRebootTime
                                     │
                              VRouterConfig Controller
                              → resolve VRouterTarget (provider info)
                              → check VM running (skip if stopped)
                              → wait for vyos-router.service active
                              → deliver config via QGA or gRPC
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
  - [vrouter-daemon](https://github.com/tjjh89017/vrouter-daemon) deployed for the `vrouter-daemon` provider
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
make deploy           # deploys ghcr.io/tjjh89017/vrouter-operator:latest by default
# or pin a version:
make deploy IMG=ghcr.io/tjjh89017/vrouter-operator:v0.1.0
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

**vrouter-daemon** (bare metal or any VM with daemon installed):

```yaml
apiVersion: vrouter.kojuro.date/v1
kind: VRouterTarget
metadata:
  name: vyos-daemon
  namespace: default
spec:
  provider:
    type: vrouter-daemon
    daemon:
      address: "vrouter-daemon.vrouter-system.svc:50052"
      agentID: "7dea4734a47e49b0952457b684587e7c"
  params:
    hostname: "my-vyos-router"
```

### 4. Create a VRouterBinding

Bind one or more templates to one or more targets:

```yaml
apiVersion: vrouter.kojuro.date/v1
kind: VRouterBinding
metadata:
  name: hostname-binding
  namespace: default
spec:
  templateRefs:                    # ordered list; config/commands concatenated in order
    - name: hostname-template
  save: true          # persist config after commit (default: true)
  targetRefs:
    - name: vyos-kubevirt
    - name: vyos-proxmox
    - name: vyos-daemon
```

The operator will automatically create a `VRouterConfig` for each target and apply it to the VM via QGA or gRPC.

### 5. Check status

```bash
kubectl get vrc                  # or: kubectl get vrouterconfig
kubectl get vrt                  # check vmRunning column
kubectl get vrb                  # or: kubectl get vrouterbinding
kubectl wait vrc/hostname-binding.vyos-kubevirt --for=condition=Applied
```

---

## Proxmox Provider

The Proxmox provider uses the Proxmox REST API to communicate with VMs via QEMU Guest Agent.

### ProxmoxCluster

`ProxmoxCluster` centralises endpoint and credential configuration for a Proxmox VE cluster. All `VRouterTarget` resources on the same cluster share one `ProxmoxCluster` instead of repeating credentials per target.

The controller polls `/cluster/resources` on `syncInterval` and writes the resolved node name into `VRouterTarget.status.proxmoxNode`, eliminating per-operation node-lookup API calls.

If the Proxmox API is unreachable (e.g. network issue), the controller treats all associated VMs as stopped (`status.vmRunning=false`) and retries after `syncInterval`.

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
  checkGuestUptime: true        # detect guest-initiated reboots via QGA (default: true)
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
| Guest `reboot` command (soft reboot) | Detected via `checkGuestUptime: true` (default); reads `/proc/uptime` via QGA |

When a reboot is detected, `VRouterTarget.status.lastRebootTime` is updated and the `VRouterConfig` controller forces a re-apply on the next reconcile.

### Check sync status

```bash
kubectl wait --for=condition=Synced proxmoxcluster/pve-cluster --timeout=120s
kubectl get vrt vyos-proxmox -o jsonpath='{.status.proxmoxNode}'
```

---

## vrouter-daemon Provider

The `vrouter-daemon` provider connects to the [vrouter-daemon](https://github.com/tjjh89017/vrouter-daemon) gRPC ControlService. The daemon runs as a standalone process (inside or alongside the router VM) and bridges gRPC calls to local VyOS configuration.

Each router registers itself with the daemon using a unique `agentID`. The operator sends raw config and commands to the daemon, which renders and executes the vbash script internally.

### VRouterTarget

```yaml
spec:
  provider:
    type: vrouter-daemon
    daemon:
      address: "vrouter-daemon.vrouter-system.svc:50052"  # gRPC endpoint
      agentID: "7dea4734a47e49b0952457b684587e7c"         # unique agent identifier
      timeoutSeconds: 60                                   # optional, default 60
```

### VM running detection

`IsVMRunning` calls `ControlService.IsConnected` — if the agent is not connected to the daemon, the VM is treated as stopped and config apply is skipped until the agent reconnects.

---

## VRouterTarget Status

The `VRouterTarget` controller polls `IsVMRunning` every 60 seconds and updates `status.vmRunning`. For KubeVirt, VMI watch events trigger an immediate check without waiting for the next poll.

| Field | Description |
|-------|-------------|
| `status.vmRunning` | Whether the VM was observed as running in the last poll |
| `status.proxmoxNode` | Proxmox node hosting the VM (Proxmox provider only) |
| `status.lastRebootTime` | Timestamp of last detected reboot (Proxmox provider only) |

When `vmRunning` changes from `false` to `true`, all referencing `VRouterConfig` resources are automatically re-enqueued for reconcile.

```bash
kubectl get vrt                  # shows VM Running column
kubectl get vrt vyos-kubevirt -o jsonpath='{.status.vmRunning}'
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
| vrouter-daemon (gRPC) | ✅ Supported | Standalone daemon; bare metal or any VM |

---

## Documentation

| Document | Description |
|----------|-------------|
| [docs/SPEC.md](docs/SPEC.md) | Authoritative design spec (implemented state) |
| [docs/TODO.md](docs/TODO.md) | Backlog — planned features, not yet implemented |
| [docs/proposals/](docs/proposals/) | Design proposals for future features |

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
