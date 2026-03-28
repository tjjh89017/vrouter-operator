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

const ProviderVRouterDaemon ProviderType = "vrouter-daemon"

// DaemonConfig holds vrouter-daemon ControlService connection parameters.
type DaemonConfig struct {
	// Address is the gRPC endpoint of the vrouter-daemon ControlService
	// (e.g., "vrouter-server.default.svc:9090").
	Address string `json:"address"`
	// AgentID is the identifier of the target router agent.
	AgentID string `json:"agentID"`
	// TimeoutSeconds is the maximum time to wait for a config apply to complete.
	// +optional
	// +kubebuilder:default=60
	TimeoutSeconds int32 `json:"timeoutSeconds,omitempty"`
}
