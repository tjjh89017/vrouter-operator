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
	"testing"
)

func TestResolveNamespace(t *testing.T) {
	tests := []struct {
		name      string
		ref       NameRef
		defaultNS string
		want      string
	}{
		{
			name:      "uses ref namespace when set",
			ref:       NameRef{Namespace: "other-ns", Name: "foo"},
			defaultNS: "default",
			want:      "other-ns",
		},
		{
			name:      "falls back to default when ref namespace is empty",
			ref:       NameRef{Name: "foo"},
			defaultNS: "default",
			want:      "default",
		},
		{
			name:      "empty ref namespace with empty default",
			ref:       NameRef{Name: "foo"},
			defaultNS: "",
			want:      "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ResolveNamespace(tt.ref, tt.defaultNS)
			if got != tt.want {
				t.Errorf("ResolveNamespace() = %q, want %q", got, tt.want)
			}
		})
	}
}
