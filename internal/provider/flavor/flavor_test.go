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

package flavor

import "testing"

// TestByID_KnownFlavors verifies that each registered /etc/os-release ID
// resolves to a Flavor with the expected router systemd unit name and no error.
func TestByID_KnownFlavors(t *testing.T) {
	tests := []struct {
		name     string
		id       string
		wantUnit string
	}{
		{
			name:     "vyos ID resolves to vyos-router.service",
			id:       "vyos",
			wantUnit: "vyos-router.service",
		},
		{
			name:     "dozenos ID resolves to dozenos-router.service",
			id:       "dozenos",
			wantUnit: "dozenos-router.service",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			fl, err := ByID(tt.id)

			// Assert
			if err != nil {
				t.Fatalf("ByID(%q) returned unexpected error: %v", tt.id, err)
			}
			if fl == nil {
				t.Fatalf("ByID(%q) returned nil flavor with no error", tt.id)
			}
			if got := fl.RouterServiceUnit(); got != tt.wantUnit {
				t.Fatalf("ByID(%q).RouterServiceUnit() = %q, want %q", tt.id, got, tt.wantUnit)
			}
		})
	}
}

// TestByID_UnknownFlavors verifies that an unrecognized /etc/os-release ID
// surfaces an error and a nil Flavor rather than silently defaulting.
func TestByID_UnknownFlavors(t *testing.T) {
	tests := []struct {
		name string
		id   string
	}{
		{name: "unrelated distro ID", id: "ubuntu"},
		{name: "empty ID", id: ""},
		{name: "arbitrary unknown ID", id: "totally-unknown-os"},
		{name: "case mismatch is not matched", id: "VyOS"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			fl, err := ByID(tt.id)

			// Assert
			if err == nil {
				t.Fatalf("ByID(%q) expected an error for unknown ID, got nil", tt.id)
			}
			if fl != nil {
				t.Fatalf("ByID(%q) expected nil flavor on error, got %#v", tt.id, fl)
			}
		})
	}
}
