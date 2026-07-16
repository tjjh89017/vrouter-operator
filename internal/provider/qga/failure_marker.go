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

import "strings"

// applyFailureMarkers is a small, curated list of failure markers emitted by
// the router config validator (shared by the vyatta-family flavors). Kept as an
// explicit list rather than a broad regex to avoid false positives on unrelated
// text that merely contains the word "failed". The canonical (title-cased)
// form is returned to callers for diagnostics; matching itself is
// case-insensitive.
var applyFailureMarkers = []string{
	"Set failed",
	"Commit failed",
	"Delete failed",
}

// ApplyOutputIndicatesFailure reports whether combined apply output (stdout+stderr)
// from the router's vbash apply script contains a failure marker that the guest
// exit code can mask (a set-time validation failure still lets `commit` exit 0
// when `load` left a committable diff). Returns the matched marker for diagnostics.
//
// Matching is case-insensitive against a curated marker list; the first marker
// found is returned. When no marker is present it returns ("", false).
func ApplyOutputIndicatesFailure(output string) (marker string, failed bool) {
	lower := strings.ToLower(output)
	for _, m := range applyFailureMarkers {
		if strings.Contains(lower, strings.ToLower(m)) {
			return m, true
		}
	}
	return "", false
}
