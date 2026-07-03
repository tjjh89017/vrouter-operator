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
	"context"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	vrouterv1 "github.com/tjjh89017/vrouter-operator/api/v1"
)

// newTestClient builds an in-process fake client (no envtest binaries required)
// seeded with the given objects, for exercising the ref-existence lookups that
// validateBinding / validateVRouterConfig perform.
func newTestClient(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := vrouterv1.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to register scheme: %v", err)
	}
	builder := fake.NewClientBuilder().WithScheme(scheme)
	if len(objs) > 0 {
		builder = builder.WithObjects(objs...)
	}
	return builder.Build()
}

// testNamespace is the namespace of the "referencing" object in every test
// case below; refs pointing anywhere else are cross-namespace.
const testNamespace = "tenant-a"

// metaObj is a small helper to build ObjectMeta for test fixtures in testNamespace.
func metaObj(name string) metav1.ObjectMeta {
	return metav1.ObjectMeta{Namespace: testNamespace, Name: name}
}

func TestValidateBinding_CrossNamespaceRefs_Rejected(t *testing.T) {
	tests := []struct {
		name    string
		binding *vrouterv1.VRouterBinding
		wantErr string
	}{
		{
			name: "cross-namespace targetRefs entry is rejected",
			binding: &vrouterv1.VRouterBinding{
				ObjectMeta: metaObj("b1"),
				Spec: vrouterv1.VRouterBindingSpec{
					TemplateRefs: []vrouterv1.NameRef{{Name: "tmpl1"}},
					TargetRefs:   []vrouterv1.NameRef{{Name: "r1", Namespace: "infra"}},
				},
			},
			wantErr: "cross-namespace reference not allowed: spec.targetRefs[0]",
		},
		{
			name: "cross-namespace templateRefs entry is rejected",
			binding: &vrouterv1.VRouterBinding{
				ObjectMeta: metaObj("b2"),
				Spec: vrouterv1.VRouterBindingSpec{
					TemplateRefs: []vrouterv1.NameRef{{Name: "tmpl1", Namespace: "infra"}},
					TargetRefs:   []vrouterv1.NameRef{{Name: "r1"}},
				},
			},
			wantErr: "cross-namespace reference not allowed: spec.templateRefs[0]",
		},
		{
			name: "cross-namespace deprecated templateRef is rejected",
			binding: &vrouterv1.VRouterBinding{
				ObjectMeta: metaObj("b3"),
				Spec: vrouterv1.VRouterBindingSpec{
					TemplateRef: &vrouterv1.NameRef{Name: "tmpl1", Namespace: "infra"}, //nolint:staticcheck // backward compat
					TargetRefs:  []vrouterv1.NameRef{{Name: "r1"}},
				},
			},
			wantErr: "cross-namespace reference not allowed: spec.templateRef",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Seed the same-namespace refs used by each case's non-offending
			// fields, so the failure only comes from the cross-namespace ref
			// under test (not an earlier "not found" from an unrelated field).
			tmpl1 := &vrouterv1.VRouterTemplate{ObjectMeta: metaObj("tmpl1")}
			r1 := &vrouterv1.VRouterTarget{ObjectMeta: metaObj("r1")}
			cl := newTestClient(t, tmpl1, r1)
			err := validateBinding(context.Background(), cl, tt.binding)
			if err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %q, want substring %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestValidateBinding_SameNamespaceRefs_Accepted(t *testing.T) {
	binding := &vrouterv1.VRouterBinding{
		ObjectMeta: metaObj("b1"),
		Spec: vrouterv1.VRouterBindingSpec{
			TemplateRefs: []vrouterv1.NameRef{{Name: "tmpl1"}},
			// Explicit same-namespace value and empty (defaulted) namespace both count as same-namespace.
			TargetRefs: []vrouterv1.NameRef{{Name: "r1", Namespace: testNamespace}, {Name: "r2"}},
		},
	}

	tmpl := &vrouterv1.VRouterTemplate{ObjectMeta: metaObj("tmpl1")}
	r1 := &vrouterv1.VRouterTarget{ObjectMeta: metaObj("r1")}
	r2 := &vrouterv1.VRouterTarget{ObjectMeta: metaObj("r2")}
	cl := newTestClient(t, tmpl, r1, r2)

	if err := validateBinding(context.Background(), cl, binding); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}

func TestValidateVRouterConfig_CrossNamespaceTargetRef_Rejected(t *testing.T) {
	cfg := &vrouterv1.VRouterConfig{
		ObjectMeta: metaObj("c1"),
		Spec: vrouterv1.VRouterConfigSpec{
			TargetRef: vrouterv1.NameRef{Name: "r1", Namespace: "infra"},
		},
	}
	// No target object seeded: the cross-namespace rejection must happen before
	// the existence lookup.
	cl := newTestClient(t)

	err := validateVRouterConfig(context.Background(), cl, cfg)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "cross-namespace reference not allowed: spec.targetRef") {
		t.Fatalf("error = %q, want cross-namespace rejection", err.Error())
	}
}

func TestValidateVRouterConfig_SameNamespaceTargetRef_Accepted(t *testing.T) {
	cfg := &vrouterv1.VRouterConfig{
		ObjectMeta: metaObj("c1"),
		Spec: vrouterv1.VRouterConfigSpec{
			TargetRef: vrouterv1.NameRef{Name: "r1"},
		},
	}
	target := &vrouterv1.VRouterTarget{ObjectMeta: metaObj("r1")}
	cl := newTestClient(t, target)

	if err := validateVRouterConfig(context.Background(), cl, cfg); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}

func TestValidateProviderConfig_ProxmoxClusterRef_CrossNamespace_Rejected(t *testing.T) {
	provider := vrouterv1.ProviderConfig{
		Type: vrouterv1.ProviderProxmox,
		Proxmox: &vrouterv1.ProxmoxConfig{
			VMID:       100,
			ClusterRef: vrouterv1.NameRef{Name: "pve", Namespace: "infra"},
		},
	}

	err := validateProviderConfig(provider, testNamespace)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "cross-namespace reference not allowed: provider.proxmox.clusterRef") {
		t.Fatalf("error = %q, want cross-namespace rejection", err.Error())
	}
}

func TestValidateProviderConfig_ProxmoxClusterRef_SameNamespace_Accepted(t *testing.T) {
	provider := vrouterv1.ProviderConfig{
		Type: vrouterv1.ProviderProxmox,
		Proxmox: &vrouterv1.ProxmoxConfig{
			VMID:       100,
			ClusterRef: vrouterv1.NameRef{Name: "pve"},
		},
	}

	if err := validateProviderConfig(provider, testNamespace); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}
