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

// Package proxmox provides a Proxmox VE provider stub.
// Full implementation (REST API + QGA) is planned for a future release.
package proxmox

import (
	"context"
	"fmt"

	vrouterv1 "github.com/tjjh89017/vrouter-operator/api/v1"
	"github.com/tjjh89017/vrouter-operator/internal/provider/types"
)

// Provider implements provider.Provider for Proxmox VE (stub).
type Provider struct{}

// New creates a Proxmox provider stub.
func New(_ *vrouterv1.ProxmoxConfig) (*Provider, error) {
	return &Provider{}, nil
}

func (p *Provider) CheckReady(_ context.Context) error {
	return fmt.Errorf("proxmox provider not yet implemented")
}

func (p *Provider) WriteFile(_ context.Context, _ []byte) error {
	return fmt.Errorf("proxmox provider not yet implemented")
}

func (p *Provider) ExecScript(_ context.Context) (int64, error) {
	return 0, fmt.Errorf("proxmox provider not yet implemented")
}

func (p *Provider) GetExecStatus(_ context.Context, _ int64) (*types.ExecStatus, error) {
	return nil, fmt.Errorf("proxmox provider not yet implemented")
}
