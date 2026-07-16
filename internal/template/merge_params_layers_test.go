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
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
)

// These tests pin the merge priority required by issue #34: paramsRefs (in
// list order) are the lowest-priority layer, binding.Params overrides them,
// and target.Params overrides everything. MergeParamsLayers is the fold
// helper that implements this by repeatedly calling MergeParams in order.

// TestMergeParamsLayers_TargetWinsOverBindingAndParamsRef pins the full
// three-layer priority: target beats binding, binding beats paramsRef.
func TestMergeParamsLayers_TargetWinsOverBindingAndParamsRef(t *testing.T) {
	paramsRef := apiextensionsv1.JSON{Raw: []byte(`{"key": "from-ref"}`)}
	bindingParams := apiextensionsv1.JSON{Raw: []byte(`{"key": "from-binding"}`)}
	targetParams := apiextensionsv1.JSON{Raw: []byte(`{"key": "from-target"}`)}

	result, err := MergeParamsLayers(paramsRef, bindingParams, targetParams)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got, want := result["key"], "from-target"; got != want {
		t.Errorf("key: got %#v, want %#v (target must win over binding and paramsRef)", got, want)
	}
}

// TestMergeParamsLayers_BindingOverridesParamsRefWhenTargetSilent pins that,
// absent a target override, binding.Params wins over the paramsRef layer,
// and a key only present in the paramsRef layer survives unchanged.
func TestMergeParamsLayers_BindingOverridesParamsRefWhenTargetSilent(t *testing.T) {
	paramsRef := apiextensionsv1.JSON{Raw: []byte(`{"key": "from-ref", "onlyRef": "r"}`)}
	bindingParams := apiextensionsv1.JSON{Raw: []byte(`{"key": "from-binding"}`)}
	targetParams := apiextensionsv1.JSON{}

	result, err := MergeParamsLayers(paramsRef, bindingParams, targetParams)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got, want := result["key"], "from-binding"; got != want {
		t.Errorf("key: got %#v, want %#v (binding.Params must win over paramsRef when target is silent)", got, want)
	}
	if got, want := result["onlyRef"], "r"; got != want {
		t.Errorf("onlyRef: got %#v, want %#v (paramsRef-only key must survive)", got, want)
	}
}

// TestMergeParamsLayers_MultipleParamsRefsLaterOverridesEarlier pins that
// among multiple paramsRefs, later entries in the list override earlier
// ones, matching the spec comment on VRouterBindingSpec.ParamsRefs.
func TestMergeParamsLayers_MultipleParamsRefsLaterOverridesEarlier(t *testing.T) {
	ref1 := apiextensionsv1.JSON{Raw: []byte(`{"key": "ref1", "onlyRef1": "a"}`)}
	ref2 := apiextensionsv1.JSON{Raw: []byte(`{"key": "ref2"}`)}
	bindingParams := apiextensionsv1.JSON{}
	targetParams := apiextensionsv1.JSON{}

	result, err := MergeParamsLayers(ref1, ref2, bindingParams, targetParams)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got, want := result["key"], "ref2"; got != want {
		t.Errorf("key: got %#v, want %#v (later paramsRef must override earlier paramsRef)", got, want)
	}
	if got, want := result["onlyRef1"], "a"; got != want {
		t.Errorf("onlyRef1: got %#v, want %#v (earlier-paramsRef-only key must survive)", got, want)
	}
}

// TestMergeParamsLayers_NoLayersReturnsEmptyMap pins that calling
// MergeParamsLayers with no layers at all does not crash and returns an
// empty, non-nil map.
func TestMergeParamsLayers_NoLayersReturnsEmptyMap(t *testing.T) {
	result, err := MergeParamsLayers()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 0 {
		t.Errorf("result: got %#v, want empty map", result)
	}
}

// TestMergeParamsLayers_EmptyAndNullLayersPassThrough pins that an empty
// paramsRef entry and an empty binding.Params layer do not crash and do not
// clobber values coming from other layers (they behave as "no-op" layers).
func TestMergeParamsLayers_EmptyAndNullLayersPassThrough(t *testing.T) {
	emptyParamsRef := apiextensionsv1.JSON{} // e.g. a VRouterParams with no .spec.params set
	bindingParams := apiextensionsv1.JSON{}  // binding.Spec.Params left unset
	targetParams := apiextensionsv1.JSON{Raw: []byte(`{"key": "from-target"}`)}

	result, err := MergeParamsLayers(emptyParamsRef, bindingParams, targetParams)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got, want := result["key"], "from-target"; got != want {
		t.Errorf("key: got %#v, want %#v (empty layers must not clobber a later real value)", got, want)
	}
}

// TestMergeParamsLayers_NestedMapMergesKeyByKeyAcrossLayers pins that nested
// maps merge field-by-field across all three layers, not just the last two:
// a key only in the paramsRef layer, a key only in binding, and a key only
// in target must all survive together in the same nested object, and a
// shared key must resolve to the highest-priority layer that sets it.
func TestMergeParamsLayers_NestedMapMergesKeyByKeyAcrossLayers(t *testing.T) {
	paramsRef := apiextensionsv1.JSON{Raw: []byte(`{
		"nested": {"a": 1, "b": "ref-b", "onlyRef": "r"}
	}`)}
	bindingParams := apiextensionsv1.JSON{Raw: []byte(`{
		"nested": {"b": "binding-b", "onlyBinding": "bd"}
	}`)}
	targetParams := apiextensionsv1.JSON{Raw: []byte(`{
		"nested": {"onlyTarget": "t"}
	}`)}

	result, err := MergeParamsLayers(paramsRef, bindingParams, targetParams)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	nested, ok := result["nested"].(map[string]any)
	if !ok {
		t.Fatalf("nested: got %#v, want map[string]any", result["nested"])
	}
	if _, ok := nested["a"]; !ok {
		t.Errorf("nested.a: missing, want it preserved from paramsRef layer")
	}
	if got, want := nested["b"], "binding-b"; got != want {
		t.Errorf("nested.b: got %#v, want %#v (binding must win over paramsRef when target is silent on this key)", got, want)
	}
	if got, want := nested["onlyRef"], "r"; got != want {
		t.Errorf("nested.onlyRef: got %#v, want %#v (paramsRef-only nested key must survive)", got, want)
	}
	if got, want := nested["onlyBinding"], "bd"; got != want {
		t.Errorf("nested.onlyBinding: got %#v, want %#v (binding-only nested key must survive)", got, want)
	}
	if got, want := nested["onlyTarget"], "t"; got != want {
		t.Errorf("nested.onlyTarget: got %#v, want %#v (target-only nested key must survive)", got, want)
	}
}
