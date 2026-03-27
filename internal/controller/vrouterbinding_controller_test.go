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
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	vrouterv1 "github.com/tjjh89017/vrouter-operator/api/v1"
)

var _ = Describe("VRouterBinding Controller", func() {
	Context("When reconciling a resource", func() {
		const resourceName = "test-resource"

		ctx := context.Background()

		typeNamespacedName := types.NamespacedName{
			Name:      resourceName,
			Namespace: "default",
		}
		vrouterbinding := &vrouterv1.VRouterBinding{}

		BeforeEach(func() {
			By("creating the custom resource for the Kind VRouterBinding")
			err := k8sClient.Get(ctx, typeNamespacedName, vrouterbinding)
			if err != nil && errors.IsNotFound(err) {
				resource := &vrouterv1.VRouterBinding{
					ObjectMeta: metav1.ObjectMeta{
						Name:      resourceName,
						Namespace: "default",
					},
					Spec: vrouterv1.VRouterBindingSpec{
						TemplateRef: &vrouterv1.NameRef{Name: "test-template"},
						TargetRefs:  []vrouterv1.NameRef{{Name: "test-target"}},
					},
				}
				Expect(k8sClient.Create(ctx, resource)).To(Succeed())
			}
		})

		AfterEach(func() {
			resource := &vrouterv1.VRouterBinding{}
			err := k8sClient.Get(ctx, typeNamespacedName, resource)
			Expect(err).NotTo(HaveOccurred())

			By("Cleanup the specific resource instance VRouterBinding")
			Expect(k8sClient.Delete(ctx, resource)).To(Succeed())
		})
		It("should successfully reconcile the resource", func() {
			By("Reconciling the created resource")
			controllerReconciler := &VRouterBindingReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())
		})
	})

	Context("effectiveTemplateRefs", func() {
		It("should return only templateRefs when templateRef is nil", func() {
			binding := &vrouterv1.VRouterBinding{
				Spec: vrouterv1.VRouterBindingSpec{
					TemplateRefs: []vrouterv1.NameRef{
						{Name: "tmpl-a"},
						{Name: "tmpl-b"},
					},
				},
			}
			refs := effectiveTemplateRefs(binding)
			Expect(refs).To(HaveLen(2))
			Expect(refs[0].Name).To(Equal("tmpl-a"))
			Expect(refs[1].Name).To(Equal("tmpl-b"))
		})

		It("should prepend templateRef when set", func() {
			binding := &vrouterv1.VRouterBinding{
				Spec: vrouterv1.VRouterBindingSpec{
					TemplateRef: &vrouterv1.NameRef{Name: "tmpl-priority"},
					TemplateRefs: []vrouterv1.NameRef{
						{Name: "tmpl-a"},
						{Name: "tmpl-b"},
					},
				},
			}
			refs := effectiveTemplateRefs(binding)
			Expect(refs).To(HaveLen(3))
			Expect(refs[0].Name).To(Equal("tmpl-priority"))
			Expect(refs[1].Name).To(Equal("tmpl-a"))
			Expect(refs[2].Name).To(Equal("tmpl-b"))
		})

		It("should return single-element list when only templateRef is set", func() {
			binding := &vrouterv1.VRouterBinding{
				Spec: vrouterv1.VRouterBindingSpec{
					TemplateRef: &vrouterv1.NameRef{Name: "tmpl-only"},
				},
			}
			refs := effectiveTemplateRefs(binding)
			Expect(refs).To(HaveLen(1))
			Expect(refs[0].Name).To(Equal("tmpl-only"))
		})

		It("should return empty when neither is set", func() {
			binding := &vrouterv1.VRouterBinding{}
			refs := effectiveTemplateRefs(binding)
			Expect(refs).To(BeEmpty())
		})

		It("should preserve namespace from templateRef", func() {
			binding := &vrouterv1.VRouterBinding{
				Spec: vrouterv1.VRouterBindingSpec{
					TemplateRef:  &vrouterv1.NameRef{Namespace: "ns-a", Name: "tmpl-a"},
					TemplateRefs: []vrouterv1.NameRef{{Namespace: "ns-b", Name: "tmpl-b"}},
				},
			}
			refs := effectiveTemplateRefs(binding)
			Expect(refs).To(HaveLen(2))
			Expect(refs[0].Namespace).To(Equal("ns-a"))
			Expect(refs[1].Namespace).To(Equal("ns-b"))
		})
	})
})
