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

// These tests cover the ref-existence path of validateVRouterConfig (same
// namespace, so cross-namespace rejection - already covered by crossns_test.go -
// never triggers). They reuse newTestClient/metaObj/testNamespace from
// crossns_test.go.

func TestValidateVRouterConfig_SameNamespaceTargetRef_Missing_Rejected(t *testing.T) {
	cfg := &vrouterv1.VRouterConfig{
		ObjectMeta: metaObj("c1"),
		Spec: vrouterv1.VRouterConfigSpec{
			TargetRef: vrouterv1.NameRef{Name: "missing-target"},
		},
	}
	cl := newTestClient(t)

	err := validateVRouterConfig(context.Background(), cl, cfg)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !strings.Contains(err.Error(), `spec.targetRef "missing-target" not found`) {
		t.Fatalf("error = %q, want a not-found error for the missing targetRef", err.Error())
	}
}

func TestValidateVRouterConfig_SameNamespaceTargetRef_Exists_Accepted(t *testing.T) {
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

// TestVRouterConfigValidateCreate_MissingTargetRef_Rejected exercises the
// webhook method's ValidateCreate wiring (not just the bare helper).
func TestVRouterConfigValidateCreate_MissingTargetRef_Rejected(t *testing.T) {
	cfg := &vrouterv1.VRouterConfig{
		ObjectMeta: metaObj("c1"),
		Spec: vrouterv1.VRouterConfigSpec{
			TargetRef: vrouterv1.NameRef{Name: "missing-target"},
		},
	}
	cl := newTestClient(t)
	validator := &VRouterConfigCustomValidator{Client: cl}

	_, err := validator.ValidateCreate(context.Background(), cfg)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Fatalf("error = %q, want a not-found error", err.Error())
	}
}

// TestVRouterConfigValidateUpdate_DeletionInProgress_SkipsTargetRefValidation
// exercises the webhook method (not the bare validateVRouterConfig helper)
// because the deletion-timestamp skip lives in ValidateUpdate itself.
func TestVRouterConfigValidateUpdate_DeletionInProgress_SkipsTargetRefValidation(t *testing.T) {
	now := metav1.Now()
	cfg := &vrouterv1.VRouterConfig{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:         testNamespace,
			Name:              "c1",
			DeletionTimestamp: &now,
			Finalizers:        []string{"vrouter.kojuro.date/finalizer"},
		},
		Spec: vrouterv1.VRouterConfigSpec{
			TargetRef: vrouterv1.NameRef{Name: "now-missing-target"},
		},
	}
	// Empty client: the targetRef does not resolve. If the deletion skip were
	// not honored, this would fail with a "not found" error.
	cl := newTestClient(t)
	validator := &VRouterConfigCustomValidator{Client: cl}

	if _, err := validator.ValidateUpdate(context.Background(), cfg.DeepCopy(), cfg); err != nil {
		t.Fatalf("expected deletion-in-progress update to skip targetRef validation, got error: %v", err)
	}
}

// TestVRouterConfigValidateUpdate_NotDeleting_StillValidatesTargetRef is the
// complementary case: when the object is not being deleted, ValidateUpdate must
// still reject a missing targetRef (i.e. the skip is deletion-only, not blanket).
func TestVRouterConfigValidateUpdate_NotDeleting_StillValidatesTargetRef(t *testing.T) {
	cfg := &vrouterv1.VRouterConfig{
		ObjectMeta: metaObj("c1"),
		Spec: vrouterv1.VRouterConfigSpec{
			TargetRef: vrouterv1.NameRef{Name: "missing-target"},
		},
	}
	cl := newTestClient(t)
	validator := &VRouterConfigCustomValidator{Client: cl}

	_, err := validator.ValidateUpdate(context.Background(), cfg.DeepCopy(), cfg)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Fatalf("error = %q, want a not-found error", err.Error())
	}
}
