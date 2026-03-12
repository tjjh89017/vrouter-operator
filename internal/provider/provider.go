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

// Package provider exposes the Provider interface and a factory function for
// creating provider instances. Callers only need to import this package.
package provider

import (
	"context"
	"fmt"

	vrouterv1 "github.com/tjjh89017/vrouter-operator/api/v1"
	"github.com/tjjh89017/vrouter-operator/internal/provider/kubevirt"
	"github.com/tjjh89017/vrouter-operator/internal/provider/proxmox"
	"github.com/tjjh89017/vrouter-operator/internal/provider/types"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Re-export core types so callers only need to import this package.
type Provider = types.Provider
type ExecStatus = types.ExecStatus

// New creates a Provider from the given ProviderConfig bound to the described target.
// namespace is the namespace of the VRouterConfig resource (used to resolve credential Secrets).
func New(ctx context.Context, cfg vrouterv1.ProviderConfig, cl client.Client, restCfg *rest.Config, namespace string) (Provider, error) {
	switch cfg.Type {
	case vrouterv1.ProviderProxmox:
		if cfg.Proxmox == nil {
			return nil, fmt.Errorf("provider.proxmox must be set when type is proxmox")
		}
		return proxmox.New(ctx, cfg.Proxmox, cl, namespace)

	default: // kubevirt (default)
		if cfg.KubeVirt == nil {
			return nil, fmt.Errorf("provider.kubevirt must be set when type is kubevirt")
		}
		return kubevirt.New(cl, restCfg, cfg.KubeVirt.Name, cfg.KubeVirt.Namespace)
	}
}
