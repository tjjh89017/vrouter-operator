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
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	vrouterv1 "github.com/tjjh89017/vrouter-operator/api/v1"
)

// nolint:unused
// log is for logging in this package.
var vrouterbindinglog = logf.Log.WithName("vrouterbinding-resource")

// SetupVRouterBindingWebhookWithManager registers the webhook for VRouterBinding in the manager.
func SetupVRouterBindingWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).For(&vrouterv1.VRouterBinding{}).
		WithValidator(&VRouterBindingCustomValidator{}).
		WithDefaulter(&VRouterBindingCustomDefaulter{}).
		Complete()
}

// TODO(user): EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!

// +kubebuilder:webhook:path=/mutate-vrouter-kojuro-date-v1-vrouterbinding,mutating=true,failurePolicy=fail,sideEffects=None,groups=vrouter.kojuro.date,resources=vrouterbindings,verbs=create;update,versions=v1,name=mvrouterbinding-v1.kb.io,admissionReviewVersions=v1

// VRouterBindingCustomDefaulter struct is responsible for setting default values on the custom resource of the
// Kind VRouterBinding when those are created or updated.
//
// NOTE: The +kubebuilder:object:generate=false marker prevents controller-gen from generating DeepCopy methods,
// as it is used only for temporary operations and does not need to be deeply copied.
type VRouterBindingCustomDefaulter struct {
	// TODO(user): Add more fields as needed for defaulting
}

var _ webhook.CustomDefaulter = &VRouterBindingCustomDefaulter{}

// Default implements webhook.CustomDefaulter so a webhook will be registered for the Kind VRouterBinding.
func (d *VRouterBindingCustomDefaulter) Default(_ context.Context, obj runtime.Object) error {
	vrouterbinding, ok := obj.(*vrouterv1.VRouterBinding)

	if !ok {
		return fmt.Errorf("expected an VRouterBinding object but got %T", obj)
	}
	vrouterbindinglog.Info("Defaulting for VRouterBinding", "name", vrouterbinding.GetName())

	// TODO(user): fill in your defaulting logic.

	return nil
}

// TODO(user): change verbs to "verbs=create;update;delete" if you want to enable deletion validation.
// NOTE: The 'path' attribute must follow a specific pattern and should not be modified directly here.
// Modifying the path for an invalid path can cause API server errors; failing to locate the webhook.
// +kubebuilder:webhook:path=/validate-vrouter-kojuro-date-v1-vrouterbinding,mutating=false,failurePolicy=fail,sideEffects=None,groups=vrouter.kojuro.date,resources=vrouterbindings,verbs=create;update,versions=v1,name=vvrouterbinding-v1.kb.io,admissionReviewVersions=v1

// VRouterBindingCustomValidator struct is responsible for validating the VRouterBinding resource
// when it is created, updated, or deleted.
//
// NOTE: The +kubebuilder:object:generate=false marker prevents controller-gen from generating DeepCopy methods,
// as this struct is used only for temporary operations and does not need to be deeply copied.
type VRouterBindingCustomValidator struct {
	// TODO(user): Add more fields as needed for validation
}

var _ webhook.CustomValidator = &VRouterBindingCustomValidator{}

// ValidateCreate implements webhook.CustomValidator so a webhook will be registered for the type VRouterBinding.
func (v *VRouterBindingCustomValidator) ValidateCreate(_ context.Context, obj runtime.Object) (admission.Warnings, error) {
	vrouterbinding, ok := obj.(*vrouterv1.VRouterBinding)
	if !ok {
		return nil, fmt.Errorf("expected a VRouterBinding object but got %T", obj)
	}
	vrouterbindinglog.Info("Validation for VRouterBinding upon creation", "name", vrouterbinding.GetName())

	// TODO(user): fill in your validation logic upon object creation.

	return nil, nil
}

// ValidateUpdate implements webhook.CustomValidator so a webhook will be registered for the type VRouterBinding.
func (v *VRouterBindingCustomValidator) ValidateUpdate(_ context.Context, oldObj, newObj runtime.Object) (admission.Warnings, error) {
	vrouterbinding, ok := newObj.(*vrouterv1.VRouterBinding)
	if !ok {
		return nil, fmt.Errorf("expected a VRouterBinding object for the newObj but got %T", newObj)
	}
	vrouterbindinglog.Info("Validation for VRouterBinding upon update", "name", vrouterbinding.GetName())

	// TODO(user): fill in your validation logic upon object update.

	return nil, nil
}

// ValidateDelete implements webhook.CustomValidator so a webhook will be registered for the type VRouterBinding.
func (v *VRouterBindingCustomValidator) ValidateDelete(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	vrouterbinding, ok := obj.(*vrouterv1.VRouterBinding)
	if !ok {
		return nil, fmt.Errorf("expected a VRouterBinding object but got %T", obj)
	}
	vrouterbindinglog.Info("Validation for VRouterBinding upon deletion", "name", vrouterbinding.GetName())

	// TODO(user): fill in your validation logic upon object deletion.

	return nil, nil
}
