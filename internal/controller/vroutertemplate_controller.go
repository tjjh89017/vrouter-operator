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

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	vrouterv1 "github.com/tjjh89017/vrouter-operator/api/v1"
)

// VRouterTemplateReconciler reconciles a VRouterTemplate object.
// VRouterTemplate is a pure data object; its content is consumed by
// VRouterBindingReconciler which watches it and re-renders on changes.
// This reconciler exists only to satisfy the manager registration requirement.
type VRouterTemplateReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=vrouter.kojuro.date,resources=vroutertemplates,verbs=get;list;watch;create;update;patch;delete

func (r *VRouterTemplateReconciler) Reconcile(_ context.Context, _ ctrl.Request) (ctrl.Result, error) {
	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *VRouterTemplateReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&vrouterv1.VRouterTemplate{}).
		Named("vroutertemplate").
		Complete(r)
}
