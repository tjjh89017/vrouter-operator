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
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	kubevirtv1 "kubevirt.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	vrouterv1 "github.com/tjjh89017/vrouter-operator/api/v1"
	"github.com/tjjh89017/vrouter-operator/internal/provider"
)

const vmRunningPollInterval = 60 * time.Second

// VRouterTargetReconciler reconciles a VRouterTarget object.
type VRouterTargetReconciler struct {
	client.Client
	Scheme     *runtime.Scheme
	RestConfig *rest.Config
}

// +kubebuilder:rbac:groups=vrouter.kojuro.date,resources=vroutertargets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=vrouter.kojuro.date,resources=vroutertargets/status,verbs=get;update;patch

func (r *VRouterTargetReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var target vrouterv1.VRouterTarget
	if err := r.Get(ctx, req.NamespacedName, &target); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	prov, err := provider.New(ctx, &target, r.Client, r.RestConfig)
	if err != nil {
		// Provider config incomplete (e.g. secret not yet available) — retry later.
		log.Error(err, "failed to build provider, will retry")
		return ctrl.Result{RequeueAfter: vmRunningPollInterval}, nil
	}

	running, err := prov.IsVMRunning(ctx)
	if err != nil {
		log.Error(err, "failed to check VM running state, will retry")
		return ctrl.Result{RequeueAfter: vmRunningPollInterval}, nil
	}

	if running != target.Status.VMRunning {
		log.Info("VM running state changed", "running", running)
		patch := client.MergeFrom(target.DeepCopy())
		target.Status.VMRunning = running
		if err := r.Status().Patch(ctx, &target, patch); err != nil {
			return ctrl.Result{}, fmt.Errorf("patch VRouterTarget status: %w", err)
		}
		// VRC controller watches VRouterTarget via configsForTarget; the status
		// patch above will automatically trigger reconciles for all referencing
		// VRouterConfigs — no explicit enqueue needed here.
	}

	return ctrl.Result{RequeueAfter: vmRunningPollInterval}, nil
}

// vmiToVRouterTargets maps a VirtualMachineInstance event to VRouterTarget
// reconcile requests, enabling immediate detection of VM start/stop for KubeVirt.
func (r *VRouterTargetReconciler) vmiToVRouterTargets(ctx context.Context, obj client.Object) []reconcile.Request {
	vmiName := obj.GetName()
	vmiNS := obj.GetNamespace()

	var targetList vrouterv1.VRouterTargetList
	if err := r.List(ctx, &targetList); err != nil {
		return nil
	}

	var requests []reconcile.Request
	for i := range targetList.Items {
		tgt := &targetList.Items[i]
		kv := tgt.Spec.Provider.KubeVirt
		if kv == nil {
			continue
		}
		ns := kv.Namespace
		if ns == "" {
			ns = tgt.Namespace
		}
		if kv.Name == vmiName && ns == vmiNS {
			requests = append(requests, reconcile.Request{
				NamespacedName: k8stypes.NamespacedName{
					Namespace: tgt.Namespace,
					Name:      tgt.Name,
				},
			})
		}
	}
	return requests
}

// SetupWithManager sets up the controller with the Manager.
func (r *VRouterTargetReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&vrouterv1.VRouterTarget{}).
		Watches(&kubevirtv1.VirtualMachineInstance{}, handler.EnqueueRequestsFromMapFunc(r.vmiToVRouterTargets)).
		Named("vroutertarget").
		Complete(r)
}
