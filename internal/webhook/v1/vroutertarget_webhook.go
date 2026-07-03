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

package v1

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	vrouterv1 "github.com/tjjh89017/vrouter-operator/api/v1"
)

// nolint:unused
// log is for logging in this package.
var vroutertargetlog = logf.Log.WithName("vroutertarget-resource")

// SetupVRouterTargetWebhookWithManager registers the webhook for VRouterTarget in the manager.
func SetupVRouterTargetWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).For(&vrouterv1.VRouterTarget{}).
		WithValidator(&VRouterTargetCustomValidator{Client: mgr.GetClient()}).
		WithDefaulter(&VRouterTargetCustomDefaulter{}).
		Complete()
}

// TODO(user): EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!

// +kubebuilder:webhook:path=/mutate-vrouter-kojuro-date-v1-vroutertarget,mutating=true,failurePolicy=fail,sideEffects=None,groups=vrouter.kojuro.date,resources=vroutertargets,verbs=create;update,versions=v1,name=mvroutertarget-v1.kb.io,admissionReviewVersions=v1

// VRouterTargetCustomDefaulter struct is responsible for setting default values on the custom resource of the
// Kind VRouterTarget when those are created or updated.
//
// NOTE: The +kubebuilder:object:generate=false marker prevents controller-gen from generating DeepCopy methods,
// as it is used only for temporary operations and does not need to be deeply copied.
type VRouterTargetCustomDefaulter struct {
	// TODO(user): Add more fields as needed for defaulting
}

var _ webhook.CustomDefaulter = &VRouterTargetCustomDefaulter{}

// Default implements webhook.CustomDefaulter so a webhook will be registered for the Kind VRouterTarget.
func (d *VRouterTargetCustomDefaulter) Default(_ context.Context, obj runtime.Object) error {
	vroutertarget, ok := obj.(*vrouterv1.VRouterTarget)

	if !ok {
		return fmt.Errorf("expected an VRouterTarget object but got %T", obj)
	}
	vroutertargetlog.Info("Defaulting for VRouterTarget", "name", vroutertarget.GetName())

	return nil
}

// NOTE: The 'path' attribute must follow a specific pattern and should not be modified directly here.
// Modifying the path for an invalid path can cause API server errors; failing to locate the webhook.
// +kubebuilder:webhook:path=/validate-vrouter-kojuro-date-v1-vroutertarget,mutating=false,failurePolicy=fail,sideEffects=None,groups=vrouter.kojuro.date,resources=vroutertargets,verbs=create;update;delete,versions=v1,name=vvroutertarget-v1.kb.io,admissionReviewVersions=v1

// VRouterTargetCustomValidator struct is responsible for validating the VRouterTarget resource
// when it is created, updated, or deleted.
//
// NOTE: The +kubebuilder:object:generate=false marker prevents controller-gen from generating DeepCopy methods,
// as this struct is used only for temporary operations and does not need to be deeply copied.
type VRouterTargetCustomValidator struct {
	client.Client
}

var _ webhook.CustomValidator = &VRouterTargetCustomValidator{}

// ValidateCreate implements webhook.CustomValidator so a webhook will be registered for the type VRouterTarget.
func (v *VRouterTargetCustomValidator) ValidateCreate(_ context.Context, obj runtime.Object) (admission.Warnings, error) {
	vroutertarget, ok := obj.(*vrouterv1.VRouterTarget)
	if !ok {
		return nil, fmt.Errorf("expected a VRouterTarget object but got %T", obj)
	}
	vroutertargetlog.Info("Validation for VRouterTarget upon creation", "name", vroutertarget.GetName())

	return nil, validateProviderConfig(vroutertarget.Spec.Provider, vroutertarget.Namespace)
}

// ValidateUpdate implements webhook.CustomValidator so a webhook will be registered for the type VRouterTarget.
func (v *VRouterTargetCustomValidator) ValidateUpdate(_ context.Context, oldObj, newObj runtime.Object) (admission.Warnings, error) {
	vroutertarget, ok := newObj.(*vrouterv1.VRouterTarget)
	if !ok {
		return nil, fmt.Errorf("expected a VRouterTarget object for the newObj but got %T", newObj)
	}
	vroutertargetlog.Info("Validation for VRouterTarget upon update", "name", vroutertarget.GetName())

	// Skip validation while the object is being deleted, consistent with the
	// deletion-path skip used by the binding and config webhooks: a resource that
	// was valid when created (e.g. before this check existed) must not be blocked
	// from having its finalizer removed.
	if !vroutertarget.GetDeletionTimestamp().IsZero() {
		return nil, nil
	}
	return nil, validateProviderConfig(vroutertarget.Spec.Provider, vroutertarget.Namespace)
}

// ValidateDelete implements webhook.CustomValidator so a webhook will be registered for the type VRouterTarget.
func (v *VRouterTargetCustomValidator) ValidateDelete(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	vroutertarget, ok := obj.(*vrouterv1.VRouterTarget)
	if !ok {
		return nil, fmt.Errorf("expected a VRouterTarget object but got %T", obj)
	}
	vroutertargetlog.Info("Validation for VRouterTarget upon deletion", "name", vroutertarget.GetName())

	return nil, checkTargetNotReferenced(ctx, v.Client, vroutertarget)
}

// checkTargetNotReferenced rejects deletion of a VRouterTarget that is still
// referenced by a VRouterBinding (spec.targetRefs) or a VRouterConfig
// (spec.targetRef). References are resolved with the same-namespace-only
// policy enforced elsewhere in this package, so only objects in the target's
// own namespace can reference it; it is enough to list within that namespace.
func checkTargetNotReferenced(ctx context.Context, cl client.Client, target *vrouterv1.VRouterTarget) error {
	var bindingList vrouterv1.VRouterBindingList
	if err := cl.List(ctx, &bindingList, client.InNamespace(target.Namespace)); err != nil {
		return fmt.Errorf("list VRouterBindings: %w", err)
	}

	var referencingBindings []string
	for _, binding := range bindingList.Items {
		for _, ref := range binding.Spec.TargetRefs {
			if ref.Name == target.Name && vrouterv1.ResolveNamespace(ref, binding.Namespace) == target.Namespace {
				referencingBindings = append(referencingBindings, binding.Name)
				break
			}
		}
	}

	var configList vrouterv1.VRouterConfigList
	if err := cl.List(ctx, &configList, client.InNamespace(target.Namespace)); err != nil {
		return fmt.Errorf("list VRouterConfigs: %w", err)
	}

	var referencingConfigs []string
	for _, cfg := range configList.Items {
		ref := cfg.Spec.TargetRef
		if ref.Name == target.Name && vrouterv1.ResolveNamespace(ref, cfg.Namespace) == target.Namespace {
			referencingConfigs = append(referencingConfigs, cfg.Name)
		}
	}

	total := len(referencingBindings) + len(referencingConfigs)
	if total == 0 {
		return nil
	}

	// Name at least one referencing object of each kind found, and note how many more exist.
	var first string
	var kind string
	switch {
	case len(referencingBindings) > 0:
		first = referencingBindings[0]
		kind = "VRouterBinding"
	default:
		first = referencingConfigs[0]
		kind = "VRouterConfig"
	}

	if total == 1 {
		return fmt.Errorf("cannot delete VRouterTarget %q: still referenced by %s %q", target.Name, kind, first)
	}
	return fmt.Errorf("cannot delete VRouterTarget %q: still referenced by %s %q (and %d others)", target.Name, kind, first, total-1)
}
