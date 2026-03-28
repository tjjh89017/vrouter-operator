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

// Package daemon implements the Provider interface via the vrouter-daemon
// ControlService gRPC API.
package daemon

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	controlpb "github.com/tjjh89017/vrouter-daemon/gen/go/controlpb"
	vrouterv1 "github.com/tjjh89017/vrouter-operator/api/v1"
	"github.com/tjjh89017/vrouter-operator/internal/provider/types"
)

// Package-level connection pool — keyed by server address.
var (
	connMu  sync.Mutex
	connMap = make(map[string]*grpc.ClientConn)
)

// Package-level async exec registry — keyed by fake PID.
var (
	execMap sync.Map // map[int64]*execEntry
	pidSeq  int64    // monotonically increasing fake PID counter
)

type execEntry struct {
	result *types.ExecStatus
	err    error
	done   chan struct{}
}

// Provider implements types.Provider via the vrouter-daemon ControlService.
type Provider struct {
	client         controlpb.ControlServiceClient
	agentID        string
	timeoutSeconds int32
	scriptBuf      []byte // cached by WriteFile, consumed by ExecScript
}

// New creates a daemon Provider that connects to the ControlService at cfg.Address.
func New(cfg *vrouterv1.DaemonConfig) (*Provider, error) {
	conn, err := getConn(cfg.Address)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", cfg.Address, err)
	}
	timeout := cfg.TimeoutSeconds
	if timeout == 0 {
		timeout = 60
	}
	return &Provider{
		client:         controlpb.NewControlServiceClient(conn),
		agentID:        cfg.AgentID,
		timeoutSeconds: timeout,
	}, nil
}

// getConn returns a cached gRPC connection for the given address, creating one
// if it doesn't exist yet.
func getConn(addr string) (*grpc.ClientConn, error) {
	connMu.Lock()
	defer connMu.Unlock()
	if conn, ok := connMap[addr]; ok {
		return conn, nil
	}
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, err
	}
	connMap[addr] = conn
	return conn, nil
}

// IsVMRunning returns true if the agent is currently connected to the daemon.
// A disconnected agent means the router is unreachable (VM stopped or agent not started).
func (p *Provider) IsVMRunning(ctx context.Context) (bool, error) {
	resp, err := p.client.IsConnected(ctx, &controlpb.IsConnectedRequest{AgentId: p.agentID})
	if err != nil {
		return false, fmt.Errorf("IsConnected: %w", err)
	}
	return resp.Connected, nil
}

// CheckReady is a no-op for the daemon provider: if the agent passed IsVMRunning,
// it is already connected and ready to receive config.
func (p *Provider) CheckReady(_ context.Context) error {
	return nil
}

// WriteFile caches the script content for the subsequent ExecScript call.
// The actual content is sent to the agent as part of ApplyConfig.
func (p *Provider) WriteFile(_ context.Context, content []byte) error {
	p.scriptBuf = make([]byte, len(content))
	copy(p.scriptBuf, content)
	return nil
}

// ExecScript submits the cached script to the daemon via ApplyConfig in a
// background goroutine and returns a fake PID for polling via GetExecStatus.
func (p *Provider) ExecScript(_ context.Context) (int64, error) {
	if len(p.scriptBuf) == 0 {
		return 0, fmt.Errorf("no script content: WriteFile must be called before ExecScript")
	}

	pid := atomic.AddInt64(&pidSeq, 1)
	entry := &execEntry{done: make(chan struct{})}
	execMap.Store(pid, entry)

	payload := p.scriptBuf
	client := p.client
	agentID := p.agentID
	timeoutSec := p.timeoutSeconds

	go func() {
		defer close(entry.done)

		// Use a fresh context with a generous deadline so the background call
		// outlives the reconcile loop that started it.
		ctx, cancel := context.WithTimeout(
			context.Background(),
			time.Duration(timeoutSec+10)*time.Second,
		)
		defer cancel()

		resp, err := client.ApplyConfig(ctx, &controlpb.ApplyConfigRequest{
			AgentId:        agentID,
			ConfigPayload:  payload,
			TimeoutSeconds: timeoutSec,
		})
		if err != nil {
			entry.err = fmt.Errorf("ApplyConfig: %w", err)
			return
		}

		exitCode := int(resp.ExitCode)
		stderr := resp.Stderr
		if resp.ErrorMessage != "" {
			stderr = resp.ErrorMessage
			if resp.Stderr != "" {
				stderr += "\n" + resp.Stderr
			}
		}
		entry.result = &types.ExecStatus{
			Exited:   true,
			ExitCode: exitCode,
			Stdout:   resp.Stdout,
			Stderr:   stderr,
		}
	}()

	return pid, nil
}

// GetExecStatus checks whether the background ApplyConfig call has completed.
// Returns {Exited: false} while still running, or the final result once done.
func (p *Provider) GetExecStatus(_ context.Context, pid int64) (*types.ExecStatus, error) {
	v, ok := execMap.Load(pid)
	if !ok {
		return nil, fmt.Errorf("no pending exec for pid %d (operator may have restarted)", pid)
	}
	entry := v.(*execEntry)

	select {
	case <-entry.done:
		execMap.Delete(pid)
		if entry.err != nil {
			return nil, entry.err
		}
		return entry.result, nil
	default:
		return &types.ExecStatus{Exited: false}, nil
	}
}
