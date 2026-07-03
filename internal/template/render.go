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

package template

import (
	"bytes"
	"encoding/json"
	"fmt"
	"text/template"

	"dario.cat/mergo"
	"github.com/Masterminds/sprig/v3"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
)

// deniedFuncs lists sprig helpers that must not be reachable from templates.
// Templates render inside the operator process, so any function that reads
// process environment variables or performs network lookups could let a
// VRouterTemplate author exfiltrate operator secrets (env, expandenv) or
// trigger unwanted DNS lookups (getHostByName). SPEC §5.2 only intends safe,
// pure helper functions (default, required, has, toYaml, range, etc.).
var deniedFuncs = []string{"env", "expandenv", "getHostByName"}

// templateFuncs returns the sprig function map with dangerous functions
// removed, so they are unavailable to templates ("function ... not defined").
func templateFuncs() template.FuncMap {
	funcs := sprig.TxtFuncMap()
	for _, name := range deniedFuncs {
		delete(funcs, name)
	}
	return funcs
}

// Render executes a Go text/template with sprig functions against the given data map.
func Render(tmplStr string, data map[string]any) (string, error) {
	tmpl, err := template.New("").Funcs(templateFuncs()).Parse(tmplStr)
	if err != nil {
		return "", fmt.Errorf("parse template: %w", err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("execute template: %w", err)
	}
	return buf.String(), nil
}

// MergeParams deep-merges base and override JSON params.
// override takes priority; slices are replaced (not appended).
// Either or both may be nil/empty — treated as empty object.
func MergeParams(base, override apiextensionsv1.JSON) (map[string]any, error) {
	baseMap, err := jsonToMap(base)
	if err != nil {
		return nil, fmt.Errorf("decode base params: %w", err)
	}
	overMap, err := jsonToMap(override)
	if err != nil {
		return nil, fmt.Errorf("decode override params: %w", err)
	}
	// mergo.WithOverride makes override zero values (false, "", 0) win over
	// base values, not just non-zero ones. Once JSON is decoded there is no
	// way to distinguish "explicitly set to false" from "absent", so a
	// truthy base default (e.g. firewall.enabled: true) can be silently
	// turned off by an override that merely mentions the key. See
	// merge_params_test.go for characterization tests pinning this
	// behavior and docs/SPEC.md §4.3, which is still pending a correction
	// to match reality.
	if err := mergo.Merge(&baseMap, overMap,
		mergo.WithOverride,
		mergo.WithOverrideEmptySlice,
	); err != nil {
		return nil, fmt.Errorf("merge params: %w", err)
	}
	return baseMap, nil
}

func jsonToMap(j apiextensionsv1.JSON) (map[string]any, error) {
	if len(j.Raw) == 0 {
		return map[string]any{}, nil
	}
	var m map[string]any
	dec := json.NewDecoder(bytes.NewReader(j.Raw))
	dec.UseNumber()
	if err := dec.Decode(&m); err != nil {
		return nil, err
	}
	return m, nil
}
