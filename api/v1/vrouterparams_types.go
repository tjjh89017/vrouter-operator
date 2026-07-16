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

// VRouterParamsSpec defines the desired state of VRouterParams.
type VRouterParamsSpec struct {
	// +kubebuilder:pruning:PreserveUnknownFields
	// +kubebuilder:validation:Schemaless
	// +optional
	Params apiextensionsv1.JSON `json:"params,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:shortName={vrtp,vrouterparams}

// VRouterParams is the Schema for the vrouterparams API.
type VRouterParams struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec VRouterParamsSpec `json:"spec,omitempty"`
}

// +kubebuilder:object:root=true

// VRouterParamsList contains a list of VRouterParams.
type VRouterParamsList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []VRouterParams `json:"items"`
}

func init() {
	SchemeBuilder.Register(&VRouterParams{}, &VRouterParamsList{})
}
