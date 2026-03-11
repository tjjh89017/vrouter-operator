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

// Render executes a Go text/template with sprig functions against the given data map.
func Render(tmplStr string, data map[string]any) (string, error) {
	tmpl, err := template.New("").Funcs(sprig.TxtFuncMap()).Parse(tmplStr)
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
