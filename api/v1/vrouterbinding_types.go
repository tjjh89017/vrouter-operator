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
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// VRouterBindingSpec defines the desired state of VRouterBinding.
type VRouterBindingSpec struct {
	// Deprecated: use TemplateRefs instead. If non-nil, prepended to TemplateRefs as highest priority.
	// +optional
	TemplateRef *NameRef `json:"templateRef,omitempty"`
	// TemplateRefs is an ordered list of templates to merge. Templates are applied in order;
	// later templates' config and commands are appended after earlier ones.
	// +optional
	TemplateRefs []NameRef `json:"templateRefs,omitempty"`
	// ParamsRefs is an ordered list of VRouterParams to merge, same-namespace only.
	// Later entries override earlier ones; the merged result is applied below
	// (i.e. is overridden by) this binding's own Params.
	// +optional
	ParamsRefs []NameRef `json:"paramsRefs,omitempty"`
	TargetRefs []NameRef `json:"targetRefs"`
	// Save controls whether the applied configuration is persisted so it
	// survives a reboot. Defaults to true when unset.
	// +kubebuilder:default=true
	// +optional
	Save *bool `json:"save,omitempty"`
	// +kubebuilder:pruning:PreserveUnknownFields
	// +kubebuilder:validation:Schemaless
	// +optional
	Params apiextensionsv1.JSON `json:"params,omitempty"`
	// Rollout controls whether binding-driven VRouterConfig updates across
	// TargetRefs are staggered instead of all written in the same reconcile.
	// Absent (nil) is equivalent to RolloutModeDisabled.
	// +optional
	Rollout *RolloutSpec `json:"rollout,omitempty"`
}

// Rollout mode enum values for RolloutSpec.Mode.
const (
	// RolloutModeDisabled updates every target's VRouterConfig in the same
	// reconcile, matching pre-rollout behavior. This is the zero value: an
	// absent rollout, an empty rollout ({}), or an unset mode all mean Disabled.
	RolloutModeDisabled = "Disabled"
	// RolloutModeWaitForApplied updates one target at a time, waiting for the
	// previous target's generated VRouterConfig to reach Applied (plus an
	// optional soak period) before updating the next target. Halts on any
	// Failed generated VRouterConfig.
	RolloutModeWaitForApplied = "WaitForApplied"
	// RolloutModeFixedInterval updates one target at a time, waiting a fixed
	// interval after each update before updating the next target, ignoring
	// Applied/Failed status entirely.
	RolloutModeFixedInterval = "FixedInterval"
)

// RolloutSpec configures serial, per-target rollout of binding-driven
// VRouterConfig updates instead of updating every target in one reconcile.
type RolloutSpec struct {
	// Mode selects the rollout strategy. Disabled (the default) preserves
	// today's behavior of updating all targets in the same reconcile.
	// +kubebuilder:validation:Enum=Disabled;WaitForApplied;FixedInterval
	// +kubebuilder:default=Disabled
	// +optional
	Mode string `json:"mode,omitempty"`
	// PollInterval is the requeue interval used while waiting for a target's
	// generated VRouterConfig to reach Applied. Used by mode WaitForApplied.
	// +kubebuilder:default="10s"
	// +optional
	PollInterval metav1.Duration `json:"pollInterval,omitempty"`
	// WaitAfterApplied is an additional soak period to wait after a target's
	// generated VRouterConfig reaches Applied before updating the next
	// target. Used by mode WaitForApplied.
	// +kubebuilder:default="0s"
	// +optional
	WaitAfterApplied metav1.Duration `json:"waitAfterApplied,omitempty"`
	// WaitInterval is the fixed wait period after each target update before
	// updating the next target, regardless of Applied/Failed status. Used by
	// mode FixedInterval; required (> 0) when that mode is selected.
	// +optional
	WaitInterval metav1.Duration `json:"waitInterval,omitempty"`
}

// EffectiveMode returns the rollout mode to use, treating a nil RolloutSpec
// or an empty Mode field as RolloutModeDisabled.
func (r *RolloutSpec) EffectiveMode() string {
	if r == nil || r.Mode == "" {
		return RolloutModeDisabled
	}
	return r.Mode
}

// VRouterBindingStatus defines the observed state of VRouterBinding.
type VRouterBindingStatus struct {
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
	// Rollout records the serial rollout frontier: the last target whose
	// generated VRouterConfig was written by the rollout walk, and when.
	// Maintained by BindingController; only meaningful when spec.rollout.mode
	// is not Disabled.
	// +optional
	Rollout *RolloutStatus `json:"rollout,omitempty"`
}

// RolloutStatus records the serial rollout frontier.
type RolloutStatus struct {
	// LastUpdatedTarget is the name of the target whose generated
	// VRouterConfig was most recently written by the rollout walk.
	// +optional
	LastUpdatedTarget string `json:"lastUpdatedTarget,omitempty"`
	// LastUpdateTime is when LastUpdatedTarget's generated VRouterConfig was
	// most recently written by the rollout walk.
	// +optional
	LastUpdateTime metav1.Time `json:"lastUpdateTime,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName={vrb,vrouterbinding}

// VRouterBinding is the Schema for the vrouterbindings API.
type VRouterBinding struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   VRouterBindingSpec   `json:"spec,omitempty"`
	Status VRouterBindingStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// VRouterBindingList contains a list of VRouterBinding.
type VRouterBindingList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []VRouterBinding `json:"items"`
}

func init() {
	SchemeBuilder.Register(&VRouterBinding{}, &VRouterBindingList{})
}
