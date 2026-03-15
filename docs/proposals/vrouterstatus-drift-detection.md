# VRouterStatus — Config Drift Detection

**Status**: Proposal (not yet implemented)
**Date**: 2026-03-15

## Goal

Detect when a router's running config drifts from what VRouterConfig last applied, and automatically trigger re-apply. Provide a debug/pause mode to suppress re-apply.

## Overview

```
VRC applies config → VRC.status.lastAppliedTime updated
                   ↓
VRS controller sees lastAppliedTime > lastBaselineTime → re-baseline
VRS controller periodic read → compare hash vs baseline
                   ↓ drift detected
VRS patches VRC.status.phase: Applied → Pending
                   ↓
VRC reconciles (Pending bypasses generation skip) → re-applies
```

**Infinite loop prevention**: VRS uses `VRC.status.lastAppliedTime` to know when to re-baseline. After VRC re-applies, lastAppliedTime updates, VRS re-baselines with the new config. If config matches baseline, no drift. Stable.

**Debug mode**: Annotation `vrouter.kojuro.date/paused: "true"` on VRC. VRS still updates its own status (drift visible) but doesn't patch VRC. VRC also checks annotation and skips apply if paused. Annotation doesn't increment generation — unpause relies on VRS's next periodic reconcile (max syncInterval delay) to detect drift and reset VRC phase.

## Config Storage

Storing config content inline in CRD status would hit etcd's 1.5 MiB per-object limit. Instead, both configs are stored in a single ConfigMap owned by the VRS object (ownerRef → GC on VRS deletion). The VRS status only records hashes and the ConfigMap name.

```
VRS object (small):
  status.startupConfigHash = "sha256:abc..."
  status.runningConfigHash = "sha256:def..."
  status.configRef         = "vrs-<name>-config"   ← ConfigMap name

ConfigMap "vrs-<name>-config":
  data:
    startup: |    ← startup config (/config/config.boot)
      ...
    running: |    ← running config (committed but possibly unsaved)
      ...
```

### Two configs, two reads

Both are fetched in the same reconcile cycle via `ReadFile`:

| Key | Source | How |
|-----|--------|-----|
| `startup` | `/config/config.boot` | `ReadFile("/config/config.boot")` |
| `running` | VyOS active config | `ReadFile` on known path **or** `ExecScript("cli-shell-api showConfig")` — path TBD at implementation time |

> **Implementation note**: VyOS stores the committed running config in a directory tree under `/opt/vyatta/config/active/`. The plain-text serialization is produced by `cli-shell-api showConfig`. If a stable single-file path is confirmed, use `ReadFile`; otherwise execute the command and capture stdout (requires an `ExecAndRead` provider method or inline exec).

### Drift detection signal

Primary drift signal uses **startup config** (`config.boot`) hash:
- If someone does `commit` only: running changes, config.boot doesn't → running hash differs but no drift trigger.
- If someone does `commit && save`: both change → drift triggered on startup hash.

Running config is stored for human inspection (shows uncommitted-only changes). Future enhancement: add a `DriftMode` field to choose which signal triggers re-apply.

Users can inspect configs:
```bash
kubectl get cm vrs-<name>-config -o jsonpath='{.data.startup}'
kubectl get cm vrs-<name>-config -o jsonpath='{.data.running}'
```

## New CRD: VRouterStatus

```go
type VRouterStatusSpec struct {
    // ConfigRef references the VRouterConfig this status monitors.
    ConfigRef NameRef `json:"configRef"`
    // SyncInterval controls how often config is read from the router.
    // +kubebuilder:default="5m"
    SyncInterval metav1.Duration `json:"syncInterval,omitempty"`
}

type VRouterStatusStatus struct {
    StartupConfigHash string       `json:"startupConfigHash,omitempty"` // SHA-256 of /config/config.boot
    RunningConfigHash string       `json:"runningConfigHash,omitempty"` // SHA-256 of running config
    ConfigRef         string       `json:"configRef,omitempty"`         // name of ConfigMap holding raw content
    BaselineHash      string       `json:"baselineHash,omitempty"`      // startupConfigHash at last VRC apply
    LastBaselineTime  *metav1.Time `json:"lastBaselineTime,omitempty"`  // when baseline was captured
    DriftDetected     bool         `json:"driftDetected,omitempty"`
    LastFetchTime     *metav1.Time `json:"lastFetchTime,omitempty"`
    Message           string       `json:"message,omitempty"`
    Conditions        []metav1.Condition `json:"conditions,omitempty"` // Synced condition
}
```

Short names: `vrs`, `vrouterstatus`. Print columns: Config, Drift, LastFetch, Age.

> Note: `ConfigRef` in spec (references VRouterConfig) vs `ConfigRef` in status (name of ConfigMap). Consider renaming status field to `ConfigMapRef` at implementation time to avoid confusion.

## Provider Interface: Add ReadFile

```go
ReadFile(ctx context.Context, path string) ([]byte, error)
```

- KubeVirt: QGA `guest-file-open` (mode "r") + `guest-file-read` loop + `guest-file-close`
- Proxmox: REST API `GET /nodes/{node}/qemu/{vmid}/agent/file-read`

New QGA constants: `CmdFileOpenRead`, `CmdFileRead`, `ConfigBootPath`, `FileReadChunkSize`.

## VRS Controller Reconcile Flow

```
1. GET VRC via vrs.spec.configRef
2. GET VRouterTarget via vrc.spec.targetRef
3. Build provider, check IsVMRunning + CheckReady
4. ReadFile(/config/config.boot) → startupContent, startupHash (sha256)
   ReadFile(<running-config-path>) → runningContent, runningHash (sha256)
   (both fetched; if running config read fails, log warning and store empty)
5. Create/update ConfigMap "vrs-<name>-config" (ownerRef → VRS):
     data["startup"] = startupContent
     data["running"]     = runningContent
6. If vrc.status.lastAppliedTime > vrs.status.lastBaselineTime:
   → Capture new baseline (baselineHash = startupHash), driftDetected = false
7. Else compare startupHash vs baselineHash:
   → Different: driftDetected = true; if VRC not paused AND phase == Applied → patch phase → Pending
   → Same: driftDetected = false
8. Update VRS status (startupConfigHash, runningConfigHash, configRef, driftDetected, lastFetchTime), requeue after syncInterval
```

Watches VRouterConfig changes (via `statusForConfig` mapper) so VRS detects lastAppliedTime changes promptly.

## VRC Controller Changes

- **Auto-create VRS** on first successful apply (ownerRef to VRC, configRef = vrc.name)
- **Paused annotation check** at start of `onChange` — skip entire reconcile if `vrouter.kojuro.date/paused: "true"`

## New Constants

```go
const AnnotationPaused = "vrouter.kojuro.date/paused"
const ConditionSynced = "Synced"
```

## Edge Cases

| Case | Handling |
|------|----------|
| VRC in Failed/Applying | VRS only resets Applied → Pending. Other phases left alone. |
| VRC paused | VRS reads config and updates driftDetected, but doesn't patch VRC. |
| Unpause | Relies on VRS next periodic reconcile (max syncInterval delay). |
| Drift persists after re-apply | Loop continues until user pauses or fixes root cause. |
| ReadFile fails | Set Synced=False, requeue. No error return (avoids exponential backoff). |

## RBAC (VRS Controller)

```
vrouterstatuses:            get;list;watch;create;update;patch;delete
vrouterstatuses/status:     get;update;patch
vrouterstatuses/finalizers: update
vrouterconfigs:             get;list;watch
vrouterconfigs/status:      get;patch
vroutertargets:             get;list;watch
configmaps:                 get;list;watch;create;update;patch;delete
pods, pods/exec, secrets:   (same as VRC controller)
virtualmachines/instances:  get;list;watch
```

## Future Extensibility

The ConfigMap + VRS design has two natural extension points:

### ConfigMap keys (add router observability data)

New keys can be added to the same ConfigMap without changing the CRD:

```
data:
  startup:          ← /config/config.boot
  running:          ← active committed config
  interfaces:       ← e.g. ip -j link show
  routes:           ← e.g. ip -j route show
  bgp-summary:      ← e.g. vtysh -c 'show bgp summary json'
  dhcp-leases:      ← e.g. show dhcp server leases
```

Any new key is purely additive — existing consumers are unaffected.

### VRS spec/status fields (structured observability or new triggers)

New fields can be added to `VRouterStatusStatus` for structured data or additional drift signals:

```go
// Future examples:
InterfaceCount   int    `json:"interfaceCount,omitempty"`
BGPPeerCount     int    `json:"bgpPeerCount,omitempty"`
DriftMode        string `json:"driftMode,omitempty"` // "startup" | "running" | "both"
```

`DriftMode` in spec would let users choose which config signal triggers re-apply (startup only, running only, or either).

## Files to Create/Modify

### New Files
- `api/v1/vrouterstatus_types.go` — CRD type definitions
- `internal/controller/vrouterstatus_controller.go` — VRS controller
- `internal/webhook/v1/vrouterstatus_webhook.go` — defaulting + validation webhook

### Modified Files
- `api/v1/constants.go` — add `AnnotationPaused`, `ConditionSynced`
- `internal/provider/types/types.go` — add `ReadFile` to Provider interface
- `internal/provider/qga/constants.go` — add file-read QGA commands
- `internal/provider/kubevirt/provider.go` — implement `ReadFile`
- `internal/provider/proxmox/provider.go` — implement `ReadFile`
- `internal/controller/vrouterconfig_controller.go` — auto-create VRS on apply, paused annotation check
- `cmd/main.go` — register VRS controller and webhook
