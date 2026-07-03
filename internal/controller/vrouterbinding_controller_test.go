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
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
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

	Context("Save field propagation to generated VRouterConfig", func() {
		var target *vrouterv1.VRouterTarget

		BeforeEach(func() {
			target = &vrouterv1.VRouterTarget{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "save-test-target",
					Namespace: "default",
				},
				Spec: vrouterv1.VRouterTargetSpec{
					Provider: vrouterv1.ProviderConfig{
						Type:     vrouterv1.ProviderKubeVirt,
						KubeVirt: &vrouterv1.KubeVirtConfig{Name: "save-test-vm"},
					},
				},
			}
			Expect(k8sClient.Create(ctx, target)).To(Succeed())
		})

		AfterEach(func() {
			Expect(k8sClient.Delete(ctx, target)).To(Succeed())
		})

		// generatedConfigFor runs onChange directly and fetches the generated VRouterConfig.
		generatedConfigFor := func(binding *vrouterv1.VRouterBinding) *vrouterv1.VRouterConfig {
			reconciler := &VRouterBindingReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := reconciler.onChange(ctx, reconcile.Request{}, binding)
			Expect(err).NotTo(HaveOccurred())

			cfg := &vrouterv1.VRouterConfig{}
			cfgName := binding.Name + "." + target.Name
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: cfgName, Namespace: "default"}, cfg)).To(Succeed())
			return cfg
		}

		It("should keep an explicit save:false on the generated VRouterConfig", func() {
			falseVal := false
			binding := &vrouterv1.VRouterBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "save-false-binding",
					Namespace: "default",
				},
				Spec: vrouterv1.VRouterBindingSpec{
					TargetRefs: []vrouterv1.NameRef{{Name: target.Name}},
					Save:       &falseVal,
				},
			}
			Expect(k8sClient.Create(ctx, binding)).To(Succeed())
			defer func() { _ = k8sClient.Delete(ctx, binding) }()

			cfg := generatedConfigFor(binding)
			Expect(cfg.Spec.Save).NotTo(BeNil())
			Expect(*cfg.Spec.Save).To(BeFalse())
		})

		// NOTE: this test creates the binding and generated VRouterConfig
		// through the real envtest apiserver via generatedConfigFor, so the
		// fetched cfg.Spec.Save has already been round-tripped through
		// Create/Get. That round-trip means the apiserver's own
		// +kubebuilder:default=true marker fills in Save on the wire whether
		// or not the controller itself ever touched the field, so a
		// controller regression that forced Save to &true instead of
		// propagating binding.Spec.Save unchanged would be indistinguishable
		// from correct behavior here. This test therefore only pins CRD-level
		// defaulting (*cfg.Spec.Save == true when unset), not the binding
		// controller's own nil-propagation code path (the plain assignment
		// `Save: binding.Spec.Save` in vrouterbinding_controller.go). See
		// TestOnChange_SaveUnset_PropagatesNilWithoutForcingDefault below for
		// a fake-client (no apiserver defaulting) test of that code path.
		It("should apply the CRD default (save:true) when the binding leaves save unset", func() {
			binding := &vrouterv1.VRouterBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "save-unset-binding",
					Namespace: "default",
				},
				Spec: vrouterv1.VRouterBindingSpec{
					TargetRefs: []vrouterv1.NameRef{{Name: target.Name}},
				},
			}
			Expect(binding.Spec.Save).To(BeNil())
			Expect(k8sClient.Create(ctx, binding)).To(Succeed())
			defer func() { _ = k8sClient.Delete(ctx, binding) }()

			cfg := generatedConfigFor(binding)
			Expect(cfg.Spec.Save).NotTo(BeNil())
			Expect(*cfg.Spec.Save).To(BeTrue())
		})
	})
})

// TestOnChange_SaveUnset_PropagatesNilWithoutForcingDefault is the
// unit-level counterpart to the "should apply the CRD default (save:true)"
// envtest spec above. It uses a fake client instead of the real envtest
// apiserver, so there is no CRD-level +kubebuilder:default=true defaulting
// to fill in Save on the wire -- whatever onChange builds is exactly what
// ends up on the fetched object. This isolates the binding controller's own
// propagation code (`Save: binding.Spec.Save` in
// vrouterbinding_controller.go) from apiserver defaulting: if that line were
// ever changed to force a value (e.g. `Save: ptr.To(true)`) instead of
// passing the binding's own *bool through unchanged, this test would still
// see a non-nil Save and fail, whereas the envtest spec above could not
// detect that regression.
func TestOnChange_SaveUnset_PropagatesNilWithoutForcingDefault(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := vrouterv1.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to register scheme: %v", err)
	}

	target := &vrouterv1.VRouterTarget{
		ObjectMeta: metav1.ObjectMeta{Name: "save-test-target", Namespace: "default"},
		Spec: vrouterv1.VRouterTargetSpec{
			Provider: vrouterv1.ProviderConfig{
				Type:     vrouterv1.ProviderKubeVirt,
				KubeVirt: &vrouterv1.KubeVirtConfig{Name: "save-test-vm"},
			},
		},
	}
	tmpl := &vrouterv1.VRouterTemplate{
		ObjectMeta: metav1.ObjectMeta{Name: "tmpl1", Namespace: "default"},
	}
	binding := &vrouterv1.VRouterBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "save-unset-binding", Namespace: "default"},
		Spec: vrouterv1.VRouterBindingSpec{
			TemplateRefs: []vrouterv1.NameRef{{Name: "tmpl1"}},
			TargetRefs:   []vrouterv1.NameRef{{Name: target.Name}},
			// Save intentionally left nil.
		},
	}

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(target, tmpl, binding).
		WithStatusSubresource(&vrouterv1.VRouterBinding{}).
		Build()
	r := &VRouterBindingReconciler{Client: cl, Scheme: scheme}

	if _, err := r.onChange(context.Background(), reconcile.Request{}, binding); err != nil {
		t.Fatalf("onChange returned error: %v", err)
	}

	var cfg vrouterv1.VRouterConfig
	cfgName := binding.Name + "." + target.Name
	if err := cl.Get(context.Background(), types.NamespacedName{Name: cfgName, Namespace: "default"}, &cfg); err != nil {
		t.Fatalf("get generated VRouterConfig: %v", err)
	}
	if cfg.Spec.Save != nil {
		t.Errorf("cfg.Spec.Save = %v, want nil (the controller must propagate binding.Spec.Save unchanged, not force a default)", *cfg.Spec.Save)
	}
}
