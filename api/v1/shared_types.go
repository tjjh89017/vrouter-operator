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

// NameRef is a reference to a resource by name.
type NameRef struct {
	Name string `json:"name"`
}

// NamespacedRef is a reference to a resource by name and namespace.
type NamespacedRef struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
}

// ProviderType specifies the virtualization backend.
// +kubebuilder:validation:Enum=kubevirt;proxmox
type ProviderType string

// ProviderConfig defines which virtualization backend to use and the target router.
type ProviderConfig struct {
	// +kubebuilder:default=kubevirt
	// +optional
	Type ProviderType `json:"type,omitempty"`
	// +optional
	KubeVirt *KubeVirtConfig `json:"kubevirt,omitempty"`
	// +optional
	Proxmox *ProxmoxConfig `json:"proxmox,omitempty"`
}

// SecretKeyRef references a specific key in a Secret.
type SecretKeyRef struct {
	Name string `json:"name"`
	Key  string `json:"key"`
}

// SecretReference references a Secret by name.
type SecretReference struct {
	Name string `json:"name"`
}
