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
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
