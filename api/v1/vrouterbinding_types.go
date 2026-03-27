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

// VRouterBindingSpec defines the desired state of VRouterBinding.
type VRouterBindingSpec struct {
	// Deprecated: use TemplateRefs instead. If non-nil, prepended to TemplateRefs as highest priority.
	// +optional
	TemplateRef *NameRef `json:"templateRef,omitempty"`
	// TemplateRefs is an ordered list of templates to merge. Templates are applied in order;
	// later templates' config and commands are appended after earlier ones.
	// +optional
	TemplateRefs []NameRef `json:"templateRefs,omitempty"`
	TargetRefs   []NameRef `json:"targetRefs"`
	// +kubebuilder:default=true
	Save bool `json:"save,omitempty"`
	// +kubebuilder:pruning:PreserveUnknownFields
	// +kubebuilder:validation:Schemaless
	// +optional
	Params apiextensionsv1.JSON `json:"params,omitempty"`
}

// VRouterBindingStatus defines the observed state of VRouterBinding.
type VRouterBindingStatus struct {
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName={vrb,vrouterbinding}

// VRouterBinding is the Schema for the vrouterbindings API.
type VRouterBinding struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   VRouterBindingSpec   `json:"spec,omitempty"`
	Status VRouterBindingStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// VRouterBindingList contains a list of VRouterBinding.
type VRouterBindingList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []VRouterBinding `json:"items"`
}

func init() {
	SchemeBuilder.Register(&VRouterBinding{}, &VRouterBindingList{})
}
