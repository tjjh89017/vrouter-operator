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
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// VRouterTargetSpec defines the desired state of VRouterTarget.
type VRouterTargetSpec struct {
	Provider ProviderConfig `json:"provider"`
	// +kubebuilder:pruning:PreserveUnknownFields
	// +kubebuilder:validation:Schemaless
	// +optional
	Params apiextensionsv1.JSON `json:"params,omitempty"`
}

// +kubebuilder:object:root=true

// VRouterTarget is the Schema for the vroutertargets API.
type VRouterTarget struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec VRouterTargetSpec `json:"spec,omitempty"`
}

// +kubebuilder:object:root=true

// VRouterTargetList contains a list of VRouterTarget.
type VRouterTargetList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []VRouterTarget `json:"items"`
}

func init() {
	SchemeBuilder.Register(&VRouterTarget{}, &VRouterTargetList{})
}
