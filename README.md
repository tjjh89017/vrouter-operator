# vrouter-operator

A Kubernetes Operator that manages VyOS virtual router configuration running on KubeVirt (and other virtualization backends). Configuration is delivered via QEMU Guest Agent (QGA) over a virtio channel — no network reachability or sidecar injection required.

## Demo

YouTube: [https://www.youtube.com/watch?v=pvdPgob3jAE](https://www.youtube.com/watch?v=pvdPgob3jAE)

[![vRouter-Operator-Demo](http://img.youtube.com/vi/pvdPgob3jAE/0.jpg)](https://www.youtube.com/watch?v=pvdPgob3jAE "vRouter-Operator Demo")

Full guide: https://hackmd.io/@TJibejKyT3OH1YR5f1duzg/ryMdLbvigx

---

## How It Works

```
VRouterTemplate  +  VRouterTarget  +  VRouterBinding
                          │
                  BindingController
                  → merge params (binding → target)
                  → render template
                  → create VRouterConfig per target
                          │
                  VRouterController
                  → wait for VM running (VMI watch)
                  → wait for vyos-router.service active (exited)
                  → write rendered script via QGA
                  → execute script via QGA
                  → update status
```

| CRD | Short name | Purpose |
|-----|-----------|---------|
| `VRouterTemplate` | `vrtt` | Go `text/template` config/command template |
| `VRouterTarget` | `vrt` | Target VM + provider config + default params |
| `VRouterBinding` | `vrb` | Binds a template to one or more targets |
| `VRouterConfig` | `vrc` | Final rendered config applied to one router (auto-generated or direct) |

---

## Prerequisites

- Kubernetes cluster with KubeVirt installed (tested on Harvester)
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

## Quick Start

### 1. Install CRDs and deploy the operator

```bash
make install   # install CRDs into current cluster
make deploy IMG=<your-image>
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

Point to the KubeVirt VM and provide default params:

```yaml
apiVersion: vrouter.kojuro.date/v1
kind: VRouterTarget
metadata:
  name: vyos-with-operator
  namespace: default
spec:
  provider:
    type: kubevirt
    kubevirt:
      name: vyos-with-operator   # VirtualMachine name
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
    - name: vyos-with-operator
```

The operator will automatically create a `VRouterConfig` for each target and apply it to the VM via QGA.

### 5. Check status

```bash
kubectl get vrc                  # or: kubectl get vrouterconfig
kubectl get vrb                  # or: kubectl get vrouterbinding
kubectl wait vrc/hostname-binding.vyos-with-operator --for=condition=Applied
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
  provider:
    type: kubevirt
    kubevirt:
      name: vyos-with-operator
      namespace: default
  save: true
  config: |
    system {
        host-name my-vyos-router
    }
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
| Proxmox VE | 🚧 Planned | REST API + QGA; credentials via Secret |

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
