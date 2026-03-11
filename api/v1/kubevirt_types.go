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

const ProviderKubeVirt ProviderType = "kubevirt"

// KubeVirtConfig holds KubeVirt provider configuration and router identification.
type KubeVirtConfig struct {
	// Name of the VirtualMachine / VirtualMachineInstance.
	Name string `json:"name"`
	// +optional
	Namespace string `json:"namespace,omitempty"`
	// +optional
	Kubeconfig *KubeconfigRef `json:"kubeconfig,omitempty"`
}

// KubeconfigRef references a kubeconfig stored in a Secret.
type KubeconfigRef struct {
	SecretRef SecretKeyRef `json:"secretRef"`
}
