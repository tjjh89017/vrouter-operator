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
	"fmt"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8stypes "k8s.io/apimachinery/pkg/types"

	vrouterv1 "github.com/tjjh89017/vrouter-operator/api/v1"
	providertypes "github.com/tjjh89017/vrouter-operator/internal/provider/types"
)

// staticPidProvider is a minimal types.Provider stub whose ExecScript always
// "succeeds" at dispatch, returning a fixed PID. It is only used to drive
// applyConfig directly; GetExecStatus is never called in these tests.
type staticPidProvider struct {
	pid int64
}

func (staticPidProvider) IsVMRunning(_ context.Context) (bool, error) { return true, nil }
func (staticPidProvider) CheckReady(_ context.Context) error          { return nil }
func (p staticPidProvider) ExecScript(_ context.Context, _, _ string, _ bool) (int64, error) {
	return p.pid, nil
}
func (staticPidProvider) GetExecStatus(_ context.Context, _ int64) (*providertypes.ExecStatus, error) {
	return &providertypes.ExecStatus{Exited: false}, nil
}

// TestApplyConfig_NewGeneration_SetsAppliedFalse is the regression test for
// C15: dispatching a new generation must not leave a stale Applied=True
// condition from a previous generation in place. Without the fix,
// `kubectl wait --for=condition=Applied` would return immediately against
// the old True condition even though the newly dispatched script has not
// run yet.
func TestApplyConfig_NewGeneration_SetsAppliedFalse(t *testing.T) {
	cfg := &vrouterv1.VRouterConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "c1", Namespace: "default", Generation: 2},
		Spec: vrouterv1.VRouterConfigSpec{
			TargetRef: vrouterv1.NameRef{Name: "r1"},
			Config:    "set system host-name test",
		},
		Status: vrouterv1.VRouterConfigStatus{
			Phase:              vrouterv1.PhaseApplied,
			ObservedGeneration: 1,
			Conditions: []metav1.Condition{
				{
					Type:               vrouterv1.ConditionApplied,
					Status:             metav1.ConditionTrue,
					Reason:             "ConfigApplied",
					Message:            "Configuration applied successfully.",
					ObservedGeneration: 1,
					LastTransitionTime: metav1.Now(),
				},
			},
		},
	}

	r, cl := newFakeReconciler(t, cfg)

	if _, err := r.applyConfig(context.Background(), cfg, staticPidProvider{pid: 55}, nil); err != nil {
		t.Fatalf("applyConfig returned error: %v", err)
	}

	if cfg.Status.Phase != vrouterv1.PhaseApplying {
		t.Errorf("status.phase = %q, want %q", cfg.Status.Phase, vrouterv1.PhaseApplying)
	}
	if cfg.Status.ObservedGeneration != 2 {
		t.Errorf("status.observedGeneration = %d, want 2 (the generation being dispatched)", cfg.Status.ObservedGeneration)
	}

	cond := findAppliedCondition(cfg.Status.Conditions)
	if cond == nil {
		t.Fatalf("expected an Applied condition to be set")
	}
	if cond.Status != metav1.ConditionFalse {
		t.Errorf("Applied condition status = %q, want %q (stale True must not survive a new dispatch)", cond.Status, metav1.ConditionFalse)
	}
	if cond.ObservedGeneration != 2 {
		t.Errorf("Applied condition observedGeneration = %d, want 2 (the generation being dispatched)", cond.ObservedGeneration)
	}

	// The persisted object (via the status patch) must reflect the same thing.
	var got vrouterv1.VRouterConfig
	if err := cl.Get(context.Background(), k8stypes.NamespacedName{Name: "c1", Namespace: "default"}, &got); err != nil {
		t.Fatalf("get updated config: %v", err)
	}
	persistedCond := findAppliedCondition(got.Status.Conditions)
	if persistedCond == nil || persistedCond.Status != metav1.ConditionFalse {
		t.Errorf("persisted Applied condition = %+v, want status False", persistedCond)
	}
}

// TestPollExecStatus_Success_StampsAppliedTrueWithDispatchedGeneration
// verifies that once the dispatched exec exits successfully, Applied=True
// is stamped with the generation the exec was actually dispatched for
// (status.observedGeneration), which in the common case (no mid-run spec
// edit) equals cfg.Generation.
func TestPollExecStatus_Success_StampsAppliedTrueWithDispatchedGeneration(t *testing.T) {
	cfg := &vrouterv1.VRouterConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "c1", Namespace: "default", Generation: 3},
		Spec: vrouterv1.VRouterConfigSpec{
			TargetRef: vrouterv1.NameRef{Name: "r1"},
			Config:    "set system host-name test",
		},
		Status: vrouterv1.VRouterConfigStatus{
			Phase:              vrouterv1.PhaseApplying,
			ExecPID:            42,
			ObservedGeneration: 3,
		},
	}

	r, cl := newFakeReconciler(t, cfg)

	if _, err := r.pollExecStatus(context.Background(), cfg, fakeExitedProvider{exitCode: 0}); err != nil {
		t.Fatalf("pollExecStatus returned error: %v", err)
	}

	if cfg.Status.Phase != vrouterv1.PhaseApplied {
		t.Errorf("status.phase = %q, want %q", cfg.Status.Phase, vrouterv1.PhaseApplied)
	}

	cond := findAppliedCondition(cfg.Status.Conditions)
	if cond == nil {
		t.Fatalf("expected an Applied condition to be set")
	}
	if cond.Status != metav1.ConditionTrue {
		t.Errorf("Applied condition status = %q, want %q", cond.Status, metav1.ConditionTrue)
	}
	if cond.ObservedGeneration != cfg.Status.ObservedGeneration {
		t.Errorf("Applied condition observedGeneration = %d, want %d (status.observedGeneration at completion time)", cond.ObservedGeneration, cfg.Status.ObservedGeneration)
	}

	var got vrouterv1.VRouterConfig
	if err := cl.Get(context.Background(), k8stypes.NamespacedName{Name: "c1", Namespace: "default"}, &got); err != nil {
		t.Fatalf("get updated config: %v", err)
	}
	if persistedCond := findAppliedCondition(got.Status.Conditions); persistedCond == nil || persistedCond.ObservedGeneration != 3 {
		t.Errorf("persisted Applied condition = %+v, want observedGeneration 3", persistedCond)
	}
}

// TestPollExecStatus_MidRunGenerationBump_DoesNotStampNeverAppliedGeneration
// covers the "related" part of C15: if the spec is edited while a script is
// still running, cfg.Generation moves ahead of the generation the running
// exec was actually dispatched for. When that exec finishes, the completed
// exec must stamp the Applied condition with the generation it was
// dispatched for (status.observedGeneration), not the newer, never-applied
// cfg.Generation.
func TestPollExecStatus_MidRunGenerationBump_DoesNotStampNeverAppliedGeneration(t *testing.T) {
	cfg := &vrouterv1.VRouterConfig{
		// The spec was edited after dispatch: cfg.Generation (4) has moved
		// ahead of status.ObservedGeneration (3), the generation this
		// in-flight exec was actually dispatched for.
		ObjectMeta: metav1.ObjectMeta{Name: "c1", Namespace: "default", Generation: 4},
		Spec: vrouterv1.VRouterConfigSpec{
			TargetRef: vrouterv1.NameRef{Name: "r1"},
			Config:    "set system host-name test",
		},
		Status: vrouterv1.VRouterConfigStatus{
			Phase:              vrouterv1.PhaseApplying,
			ExecPID:            42,
			ObservedGeneration: 3,
		},
	}

	r, _ := newFakeReconciler(t, cfg)

	if _, err := r.pollExecStatus(context.Background(), cfg, fakeExitedProvider{exitCode: 0}); err != nil {
		t.Fatalf("pollExecStatus returned error: %v", err)
	}

	cond := findAppliedCondition(cfg.Status.Conditions)
	if cond == nil {
		t.Fatalf("expected an Applied condition to be set")
	}
	if cond.ObservedGeneration != 3 {
		t.Errorf("Applied condition observedGeneration = %d, want 3 (the generation this exec was dispatched for, not cfg.Generation=4 which was never applied)", cond.ObservedGeneration)
	}
	if cond.ObservedGeneration == cfg.Generation {
		t.Errorf("Applied condition observedGeneration must not equal the never-applied cfg.Generation (%d)", cfg.Generation)
	}
}

// TestHandleRouterNotReady_PreviouslyApplied_ClearsStaleAppliedTrue is the
// regression test for the CheckReady-failure gap: when the router stops
// answering ready checks (e.g. it is mid-reboot), a config that was
// previously Applied resets phase to Pending because that result can no
// longer be trusted -- but before this fix, the Applied condition was left
// at its stale True value. That would let
// `kubectl wait --for=condition=Applied` report success for the entire
// window the router is unreachable, even though the controller itself no
// longer trusts the previous apply (phase=Pending). The condition must flip
// to False in the same patch that resets phase.
func TestHandleRouterNotReady_PreviouslyApplied_ClearsStaleAppliedTrue(t *testing.T) {
	cfg := &vrouterv1.VRouterConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "c1", Namespace: "default", Generation: 1},
		Spec: vrouterv1.VRouterConfigSpec{
			TargetRef: vrouterv1.NameRef{Name: "r1"},
			Config:    "set system host-name test",
		},
		Status: vrouterv1.VRouterConfigStatus{
			Phase:              vrouterv1.PhaseApplied,
			ObservedGeneration: 1,
			Conditions: []metav1.Condition{
				{
					Type:               vrouterv1.ConditionApplied,
					Status:             metav1.ConditionTrue,
					Reason:             "ConfigApplied",
					Message:            "Configuration applied successfully.",
					ObservedGeneration: 1,
					LastTransitionTime: metav1.Now(),
				},
			},
		},
	}

	r, cl := newFakeReconciler(t, cfg)

	result, err := r.handleRouterNotReady(context.Background(), cfg, fmt.Errorf("QGA not responding"))
	if err != nil {
		t.Fatalf("handleRouterNotReady returned error: %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Fatalf("expected a non-zero RequeueAfter so the reconcile retries once the router is ready again")
	}

	if cfg.Status.Phase != vrouterv1.PhasePending {
		t.Errorf("status.phase = %q, want %q", cfg.Status.Phase, vrouterv1.PhasePending)
	}

	cond := findAppliedCondition(cfg.Status.Conditions)
	if cond == nil {
		t.Fatalf("expected an Applied condition to be set")
	}
	if cond.Status != metav1.ConditionFalse {
		t.Errorf("Applied condition status = %q, want %q (a stale True must not survive the router going unready)", cond.Status, metav1.ConditionFalse)
	}
	if cond.ObservedGeneration != 1 {
		t.Errorf("Applied condition observedGeneration = %d, want 1", cond.ObservedGeneration)
	}

	var got vrouterv1.VRouterConfig
	if err := cl.Get(context.Background(), k8stypes.NamespacedName{Name: "c1", Namespace: "default"}, &got); err != nil {
		t.Fatalf("get updated config: %v", err)
	}
	persistedCond := findAppliedCondition(got.Status.Conditions)
	if persistedCond == nil || persistedCond.Status != metav1.ConditionFalse {
		t.Errorf("persisted Applied condition = %+v, want status False", persistedCond)
	}
}

// TestHandleRouterNotReady_NotPreviouslyApplied_NoStatusPatch verifies that
// when the config was not in the Applied phase (e.g. it is already Pending
// or Applying), handleRouterNotReady does not touch status at all -- there
// is no stale True condition to clear, and patching here would be
// pointless churn.
func TestHandleRouterNotReady_NotPreviouslyApplied_NoStatusPatch(t *testing.T) {
	cfg := &vrouterv1.VRouterConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "c1", Namespace: "default", Generation: 1},
		Spec: vrouterv1.VRouterConfigSpec{
			TargetRef: vrouterv1.NameRef{Name: "r1"},
			Config:    "set system host-name test",
		},
		Status: vrouterv1.VRouterConfigStatus{
			Phase: vrouterv1.PhasePending,
		},
	}

	r, _ := newFakeReconciler(t, cfg)

	if _, err := r.handleRouterNotReady(context.Background(), cfg, fmt.Errorf("QGA not responding")); err != nil {
		t.Fatalf("handleRouterNotReady returned error: %v", err)
	}

	if cfg.Status.Phase != vrouterv1.PhasePending {
		t.Errorf("status.phase = %q, want unchanged %q", cfg.Status.Phase, vrouterv1.PhasePending)
	}
	if cond := findAppliedCondition(cfg.Status.Conditions); cond != nil {
		t.Errorf("expected no Applied condition to be set, got %+v", cond)
	}
}
