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
	"strings"
	"testing"

	vrouterv1 "github.com/tjjh89017/vrouter-operator/api/v1"
)

func TestValidateProviderConfig_KubeVirtName_Empty_Rejected(t *testing.T) {
	provider := vrouterv1.ProviderConfig{
		Type: vrouterv1.ProviderKubeVirt,
		KubeVirt: &vrouterv1.KubeVirtConfig{
			Name: "",
		},
	}

	err := validateProviderConfig(provider, testNamespace)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "provider.kubevirt.name") {
		t.Fatalf("error = %q, want it to mention provider.kubevirt.name", err.Error())
	}
}

func TestValidateProviderConfig_KubeVirtName_Valid_Accepted(t *testing.T) {
	provider := vrouterv1.ProviderConfig{
		Type: vrouterv1.ProviderKubeVirt,
		KubeVirt: &vrouterv1.KubeVirtConfig{
			Name: "router-vm",
		},
	}

	if err := validateProviderConfig(provider, testNamespace); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}
