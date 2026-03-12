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

// VRouterConfigSpec defines the desired state of VRouterConfig.
type VRouterConfigSpec struct {
	// TargetRef references the VRouterTarget that describes the provider and router.
	TargetRef NameRef `json:"targetRef"`
	// +kubebuilder:default=true
	Save bool `json:"save,omitempty"`
	// +optional
	Config string `json:"config,omitempty"`
	// +optional
	Commands string `json:"commands,omitempty"`
}

// VRouterConfigStatus defines the observed state of VRouterConfig.
type VRouterConfigStatus struct {
	// +kubebuilder:validation:Enum=Pending;Applying;Applied;Failed
	// +kubebuilder:default=Pending
	Phase string `json:"phase,omitempty"`
	// +optional
	ExecPID int64 `json:"execPID,omitempty"`
	// +optional
	LastAppliedTime *metav1.Time `json:"lastAppliedTime,omitempty"`
	// +optional
	Message string `json:"message,omitempty"`
	// ObservedGeneration is the generation for which exec was last dispatched.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// Conditions for kubectl wait support.
	// The "Applied" condition is True when phase=Applied.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName={vrc,vrouterconfig}
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Target",type=string,JSONPath=`.spec.targetRef.name`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// VRouterConfig is the Schema for the vrouterconfigs API.
type VRouterConfig struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   VRouterConfigSpec   `json:"spec,omitempty"`
	Status VRouterConfigStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// VRouterConfigList contains a list of VRouterConfig.
type VRouterConfigList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []VRouterConfig `json:"items"`
}

func init() {
	SchemeBuilder.Register(&VRouterConfig{}, &VRouterConfigList{})
}
