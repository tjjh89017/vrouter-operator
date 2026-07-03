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
	"strings"
	"testing"
)

// TestRender_DeniesEnvFunc asserts that a template calling the sprig "env"
// function fails to render, since it is not registered in the FuncMap.
// This prevents a VRouterTemplate author from reading the operator
// process's environment variables (which may hold injected secrets).
func TestRender_DeniesEnvFunc(t *testing.T) {
	t.Setenv("HOME", "/should/not/leak")

	out, err := Render(`{{ env "HOME" }}`, map[string]any{})
	if err == nil {
		t.Fatalf("expected error for disabled env func, got output %q", out)
	}
	if !strings.Contains(err.Error(), "function \"env\" not defined") {
		t.Fatalf("expected 'function \"env\" not defined' error, got: %v", err)
	}
}

// TestRender_DeniesExpandenvFunc asserts that a template calling the sprig
// "expandenv" function fails to render for the same reason as env above.
func TestRender_DeniesExpandenvFunc(t *testing.T) {
	t.Setenv("HOME", "/should/not/leak")

	out, err := Render(`{{ expandenv "$HOME" }}`, map[string]any{})
	if err == nil {
		t.Fatalf("expected error for disabled expandenv func, got output %q", out)
	}
	if !strings.Contains(err.Error(), "function \"expandenv\" not defined") {
		t.Fatalf("expected 'function \"expandenv\" not defined' error, got: %v", err)
	}
}

// TestRender_DeniesGetHostByNameFunc asserts that a template calling the
// sprig "getHostByName" function fails to render, since it lets a template
// author trigger DNS lookups from the operator process.
func TestRender_DeniesGetHostByNameFunc(t *testing.T) {
	out, err := Render(`{{ getHostByName "localhost" }}`, map[string]any{})
	if err == nil {
		t.Fatalf("expected error for disabled getHostByName func, got output %q", out)
	}
	if !strings.Contains(err.Error(), "function \"getHostByName\" not defined") {
		t.Fatalf("expected 'function \"getHostByName\" not defined' error, got: %v", err)
	}
}

// TestRender_BenignTemplateStillWorks proves that removing the dangerous
// functions does not break normal template usage: field interpolation and
// the sprig "default" helper (used by SPEC §5.2) both still work.
func TestRender_BenignTemplateStillWorks(t *testing.T) {
	data := map[string]any{
		"Name": "router-1",
	}

	out, err := Render(`hostname {{ .Name }}; mtu {{ .MTU | default "1500" }};`, data)
	if err != nil {
		t.Fatalf("unexpected error rendering benign template: %v", err)
	}

	want := `hostname router-1; mtu 1500;`
	if out != want {
		t.Fatalf("unexpected render output: got %q, want %q", out, want)
	}
}
