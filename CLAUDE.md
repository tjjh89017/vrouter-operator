# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Commands

```bash
# Build
make build           # runs manifests + generate + fmt + vet + go build
go build ./...       # quick compile check without code generation

# Code generation (run after modifying api/v1/ types or kubebuilder markers)
make generate        # regenerates zz_generated.deepcopy.go
make manifests       # regenerates CRDs, RBAC, webhook manifests in config/

# Tests
make test            # unit tests via envtest (downloads K8s binaries on first run)
go test ./internal/controller/... -run TestFoo  # run a single test

# Lint / format
make fmt             # go fmt
make vet             # go vet
make lint            # golangci-lint (downloads to bin/ on first run)

# Run locally (no webhook TLS)
ENABLE_WEBHOOKS=false go run ./cmd/main.go

# Install CRDs into current cluster
make install

# Deploy to cluster
make deploy IMG=<image>
```

## Git

Always use `git commit -s` (DCO sign-off) for all commits.

## Design spec

**Read `SPEC.md` first.** It is the authoritative design document and covers CRD schemas, controller flow, provider abstraction, params merge semantics, template engine, webhook rules, QGA script execution, and RBAC requirements in detail.

**`TODO.md` is the backlog.** Items there are design proposals only — do not implement anything from TODO.md unless explicitly instructed.

## Architecture

This is a **kubebuilder/operator-sdk** operator (Go module: `github.com/tjjh89017/vrouter-operator`, API group: `vrouter.kojuro.date/v1`). See `SPEC.md §2` for the full architecture diagram.

### Key directories

| Path | Purpose |
|------|---------|
| `api/v1/` | CRD type definitions (flat, no subfolders) |
| `internal/controller/` | Reconcilers for all 4 CRDs |
| `internal/webhook/v1/` | Defaulting + validating webhooks for all 4 CRDs |
| `internal/provider/` | Provider interface (`types/`), factory (`provider.go`), KubeVirt impl (`kubevirt/`), Proxmox stub (`proxmox/`), shared QGA constants (`qga/`) |
| `config/` | Kustomize manifests (CRDs, RBAC, webhook config) |
| `cmd/main.go` | Manager entrypoint; registers all controllers and webhooks |

### API types

Shared types live in `api/v1/shared_types.go` (ProviderConfig, SecretReference, etc.).
Provider-specific types are in `api/v1/kubevirt_types.go` and `api/v1/proxmox_types.go`.
Constants (finalizer name, label keys) are in `api/v1/constants.go`.

Provider identification: KubeVirt uses `KubeVirtConfig.Name` (VM name), Proxmox uses `ProxmoxConfig.VMID` (integer). See `SPEC.md §9.6–9.7`.

### Controllers

See `SPEC.md §7` for full reconcile flows and the internal vbash script template.

All controllers follow the same pattern in `Reconcile()`: check `DeletionTimestamp` → `onDelete`; ensure finalizer → `onChange`. Both helpers have signature `(ctx, req, obj) (Result, error)`.

- **BindingController**: `onDelete` removes finalizer (ownerRef cascade handles VRouterConfig GC). `onChange` resolves targets, merges params, renders templates, creates/updates VRouterConfigs, cleans orphans.
- **VRouterController**: `onChange` runs CheckReady → generation-based apply → poll execPID. `phase` is display-only; `observedGeneration`+`execPID` drive control flow. `Applied` condition enables `kubectl wait`. Failed has no auto-retry.

### Webhooks

Webhooks are disabled when `ENABLE_WEBHOOKS=false` (env var, checked in `cmd/main.go`).
Cross-field validation (provider type vs sub-config presence) is in `internal/webhook/v1/validation.go`.
Defaulting webhooks are no-ops; field defaults use `+kubebuilder:default=` markers. See `SPEC.md §6`.

### Code generation

After editing any `api/v1/` type or kubebuilder markers, always run:
```bash
make generate   # updates zz_generated.deepcopy.go
make manifests  # updates config/crd/bases/*.yaml
```
