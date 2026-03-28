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
	"text/template"
)

var applyScriptTmpl = template.Must(template.New("apply").Parse(`#!/bin/vbash
if [ "$(id -g -n)" != 'vyattacfg' ] ; then
    exec sg vyattacfg -c "/bin/vbash $(readlink -f $0) $@"
fi
source /opt/vyatta/etc/functions/script-template
configure

# --- config section ---
{{- if .Config }}
load /dev/stdin <<'VYOS_CONFIG_EOF'
{{ .Config }}
VYOS_CONFIG_EOF
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
	Config   string
	Commands string
	Save     bool
}

// RenderScript renders a vbash apply script for the given VyOS config params.
func RenderScript(config, commands string, save bool) ([]byte, error) {
	var buf bytes.Buffer
	if err := applyScriptTmpl.Execute(&buf, scriptData{
		Config:   config,
		Commands: commands,
		Save:     save,
	}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
