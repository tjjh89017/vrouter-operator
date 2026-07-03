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
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	vrouterv1 "github.com/tjjh89017/vrouter-operator/api/v1"
)

// TestNextRebootTime_NoStoredValue verifies that when a target has never had
// a reboot recorded, a low uptime observation always sets LastRebootTime to
// the derived boot time (now - uptime), not to `now` itself.
func TestNextRebootTime_NoStoredValue(t *testing.T) {
	now := time.Date(2026, 7, 3, 0, 0, 30, 0, time.UTC) // t=30
	uptime := 30 * time.Second                          // rebooted at t=0

	got := nextRebootTime(now, uptime, nil)

	if got == nil {
		t.Fatalf("nextRebootTime() = nil, want non-nil boot time")
	}
	wantBootTime := now.Add(-uptime)
	if !got.Time.Equal(wantBootTime) {
		t.Fatalf("nextRebootTime() = %v, want %v", got.Time, wantBootTime)
	}
}

// TestNextRebootTime_SameRebootWindow verifies the C17 scenario: a later
// sync within the SAME reboot's low-uptime window derives (approximately)
// the same boot time as an earlier sync and must NOT advance
// LastRebootTime, even though `now` has moved forward.
func TestNextRebootTime_SameRebootWindow(t *testing.T) {
	rebootedAt := time.Date(2026, 7, 3, 0, 0, 0, 0, time.UTC) // t=0

	// First sync at t=30: uptime=30s -> derived bootTime = t0.
	firstSyncNow := rebootedAt.Add(30 * time.Second)
	stored := nextRebootTime(firstSyncNow, 30*time.Second, nil)
	if stored == nil {
		t.Fatalf("first sync: nextRebootTime() = nil, want non-nil")
	}

	// Second sync at t=80 (still within the 90s threshold window):
	// uptime=80s -> derived bootTime = t0 again (same physical reboot).
	secondSyncNow := rebootedAt.Add(80 * time.Second)
	got := nextRebootTime(secondSyncNow, 80*time.Second, stored)

	if got != nil {
		t.Fatalf("second sync: nextRebootTime() = %v, want nil (unchanged, same reboot window)", got.Time)
	}
}

// TestNextRebootTime_NewerReboot verifies that a genuinely new reboot -
// derived boot time more than the tolerance newer than the stored value -
// still advances LastRebootTime.
func TestNextRebootTime_NewerReboot(t *testing.T) {
	firstReboot := time.Date(2026, 7, 3, 0, 0, 0, 0, time.UTC) // t=0
	stored := metav1.NewTime(firstReboot)

	// A second, later reboot: VM rebooted again at t=500, observed at t=520
	// with uptime=20s -> derived bootTime = t=500, well beyond tolerance.
	secondRebootObservedAt := firstReboot.Add(520 * time.Second)
	uptime := 20 * time.Second

	got := nextRebootTime(secondRebootObservedAt, uptime, &stored)

	if got == nil {
		t.Fatalf("nextRebootTime() = nil, want non-nil for a genuinely new reboot")
	}
	wantBootTime := secondRebootObservedAt.Add(-uptime)
	if !got.Time.Equal(wantBootTime) {
		t.Fatalf("nextRebootTime() = %v, want %v", got.Time, wantBootTime)
	}
}

// TestNextRebootTime_WithinToleranceJitter verifies that a derived boot time
// that is only slightly newer than the stored value (within tolerance, e.g.
// from second-granularity uptime rounding) is treated as the same reboot.
func TestNextRebootTime_WithinToleranceJitter(t *testing.T) {
	rebootedAt := time.Date(2026, 7, 3, 0, 0, 0, 0, time.UTC)
	stored := metav1.NewTime(rebootedAt)

	// Derived boot time is 2s newer than stored, well within the 5s
	// tolerance - should be treated as jitter from the same reboot.
	now := rebootedAt.Add(2*time.Second + 10*time.Second)
	uptime := 10 * time.Second

	got := nextRebootTime(now, uptime, &stored)

	if got != nil {
		t.Fatalf("nextRebootTime() = %v, want nil (within tolerance jitter)", got.Time)
	}
}

// TestNextRebootTime_DerivedBootTimeOlderThanStored verifies that clock
// skew or measurement noise producing a derived boot time OLDER than the
// stored value does not regress LastRebootTime backwards.
func TestNextRebootTime_DerivedBootTimeOlderThanStored(t *testing.T) {
	rebootedAt := time.Date(2026, 7, 3, 0, 0, 30, 0, time.UTC)
	stored := metav1.NewTime(rebootedAt)

	// Derived boot time is 1s OLDER than stored (negative diff).
	now := rebootedAt.Add(-1*time.Second + 10*time.Second)
	uptime := 10 * time.Second

	got := nextRebootTime(now, uptime, &stored)

	if got != nil {
		t.Fatalf("nextRebootTime() = %v, want nil (derived boot time not newer than stored)", got.Time)
	}
}

// TestOnChange_SlowGuestUptimePoll_DoesNotRestampSameReboot is an end-to-end
// regression test (real onChange, mocked Proxmox API) for the interaction
// between nextRebootTime's tolerance window and fetchGuestUptime's slow QGA
// poll (which SPEC §7.3 documents as taking up to 10s). If the timestamp
// paired with a guestUptime reading is captured before that slow poll
// (rather than right after it returns), a poll that happens to take a few
// seconds on one sync but not the next derives a bootTime that drifts by
// however long the poll took — easily exceeding rebootTimeTolerance and
// re-stamping LastRebootTime for what is still the same physical reboot,
// which is exactly the loop e1bd9b5 was written to close.
//
// The first sync's mocked guest-agent exec-status response is delayed well
// past rebootTimeTolerance; the second sync's is answered immediately. Both
// report uptime measured relative to the same fixed bootInstant. A correct
// implementation derives (approximately) the same bootTime both times and
// must not advance LastRebootTime on the second sync.
func TestOnChange_SlowGuestUptimePoll_DoesNotRestampSameReboot(t *testing.T) {
	const (
		vmid          = 100
		node          = "nodeA"
		slowPollDelay = 7 * time.Second
		syncInterval  = 6 * time.Second
	)
	bootInstant := time.Now()
	var syncCount atomic.Int32

	mux := http.NewServeMux()
	mux.HandleFunc("/api2/json/cluster/resources", func(w http.ResponseWriter, r *http.Request) {
		syncCount.Add(1)
		// Host-level (Proxmox) uptime is reported far above the threshold so
		// only the guest-uptime (QGA) path under test can fire.
		fmt.Fprintf(w, `{"data":[{"vmid":%d,"node":%q,"uptime":999999}]}`, vmid, node)
	})
	mux.HandleFunc(fmt.Sprintf("/api2/json/nodes/%s/qemu/%d/agent/exec", node, vmid), func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":{"pid":123}}`))
	})
	mux.HandleFunc(fmt.Sprintf("/api2/json/nodes/%s/qemu/%d/agent/exec-status", node, vmid), func(w http.ResponseWriter, r *http.Request) {
		if syncCount.Load() == 1 {
			// Simulate a guest agent slow to respond (e.g. still finishing
			// boot) on the sync that first observes the reboot.
			time.Sleep(slowPollDelay)
		}
		uptime := time.Since(bootInstant).Seconds()
		out := struct {
			Data struct {
				Exited  int    `json:"exited"`
				OutData string `json:"out-data"`
			} `json:"data"`
		}{}
		out.Data.Exited = 1
		out.Data.OutData = fmt.Sprintf("%f 0", uptime)
		_ = json.NewEncoder(w).Encode(out)
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	scheme := runtime.NewScheme()
	if err := vrouterv1.AddToScheme(scheme); err != nil {
		t.Fatalf("register vrouterv1 scheme: %v", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("register corev1 scheme: %v", err)
	}

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "pve-creds", Namespace: "default"},
		Data: map[string][]byte{
			"api-token-id":     []byte("user@pve!token"),
			"api-token-secret": []byte("secret"),
		},
	}
	cluster := &vrouterv1.ProxmoxCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster1", Namespace: "default"},
		Spec: vrouterv1.ProxmoxClusterSpec{
			Endpoints:      []string{server.URL},
			CredentialsRef: vrouterv1.SecretReference{Name: "pve-creds"},
			SyncInterval:   metav1.Duration{Duration: syncInterval},
		},
	}
	target := &vrouterv1.VRouterTarget{
		ObjectMeta: metav1.ObjectMeta{Name: "target1", Namespace: "default"},
		Spec: vrouterv1.VRouterTargetSpec{
			Provider: vrouterv1.ProviderConfig{
				Type:    vrouterv1.ProviderProxmox,
				Proxmox: &vrouterv1.ProxmoxConfig{VMID: vmid, ClusterRef: vrouterv1.NameRef{Name: "cluster1"}},
			},
		},
	}

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(secret, cluster, target).
		WithStatusSubresource(&vrouterv1.ProxmoxCluster{}, &vrouterv1.VRouterTarget{}).
		Build()
	r := &ProxmoxClusterReconciler{Client: cl, Scheme: scheme}
	ctx := context.Background()
	req := reconcile.Request{NamespacedName: k8stypes.NamespacedName{Name: "cluster1", Namespace: "default"}}

	// The first Reconcile call only adds the finalizer and requeues (same
	// pattern as VRouterConfigReconciler); it does not touch the Proxmox API.
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatalf("finalizer bootstrap reconcile: %v", err)
	}

	// --- Sync 1: reboot never observed before -> stamps LastRebootTime,
	// derived from a guestUptime reading that arrives slowPollDelay late.
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatalf("sync 1: %v", err)
	}
	var got1 vrouterv1.VRouterTarget
	if err := cl.Get(ctx, k8stypes.NamespacedName{Name: "target1", Namespace: "default"}, &got1); err != nil {
		t.Fatalf("get target after sync 1: %v", err)
	}
	if got1.Status.LastRebootTime == nil {
		t.Fatalf("after sync 1: LastRebootTime is nil, want set")
	}
	firstBoot := got1.Status.LastRebootTime.Time
	if d := firstBoot.Sub(bootInstant).Abs(); d > 3*time.Second {
		t.Fatalf("after sync 1: derived bootTime off by %v from the true boot instant (want close to it)", d)
	}

	// --- Sync 2: same physical reboot, but this time the guest agent
	// answers immediately. Must NOT advance LastRebootTime.
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatalf("sync 2: %v", err)
	}
	var got2 vrouterv1.VRouterTarget
	if err := cl.Get(ctx, k8stypes.NamespacedName{Name: "target1", Namespace: "default"}, &got2); err != nil {
		t.Fatalf("get target after sync 2: %v", err)
	}
	if got2.Status.LastRebootTime == nil {
		t.Fatalf("after sync 2: LastRebootTime is nil, want still set")
	}
	if diff := got2.Status.LastRebootTime.Time.Sub(firstBoot); diff.Abs() > rebootTimeTolerance {
		t.Fatalf("after sync 2: LastRebootTime moved by %v (want within tolerance %v of sync 1's value %v, got %v) -- "+
			"a slow guest-agent poll on one sync must not make the next sync look like a new reboot",
			diff, rebootTimeTolerance, firstBoot, got2.Status.LastRebootTime.Time)
	}
}
