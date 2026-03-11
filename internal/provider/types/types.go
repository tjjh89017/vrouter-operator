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

// Package types defines the core Provider interface and shared types used by
// all provider implementations.
package types

import "context"

// ExecStatus represents the result of a previously started async script execution.
type ExecStatus struct {
	Exited   bool
	ExitCode int
	Stdout   string
	Stderr   string
}

// Provider abstracts virtualization backend operations for router management.
// Each instance is bound to a specific target router at construction time.
type Provider interface {
	// IsVMRunning checks whether the underlying VM is in a running (non-stopped) state.
	IsVMRunning(ctx context.Context) (bool, error)
	// CheckReady verifies the router is reachable and ready to accept config
	// (QGA ping + vyos-router.service is-active).
	CheckReady(ctx context.Context) error
	// WriteFile writes the apply script content to the router via guest agent.
	WriteFile(ctx context.Context, content []byte) error
	// ExecScript executes the apply script asynchronously, returns PID for tracking.
	ExecScript(ctx context.Context) (pid int64, err error)
	// GetExecStatus polls the result of a previously started script.
	GetExecStatus(ctx context.Context, pid int64) (*ExecStatus, error)
}
