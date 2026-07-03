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
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"text/template"
)

// heredocDelimiterPrefix is the stable, human-readable prefix for the config
// heredoc delimiter. A random suffix is appended per render so the delimiter
// cannot be predicted and embedded in the config to break out of the heredoc.
const heredocDelimiterPrefix = "VROUTER_CONFIG_EOF_"

// heredocDelimiterAttempts bounds how many times we grow the random suffix
// while trying to find a delimiter that does not collide with the config.
const heredocDelimiterAttempts = 8

var applyScriptTmpl = template.Must(template.New("apply").Parse(`#!/bin/vbash
if [ "$(id -g -n)" != 'vyattacfg' ] ; then
    exec sg vyattacfg -c "/bin/vbash $(readlink -f $0) $@"
fi
source /opt/vyatta/etc/functions/script-template
configure

# --- config section ---
{{- if .Config }}
load /dev/stdin <<'{{ .Delimiter }}'
{{ .Config }}
{{ .Delimiter }}
{{- else }}
load /opt/vyatta/etc/config.boot.default
{{- end }}

# --- commands section (optional) ---
{{- if .Commands }}
{{ .Commands }}
{{- end }}

commit
{{- if .Save }}
save
{{- end }}
`))

type scriptData struct {
	Config    string
	Commands  string
	Save      bool
	Delimiter string
}

// heredocDelimiter returns a random, unpredictable delimiter that is guaranteed
// not to appear as a standalone line inside config. The config is untrusted
// (rendered from lower-privileged user params), so a fixed delimiter would let a
// crafted line close the heredoc early and run the remainder as root. We derive
// the delimiter from crypto/rand and, on the astronomically unlikely chance it
// still collides with a config line, grow the random suffix and retry.
func heredocDelimiter(config string) (string, error) {
	// Index the config lines once so each candidate is an O(1) lookup.
	lines := make(map[string]struct{})
	for _, line := range strings.Split(config, "\n") {
		lines[line] = struct{}{}
	}

	size := 16
	for attempt := 0; attempt < heredocDelimiterAttempts; attempt++ {
		buf := make([]byte, size)
		if _, err := rand.Read(buf); err != nil {
			return "", fmt.Errorf("generating heredoc delimiter: %w", err)
		}
		candidate := heredocDelimiterPrefix + hex.EncodeToString(buf)
		if _, collides := lines[candidate]; !collides {
			return candidate, nil
		}
		size *= 2
	}
	return "", fmt.Errorf("unable to generate collision-free heredoc delimiter after %d attempts", heredocDelimiterAttempts)
}

// RenderScript renders a vbash apply script for the given VyOS config params.
func RenderScript(config, commands string, save bool) ([]byte, error) {
	delimiter, err := heredocDelimiter(config)
	if err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	if err := applyScriptTmpl.Execute(&buf, scriptData{
		Config:    config,
		Commands:  commands,
		Save:      save,
		Delimiter: delimiter,
	}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
