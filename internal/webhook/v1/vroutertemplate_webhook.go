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
var vroutertemplatelog = logf.Log.WithName("vroutertemplate-resource")

// SetupVRouterTemplateWebhookWithManager registers the webhook for VRouterTemplate in the manager.
func SetupVRouterTemplateWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).For(&vrouterv1.VRouterTemplate{}).
		WithValidator(&VRouterTemplateCustomValidator{}).
		WithDefaulter(&VRouterTemplateCustomDefaulter{}).
		Complete()
}

// TODO(user): EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!

// +kubebuilder:webhook:path=/mutate-vrouter-kojuro-date-v1-vroutertemplate,mutating=true,failurePolicy=fail,sideEffects=None,groups=vrouter.kojuro.date,resources=vroutertemplates,verbs=create;update,versions=v1,name=mvroutertemplate-v1.kb.io,admissionReviewVersions=v1

// VRouterTemplateCustomDefaulter struct is responsible for setting default values on the custom resource of the
// Kind VRouterTemplate when those are created or updated.
//
// NOTE: The +kubebuilder:object:generate=false marker prevents controller-gen from generating DeepCopy methods,
// as it is used only for temporary operations and does not need to be deeply copied.
type VRouterTemplateCustomDefaulter struct {
	// TODO(user): Add more fields as needed for defaulting
}

var _ webhook.CustomDefaulter = &VRouterTemplateCustomDefaulter{}

// Default implements webhook.CustomDefaulter so a webhook will be registered for the Kind VRouterTemplate.
func (d *VRouterTemplateCustomDefaulter) Default(_ context.Context, obj runtime.Object) error {
	vroutertemplate, ok := obj.(*vrouterv1.VRouterTemplate)

	if !ok {
		return fmt.Errorf("expected an VRouterTemplate object but got %T", obj)
	}
	vroutertemplatelog.Info("Defaulting for VRouterTemplate", "name", vroutertemplate.GetName())

	return nil
}

// TODO(user): change verbs to "verbs=create;update;delete" if you want to enable deletion validation.
// NOTE: The 'path' attribute must follow a specific pattern and should not be modified directly here.
// Modifying the path for an invalid path can cause API server errors; failing to locate the webhook.
// +kubebuilder:webhook:path=/validate-vrouter-kojuro-date-v1-vroutertemplate,mutating=false,failurePolicy=fail,sideEffects=None,groups=vrouter.kojuro.date,resources=vroutertemplates,verbs=create;update,versions=v1,name=vvroutertemplate-v1.kb.io,admissionReviewVersions=v1

// VRouterTemplateCustomValidator struct is responsible for validating the VRouterTemplate resource
// when it is created, updated, or deleted.
//
// NOTE: The +kubebuilder:object:generate=false marker prevents controller-gen from generating DeepCopy methods,
// as this struct is used only for temporary operations and does not need to be deeply copied.
type VRouterTemplateCustomValidator struct {
	// TODO(user): Add more fields as needed for validation
}

var _ webhook.CustomValidator = &VRouterTemplateCustomValidator{}

// ValidateCreate implements webhook.CustomValidator so a webhook will be registered for the type VRouterTemplate.
func (v *VRouterTemplateCustomValidator) ValidateCreate(_ context.Context, obj runtime.Object) (admission.Warnings, error) {
	vroutertemplate, ok := obj.(*vrouterv1.VRouterTemplate)
	if !ok {
		return nil, fmt.Errorf("expected a VRouterTemplate object but got %T", obj)
	}
	vroutertemplatelog.Info("Validation for VRouterTemplate upon creation", "name", vroutertemplate.GetName())

	return nil, nil
}

// ValidateUpdate implements webhook.CustomValidator so a webhook will be registered for the type VRouterTemplate.
func (v *VRouterTemplateCustomValidator) ValidateUpdate(_ context.Context, oldObj, newObj runtime.Object) (admission.Warnings, error) {
	vroutertemplate, ok := newObj.(*vrouterv1.VRouterTemplate)
	if !ok {
		return nil, fmt.Errorf("expected a VRouterTemplate object for the newObj but got %T", newObj)
	}
	vroutertemplatelog.Info("Validation for VRouterTemplate upon update", "name", vroutertemplate.GetName())

	return nil, nil
}

// ValidateDelete implements webhook.CustomValidator so a webhook will be registered for the type VRouterTemplate.
func (v *VRouterTemplateCustomValidator) ValidateDelete(_ context.Context, obj runtime.Object) (admission.Warnings, error) {
	vroutertemplate, ok := obj.(*vrouterv1.VRouterTemplate)
	if !ok {
		return nil, fmt.Errorf("expected a VRouterTemplate object but got %T", obj)
	}
	vroutertemplatelog.Info("Validation for VRouterTemplate upon deletion", "name", vroutertemplate.GetName())

	return nil, nil
}
