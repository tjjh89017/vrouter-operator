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

// ProxmoxClusterSpec defines the desired state of ProxmoxCluster.
type ProxmoxClusterSpec struct {
	// Proxmox API endpoint URLs. Multiple endpoints can be specified for HA clusters;
	// each request tries them in order until one succeeds.
	Endpoints []string `json:"endpoints"`
	// Reference to a Secret containing api-token-id and api-token-secret.
	CredentialsRef SecretReference `json:"credentialsRef"`
	// +kubebuilder:default=false
	// +optional
	InsecureSkipTLSVerify bool `json:"insecureSkipTLSVerify,omitempty"`
	// SyncInterval controls how often the controller polls /cluster/resources and
	// updates VRouterTarget.status.proxmoxNode.
	// +kubebuilder:default="60s"
	// +optional
	SyncInterval metav1.Duration `json:"syncInterval,omitempty"`
	// CheckGuestUptime enables guest OS uptime detection via QEMU Guest Agent.
	// When true, the controller queries /proc/uptime inside each VM during every
	// sync and sets VRouterTarget.status.lastRebootTime when guest uptime is within
	// 1.5× syncInterval. This detects soft reboots (guest-initiated reboot) that do
	// not restart the QEMU process and therefore cannot be detected from Proxmox
	// cluster resource uptime alone.
	// Requires QEMU Guest Agent to be running in the VM.
	// +kubebuilder:default=false
	// +optional
	CheckGuestUptime bool `json:"checkGuestUptime,omitempty"`
}

// ProxmoxClusterStatus defines the observed state of ProxmoxCluster.
type ProxmoxClusterStatus struct {
	// LastSyncTime is the last time the controller successfully polled the Proxmox cluster.
	// +optional
	LastSyncTime *metav1.Time `json:"lastSyncTime,omitempty"`
	// Conditions represent the current state of the ProxmoxCluster.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName={pxc,proxmoxcluster}
// +kubebuilder:printcolumn:name="Endpoint",type=string,JSONPath=`.spec.endpoints[0]`
// +kubebuilder:printcolumn:name="LastSync",type=date,JSONPath=`.status.lastSyncTime`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// ProxmoxCluster is the Schema for the proxmoxclusters API.
type ProxmoxCluster struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ProxmoxClusterSpec   `json:"spec,omitempty"`
	Status ProxmoxClusterStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ProxmoxClusterList contains a list of ProxmoxCluster.
type ProxmoxClusterList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ProxmoxCluster `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ProxmoxCluster{}, &ProxmoxClusterList{})
}
