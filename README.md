# vRouter-Operator

A Kubernetes Operator that manages the virtual router configuration running on KubeVirt, Proxmox VE, or bare-metal via the vrouter-daemon gRPC agent. Configuration is delivered via QEMU Guest Agent (QGA) over a virtio channel or gRPC - no network reachability or sidecar injection required.

vRouter-Operator is a community project for managing virtual routers across Kubernetes/KubeVirt, Proxmox VE, and bare-metal deployments.

## Demo

YouTube: [https://www.youtube.com/watch?v=RsieH9gFU4I](https://www.youtube.com/watch?v=RsieH9gFU4I)

[![vRouter-Operator-Demo](http://img.youtube.com/vi/RsieH9gFU4I/0.jpg)](https://www.youtube.com/watch?v=RsieH9gFU4I "vRouter-Operator Demo")

---

## Talks

**DevConf.cz 2026** — *vRouter-Operator: Bringing GitOps and IaC to Virtual Network Functions in Kubernetes*

- Recording: [https://www.youtube.com/watch?v=yQsc4NWkfoI](https://www.youtube.com/watch?v=yQsc4NWkfoI)
- Slides: [https://speakerdeck.com/tjjh89017/devconf-dot-cz-2026-vrouter-operator-bringing-gitops-and-iac-to-virtual-network-functions-in-kubernetes](https://speakerdeck.com/tjjh89017/devconf-dot-cz-2026-vrouter-operator-bringing-gitops-and-iac-to-virtual-network-functions-in-kubernetes)

[![DevConf.cz 2026 talk](http://img.youtube.com/vi/yQsc4NWkfoI/0.jpg)](https://www.youtube.com/watch?v=yQsc4NWkfoI "DevConf.cz 2026 — vRouter-Operator")

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
                              → wait for the router's config service to be ready
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
- A router VM image with `qemu-guest-agent` installed (no `cloud-init` needed) — required for the `kubevirt` and `proxmox` providers, which deliver configuration over QGA. See [docs/SPEC.md §7.4-7.5](docs/SPEC.md) for exactly how config is applied inside the guest.

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
# Webhooks are controlled by the ENABLE_WEBHOOKS env var read in cmd/main.go,
# not a CLI flag, so set it via controllerManager.manager.env.
helm install vrouter-operator ./charts/vrouter-operator \
  --namespace vrouter-system --create-namespace \
  --set controllerManager.manager.env[0].name=ENABLE_WEBHOOKS \
  --set-string controllerManager.manager.env[0].value=false

# Pull from a private registry
helm install vrouter-operator ./charts/vrouter-operator \
  --namespace vrouter-system --create-namespace \
  --set imagePullSecrets[0].name=my-registry-secret
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

### Single YAML

```bash
make build-installer IMG=ghcr.io/tjjh89017/vrouter-operator:v0.1.0   # writes dist/install.yaml
kubectl apply -f dist/install.yaml
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
  name: router-kubevirt
  namespace: default
spec:
  provider:
    type: kubevirt
    kubevirt:
      name: router-kubevirt   # VirtualMachine name
      namespace: default
  params:
    hostname: "my-router"
```

**Proxmox VE** (requires a `ProxmoxCluster`, see below):

```yaml
apiVersion: vrouter.kojuro.date/v1
kind: VRouterTarget
metadata:
  name: router-proxmox
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
    hostname: "my-router"
```

**vrouter-daemon** (bare metal or any VM with daemon installed):

```yaml
apiVersion: vrouter.kojuro.date/v1
kind: VRouterTarget
metadata:
  name: router-daemon
  namespace: default
spec:
  provider:
    type: vrouter-daemon
    daemon:
      address: "vrouter-daemon.vrouter-system.svc:50052"
      agentID: "7dea4734a47e49b0952457b684587e7c"
  params:
    hostname: "my-router"
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
    - name: router-kubevirt
    - name: router-proxmox
    - name: router-daemon
```

The operator will automatically create a `VRouterConfig` for each target and apply it to the VM via QGA or gRPC.

### 5. Check status

```bash
kubectl get vrc                  # or: kubectl get vrouterconfig
kubectl get vrt                  # check vmRunning column
kubectl get vrb                  # or: kubectl get vrouterbinding
kubectl wait vrc/hostname-binding.router-kubevirt --for=condition=Applied
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
kubectl get vrt router-proxmox -o jsonpath='{.status.proxmoxNode}'
```

---

## vrouter-daemon Provider

The `vrouter-daemon` provider enables managing virtual routers on **bare metal or any VM** without KubeVirt or Proxmox VE. It uses the [vrouter-daemon](https://github.com/tjjh89017/vrouter-daemon) gRPC agent, which runs on the router host and connects back to a server deployed in Kubernetes.

### Architecture

```
vrouter-operator               vrouter-server (k8s)
┌──────────────┐               ┌──────────────────────────────┐
│  Controller  │   gRPC        │  ControlService (port 50052) │
│       │      │──────────────→│         │                    │
│  gRPC Client │               │    Redis (broker + registry) │
└──────────────┘               │         │                    │
                               │  AgentService (port 50051)   │
                               └─────────┬────────────────────┘
                                         │ gRPC bidir stream
                                   ┌───────┴───────┐
                                   │ router agents │  (bare metal)
                                   └───────────────┘
```

- **vrouter-server** — runs in Kubernetes; two services:
  - `ControlService` (ClusterIP `:50052`) — operator-facing: `IsConnected`, `ApplyConfig`
  - `AgentService` (NodePort `30051`) — agent-facing bidirectional stream
- **vrouter-agent** — runs on router host; connects to `AgentService` and executes config pushes locally
- **Redis** (HA via Sentinel) — brokers config payloads between server replicas and agents

### Deploy vrouter-server

```bash
kubectl apply -f deploy/kubernetes/namespace.yaml
kubectl apply -f deploy/kubernetes/redis-ha.yaml
kubectl apply -f deploy/kubernetes/vrouter-daemon.yaml
```

Creates in `vrouter-system` namespace: Redis HA (3+3 Sentinel), vrouter-daemon deployment (2 replicas), ClusterIP service for the operator, NodePort service for agents.

### Install vrouter-agent on the router host

Download the `.deb` from the [latest release](https://github.com/tjjh89017/vrouter-daemon/releases/latest):

```bash
dpkg -i vrouter-agent_<version>_amd64.deb
```

Edit `/etc/default/vrouter-agent`:

```bash
AGENT_ARGS="--server 172.30.0.40:30051"   # NodePort address
VRF_NAME=mgmt                              # optional: run inside a VRF
```

```bash
systemctl start vrouter-agent
systemctl status vrouter-agent
```

### Getting the agentID

By default `vrouter-agent` uses `/etc/machine-id` as its agent ID:

```bash
cat /etc/machine-id
# e.g. 7dea4734a47e49b0952457b684587e7c
```

You can override it with `--agent-id` or the `AGENT_ID` environment variable in `/etc/default/vrouter-agent`.

### VRouterTarget configuration

```yaml
apiVersion: vrouter.kojuro.date/v1
kind: VRouterTarget
metadata:
  name: router-daemon
  namespace: default
spec:
  provider:
    type: vrouter-daemon
    daemon:
      address: "vrouter-daemon.vrouter-system.svc:50052"  # ClusterIP service
      agentID: "7dea4734a47e49b0952457b684587e7c"         # /etc/machine-id on router host
      timeoutSeconds: 60                                   # optional, default 60
  params:
    hostname: "my-router"
```

### VM running detection

`IsVMRunning` calls `ControlService.IsConnected`. If the agent is not connected to the daemon (VM stopped, agent not running, or network issue), the VM is treated as stopped and config apply is skipped until the agent reconnects.

### Disconnect policy

The agent supports a `--disconnect-policy` flag that controls behavior when the server is unreachable:

| Policy | Behavior |
|--------|----------|
| `keep` (default) | Maintain current config |
| `rollback` | Re-apply init config after repeated failures |

### Comparison with other providers

| | KubeVirt | Proxmox VE | vrouter-daemon |
|---|---|---|---|
| Config delivery | QGA via SPDY exec | QGA via REST API | gRPC (daemon) |
| Script rendering | Operator-side | Operator-side | Daemon-side |
| VM running detect | VMI object + watch | Proxmox API | `IsConnected` RPC |
| Bare metal support | No | No | Yes |
| Extra infra needed | KubeVirt | Proxmox VE | vrouter-daemon + Redis |

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
kubectl get vrt router-kubevirt -o jsonpath='{.status.vmRunning}'
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
    name: router-kubevirt
  save: true
  commands: |
    set system host-name 'my-router'
```

---

## Params Merge

When a `VRouterBinding` has both `params` and its `VRouterTarget` has `params`, they are merged with **target params taking priority**:

```
final params = binding.params ← target.params
```

---

## Validation

The validating webhook enforces rules that the CRD schema alone can't express — see [docs/SPEC.md §6.2](docs/SPEC.md) for the full list. The most common ones you'll hit:

- Every `templateRef`/`templateRefs`/`targetRefs`/`clusterRef` must point to an object that exists **in the same namespace** as the object referencing it — cross-namespace references are rejected.
- A `VRouterTarget` cannot be deleted while any `VRouterBinding` or `VRouterConfig` in the same namespace still references it.
- `VRouterTemplate.spec.config`/`commands` must parse as valid `text/template` syntax at admission time, not just at render time.

Webhooks require cert-manager for TLS (see [Installation](#installation)) and can be disabled with the `ENABLE_WEBHOOKS=false` environment variable (dev/testing only — disables all of the above validation). For Helm installs, set it via `--set controllerManager.manager.env[0].name=ENABLE_WEBHOOKS --set-string controllerManager.manager.env[0].value=false` (see [Common values overrides](#common-values-overrides)); for local development, run `ENABLE_WEBHOOKS=false go run ./cmd/main.go`.

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

# Lint / format
make fmt
make vet
make lint

# After modifying api/v1/ types or kubebuilder markers
make generate   # regenerate zz_generated.deepcopy.go
make manifests  # regenerate CRDs and RBAC
```

See [docs/SPEC.md](docs/SPEC.md) for the full design (controller reconcile flows, provider interface, params merge semantics, template engine, and webhook validation rules).

> All commits require DCO sign-off: `git commit -s`
