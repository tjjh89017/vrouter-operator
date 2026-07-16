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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8stypes "k8s.io/apimachinery/pkg/types"

	vrouterv1 "github.com/tjjh89017/vrouter-operator/api/v1"
	providertypes "github.com/tjjh89017/vrouter-operator/internal/provider/types"
)

// fakeOutputProvider is a types.Provider stub whose GetExecStatus reports the
// exec as exited with a fixed exit code and a fixed stdout/stderr, so tests can
// drive the output-marker scanning path in pollExecStatus.
type fakeOutputProvider struct {
	exitCode int
	stdout   string
	stderr   string
}

func (fakeOutputProvider) IsVMRunning(_ context.Context) (bool, error) { return true, nil }
func (fakeOutputProvider) CheckReady(_ context.Context) error          { return nil }
func (fakeOutputProvider) ExecScript(_ context.Context, _, _ string, _ bool) (int64, error) {
	return 0, nil
}
func (p fakeOutputProvider) GetExecStatus(_ context.Context, _ int64) (*providertypes.ExecStatus, error) {
	return &providertypes.ExecStatus{Exited: true, ExitCode: p.exitCode, Stdout: p.stdout, Stderr: p.stderr}, nil
}

// TestPollExecStatus_ZeroExitWithFailureMarker_SetsAppliedFalse is the
// regression test for the exit-code masking bug: a set-time validation failure
// prints "Set failed" to the apply output but `commit` still exits 0 (the
// preceding `load` left a committable diff). The exit code lies, so the
// operator must also scan the output for a failure marker and report Failed —
// otherwise it would wrongly report Applied.
func TestPollExecStatus_ZeroExitWithFailureMarker_SetsAppliedFalse(t *testing.T) {
	cfg := &vrouterv1.VRouterConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "c1", Namespace: "default", Generation: 1},
		Spec: vrouterv1.VRouterConfigSpec{
			TargetRef: vrouterv1.NameRef{Name: "r1"},
			Config:    "set interfaces ethernet eth0 address 'not-an-ip'",
		},
		Status: vrouterv1.VRouterConfigStatus{
			Phase:              vrouterv1.PhaseApplying,
			ExecPID:            42,
			ObservedGeneration: 1,
		},
	}

	r, cl := newFakeReconciler(t, cfg)

	prov := fakeOutputProvider{
		exitCode: 0,
		stdout: "Load complete. Use 'commit' to make changes effective.\n" +
			"  Error: not-an-ip is not a valid network interface address\n" +
			"  Invalid value\n  Value validation failed\n  Set failed",
	}

	if _, err := r.pollExecStatus(context.Background(), cfg, prov); err != nil {
		t.Fatalf("pollExecStatus returned error: %v", err)
	}

	if cfg.Status.Phase != vrouterv1.PhaseFailed {
		t.Errorf("status.phase = %q, want %q (marker in output must fail the apply despite exitCode=0)",
			cfg.Status.Phase, vrouterv1.PhaseFailed)
	}

	cond := findAppliedCondition(cfg.Status.Conditions)
	if cond == nil {
		t.Fatalf("expected an Applied condition to be set")
	}
	if cond.Status != metav1.ConditionFalse {
		t.Errorf("Applied condition status = %q, want %q", cond.Status, metav1.ConditionFalse)
	}
	if cond.Reason != "ConfigFailed" {
		t.Errorf("Applied condition reason = %q, want %q", cond.Reason, "ConfigFailed")
	}

	var got vrouterv1.VRouterConfig
	if err := cl.Get(context.Background(), k8stypes.NamespacedName{Name: "c1", Namespace: "default"}, &got); err != nil {
		t.Fatalf("get updated config: %v", err)
	}
	if got.Status.Phase != vrouterv1.PhaseFailed {
		t.Errorf("persisted status.phase = %q, want %q", got.Status.Phase, vrouterv1.PhaseFailed)
	}
	if persistedCond := findAppliedCondition(got.Status.Conditions); persistedCond == nil ||
		persistedCond.Status != metav1.ConditionFalse {
		t.Errorf("persisted Applied condition = %+v, want status False", persistedCond)
	}
}

// TestPollExecStatus_ZeroExitCleanOutput_StaysApplied guards the happy path:
// a clean exit-0 apply whose output contains no failure marker must still
// reach Applied — the marker scan must not produce false positives.
func TestPollExecStatus_ZeroExitCleanOutput_StaysApplied(t *testing.T) {
	cfg := &vrouterv1.VRouterConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "c1", Namespace: "default", Generation: 1},
		Spec: vrouterv1.VRouterConfigSpec{
			TargetRef: vrouterv1.NameRef{Name: "r1"},
			Config:    "set system host-name test",
		},
		Status: vrouterv1.VRouterConfigStatus{
			Phase:              vrouterv1.PhaseApplying,
			ExecPID:            42,
			ObservedGeneration: 1,
		},
	}

	r, _ := newFakeReconciler(t, cfg)

	prov := fakeOutputProvider{
		exitCode: 0,
		stdout:   "Load complete. Use 'commit' to make changes effective.",
	}

	if _, err := r.pollExecStatus(context.Background(), cfg, prov); err != nil {
		t.Fatalf("pollExecStatus returned error: %v", err)
	}

	if cfg.Status.Phase != vrouterv1.PhaseApplied {
		t.Errorf("status.phase = %q, want %q", cfg.Status.Phase, vrouterv1.PhaseApplied)
	}
	cond := findAppliedCondition(cfg.Status.Conditions)
	if cond == nil || cond.Status != metav1.ConditionTrue {
		t.Errorf("Applied condition = %+v, want status True", cond)
	}
}
