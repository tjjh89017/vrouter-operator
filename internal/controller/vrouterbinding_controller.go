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

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	vrouterv1 "github.com/tjjh89017/vrouter-operator/api/v1"
	vrotemplate "github.com/tjjh89017/vrouter-operator/internal/template"
)

// VRouterBindingReconciler reconciles a VRouterBinding object.
type VRouterBindingReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=vrouter.kojuro.date,resources=vrouterbindings,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=vrouter.kojuro.date,resources=vrouterbindings/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=vrouter.kojuro.date,resources=vrouterbindings/finalizers,verbs=update
// +kubebuilder:rbac:groups=vrouter.kojuro.date,resources=vroutertemplates,verbs=get;list;watch
// +kubebuilder:rbac:groups=vrouter.kojuro.date,resources=vroutertargets,verbs=get;list;watch
// +kubebuilder:rbac:groups=vrouter.kojuro.date,resources=vrouterconfigs,verbs=get;list;watch;create;update;patch;delete

func (r *VRouterBindingReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var binding vrouterv1.VRouterBinding
	if err := r.Get(ctx, req.NamespacedName, &binding); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !binding.DeletionTimestamp.IsZero() {
		return r.onDelete(ctx, req, &binding)
	}

	if !controllerutil.ContainsFinalizer(&binding, vrouterv1.FinalizerName) {
		controllerutil.AddFinalizer(&binding, vrouterv1.FinalizerName)
		if err := r.Update(ctx, &binding); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	return r.onChange(ctx, req, &binding)
}

func (r *VRouterBindingReconciler) onDelete(ctx context.Context, _ ctrl.Request, binding *vrouterv1.VRouterBinding) (ctrl.Result, error) {
	controllerutil.RemoveFinalizer(binding, vrouterv1.FinalizerName)
	return ctrl.Result{}, r.Update(ctx, binding)
}

// setReadyCondition patches the Ready condition on the binding status.
func (r *VRouterBindingReconciler) setReadyCondition(ctx context.Context, binding *vrouterv1.VRouterBinding, status metav1.ConditionStatus, reason, message string) {
	patch := client.MergeFrom(binding.DeepCopy())
	meta.SetStatusCondition(&binding.Status.Conditions, metav1.Condition{
		Type:               vrouterv1.ConditionReady,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: binding.Generation,
	})
	if err := r.Status().Patch(ctx, binding, patch); err != nil {
		logf.FromContext(ctx).Error(err, "failed to patch binding status")
	}
}

func (r *VRouterBindingReconciler) onChange(ctx context.Context, _ ctrl.Request, binding *vrouterv1.VRouterBinding) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// Step 1: get template.
	var tmpl vrouterv1.VRouterTemplate
	if err := r.Get(ctx, client.ObjectKey{
		Name:      binding.Spec.TemplateRef.Name,
		Namespace: binding.Namespace,
	}, &tmpl); err != nil {
		err = fmt.Errorf("get template %q: %w", binding.Spec.TemplateRef.Name, err)
		r.setReadyCondition(ctx, binding, metav1.ConditionFalse, "ReconcileError", err.Error())
		return ctrl.Result{}, err
	}

	// Step 2: resolve targets, render and reconcile one VRouterConfig per target.
	desired := make(map[string]bool, len(binding.Spec.TargetRefs))
	for _, ref := range binding.Spec.TargetRefs {
		var target vrouterv1.VRouterTarget
		if err := r.Get(ctx, client.ObjectKey{
			Name:      ref.Name,
			Namespace: binding.Namespace,
		}, &target); err != nil {
			err = fmt.Errorf("get target %q: %w", ref.Name, err)
			r.setReadyCondition(ctx, binding, metav1.ConditionFalse, "ReconcileError", err.Error())
			return ctrl.Result{}, err
		}

		// Step 3: merge params — binding (base) overridden by target.
		params, err := vrotemplate.MergeParams(binding.Spec.Params, target.Spec.Params)
		if err != nil {
			err = fmt.Errorf("merge params for target %q: %w", ref.Name, err)
			r.setReadyCondition(ctx, binding, metav1.ConditionFalse, "ReconcileError", err.Error())
			return ctrl.Result{}, err
		}

		// Step 4: render config and commands.
		renderedConfig, err := vrotemplate.Render(tmpl.Spec.Config, params)
		if err != nil {
			err = fmt.Errorf("render config for target %q: %w", ref.Name, err)
			r.setReadyCondition(ctx, binding, metav1.ConditionFalse, "ReconcileError", err.Error())
			return ctrl.Result{}, err
		}
		renderedCommands, err := vrotemplate.Render(tmpl.Spec.Commands, params)
		if err != nil {
			err = fmt.Errorf("render commands for target %q: %w", ref.Name, err)
			r.setReadyCondition(ctx, binding, metav1.ConditionFalse, "ReconcileError", err.Error())
			return ctrl.Result{}, err
		}

		// Step 5: create/update VRouterConfig.
		cfgName := fmt.Sprintf("%s.%s", binding.Name, ref.Name)
		desired[cfgName] = true

		cfg := &vrouterv1.VRouterConfig{
			ObjectMeta: metav1.ObjectMeta{
				Name:      cfgName,
				Namespace: binding.Namespace,
			},
		}
		_, err = controllerutil.CreateOrUpdate(ctx, r.Client, cfg, func() error {
			if cfg.Labels == nil {
				cfg.Labels = map[string]string{}
			}
			cfg.Labels[vrouterv1.LabelBinding] = binding.Name
			cfg.Labels[vrouterv1.LabelTarget] = ref.Name
			cfg.Spec = vrouterv1.VRouterConfigSpec{
				TargetRef: vrouterv1.NameRef{Name: ref.Name},
				Save:      binding.Spec.Save,
				Config:    renderedConfig,
				Commands:  renderedCommands,
			}
			return controllerutil.SetControllerReference(binding, cfg, r.Scheme)
		})
		if err != nil {
			err = fmt.Errorf("sync VRouterConfig %q: %w", cfgName, err)
			r.setReadyCondition(ctx, binding, metav1.ConditionFalse, "ReconcileError", err.Error())
			return ctrl.Result{}, err
		}
		log.Info("synced VRouterConfig", "name", cfgName)
	}

	// Step 6: orphan cleanup — delete configs owned by this binding but no longer desired.
	var existing vrouterv1.VRouterConfigList
	if err := r.List(ctx, &existing,
		client.InNamespace(binding.Namespace),
		client.MatchingLabels{vrouterv1.LabelBinding: binding.Name},
	); err != nil {
		err = fmt.Errorf("list VRouterConfigs: %w", err)
		r.setReadyCondition(ctx, binding, metav1.ConditionFalse, "ReconcileError", err.Error())
		return ctrl.Result{}, err
	}
	for i := range existing.Items {
		if !desired[existing.Items[i].Name] {
			if err := r.Delete(ctx, &existing.Items[i]); err != nil {
				err = fmt.Errorf("delete orphan VRouterConfig %q: %w", existing.Items[i].Name, err)
				r.setReadyCondition(ctx, binding, metav1.ConditionFalse, "ReconcileError", err.Error())
				return ctrl.Result{}, err
			}
			log.Info("deleted orphan VRouterConfig", "name", existing.Items[i].Name)
		}
	}

	r.setReadyCondition(ctx, binding, metav1.ConditionTrue, "ReconcileSucceeded", "All VRouterConfigs reconciled successfully.")
	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *VRouterBindingReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&vrouterv1.VRouterBinding{}).
		// Re-reconcile bindings when the template they reference changes.
		Watches(&vrouterv1.VRouterTemplate{}, handler.EnqueueRequestsFromMapFunc(r.bindingsForTemplate)).
		// Re-reconcile bindings when a target they reference changes.
		Watches(&vrouterv1.VRouterTarget{}, handler.EnqueueRequestsFromMapFunc(r.bindingsForTarget)).
		Named("vrouterbinding").
		Complete(r)
}

// bindingsForTemplate returns reconcile requests for all VRouterBindings that
// reference the given VRouterTemplate.
func (r *VRouterBindingReconciler) bindingsForTemplate(ctx context.Context, obj client.Object) []reconcile.Request {
	var list vrouterv1.VRouterBindingList
	if err := r.List(ctx, &list, client.InNamespace(obj.GetNamespace())); err != nil {
		return nil
	}
	var reqs []reconcile.Request
	for i := range list.Items {
		if list.Items[i].Spec.TemplateRef.Name == obj.GetName() {
			reqs = append(reqs, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&list.Items[i])})
		}
	}
	return reqs
}

// bindingsForTarget returns reconcile requests for all VRouterBindings that
// reference the given VRouterTarget.
func (r *VRouterBindingReconciler) bindingsForTarget(ctx context.Context, obj client.Object) []reconcile.Request {
	var list vrouterv1.VRouterBindingList
	if err := r.List(ctx, &list, client.InNamespace(obj.GetNamespace())); err != nil {
		return nil
	}
	var reqs []reconcile.Request
	for i := range list.Items {
		for _, ref := range list.Items[i].Spec.TargetRefs {
			if ref.Name == obj.GetName() {
				reqs = append(reqs, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&list.Items[i])})
				break
			}
		}
	}
	return reqs
}
