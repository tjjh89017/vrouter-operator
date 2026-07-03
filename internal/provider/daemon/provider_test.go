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

package daemon

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"

	controlpb "github.com/tjjh89017/vrouter-daemon/gen/go/controlpb"
	"github.com/tjjh89017/vrouter-operator/internal/provider/types"
)

// fakeControlServiceClient is a minimal in-process stand-in for
// controlpb.ControlServiceClient. Only ApplyConfig is exercised by the
// tests in this file; IsConnected/GetStatus panic if called since ExecScript
// and GetExecStatus never invoke them.
type fakeControlServiceClient struct {
	applyConfigFn func(ctx context.Context, in *controlpb.ApplyConfigRequest) (*controlpb.ApplyConfigResponse, error)
}

func (f *fakeControlServiceClient) IsConnected(context.Context, *controlpb.IsConnectedRequest, ...grpc.CallOption) (*controlpb.IsConnectedResponse, error) {
	panic("not used by these tests")
}

func (f *fakeControlServiceClient) GetStatus(context.Context, *controlpb.GetStatusRequest, ...grpc.CallOption) (*controlpb.GetStatusResponse, error) {
	panic("not used by these tests")
}

func (f *fakeControlServiceClient) ApplyConfig(ctx context.Context, in *controlpb.ApplyConfigRequest, _ ...grpc.CallOption) (*controlpb.ApplyConfigResponse, error) {
	return f.applyConfigFn(ctx, in)
}

// waitForExecDone polls GetExecStatus until the background ExecScript call
// completes or the deadline elapses. This is the Eventually-style
// equivalent for this package's plain `testing` API (no Gomega import
// here): ExecScript intentionally hands the result back asynchronously via
// a background goroutine, so a fixed sleep would either be flaky (too
// short) or needlessly slow the suite down (long enough to never flake).
func waitForExecDone(t *testing.T, p *Provider, pid int64, timeout time.Duration) (*types.ExecStatus, error) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		status, err := p.GetExecStatus(context.Background(), pid)
		if err != nil {
			return nil, err
		}
		if status.Exited {
			return status, nil
		}
		if time.Now().After(deadline) {
			t.Fatalf("exec pid %d did not complete within %s", pid, timeout)
		}
		time.Sleep(time.Millisecond)
	}
}

// TestGetExecStatus_UnknownPID_ReturnsErrExecResultLost simulates an operator
// restart: the package-level execMap has no entry for the requested pid
// (either because the process just started, or a previous entry already got
// garbage collected). GetExecStatus must signal this as a lost result via
// types.ErrExecResultLost so the controller can recover instead of retrying
// the same handle forever.
func TestGetExecStatus_UnknownPID_ReturnsErrExecResultLost(t *testing.T) {
	p := &Provider{}

	// Use a pid that was never stored via ExecScript in this process, which is
	// exactly what happens after an operator restart: the in-memory execMap is
	// empty but Status.ExecPID on the CR still references an old handle.
	_, err := p.GetExecStatus(context.Background(), 999999)

	if err == nil {
		t.Fatalf("expected an error for an unknown pid, got nil")
	}
	if !errors.Is(err, types.ErrExecResultLost) {
		t.Fatalf("error = %v, want errors.Is(err, types.ErrExecResultLost) to be true", err)
	}
}

// TestExecScript_StderrConcatenation is a table test over ExecScript's
// three-way stderr/ErrorMessage combination logic: ErrorMessage-only,
// ErrorMessage+Stderr combined (newline-joined), and Stderr-only. This
// branch silently drops output if refactored incorrectly (e.g. always
// preferring one field over the other), and had no direct test before.
func TestExecScript_StderrConcatenation(t *testing.T) {
	tests := []struct {
		name         string
		errorMessage string
		stderr       string
		wantStderr   string
	}{
		{name: "ErrorMessage only", errorMessage: "dispatch failed", stderr: "", wantStderr: "dispatch failed"},
		{name: "ErrorMessage and Stderr combined", errorMessage: "commit failed", stderr: "syntax error", wantStderr: "commit failed\nsyntax error"},
		{name: "Stderr only", errorMessage: "", stderr: "syntax error", wantStderr: "syntax error"},
		{name: "neither set", errorMessage: "", stderr: "", wantStderr: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := &fakeControlServiceClient{
				applyConfigFn: func(_ context.Context, _ *controlpb.ApplyConfigRequest) (*controlpb.ApplyConfigResponse, error) {
					return &controlpb.ApplyConfigResponse{
						ExitCode:     1,
						Stdout:       "some stdout",
						Stderr:       tt.stderr,
						ErrorMessage: tt.errorMessage,
					}, nil
				},
			}
			p := &Provider{client: client, agentID: "agent-1", timeoutSeconds: 60}

			pid, err := p.ExecScript(context.Background(), "set system host-name test", "", true)
			if err != nil {
				t.Fatalf("ExecScript() returned error: %v", err)
			}

			status, err := waitForExecDone(t, p, pid, 2*time.Second)
			if err != nil {
				t.Fatalf("GetExecStatus() returned error: %v", err)
			}
			if status.Stderr != tt.wantStderr {
				t.Errorf("Stderr = %q, want %q", status.Stderr, tt.wantStderr)
			}
			if status.Stdout != "some stdout" {
				t.Errorf("Stdout = %q, want unchanged %q", status.Stdout, "some stdout")
			}
			if status.ExitCode != 1 {
				t.Errorf("ExitCode = %d, want 1", status.ExitCode)
			}
		})
	}
}

// TestGetExecStatus_PendingThenDone verifies the state transition GetExecStatus
// exposes to callers: {Exited:false} while the background ApplyConfig call
// has not returned yet, then the final result once it has -- as opposed to
// only ever testing the two endpoints (unknown-pid, and a call that happens
// to already be done).
func TestGetExecStatus_PendingThenDone(t *testing.T) {
	release := make(chan struct{})
	client := &fakeControlServiceClient{
		applyConfigFn: func(_ context.Context, _ *controlpb.ApplyConfigRequest) (*controlpb.ApplyConfigResponse, error) {
			<-release // held open until the test observes the pending state
			return &controlpb.ApplyConfigResponse{ExitCode: 0, Stdout: "ok"}, nil
		},
	}
	p := &Provider{client: client, agentID: "agent-1", timeoutSeconds: 60}

	pid, err := p.ExecScript(context.Background(), "set system host-name test", "", true)
	if err != nil {
		t.Fatalf("ExecScript() returned error: %v", err)
	}

	status, err := p.GetExecStatus(context.Background(), pid)
	if err != nil {
		t.Fatalf("GetExecStatus() (pending) returned error: %v", err)
	}
	if status.Exited {
		t.Fatalf("GetExecStatus() (pending) = %+v, want Exited=false while ApplyConfig is still blocked", status)
	}

	close(release)

	final, err := waitForExecDone(t, p, pid, 2*time.Second)
	if err != nil {
		t.Fatalf("GetExecStatus() (final) returned error: %v", err)
	}
	if final.ExitCode != 0 || final.Stdout != "ok" {
		t.Errorf("final status = %+v, want ExitCode=0 Stdout=%q", final, "ok")
	}
}

// TestExecScript_ApplyConfigError verifies that a transport/RPC-level error
// from ApplyConfig itself (as opposed to a non-zero exit code from the
// remote script) is surfaced through GetExecStatus as an error, not folded
// into a successful ExecStatus.
func TestExecScript_ApplyConfigError(t *testing.T) {
	client := &fakeControlServiceClient{
		applyConfigFn: func(_ context.Context, _ *controlpb.ApplyConfigRequest) (*controlpb.ApplyConfigResponse, error) {
			return nil, errors.New("connection reset by peer")
		},
	}
	p := &Provider{client: client, agentID: "agent-1", timeoutSeconds: 60}

	pid, err := p.ExecScript(context.Background(), "set system host-name test", "", true)
	if err != nil {
		t.Fatalf("ExecScript() returned error: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		status, err := p.GetExecStatus(context.Background(), pid)
		if err != nil {
			if !strings.Contains(err.Error(), "connection reset by peer") {
				t.Fatalf("error = %q, want it to wrap the ApplyConfig transport error", err.Error())
			}
			return
		}
		if status.Exited {
			t.Fatalf("GetExecStatus() = %+v, Exited=true, want the ApplyConfig error to be surfaced instead", status)
		}
		if time.Now().After(deadline) {
			t.Fatalf("exec pid %d never surfaced the ApplyConfig error", pid)
		}
		time.Sleep(time.Millisecond)
	}
}
