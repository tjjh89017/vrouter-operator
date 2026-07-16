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

// Package flavor abstracts guest-OS-flavor-specific behavior so that provider
// readiness/apply logic does not hardcode assumptions that differ between guest
// images. A flavor is identified by the /etc/os-release ID field; new guest
// OSes slot in as additional Flavor implementations registered in byID.
package flavor

import "fmt"

// Flavor answers guest-OS-flavor-specific questions. For now that is just the
// router systemd unit name; future flavor-specific behavior extends this
// interface (or is answered by the concrete implementations).
type Flavor interface {
	// RouterServiceUnit returns the systemd unit name of the router service
	// whose completion signals the guest is ready to accept config.
	RouterServiceUnit() string
}

// vyos is the flavor for VyOS guests (/etc/os-release ID=vyos).
type vyos struct{}

func (vyos) RouterServiceUnit() string { return "vyos-router.service" }

// dozenos is the flavor for DozenOS guests (/etc/os-release ID=dozenos).
type dozenos struct{}

func (dozenos) RouterServiceUnit() string { return "dozenos-router.service" }

// byID maps an /etc/os-release ID to its Flavor. Register new flavors here.
var byID = map[string]Flavor{
	"vyos":    vyos{},
	"dozenos": dozenos{},
}

// ByID resolves the Flavor for an /etc/os-release ID. It returns an error for
// an unknown ID rather than defaulting, so an unrecognized guest surfaces
// loudly instead of silently using the wrong unit name.
func ByID(id string) (Flavor, error) {
	f, ok := byID[id]
	if !ok {
		return nil, fmt.Errorf("unknown guest OS flavor ID %q", id)
	}
	return f, nil
}
