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
var vroutertargetlog = logf.Log.WithName("vroutertarget-resource")

// SetupVRouterTargetWebhookWithManager registers the webhook for VRouterTarget in the manager.
func SetupVRouterTargetWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).For(&vrouterv1.VRouterTarget{}).
		WithValidator(&VRouterTargetCustomValidator{}).
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

// TODO(user): change verbs to "verbs=create;update;delete" if you want to enable deletion validation.
// NOTE: The 'path' attribute must follow a specific pattern and should not be modified directly here.
// Modifying the path for an invalid path can cause API server errors; failing to locate the webhook.
// +kubebuilder:webhook:path=/validate-vrouter-kojuro-date-v1-vroutertarget,mutating=false,failurePolicy=fail,sideEffects=None,groups=vrouter.kojuro.date,resources=vroutertargets,verbs=create;update,versions=v1,name=vvroutertarget-v1.kb.io,admissionReviewVersions=v1

// VRouterTargetCustomValidator struct is responsible for validating the VRouterTarget resource
// when it is created, updated, or deleted.
//
// NOTE: The +kubebuilder:object:generate=false marker prevents controller-gen from generating DeepCopy methods,
// as this struct is used only for temporary operations and does not need to be deeply copied.
type VRouterTargetCustomValidator struct {
	// TODO(user): Add more fields as needed for validation
}

var _ webhook.CustomValidator = &VRouterTargetCustomValidator{}

// ValidateCreate implements webhook.CustomValidator so a webhook will be registered for the type VRouterTarget.
func (v *VRouterTargetCustomValidator) ValidateCreate(_ context.Context, obj runtime.Object) (admission.Warnings, error) {
	vroutertarget, ok := obj.(*vrouterv1.VRouterTarget)
	if !ok {
		return nil, fmt.Errorf("expected a VRouterTarget object but got %T", obj)
	}
	vroutertargetlog.Info("Validation for VRouterTarget upon creation", "name", vroutertarget.GetName())

	return nil, validateProviderConfig(vroutertarget.Spec.Provider)
}

// ValidateUpdate implements webhook.CustomValidator so a webhook will be registered for the type VRouterTarget.
func (v *VRouterTargetCustomValidator) ValidateUpdate(_ context.Context, oldObj, newObj runtime.Object) (admission.Warnings, error) {
	vroutertarget, ok := newObj.(*vrouterv1.VRouterTarget)
	if !ok {
		return nil, fmt.Errorf("expected a VRouterTarget object for the newObj but got %T", newObj)
	}
	vroutertargetlog.Info("Validation for VRouterTarget upon update", "name", vroutertarget.GetName())

	return nil, validateProviderConfig(vroutertarget.Spec.Provider)
}

// ValidateDelete implements webhook.CustomValidator so a webhook will be registered for the type VRouterTarget.
func (v *VRouterTargetCustomValidator) ValidateDelete(_ context.Context, obj runtime.Object) (admission.Warnings, error) {
	vroutertarget, ok := obj.(*vrouterv1.VRouterTarget)
	if !ok {
		return nil, fmt.Errorf("expected a VRouterTarget object but got %T", obj)
	}
	vroutertargetlog.Info("Validation for VRouterTarget upon deletion", "name", vroutertarget.GetName())

	return nil, nil
}
