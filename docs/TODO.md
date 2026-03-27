# TODO (Backlog)

Items here are design proposals and planned features. Do not implement unless explicitly instructed.

For detailed proposals, see [proposals/](proposals/).

---

## VRouterTarget Health Probe

**Goal**: Detect when a target VM goes down and recovers, then automatically re-trigger reconciliation of related VRouterConfigs so config is re-applied after recovery.

> Note: `VRouterTarget` already has `+kubebuilder:subresource:status` and a status struct with `ProxmoxNode` and `LastRebootTime`. The Health Probe would add `Health`, `Message`, `LastProbeTime`, and `LastTransitionTime` fields to the existing status.

### New Fields on VRouterTargetStatus

```go
type VRouterTargetStatus struct {
    // existing fields
    ProxmoxNode    string       `json:"proxmoxNode,omitempty"`
    LastRebootTime *metav1.Time `json:"lastRebootTime,omitempty"`

    // new fields for Health Probe
    // +kubebuilder:validation:Enum=Healthy;Unhealthy;Unknown
    // +kubebuilder:default=Unknown
    Health string `json:"health,omitempty"`
    // +optional
    Message string `json:"message,omitempty"`
    // +optional
    LastProbeTime *metav1.Time `json:"lastProbeTime,omitempty"`
    // +optional
    LastTransitionTime *metav1.Time `json:"lastTransitionTime,omitempty"`
}
```

### New: TargetController

| Item | Description |
|------|-------------|
| Watch | VRouterTarget, VMI (KubeVirt); Proxmox: poll |
| Output | Updates VRouterTarget.Status.Health |
| Side effect | On Unhealthy→Healthy transition, resets related VRouterConfig phases |

**Reconcile flow:**

```
1. Query VM state by provider type:
   KubeVirt: check VMI.status.phase == Running
   Proxmox:  GET /api2/json/nodes/{node}/qemu/{vmid}/status/current

2. Update VRouterTarget.Status.Health:
   VM Running   → Healthy
   VM other     → Unhealthy
   Query failed → Unknown

3. If Health transitions non-Healthy → Healthy:
   → List all VRouterConfigs with label vrouter.kojuro.date/target={name}
   → Patch phase: Applied → Pending for each
   → VRouterController picks up the change and re-applies
```

**Watch setup (KubeVirt):**

```go
ctrl.NewControllerManagedBy(mgr).
    For(&vrouterv1.VRouterTarget{}).
    Watches(
        &kubevirtv1.VirtualMachineInstance{},
        handler.EnqueueRequestsFromMapFunc(mapVMIToTarget),
    ).
    Complete(r)
```

Use a field index on `.spec.provider.kubevirt.name` + `.spec.provider.kubevirt.namespace` to efficiently map VMI → VRouterTarget.

### Trigger Summary

| Event | Action |
|-------|--------|
| VM goes down | Health: Healthy → Unhealthy. No VRouterConfig changes (wait for recovery). |
| VM recovers | Health: Unhealthy → Healthy. Batch patch VRouterConfigs phase: Applied → Pending. |
| VRouterConfig re-queued | VRouterController re-applies config via QGA. |

BindingController already watches VRouterTarget and will re-reconcile on health changes, but Binding re-reconcile only re-renders spec — it does **not** reset VRouterConfig phase. Phase reset must be done by TargetController.

---

## VRouterConfig: loadDefaultConfigOnDelete

**Goal**: Option to load a default/baseline config on the router when a VRouterConfig is deleted, so the router doesn't retain stale configuration.

### Considerations

- Semantics of "default config" need clear definition (VyOS startup config? user-specified baseline?)
- Deletion path becomes slow and can block if VM/QGA is unreachable (finalizer stuck)
- Interaction with `save: true` — after loading default, should it save again?
- Orphan cleanup in BindingController already deletes VRouterConfigs; triggering load-default on every orphan delete may cause unintended rollbacks when multiple configs target the same router
- Consider placing the policy at VRouterBinding level (`cleanupPolicy: LoadDefault | Retain`, similar to PV reclaimPolicy) rather than per-VRouterConfig
- Need a timeout + force-delete escape hatch to avoid permanently stuck finalizers

---

## Open Design Questions

1. **QGA ping vs VMI phase**: VMI Running does not guarantee QGA is responsive (boot window). Option: use VMI phase as proxy; let VRouterController's natural failure → Failed handle the transient window. QGA-level ping adds complexity.

2. **Direct VRouterConfigs (no Binding)**: Configs created without a Binding have no `vrouter.kojuro.date/target` label. TargetController cannot find them. Option: VRouterController stamps the target label when `spec.provider` can be matched to a VRouterTarget.

3. **RBAC**: TargetController needs `get/list/watch` on `virtualmachineinstances` (already in §8) and `update/patch` on `vrouterconfigs/status` and `vrouterconfigs`.
