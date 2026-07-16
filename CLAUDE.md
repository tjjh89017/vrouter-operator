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

# Release packaging — single YAML (dist/install.yaml)
make build-installer IMG=<image>

# Release packaging — Helm chart (regenerate after CRD/manifest changes)
# Requires: go install github.com/arttor/helmify/cmd/helmify@latest
# Chart lives in charts/vrouter-operator/; run helm lint before committing
# CAUTION: the chart carries manual fixes helmify does not generate. Templates
# are intentionally comment-free, so this list is the only record — re-apply
# and verify each item after regenerating:
#   - deployment.yaml, metrics-service.yaml, webhook-service.yaml: no hardcoded
#     app.kubernetes.io/name in selectors / pod template labels; helmify emits
#     it alongside selectorLabels (arttor/helmify#196) and the duplicate key is
#     rejected by Flux/ArgoCD strict YAML parsers
#   - metrics-service.yaml: Service name shortened from the generated
#     "-controller-manager-metrics-service" suffix and wrapped with
#     trunc 63/trimSuffix to respect the 63-char Service name limit
#   - webhook-service.yaml (and every template referencing it): name via the
#     vrouter-operator.webhookServiceName helper in _helpers.tpl (trunc-63 guard)
#   - values.yaml + deployment.yaml: manager `env` list so operators can set
#     e.g. ENABLE_WEBHOOKS=false (helmify does not generate it)
#   - values.yaml + deployment.yaml: manager `imagePullPolicy` field wired to
#     the container spec (helmify does not generate it)
#   - values.yaml + deployment.yaml: top-level `imagePullSecrets: []` rendered
#     into the pod spec via `with` (helmify does not generate it)
#   - values.yaml + deployment.yaml: `image.tag` defaults to `""` and falls
#     back to `.Chart.AppVersion` in the template, not a hardcoded `latest`
bin/kustomize build config/default | helmify charts/vrouter-operator
helm package charts/vrouter-operator  # → vrouter-operator-<version>.tgz
```

## Git

Always use `git commit -s` (DCO sign-off) for all commits.

## Design spec

**Read `docs/SPEC.md` first.** It is the authoritative design document and covers CRD schemas, controller flow, provider abstraction, params merge semantics, template engine, webhook rules, QGA script execution, and RBAC requirements in detail.

**`docs/TODO.md` is the backlog.** Items there are design proposals only — do not implement anything from TODO.md unless explicitly instructed.

**`docs/proposals/`** contains detailed design proposals for future features (gRPC agent, drift detection, multi-OS support).

## Architecture

This is a **kubebuilder/operator-sdk** operator (Go module: `github.com/tjjh89017/vrouter-operator`, API group: `vrouter.kojuro.date/v1`). See `docs/SPEC.md §2` for the full architecture diagram.

### Key directories

| Path | Purpose |
|------|---------|
| `api/v1/` | CRD type definitions (flat, no subfolders) |
| `internal/controller/` | Reconcilers for all 6 CRDs |
| `internal/webhook/v1/` | Defaulting + validating webhooks for all 6 CRDs |
| `internal/provider/` | Provider interface (`types/`), factory (`provider.go`), KubeVirt impl (`kubevirt/`), Proxmox impl (`proxmox/`), vrouter-daemon gRPC impl (`daemon/`), shared QGA constants (`qga/`) |
| `config/` | Kustomize manifests (CRDs, RBAC, webhook config) |
| `cmd/main.go` | Manager entrypoint; registers all controllers and webhooks |

### API types

Shared types live in `api/v1/shared_types.go` (ProviderConfig, SecretReference, etc.).
Provider-specific types are in `api/v1/kubevirt_types.go`, `api/v1/proxmox_types.go` (plus `api/v1/proxmoxcluster_types.go`), and `api/v1/daemon_types.go`.
Constants (finalizer name, label keys) are in `api/v1/constants.go`.

Provider identification: KubeVirt uses `KubeVirtConfig.Name` (VM name), Proxmox uses `ProxmoxConfig.VMID` (integer), vrouter-daemon uses `DaemonConfig.Address`/`AgentID`. See `docs/SPEC.md §9.6–9.8`.

### Controllers

See `docs/SPEC.md §7` for full reconcile flows and the internal vbash script template.

All controllers follow the same pattern in `Reconcile()`: check `DeletionTimestamp` → `onDelete`; ensure finalizer → `onChange`. Both helpers have signature `(ctx, req, obj) (Result, error)`.

- **BindingController**: `onDelete` removes finalizer (ownerRef cascade handles VRouterConfig GC). `onChange` resolves paramsRefs and targets, merges params (`paramsRefs` in list order → `binding.params` → `target.params`, later layers override), renders templates, cleans orphans, then either writes every VRouterConfig in one pass (`spec.rollout` Disabled, the default) or hands off to the serial rollout walk (`FixedInterval`/`WaitForApplied`), which writes at most one target's config per reconcile, gated by a persisted frontier in `status.rollout`. Watches VRouterTemplate/VRouterTarget/VRouterParams and maps events to referencing bindings.
- **VRouterController**: `onChange` runs CheckReady → generation-based apply → poll execPID. `phase` is display-only; `observedGeneration`+`execPID` drive control flow. `Applied` condition enables `kubectl wait`. Failed has no auto-retry.

### Webhooks

Webhooks are disabled when `ENABLE_WEBHOOKS=false` (env var, checked in `cmd/main.go`).
Cross-field validation (provider type vs sub-config presence) is in `internal/webhook/v1/validation.go`.
Defaulting webhooks are no-ops; field defaults use `+kubebuilder:default=` markers. See `docs/SPEC.md §6`.

### Code generation

After editing any `api/v1/` type or kubebuilder markers, always run:
```bash
make generate   # updates zz_generated.deepcopy.go
make manifests  # updates config/crd/bases/*.yaml
```
