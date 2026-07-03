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
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	vrouterv1 "github.com/tjjh89017/vrouter-operator/api/v1"
	"github.com/tjjh89017/vrouter-operator/internal/provider/qga"
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

const (
	testNode = "nodeA"
	testVMID = 100
)

// execStatusResponse builds the JSON body returned by the Proxmox
// agent/exec-status endpoint.
func execStatusResponse(exited bool, exitCode int, stdout, stderr string) []byte {
	exitedInt := 0
	if exited {
		exitedInt = 1
	}
	body := struct {
		Data struct {
			Exited   int    `json:"exited"`
			ExitCode int    `json:"exitcode"`
			OutData  string `json:"out-data"`
			ErrData  string `json:"err-data"`
		} `json:"data"`
	}{}
	body.Data.Exited = exitedInt
	body.Data.ExitCode = exitCode
	body.Data.OutData = base64.StdEncoding.EncodeToString([]byte(stdout))
	body.Data.ErrData = base64.StdEncoding.EncodeToString([]byte(stderr))
	out, err := json.Marshal(body)
	if err != nil {
		panic(err)
	}
	return out
}

func newCachedNodeProvider(handler http.Handler) (*Provider, *httptest.Server) {
	srv := httptest.NewServer(handler)
	p := &Provider{
		httpClient:  &http.Client{Timeout: 5 * time.Second},
		endpoints:   []string{srv.URL},
		tokenID:     "id",
		tokenSecret: "secret",
		vmid:        testVMID,
		cachedNode:  testNode, // pre-resolved: no /cluster/resources round trip needed
	}
	return p, srv
}

// should succeed when QGA answers the ping and the vyos-router.service
// substate check reports "exited" (systemd's terminal state for a oneshot
// unit) with exit code 0. This is the normal, fully-ready path -- previously
// entirely untested despite gating whether config gets applied at all.
func TestCheckReady_Success(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc(fmt.Sprintf("/api2/json/nodes/%s/qemu/%d/agent/ping", testNode, testVMID), func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":{}}`))
	})
	mux.HandleFunc(fmt.Sprintf("/api2/json/nodes/%s/qemu/%d/agent/exec", testNode, testVMID), func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"pid":123}}`))
	})
	mux.HandleFunc(fmt.Sprintf("/api2/json/nodes/%s/qemu/%d/agent/exec-status", testNode, testVMID), func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(execStatusResponse(true, 0, "exited", ""))
	})
	p, srv := newCachedNodeProvider(mux)
	defer srv.Close()

	if err := p.CheckReady(context.Background()); err != nil {
		t.Fatalf("CheckReady() returned error, want nil: %v", err)
	}
}

// should surface a clear "QGA not responding" error when the agent ping
// itself fails (e.g. the guest agent is not running), without attempting the
// substate check at all.
func TestCheckReady_AgentPingFails(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc(fmt.Sprintf("/api2/json/nodes/%s/qemu/%d/agent/ping", testNode, testVMID), func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"data":{}}`))
	})
	mux.HandleFunc(fmt.Sprintf("/api2/json/nodes/%s/qemu/%d/agent/exec", testNode, testVMID), func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("agent/exec must not be called when the ping already failed")
	})
	p, srv := newCachedNodeProvider(mux)
	defer srv.Close()

	err := p.CheckReady(context.Background())
	if err == nil {
		t.Fatalf("CheckReady() returned nil error, want a QGA-not-responding error")
	}
	if !strings.Contains(err.Error(), "QGA not responding") {
		t.Fatalf("error = %q, want it to mention QGA not responding", err.Error())
	}
}

// should reject readiness when the service substate check itself exits
// non-zero (e.g. systemctl failed to run), distinct from a substate value
// that is merely not "exited".
func TestCheckReady_SubstateCheckNonZeroExit(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc(fmt.Sprintf("/api2/json/nodes/%s/qemu/%d/agent/ping", testNode, testVMID), func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":{}}`))
	})
	mux.HandleFunc(fmt.Sprintf("/api2/json/nodes/%s/qemu/%d/agent/exec", testNode, testVMID), func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"pid":123}}`))
	})
	mux.HandleFunc(fmt.Sprintf("/api2/json/nodes/%s/qemu/%d/agent/exec-status", testNode, testVMID), func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(execStatusResponse(true, 1, "", "systemctl: command not found"))
	})
	p, srv := newCachedNodeProvider(mux)
	defer srv.Close()

	err := p.CheckReady(context.Background())
	if err == nil {
		t.Fatalf("CheckReady() returned nil error, want an error for a non-zero substate-check exit code")
	}
	if !strings.Contains(err.Error(), "systemctl show failed") {
		t.Fatalf("error = %q, want it to mention the failed substate check", err.Error())
	}
}

// should reject readiness when the service substate check succeeds but
// reports a substate other than "exited" (e.g. "running" or "failed"),
// distinguishing this from the non-zero-exit-code case above.
func TestCheckReady_SubstateNotExited_Rejected(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc(fmt.Sprintf("/api2/json/nodes/%s/qemu/%d/agent/ping", testNode, testVMID), func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":{}}`))
	})
	mux.HandleFunc(fmt.Sprintf("/api2/json/nodes/%s/qemu/%d/agent/exec", testNode, testVMID), func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"pid":123}}`))
	})
	mux.HandleFunc(fmt.Sprintf("/api2/json/nodes/%s/qemu/%d/agent/exec-status", testNode, testVMID), func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(execStatusResponse(true, 0, "running", ""))
	})
	p, srv := newCachedNodeProvider(mux)
	defer srv.Close()

	err := p.CheckReady(context.Background())
	if err == nil {
		t.Fatalf("CheckReady() returned nil error, want an error for substate=running")
	}
	if !strings.Contains(err.Error(), "not ready (substate=running)") {
		t.Fatalf("error = %q, want it to mention substate=running", err.Error())
	}
}

// should keep polling agent/exec-status while the substate check has not
// exited yet, and only decide readiness once it has -- the loop at the end
// of CheckReady was entirely untested before this.
func TestCheckReady_PollsUntilSubstateCheckExits(t *testing.T) {
	var pollCount atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc(fmt.Sprintf("/api2/json/nodes/%s/qemu/%d/agent/ping", testNode, testVMID), func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":{}}`))
	})
	mux.HandleFunc(fmt.Sprintf("/api2/json/nodes/%s/qemu/%d/agent/exec", testNode, testVMID), func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"pid":123}}`))
	})
	mux.HandleFunc(fmt.Sprintf("/api2/json/nodes/%s/qemu/%d/agent/exec-status", testNode, testVMID), func(w http.ResponseWriter, r *http.Request) {
		if pollCount.Add(1) == 1 {
			_, _ = w.Write(execStatusResponse(false, 0, "", ""))
			return
		}
		_, _ = w.Write(execStatusResponse(true, 0, "exited", ""))
	})
	p, srv := newCachedNodeProvider(mux)
	defer srv.Close()

	if err := p.CheckReady(context.Background()); err != nil {
		t.Fatalf("CheckReady() returned error, want nil: %v", err)
	}
	if got := pollCount.Load(); got < 2 {
		t.Fatalf("agent/exec-status called %d times, want at least 2 (must poll past the first not-yet-exited response)", got)
	}
}

// should return ctx.Err() rather than looping forever if the caller's
// context is canceled while still polling for the substate check to exit.
func TestCheckReady_ContextCanceledWhilePolling(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc(fmt.Sprintf("/api2/json/nodes/%s/qemu/%d/agent/ping", testNode, testVMID), func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":{}}`))
	})
	mux.HandleFunc(fmt.Sprintf("/api2/json/nodes/%s/qemu/%d/agent/exec", testNode, testVMID), func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"pid":123}}`))
	})
	mux.HandleFunc(fmt.Sprintf("/api2/json/nodes/%s/qemu/%d/agent/exec-status", testNode, testVMID), func(w http.ResponseWriter, r *http.Request) {
		// Always "still running" so CheckReady would poll forever without a
		// working context-cancellation check.
		_, _ = w.Write(execStatusResponse(false, 0, "", ""))
	})
	p, srv := newCachedNodeProvider(mux)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	err := p.CheckReady(ctx)
	if err == nil {
		t.Fatalf("CheckReady() returned nil error, want context.DeadlineExceeded")
	}
}

// should write the rendered vbash apply script to the guest and return the
// PID from the subsequent agent/exec call, exercising ExecScript end to end
// including the file-write payload it sends.
func TestExecScript_WritesRenderedScriptAndReturnsPID(t *testing.T) {
	var capturedContent string
	mux := http.NewServeMux()
	mux.HandleFunc(fmt.Sprintf("/api2/json/nodes/%s/qemu/%d/agent/file-write", testNode, testVMID), func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			File    string `json:"file"`
			Content string `json:"content"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode file-write payload: %v", err)
		}
		if payload.File != qga.ScriptPath {
			t.Errorf("file-write path = %q, want %q", payload.File, qga.ScriptPath)
		}
		capturedContent = payload.Content
		_, _ = w.Write([]byte(`{"data":{}}`))
	})
	mux.HandleFunc(fmt.Sprintf("/api2/json/nodes/%s/qemu/%d/agent/exec", testNode, testVMID), func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"pid":999}}`))
	})
	p, srv := newCachedNodeProvider(mux)
	defer srv.Close()

	pid, err := p.ExecScript(context.Background(), "set system host-name test", "run show version", true)
	if err != nil {
		t.Fatalf("ExecScript() returned error: %v", err)
	}
	if pid != 999 {
		t.Errorf("ExecScript() pid = %d, want 999 (from the agent/exec response)", pid)
	}
	if !strings.Contains(capturedContent, "set system host-name test") {
		t.Errorf("file-write content = %q, want it to contain the rendered config", capturedContent)
	}
	if !strings.Contains(capturedContent, "run show version") {
		t.Errorf("file-write content = %q, want it to contain the rendered commands", capturedContent)
	}
}

// should decode a base64-encoded out-data field and fall back to the raw
// string for an err-data field that is not valid base64, matching
// decodeBase64OrRaw's documented fallback behavior end-to-end through
// GetExecStatus.
func TestGetExecStatus_DecodesBase64OutDataAndFallsBackRawErrData(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc(fmt.Sprintf("/api2/json/nodes/%s/qemu/%d/agent/exec-status", testNode, testVMID), func(w http.ResponseWriter, r *http.Request) {
		body := struct {
			Data struct {
				Exited   int    `json:"exited"`
				ExitCode int    `json:"exitcode"`
				OutData  string `json:"out-data"`
				ErrData  string `json:"err-data"`
			} `json:"data"`
		}{}
		body.Data.Exited = 1
		body.Data.ExitCode = 1
		body.Data.OutData = base64.StdEncoding.EncodeToString([]byte("hello stdout"))
		body.Data.ErrData = "not-valid-base64!!!"
		out, _ := json.Marshal(body)
		_, _ = w.Write(out)
	})
	p, srv := newCachedNodeProvider(mux)
	defer srv.Close()

	status, err := p.GetExecStatus(context.Background(), 123)
	if err != nil {
		t.Fatalf("GetExecStatus() returned error: %v", err)
	}
	if status.Stdout != "hello stdout" {
		t.Errorf("Stdout = %q, want the base64-decoded value %q", status.Stdout, "hello stdout")
	}
	if status.Stderr != "not-valid-base64!!!" {
		t.Errorf("Stderr = %q, want the raw fallback value unchanged", status.Stderr)
	}
	if !status.Exited || status.ExitCode != 1 {
		t.Errorf("ExecStatus = %+v, want Exited=true ExitCode=1", status)
	}
}

// should fall through to a working second endpoint when the first endpoint
// is unreachable at the network level, exercising tryDo's multi-endpoint
// failover (used for Proxmox cluster HA where any node's API may answer).
func TestTryDo_FirstEndpointDown_FallsBackToSecond(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc(fmt.Sprintf("/api2/json/nodes/%s/qemu/%d/agent/ping", testNode, testVMID), func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":{}}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	p := &Provider{
		httpClient:  &http.Client{Timeout: 2 * time.Second},
		endpoints:   []string{"http://127.0.0.1:0", srv.URL}, // first is unroutable
		tokenID:     "id",
		tokenSecret: "secret",
		vmid:        testVMID,
		cachedNode:  testNode,
	}

	if err := p.agentPing(context.Background(), testNode); err != nil {
		t.Fatalf("agentPing() returned error, want the second endpoint to serve the request: %v", err)
	}
}

// should fail construction when the referenced credentials Secret does not
// exist, rather than constructing a Provider that would only fail later on
// its first API call.
func TestNew_MissingCredentialsSecret_Errors(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("register corev1 scheme: %v", err)
	}
	if err := vrouterv1.AddToScheme(scheme); err != nil {
		t.Fatalf("register vrouterv1 scheme: %v", err)
	}
	cl := fake.NewClientBuilder().WithScheme(scheme).Build()

	cluster := &vrouterv1.ProxmoxCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster1", Namespace: "default"},
		Spec: vrouterv1.ProxmoxClusterSpec{
			Endpoints:      []string{"https://pve.example.com:8006"},
			CredentialsRef: vrouterv1.SecretReference{Name: "missing-secret"},
		},
	}
	cfg := &vrouterv1.ProxmoxConfig{VMID: testVMID}

	if _, err := New(context.Background(), cfg, cluster, "", cl); err == nil {
		t.Fatalf("New() returned nil error, want an error for a missing credentials secret")
	}
}

// should reject a credentials Secret that is missing (or has empty) the
// api-token-id / api-token-secret keys, instead of silently constructing a
// Provider that would send unauthenticated requests.
func TestNew_EmptyTokenFields_Errors(t *testing.T) {
	tests := []struct {
		name string
		data map[string][]byte
	}{
		{name: "both empty", data: map[string][]byte{"api-token-id": []byte(""), "api-token-secret": []byte("")}},
		{name: "id missing", data: map[string][]byte{"api-token-secret": []byte("secret")}},
		{name: "secret missing", data: map[string][]byte{"api-token-id": []byte("id")}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			if err := corev1.AddToScheme(scheme); err != nil {
				t.Fatalf("register corev1 scheme: %v", err)
			}
			if err := vrouterv1.AddToScheme(scheme); err != nil {
				t.Fatalf("register vrouterv1 scheme: %v", err)
			}
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: "pve-creds", Namespace: "default"},
				Data:       tt.data,
			}
			cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(secret).Build()

			cluster := &vrouterv1.ProxmoxCluster{
				ObjectMeta: metav1.ObjectMeta{Name: "cluster1", Namespace: "default"},
				Spec: vrouterv1.ProxmoxClusterSpec{
					Endpoints:      []string{"https://pve.example.com:8006"},
					CredentialsRef: vrouterv1.SecretReference{Name: "pve-creds"},
				},
			}
			cfg := &vrouterv1.ProxmoxConfig{VMID: testVMID}

			if _, err := New(context.Background(), cfg, cluster, "", cl); err == nil {
				t.Fatalf("New() returned nil error, want an error for missing token fields")
			}
		})
	}
}

// should accept a well-formed credentials secret and endpoint list, trimming
// a trailing slash from each endpoint so later path concatenation does not
// produce a double slash.
func TestNew_ValidCredentials_TrimsEndpointTrailingSlash(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("register corev1 scheme: %v", err)
	}
	if err := vrouterv1.AddToScheme(scheme); err != nil {
		t.Fatalf("register vrouterv1 scheme: %v", err)
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "pve-creds", Namespace: "default"},
		Data: map[string][]byte{
			"api-token-id":     []byte("user@pve!token"),
			"api-token-secret": []byte("secret"),
		},
	}
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(secret).Build()

	cluster := &vrouterv1.ProxmoxCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster1", Namespace: "default"},
		Spec: vrouterv1.ProxmoxClusterSpec{
			Endpoints:      []string{"https://pve.example.com:8006/"},
			CredentialsRef: vrouterv1.SecretReference{Name: "pve-creds"},
		},
	}
	cfg := &vrouterv1.ProxmoxConfig{VMID: testVMID}

	p, err := New(context.Background(), cfg, cluster, "cachedNode1", cl)
	if err != nil {
		t.Fatalf("New() returned error: %v", err)
	}
	if len(p.endpoints) != 1 || p.endpoints[0] != "https://pve.example.com:8006" {
		t.Errorf("endpoints = %v, want the trailing slash trimmed", p.endpoints)
	}
	if p.cachedNode != "cachedNode1" {
		t.Errorf("cachedNode = %q, want %q", p.cachedNode, "cachedNode1")
	}
}
