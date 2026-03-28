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

// VRouterTargetStatus defines the observed state of VRouterTarget.
type VRouterTargetStatus struct {
	// ProxmoxNode is the Proxmox cluster node currently hosting the VM.
	// Populated and kept up-to-date by the ProxmoxCluster controller.
	// +optional
	ProxmoxNode string `json:"proxmoxNode,omitempty"`
	// LastRebootTime is set when the ProxmoxCluster controller detects that
	// the VM uptime is within 1.5× syncInterval (i.e. recently rebooted).
	// The VRouterConfig controller uses this to force re-apply after a reboot.
	// +optional
	LastRebootTime *metav1.Time `json:"lastRebootTime,omitempty"`
	// VMRunning reflects whether the target VM was observed as running during
	// the most recent poll by the VRouterTarget controller.
	// +optional
	VMRunning bool `json:"vmRunning,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName={vrt,vroutertarget}
// +kubebuilder:printcolumn:name="VM Running",type=boolean,JSONPath=`.status.vmRunning`

// VRouterTarget is the Schema for the vroutertargets API.
type VRouterTarget struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   VRouterTargetSpec   `json:"spec,omitempty"`
	Status VRouterTargetStatus `json:"status,omitempty"`
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
