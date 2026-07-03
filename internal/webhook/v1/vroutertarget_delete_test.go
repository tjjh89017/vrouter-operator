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

	vrouterv1 "github.com/tjjh89017/vrouter-operator/api/v1"
)

// otherNamespace is used by the cross-namespace test case below; a binding
// living here must never block deletion of a target in testNamespace, even
// if it happens to reference a target with the same name (same-namespace-only
// policy means such a reference could never resolve to this target anyway).
const otherNamespace = "tenant-b"

func TestCheckTargetNotReferenced_ReferencedByBinding_Rejected(t *testing.T) {
	target := &vrouterv1.VRouterTarget{ObjectMeta: metaObj("r1")}
	binding := &vrouterv1.VRouterBinding{
		ObjectMeta: metaObj("b1"),
		Spec: vrouterv1.VRouterBindingSpec{
			TemplateRefs: []vrouterv1.NameRef{{Name: "tmpl1"}},
			TargetRefs:   []vrouterv1.NameRef{{Name: "r1"}},
		},
	}
	cl := newTestClient(t, target, binding)

	err := checkTargetNotReferenced(context.Background(), cl, target)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !strings.Contains(err.Error(), `still referenced by VRouterBinding "b1"`) {
		t.Fatalf("error = %q, want it to name the referencing binding", err.Error())
	}
}

func TestCheckTargetNotReferenced_ReferencedByConfig_Rejected(t *testing.T) {
	target := &vrouterv1.VRouterTarget{ObjectMeta: metaObj("r1")}
	cfg := &vrouterv1.VRouterConfig{
		ObjectMeta: metaObj("c1"),
		Spec: vrouterv1.VRouterConfigSpec{
			TargetRef: vrouterv1.NameRef{Name: "r1"},
		},
	}
	cl := newTestClient(t, target, cfg)

	err := checkTargetNotReferenced(context.Background(), cl, target)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !strings.Contains(err.Error(), `still referenced by VRouterConfig "c1"`) {
		t.Fatalf("error = %q, want it to name the referencing config", err.Error())
	}
}

func TestCheckTargetNotReferenced_Unreferenced_Allowed(t *testing.T) {
	target := &vrouterv1.VRouterTarget{ObjectMeta: metaObj("r1")}
	// A binding/config that reference a different target must not block r1.
	other := &vrouterv1.VRouterTarget{ObjectMeta: metaObj("r2")}
	binding := &vrouterv1.VRouterBinding{
		ObjectMeta: metaObj("b1"),
		Spec: vrouterv1.VRouterBindingSpec{
			TemplateRefs: []vrouterv1.NameRef{{Name: "tmpl1"}},
			TargetRefs:   []vrouterv1.NameRef{{Name: "r2"}},
		},
	}
	cl := newTestClient(t, target, other, binding)

	if err := checkTargetNotReferenced(context.Background(), cl, target); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}

func TestCheckTargetNotReferenced_DifferentNamespaceReference_Allowed(t *testing.T) {
	target := &vrouterv1.VRouterTarget{ObjectMeta: metaObj("r1")}
	// A binding in a different namespace that happens to reference the same
	// name is not a real reference: same-namespace-only policy means it can
	// only ever resolve to a target in its own namespace.
	crossNSBinding := &vrouterv1.VRouterBinding{
		ObjectMeta: metav1.ObjectMeta{Namespace: otherNamespace, Name: "b1"},
		Spec: vrouterv1.VRouterBindingSpec{
			TemplateRefs: []vrouterv1.NameRef{{Name: "tmpl1"}},
			TargetRefs:   []vrouterv1.NameRef{{Name: "r1"}},
		},
	}
	cl := newTestClient(t, target, crossNSBinding)

	if err := checkTargetNotReferenced(context.Background(), cl, target); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}

func TestVRouterTargetCustomValidator_ValidateDelete_Referenced_Rejected(t *testing.T) {
	target := &vrouterv1.VRouterTarget{ObjectMeta: metaObj("r1")}
	binding := &vrouterv1.VRouterBinding{
		ObjectMeta: metaObj("b1"),
		Spec: vrouterv1.VRouterBindingSpec{
			TemplateRefs: []vrouterv1.NameRef{{Name: "tmpl1"}},
			TargetRefs:   []vrouterv1.NameRef{{Name: "r1"}},
		},
	}
	cl := newTestClient(t, target, binding)
	validator := &VRouterTargetCustomValidator{Client: cl}

	_, err := validator.ValidateDelete(context.Background(), target)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !strings.Contains(err.Error(), `still referenced by VRouterBinding "b1"`) {
		t.Fatalf("error = %q, want it to name the referencing binding", err.Error())
	}
}

func TestVRouterTargetCustomValidator_ValidateDelete_Unreferenced_Allowed(t *testing.T) {
	target := &vrouterv1.VRouterTarget{ObjectMeta: metaObj("r1")}
	cl := newTestClient(t, target)
	validator := &VRouterTargetCustomValidator{Client: cl}

	if _, err := validator.ValidateDelete(context.Background(), target); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}
