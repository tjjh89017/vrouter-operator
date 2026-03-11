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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

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

func (r *VRouterBindingReconciler) onChange(ctx context.Context, _ ctrl.Request, binding *vrouterv1.VRouterBinding) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// Step 1: get template.
	var tmpl vrouterv1.VRouterTemplate
	if err := r.Get(ctx, client.ObjectKey{
		Name:      binding.Spec.TemplateRef.Name,
		Namespace: binding.Namespace,
	}, &tmpl); err != nil {
		return ctrl.Result{}, fmt.Errorf("get template %q: %w", binding.Spec.TemplateRef.Name, err)
	}

	// Step 2: resolve targets, render and reconcile one VRouterConfig per target.
	desired := make(map[string]bool, len(binding.Spec.TargetRefs))
	for _, ref := range binding.Spec.TargetRefs {
		var target vrouterv1.VRouterTarget
		if err := r.Get(ctx, client.ObjectKey{
			Name:      ref.Name,
			Namespace: binding.Namespace,
		}, &target); err != nil {
			return ctrl.Result{}, fmt.Errorf("get target %q: %w", ref.Name, err)
		}

		// Step 3: merge params — binding (base) overridden by target.
		params, err := vrotemplate.MergeParams(binding.Spec.Params, target.Spec.Params)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("merge params for target %q: %w", ref.Name, err)
		}

		// Step 4: render config and commands.
		renderedConfig, err := vrotemplate.Render(tmpl.Spec.Config, params)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("render config for target %q: %w", ref.Name, err)
		}
		renderedCommands, err := vrotemplate.Render(tmpl.Spec.Commands, params)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("render commands for target %q: %w", ref.Name, err)
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
				Provider: target.Spec.Provider,
				Save:     binding.Spec.Save,
				Config:   renderedConfig,
				Commands: renderedCommands,
			}
			return controllerutil.SetControllerReference(binding, cfg, r.Scheme)
		})
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("reconcile VRouterConfig %q: %w", cfgName, err)
		}
		log.Info("reconciled VRouterConfig", "name", cfgName)
	}

	// Step 6: orphan cleanup — delete configs owned by this binding but no longer desired.
	var existing vrouterv1.VRouterConfigList
	if err := r.List(ctx, &existing,
		client.InNamespace(binding.Namespace),
		client.MatchingLabels{vrouterv1.LabelBinding: binding.Name},
	); err != nil {
		return ctrl.Result{}, fmt.Errorf("list VRouterConfigs: %w", err)
	}
	for i := range existing.Items {
		if !desired[existing.Items[i].Name] {
			if err := r.Delete(ctx, &existing.Items[i]); err != nil {
				return ctrl.Result{}, fmt.Errorf("delete orphan VRouterConfig %q: %w", existing.Items[i].Name, err)
			}
			log.Info("deleted orphan VRouterConfig", "name", existing.Items[i].Name)
		}
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *VRouterBindingReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&vrouterv1.VRouterBinding{}).
		Named("vrouterbinding").
		Complete(r)
}
