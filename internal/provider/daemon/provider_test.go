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
	"testing"

	"github.com/tjjh89017/vrouter-operator/internal/provider/types"
)

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
