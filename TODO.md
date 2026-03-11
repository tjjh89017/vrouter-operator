# TODO (Backlog)

Items here are design proposals and planned features. Do not implement unless explicitly instructed.

---

## VRouterTarget Health Probe

**Goal**: Detect when a target VM goes down and recovers, then automatically re-trigger reconciliation of related VRouterConfigs so config is re-applied after recovery.

### New: VRouterTarget Status

Add `//+kubebuilder:subresource:status` and a status struct:

```go
//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:printcolumn:name="Health",type=string,JSONPath=`.status.health`
type VRouterTarget struct { ... }

type VRouterTargetStatus struct {
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

### Open Design Questions

1. **QGA ping vs VMI phase**: VMI Running does not guarantee QGA is responsive (boot window). Option: use VMI phase as proxy; let VRouterController's natural failure → Failed handle the transient window. QGA-level ping adds complexity.

2. **Direct VRouterConfigs (no Binding)**: Configs created without a Binding have no `vrouter.kojuro.date/target` label. TargetController cannot find them. Option: VRouterController stamps the target label when `spec.provider` can be matched to a VRouterTarget.

3. **RBAC**: TargetController needs `get/list/watch` on `virtualmachineinstances` (already in §8) and `update/patch` on `vrouterconfigs/status` and `vrouterconfigs`.
