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

// These tests cover validateBinding's handling of spec.rollout, per issue #35:
// mode: FixedInterval requires waitInterval > 0; fields belonging to the other
// mode are ignored; Disabled (absent/empty/unset mode) skips rollout checks
// entirely. They reuse newTestClient/metaObj/testNamespace from crossns_test.go.

// rolloutTestBinding builds a binding with valid templateRefs/targetRefs (so
// validateBinding reaches the rollout check) and the given rollout spec.
func rolloutTestBinding(rollout *vrouterv1.RolloutSpec) *vrouterv1.VRouterBinding {
	return &vrouterv1.VRouterBinding{
		ObjectMeta: metaObj("b1"),
		Spec: vrouterv1.VRouterBindingSpec{
			TemplateRefs: []vrouterv1.NameRef{{Name: "tmpl1"}},
			TargetRefs:   []vrouterv1.NameRef{{Name: "r1"}},
			Rollout:      rollout,
		},
	}
}

func TestValidateBinding_Rollout_FixedInterval_NoWaitInterval_Rejected(t *testing.T) {
	tmpl := &vrouterv1.VRouterTemplate{ObjectMeta: metaObj("tmpl1")}
	r1 := &vrouterv1.VRouterTarget{ObjectMeta: metaObj("r1")}
	cl := newTestClient(t, tmpl, r1)

	binding := rolloutTestBinding(&vrouterv1.RolloutSpec{
		Mode: vrouterv1.RolloutModeFixedInterval,
	})

	err := validateBinding(context.Background(), cl, binding)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "spec.rollout.waitInterval") {
		t.Fatalf("error = %q, want it to mention spec.rollout.waitInterval", err.Error())
	}
}

func TestValidateBinding_Rollout_FixedInterval_ZeroWaitInterval_Rejected(t *testing.T) {
	tmpl := &vrouterv1.VRouterTemplate{ObjectMeta: metaObj("tmpl1")}
	r1 := &vrouterv1.VRouterTarget{ObjectMeta: metaObj("r1")}
	cl := newTestClient(t, tmpl, r1)

	binding := rolloutTestBinding(&vrouterv1.RolloutSpec{
		Mode:         vrouterv1.RolloutModeFixedInterval,
		WaitInterval: metav1.Duration{Duration: 0},
	})

	err := validateBinding(context.Background(), cl, binding)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "spec.rollout.waitInterval") {
		t.Fatalf("error = %q, want it to mention spec.rollout.waitInterval", err.Error())
	}
}

func TestValidateBinding_Rollout_FixedInterval_NegativeWaitInterval_Rejected(t *testing.T) {
	tmpl := &vrouterv1.VRouterTemplate{ObjectMeta: metaObj("tmpl1")}
	r1 := &vrouterv1.VRouterTarget{ObjectMeta: metaObj("r1")}
	cl := newTestClient(t, tmpl, r1)

	binding := rolloutTestBinding(&vrouterv1.RolloutSpec{
		Mode:         vrouterv1.RolloutModeFixedInterval,
		WaitInterval: metav1.Duration{Duration: -1},
	})

	err := validateBinding(context.Background(), cl, binding)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "spec.rollout.waitInterval") {
		t.Fatalf("error = %q, want it to mention spec.rollout.waitInterval", err.Error())
	}
}

func TestValidateBinding_Rollout_FixedInterval_PositiveWaitInterval_Accepted(t *testing.T) {
	tmpl := &vrouterv1.VRouterTemplate{ObjectMeta: metaObj("tmpl1")}
	r1 := &vrouterv1.VRouterTarget{ObjectMeta: metaObj("r1")}
	cl := newTestClient(t, tmpl, r1)

	binding := rolloutTestBinding(&vrouterv1.RolloutSpec{
		Mode:         vrouterv1.RolloutModeFixedInterval,
		WaitInterval: metav1.Duration{Duration: 60_000_000_000}, // 60s
	})

	if err := validateBinding(context.Background(), cl, binding); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}

// TestValidateBinding_Rollout_WaitForApplied_NoWaitInterval_Accepted proves
// waitInterval (a FixedInterval-only field) is ignored under WaitForApplied:
// leaving it unset must not be rejected.
func TestValidateBinding_Rollout_WaitForApplied_NoWaitInterval_Accepted(t *testing.T) {
	tmpl := &vrouterv1.VRouterTemplate{ObjectMeta: metaObj("tmpl1")}
	r1 := &vrouterv1.VRouterTarget{ObjectMeta: metaObj("r1")}
	cl := newTestClient(t, tmpl, r1)

	binding := rolloutTestBinding(&vrouterv1.RolloutSpec{
		Mode: vrouterv1.RolloutModeWaitForApplied,
	})

	if err := validateBinding(context.Background(), cl, binding); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}

// TestValidateBinding_Rollout_WaitForApplied_NegativeWaitInterval_Ignored proves
// a negative waitInterval (FixedInterval-only field) does not get rejected
// under WaitForApplied either, since it belongs to the other mode.
func TestValidateBinding_Rollout_WaitForApplied_NegativeWaitInterval_Ignored(t *testing.T) {
	tmpl := &vrouterv1.VRouterTemplate{ObjectMeta: metaObj("tmpl1")}
	r1 := &vrouterv1.VRouterTarget{ObjectMeta: metaObj("r1")}
	cl := newTestClient(t, tmpl, r1)

	binding := rolloutTestBinding(&vrouterv1.RolloutSpec{
		Mode:         vrouterv1.RolloutModeWaitForApplied,
		WaitInterval: metav1.Duration{Duration: -1},
	})

	if err := validateBinding(context.Background(), cl, binding); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}

func TestValidateBinding_Rollout_WaitForApplied_NegativePollInterval_Rejected(t *testing.T) {
	tmpl := &vrouterv1.VRouterTemplate{ObjectMeta: metaObj("tmpl1")}
	r1 := &vrouterv1.VRouterTarget{ObjectMeta: metaObj("r1")}
	cl := newTestClient(t, tmpl, r1)

	binding := rolloutTestBinding(&vrouterv1.RolloutSpec{
		Mode:         vrouterv1.RolloutModeWaitForApplied,
		PollInterval: metav1.Duration{Duration: -1},
	})

	err := validateBinding(context.Background(), cl, binding)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "spec.rollout.pollInterval") {
		t.Fatalf("error = %q, want it to mention spec.rollout.pollInterval", err.Error())
	}
}

func TestValidateBinding_Rollout_WaitForApplied_NegativeWaitAfterApplied_Rejected(t *testing.T) {
	tmpl := &vrouterv1.VRouterTemplate{ObjectMeta: metaObj("tmpl1")}
	r1 := &vrouterv1.VRouterTarget{ObjectMeta: metaObj("r1")}
	cl := newTestClient(t, tmpl, r1)

	binding := rolloutTestBinding(&vrouterv1.RolloutSpec{
		Mode:             vrouterv1.RolloutModeWaitForApplied,
		WaitAfterApplied: metav1.Duration{Duration: -1},
	})

	err := validateBinding(context.Background(), cl, binding)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "spec.rollout.waitAfterApplied") {
		t.Fatalf("error = %q, want it to mention spec.rollout.waitAfterApplied", err.Error())
	}
}

func TestValidateBinding_Rollout_Absent_Accepted(t *testing.T) {
	tmpl := &vrouterv1.VRouterTemplate{ObjectMeta: metaObj("tmpl1")}
	r1 := &vrouterv1.VRouterTarget{ObjectMeta: metaObj("r1")}
	cl := newTestClient(t, tmpl, r1)

	binding := rolloutTestBinding(nil)

	if err := validateBinding(context.Background(), cl, binding); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}

func TestValidateBinding_Rollout_Empty_Accepted(t *testing.T) {
	tmpl := &vrouterv1.VRouterTemplate{ObjectMeta: metaObj("tmpl1")}
	r1 := &vrouterv1.VRouterTarget{ObjectMeta: metaObj("r1")}
	cl := newTestClient(t, tmpl, r1)

	binding := rolloutTestBinding(&vrouterv1.RolloutSpec{})

	if err := validateBinding(context.Background(), cl, binding); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}

// TestValidateBinding_Rollout_ModeDisabledExplicit_IgnoresInvalidFields proves
// an explicit mode: Disabled skips rollout checks entirely (no checks at all),
// same as absent/empty, even when other fields hold values that would be
// rejected under another mode.
func TestValidateBinding_Rollout_ModeDisabledExplicit_IgnoresInvalidFields(t *testing.T) {
	tmpl := &vrouterv1.VRouterTemplate{ObjectMeta: metaObj("tmpl1")}
	r1 := &vrouterv1.VRouterTarget{ObjectMeta: metaObj("r1")}
	cl := newTestClient(t, tmpl, r1)

	binding := rolloutTestBinding(&vrouterv1.RolloutSpec{
		Mode:         vrouterv1.RolloutModeDisabled,
		WaitInterval: metav1.Duration{Duration: -1},
		PollInterval: metav1.Duration{Duration: -1},
	})

	if err := validateBinding(context.Background(), cl, binding); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}
