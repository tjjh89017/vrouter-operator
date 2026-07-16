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

	vrouterv1 "github.com/tjjh89017/vrouter-operator/api/v1"
)

// These tests cover validateBinding's handling of spec.paramsRefs, mirroring
// the standing policy already enforced for spec.templateRefs: every entry
// must resolve to an existing VRouterParams and must be same-namespace. They
// reuse newTestClient/metaObj/testNamespace from crossns_test.go.

func TestValidateBinding_NoParamsRefs_Accepted(t *testing.T) {
	// paramsRefs is optional; an empty (or unset) list must not be rejected,
	// unlike templateRefs which requires at least one entry.
	binding := &vrouterv1.VRouterBinding{
		ObjectMeta: metaObj("b1"),
		Spec: vrouterv1.VRouterBindingSpec{
			TemplateRefs: []vrouterv1.NameRef{{Name: "tmpl1"}},
			TargetRefs:   []vrouterv1.NameRef{{Name: "r1"}},
		},
	}
	tmpl := &vrouterv1.VRouterTemplate{ObjectMeta: metaObj("tmpl1")}
	r1 := &vrouterv1.VRouterTarget{ObjectMeta: metaObj("r1")}
	cl := newTestClient(t, tmpl, r1)

	if err := validateBinding(context.Background(), cl, binding); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}

func TestValidateBinding_SameNamespaceParamsRef_Missing_Rejected(t *testing.T) {
	binding := &vrouterv1.VRouterBinding{
		ObjectMeta: metaObj("b1"),
		Spec: vrouterv1.VRouterBindingSpec{
			TemplateRefs: []vrouterv1.NameRef{{Name: "tmpl1"}},
			TargetRefs:   []vrouterv1.NameRef{{Name: "r1"}},
			ParamsRefs:   []vrouterv1.NameRef{{Name: "missing-params"}},
		},
	}
	tmpl := &vrouterv1.VRouterTemplate{ObjectMeta: metaObj("tmpl1")}
	r1 := &vrouterv1.VRouterTarget{ObjectMeta: metaObj("r1")}
	cl := newTestClient(t, tmpl, r1)

	err := validateBinding(context.Background(), cl, binding)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !strings.Contains(err.Error(), `spec.paramsRefs[0] "missing-params" (namespace "tenant-a") not found`) {
		t.Fatalf("error = %q, want a not-found error for the missing paramsRefs entry", err.Error())
	}
}

func TestValidateBinding_AllParamsRefsExist_Accepted(t *testing.T) {
	binding := &vrouterv1.VRouterBinding{
		ObjectMeta: metaObj("b1"),
		Spec: vrouterv1.VRouterBindingSpec{
			TemplateRefs: []vrouterv1.NameRef{{Name: "tmpl1"}},
			TargetRefs:   []vrouterv1.NameRef{{Name: "r1"}},
			ParamsRefs:   []vrouterv1.NameRef{{Name: "p1"}, {Name: "p2"}},
		},
	}
	tmpl := &vrouterv1.VRouterTemplate{ObjectMeta: metaObj("tmpl1")}
	r1 := &vrouterv1.VRouterTarget{ObjectMeta: metaObj("r1")}
	p1 := &vrouterv1.VRouterParams{ObjectMeta: metaObj("p1")}
	p2 := &vrouterv1.VRouterParams{ObjectMeta: metaObj("p2")}
	cl := newTestClient(t, tmpl, r1, p1, p2)

	if err := validateBinding(context.Background(), cl, binding); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}

func TestValidateBinding_CrossNamespaceParamsRef_Rejected(t *testing.T) {
	binding := &vrouterv1.VRouterBinding{
		ObjectMeta: metaObj("b1"),
		Spec: vrouterv1.VRouterBindingSpec{
			TemplateRefs: []vrouterv1.NameRef{{Name: "tmpl1"}},
			TargetRefs:   []vrouterv1.NameRef{{Name: "r1"}},
			ParamsRefs:   []vrouterv1.NameRef{{Name: "p1", Namespace: "infra"}},
		},
	}
	// Seed the non-offending same-namespace refs so the failure only comes
	// from the cross-namespace paramsRefs entry under test.
	tmpl := &vrouterv1.VRouterTemplate{ObjectMeta: metaObj("tmpl1")}
	r1 := &vrouterv1.VRouterTarget{ObjectMeta: metaObj("r1")}
	cl := newTestClient(t, tmpl, r1)

	err := validateBinding(context.Background(), cl, binding)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "cross-namespace reference not allowed: spec.paramsRefs[0]") {
		t.Fatalf("error = %q, want cross-namespace rejection", err.Error())
	}
}

func TestValidateBinding_SameNamespaceParamsRef_ExplicitAndEmpty_Accepted(t *testing.T) {
	binding := &vrouterv1.VRouterBinding{
		ObjectMeta: metaObj("b1"),
		Spec: vrouterv1.VRouterBindingSpec{
			TemplateRefs: []vrouterv1.NameRef{{Name: "tmpl1"}},
			TargetRefs:   []vrouterv1.NameRef{{Name: "r1"}},
			// Explicit same-namespace value and empty (defaulted) namespace both count as same-namespace.
			ParamsRefs: []vrouterv1.NameRef{{Name: "p1", Namespace: testNamespace}, {Name: "p2"}},
		},
	}
	tmpl := &vrouterv1.VRouterTemplate{ObjectMeta: metaObj("tmpl1")}
	r1 := &vrouterv1.VRouterTarget{ObjectMeta: metaObj("r1")}
	p1 := &vrouterv1.VRouterParams{ObjectMeta: metaObj("p1")}
	p2 := &vrouterv1.VRouterParams{ObjectMeta: metaObj("p2")}
	cl := newTestClient(t, tmpl, r1, p1, p2)

	if err := validateBinding(context.Background(), cl, binding); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}
