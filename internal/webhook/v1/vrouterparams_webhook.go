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
var vrouterparamslog = logf.Log.WithName("vrouterparams-resource")

// SetupVRouterParamsWebhookWithManager registers the webhook for VRouterParams in the manager.
func SetupVRouterParamsWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).For(&vrouterv1.VRouterParams{}).
		WithValidator(&VRouterParamsCustomValidator{Client: mgr.GetClient()}).
		WithDefaulter(&VRouterParamsCustomDefaulter{}).
		Complete()
}

// +kubebuilder:webhook:path=/mutate-vrouter-kojuro-date-v1-vrouterparams,mutating=true,failurePolicy=fail,sideEffects=None,groups=vrouter.kojuro.date,resources=vrouterparams,verbs=create;update,versions=v1,name=mvrouterparams-v1.kb.io,admissionReviewVersions=v1

// VRouterParamsCustomDefaulter struct is responsible for setting default values on the custom resource of the
// Kind VRouterParams when those are created or updated.
//
// NOTE: The +kubebuilder:object:generate=false marker prevents controller-gen from generating DeepCopy methods,
// as it is used only for temporary operations and does not need to be deeply copied.
type VRouterParamsCustomDefaulter struct{}

var _ webhook.CustomDefaulter = &VRouterParamsCustomDefaulter{}

// Default implements webhook.CustomDefaulter so a webhook will be registered for the Kind VRouterParams.
func (d *VRouterParamsCustomDefaulter) Default(_ context.Context, obj runtime.Object) error {
	vrouterparams, ok := obj.(*vrouterv1.VRouterParams)

	if !ok {
		return fmt.Errorf("expected an VRouterParams object but got %T", obj)
	}
	vrouterparamslog.Info("Defaulting for VRouterParams", "name", vrouterparams.GetName())

	return nil
}

// NOTE: The 'path' attribute must follow a specific pattern and should not be modified directly here.
// Modifying the path for an invalid path can cause API server errors; failing to locate the webhook.
// +kubebuilder:webhook:path=/validate-vrouter-kojuro-date-v1-vrouterparams,mutating=false,failurePolicy=fail,sideEffects=None,groups=vrouter.kojuro.date,resources=vrouterparams,verbs=create;update;delete,versions=v1,name=vvrouterparams-v1.kb.io,admissionReviewVersions=v1

// VRouterParamsCustomValidator struct is responsible for validating the VRouterParams resource
// when it is created, updated, or deleted.
//
// NOTE: The +kubebuilder:object:generate=false marker prevents controller-gen from generating DeepCopy methods,
// as this struct is used only for temporary operations and does not need to be deeply copied.
type VRouterParamsCustomValidator struct {
	client.Client
}

var _ webhook.CustomValidator = &VRouterParamsCustomValidator{}

// ValidateCreate implements webhook.CustomValidator so a webhook will be registered for the type VRouterParams.
func (v *VRouterParamsCustomValidator) ValidateCreate(_ context.Context, obj runtime.Object) (admission.Warnings, error) {
	vrouterparams, ok := obj.(*vrouterv1.VRouterParams)
	if !ok {
		return nil, fmt.Errorf("expected a VRouterParams object but got %T", obj)
	}
	vrouterparamslog.Info("Validation for VRouterParams upon creation", "name", vrouterparams.GetName())

	return nil, nil
}

// ValidateUpdate implements webhook.CustomValidator so a webhook will be registered for the type VRouterParams.
func (v *VRouterParamsCustomValidator) ValidateUpdate(_ context.Context, _, newObj runtime.Object) (admission.Warnings, error) {
	vrouterparams, ok := newObj.(*vrouterv1.VRouterParams)
	if !ok {
		return nil, fmt.Errorf("expected a VRouterParams object for the newObj but got %T", newObj)
	}
	vrouterparamslog.Info("Validation for VRouterParams upon update", "name", vrouterparams.GetName())

	return nil, nil
}

// ValidateDelete implements webhook.CustomValidator so a webhook will be registered for the type VRouterParams.
func (v *VRouterParamsCustomValidator) ValidateDelete(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	vrouterparams, ok := obj.(*vrouterv1.VRouterParams)
	if !ok {
		return nil, fmt.Errorf("expected a VRouterParams object but got %T", obj)
	}
	vrouterparamslog.Info("Validation for VRouterParams upon deletion", "name", vrouterparams.GetName())

	return nil, checkParamsNotReferenced(ctx, v.Client, vrouterparams)
}

// checkParamsNotReferenced rejects deletion of a VRouterParams that is still
// referenced by a VRouterBinding (spec.paramsRefs). References are resolved
// with the same-namespace-only policy enforced elsewhere in this package, so
// only bindings in the params' own namespace can reference it; it is enough
// to list within that namespace. Unlike VRouterTarget, no other CRD
// references a VRouterParams, so only bindings are checked.
func checkParamsNotReferenced(ctx context.Context, cl client.Client, params *vrouterv1.VRouterParams) error {
	var bindingList vrouterv1.VRouterBindingList
	if err := cl.List(ctx, &bindingList, client.InNamespace(params.Namespace)); err != nil {
		return fmt.Errorf("list VRouterBindings: %w", err)
	}

	var referencingBindings []string
	for _, binding := range bindingList.Items {
		for _, ref := range binding.Spec.ParamsRefs {
			if ref.Name == params.Name && vrouterv1.ResolveNamespace(ref, binding.Namespace) == params.Namespace {
				referencingBindings = append(referencingBindings, binding.Name)
				break
			}
		}
	}

	total := len(referencingBindings)
	if total == 0 {
		return nil
	}

	if total == 1 {
		return fmt.Errorf("cannot delete VRouterParams %q: still referenced by binding %q", params.Name, referencingBindings[0])
	}
	return fmt.Errorf("cannot delete VRouterParams %q: still referenced by binding %q (and %d others)", params.Name, referencingBindings[0], total-1)
}
