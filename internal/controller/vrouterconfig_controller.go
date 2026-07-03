/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	kubevirtv1 "kubevirt.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	vrouterv1 "github.com/tjjh89017/vrouter-operator/api/v1"
	"github.com/tjjh89017/vrouter-operator/internal/provider"
	providertypes "github.com/tjjh89017/vrouter-operator/internal/provider/types"
)

// VRouterConfigReconciler reconciles a VRouterConfig object.
type VRouterConfigReconciler struct {
	client.Client
	Scheme     *runtime.Scheme
	RestConfig *rest.Config
}

// +kubebuilder:rbac:groups=vrouter.kojuro.date,resources=vrouterconfigs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=vrouter.kojuro.date,resources=vrouterconfigs/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=vrouter.kojuro.date,resources=vrouterconfigs/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=pods/exec,verbs=create
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups=kubevirt.io,resources=virtualmachines;virtualmachineinstances,verbs=get;list;watch

const requeueAfter = 3 * time.Second

// execApplyTimeout bounds how long a dispatched apply script may run before
// the controller gives up on it and fails the config, instead of polling
// GetExecStatus forever. Without this bound, a script that never exits (a
// blocked commit lock, a hung command) pins the config in Applying with no
// failure path. This is a fixed constant rather than a CRD field for now.
const execApplyTimeout = 5 * time.Minute

func (r *VRouterConfigReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var cfg vrouterv1.VRouterConfig
	if err := r.Get(ctx, req.NamespacedName, &cfg); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !cfg.DeletionTimestamp.IsZero() {
		return r.onDelete(ctx, req, &cfg)
	}

	// Ensure finalizer is present (only for non-deleting objects).
	if !controllerutil.ContainsFinalizer(&cfg, vrouterv1.FinalizerName) {
		controllerutil.AddFinalizer(&cfg, vrouterv1.FinalizerName)
		if err := r.Update(ctx, &cfg); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	return r.onChange(ctx, req, &cfg)
}

func (r *VRouterConfigReconciler) onChange(ctx context.Context, _ ctrl.Request, cfg *vrouterv1.VRouterConfig) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var target vrouterv1.VRouterTarget
	if err := r.Get(ctx, k8stypes.NamespacedName{Name: cfg.Spec.TargetRef.Name, Namespace: vrouterv1.ResolveNamespace(cfg.Spec.TargetRef, cfg.Namespace)}, &target); err != nil {
		return ctrl.Result{}, fmt.Errorf("get target %q: %w", cfg.Spec.TargetRef.Name, err)
	}

	prov, err := provider.New(ctx, &target, r.Client, r.RestConfig)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("build provider: %w", err)
	}

	// Check whether the VM is running; if stopped, return without requeue.
	// The VRouterTarget controller polls IsVMRunning every 60 s and patches
	// Status.VMRunning; that status change triggers a configsForTarget watch
	// event which will re-enqueue this VRouterConfig when the VM comes back.
	running, err := prov.IsVMRunning(ctx)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("check VM running: %w", err)
	}
	if !running {
		log.Info("VM is stopped, skipping reconcile")
		return ctrl.Result{}, nil
	}

	// Step 1: pre-check — QGA ping + vyos-router.service is-active.
	if err := prov.CheckReady(ctx); err != nil {
		log.Info("router not ready, will retry", "reason", err.Error())
		// VM may have rebooted; reset phase so we re-apply when it comes back.
		if cfg.Status.Phase == vrouterv1.PhaseApplied {
			patch := client.MergeFrom(cfg.DeepCopy())
			cfg.Status.Phase = vrouterv1.PhasePending
			if err := r.Status().Patch(ctx, cfg, patch); err != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}

	// Steps 2-3: poll a running script, or decide whether to (re-)dispatch.
	return r.dispatchOrPoll(ctx, cfg, prov, &target)
}

// dispatchOrPoll implements steps 2-3 of onChange: if a script is already
// running, poll it; otherwise decide whether the current generation (or a
// target reboot) requires a fresh apply dispatch. It is factored out of
// onChange so it can be exercised directly in tests with a fake Provider,
// without needing a real provider.New target (which performs live network
// calls in CheckReady/IsVMRunning).
func (r *VRouterConfigReconciler) dispatchOrPoll(ctx context.Context, cfg *vrouterv1.VRouterConfig, prov provider.Provider, target *vrouterv1.VRouterTarget) (ctrl.Result, error) {
	// Step 2: check for a running script first.
	if cfg.Status.ExecPID > 0 {
		return r.pollExecStatus(ctx, cfg, prov)
	}

	// If the VM was rebooted more recently than the last reboot-forced apply
	// we dispatched, force re-apply by resetting observedGeneration so the
	// generation check below triggers the apply path. shouldForceReapplyForReboot
	// only fires for a reboot we have not yet dispatched an attempt for, so a
	// single reboot forces at most one re-apply — even if that apply fails —
	// instead of hot-looping (see SPEC §7.2: Failed has no auto-retry).
	effectiveObservedGen := cfg.Status.ObservedGeneration
	if shouldForceReapplyForReboot(cfg.Status.LastRebootHandledTime, target.Status.LastRebootTime) {
		effectiveObservedGen = 0
	}

	// Skip only when this generation is conclusively done (Applied or Failed).
	if cfg.Generation == effectiveObservedGen &&
		(cfg.Status.Phase == vrouterv1.PhaseApplied || cfg.Status.Phase == vrouterv1.PhaseFailed) {
		return ctrl.Result{}, nil
	}

	// Step 3: phase is Pending or spec changed — render and apply.
	return r.applyConfig(ctx, cfg, prov, target.Status.LastRebootTime)
}

// shouldForceReapplyForReboot decides whether a target reboot should force a
// fresh apply attempt for this generation. It returns true only when the
// target has rebooted more recently than the last reboot-forced dispatch
// recorded in lastRebootHandledTime, so a given reboot forces at most one
// re-apply attempt — regardless of whether that attempt later succeeds or
// fails. Callers must stamp lastRebootHandledTime at dispatch time (not only
// on success) so a failing apply does not get re-dispatched on every
// reconcile.
func shouldForceReapplyForReboot(lastRebootHandledTime, targetLastRebootTime *metav1.Time) bool {
	if targetLastRebootTime == nil {
		return false
	}
	if lastRebootHandledTime == nil {
		return true
	}
	return lastRebootHandledTime.Before(targetLastRebootTime)
}

func (r *VRouterConfigReconciler) onDelete(ctx context.Context, _ ctrl.Request, cfg *vrouterv1.VRouterConfig) (ctrl.Result, error) {
	controllerutil.RemoveFinalizer(cfg, vrouterv1.FinalizerName)
	return ctrl.Result{}, r.Update(ctx, cfg)
}

// pollExecStatus checks the running script and updates status accordingly.
func (r *VRouterConfigReconciler) pollExecStatus(ctx context.Context, cfg *vrouterv1.VRouterConfig, prov provider.Provider) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	status, err := prov.GetExecStatus(ctx, cfg.Status.ExecPID)
	if err != nil && errors.Is(err, providertypes.ErrExecResultLost) {
		// The previously dispatched exec handle is gone for good (operator
		// restart lost its in-memory registry, or the guest agent that owned
		// the PID restarted after a VM reboot). Retrying GetExecStatus with
		// the same handle will never succeed, so give up on it and reset to
		// Pending: the next reconcile's generation check in onChange will
		// re-dispatch a fresh ExecScript call instead of looping forever.
		log.Info("exec result lost, resetting to re-dispatch", "pid", cfg.Status.ExecPID, "reason", err.Error())
		patch := client.MergeFrom(cfg.DeepCopy())
		cfg.Status.ExecPID = 0
		cfg.Status.ExecStartedTime = nil
		cfg.Status.Phase = vrouterv1.PhasePending
		cfg.Status.Message = "previous apply result was lost; re-applying"
		if patchErr := r.Status().Patch(ctx, cfg, patch); patchErr != nil {
			return ctrl.Result{}, patchErr
		}
		return ctrl.Result{RequeueAfter: requeueAfter}, nil
	}

	// Not yet a confirmed exit: either GetExecStatus itself failed (a
	// transient error, e.g. a network blip, or — for providers that cannot
	// reliably distinguish "unknown pid" from other failures, see the
	// KubeVirt/Proxmox GetExecStatus doc comments — a guest agent that no
	// longer recognizes the pid after a reboot) or it succeeded but reports
	// the script as still running. Either way, this exec has not produced a
	// result yet, so the same execApplyTimeout bound must apply: without
	// this check here, a persistently erroring GetExecStatus would return
	// early on every reconcile and never reach the timeout logic, leaving
	// the config stuck in Applying forever (with ExecPID>0 also preventing
	// the generation check from ever running) — the exact deadlock this
	// timeout and the lost-result recovery above both exist to prevent.
	if err != nil || !status.Exited {
		if execStarted := cfg.Status.ExecStartedTime; execStarted != nil && time.Since(execStarted.Time) > execApplyTimeout {
			// The script has been running longer than execApplyTimeout with no
			// sign of exiting (e.g. a blocked commit lock or a hung command)
			// or confirmed result (persistent GetExecStatus errors). Give up
			// on this exec so it does not pin the config in Applying forever:
			// fail the config, clear execPID/ExecStartedTime so a later
			// generation change or reboot can dispatch a fresh attempt
			// through the normal path, and do NOT requeue for retry (Failed
			// has no auto-retry, same as any other apply failure).
			msg := fmt.Sprintf("apply timed out after %s", execApplyTimeout)
			if err != nil {
				msg = fmt.Sprintf("apply timed out after %s (last error polling exec status: %s)", execApplyTimeout, err.Error())
			}
			log.Info("apply exec timed out, marking failed", "pid", cfg.Status.ExecPID, "startedAt", execStarted.Time, "timeout", execApplyTimeout, "lastPollErr", err)
			patch := client.MergeFrom(cfg.DeepCopy())
			cfg.Status.ExecPID = 0
			cfg.Status.ExecStartedTime = nil
			cfg.Status.Phase = vrouterv1.PhaseFailed
			cfg.Status.Message = msg
			meta.SetStatusCondition(&cfg.Status.Conditions, metav1.Condition{
				Type:               vrouterv1.ConditionApplied,
				Status:             metav1.ConditionFalse,
				Reason:             "ApplyTimeout",
				Message:            cfg.Status.Message,
				ObservedGeneration: cfg.Status.ObservedGeneration,
			})
			return ctrl.Result{}, r.Status().Patch(ctx, cfg, patch)
		}

		if err != nil {
			return ctrl.Result{}, fmt.Errorf("get exec status: %w", err)
		}

		patch := client.MergeFrom(cfg.DeepCopy())
		cfg.Status.Phase = vrouterv1.PhaseApplying
		if err := r.Status().Patch(ctx, cfg, patch); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: requeueAfter}, nil
	}

	// Script exited — update status.
	patch := client.MergeFrom(cfg.DeepCopy())
	cfg.Status.ExecPID = 0
	cfg.Status.ExecStartedTime = nil
	// Stamp the condition with status.ObservedGeneration -- the generation
	// this exec was dispatched for in applyConfig -- rather than the current
	// cfg.Generation. If the spec changed while the script was running,
	// cfg.Generation may already have moved on to a generation that was
	// never actually applied; the exec that just finished only speaks for
	// the generation it was dispatched for.
	if status.ExitCode == 0 {
		now := metav1.Now()
		cfg.Status.Phase = vrouterv1.PhaseApplied
		cfg.Status.LastAppliedTime = &now
		cfg.Status.Message = ""
		meta.SetStatusCondition(&cfg.Status.Conditions, metav1.Condition{
			Type:               vrouterv1.ConditionApplied,
			Status:             metav1.ConditionTrue,
			Reason:             "ConfigApplied",
			Message:            "Configuration applied successfully.",
			ObservedGeneration: cfg.Status.ObservedGeneration,
		})
		log.Info("config applied successfully")
	} else {
		cfg.Status.Phase = vrouterv1.PhaseFailed
		cfg.Status.Message = fmt.Sprintf("exitCode=%d stderr=%s", status.ExitCode, strings.TrimSpace(status.Stderr))
		meta.SetStatusCondition(&cfg.Status.Conditions, metav1.Condition{
			Type:               vrouterv1.ConditionApplied,
			Status:             metav1.ConditionFalse,
			Reason:             "ConfigFailed",
			Message:            cfg.Status.Message,
			ObservedGeneration: cfg.Status.ObservedGeneration,
		})
		log.Info("config apply failed", "exitCode", status.ExitCode, "stderr", status.Stderr)
	}
	return ctrl.Result{}, r.Status().Patch(ctx, cfg, patch)
}

// applyConfig dispatches config execution to the provider. targetRebootTime
// is the current VRouterTarget.Status.LastRebootTime (may be nil); it is
// stamped into cfg.Status.LastRebootHandledTime at dispatch time — whether
// the apply later succeeds or fails — so a reboot forces at most one
// re-apply attempt instead of re-dispatching on every reconcile.
func (r *VRouterConfigReconciler) applyConfig(ctx context.Context, cfg *vrouterv1.VRouterConfig, prov provider.Provider, targetRebootTime *metav1.Time) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	pid, err := prov.ExecScript(ctx, cfg.Spec.Config, cfg.Spec.Commands, vrouterv1.BoolValue(cfg.Spec.Save, true))
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("exec script: %w", err)
	}
	log.Info("script dispatched", "pid", pid)

	now := metav1.Now()
	patch := client.MergeFrom(cfg.DeepCopy())
	cfg.Status.ExecPID = pid
	cfg.Status.ExecStartedTime = &now
	cfg.Status.ObservedGeneration = cfg.Generation
	cfg.Status.Phase = vrouterv1.PhaseApplying
	cfg.Status.Message = fmt.Sprintf("applying generation %d", cfg.Generation)
	cfg.Status.LastRebootHandledTime = targetRebootTime
	// A new generation is being dispatched, so any previous Applied=True
	// condition is now stale (it describes a generation that is no longer
	// current). Mark Applied=False for the generation being dispatched here
	// so `kubectl wait --for=condition=Applied` does not return early against
	// the old True condition while the new script is still running.
	meta.SetStatusCondition(&cfg.Status.Conditions, metav1.Condition{
		Type:               vrouterv1.ConditionApplied,
		Status:             metav1.ConditionFalse,
		Reason:             "Applying",
		Message:            cfg.Status.Message,
		ObservedGeneration: cfg.Status.ObservedGeneration,
	})
	if err := r.Status().Patch(ctx, cfg, patch); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: requeueAfter}, nil
}

// vmiToVRouterConfigs maps a VirtualMachineInstance change event to VRouterConfig reconcile requests.
func (r *VRouterConfigReconciler) vmiToVRouterConfigs(ctx context.Context, obj client.Object) []reconcile.Request {
	vmiName := obj.GetName()
	vmiNS := obj.GetNamespace()
	var cfgList vrouterv1.VRouterConfigList
	if err := r.List(ctx, &cfgList); err != nil {
		return nil
	}
	var requests []reconcile.Request
	for i := range cfgList.Items {
		cfg := &cfgList.Items[i]
		var target vrouterv1.VRouterTarget
		if err := r.Get(ctx, k8stypes.NamespacedName{Name: cfg.Spec.TargetRef.Name, Namespace: vrouterv1.ResolveNamespace(cfg.Spec.TargetRef, cfg.Namespace)}, &target); err != nil {
			continue
		}
		kv := target.Spec.Provider.KubeVirt
		if kv == nil {
			continue
		}
		ns := kv.Namespace
		if ns == "" {
			ns = target.Namespace
		}
		if kv.Name == vmiName && ns == vmiNS {
			requests = append(requests, reconcile.Request{
				NamespacedName: k8stypes.NamespacedName{
					Namespace: cfg.Namespace,
					Name:      cfg.Name,
				},
			})
		}
	}
	return requests
}

// configsForTarget maps a VRouterTarget change to reconcile requests for all VRouterConfigs that reference it.
func (r *VRouterConfigReconciler) configsForTarget(ctx context.Context, obj client.Object) []reconcile.Request {
	var cfgList vrouterv1.VRouterConfigList
	if err := r.List(ctx, &cfgList, client.InNamespace(obj.GetNamespace())); err != nil {
		return nil
	}
	var requests []reconcile.Request
	for i := range cfgList.Items {
		if cfgList.Items[i].Spec.TargetRef.Name == obj.GetName() {
			requests = append(requests, reconcile.Request{
				NamespacedName: k8stypes.NamespacedName{
					Namespace: cfgList.Items[i].Namespace,
					Name:      cfgList.Items[i].Name,
				},
			})
		}
	}
	return requests
}

// SetupWithManager sets up the controller with the Manager.
func (r *VRouterConfigReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&vrouterv1.VRouterConfig{}).
		Watches(&kubevirtv1.VirtualMachineInstance{}, handler.EnqueueRequestsFromMapFunc(r.vmiToVRouterConfigs)).
		Watches(&vrouterv1.VRouterTarget{}, handler.EnqueueRequestsFromMapFunc(r.configsForTarget)).
		Named("vrouterconfig").
		Complete(r)
}
