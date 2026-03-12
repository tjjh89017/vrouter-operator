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
var proxmoxclusterlog = logf.Log.WithName("proxmoxcluster-resource")

// SetupProxmoxClusterWebhookWithManager registers the webhook for ProxmoxCluster in the manager.
func SetupProxmoxClusterWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).For(&vrouterv1.ProxmoxCluster{}).
		WithValidator(&ProxmoxClusterCustomValidator{}).
		WithDefaulter(&ProxmoxClusterCustomDefaulter{}).
		Complete()
}

// TODO(user): EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!

// +kubebuilder:webhook:path=/mutate-vrouter-kojuro-date-v1-proxmoxcluster,mutating=true,failurePolicy=fail,sideEffects=None,groups=vrouter.kojuro.date,resources=proxmoxclusters,verbs=create;update,versions=v1,name=mproxmoxcluster-v1.kb.io,admissionReviewVersions=v1

// ProxmoxClusterCustomDefaulter struct is responsible for setting default values on the custom resource of the
// Kind ProxmoxCluster when those are created or updated.
//
// NOTE: The +kubebuilder:object:generate=false marker prevents controller-gen from generating DeepCopy methods,
// as it is used only for temporary operations and does not need to be deeply copied.
type ProxmoxClusterCustomDefaulter struct {
	// TODO(user): Add more fields as needed for defaulting
}

var _ webhook.CustomDefaulter = &ProxmoxClusterCustomDefaulter{}

// Default implements webhook.CustomDefaulter so a webhook will be registered for the Kind ProxmoxCluster.
func (d *ProxmoxClusterCustomDefaulter) Default(_ context.Context, obj runtime.Object) error {
	proxmoxcluster, ok := obj.(*vrouterv1.ProxmoxCluster)

	if !ok {
		return fmt.Errorf("expected an ProxmoxCluster object but got %T", obj)
	}
	proxmoxclusterlog.Info("Defaulting for ProxmoxCluster", "name", proxmoxcluster.GetName())

	// TODO(user): fill in your defaulting logic.

	return nil
}

// TODO(user): change verbs to "verbs=create;update;delete" if you want to enable deletion validation.
// NOTE: The 'path' attribute must follow a specific pattern and should not be modified directly here.
// Modifying the path for an invalid path can cause API server errors; failing to locate the webhook.
// +kubebuilder:webhook:path=/validate-vrouter-kojuro-date-v1-proxmoxcluster,mutating=false,failurePolicy=fail,sideEffects=None,groups=vrouter.kojuro.date,resources=proxmoxclusters,verbs=create;update,versions=v1,name=vproxmoxcluster-v1.kb.io,admissionReviewVersions=v1

// ProxmoxClusterCustomValidator struct is responsible for validating the ProxmoxCluster resource
// when it is created, updated, or deleted.
//
// NOTE: The +kubebuilder:object:generate=false marker prevents controller-gen from generating DeepCopy methods,
// as this struct is used only for temporary operations and does not need to be deeply copied.
type ProxmoxClusterCustomValidator struct {
	// TODO(user): Add more fields as needed for validation
}

var _ webhook.CustomValidator = &ProxmoxClusterCustomValidator{}

// ValidateCreate implements webhook.CustomValidator so a webhook will be registered for the type ProxmoxCluster.
func (v *ProxmoxClusterCustomValidator) ValidateCreate(_ context.Context, obj runtime.Object) (admission.Warnings, error) {
	proxmoxcluster, ok := obj.(*vrouterv1.ProxmoxCluster)
	if !ok {
		return nil, fmt.Errorf("expected a ProxmoxCluster object but got %T", obj)
	}
	proxmoxclusterlog.Info("Validation for ProxmoxCluster upon creation", "name", proxmoxcluster.GetName())

	// TODO(user): fill in your validation logic upon object creation.

	return nil, nil
}

// ValidateUpdate implements webhook.CustomValidator so a webhook will be registered for the type ProxmoxCluster.
func (v *ProxmoxClusterCustomValidator) ValidateUpdate(_ context.Context, oldObj, newObj runtime.Object) (admission.Warnings, error) {
	proxmoxcluster, ok := newObj.(*vrouterv1.ProxmoxCluster)
	if !ok {
		return nil, fmt.Errorf("expected a ProxmoxCluster object for the newObj but got %T", newObj)
	}
	proxmoxclusterlog.Info("Validation for ProxmoxCluster upon update", "name", proxmoxcluster.GetName())

	// TODO(user): fill in your validation logic upon object update.

	return nil, nil
}

// ValidateDelete implements webhook.CustomValidator so a webhook will be registered for the type ProxmoxCluster.
func (v *ProxmoxClusterCustomValidator) ValidateDelete(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	proxmoxcluster, ok := obj.(*vrouterv1.ProxmoxCluster)
	if !ok {
		return nil, fmt.Errorf("expected a ProxmoxCluster object but got %T", obj)
	}
	proxmoxclusterlog.Info("Validation for ProxmoxCluster upon deletion", "name", proxmoxcluster.GetName())

	// TODO(user): fill in your validation logic upon object deletion.

	return nil, nil
}
