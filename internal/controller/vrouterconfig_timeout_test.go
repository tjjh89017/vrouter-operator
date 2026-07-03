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
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8stypes "k8s.io/apimachinery/pkg/types"

	vrouterv1 "github.com/tjjh89017/vrouter-operator/api/v1"
	providertypes "github.com/tjjh89017/vrouter-operator/internal/provider/types"
)

// fakeNeverExitingProvider is a types.Provider stub whose GetExecStatus
// always reports the dispatched exec as still running (Exited: false),
// simulating an apply script that hangs forever (e.g. a blocked commit
// lock, or a command that never returns).
type fakeNeverExitingProvider struct{}

func (fakeNeverExitingProvider) IsVMRunning(_ context.Context) (bool, error) { return true, nil }
func (fakeNeverExitingProvider) CheckReady(_ context.Context) error          { return nil }
func (fakeNeverExitingProvider) ExecScript(_ context.Context, _, _ string, _ bool) (int64, error) {
	return 0, nil
}
func (fakeNeverExitingProvider) GetExecStatus(_ context.Context, _ int64) (*providertypes.ExecStatus, error) {
	return &providertypes.ExecStatus{Exited: false}, nil
}

// TestPollExecStatus_StuckExec_TimesOutToFailed is the regression test for
// B10: an apply script that never exits must not pin the config in
// Applying forever. Once the dispatched exec has been running longer than
// execApplyTimeout, pollExecStatus must fail the config, clear execPID (so
// a later generation change or reboot can re-dispatch through the normal
// path), and must NOT requeue for retry -- consistent with "Failed has no
// auto-retry".
func TestPollExecStatus_StuckExec_TimesOutToFailed(t *testing.T) {
	startedLongAgo := metav1.NewTime(time.Now().Add(-2 * execApplyTimeout))
	cfg := &vrouterv1.VRouterConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "c1", Namespace: "default", Generation: 3},
		Spec: vrouterv1.VRouterConfigSpec{
			TargetRef: vrouterv1.NameRef{Name: "r1"},
			Config:    "set system host-name test",
		},
		Status: vrouterv1.VRouterConfigStatus{
			Phase:              vrouterv1.PhaseApplying,
			ExecPID:            42,
			ExecStartedTime:    &startedLongAgo,
			ObservedGeneration: 3,
		},
	}

	r, cl := newFakeReconciler(t, cfg)

	result, err := r.pollExecStatus(context.Background(), cfg, fakeNeverExitingProvider{})
	if err != nil {
		t.Fatalf("pollExecStatus returned error, want nil (timeout must not be treated as a hard error to retry): %v", err)
	}
	if result.RequeueAfter != 0 {
		t.Errorf("result = %+v, want no requeue-for-retry after a timeout (Failed has no auto-retry)", result)
	}

	var got vrouterv1.VRouterConfig
	if err := cl.Get(context.Background(), k8stypes.NamespacedName{Name: "c1", Namespace: "default"}, &got); err != nil {
		t.Fatalf("get updated config: %v", err)
	}
	if got.Status.Phase != vrouterv1.PhaseFailed {
		t.Errorf("status.phase = %q, want %q", got.Status.Phase, vrouterv1.PhaseFailed)
	}
	if got.Status.ExecPID != 0 {
		t.Errorf("status.execPID = %d, want 0 (cleared so a later change can re-dispatch)", got.Status.ExecPID)
	}
	if got.Status.ExecStartedTime != nil {
		t.Errorf("status.execStartedTime = %v, want nil (cleared)", got.Status.ExecStartedTime)
	}
	cond := findAppliedCondition(got.Status.Conditions)
	if cond == nil {
		t.Fatalf("expected an Applied condition to be set")
	}
	if cond.Status != metav1.ConditionFalse {
		t.Errorf("Applied condition status = %q, want %q", cond.Status, metav1.ConditionFalse)
	}
}

// TestPollExecStatus_StuckExec_WithinTimeout_StillPolls verifies the
// timeout does not fire early: an exec dispatched recently (well within
// execApplyTimeout) that has not exited yet must keep polling exactly as
// before the fix, not be failed prematurely.
func TestPollExecStatus_StuckExec_WithinTimeout_StillPolls(t *testing.T) {
	startedRecently := metav1.NewTime(time.Now().Add(-1 * time.Second))
	cfg := &vrouterv1.VRouterConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "c2", Namespace: "default", Generation: 1},
		Spec: vrouterv1.VRouterConfigSpec{
			TargetRef: vrouterv1.NameRef{Name: "r1"},
			Config:    "set system host-name test",
		},
		Status: vrouterv1.VRouterConfigStatus{
			Phase:              vrouterv1.PhaseApplying,
			ExecPID:            42,
			ExecStartedTime:    &startedRecently,
			ObservedGeneration: 1,
		},
	}

	r, cl := newFakeReconciler(t, cfg)

	result, err := r.pollExecStatus(context.Background(), cfg, fakeNeverExitingProvider{})
	if err != nil {
		t.Fatalf("pollExecStatus returned error, want nil: %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Errorf("result = %+v, want RequeueAfter > 0 (still polling within the timeout window)", result)
	}

	var got vrouterv1.VRouterConfig
	if err := cl.Get(context.Background(), k8stypes.NamespacedName{Name: "c2", Namespace: "default"}, &got); err != nil {
		t.Fatalf("get updated config: %v", err)
	}
	if got.Status.Phase != vrouterv1.PhaseApplying {
		t.Errorf("status.phase = %q, want unchanged %q (must not time out early)", got.Status.Phase, vrouterv1.PhaseApplying)
	}
	if got.Status.ExecPID != 42 {
		t.Errorf("status.execPID = %d, want unchanged 42", got.Status.ExecPID)
	}
}

// TestPollExecStatus_ExitedWithinWindow_SucceedsNormally verifies the
// normal success path (exec exits with code 0 before the timeout) is
// unchanged by the timeout logic.
func TestPollExecStatus_ExitedWithinWindow_SucceedsNormally(t *testing.T) {
	startedRecently := metav1.NewTime(time.Now().Add(-1 * time.Second))
	cfg := &vrouterv1.VRouterConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "c3", Namespace: "default", Generation: 1},
		Spec: vrouterv1.VRouterConfigSpec{
			TargetRef: vrouterv1.NameRef{Name: "r1"},
			Config:    "set system host-name test",
		},
		Status: vrouterv1.VRouterConfigStatus{
			Phase:              vrouterv1.PhaseApplying,
			ExecPID:            42,
			ExecStartedTime:    &startedRecently,
			ObservedGeneration: 1,
		},
	}

	r, cl := newFakeReconciler(t, cfg)

	exitedOK := fakeExitedProvider{exitCode: 0}
	result, err := r.pollExecStatus(context.Background(), cfg, exitedOK)
	if err != nil {
		t.Fatalf("pollExecStatus returned error, want nil: %v", err)
	}
	if result.RequeueAfter != 0 {
		t.Errorf("result = %+v, want no requeue on a completed success", result)
	}

	var got vrouterv1.VRouterConfig
	if err := cl.Get(context.Background(), k8stypes.NamespacedName{Name: "c3", Namespace: "default"}, &got); err != nil {
		t.Fatalf("get updated config: %v", err)
	}
	if got.Status.Phase != vrouterv1.PhaseApplied {
		t.Errorf("status.phase = %q, want %q", got.Status.Phase, vrouterv1.PhaseApplied)
	}
	if got.Status.ExecPID != 0 {
		t.Errorf("status.execPID = %d, want 0", got.Status.ExecPID)
	}
	if got.Status.ExecStartedTime != nil {
		t.Errorf("status.execStartedTime = %v, want nil (cleared on exit)", got.Status.ExecStartedTime)
	}
}

// fakeExitedProvider is a types.Provider stub whose GetExecStatus always
// reports the exec as exited with a fixed exit code.
type fakeExitedProvider struct {
	exitCode int
}

func (fakeExitedProvider) IsVMRunning(_ context.Context) (bool, error) { return true, nil }
func (fakeExitedProvider) CheckReady(_ context.Context) error          { return nil }
func (fakeExitedProvider) ExecScript(_ context.Context, _, _ string, _ bool) (int64, error) {
	return 0, nil
}
func (p fakeExitedProvider) GetExecStatus(_ context.Context, _ int64) (*providertypes.ExecStatus, error) {
	return &providertypes.ExecStatus{Exited: true, ExitCode: p.exitCode}, nil
}

// findAppliedCondition returns the Applied condition from a condition list,
// or nil if not present.
func findAppliedCondition(conditions []metav1.Condition) *metav1.Condition {
	for i := range conditions {
		if conditions[i].Type == vrouterv1.ConditionApplied {
			return &conditions[i]
		}
	}
	return nil
}
