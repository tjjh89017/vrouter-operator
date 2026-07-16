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

func TestCheckParamsNotReferenced_ReferencedByBinding_Rejected(t *testing.T) {
	params := &vrouterv1.VRouterParams{ObjectMeta: metaObj("p1")}
	binding := &vrouterv1.VRouterBinding{
		ObjectMeta: metaObj("b1"),
		Spec: vrouterv1.VRouterBindingSpec{
			TemplateRefs: []vrouterv1.NameRef{{Name: "tmpl1"}},
			TargetRefs:   []vrouterv1.NameRef{{Name: "r1"}},
			ParamsRefs:   []vrouterv1.NameRef{{Name: "p1"}},
		},
	}
	cl := newTestClient(t, params, binding)

	err := checkParamsNotReferenced(context.Background(), cl, params)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !strings.Contains(err.Error(), `still referenced by binding "b1"`) {
		t.Fatalf("error = %q, want it to name the referencing binding", err.Error())
	}
}

func TestCheckParamsNotReferenced_Unreferenced_Allowed(t *testing.T) {
	params := &vrouterv1.VRouterParams{ObjectMeta: metaObj("p1")}
	// A binding that references a different params object must not block p1.
	other := &vrouterv1.VRouterParams{ObjectMeta: metaObj("p2")}
	binding := &vrouterv1.VRouterBinding{
		ObjectMeta: metaObj("b1"),
		Spec: vrouterv1.VRouterBindingSpec{
			TemplateRefs: []vrouterv1.NameRef{{Name: "tmpl1"}},
			TargetRefs:   []vrouterv1.NameRef{{Name: "r1"}},
			ParamsRefs:   []vrouterv1.NameRef{{Name: "p2"}},
		},
	}
	cl := newTestClient(t, params, other, binding)

	if err := checkParamsNotReferenced(context.Background(), cl, params); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}

func TestCheckParamsNotReferenced_DifferentNamespaceReference_Allowed(t *testing.T) {
	params := &vrouterv1.VRouterParams{ObjectMeta: metaObj("p1")}
	// A binding in a different namespace that happens to reference the same
	// name is not a real reference: same-namespace-only policy means it can
	// only ever resolve to a params object in its own namespace.
	crossNSBinding := &vrouterv1.VRouterBinding{
		ObjectMeta: metav1.ObjectMeta{Namespace: otherNamespace, Name: "b1"},
		Spec: vrouterv1.VRouterBindingSpec{
			TemplateRefs: []vrouterv1.NameRef{{Name: "tmpl1"}},
			TargetRefs:   []vrouterv1.NameRef{{Name: "r1"}},
			ParamsRefs:   []vrouterv1.NameRef{{Name: "p1"}},
		},
	}
	cl := newTestClient(t, params, crossNSBinding)

	if err := checkParamsNotReferenced(context.Background(), cl, params); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}

func TestCheckParamsNotReferenced_MultipleReferencingBindings_ReportsOthers(t *testing.T) {
	params := &vrouterv1.VRouterParams{ObjectMeta: metaObj("p1")}
	binding1 := &vrouterv1.VRouterBinding{
		ObjectMeta: metaObj("b1"),
		Spec: vrouterv1.VRouterBindingSpec{
			TemplateRefs: []vrouterv1.NameRef{{Name: "tmpl1"}},
			TargetRefs:   []vrouterv1.NameRef{{Name: "r1"}},
			ParamsRefs:   []vrouterv1.NameRef{{Name: "p1"}},
		},
	}
	binding2 := &vrouterv1.VRouterBinding{
		ObjectMeta: metaObj("b2"),
		Spec: vrouterv1.VRouterBindingSpec{
			TemplateRefs: []vrouterv1.NameRef{{Name: "tmpl1"}},
			TargetRefs:   []vrouterv1.NameRef{{Name: "r1"}},
			ParamsRefs:   []vrouterv1.NameRef{{Name: "p1"}},
		},
	}
	cl := newTestClient(t, params, binding1, binding2)

	err := checkParamsNotReferenced(context.Background(), cl, params)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "(and 1 others)") {
		t.Fatalf("error = %q, want it to mention additional referencing bindings", err.Error())
	}
}

func TestVRouterParamsCustomValidator_ValidateDelete_Referenced_Rejected(t *testing.T) {
	params := &vrouterv1.VRouterParams{ObjectMeta: metaObj("p1")}
	binding := &vrouterv1.VRouterBinding{
		ObjectMeta: metaObj("b1"),
		Spec: vrouterv1.VRouterBindingSpec{
			TemplateRefs: []vrouterv1.NameRef{{Name: "tmpl1"}},
			TargetRefs:   []vrouterv1.NameRef{{Name: "r1"}},
			ParamsRefs:   []vrouterv1.NameRef{{Name: "p1"}},
		},
	}
	cl := newTestClient(t, params, binding)
	validator := &VRouterParamsCustomValidator{Client: cl}

	_, err := validator.ValidateDelete(context.Background(), params)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !strings.Contains(err.Error(), `still referenced by binding "b1"`) {
		t.Fatalf("error = %q, want it to name the referencing binding", err.Error())
	}
}

func TestVRouterParamsCustomValidator_ValidateDelete_Unreferenced_Allowed(t *testing.T) {
	params := &vrouterv1.VRouterParams{ObjectMeta: metaObj("p1")}
	cl := newTestClient(t, params)
	validator := &VRouterParamsCustomValidator{Client: cl}

	if _, err := validator.ValidateDelete(context.Background(), params); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}
