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
	"encoding/json"
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
)

// These tests characterize (pin) the actual behavior of MergeParams's
// mergo.Merge call, observed by running the merge and inspecting the
// output. They exist so that any future change to the merge options
// (e.g. an upgrade of dario.cat/mergo, or a tweak to the WithOverride /
// WithOverrideEmptySlice flags) is caught by a failing test instead of
// silently changing behavior in production. See docs/SPEC.md §4.3 for the
// design intent; that doc still needs a follow-up correction (tracked
// separately) since the actual behavior below is stricter than "override
// nil keeps base value" for zero values.

// TestMergeParams_ZeroValueOverrideWinsOverTruthyBase pins the
// security-relevant footgun: when the override JSON explicitly sets a key
// to its Go zero value (false, "", 0), that zero value wins over a
// truthy/non-empty base value. Once JSON is decoded there is no way to
// tell "explicitly set to false" apart from "not set", so a base default
// like a firewall being enabled can be silently turned off by an override
// that merely mentions the key.
func TestMergeParams_ZeroValueOverrideWinsOverTruthyBase(t *testing.T) {
	base := apiextensionsv1.JSON{Raw: []byte(`{
		"enabled": true,
		"name": "base-name",
		"count": 5
	}`)}
	override := apiextensionsv1.JSON{Raw: []byte(`{
		"enabled": false,
		"name": "",
		"count": 0
	}`)}

	result, err := MergeParams(base, override)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got := result["enabled"]; got != false {
		t.Errorf("enabled: got %#v, want false (override zero value must win over truthy base)", got)
	}
	if got := result["name"]; got != "" {
		t.Errorf("name: got %#v, want \"\" (override zero value must win over non-empty base)", got)
	}
	if got, want := result["count"].(json.Number).String(), "0"; got != want {
		t.Errorf("count: got %v, want %v (override zero value must win over non-zero base)", got, want)
	}
}

// TestMergeParams_BaseOnlyKeyIsPreserved pins that a key present only in
// base (not mentioned at all in override) survives the merge unchanged.
func TestMergeParams_BaseOnlyKeyIsPreserved(t *testing.T) {
	base := apiextensionsv1.JSON{Raw: []byte(`{"onlyInBase": "keep-me"}`)}
	override := apiextensionsv1.JSON{Raw: []byte(`{"other": "value"}`)}

	result, err := MergeParams(base, override)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got := result["onlyInBase"]; got != "keep-me" {
		t.Errorf("onlyInBase: got %#v, want \"keep-me\" (base-only key must be preserved)", got)
	}
	if got := result["other"]; got != "value" {
		t.Errorf("other: got %#v, want \"value\"", got)
	}
}

// TestMergeParams_NestedMapMergesKeyByKey pins that nested maps are merged
// field-by-field rather than the override map replacing the base map
// wholesale: keys only present in the base nested map survive, and keys
// present in both are taken from the override.
func TestMergeParams_NestedMapMergesKeyByKey(t *testing.T) {
	base := apiextensionsv1.JSON{Raw: []byte(`{
		"nested": {"a": 1, "b": "base-b", "onlyBase": "x"}
	}`)}
	override := apiextensionsv1.JSON{Raw: []byte(`{
		"nested": {"b": "override-b"}
	}`)}

	result, err := MergeParams(base, override)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	nested, ok := result["nested"].(map[string]any)
	if !ok {
		t.Fatalf("nested: got %#v, want map[string]any", result["nested"])
	}
	if got, want := nested["b"], "override-b"; got != want {
		t.Errorf("nested.b: got %#v, want %#v (override value must win for shared key)", got, want)
	}
	if got, want := nested["onlyBase"], "x"; got != want {
		t.Errorf("nested.onlyBase: got %#v, want %#v (base-only nested key must be preserved)", got, want)
	}
	if _, ok := nested["a"]; !ok {
		t.Errorf("nested.a: missing, want it preserved from base (base-only nested key)")
	}
}

// TestMergeParams_NonEmptyOverrideSliceReplacesBaseSlice pins that a
// non-empty override slice fully replaces the base slice rather than
// appending to it, matching the MergeParams doc comment ("slices are
// replaced (not appended)").
func TestMergeParams_NonEmptyOverrideSliceReplacesBaseSlice(t *testing.T) {
	base := apiextensionsv1.JSON{Raw: []byte(`{"list": [1, 2, 3]}`)}
	override := apiextensionsv1.JSON{Raw: []byte(`{"list": [9]}`)}

	result, err := MergeParams(base, override)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	list, ok := result["list"].([]any)
	if !ok {
		t.Fatalf("list: got %#v, want []any", result["list"])
	}
	if len(list) != 1 {
		t.Fatalf("list: got %#v, want a single-element slice replacing the base slice", list)
	}
	if got, want := list[0].(json.Number).String(), "9"; got != want {
		t.Errorf("list[0]: got %v, want %v", got, want)
	}
}

// TestMergeParams_EmptyOverrideSliceReplacesBaseSlice pins the
// WithOverrideEmptySlice behavior: an override that explicitly sets a key
// to an empty slice ([]) replaces a non-empty base slice with an empty
// one, rather than being treated as "not set" and falling back to base.
func TestMergeParams_EmptyOverrideSliceReplacesBaseSlice(t *testing.T) {
	base := apiextensionsv1.JSON{Raw: []byte(`{"list": [1, 2, 3]}`)}
	override := apiextensionsv1.JSON{Raw: []byte(`{"list": []}`)}

	result, err := MergeParams(base, override)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	list, ok := result["list"].([]any)
	if !ok {
		t.Fatalf("list: got %#v, want []any", result["list"])
	}
	if len(list) != 0 {
		t.Errorf("list: got %#v, want an empty slice (WithOverrideEmptySlice must let an explicit empty override win)", list)
	}
}
