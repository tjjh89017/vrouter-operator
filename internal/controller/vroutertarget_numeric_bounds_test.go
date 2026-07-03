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

package controller

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	vrouterv1 "github.com/tjjh89017/vrouter-operator/api/v1"
)

// These tests confirm that the Kubernetes API server rejects VRouterTarget
// resources with out-of-range numeric fields, based on the CRD schema
// generated from the +kubebuilder:validation:Minimum markers on
// ProxmoxConfig.VMID and DaemonConfig.TimeoutSeconds. A VMID below 100 is
// never a valid Proxmox VM ID, and a non-positive TimeoutSeconds cannot
// describe a wait duration.
var _ = Describe("VRouterTarget numeric bounds", func() {
	Context("proxmox.vmid", func() {
		It("should reject a vmid of 0", func() {
			target := &vrouterv1.VRouterTarget{
				ObjectMeta: metav1.ObjectMeta{
					GenerateName: "bounds-vmid-zero-",
					Namespace:    "default",
				},
				Spec: vrouterv1.VRouterTargetSpec{
					Provider: vrouterv1.ProviderConfig{
						Type: vrouterv1.ProviderProxmox,
						Proxmox: &vrouterv1.ProxmoxConfig{
							VMID:       0,
							ClusterRef: vrouterv1.NameRef{Name: "cluster"},
						},
					},
				},
			}
			err := k8sClient.Create(ctx, target)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("vmid"))
		})

		It("should reject a vmid of 99 (below the valid Proxmox range)", func() {
			target := &vrouterv1.VRouterTarget{
				ObjectMeta: metav1.ObjectMeta{
					GenerateName: "bounds-vmid-99-",
					Namespace:    "default",
				},
				Spec: vrouterv1.VRouterTargetSpec{
					Provider: vrouterv1.ProviderConfig{
						Type: vrouterv1.ProviderProxmox,
						Proxmox: &vrouterv1.ProxmoxConfig{
							VMID:       99,
							ClusterRef: vrouterv1.NameRef{Name: "cluster"},
						},
					},
				},
			}
			err := k8sClient.Create(ctx, target)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("vmid"))
		})

		It("should reject a negative vmid", func() {
			target := &vrouterv1.VRouterTarget{
				ObjectMeta: metav1.ObjectMeta{
					GenerateName: "bounds-vmid-negative-",
					Namespace:    "default",
				},
				Spec: vrouterv1.VRouterTargetSpec{
					Provider: vrouterv1.ProviderConfig{
						Type: vrouterv1.ProviderProxmox,
						Proxmox: &vrouterv1.ProxmoxConfig{
							VMID:       -1,
							ClusterRef: vrouterv1.NameRef{Name: "cluster"},
						},
					},
				},
			}
			err := k8sClient.Create(ctx, target)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("vmid"))
		})

		It("should admit the minimum valid vmid of 100", func() {
			target := &vrouterv1.VRouterTarget{
				ObjectMeta: metav1.ObjectMeta{
					GenerateName: "bounds-vmid-100-",
					Namespace:    "default",
				},
				Spec: vrouterv1.VRouterTargetSpec{
					Provider: vrouterv1.ProviderConfig{
						Type: vrouterv1.ProviderProxmox,
						Proxmox: &vrouterv1.ProxmoxConfig{
							VMID:       100,
							ClusterRef: vrouterv1.NameRef{Name: "cluster"},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, target)).To(Succeed())
			Expect(k8sClient.Delete(ctx, target)).To(Succeed())
		})
	})

	Context("daemon.timeoutSeconds", func() {
		It("should reject a timeoutSeconds of 0", func() {
			// TimeoutSeconds is `json:"timeoutSeconds,omitempty"`, so a typed
			// Go client would silently drop an explicit 0 (the Go zero value)
			// before it reaches the API server, letting the default of 60
			// apply instead. Use an unstructured object to put a literal 0
			// on the wire and confirm the API server itself rejects it.
			target := &unstructured.Unstructured{
				Object: map[string]interface{}{
					"apiVersion": "vrouter.kojuro.date/v1",
					"kind":       "VRouterTarget",
					"metadata": map[string]interface{}{
						"generateName": "bounds-timeout-zero-",
						"namespace":    "default",
					},
					"spec": map[string]interface{}{
						"provider": map[string]interface{}{
							"type": "vrouter-daemon",
							"daemon": map[string]interface{}{
								"address":        "vrouter-server.default.svc:9090",
								"agentID":        "agent-1",
								"timeoutSeconds": int64(0),
							},
						},
					},
				},
			}
			err := k8sClient.Create(ctx, target)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("timeoutSeconds"))
		})

		It("should reject a negative timeoutSeconds", func() {
			target := &vrouterv1.VRouterTarget{
				ObjectMeta: metav1.ObjectMeta{
					GenerateName: "bounds-timeout-negative-",
					Namespace:    "default",
				},
				Spec: vrouterv1.VRouterTargetSpec{
					Provider: vrouterv1.ProviderConfig{
						Type: vrouterv1.ProviderVRouterDaemon,
						Daemon: &vrouterv1.DaemonConfig{
							Address:        "vrouter-server.default.svc:9090",
							AgentID:        "agent-1",
							TimeoutSeconds: -5,
						},
					},
				},
			}
			err := k8sClient.Create(ctx, target)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("timeoutSeconds"))
		})

		It("should admit the minimum valid timeoutSeconds of 1", func() {
			target := &vrouterv1.VRouterTarget{
				ObjectMeta: metav1.ObjectMeta{
					GenerateName: "bounds-timeout-one-",
					Namespace:    "default",
				},
				Spec: vrouterv1.VRouterTargetSpec{
					Provider: vrouterv1.ProviderConfig{
						Type: vrouterv1.ProviderVRouterDaemon,
						Daemon: &vrouterv1.DaemonConfig{
							Address:        "vrouter-server.default.svc:9090",
							AgentID:        "agent-1",
							TimeoutSeconds: 1,
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, target)).To(Succeed())
			Expect(k8sClient.Delete(ctx, target)).To(Succeed())
		})
	})
})
