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

// TestValidateProviderConfig_Matrix exercises every branch of
// validateProviderConfig's switch, not just the KubeVirt-name case covered
// above and the Proxmox cross-namespace cases covered in crossns_test.go.
// Before this test, the proxmox/daemon nil-subconfig and empty-required-field
// branches, the implicit-kubevirt-on-empty-type branch, and the
// unknown-provider-type default branch were entirely unexercised.
func TestValidateProviderConfig_Matrix(t *testing.T) {
	tests := []struct {
		name     string
		provider vrouterv1.ProviderConfig
		wantErr  string // empty means "expect no error"
	}{
		{
			name:     "kubevirt: nil sub-config rejected",
			provider: vrouterv1.ProviderConfig{Type: vrouterv1.ProviderKubeVirt},
			wantErr:  "provider.kubevirt must be set",
		},
		{
			name: "empty type defaults to kubevirt and is validated the same way",
			provider: vrouterv1.ProviderConfig{
				Type: "",
			},
			wantErr: "provider.kubevirt must be set",
		},
		{
			name: "empty type with a valid kubevirt sub-config is accepted",
			provider: vrouterv1.ProviderConfig{
				Type:     "",
				KubeVirt: &vrouterv1.KubeVirtConfig{Name: "router-vm"},
			},
		},
		{
			name:     "proxmox: nil sub-config rejected",
			provider: vrouterv1.ProviderConfig{Type: vrouterv1.ProviderProxmox},
			wantErr:  "provider.proxmox must be set",
		},
		{
			name: "proxmox: empty clusterRef.name rejected",
			provider: vrouterv1.ProviderConfig{
				Type:    vrouterv1.ProviderProxmox,
				Proxmox: &vrouterv1.ProxmoxConfig{VMID: 100},
			},
			wantErr: "provider.proxmox.clusterRef.name must be set",
		},
		{
			name: "proxmox: valid config accepted",
			provider: vrouterv1.ProviderConfig{
				Type:    vrouterv1.ProviderProxmox,
				Proxmox: &vrouterv1.ProxmoxConfig{VMID: 100, ClusterRef: vrouterv1.NameRef{Name: "pve"}},
			},
		},
		{
			name:     "daemon: nil sub-config rejected",
			provider: vrouterv1.ProviderConfig{Type: vrouterv1.ProviderVRouterDaemon},
			wantErr:  "provider.daemon must be set",
		},
		{
			name: "daemon: empty address rejected",
			provider: vrouterv1.ProviderConfig{
				Type:   vrouterv1.ProviderVRouterDaemon,
				Daemon: &vrouterv1.DaemonConfig{AgentID: "agent-1"},
			},
			wantErr: "provider.daemon.address must be set",
		},
		{
			name: "daemon: empty agentID rejected",
			provider: vrouterv1.ProviderConfig{
				Type:   vrouterv1.ProviderVRouterDaemon,
				Daemon: &vrouterv1.DaemonConfig{Address: "vrouter-daemon:9090"},
			},
			wantErr: "provider.daemon.agentID must be set",
		},
		{
			name: "daemon: valid config accepted",
			provider: vrouterv1.ProviderConfig{
				Type:   vrouterv1.ProviderVRouterDaemon,
				Daemon: &vrouterv1.DaemonConfig{Address: "vrouter-daemon:9090", AgentID: "agent-1"},
			},
		},
		{
			name:     "unknown provider type rejected",
			provider: vrouterv1.ProviderConfig{Type: vrouterv1.ProviderType("made-up")},
			wantErr:  "unknown provider type: made-up",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateProviderConfig(tt.provider, testNamespace)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("expected no error, got %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %q, want substring %q", err.Error(), tt.wantErr)
			}
		})
	}
}
