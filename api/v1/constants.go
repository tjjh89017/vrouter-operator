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

const (
	FinalizerName = "vrouter.kojuro.date/finalizer"
	LabelBinding  = "vrouter.kojuro.date/binding"
	LabelTarget   = "vrouter.kojuro.date/target"
)

// VRouterConfig phase constants.
const (
	PhasePending  = "Pending"
	PhaseApplying = "Applying"
	PhaseApplied  = "Applied"
	PhaseFailed   = "Failed"
)

// ConditionApplied is the condition type used for kubectl wait support.
// Status=True when phase=Applied; Status=False when phase=Failed or Applying.
const ConditionApplied = "Applied"

// ConditionReady is the condition type for VRouterBinding.
// Status=True when all VRouterConfigs were reconciled successfully; Status=False on any reconcile error.
const ConditionReady = "Ready"
