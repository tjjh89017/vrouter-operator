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

import (
	"regexp"
	"strings"
	"testing"
)

// heredocStartRe matches the line that opens the config heredoc and captures
// the quoted delimiter, e.g. `load /dev/stdin <<'DELIM'`.
var heredocStartRe = regexp.MustCompile(`^load /dev/stdin <<'([^']+)'$`)

// extractHeredocBody simulates how bash captures a quoted heredoc: it finds the
// opening `load /dev/stdin <<'DELIM'` line, then collects every following line
// up to (but not including) the first line that is exactly DELIM. It returns the
// captured body, the delimiter, and the script lines that come after the closing
// delimiter (which bash would execute as commands).
func extractHeredocBody(t *testing.T, script string) (body, delimiter string, after []string) {
	t.Helper()
	lines := strings.Split(script, "\n")
	start := -1
	for i, line := range lines {
		if m := heredocStartRe.FindStringSubmatch(line); m != nil {
			start = i
			delimiter = m[1]
			break
		}
	}
	if start == -1 {
		t.Fatalf("no config heredoc found in script:\n%s", script)
	}
	for i := start + 1; i < len(lines); i++ {
		if lines[i] == delimiter {
			return strings.Join(lines[start+1:i], "\n"), delimiter, lines[i+1:]
		}
	}
	t.Fatalf("heredoc delimiter %q never closed in script:\n%s", delimiter, script)
	return "", "", nil
}

// TestRenderScript_MaliciousConfigCannotEscapeHeredoc verifies that config
// content which contains the naive fixed delimiter (and other terminator-looking
// lines) cannot break out of the heredoc and inject shell commands.
func TestRenderScript_MaliciousConfigCannotEscapeHeredoc(t *testing.T) {
	// Arrange: config crafted to close the old, fixed delimiter early and then
	// run an arbitrary command as root.
	marker := "rm -rf / # pwned by injected command"
	malicious := strings.Join([]string{
		"system {",
		"    host-name router",
		"}",
		"VYOS_CONFIG_EOF",
		marker,
	}, "\n")

	// Act
	out, err := RenderScript(malicious, "", true)
	if err != nil {
		t.Fatalf("RenderScript returned unexpected error: %v", err)
	}
	script := string(out)

	// Assert: the whole malicious config stays inside the heredoc body, and the
	// injected marker never appears as an executable line after the delimiter.
	body, delimiter, after := extractHeredocBody(t, script)
	if body != malicious {
		t.Fatalf("config content was altered or truncated inside heredoc.\nwant:\n%s\ngot:\n%s", malicious, body)
	}
	for _, line := range after {
		if strings.Contains(line, marker) {
			t.Fatalf("injected command escaped the heredoc and would run as root: %q", line)
		}
	}
	// The chosen delimiter must not collide with any line of the config.
	for _, line := range strings.Split(malicious, "\n") {
		if line == delimiter {
			t.Fatalf("delimiter %q collides with a config line, allowing breakout", delimiter)
		}
	}
}

// TestRenderScript_BenignConfigIntact verifies that a normal config still renders
// correctly inside the heredoc and the script structure stays intact.
func TestRenderScript_BenignConfigIntact(t *testing.T) {
	config := strings.Join([]string{
		"interfaces {",
		"    ethernet eth0 {",
		"        address 192.0.2.1/24",
		"    }",
		"}",
	}, "\n")

	out, err := RenderScript(config, "run show version", true)
	if err != nil {
		t.Fatalf("RenderScript returned unexpected error: %v", err)
	}
	script := string(out)

	body, _, _ := extractHeredocBody(t, script)
	if body != config {
		t.Fatalf("benign config not preserved inside heredoc.\nwant:\n%s\ngot:\n%s", config, body)
	}

	for _, want := range []string{"configure", "run show version", "commit", "save"} {
		if !strings.Contains(script, want) {
			t.Fatalf("expected rendered script to contain %q, script:\n%s", want, script)
		}
	}
}

// TestRenderScript_DelimiterIsUnpredictable verifies the delimiter is not the old
// fixed value and differs between renders, so it cannot be guessed and embedded.
func TestRenderScript_DelimiterIsUnpredictable(t *testing.T) {
	config := "system {\n    host-name router\n}"

	out1, err := RenderScript(config, "", false)
	if err != nil {
		t.Fatalf("RenderScript error: %v", err)
	}
	out2, err := RenderScript(config, "", false)
	if err != nil {
		t.Fatalf("RenderScript error: %v", err)
	}

	_, delim1, _ := extractHeredocBody(t, string(out1))
	_, delim2, _ := extractHeredocBody(t, string(out2))

	if delim1 == "VYOS_CONFIG_EOF" || delim2 == "VYOS_CONFIG_EOF" {
		t.Fatalf("delimiter is still the predictable fixed value")
	}
	if delim1 == delim2 {
		t.Fatalf("delimiter is not unpredictable between renders: %q", delim1)
	}
}
