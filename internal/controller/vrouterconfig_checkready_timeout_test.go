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
	"errors"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	vrouterv1 "github.com/tjjh89017/vrouter-operator/api/v1"
	providertypes "github.com/tjjh89017/vrouter-operator/internal/provider/types"
)

// fakeCheckReadyFailingProvider is a types.Provider stub whose CheckReady
// always fails, simulating a router that is persistently unreachable (dead,
// or stuck mid-reboot). ExecScript/GetExecStatus are never expected to be
// called by the code paths under test here.
type fakeCheckReadyFailingProvider struct{}

func (fakeCheckReadyFailingProvider) IsVMRunning(_ context.Context) (bool, error) { return true, nil }
func (fakeCheckReadyFailingProvider) CheckReady(_ context.Context) error {
	return errors.New("QGA unreachable")
}
func (fakeCheckReadyFailingProvider) ExecScript(_ context.Context, _, _ string, _ bool) (int64, error) {
	return 0, nil
}
func (fakeCheckReadyFailingProvider) GetExecStatus(_ context.Context, _ int64) (*providertypes.ExecStatus, error) {
	return &providertypes.ExecStatus{Exited: false}, nil
}

// TestCheckReadyAndDispatch_ExecInFlight_TimeoutExpired_FailsApply is the
// regression test for the apply-timeout evaluation gap: CheckReady failing
// on every reconcile while an exec is in flight (status.execPID > 0) used to
// return early via handleRouterNotReady every time, so
// dispatchOrPoll/pollExecStatus -- which owns the apply-timeout evaluation
// -- was never reached, and a config could stay in Applying forever against
// a dead router even though the timeout clock (execStartedTime) exists.
// Once the dispatched exec has been running longer than execApplyTimeout,
// checkReadyAndDispatch must fail the config the same way pollExecStatus's
// own timeout path does: Failed/ApplyTimeout, execPID cleared, condition
// stamped from status.ObservedGeneration.
func TestCheckReadyAndDispatch_ExecInFlight_TimeoutExpired_FailsApply(t *testing.T) {
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
	target := &vrouterv1.VRouterTarget{
		ObjectMeta: metav1.ObjectMeta{Name: "r1", Namespace: "default"},
	}

	r, cl := newFakeReconciler(t, cfg)

	result, err := r.checkReadyAndDispatch(context.Background(), cfg, fakeCheckReadyFailingProvider{}, target)
	if err != nil {
		t.Fatalf("checkReadyAndDispatch returned error, want nil (timeout must not be treated as a hard error to retry): %v", err)
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
	if cond.Reason != "ApplyTimeout" {
		t.Errorf("Applied condition reason = %q, want %q", cond.Reason, "ApplyTimeout")
	}
	if cond.ObservedGeneration != 3 {
		t.Errorf("Applied condition observedGeneration = %d, want 3 (status.observedGeneration)", cond.ObservedGeneration)
	}
}

// TestCheckReadyAndDispatch_ExecInFlight_TimeoutNotExpired_PreservesRequeue
// pins the "not yet timed out" side of the same interaction: CheckReady
// failing with an exec in flight that started recently must preserve the
// pre-existing behavior (requeue via handleRouterNotReady), not fail the
// config early.
func TestCheckReadyAndDispatch_ExecInFlight_TimeoutNotExpired_PreservesRequeue(t *testing.T) {
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
	target := &vrouterv1.VRouterTarget{
		ObjectMeta: metav1.ObjectMeta{Name: "r1", Namespace: "default"},
	}

	r, cl := newFakeReconciler(t, cfg)

	result, err := r.checkReadyAndDispatch(context.Background(), cfg, fakeCheckReadyFailingProvider{}, target)
	if err != nil {
		t.Fatalf("checkReadyAndDispatch returned error, want nil: %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Errorf("result = %+v, want RequeueAfter > 0 (still within the timeout window, must keep retrying via handleRouterNotReady)", result)
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

// TestCheckReadyAndDispatch_NilExecStartedTime_StampsClockThenBoundsTimeout
// is the regression test for the upgrade edge: a config left mid-apply by a
// controller version that predates status.execStartedTime has execPID > 0
// but execStartedTime == nil. The chosen behavior is to stamp
// execStartedTime to "now" on first observation of that state (starting the
// timeout clock late, rather than never), which this test pins in two
// steps: the first reconcile must stamp the clock and must NOT fail
// immediately (the clock just started); a later reconcile, once that
// stamped time is far enough in the past, must then time out exactly like
// any other stuck exec.
func TestCheckReadyAndDispatch_NilExecStartedTime_StampsClockThenBoundsTimeout(t *testing.T) {
	cfg := &vrouterv1.VRouterConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "c3", Namespace: "default", Generation: 1},
		Spec: vrouterv1.VRouterConfigSpec{
			TargetRef: vrouterv1.NameRef{Name: "r1"},
			Config:    "set system host-name test",
		},
		Status: vrouterv1.VRouterConfigStatus{
			Phase:              vrouterv1.PhaseApplying,
			ExecPID:            42,
			ExecStartedTime:    nil,
			ObservedGeneration: 1,
		},
	}
	target := &vrouterv1.VRouterTarget{
		ObjectMeta: metav1.ObjectMeta{Name: "r1", Namespace: "default"},
	}

	r, cl := newFakeReconciler(t, cfg)

	// Step 1: first observation of execPID>0 with no recorded start time.
	result, err := r.checkReadyAndDispatch(context.Background(), cfg, fakeCheckReadyFailingProvider{}, target)
	if err != nil {
		t.Fatalf("checkReadyAndDispatch returned error, want nil: %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Errorf("result = %+v, want RequeueAfter > 0 (clock just started, must not fail immediately)", result)
	}

	var got vrouterv1.VRouterConfig
	if err := cl.Get(context.Background(), k8stypes.NamespacedName{Name: "c3", Namespace: "default"}, &got); err != nil {
		t.Fatalf("get updated config: %v", err)
	}
	if got.Status.Phase != vrouterv1.PhaseApplying {
		t.Errorf("status.phase = %q, want unchanged %q (must not fail immediately after backfilling the clock)", got.Status.Phase, vrouterv1.PhaseApplying)
	}
	if got.Status.ExecStartedTime == nil {
		t.Fatalf("status.execStartedTime = nil, want backfilled to a non-nil value")
	}
	if since := time.Since(got.Status.ExecStartedTime.Time); since < 0 || since > time.Minute {
		t.Errorf("status.execStartedTime = %v, want stamped to approximately now", got.Status.ExecStartedTime.Time)
	}

	// Step 2: simulate the backfilled clock having run out, as if the router
	// stayed unreachable for the entire execApplyTimeout window since the
	// backfill.
	longAgo := metav1.NewTime(time.Now().Add(-2 * execApplyTimeout))
	patch := client.MergeFrom(got.DeepCopy())
	got.Status.ExecStartedTime = &longAgo
	if err := cl.Status().Patch(context.Background(), &got, patch); err != nil {
		t.Fatalf("patch execStartedTime for step 2: %v", err)
	}

	result, err = r.checkReadyAndDispatch(context.Background(), &got, fakeCheckReadyFailingProvider{}, target)
	if err != nil {
		t.Fatalf("checkReadyAndDispatch (step 2) returned error, want nil: %v", err)
	}
	if result.RequeueAfter != 0 {
		t.Errorf("result = %+v, want no requeue-for-retry once the backfilled clock has expired", result)
	}

	if err := cl.Get(context.Background(), k8stypes.NamespacedName{Name: "c3", Namespace: "default"}, &got); err != nil {
		t.Fatalf("get updated config (step 2): %v", err)
	}
	if got.Status.Phase != vrouterv1.PhaseFailed {
		t.Errorf("status.phase = %q, want %q (backfilled clock must still bound the exec)", got.Status.Phase, vrouterv1.PhaseFailed)
	}
	if got.Status.ExecPID != 0 {
		t.Errorf("status.execPID = %d, want 0 (cleared)", got.Status.ExecPID)
	}
}
