/*
Copyright 2025.

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
	"os"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	kubevirtv1 "kubevirt.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"github.com/tjjh89017/vrouter-operator/constants"
)

// nolint:unused
// log is for logging in this package.
var podlog = logf.Log.WithName("pod-resource")

// SetupPodWebhookWithManager registers the webhook for Pod in the manager.
func SetupPodWebhookWithManager(mgr ctrl.Manager) error {
	sidecarContainerImage := os.Getenv("SIDECAR_CONTAINER_IMAGE")
	if sidecarContainerImage == "" {
		sidecarContainerImage = constants.SidecarContainerImage
	}
	podlog.Info("Setting up Pod webhook", "sidecarContainerImage", sidecarContainerImage)

	return ctrl.NewWebhookManagedBy(mgr).For(&corev1.Pod{}).
		WithValidator(&PodCustomValidator{}).
		WithDefaulter(&PodCustomDefaulter{
			Client:                mgr.GetClient(),
			Scheme:                mgr.GetScheme(),
			SidecarContainerImage: sidecarContainerImage,
		}).
		Complete()
}

// TODO(user): EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!

// +kubebuilder:webhook:path=/mutate--v1-pod,mutating=true,failurePolicy=fail,sideEffects=None,groups="",resources=pods,verbs=create,versions=v1,name=mpod-v1.kb.io,admissionReviewVersions=v1

// +kubebuilder:rbac:groups=kubevirt.io,resources=virtualmachines,verbs=get;list;watch
// +kubebuilder:rbac:groups=kubevirt.io,resources=virtualmachines/status,verbs=get

// PodCustomDefaulter struct is responsible for setting default values on the custom resource of the
// Kind Pod when those are created or updated.
//
// NOTE: The +kubebuilder:object:generate=false marker prevents controller-gen from generating DeepCopy methods,
// as it is used only for temporary operations and does not need to be deeply copied.
type PodCustomDefaulter struct {
	client.Client
	Scheme *runtime.Scheme

	SidecarContainerImage string
}

var _ webhook.CustomDefaulter = &PodCustomDefaulter{}

// Default implements webhook.CustomDefaulter so a webhook will be registered for the Kind Pod.
func (d *PodCustomDefaulter) Default(ctx context.Context, obj runtime.Object) error {
	pod, ok := obj.(*corev1.Pod)

	if !ok {
		return fmt.Errorf("expected an Pod object but got %T", obj)
	}

	if metav1.HasAnnotation(pod.ObjectMeta, constants.VRouterConfigAnnotation) {
		podlog.Info("Pod already has VRouterConfigAnnotation, skipping defaulting", "name", pod.GetName())
		return nil // No need to default if the annotation is already present
	}

	podlog.Info("Defaulting for Pod", "name", pod.GetName())

	// Check if the Pod is a KubeVirt VirtualMachineInstance
	if !metav1.HasLabel(pod.ObjectMeta, kubevirtv1.VirtualMachineNameLabel) {
		podlog.Info("Pod is not a KubeVirt VirtualMachineInstance, skipping defaulting")
		return nil
	}

	podlog.Info("Detected a KubeVirt VirtualMachineInstance", "name", pod.GetName())
	namespace := pod.GetNamespace()
	vmName := pod.GetLabels()[kubevirtv1.VirtualMachineNameLabel]

	podlog.Info("Retrieving VirtualMachine", "name", vmName, "namespace", namespace)

	vm := &kubevirtv1.VirtualMachine{}
	if err := d.Get(ctx, client.ObjectKey{Namespace: namespace, Name: vmName}, vm); err != nil {
		podlog.Error(err, "Failed to get VirtualMachine", "name", vmName, "namespace", namespace)
		return err
	}

	// Inject sidecar container if Annotations are present
	if metav1.HasAnnotation(vm.ObjectMeta, constants.VRouterConfigAnnotation) {
		podlog.Info("Injecting sidecar container into Pod", "name", pod.GetName(), "sidecarImage", constants.SidecarContainerImage)
		for _, container := range pod.Spec.Containers {
			if container.Name == constants.SidecarContainer {
				podlog.Info("Sidecar container already exists in Pod", "name", pod.GetName())
				return nil // Sidecar already exists, no need to add it again
			}
		}

		index := -1
		for i, container := range pod.Spec.Containers {
			if container.Name == "compute" {
				index = i
				break
			}
		}

		if index == -1 {
			podlog.Info("No compute container found")
			return fmt.Errorf("no compute container found in Pod %s", pod.GetName())
		}

		pod.Spec.AutomountServiceAccountToken = ptr.To(true)
		pod.Spec.ServiceAccountName = "vrouter-operator-controller-manager" // TODO fix it with proper way, or mount service account token with volume
		pod.Annotations[constants.VRouterConfigAnnotation] = vm.Annotations[constants.VRouterConfigAnnotation]
		pod.Spec.Containers = append(pod.Spec.Containers, corev1.Container{
			Name:  constants.SidecarContainer,
			Image: d.SidecarContainerImage,
			ImagePullPolicy: corev1.PullIfNotPresent,
			//RestartPolicy: ptr.To(corev1.ContainerRestartPolicyAlways),
			Env: append(
				pod.Spec.Containers[index].Env,
				corev1.EnvVar{
					Name: "POD_NAMESPACE",
					ValueFrom: &corev1.EnvVarSource{
						FieldRef: &corev1.ObjectFieldSelector{
							FieldPath: "metadata.namespace",
						},
					},
				}, corev1.EnvVar{
					Name:  "LIBVIRT_DEFAULT_URI",
					Value: "qemu:///session",
				},
			),
			SecurityContext: &corev1.SecurityContext{
				AllowPrivilegeEscalation: ptr.To(false),
				Privileged:               ptr.To(false),
				RunAsUser:                ptr.To(int64(107)), // TODO 107 is the user ID for the qemu user
				RunAsNonRoot:             ptr.To(true),
				RunAsGroup:               ptr.To(int64(107)), // TODO 107 is the group ID for the qemu group
			},
			VolumeMounts: pod.Spec.Containers[index].VolumeMounts,
		})
	}

	return nil
}

// TODO(user): change verbs to "verbs=create;update;delete" if you want to enable deletion validation.
// NOTE: The 'path' attribute must follow a specific pattern and should not be modified directly here.
// Modifying the path for an invalid path can cause API server errors; failing to locate the webhook.
// +kubebuilder:webhook:path=/validate--v1-pod,mutating=false,failurePolicy=fail,sideEffects=None,groups="",resources=pods,verbs=create;update,versions=v1,name=vpod-v1.kb.io,admissionReviewVersions=v1

// PodCustomValidator struct is responsible for validating the Pod resource
// when it is created, updated, or deleted.
//
// NOTE: The +kubebuilder:object:generate=false marker prevents controller-gen from generating DeepCopy methods,
// as this struct is used only for temporary operations and does not need to be deeply copied.
type PodCustomValidator struct {
	// TODO(user): Add more fields as needed for validation
}

var _ webhook.CustomValidator = &PodCustomValidator{}

// ValidateCreate implements webhook.CustomValidator so a webhook will be registered for the type Pod.
func (v *PodCustomValidator) ValidateCreate(_ context.Context, obj runtime.Object) (admission.Warnings, error) {
	pod, ok := obj.(*corev1.Pod)
	if !ok {
		return nil, fmt.Errorf("expected a Pod object but got %T", obj)
	}
	podlog.Info("Validation for Pod upon creation", "name", pod.GetName())

	// TODO(user): fill in your validation logic upon object creation.

	return nil, nil
}

// ValidateUpdate implements webhook.CustomValidator so a webhook will be registered for the type Pod.
func (v *PodCustomValidator) ValidateUpdate(_ context.Context, oldObj, newObj runtime.Object) (admission.Warnings, error) {
	pod, ok := newObj.(*corev1.Pod)
	if !ok {
		return nil, fmt.Errorf("expected a Pod object for the newObj but got %T", newObj)
	}
	podlog.Info("Validation for Pod upon update", "name", pod.GetName())

	// TODO(user): fill in your validation logic upon object update.

	return nil, nil
}

// ValidateDelete implements webhook.CustomValidator so a webhook will be registered for the type Pod.
func (v *PodCustomValidator) ValidateDelete(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	pod, ok := obj.(*corev1.Pod)
	if !ok {
		return nil, fmt.Errorf("expected a Pod object but got %T", obj)
	}
	podlog.Info("Validation for Pod upon deletion", "name", pod.GetName())

	// TODO(user): fill in your validation logic upon object deletion.

	return nil, nil
}
