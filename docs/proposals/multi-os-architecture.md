# Multi-OS Router Support Architecture

**Status**: Proposal (not yet implemented)
**Date**: 2026-03-27

## Goal

Support multiple router OS types (VyOS, OpenWrt, Cisco, etc.) within a single operator, sharing infrastructure while isolating OS-specific logic in separate CRDs and controllers.

## Motivation

vrouter-operator is currently VyOS-specific. The gRPC agent design (see `grpc-agent-architecture.md`) already decouples transport from config format, making multi-OS support a natural extension. Rather than creating a separate operator per OS (duplicating reconcile logic, deployment infra, RBAC, webhooks), a single operator with per-OS CRD sets is more maintainable.

## Architecture

### Per-OS CRD Sets, Shared Infrastructure

Each OS gets its own Template, Config, and Binding CRD types. Target and provider infrastructure are shared across all OS types.

```
vrouter-operator/
├── api/v1/
│   ├── vroutertemplate_types.go      # VyOS template
│   ├── vrouterconfig_types.go        # VyOS config
│   ├── vrouterbinding_types.go       # VyOS binding
│   ├── owrttemplate_types.go         # OpenWrt template (future)
│   ├── owrtconfig_types.go           # OpenWrt config (future)
│   ├── owrtbinding_types.go          # OpenWrt binding (future)
│   ├── vroutertarget_types.go        # Shared — describes VM, not OS-specific
│   ├── shared_types.go               # ProviderConfig, NameRef, etc.
│   └── constants.go
├── internal/
│   ├── controller/
│   │   ├── vrouterconfig_controller.go   # wraps config in vbash
│   │   ├── vrouterbinding_controller.go
│   │   ├── owrtconfig_controller.go      # wraps config in UCI batch (future)
│   │   ├── owrtbinding_controller.go     # (future)
│   │   └── ...
│   ├── agentpool/                         # Shared gRPC infra
│   └── provider/                          # Shared provider abstraction
```

### What is Shared vs OS-Specific

| Component | Shared or Per-OS | Reason |
|-----------|-----------------|--------|
| VRouterTarget | **Shared** | Describes the VM, not the OS |
| Provider interface | **Shared** | VM-level operations (WriteFile, ExecScript, gRPC) |
| AgentPool / gRPC | **Shared** | Transport is OS-agnostic |
| Template CRD | **Per-OS** | Config format differs (VyOS `set` vs OpenWrt UCI) |
| Config CRD | **Per-OS** | Controller wraps config differently per OS |
| Binding CRD | **Per-OS** | Binds OS-specific template to targets |
| Config Controller | **Per-OS** | vbash vs UCI batch vs IOS commands |
| Webhooks | **Per-OS** | Cross-resource validation against OS type |

### OS Type on VRouterTarget

VRouterTarget gains an `os` field to declare what OS the target VM runs:

```go
type VRouterTargetSpec struct {
    // OS type of the router.
    // +kubebuilder:validation:Enum=vyos;openwrt
    // +kubebuilder:default=vyos
    OS string `json:"os"`

    Provider ProviderConfig `json:"provider"`
    Params   map[string]string `json:"params,omitempty"`
}
```

This enables webhook cross-resource validation:

| Scenario | Result |
|----------|--------|
| VRouterConfig → Target with `os: vyos` | **Allow** |
| VRouterConfig → Target with `os: openwrt` | **Reject** |
| OwrtConfig → Target with `os: openwrt` | **Allow** |
| OwrtConfig → Target with `os: vyos` | **Reject** |
| Binding template type doesn't match target OS | **Reject** |

The CRD kind itself implies the OS type. The `os` field on Target lets webhooks enforce the match.

## Why Per-OS CRDs Instead of a Generic RouterConfig

Considered alternative: a single `RouterConfig` CRD with an `os` field. Rejected because:

1. **Validation diverges** — VyOS config has different constraints than OpenWrt UCI. Separate CRDs = separate webhook validators with correct type-specific rules.
2. **Controller logic diverges** — VyOS wraps in vbash, OpenWrt uses UCI batch, Cisco uses NETCONF. One controller with a big switch is worse than separate controllers.
3. **CRD schema diverges** — different OS may need different spec fields. Separate types = clean Go structs, no `interface{}` or union types.
4. **Kubernetes convention** — different resource semantics = different kinds. `Deployment` vs `StatefulSet`, not `Workload{type: deployment}`.

## Relationship to gRPC Agent

The gRPC agent (`vrouter-agent` repo) supports multi-OS via a pluggable `Backend` interface:

```go
// In vrouter-agent
type Backend interface {
    ApplyConfig(ctx context.Context, payload []byte) (*Result, error)
    GetStatus(ctx context.Context) (*Status, error)
}
```

The mapping:

```
Operator side                    Agent side
─────────────                    ──────────
VRouterConfig controller         VyOS Backend
  → renders vbash/set commands
  → sends via AgentPool

OwrtConfig controller            OpenWrt Backend
  → renders UCI commands
  → sends via AgentPool
```

Operator renders OS-specific config, agent applies OS-specific config. gRPC transport in the middle is OS-agnostic.

## Relationship to Drift Detection

See `vrouterstatus-drift-detection.md`. Drift detection needs to read config back from the router. This works per-OS:

- **QGA providers**: `ReadFile` on Provider interface (OS-specific paths: `/config/config.boot` for VyOS, `/etc/config/` for OpenWrt)
- **gRPC providers**: Agent's `GetStatus` pushes config hash back (Backend decides what to hash)

VRouterStatus (drift detection CRD) would also be per-OS, following the same pattern.

## Implementation Priority

1. **gRPC agent** — new capability (bare metal support), larger architectural impact
2. **Drift detection** — enhancement for existing providers, design should account for both pull (QGA ReadFile) and push (gRPC agent status) patterns
3. **Multi-OS** — when a second OS is actually needed, not before. The `os` field on Target and per-OS CRD pattern are ready to adopt.

Current focus: get VyOS right first. This proposal documents the architecture so multi-OS can be added without rework.
