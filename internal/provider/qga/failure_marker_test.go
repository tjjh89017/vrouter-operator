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

package qga

import "testing"

// invalidAddressOutput is the exact combined output observed on a real guest
// when a set-time validation failure occurs: `commit` still exits 0 (the
// preceding `load` left a committable diff), so only the "Set failed" text in
// the output reveals the failure.
const invalidAddressOutput = `Load complete. Use 'commit' to make changes effective.
  Error: not-an-ip is not a valid network interface address
  Invalid value
  Value validation failed
  Set failed`

func TestApplyOutputIndicatesFailure(t *testing.T) {
	tests := []struct {
		name       string
		output     string
		wantFailed bool
		wantMarker string
	}{
		{
			name:       "set failure from real invalid-address output",
			output:     invalidAddressOutput,
			wantFailed: true,
			wantMarker: "Set failed",
		},
		{
			name:       "set failed marker",
			output:     "Set failed",
			wantFailed: true,
			wantMarker: "Set failed",
		},
		{
			name:       "commit failed marker",
			output:     "some noise\nCommit failed\nmore noise",
			wantFailed: true,
			wantMarker: "Commit failed",
		},
		{
			name:       "delete failed marker",
			output:     "Delete failed",
			wantFailed: true,
			wantMarker: "Delete failed",
		},
		{
			name:       "case-insensitive match",
			output:     "  set FAILED",
			wantFailed: true,
			wantMarker: "Set failed",
		},
		{
			name:       "clean success output does not match",
			output:     "Load complete. Use 'commit' to make changes effective.",
			wantFailed: false,
			wantMarker: "",
		},
		{
			name:       "empty output does not match",
			output:     "",
			wantFailed: false,
			wantMarker: "",
		},
		{
			name:       "unrelated failure word does not match",
			output:     "failed to reach neighbor",
			wantFailed: false,
			wantMarker: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			marker, failed := ApplyOutputIndicatesFailure(tt.output)
			if failed != tt.wantFailed {
				t.Errorf("failed = %v, want %v", failed, tt.wantFailed)
			}
			if marker != tt.wantMarker {
				t.Errorf("marker = %q, want %q", marker, tt.wantMarker)
			}
		})
	}
}
