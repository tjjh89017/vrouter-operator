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

package v1

import (
	"context"
	"strings"
	"testing"

	vrouterv1 "github.com/tjjh89017/vrouter-operator/api/v1"
)

// These tests exercise VRouterTemplateCustomValidator/Defaulter directly (no fake
// client, no envtest) since validateTemplateSpec only parses Go template syntax
// and never talks to the API server.

func TestVRouterTemplateValidateCreate_ValidSyntax_Accepted(t *testing.T) {
	tmpl := &vrouterv1.VRouterTemplate{
		ObjectMeta: metaObj("tmpl1"),
		Spec: vrouterv1.VRouterTemplateSpec{
			Config:   "interface {{ .IfaceName }} { address {{ .Address }} }",
			Commands: "commit\n{{- if .Save }}\nsave\n{{- end }}",
		},
	}
	v := &VRouterTemplateCustomValidator{}
	if _, err := v.ValidateCreate(context.Background(), tmpl); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}

func TestVRouterTemplateValidateCreate_InvalidConfigSyntax_Rejected(t *testing.T) {
	tmpl := &vrouterv1.VRouterTemplate{
		ObjectMeta: metaObj("tmpl-bad-config"),
		Spec: vrouterv1.VRouterTemplateSpec{
			// Unclosed action: missing the closing "}}".
			Config: "interface {{ .IfaceName",
		},
	}
	v := &VRouterTemplateCustomValidator{}
	_, err := v.ValidateCreate(context.Background(), tmpl)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "spec.config: invalid template syntax") {
		t.Fatalf("error = %q, want substring %q", err.Error(), "spec.config: invalid template syntax")
	}
}

func TestVRouterTemplateValidateCreate_InvalidCommandsSyntax_Rejected(t *testing.T) {
	tmpl := &vrouterv1.VRouterTemplate{
		ObjectMeta: metaObj("tmpl-bad-commands"),
		Spec: vrouterv1.VRouterTemplateSpec{
			Config: "interface eth0 { }",
			// Unclosed "if" block: no matching "{{ end }}".
			Commands: "{{ if .Save }}\nsave",
		},
	}
	v := &VRouterTemplateCustomValidator{}
	_, err := v.ValidateCreate(context.Background(), tmpl)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "spec.commands: invalid template syntax") {
		t.Fatalf("error = %q, want substring %q", err.Error(), "spec.commands: invalid template syntax")
	}
}

func TestVRouterTemplateValidateUpdate_ValidSyntax_Accepted(t *testing.T) {
	oldTmpl := &vrouterv1.VRouterTemplate{ObjectMeta: metaObj("tmpl1")}
	newTmpl := &vrouterv1.VRouterTemplate{
		ObjectMeta: metaObj("tmpl1"),
		Spec: vrouterv1.VRouterTemplateSpec{
			Config: "interface {{ .IfaceName }} { }",
		},
	}
	v := &VRouterTemplateCustomValidator{}
	if _, err := v.ValidateUpdate(context.Background(), oldTmpl, newTmpl); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}

func TestVRouterTemplateValidateUpdate_InvalidSyntax_Rejected(t *testing.T) {
	oldTmpl := &vrouterv1.VRouterTemplate{ObjectMeta: metaObj("tmpl1")}
	newTmpl := &vrouterv1.VRouterTemplate{
		ObjectMeta: metaObj("tmpl1"),
		Spec: vrouterv1.VRouterTemplateSpec{
			Config: "interface {{ .IfaceName",
		},
	}
	v := &VRouterTemplateCustomValidator{}
	_, err := v.ValidateUpdate(context.Background(), oldTmpl, newTmpl)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "spec.config: invalid template syntax") {
		t.Fatalf("error = %q, want substring %q", err.Error(), "spec.config: invalid template syntax")
	}
}

// TestVRouterTemplateValidateCreate_DeniedFunc_Rejected asserts that a
// template calling a sprig function removed from the render-time FuncMap
// (env, expandenv, getHostByName — see internal/template.Funcs) is rejected
// at admission. Before this test, the webhook parsed templates with the full,
// unfiltered sprig.TxtFuncMap(), so such a template was accepted here and
// only failed later at reconcile with a "function ... not defined" error,
// contradicting SPEC §6.2 ("parsed with the same sprig FuncMap used at
// render time").
func TestVRouterTemplateValidateCreate_DeniedFunc_Rejected(t *testing.T) {
	for _, tc := range []struct {
		name   string
		config string
	}{
		{name: "env", config: `{{ env "HOME" }}`},
		{name: "expandenv", config: `{{ expandenv "$HOME" }}`},
		{name: "getHostByName", config: `{{ getHostByName "localhost" }}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tmpl := &vrouterv1.VRouterTemplate{
				ObjectMeta: metaObj("tmpl-denied-" + tc.name),
				Spec: vrouterv1.VRouterTemplateSpec{
					Config: tc.config,
				},
			}
			v := &VRouterTemplateCustomValidator{}
			_, err := v.ValidateCreate(context.Background(), tmpl)
			if err == nil {
				t.Fatalf("expected error for denied function %q, got nil", tc.name)
			}
			if !strings.Contains(err.Error(), "spec.config: invalid template syntax") {
				t.Fatalf("error = %q, want substring %q", err.Error(), "spec.config: invalid template syntax")
			}
		})
	}
}

func TestVRouterTemplateDefault_NoopDoesNotMutateOrError(t *testing.T) {
	tmpl := &vrouterv1.VRouterTemplate{
		ObjectMeta: metaObj("tmpl1"),
		Spec: vrouterv1.VRouterTemplateSpec{
			Config:   "interface {{ .IfaceName }} { }",
			Commands: "commit",
		},
	}
	want := tmpl.DeepCopy()

	d := &VRouterTemplateCustomDefaulter{}
	if err := d.Default(context.Background(), tmpl); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if tmpl.Spec.Config != want.Spec.Config || tmpl.Spec.Commands != want.Spec.Commands {
		t.Fatalf("Default mutated spec: got %+v, want %+v", tmpl.Spec, want.Spec)
	}
}
