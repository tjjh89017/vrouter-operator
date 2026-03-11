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

// validateProviderConfig checks that the provider-specific sub-config is present
// for the declared provider type. Field-level validation is handled by kubebuilder markers.
func validateProviderConfig(provider vrouterv1.ProviderConfig) error {
	switch provider.Type {
	case vrouterv1.ProviderKubeVirt, "":
		if provider.KubeVirt == nil {
			return fmt.Errorf("provider.kubevirt must be set when type is kubevirt")
		}
	case vrouterv1.ProviderProxmox:
		if provider.Proxmox == nil {
			return fmt.Errorf("provider.proxmox must be set when type is proxmox")
		}
	default:
		return fmt.Errorf("unknown provider type: %s", provider.Type)
	}
	return nil
}
