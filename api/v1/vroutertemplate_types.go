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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// VRouterTemplateSpec defines the desired state of VRouterTemplate.
type VRouterTemplateSpec struct {
	// +optional
	Config string `json:"config,omitempty"`
	// +optional
	Commands string `json:"commands,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:shortName={vrtt,vroutertemplate}

// VRouterTemplate is the Schema for the vroutertemplates API.
type VRouterTemplate struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec VRouterTemplateSpec `json:"spec,omitempty"`
}

// +kubebuilder:object:root=true

// VRouterTemplateList contains a list of VRouterTemplate.
type VRouterTemplateList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []VRouterTemplate `json:"items"`
}

func init() {
	SchemeBuilder.Register(&VRouterTemplate{}, &VRouterTemplateList{})
}
