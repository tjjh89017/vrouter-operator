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
	"fmt"

	vrouterv1 "github.com/tjjh89017/vrouter-operator/api/v1"
)

// validateRollout checks spec.rollout's mode-specific field requirements.
// Disabled (rollout nil/empty, or an explicit mode: Disabled) runs no checks
// at all: a rollout only activates when a mode is explicitly written, so an
// otherwise-invalid Disabled rollout must never block a create/update. Only
// fields belonging to the selected mode are validated; fields belonging to
// the other mode are ignored entirely, per the design's "fields belonging to
// the other mode are ignored" rule.
func validateRollout(rollout *vrouterv1.RolloutSpec) error {
	switch rollout.EffectiveMode() {
	case vrouterv1.RolloutModeFixedInterval:
		if rollout.WaitInterval.Duration <= 0 {
			return fmt.Errorf("spec.rollout.waitInterval must be greater than zero when spec.rollout.mode is FixedInterval")
		}
	case vrouterv1.RolloutModeWaitForApplied:
		if rollout.PollInterval.Duration < 0 {
			return fmt.Errorf("spec.rollout.pollInterval must not be negative")
		}
		if rollout.WaitAfterApplied.Duration < 0 {
			return fmt.Errorf("spec.rollout.waitAfterApplied must not be negative")
		}
	}
	return nil
}

// validateProviderConfig checks that the provider-specific sub-config is present
// for the declared provider type. Field-level validation is handled by kubebuilder markers.
// namespace is the namespace of the object that owns this provider config (e.g. the
// VRouterTarget), used to reject cross-namespace references.
func validateProviderConfig(provider vrouterv1.ProviderConfig, namespace string) error {
	switch provider.Type {
	case vrouterv1.ProviderKubeVirt, "":
		if provider.KubeVirt == nil {
			return fmt.Errorf("provider.kubevirt must be set when type is kubevirt")
		}
		if provider.KubeVirt.Name == "" {
			return fmt.Errorf("provider.kubevirt.name must not be empty")
		}
	case vrouterv1.ProviderProxmox:
		if provider.Proxmox == nil {
			return fmt.Errorf("provider.proxmox must be set when type is proxmox")
		}
		if provider.Proxmox.ClusterRef.Name == "" {
			return fmt.Errorf("provider.proxmox.clusterRef.name must be set")
		}
		if ns := vrouterv1.ResolveNamespace(provider.Proxmox.ClusterRef, namespace); ns != namespace {
			return fmt.Errorf("cross-namespace reference not allowed: provider.proxmox.clusterRef may only reference the same namespace")
		}
	case vrouterv1.ProviderVRouterDaemon:
		if provider.Daemon == nil {
			return fmt.Errorf("provider.daemon must be set when type is vrouter-daemon")
		}
		if provider.Daemon.Address == "" {
			return fmt.Errorf("provider.daemon.address must be set")
		}
		if provider.Daemon.AgentID == "" {
			return fmt.Errorf("provider.daemon.agentID must be set")
		}
	default:
		return fmt.Errorf("unknown provider type: %s", provider.Type)
	}
	return nil
}
