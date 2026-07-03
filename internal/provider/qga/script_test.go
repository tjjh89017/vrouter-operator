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
	"os/exec"
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

// TestRenderScript_EmptyConfig_LoadsDefaultBoot verifies that an empty
// config skips the heredoc entirely and falls back to loading the default
// boot config, exercising the template's {{ else }} branch (untested
// before: every other test in this file passes a non-empty config).
func TestRenderScript_EmptyConfig_LoadsDefaultBoot(t *testing.T) {
	out, err := RenderScript("", "", true)
	if err != nil {
		t.Fatalf("RenderScript returned unexpected error: %v", err)
	}
	script := string(out)

	if strings.Contains(script, "load /dev/stdin") {
		t.Fatalf("expected no heredoc for an empty config, script:\n%s", script)
	}
	if !strings.Contains(script, "load /opt/vyatta/etc/config.boot.default") {
		t.Fatalf("expected the default-boot-config fallback line, script:\n%s", script)
	}
}

// TestRenderScript_SaveFalse_OmitsSaveLine is the negative counterpart to
// TestRenderScript_BenignConfigIntact's save=true assertion: previously
// nothing in this file asserted that save=false actually *omits* the
// "save" line, only that save=true includes it.
func TestRenderScript_SaveFalse_OmitsSaveLine(t *testing.T) {
	out, err := RenderScript("system {\n    host-name router\n}", "", false)
	if err != nil {
		t.Fatalf("RenderScript returned unexpected error: %v", err)
	}
	script := string(out)

	if !strings.Contains(script, "commit") {
		t.Fatalf("expected the script to still contain commit, script:\n%s", script)
	}
	for _, line := range strings.Split(script, "\n") {
		if strings.TrimSpace(line) == "save" {
			t.Fatalf("save=false must omit the \"save\" line, script:\n%s", script)
		}
	}
}

// TestRenderScript_EmptyCommands_OmitsCommandsSection verifies that an empty
// commands string produces a script with no content between the config
// section and "commit" -- as opposed to an empty line or a stray marker.
func TestRenderScript_EmptyCommands_OmitsCommandsSection(t *testing.T) {
	out, err := RenderScript("system {\n    host-name router\n}", "", true)
	if err != nil {
		t.Fatalf("RenderScript returned unexpected error: %v", err)
	}
	script := string(out)

	_, _, after := extractHeredocBody(t, script)
	afterJoined := strings.Join(after, "\n")
	if strings.Contains(afterJoined, "run ") {
		t.Fatalf("expected no commands content for an empty Commands string, script:\n%s", script)
	}
}

// TestRenderScript_MultiLineCommands_AllLinesPreserved verifies that a
// multi-line Commands string is rendered verbatim (all lines present, in
// order), not just a single command line as every other test in this file
// uses.
func TestRenderScript_MultiLineCommands_AllLinesPreserved(t *testing.T) {
	commands := strings.Join([]string{
		"set interfaces ethernet eth0 address 192.0.2.1/24",
		"set protocols static route 0.0.0.0/0 next-hop 192.0.2.254",
		"run show interfaces",
	}, "\n")

	out, err := RenderScript("system {\n    host-name router\n}", commands, true)
	if err != nil {
		t.Fatalf("RenderScript returned unexpected error: %v", err)
	}
	script := string(out)

	for _, line := range strings.Split(commands, "\n") {
		if !strings.Contains(script, line) {
			t.Fatalf("expected script to contain command line %q, script:\n%s", line, script)
		}
	}
}

// TestRenderScript_HeredocQuotingPreventsRealShellExpansion is the one test
// in this file that actually hands the rendered heredoc to a real bash
// process, rather than only parsing it as a Go string. The other tests in
// this file (extractHeredocBody-based) prove the delimiter cannot collide
// with a config line, but that alone does not prove bash won't expand
// shell metacharacters (command substitution, variable expansion) embedded
// in the config -- that guarantee comes from the heredoc being quoted
// (<<'DELIM'), a detail this test verifies against a real bash, not just a
// regex match on the template output.
func TestRenderScript_HeredocQuotingPreventsRealShellExpansion(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available in test environment")
	}

	config := strings.Join([]string{
		"before-marker",
		"$(echo COMMAND_SUBSTITUTION_RAN)",
		"`echo BACKTICK_SUBSTITUTION_RAN`",
		"$HOME-should-stay-literal",
		"after-marker",
	}, "\n")

	out, err := RenderScript(config, "", false)
	if err != nil {
		t.Fatalf("RenderScript returned unexpected error: %v", err)
	}
	script := string(out)

	_, delimiter, _ := extractHeredocBody(t, script)

	// Feed just the heredoc idiom (same quoted delimiter RenderScript chose)
	// to a real bash and capture what "cat" actually receives -- proving the
	// quoting bash applies, not a Go-side string comparison.
	bashScript := "cat <<'" + delimiter + "'\n" + config + "\n" + delimiter + "\n"
	cmd := exec.Command("bash", "-c", bashScript)
	outBytes, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("running bash heredoc failed: %v, output:\n%s", err, outBytes)
	}
	got := string(outBytes)

	// A byte-for-byte match against the original config is the real
	// assertion: if bash had expanded $(...), `...`, or $HOME, got would
	// contain the *expanded* command output instead of the literal source
	// text, so it would no longer equal config verbatim. (Checking for the
	// presence of e.g. "COMMAND_SUBSTITUTION_RAN" as a separate assertion
	// would be wrong here -- that substring is part of the literal
	// unexpanded command text itself, "$(echo COMMAND_SUBSTITUTION_RAN)",
	// so it is present in `got` either way and proves nothing on its own.)
	if got != config+"\n" {
		t.Fatalf("bash did not pass the heredoc body through unmodified -- shell metacharacters were expanded.\nwant:\n%q\ngot:\n%q", config+"\n", got)
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
