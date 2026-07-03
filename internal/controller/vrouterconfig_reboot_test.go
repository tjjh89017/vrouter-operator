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

// TestShouldForceReapplyForReboot pins the pure decision function used by
// dispatchOrPoll: a reboot forces a re-apply only when the target has
// rebooted more recently than the last reboot-forced dispatch we already
// recorded. This is the core fix for B9 (reboot detection + a failing apply
// looping forever): the same rebootTime must not keep firing true.
func TestShouldForceReapplyForReboot(t *testing.T) {
	t0 := metav1.NewTime(time.Unix(1000, 0))
	t1 := metav1.NewTime(time.Unix(2000, 0)) // newer than t0

	tests := []struct {
		name                  string
		lastRebootHandledTime *metav1.Time
		targetLastRebootTime  *metav1.Time
		want                  bool
	}{
		{
			name:                  "no reboot recorded on target",
			lastRebootHandledTime: nil,
			targetLastRebootTime:  nil,
			want:                  false,
		},
		{
			name:                  "first ever reboot, never handled",
			lastRebootHandledTime: nil,
			targetLastRebootTime:  &t0,
			want:                  true,
		},
		{
			name:                  "same reboot already handled (e.g. a failed apply attempt)",
			lastRebootHandledTime: &t0,
			targetLastRebootTime:  &t0,
			want:                  false,
		},
		{
			name:                  "newer reboot than the one already handled",
			lastRebootHandledTime: &t0,
			targetLastRebootTime:  &t1,
			want:                  true,
		},
		{
			name:                  "handled time newer than target reboot time (stale target read)",
			lastRebootHandledTime: &t1,
			targetLastRebootTime:  &t0,
			want:                  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shouldForceReapplyForReboot(tt.lastRebootHandledTime, tt.targetLastRebootTime)
			if got != tt.want {
				t.Errorf("shouldForceReapplyForReboot(%v, %v) = %v, want %v",
					tt.lastRebootHandledTime, tt.targetLastRebootTime, got, tt.want)
			}
		})
	}
}

// countingFailingProvider is a fake types.Provider whose ExecScript always
// "succeeds" at dispatch (returns a PID) but whose GetExecStatus always
// reports the script exited with a non-zero exit code, simulating a
// render/apply script that keeps failing against the router. execCount
// tracks how many times ExecScript was actually invoked, which is exactly
// what must NOT grow without bound across repeated reconciles of the same
// reboot.
type countingFailingProvider struct {
	execCount int
}

func (p *countingFailingProvider) IsVMRunning(_ context.Context) (bool, error) { return true, nil }
func (p *countingFailingProvider) CheckReady(_ context.Context) error          { return nil }
func (p *countingFailingProvider) ExecScript(_ context.Context, _, _ string, _ bool) (int64, error) {
	p.execCount++
	return 100 + int64(p.execCount), nil
}
func (p *countingFailingProvider) GetExecStatus(_ context.Context, _ int64) (*providertypes.ExecStatus, error) {
	return &providertypes.ExecStatus{Exited: true, ExitCode: 1, Stderr: "bad config"}, nil
}

// TestDispatchOrPoll_RebootWithFailingApply_DoesNotLoop is the regression
// test for B9. It drives the same dispatchOrPoll/pollExecStatus path that
// onChange uses across three simulated reconciles:
//
//  1. A target reboot with never handled forces a dispatch. The apply fails.
//  2. A second reconcile with the SAME reboot time must NOT re-dispatch —
//     without the fix, LastAppliedTime stays nil forever on a failing apply
//     and the old check kept forcing effectiveObservedGen=0 every time.
//  3. A genuinely NEWER reboot time must force exactly one fresh re-apply.
func TestDispatchOrPoll_RebootWithFailingApply_DoesNotLoop(t *testing.T) {
	ctx := context.Background()
	rebootTime := metav1.NewTime(time.Unix(1_700_000_000, 0))

	cfg := &vrouterv1.VRouterConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "c1", Namespace: "default", Generation: 1},
		Spec: vrouterv1.VRouterConfigSpec{
			TargetRef: vrouterv1.NameRef{Name: "r1"},
			Config:    "set system host-name test",
		},
	}
	target := &vrouterv1.VRouterTarget{
		ObjectMeta: metav1.ObjectMeta{Name: "r1", Namespace: "default"},
		Status:     vrouterv1.VRouterTargetStatus{LastRebootTime: &rebootTime},
	}

	r, cl := newFakeReconciler(t, cfg)
	prov := &countingFailingProvider{}

	// --- Reconcile 1: reboot never handled before -> forces one dispatch.
	if _, err := r.dispatchOrPoll(ctx, cfg, prov, target); err != nil {
		t.Fatalf("reconcile 1 (dispatch): %v", err)
	}
	if prov.execCount != 1 {
		t.Fatalf("after reconcile 1 dispatch: execCount = %d, want 1", prov.execCount)
	}
	// The script "runs" and is picked up as exited+failed on the next poll.
	if _, err := r.dispatchOrPoll(ctx, cfg, prov, target); err != nil {
		t.Fatalf("reconcile 1 (poll): %v", err)
	}
	if cfg.Status.Phase != vrouterv1.PhaseFailed {
		t.Fatalf("after reconcile 1 poll: phase = %q, want %q", cfg.Status.Phase, vrouterv1.PhaseFailed)
	}
	if cfg.Status.LastRebootHandledTime == nil || !cfg.Status.LastRebootHandledTime.Time.Equal(rebootTime.Time) {
		t.Fatalf("after reconcile 1: lastRebootHandledTime = %v, want %v", cfg.Status.LastRebootHandledTime, rebootTime)
	}

	// --- Reconcile 2: same reboot time again — must NOT re-dispatch.
	if _, err := r.dispatchOrPoll(ctx, cfg, prov, target); err != nil {
		t.Fatalf("reconcile 2: %v", err)
	}
	if prov.execCount != 1 {
		t.Fatalf("after reconcile 2: execCount = %d, want still 1 (no re-dispatch for the same reboot)", prov.execCount)
	}
	if cfg.Status.Phase != vrouterv1.PhaseFailed {
		t.Fatalf("after reconcile 2: phase = %q, want unchanged %q (Failed has no auto-retry)", cfg.Status.Phase, vrouterv1.PhaseFailed)
	}

	// --- Reconcile 3: a genuinely newer reboot must force one fresh re-apply.
	newerReboot := metav1.NewTime(rebootTime.Add(time.Hour))
	target.Status.LastRebootTime = &newerReboot

	if _, err := r.dispatchOrPoll(ctx, cfg, prov, target); err != nil {
		t.Fatalf("reconcile 3 (dispatch): %v", err)
	}
	if prov.execCount != 2 {
		t.Fatalf("after reconcile 3 dispatch: execCount = %d, want 2 (newer reboot forces exactly one fresh re-apply)", prov.execCount)
	}
	if _, err := r.dispatchOrPoll(ctx, cfg, prov, target); err != nil {
		t.Fatalf("reconcile 3 (poll): %v", err)
	}
	if cfg.Status.Phase != vrouterv1.PhaseFailed {
		t.Fatalf("after reconcile 3 poll: phase = %q, want %q", cfg.Status.Phase, vrouterv1.PhaseFailed)
	}
	if cfg.Status.LastRebootHandledTime == nil || !cfg.Status.LastRebootHandledTime.Time.Equal(newerReboot.Time) {
		t.Fatalf("after reconcile 3: lastRebootHandledTime = %v, want %v", cfg.Status.LastRebootHandledTime, newerReboot)
	}

	// --- Reconcile 4: same newer reboot time again — still must not re-dispatch.
	if _, err := r.dispatchOrPoll(ctx, cfg, prov, target); err != nil {
		t.Fatalf("reconcile 4: %v", err)
	}
	if prov.execCount != 2 {
		t.Fatalf("after reconcile 4: execCount = %d, want still 2 (no re-dispatch for the same reboot)", prov.execCount)
	}

	// Sanity: the persisted object in the fake client reflects the final state.
	var got vrouterv1.VRouterConfig
	if err := cl.Get(ctx, k8stypes.NamespacedName{Name: "c1", Namespace: "default"}, &got); err != nil {
		t.Fatalf("get persisted config: %v", err)
	}
	if got.Status.Phase != vrouterv1.PhaseFailed {
		t.Errorf("persisted status.phase = %q, want %q", got.Status.Phase, vrouterv1.PhaseFailed)
	}
}
