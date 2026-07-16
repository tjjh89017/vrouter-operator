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

package kubevirt

import (
	"strings"
	"testing"
)

// TestParseOSReleaseID verifies parseOSReleaseID extracts the ID field from
// /etc/os-release text: it strips surrounding quotes and whitespace, matches
// only the exact "ID=" key (never "ID_LIKE="), and returns "" when absent.
func TestParseOSReleaseID(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{
			name:    "unquoted value",
			content: "ID=dozenos",
			want:    "dozenos",
		},
		{
			name:    "double-quoted value",
			content: `ID="vyos"`,
			want:    "vyos",
		},
		{
			name:    "single-quoted value",
			content: "ID='vyos'",
			want:    "vyos",
		},
		{
			name:    "ID_LIKE present but no ID must not match",
			content: "ID_LIKE=debian",
			want:    "",
		},
		{
			name: "realistic os-release with ID_LIKE before and after ID",
			content: strings.Join([]string{
				`NAME="VyOS"`,
				`PRETTY_NAME="VyOS 1.4"`,
				`ID_LIKE=debian`,
				`ID=vyos`,
				`VERSION_ID="1.4"`,
				`ID_LIKE=debian`,
			}, "\n"),
			want: "vyos",
		},
		{
			name:    "CRLF line endings strip trailing carriage return",
			content: "NAME=\"DozenOS\"\r\nID=dozenos\r\nVERSION_ID=\"1.0\"\r\n",
			want:    "dozenos",
		},
		{
			name:    "leading and trailing whitespace around the line",
			content: "   \t ID=vyos \t  ",
			want:    "vyos",
		},
		{
			name: "missing ID entirely returns empty",
			content: strings.Join([]string{
				`NAME="SomeOS"`,
				`ID_LIKE=debian`,
				`VERSION_ID="9"`,
			}, "\n"),
			want: "",
		},
		{
			name:    "empty content returns empty",
			content: "",
			want:    "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got := parseOSReleaseID(tt.content)

			// Assert
			if got != tt.want {
				t.Fatalf("parseOSReleaseID(%q) = %q, want %q", tt.content, got, tt.want)
			}
		})
	}
}
