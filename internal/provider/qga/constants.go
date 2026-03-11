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

// Package qga defines shared QEMU Guest Agent command templates and constants
// used by all provider implementations.
package qga

// QGA JSON command templates. Dynamic fields are filled with fmt.Sprintf
// where placeholders (%q, %d) are present.
const (
	CmdPing = `{"execute":"guest-ping"}`

	CmdFileOpen  = `{"execute":"guest-file-open","arguments":{"path":%q,"mode":"w"}}`
	CmdFileWrite = `{"execute":"guest-file-write","arguments":{"handle":%d,"buf-b64":%q}}`
	CmdFileClose = `{"execute":"guest-file-close","arguments":{"handle":%d}}`

	// CmdExecServiceSubState queries the SubState of a systemd service.
	// The command exits 0 and prints the SubState string (e.g. "exited", "running").
	// Use this instead of "is-active" to distinguish active(exited) from active(running).
	CmdExecServiceSubState = `{"execute":"guest-exec","arguments":{"path":"/usr/bin/systemctl","arg":["show","-p","SubState","--value",%q],"capture-output":true}}`
	// CmdExecScript runs /bin/vbash with the given script path, capturing output.
	CmdExecScript = `{"execute":"guest-exec","arguments":{"path":"/bin/vbash","arg":[%q],"capture-output":true}}`

	CmdExecStatus = `{"execute":"guest-exec-status","arguments":{"pid":%d}}`
)

// VyOSService is the systemd service that must be active before config can be applied.
const VyOSService = "vyos-router.service"

// ScriptPath is the fixed path where the apply script is written on the router.
const ScriptPath = "/tmp/vrouter-apply.sh"
