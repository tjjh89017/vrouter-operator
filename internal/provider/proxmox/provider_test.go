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

package proxmox

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// should report a destroyed VM (VMID absent from a live /cluster/resources
// lookup) as "not running" rather than surfacing an error, so callers take
// the clean stopped path instead of looping on a reconcile error. This is
// the counterpart to ProxmoxClusterController clearing status.proxmoxNode
// when the VMID disappears (SPEC.md §7.3 step 4a): once the cached node is
// cleared, IsVMRunning falls back to this same live lookup.
func TestIsVMRunning_VMIDAbsentFromClusterResourcesIsNotAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api2/json/cluster/resources" {
			t.Fatalf("unexpected request path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"vmid":200,"node":"pve2","uptime":100}]}`))
	}))
	defer srv.Close()

	p := &Provider{
		httpClient:  &http.Client{Timeout: 5 * time.Second},
		endpoints:   []string{srv.URL},
		tokenID:     "id",
		tokenSecret: "secret",
		vmid:        100, // not present in the cluster resources response above
		cachedNode:  "",  // force a live lookup
	}

	running, err := p.IsVMRunning(context.Background())
	if err != nil {
		t.Fatalf("IsVMRunning() returned error %v, want nil (VM-not-found should report false,nil)", err)
	}
	if running {
		t.Fatalf("IsVMRunning() = true, want false for a VMID absent from cluster resources")
	}
}

// should still surface a real transport/API error (as opposed to a clean
// "VMID not found") so callers correctly retry rather than treating a
// temporarily unreachable Proxmox API as a stopped VM.
func TestIsVMRunning_TransportErrorIsNotSwallowed(t *testing.T) {
	p := &Provider{
		httpClient:  &http.Client{Timeout: 1 * time.Second},
		endpoints:   []string{"http://127.0.0.1:0"}, // unroutable, guaranteed connection failure
		tokenID:     "id",
		tokenSecret: "secret",
		vmid:        100,
		cachedNode:  "",
	}

	_, err := p.IsVMRunning(context.Background())
	if err == nil {
		t.Fatalf("IsVMRunning() returned nil error, want a transport error to be surfaced")
	}
}
