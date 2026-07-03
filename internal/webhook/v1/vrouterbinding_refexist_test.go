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

// These tests cover the ref-existence path of validateBinding (same namespace,
// so cross-namespace rejection - already covered by crossns_test.go - never
// triggers). They reuse newTestClient/metaObj/testNamespace from crossns_test.go.

func TestValidateBinding_SameNamespaceTargetRef_Missing_Rejected(t *testing.T) {
	binding := &vrouterv1.VRouterBinding{
		ObjectMeta: metaObj("b1"),
		Spec: vrouterv1.VRouterBindingSpec{
			TemplateRefs: []vrouterv1.NameRef{{Name: "tmpl1"}},
			TargetRefs:   []vrouterv1.NameRef{{Name: "missing-target"}},
		},
	}
	// Seed the template so the target lookup is what fails.
	tmpl := &vrouterv1.VRouterTemplate{ObjectMeta: metaObj("tmpl1")}
	cl := newTestClient(t, tmpl)

	err := validateBinding(context.Background(), cl, binding)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !strings.Contains(err.Error(), `spec.targetRefs "missing-target" (namespace "tenant-a") not found`) {
		t.Fatalf("error = %q, want a not-found error for the missing targetRef", err.Error())
	}
}

func TestValidateBinding_SameNamespaceTemplateRef_Missing_Rejected(t *testing.T) {
	binding := &vrouterv1.VRouterBinding{
		ObjectMeta: metaObj("b1"),
		Spec: vrouterv1.VRouterBindingSpec{
			TemplateRefs: []vrouterv1.NameRef{{Name: "missing-template"}},
			TargetRefs:   []vrouterv1.NameRef{{Name: "r1"}},
		},
	}
	target := &vrouterv1.VRouterTarget{ObjectMeta: metaObj("r1")}
	cl := newTestClient(t, target)

	err := validateBinding(context.Background(), cl, binding)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !strings.Contains(err.Error(), `spec.templateRefs[0] "missing-template" (namespace "tenant-a") not found`) {
		t.Fatalf("error = %q, want a not-found error for the missing templateRefs entry", err.Error())
	}
}

func TestValidateBinding_DeprecatedTemplateRef_Missing_Rejected(t *testing.T) {
	binding := &vrouterv1.VRouterBinding{
		ObjectMeta: metaObj("b1"),
		Spec: vrouterv1.VRouterBindingSpec{
			TemplateRef: &vrouterv1.NameRef{Name: "missing-template"}, //nolint:staticcheck // backward compat
			TargetRefs:  []vrouterv1.NameRef{{Name: "r1"}},
		},
	}
	target := &vrouterv1.VRouterTarget{ObjectMeta: metaObj("r1")}
	cl := newTestClient(t, target)

	err := validateBinding(context.Background(), cl, binding)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !strings.Contains(err.Error(), `spec.templateRef "missing-template" (namespace "tenant-a") not found`) {
		t.Fatalf("error = %q, want a not-found error for the missing templateRef", err.Error())
	}
}

func TestValidateBinding_NoTemplateRefAtAll_Rejected(t *testing.T) {
	binding := &vrouterv1.VRouterBinding{
		ObjectMeta: metaObj("b1"),
		Spec: vrouterv1.VRouterBindingSpec{
			TargetRefs: []vrouterv1.NameRef{{Name: "r1"}},
		},
	}
	target := &vrouterv1.VRouterTarget{ObjectMeta: metaObj("r1")}
	cl := newTestClient(t, target)

	err := validateBinding(context.Background(), cl, binding)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "at least one of spec.templateRef or spec.templateRefs must be set") {
		t.Fatalf("error = %q, want the missing-template-ref error", err.Error())
	}
}

func TestValidateBinding_AllRefsExist_Accepted(t *testing.T) {
	binding := &vrouterv1.VRouterBinding{
		ObjectMeta: metaObj("b1"),
		Spec: vrouterv1.VRouterBindingSpec{
			TemplateRefs: []vrouterv1.NameRef{{Name: "tmpl1"}},
			TargetRefs:   []vrouterv1.NameRef{{Name: "r1"}, {Name: "r2"}},
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

// TestVRouterBindingValidateUpdate_DeletionInProgress_SkipsRefValidation exercises
// the webhook method (not the bare validateBinding helper) because the
// deletion-timestamp skip lives in ValidateUpdate itself.
func TestVRouterBindingValidateUpdate_DeletionInProgress_SkipsRefValidation(t *testing.T) {
	now := metav1.Now()
	binding := &vrouterv1.VRouterBinding{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:         testNamespace,
			Name:              "b1",
			DeletionTimestamp: &now,
			Finalizers:        []string{"vrouter.kojuro.date/finalizer"},
		},
		Spec: vrouterv1.VRouterBindingSpec{
			TemplateRefs: []vrouterv1.NameRef{{Name: "now-missing-template"}},
			TargetRefs:   []vrouterv1.NameRef{{Name: "now-missing-target"}},
		},
	}
	// Empty client: none of the refs resolve. If the deletion skip were not
	// honored, this would fail with a "not found" error.
	cl := newTestClient(t)
	validator := &VRouterBindingCustomValidator{Client: cl}

	if _, err := validator.ValidateUpdate(context.Background(), binding.DeepCopy(), binding); err != nil {
		t.Fatalf("expected deletion-in-progress update to skip ref validation, got error: %v", err)
	}
}

// TestVRouterBindingValidateUpdate_NotDeleting_StillValidatesRefs is the
// complementary case: when the object is not being deleted, ValidateUpdate must
// still reject missing refs (i.e. the skip is deletion-only, not blanket).
func TestVRouterBindingValidateUpdate_NotDeleting_StillValidatesRefs(t *testing.T) {
	binding := &vrouterv1.VRouterBinding{
		ObjectMeta: metaObj("b1"),
		Spec: vrouterv1.VRouterBindingSpec{
			TemplateRefs: []vrouterv1.NameRef{{Name: "missing-template"}},
			TargetRefs:   []vrouterv1.NameRef{{Name: "r1"}},
		},
	}
	target := &vrouterv1.VRouterTarget{ObjectMeta: metaObj("r1")}
	cl := newTestClient(t, target)
	validator := &VRouterBindingCustomValidator{Client: cl}

	_, err := validator.ValidateUpdate(context.Background(), binding.DeepCopy(), binding)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Fatalf("error = %q, want a not-found error", err.Error())
	}
}
