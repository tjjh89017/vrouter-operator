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
	"fmt"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	vrouterv1 "github.com/tjjh89017/vrouter-operator/api/v1"
	providertypes "github.com/tjjh89017/vrouter-operator/internal/provider/types"
)

// fakeLostProvider is a minimal types.Provider stub whose GetExecStatus
// always reports the exec result as lost, simulating an operator restart
// (daemon provider) or a guest agent restart after a VM reboot (KubeVirt /
// Proxmox providers).
type fakeLostProvider struct{}

func (fakeLostProvider) IsVMRunning(_ context.Context) (bool, error) { return true, nil }
func (fakeLostProvider) CheckReady(_ context.Context) error          { return nil }
func (fakeLostProvider) ExecScript(_ context.Context, _, _ string, _ bool) (int64, error) {
	return 0, nil
}
func (fakeLostProvider) GetExecStatus(_ context.Context, pid int64) (*providertypes.ExecStatus, error) {
	return nil, fmt.Errorf("no pending exec for pid %d (operator may have restarted): %w", pid, providertypes.ErrExecResultLost)
}

// fakeTransientErrProvider reports a plain, non-sentinel error from
// GetExecStatus, simulating a genuinely transient failure (e.g. a network
// blip or a 503) that must still be retried as an error rather than treated
// as a lost result.
type fakeTransientErrProvider struct{}

func (fakeTransientErrProvider) IsVMRunning(_ context.Context) (bool, error) { return true, nil }
func (fakeTransientErrProvider) CheckReady(_ context.Context) error          { return nil }
func (fakeTransientErrProvider) ExecScript(_ context.Context, _, _ string, _ bool) (int64, error) {
	return 0, nil
}
func (fakeTransientErrProvider) GetExecStatus(_ context.Context, _ int64) (*providertypes.ExecStatus, error) {
	return nil, errors.New("connection reset by peer")
}

func newFakeReconciler(t *testing.T, objs ...client.Object) (*VRouterConfigReconciler, client.Client) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := vrouterv1.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to register scheme: %v", err)
	}
	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objs...).
		WithStatusSubresource(&vrouterv1.VRouterConfig{}).
		Build()
	return &VRouterConfigReconciler{Client: cl, Scheme: scheme}, cl
}

// TestPollExecStatus_LostResult_ResetsToRedispatch verifies that when the
// provider reports the exec result as lost (types.ErrExecResultLost), the
// controller clears status.execPID and resets phase back to Pending instead
// of returning an error and looping forever on the stale PID. This is the
// A6 fix: without it, a VM reboot or operator restart could deadlock the
// VRouterConfig permanently.
func TestPollExecStatus_LostResult_ResetsToRedispatch(t *testing.T) {
	cfg := &vrouterv1.VRouterConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "c1", Namespace: "default", Generation: 2},
		Spec: vrouterv1.VRouterConfigSpec{
			TargetRef: vrouterv1.NameRef{Name: "r1"},
			Config:    "set system host-name test",
		},
		Status: vrouterv1.VRouterConfigStatus{
			Phase:              vrouterv1.PhaseApplying,
			ExecPID:            42,
			ObservedGeneration: 2,
		},
	}

	r, cl := newFakeReconciler(t, cfg)

	result, err := r.pollExecStatus(context.Background(), cfg, fakeLostProvider{})
	if err != nil {
		t.Fatalf("pollExecStatus returned error, want nil (lost result must not be treated as a hard error): %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Fatalf("expected RequeueAfter > 0 after a lost result, got %+v", result)
	}

	var got vrouterv1.VRouterConfig
	if err := cl.Get(context.Background(), k8stypes.NamespacedName{Name: "c1", Namespace: "default"}, &got); err != nil {
		t.Fatalf("get updated config: %v", err)
	}
	if got.Status.ExecPID != 0 {
		t.Errorf("status.execPID = %d, want 0 (cleared)", got.Status.ExecPID)
	}
	if got.Status.Phase != vrouterv1.PhasePending {
		t.Errorf("status.phase = %q, want %q (reset so the generation-based apply re-dispatches)", got.Status.Phase, vrouterv1.PhasePending)
	}
}

// TestPollExecStatus_TransientError_StillRetriedAsError verifies that a
// genuinely transient GetExecStatus error (not types.ErrExecResultLost) is
// still returned as an error so controller-runtime retries with backoff,
// and does NOT get treated as a lost result (which would wrongly clear
// execPID and re-run the apply script while the original one may still be
// running).
func TestPollExecStatus_TransientError_StillRetriedAsError(t *testing.T) {
	cfg := &vrouterv1.VRouterConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "c2", Namespace: "default", Generation: 1},
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

	r, cl := newFakeReconciler(t, cfg)

	_, err := r.pollExecStatus(context.Background(), cfg, fakeTransientErrProvider{})
	if err == nil {
		t.Fatalf("expected a transient GetExecStatus error to be returned, got nil")
	}

	var got vrouterv1.VRouterConfig
	if getErr := cl.Get(context.Background(), k8stypes.NamespacedName{Name: "c2", Namespace: "default"}, &got); getErr != nil {
		t.Fatalf("get config: %v", getErr)
	}
	if got.Status.ExecPID != 42 {
		t.Errorf("status.execPID = %d, want unchanged 42 (transient errors must not clear the pending exec handle)", got.Status.ExecPID)
	}
	if got.Status.Phase != vrouterv1.PhaseApplying {
		t.Errorf("status.phase = %q, want unchanged %q", got.Status.Phase, vrouterv1.PhaseApplying)
	}
}
