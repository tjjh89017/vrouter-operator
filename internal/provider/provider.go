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

	k8stypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	vrouterv1 "github.com/tjjh89017/vrouter-operator/api/v1"
	"github.com/tjjh89017/vrouter-operator/internal/provider/kubevirt"
	"github.com/tjjh89017/vrouter-operator/internal/provider/proxmox"
	"github.com/tjjh89017/vrouter-operator/internal/provider/types"
)

// Re-export core types so callers only need to import this package.
type Provider = types.Provider
type ExecStatus = types.ExecStatus

// GetProxmoxCluster fetches the ProxmoxCluster referenced by cfg from Kubernetes.
// If cfg.ClusterRef.Namespace is empty, fallbackNamespace is used.
func GetProxmoxCluster(ctx context.Context, cfg *vrouterv1.ProxmoxConfig, fallbackNamespace string, cl client.Client) (*vrouterv1.ProxmoxCluster, error) {
	ref := cfg.ClusterRef
	ns := ref.Namespace
	if ns == "" {
		ns = fallbackNamespace
	}
	var cluster vrouterv1.ProxmoxCluster
	if err := cl.Get(ctx, k8stypes.NamespacedName{Name: ref.Name, Namespace: ns}, &cluster); err != nil {
		return nil, fmt.Errorf("get ProxmoxCluster %s/%s: %w", ns, ref.Name, err)
	}
	return &cluster, nil
}

// New creates a Provider from the VRouterTarget's ProviderConfig.
// For the Proxmox provider it fetches the referenced ProxmoxCluster and uses
// target.Status.ProxmoxNode as the cached node to avoid redundant API calls.
func New(ctx context.Context, target *vrouterv1.VRouterTarget, cl client.Client, restCfg *rest.Config) (Provider, error) {
	cfg := target.Spec.Provider
	switch cfg.Type {
	case vrouterv1.ProviderProxmox:
		if cfg.Proxmox == nil {
			return nil, fmt.Errorf("provider.proxmox must be set when type is proxmox")
		}
		cluster, err := GetProxmoxCluster(ctx, cfg.Proxmox, target.Namespace, cl)
		if err != nil {
			return nil, err
		}
		return proxmox.New(ctx, cfg.Proxmox, cluster, target.Status.ProxmoxNode, cl)

	default: // kubevirt (default)
		if cfg.KubeVirt == nil {
			return nil, fmt.Errorf("provider.kubevirt must be set when type is kubevirt")
		}
		return kubevirt.New(cl, restCfg, cfg.KubeVirt.Name, cfg.KubeVirt.Namespace)
	}
}
