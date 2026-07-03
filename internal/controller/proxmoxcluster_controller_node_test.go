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

package controller

import "testing"

// TestDesiredProxmoxNode_VMIDPresent verifies that when the VMID is listed
// in the freshly fetched cluster resources, its node is returned.
func TestDesiredProxmoxNode_VMIDPresent(t *testing.T) {
	vmMap := map[int]vmInfo{
		100: {node: "pve1", uptime: 3600},
	}

	got := desiredProxmoxNode(vmMap, 100)

	if got != "pve1" {
		t.Fatalf("desiredProxmoxNode() = %q, want %q", got, "pve1")
	}
}

// TestDesiredProxmoxNode_VMIDAbsent verifies that when the VMID is absent
// from a successfully fetched cluster resource list (e.g. the VM was
// destroyed), the desired node is "" so a previously cached node gets
// cleared instead of left stale (SPEC.md §7.3 step 4a). This guards against
// the provider's cached-node fallback resolving to a now-wrong host after
// the VMID is reused on a different node.
func TestDesiredProxmoxNode_VMIDAbsent(t *testing.T) {
	vmMap := map[int]vmInfo{
		200: {node: "pve2", uptime: 100},
	}

	// VMID 100 is not in the list at all, simulating a destroyed VM whose
	// target still carries a stale node from a previous sync.
	got := desiredProxmoxNode(vmMap, 100)

	if got != "" {
		t.Fatalf("desiredProxmoxNode() = %q, want empty string when VMID is absent", got)
	}
}

// TestDesiredProxmoxNode_EmptyMap verifies the same clearing behavior when
// the resource list is empty (all VMs gone) rather than merely missing one
// entry.
func TestDesiredProxmoxNode_EmptyMap(t *testing.T) {
	vmMap := map[int]vmInfo{}

	got := desiredProxmoxNode(vmMap, 100)

	if got != "" {
		t.Fatalf("desiredProxmoxNode() = %q, want empty string for empty vmMap", got)
	}
}
