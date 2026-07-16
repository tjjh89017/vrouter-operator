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

import (
	"context"
	"testing"
	"time"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	vrouterv1 "github.com/tjjh89017/vrouter-operator/api/v1"
)

// These tests cover the issue #35 Phase 1 serial rollout walk for
// mode: FixedInterval in onChange/runFixedIntervalRollout. They use a fake
// client (as the existing TestOnChange_* tests in vrouterbinding_controller_test.go
// do) rather than envtest, so status.rollout.lastUpdateTime can be pushed
// backward directly through the status subresource to fast-forward the
// walk's time gates without sleeping.

// newRolloutFixture builds a fake client (with the VRouterBinding and
// VRouterConfig status subresources enabled -- the latter so tests can drive
// a generated VRouterConfig's Generation/Applied condition directly, the way
// the real apiserver + config controller would) and returns it along with
// the scheme the reconciler needs for SetControllerReference.
func newRolloutFixture(t *testing.T, objs ...client.Object) (client.Client, *runtime.Scheme) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := vrouterv1.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to register scheme: %v", err)
	}
	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objs...).
		WithStatusSubresource(&vrouterv1.VRouterBinding{}, &vrouterv1.VRouterConfig{}).
		Build()
	return cl, scheme
}

func rolloutTarget(name string) *vrouterv1.VRouterTarget {
	return &vrouterv1.VRouterTarget{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: vrouterv1.VRouterTargetSpec{
			Provider: vrouterv1.ProviderConfig{
				Type:     vrouterv1.ProviderKubeVirt,
				KubeVirt: &vrouterv1.KubeVirtConfig{Name: name + "-vm"},
			},
		},
	}
}

// rolloutTemplate builds the shared "tmpl" VRouterTemplate used by every
// rollout test in this file.
func rolloutTemplate(config string) *vrouterv1.VRouterTemplate {
	return &vrouterv1.VRouterTemplate{
		ObjectMeta: metav1.ObjectMeta{Name: "tmpl", Namespace: "default"},
		Spec:       vrouterv1.VRouterTemplateSpec{Config: config},
	}
}

// rolloutBinding builds the shared "b" VRouterBinding, referencing "tmpl" and
// targetNames in order, used by every rollout test in this file.
func rolloutBinding(targetNames []string, rollout *vrouterv1.RolloutSpec) *vrouterv1.VRouterBinding {
	refs := make([]vrouterv1.NameRef, len(targetNames))
	for i, tn := range targetNames {
		refs[i] = vrouterv1.NameRef{Name: tn}
	}
	return &vrouterv1.VRouterBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "b", Namespace: "default"},
		Spec: vrouterv1.VRouterBindingSpec{
			TemplateRefs: []vrouterv1.NameRef{{Name: "tmpl"}},
			TargetRefs:   refs,
			Rollout:      rollout,
		},
	}
}

// getConfig fetches the generated VRouterConfig "b.<target>", returning
// (nil, false) if it does not exist.
func getConfig(t *testing.T, cl client.Client, targetName string) (*vrouterv1.VRouterConfig, bool) {
	t.Helper()
	var cfg vrouterv1.VRouterConfig
	err := cl.Get(context.Background(), types.NamespacedName{Name: "b." + targetName, Namespace: "default"}, &cfg)
	if errors.IsNotFound(err) {
		return nil, false
	}
	if err != nil {
		t.Fatalf("get VRouterConfig b.%s: %v", targetName, err)
	}
	return &cfg, true
}

func readyCondition(binding *vrouterv1.VRouterBinding) *metav1.Condition {
	return meta.FindStatusCondition(binding.Status.Conditions, vrouterv1.ConditionReady)
}

// backdateFrontier pushes binding.Status.Rollout.LastUpdateTime back by d and
// persists it through the status subresource, simulating that the frontier's
// wait interval has already elapsed -- the sanctioned way to fast-forward
// the rollout walk's time gates without sleeping.
func backdateFrontier(t *testing.T, cl client.Client, binding *vrouterv1.VRouterBinding, d time.Duration) {
	t.Helper()
	if binding.Status.Rollout == nil {
		t.Fatal("backdateFrontier: binding.Status.Rollout is nil")
	}
	binding.Status.Rollout.LastUpdateTime = metav1.NewTime(binding.Status.Rollout.LastUpdateTime.Add(-d))
	if err := cl.Status().Update(context.Background(), binding); err != nil {
		t.Fatalf("backdate frontier: %v", err)
	}
}

// markConfigAppliedForGeneration sets the generated VRouterConfig
// "b.<targetName>"'s Generation to currentGeneration and stamps an Applied
// condition True with the given appliedGeneration and transitionTime. The
// fake client (unlike a real apiserver) never increments Generation on a
// spec update, so tests drive it directly to model both the common case
// (appliedGeneration == currentGeneration: Applied for the current render)
// and the stale case (appliedGeneration < currentGeneration: a True Applied
// condition left over from before a re-render bumped the generation, which
// must NOT count as "Applied for the current generation").
func markConfigAppliedForGeneration(t *testing.T, cl client.Client, targetName string, currentGeneration, appliedGeneration int64, transitionTime time.Time) {
	t.Helper()
	ctx := context.Background()
	key := types.NamespacedName{Name: "b." + targetName, Namespace: "default"}

	var cfg vrouterv1.VRouterConfig
	if err := cl.Get(ctx, key, &cfg); err != nil {
		t.Fatalf("get VRouterConfig b.%s: %v", targetName, err)
	}
	cfg.Generation = currentGeneration
	if err := cl.Update(ctx, &cfg); err != nil {
		t.Fatalf("update VRouterConfig b.%s generation: %v", targetName, err)
	}

	if err := cl.Get(ctx, key, &cfg); err != nil {
		t.Fatalf("re-get VRouterConfig b.%s: %v", targetName, err)
	}
	cfg.Status.Phase = vrouterv1.PhaseApplied
	cfg.Status.ObservedGeneration = appliedGeneration
	cfg.Status.Conditions = []metav1.Condition{
		{
			Type:               vrouterv1.ConditionApplied,
			Status:             metav1.ConditionTrue,
			Reason:             "ConfigApplied",
			Message:            "Configuration applied successfully.",
			ObservedGeneration: appliedGeneration,
			LastTransitionTime: metav1.NewTime(transitionTime),
		},
	}
	if err := cl.Status().Update(ctx, &cfg); err != nil {
		t.Fatalf("update VRouterConfig b.%s status: %v", targetName, err)
	}
}

// markConfigApplied is the common-case convenience wrapper around
// markConfigAppliedForGeneration: the Applied condition matches the config's
// own (default zero) Generation, i.e. "Applied for the current generation".
func markConfigApplied(t *testing.T, cl client.Client, targetName string, transitionTime time.Time) {
	t.Helper()
	markConfigAppliedForGeneration(t, cl, targetName, 0, 0, transitionTime)
}

// TestOnChange_RolloutDisabled_WritesAllTargetsInOnePass pins that both an
// absent rollout and an explicit empty rollout ({}) preserve pre-#35
// behavior: every target's VRouterConfig is written in the same reconcile,
// with no frontier bookkeeping.
func TestOnChange_RolloutDisabled_WritesAllTargetsInOnePass(t *testing.T) {
	cases := []struct {
		name    string
		rollout *vrouterv1.RolloutSpec
	}{
		{name: "absent rollout (nil)", rollout: nil},
		{name: "empty rollout ({})", rollout: &vrouterv1.RolloutSpec{}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t0, t1, t2 := rolloutTarget("t0"), rolloutTarget("t1"), rolloutTarget("t2")
			tmpl := rolloutTemplate("cfg")
			binding := rolloutBinding([]string{"t0", "t1", "t2"}, tc.rollout)

			cl, scheme := newRolloutFixture(t, t0, t1, t2, tmpl, binding)
			r := &VRouterBindingReconciler{Client: cl, Scheme: scheme}

			result, err := r.onChange(context.Background(), reconcile.Request{}, binding)
			if err != nil {
				t.Fatalf("onChange returned error: %v", err)
			}
			if result.RequeueAfter != 0 {
				t.Errorf("result.RequeueAfter = %v, want 0 (Disabled mode does not requeue)", result.RequeueAfter)
			}

			for _, tn := range []string{"t0", "t1", "t2"} {
				if _, ok := getConfig(t, cl, tn); !ok {
					t.Errorf("VRouterConfig for target %q was not written", tn)
				}
			}

			if binding.Status.Rollout != nil {
				t.Errorf("Status.Rollout = %+v, want nil (Disabled mode must not stamp a frontier)", binding.Status.Rollout)
			}
			if cond := readyCondition(binding); cond == nil || cond.Status != metav1.ConditionTrue {
				t.Errorf("Ready condition = %+v, want status True", cond)
			}
		})
	}
}

// TestOnChange_FixedInterval_StaggersWritesAndCompletesAcrossReconciles is
// the primary Phase 1 walk test: it drives a fresh 3-target FixedInterval
// rollout end to end, pinning (a) only target[0] is written on the first
// reconcile, with the frontier stamped and Ready=False/RolloutInProgress in
// that same reconcile; (b) a reconcile before the interval elapses writes
// nothing; (c) backdating status.rollout.lastUpdateTime past the interval
// lets the next reconcile advance and write the next target; (d) once every
// target matches and the last frontier's wait has elapsed, Ready flips True.
func TestOnChange_FixedInterval_StaggersWritesAndCompletesAcrossReconciles(t *testing.T) {
	t0, t1, t2 := rolloutTarget("t0"), rolloutTarget("t1"), rolloutTarget("t2")
	tmpl := rolloutTemplate("cfg")
	waitInterval := time.Hour
	binding := rolloutBinding([]string{"t0", "t1", "t2"}, &vrouterv1.RolloutSpec{
		Mode:         vrouterv1.RolloutModeFixedInterval,
		WaitInterval: metav1.Duration{Duration: waitInterval},
	})

	cl, scheme := newRolloutFixture(t, t0, t1, t2, tmpl, binding)
	r := &VRouterBindingReconciler{Client: cl, Scheme: scheme}
	ctx := context.Background()

	t.Run("first reconcile writes only t0 and stamps frontier+Ready together", func(t *testing.T) {
		assertFixedIntervalFirstReconcile(t, ctx, r, cl, binding, waitInterval)
	})
	t.Run("reconcile before interval elapses writes nothing", func(t *testing.T) {
		assertFixedIntervalNoOpBeforeInterval(t, ctx, r, cl, binding, waitInterval)
	})
	t.Run("interval elapsed advances to t1", func(t *testing.T) {
		assertFixedIntervalAdvanceToT1(t, ctx, r, cl, binding, waitInterval)
	})
	t.Run("interval elapsed again advances to t2", func(t *testing.T) {
		assertFixedIntervalAdvanceToT2(t, ctx, r, cl, binding, waitInterval)
	})
	t.Run("final frontier elapsed completes the rollout", func(t *testing.T) {
		assertFixedIntervalCompletes(t, ctx, r, cl, binding, waitInterval)
	})
}

func assertFixedIntervalFirstReconcile(t *testing.T, ctx context.Context, r *VRouterBindingReconciler, cl client.Client, binding *vrouterv1.VRouterBinding, waitInterval time.Duration) {
	t.Helper()
	result, err := r.onChange(ctx, reconcile.Request{}, binding)
	if err != nil {
		t.Fatalf("onChange returned error: %v", err)
	}
	if result.RequeueAfter != waitInterval {
		t.Errorf("RequeueAfter = %v, want %v", result.RequeueAfter, waitInterval)
	}
	if _, ok := getConfig(t, cl, "t0"); !ok {
		t.Fatal("VRouterConfig for t0 was not written")
	}
	if _, ok := getConfig(t, cl, "t1"); ok {
		t.Error("VRouterConfig for t1 was written, want only t0 written")
	}
	if _, ok := getConfig(t, cl, "t2"); ok {
		t.Error("VRouterConfig for t2 was written, want only t0 written")
	}
	if binding.Status.Rollout == nil || binding.Status.Rollout.LastUpdatedTarget != "t0" {
		t.Fatalf("Status.Rollout = %+v, want LastUpdatedTarget=t0", binding.Status.Rollout)
	}
	if time.Since(binding.Status.Rollout.LastUpdateTime.Time) > time.Minute {
		t.Errorf("LastUpdateTime = %v, want close to now", binding.Status.Rollout.LastUpdateTime.Time)
	}
	if cond := readyCondition(binding); cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != ReasonRolloutInProgress || cond.Message != "1/3 targets updated" {
		t.Errorf("Ready condition = %+v, want False/RolloutInProgress/\"1/3 targets updated\"", cond)
	}
}

func assertFixedIntervalNoOpBeforeInterval(t *testing.T, ctx context.Context, r *VRouterBindingReconciler, cl client.Client, binding *vrouterv1.VRouterBinding, waitInterval time.Duration) {
	t.Helper()
	result, err := r.onChange(ctx, reconcile.Request{}, binding)
	if err != nil {
		t.Fatalf("onChange returned error: %v", err)
	}
	if result.RequeueAfter <= 0 || result.RequeueAfter > waitInterval {
		t.Errorf("RequeueAfter = %v, want in (0, %v]", result.RequeueAfter, waitInterval)
	}
	if _, ok := getConfig(t, cl, "t1"); ok {
		t.Error("VRouterConfig for t1 was written before the interval elapsed")
	}
	if binding.Status.Rollout.LastUpdatedTarget != "t0" {
		t.Errorf("frontier advanced to %q, want it to stay t0", binding.Status.Rollout.LastUpdatedTarget)
	}
}

func assertFixedIntervalAdvanceToT1(t *testing.T, ctx context.Context, r *VRouterBindingReconciler, cl client.Client, binding *vrouterv1.VRouterBinding, waitInterval time.Duration) {
	t.Helper()
	backdateFrontier(t, cl, binding, 2*waitInterval)
	result, err := r.onChange(ctx, reconcile.Request{}, binding)
	if err != nil {
		t.Fatalf("onChange returned error: %v", err)
	}
	if result.RequeueAfter != waitInterval {
		t.Errorf("RequeueAfter = %v, want %v", result.RequeueAfter, waitInterval)
	}
	if _, ok := getConfig(t, cl, "t1"); !ok {
		t.Fatal("VRouterConfig for t1 was not written after the interval elapsed")
	}
	if _, ok := getConfig(t, cl, "t2"); ok {
		t.Error("VRouterConfig for t2 was written, want only up to t1")
	}
	if binding.Status.Rollout.LastUpdatedTarget != "t1" {
		t.Fatalf("frontier = %q, want t1", binding.Status.Rollout.LastUpdatedTarget)
	}
	if cond := readyCondition(binding); cond == nil || cond.Message != "2/3 targets updated" {
		t.Errorf("Ready condition message = %q, want \"2/3 targets updated\"", cond.Message)
	}
}

func assertFixedIntervalAdvanceToT2(t *testing.T, ctx context.Context, r *VRouterBindingReconciler, cl client.Client, binding *vrouterv1.VRouterBinding, waitInterval time.Duration) {
	t.Helper()
	backdateFrontier(t, cl, binding, 2*waitInterval)
	if _, err := r.onChange(ctx, reconcile.Request{}, binding); err != nil {
		t.Fatalf("onChange returned error: %v", err)
	}
	if _, ok := getConfig(t, cl, "t2"); !ok {
		t.Fatal("VRouterConfig for t2 was not written after the interval elapsed")
	}
	if binding.Status.Rollout.LastUpdatedTarget != "t2" {
		t.Fatalf("frontier = %q, want t2", binding.Status.Rollout.LastUpdatedTarget)
	}
	if cond := readyCondition(binding); cond == nil || cond.Status != metav1.ConditionFalse {
		t.Errorf("Ready condition = %+v, want still False (t2's own wait has not elapsed)", cond)
	}
}

func assertFixedIntervalCompletes(t *testing.T, ctx context.Context, r *VRouterBindingReconciler, cl client.Client, binding *vrouterv1.VRouterBinding, waitInterval time.Duration) {
	t.Helper()
	backdateFrontier(t, cl, binding, 2*waitInterval)
	result, err := r.onChange(ctx, reconcile.Request{}, binding)
	if err != nil {
		t.Fatalf("onChange returned error: %v", err)
	}
	if result.RequeueAfter != 0 {
		t.Errorf("RequeueAfter = %v, want 0 (rollout complete)", result.RequeueAfter)
	}
	if cond := readyCondition(binding); cond == nil || cond.Status != metav1.ConditionTrue {
		t.Errorf("Ready condition = %+v, want status True (rollout complete)", cond)
	}
	if binding.Status.Rollout == nil || binding.Status.Rollout.LastUpdatedTarget != "t2" {
		t.Errorf("Status.Rollout = %+v, want the stale t2 frontier left in place", binding.Status.Rollout)
	}
}

// TestOnChange_FixedInterval_UniversalWriteGate_BlocksReRenderMidRollout
// pins the universal write gate's core purpose: a re-render that produces a
// new first-mismatch target which is NOT the current frontier must not be
// written until the frontier's own interval has elapsed, even though that
// new first-mismatch target was never touched by the rollout before.
func TestOnChange_FixedInterval_UniversalWriteGate_BlocksReRenderMidRollout(t *testing.T) {
	t0, t1, t2 := rolloutTarget("t0"), rolloutTarget("t1"), rolloutTarget("t2")
	tmpl := rolloutTemplate("A")
	waitInterval := time.Hour
	binding := rolloutBinding([]string{"t0", "t1", "t2"}, &vrouterv1.RolloutSpec{
		Mode:         vrouterv1.RolloutModeFixedInterval,
		WaitInterval: metav1.Duration{Duration: waitInterval},
	})

	cl, scheme := newRolloutFixture(t, t0, t1, t2, tmpl, binding)
	r := &VRouterBindingReconciler{Client: cl, Scheme: scheme}
	ctx := context.Background()

	// Advance the rollout to frontier=t1 (t0 already written and passed).
	if _, err := r.onChange(ctx, reconcile.Request{}, binding); err != nil {
		t.Fatalf("reconcile 1: %v", err)
	}
	backdateFrontier(t, cl, binding, 2*waitInterval)
	if _, err := r.onChange(ctx, reconcile.Request{}, binding); err != nil {
		t.Fatalf("reconcile 2: %v", err)
	}
	if binding.Status.Rollout.LastUpdatedTarget != "t1" {
		t.Fatalf("setup: frontier = %q, want t1", binding.Status.Rollout.LastUpdatedTarget)
	}
	t0Before, ok := getConfig(t, cl, "t0")
	if !ok {
		t.Fatal("setup: t0 config missing")
	}

	// Re-render: change the template so every target's desired spec changes.
	// The walk order is t0, t1, t2 -- t0 (already written, spec "A") becomes
	// the new first mismatch against desired spec "B", but t0 is NOT the
	// frontier (t1 is). The universal write gate must block this write until
	// t1's interval elapses.
	tmpl.Spec.Config = "B"
	if err := cl.Update(ctx, tmpl); err != nil {
		t.Fatalf("update template: %v", err)
	}

	result, err := r.onChange(ctx, reconcile.Request{}, binding)
	if err != nil {
		t.Fatalf("reconcile 3: onChange returned error: %v", err)
	}
	if result.RequeueAfter <= 0 {
		t.Errorf("reconcile 3: RequeueAfter = %v, want > 0 (gated on frontier t1)", result.RequeueAfter)
	}
	t0After, ok := getConfig(t, cl, "t0")
	if !ok {
		t.Fatal("reconcile 3: t0 config disappeared")
	}
	if t0After.Spec.Config != t0Before.Spec.Config {
		t.Errorf("reconcile 3: t0 config changed to %q, want it untouched until the gate passes", t0After.Spec.Config)
	}
	if binding.Status.Rollout.LastUpdatedTarget != "t1" {
		t.Errorf("reconcile 3: frontier = %q, want it to stay t1 (no write happened)", binding.Status.Rollout.LastUpdatedTarget)
	}

	// Backdate frontier t1's stamp past the interval: the gate now passes and
	// t0 (the first mismatch) is written directly, becoming the new frontier.
	backdateFrontier(t, cl, binding, 2*waitInterval)
	if _, err := r.onChange(ctx, reconcile.Request{}, binding); err != nil {
		t.Fatalf("reconcile 4: onChange returned error: %v", err)
	}
	t0Final, ok := getConfig(t, cl, "t0")
	if !ok {
		t.Fatal("reconcile 4: t0 config missing")
	}
	if t0Final.Spec.Config != "B" {
		t.Errorf("reconcile 4: t0 config = %q, want the new render \"B\" once the gate passed", t0Final.Spec.Config)
	}
	if binding.Status.Rollout.LastUpdatedTarget != "t0" {
		t.Errorf("reconcile 4: frontier = %q, want t0 (the new first mismatch)", binding.Status.Rollout.LastUpdatedTarget)
	}
}

// TestOnChange_FixedInterval_FrontierIsFirstMismatch_OverwrittenWithoutWaiting
// pins the "first mismatch IS the frontier itself" edge case: when the
// render changes for the target that is still the in-flight frontier, the
// universal write gate must pass immediately (no wait), even though almost
// no time has passed since the frontier was stamped.
func TestOnChange_FixedInterval_FrontierIsFirstMismatch_OverwrittenWithoutWaiting(t *testing.T) {
	t0, t1 := rolloutTarget("t0"), rolloutTarget("t1")
	tmpl := rolloutTemplate("cfg")
	waitInterval := time.Hour
	binding := rolloutBinding([]string{"t0", "t1"}, &vrouterv1.RolloutSpec{
		Mode:         vrouterv1.RolloutModeFixedInterval,
		WaitInterval: metav1.Duration{Duration: waitInterval},
	})

	cl, scheme := newRolloutFixture(t, t0, t1, tmpl, binding)
	r := &VRouterBindingReconciler{Client: cl, Scheme: scheme}
	ctx := context.Background()

	if _, err := r.onChange(ctx, reconcile.Request{}, binding); err != nil {
		t.Fatalf("reconcile 1: %v", err)
	}
	if binding.Status.Rollout.LastUpdatedTarget != "t0" {
		t.Fatalf("setup: frontier = %q, want t0", binding.Status.Rollout.LastUpdatedTarget)
	}
	stampBefore := binding.Status.Rollout.LastUpdateTime

	// Change t0's own render input (not the shared template) so only t0's
	// desired spec changes, without backdating the frontier at all.
	t0.Spec.Params = apiextensionsv1.JSON{Raw: []byte(`{"x":"y"}`)}
	if err := cl.Update(ctx, t0); err != nil {
		t.Fatalf("update t0: %v", err)
	}
	tmpl.Spec.Config = "cfg {{ .x }}"
	if err := cl.Update(ctx, tmpl); err != nil {
		t.Fatalf("update template: %v", err)
	}

	result, err := r.onChange(ctx, reconcile.Request{}, binding)
	if err != nil {
		t.Fatalf("reconcile 2: onChange returned error: %v", err)
	}
	// The write itself happened (RequeueAfter == waitInterval, the normal
	// post-write requeue), proving the gate did not block despite the
	// frontier having just been stamped moments ago.
	if result.RequeueAfter != waitInterval {
		t.Errorf("reconcile 2: RequeueAfter = %v, want %v (write proceeded without waiting)", result.RequeueAfter, waitInterval)
	}
	cfg, ok := getConfig(t, cl, "t0")
	if !ok {
		t.Fatal("reconcile 2: t0 config missing")
	}
	if cfg.Spec.Config != "cfg y" {
		t.Errorf("reconcile 2: t0 config = %q, want the new render \"cfg y\" written immediately", cfg.Spec.Config)
	}
	if binding.Status.Rollout.LastUpdatedTarget != "t0" {
		t.Errorf("reconcile 2: frontier = %q, want it to stay t0", binding.Status.Rollout.LastUpdatedTarget)
	}
	if binding.Status.Rollout.LastUpdateTime.Time.Before(stampBefore.Time) {
		t.Errorf("reconcile 2: LastUpdateTime = %v, want it refreshed to at least %v", binding.Status.Rollout.LastUpdateTime.Time, stampBefore.Time)
	}
}

// TestOnChange_FixedInterval_FrontierEvictedFromTargetRefs_TimeDegradedGate
// pins the frontier-eviction edge case: when the frontier target is removed
// from targetRefs mid-rollout, its orphan VRouterConfig is cleaned up as
// usual, and the universal write gate degrades to a pure time-based wait
// (measured from the persisted stamp) rather than trying to health-gate on
// an object that may no longer exist.
func TestOnChange_FixedInterval_FrontierEvictedFromTargetRefs_TimeDegradedGate(t *testing.T) {
	t0, t1, t2 := rolloutTarget("t0"), rolloutTarget("t1"), rolloutTarget("t2")
	tmpl := rolloutTemplate("cfg")
	waitInterval := time.Hour
	binding := rolloutBinding([]string{"t0", "t1", "t2"}, &vrouterv1.RolloutSpec{
		Mode:         vrouterv1.RolloutModeFixedInterval,
		WaitInterval: metav1.Duration{Duration: waitInterval},
	})

	cl, scheme := newRolloutFixture(t, t0, t1, t2, tmpl, binding)
	r := &VRouterBindingReconciler{Client: cl, Scheme: scheme}
	ctx := context.Background()

	if _, err := r.onChange(ctx, reconcile.Request{}, binding); err != nil {
		t.Fatalf("reconcile 1: %v", err)
	}
	if binding.Status.Rollout.LastUpdatedTarget != "t0" {
		t.Fatalf("setup: frontier = %q, want t0", binding.Status.Rollout.LastUpdatedTarget)
	}

	// Remove t0 from targetRefs mid-rollout. This must be persisted via a
	// regular (non-status) update: backdateFrontier below goes through the
	// status subresource, which (like the real apiserver) leaves spec alone
	// on the object it hands back -- an in-memory-only spec mutation here
	// would otherwise appear to "revert" once the status subresource update
	// refreshes the rest of the object from the store.
	binding.Spec.TargetRefs = []vrouterv1.NameRef{{Name: "t1"}, {Name: "t2"}}
	if err := cl.Update(ctx, binding); err != nil {
		t.Fatalf("persist targetRefs eviction: %v", err)
	}

	result, err := r.onChange(ctx, reconcile.Request{}, binding)
	if err != nil {
		t.Fatalf("reconcile 2: onChange returned error: %v", err)
	}
	if _, ok := getConfig(t, cl, "t0"); ok {
		t.Error("reconcile 2: orphan VRouterConfig for evicted t0 was not cleaned up")
	}
	if result.RequeueAfter <= 0 {
		t.Errorf("reconcile 2: RequeueAfter = %v, want > 0 (time-degraded gate on evicted frontier)", result.RequeueAfter)
	}
	if _, ok := getConfig(t, cl, "t1"); ok {
		t.Error("reconcile 2: t1 config was written despite the time-degraded gate not having elapsed")
	}
	if binding.Status.Rollout.LastUpdatedTarget != "t0" {
		t.Errorf("reconcile 2: frontier = %q, want it to stay t0 (record kept even though the target/object is gone)", binding.Status.Rollout.LastUpdatedTarget)
	}

	// Backdate the (evicted) frontier's stamp past the interval: the
	// time-degraded gate now passes and t1 is written.
	backdateFrontier(t, cl, binding, 2*waitInterval)
	if _, err := r.onChange(ctx, reconcile.Request{}, binding); err != nil {
		t.Fatalf("reconcile 3: onChange returned error: %v", err)
	}
	if _, ok := getConfig(t, cl, "t1"); !ok {
		t.Fatal("reconcile 3: t1 config was not written after the evicted frontier's interval elapsed")
	}
	if binding.Status.Rollout.LastUpdatedTarget != "t1" {
		t.Errorf("reconcile 3: frontier = %q, want t1", binding.Status.Rollout.LastUpdatedTarget)
	}
}

// TestOnChange_FixedInterval_CrashBetweenStampAndWrite_RetriesSameTarget
// pins the write-ahead stamping guarantee: the frontier is stamped in
// status.rollout BEFORE the generated VRouterConfig is written, so a crash
// between the two (simulated here by pre-stamping the frontier without
// creating the config) leaves the config still mismatched. The next
// reconcile must find that same target as the first mismatch, see that it
// IS the (already-stamped) frontier, pass the gate immediately, and write
// the config -- proving the rollout resumes correctly from that crash
// window instead of getting stuck or skipping the target.
func TestOnChange_FixedInterval_CrashBetweenStampAndWrite_RetriesSameTarget(t *testing.T) {
	t0, t1 := rolloutTarget("t0"), rolloutTarget("t1")
	tmpl := rolloutTemplate("cfg")
	waitInterval := time.Hour
	binding := rolloutBinding([]string{"t0", "t1"}, &vrouterv1.RolloutSpec{
		Mode:         vrouterv1.RolloutModeFixedInterval,
		WaitInterval: metav1.Duration{Duration: waitInterval},
	})
	// Simulate a crash immediately after the write-ahead stamp landed but
	// before the config write: the frontier already names t0, yet t0's
	// generated VRouterConfig does not exist.
	binding.Status.Rollout = &vrouterv1.RolloutStatus{
		LastUpdatedTarget: "t0",
		LastUpdateTime:    metav1.Now(),
	}

	cl, scheme := newRolloutFixture(t, t0, t1, tmpl, binding)
	r := &VRouterBindingReconciler{Client: cl, Scheme: scheme}
	ctx := context.Background()

	if _, ok := getConfig(t, cl, "t0"); ok {
		t.Fatal("setup: t0 config should not exist yet")
	}

	result, err := r.onChange(ctx, reconcile.Request{}, binding)
	if err != nil {
		t.Fatalf("onChange returned error: %v", err)
	}
	if result.RequeueAfter != waitInterval {
		t.Errorf("RequeueAfter = %v, want %v (write proceeded without waiting, since t0 is its own frontier)", result.RequeueAfter, waitInterval)
	}
	if _, ok := getConfig(t, cl, "t0"); !ok {
		t.Fatal("t0 config was not written on retry after the simulated crash")
	}
	if _, ok := getConfig(t, cl, "t1"); ok {
		t.Error("t1 config was written, want only t0 (still the first mismatch)")
	}
	if binding.Status.Rollout.LastUpdatedTarget != "t0" {
		t.Errorf("frontier = %q, want it to stay t0", binding.Status.Rollout.LastUpdatedTarget)
	}
}

// These tests cover the issue #35 Phase 2 WaitForApplied health gate:
// frontierRemaining/gateRemaining's WaitForApplied branches, layered on the
// Phase 1 walk (runRollout) which is otherwise unchanged and shared between
// both modes. The all-Failed halt and the three-state Ready/all-Applied
// completion check are explicitly out of scope for this slice (next items).

// TestOnChange_WaitForApplied_PollsUntilAppliedThenAdvances is the primary
// WaitForApplied walk test: it drives a fresh 2-target rollout end to end,
// pinning (a) only target[0] is written on the first reconcile, with the
// frontier stamped and Ready=False/RolloutInProgress in that same reconcile,
// requeuing at pollInterval; (b) a reconcile while target[0]'s config is not
// yet Applied for the current generation writes nothing and requeues at
// exactly pollInterval again; (c) once target[0] reaches Applied (with
// waitAfterApplied=0, no soak to wait out), the next reconcile advances and
// writes target[1].
func TestOnChange_WaitForApplied_PollsUntilAppliedThenAdvances(t *testing.T) {
	t0, t1 := rolloutTarget("t0"), rolloutTarget("t1")
	tmpl := rolloutTemplate("cfg")
	pollInterval := 10 * time.Second
	binding := rolloutBinding([]string{"t0", "t1"}, &vrouterv1.RolloutSpec{
		Mode:         vrouterv1.RolloutModeWaitForApplied,
		PollInterval: metav1.Duration{Duration: pollInterval},
		// WaitAfterApplied left zero: no soak, advance as soon as Applied.
	})

	cl, scheme := newRolloutFixture(t, t0, t1, tmpl, binding)
	r := &VRouterBindingReconciler{Client: cl, Scheme: scheme}
	ctx := context.Background()

	// First reconcile: writes only t0, stamps the frontier, Ready=False.
	result, err := r.onChange(ctx, reconcile.Request{}, binding)
	if err != nil {
		t.Fatalf("reconcile 1: onChange returned error: %v", err)
	}
	if result.RequeueAfter != pollInterval {
		t.Errorf("reconcile 1: RequeueAfter = %v, want %v", result.RequeueAfter, pollInterval)
	}
	if _, ok := getConfig(t, cl, "t0"); !ok {
		t.Fatal("reconcile 1: VRouterConfig for t0 was not written")
	}
	if _, ok := getConfig(t, cl, "t1"); ok {
		t.Error("reconcile 1: VRouterConfig for t1 was written, want only t0 written")
	}
	if binding.Status.Rollout == nil || binding.Status.Rollout.LastUpdatedTarget != "t0" {
		t.Fatalf("reconcile 1: Status.Rollout = %+v, want LastUpdatedTarget=t0", binding.Status.Rollout)
	}
	if cond := readyCondition(binding); cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != ReasonRolloutInProgress || cond.Message != "1/2 targets updated" {
		t.Errorf("reconcile 1: Ready condition = %+v, want False/RolloutInProgress/\"1/2 targets updated\"", cond)
	}

	// Second reconcile: t0's config is not yet Applied. Must write nothing
	// and requeue at exactly pollInterval (a fixed duration, not derived from
	// elapsed wall time, so this is safe to assert exactly).
	result, err = r.onChange(ctx, reconcile.Request{}, binding)
	if err != nil {
		t.Fatalf("reconcile 2: onChange returned error: %v", err)
	}
	if result.RequeueAfter != pollInterval {
		t.Errorf("reconcile 2: RequeueAfter = %v, want %v (time-based Applied poll)", result.RequeueAfter, pollInterval)
	}
	if _, ok := getConfig(t, cl, "t1"); ok {
		t.Error("reconcile 2: VRouterConfig for t1 was written before t0 reached Applied")
	}
	if binding.Status.Rollout.LastUpdatedTarget != "t0" {
		t.Errorf("reconcile 2: frontier = %q, want it to stay t0", binding.Status.Rollout.LastUpdatedTarget)
	}

	// Mark t0 Applied for its current (zero) generation. With
	// waitAfterApplied=0 the next reconcile must advance past it immediately
	// and write t1.
	markConfigApplied(t, cl, "t0", time.Now())

	result, err = r.onChange(ctx, reconcile.Request{}, binding)
	if err != nil {
		t.Fatalf("reconcile 3: onChange returned error: %v", err)
	}
	if result.RequeueAfter != pollInterval {
		t.Errorf("reconcile 3: RequeueAfter = %v, want %v", result.RequeueAfter, pollInterval)
	}
	if _, ok := getConfig(t, cl, "t1"); !ok {
		t.Fatal("reconcile 3: VRouterConfig for t1 was not written after t0 reached Applied")
	}
	if binding.Status.Rollout.LastUpdatedTarget != "t1" {
		t.Fatalf("reconcile 3: frontier = %q, want t1", binding.Status.Rollout.LastUpdatedTarget)
	}
	if cond := readyCondition(binding); cond == nil || cond.Message != "2/2 targets updated" {
		t.Errorf("reconcile 3: Ready condition message = %q, want \"2/2 targets updated\"", cond.Message)
	}
}

// TestOnChange_WaitForApplied_SoaksAfterAppliedBeforeAdvancing pins the
// waitAfterApplied soak: once the frontier's config reaches Applied, the walk
// must not advance past it until waitAfterApplied has elapsed since the
// Applied condition's own lastTransitionTime -- not just requeue at
// pollInterval indefinitely, and not advance immediately either.
func TestOnChange_WaitForApplied_SoaksAfterAppliedBeforeAdvancing(t *testing.T) {
	t0, t1 := rolloutTarget("t0"), rolloutTarget("t1")
	tmpl := rolloutTemplate("cfg")
	pollInterval := 10 * time.Second
	waitAfterApplied := time.Hour
	binding := rolloutBinding([]string{"t0", "t1"}, &vrouterv1.RolloutSpec{
		Mode:             vrouterv1.RolloutModeWaitForApplied,
		PollInterval:     metav1.Duration{Duration: pollInterval},
		WaitAfterApplied: metav1.Duration{Duration: waitAfterApplied},
	})

	cl, scheme := newRolloutFixture(t, t0, t1, tmpl, binding)
	r := &VRouterBindingReconciler{Client: cl, Scheme: scheme}
	ctx := context.Background()

	if _, err := r.onChange(ctx, reconcile.Request{}, binding); err != nil {
		t.Fatalf("reconcile 1: %v", err)
	}
	if binding.Status.Rollout.LastUpdatedTarget != "t0" {
		t.Fatalf("setup: frontier = %q, want t0", binding.Status.Rollout.LastUpdatedTarget)
	}

	// t0 just reached Applied (lastTransitionTime ~= now): the soak has
	// barely started, so t1 must not be written yet, and the requeue must
	// reflect the (nearly full) remaining soak -- not pollInterval.
	markConfigApplied(t, cl, "t0", time.Now())

	result, err := r.onChange(ctx, reconcile.Request{}, binding)
	if err != nil {
		t.Fatalf("reconcile 2: onChange returned error: %v", err)
	}
	if result.RequeueAfter <= 0 || result.RequeueAfter > waitAfterApplied {
		t.Errorf("reconcile 2: RequeueAfter = %v, want in (0, %v] (soak remaining)", result.RequeueAfter, waitAfterApplied)
	}
	if result.RequeueAfter < waitAfterApplied-5*time.Second {
		t.Errorf("reconcile 2: RequeueAfter = %v, want close to the full %v soak (not pollInterval=%v)", result.RequeueAfter, waitAfterApplied, pollInterval)
	}
	if _, ok := getConfig(t, cl, "t1"); ok {
		t.Error("reconcile 2: VRouterConfig for t1 was written before the soak elapsed")
	}
	if binding.Status.Rollout.LastUpdatedTarget != "t0" {
		t.Errorf("reconcile 2: frontier = %q, want it to stay t0", binding.Status.Rollout.LastUpdatedTarget)
	}

	// Fast-forward the soak by backdating the Applied condition's
	// lastTransitionTime well past waitAfterApplied.
	markConfigApplied(t, cl, "t0", time.Now().Add(-2*waitAfterApplied))

	if _, err := r.onChange(ctx, reconcile.Request{}, binding); err != nil {
		t.Fatalf("reconcile 3: onChange returned error: %v", err)
	}
	if _, ok := getConfig(t, cl, "t1"); !ok {
		t.Fatal("reconcile 3: VRouterConfig for t1 was not written after the soak elapsed")
	}
	if binding.Status.Rollout.LastUpdatedTarget != "t1" {
		t.Errorf("reconcile 3: frontier = %q, want t1", binding.Status.Rollout.LastUpdatedTarget)
	}
}

// TestOnChange_WaitForApplied_StaleGenerationAppliedDoesNotAdvance pins the
// generation-correctness of the Applied gate: an Applied=True condition left
// over from before a re-render bumped the config's generation must not count
// as "Applied for the current generation" -- the walk must keep polling
// rather than treating a stale success as current.
func TestOnChange_WaitForApplied_StaleGenerationAppliedDoesNotAdvance(t *testing.T) {
	t0, t1 := rolloutTarget("t0"), rolloutTarget("t1")
	tmpl := rolloutTemplate("cfg")
	pollInterval := 10 * time.Second
	binding := rolloutBinding([]string{"t0", "t1"}, &vrouterv1.RolloutSpec{
		Mode:         vrouterv1.RolloutModeWaitForApplied,
		PollInterval: metav1.Duration{Duration: pollInterval},
	})

	cl, scheme := newRolloutFixture(t, t0, t1, tmpl, binding)
	r := &VRouterBindingReconciler{Client: cl, Scheme: scheme}
	ctx := context.Background()

	if _, err := r.onChange(ctx, reconcile.Request{}, binding); err != nil {
		t.Fatalf("reconcile 1: %v", err)
	}
	if binding.Status.Rollout.LastUpdatedTarget != "t0" {
		t.Fatalf("setup: frontier = %q, want t0", binding.Status.Rollout.LastUpdatedTarget)
	}

	// t0's config is now at generation 2 (e.g. a later spec edit), but its
	// Applied condition is still True for the older generation 1 -- stale.
	markConfigAppliedForGeneration(t, cl, "t0", 2, 1, time.Now())

	result, err := r.onChange(ctx, reconcile.Request{}, binding)
	if err != nil {
		t.Fatalf("reconcile 2: onChange returned error: %v", err)
	}
	if result.RequeueAfter != pollInterval {
		t.Errorf("reconcile 2: RequeueAfter = %v, want %v (still polling, stale Applied must not satisfy the gate)", result.RequeueAfter, pollInterval)
	}
	if _, ok := getConfig(t, cl, "t1"); ok {
		t.Error("reconcile 2: VRouterConfig for t1 was written despite t0's Applied condition being stale (wrong generation)")
	}
	if binding.Status.Rollout.LastUpdatedTarget != "t0" {
		t.Errorf("reconcile 2: frontier = %q, want it to stay t0", binding.Status.Rollout.LastUpdatedTarget)
	}
}

// TestOnChange_WaitForApplied_UniversalWriteGate_BlocksReRenderMidRollout
// mirrors the FixedInterval universal-write-gate test for WaitForApplied: a
// re-render that produces a new first-mismatch target which is NOT the
// current frontier must not be written until the frontier's own Applied+soak
// gate is satisfied, even though that new first-mismatch target was never
// touched by the rollout before.
func TestOnChange_WaitForApplied_UniversalWriteGate_BlocksReRenderMidRollout(t *testing.T) {
	t0, t1, t2 := rolloutTarget("t0"), rolloutTarget("t1"), rolloutTarget("t2")
	tmpl := rolloutTemplate("A")
	pollInterval := 10 * time.Second
	binding := rolloutBinding([]string{"t0", "t1", "t2"}, &vrouterv1.RolloutSpec{
		Mode:         vrouterv1.RolloutModeWaitForApplied,
		PollInterval: metav1.Duration{Duration: pollInterval},
	})

	cl, scheme := newRolloutFixture(t, t0, t1, t2, tmpl, binding)
	r := &VRouterBindingReconciler{Client: cl, Scheme: scheme}
	ctx := context.Background()

	// Advance the rollout to frontier=t1 (t0 already written and applied).
	if _, err := r.onChange(ctx, reconcile.Request{}, binding); err != nil {
		t.Fatalf("reconcile 1: %v", err)
	}
	markConfigApplied(t, cl, "t0", time.Now())
	if _, err := r.onChange(ctx, reconcile.Request{}, binding); err != nil {
		t.Fatalf("reconcile 2: %v", err)
	}
	if binding.Status.Rollout.LastUpdatedTarget != "t1" {
		t.Fatalf("setup: frontier = %q, want t1", binding.Status.Rollout.LastUpdatedTarget)
	}
	t0Before, ok := getConfig(t, cl, "t0")
	if !ok {
		t.Fatal("setup: t0 config missing")
	}

	// Re-render: change the template so every target's desired spec changes.
	// t0 (already written, spec "A") becomes the new first mismatch against
	// desired spec "B", but t0 is NOT the frontier (t1 is, and t1 has not
	// reached Applied yet). The universal write gate must block this write.
	tmpl.Spec.Config = "B"
	if err := cl.Update(ctx, tmpl); err != nil {
		t.Fatalf("update template: %v", err)
	}

	result, err := r.onChange(ctx, reconcile.Request{}, binding)
	if err != nil {
		t.Fatalf("reconcile 3: onChange returned error: %v", err)
	}
	if result.RequeueAfter != pollInterval {
		t.Errorf("reconcile 3: RequeueAfter = %v, want %v (gated on frontier t1's Applied poll)", result.RequeueAfter, pollInterval)
	}
	t0After, ok := getConfig(t, cl, "t0")
	if !ok {
		t.Fatal("reconcile 3: t0 config disappeared")
	}
	if t0After.Spec.Config != t0Before.Spec.Config {
		t.Errorf("reconcile 3: t0 config changed to %q, want it untouched until the gate passes", t0After.Spec.Config)
	}
	if binding.Status.Rollout.LastUpdatedTarget != "t1" {
		t.Errorf("reconcile 3: frontier = %q, want it to stay t1 (no write happened)", binding.Status.Rollout.LastUpdatedTarget)
	}

	// Mark t1 Applied: the gate now passes and t0 (the first mismatch) is
	// written directly with the new render, becoming the new frontier.
	markConfigApplied(t, cl, "t1", time.Now())
	if _, err := r.onChange(ctx, reconcile.Request{}, binding); err != nil {
		t.Fatalf("reconcile 4: onChange returned error: %v", err)
	}
	t0Final, ok := getConfig(t, cl, "t0")
	if !ok {
		t.Fatal("reconcile 4: t0 config missing")
	}
	if t0Final.Spec.Config != "B" {
		t.Errorf("reconcile 4: t0 config = %q, want the new render \"B\" once the gate passed", t0Final.Spec.Config)
	}
	if binding.Status.Rollout.LastUpdatedTarget != "t0" {
		t.Errorf("reconcile 4: frontier = %q, want t0 (the new first mismatch)", binding.Status.Rollout.LastUpdatedTarget)
	}
}

// TestOnChange_WaitForApplied_FrontierEvictedFromTargetRefs_TimeDegradedGate
// pins the frontier-eviction edge case for WaitForApplied: when the frontier
// target is removed from targetRefs mid-rollout, the universal write gate
// degrades to a pure time-based wait of waitAfterApplied (not pollInterval,
// and not health-gated on an object that may no longer exist) measured from
// the persisted frontier stamp.
func TestOnChange_WaitForApplied_FrontierEvictedFromTargetRefs_TimeDegradedGate(t *testing.T) {
	t0, t1, t2 := rolloutTarget("t0"), rolloutTarget("t1"), rolloutTarget("t2")
	tmpl := rolloutTemplate("cfg")
	pollInterval := 10 * time.Second
	waitAfterApplied := time.Hour
	binding := rolloutBinding([]string{"t0", "t1", "t2"}, &vrouterv1.RolloutSpec{
		Mode:             vrouterv1.RolloutModeWaitForApplied,
		PollInterval:     metav1.Duration{Duration: pollInterval},
		WaitAfterApplied: metav1.Duration{Duration: waitAfterApplied},
	})

	cl, scheme := newRolloutFixture(t, t0, t1, t2, tmpl, binding)
	r := &VRouterBindingReconciler{Client: cl, Scheme: scheme}
	ctx := context.Background()

	if _, err := r.onChange(ctx, reconcile.Request{}, binding); err != nil {
		t.Fatalf("reconcile 1: %v", err)
	}
	if binding.Status.Rollout.LastUpdatedTarget != "t0" {
		t.Fatalf("setup: frontier = %q, want t0", binding.Status.Rollout.LastUpdatedTarget)
	}

	// Remove t0 from targetRefs mid-rollout, without ever marking it Applied.
	binding.Spec.TargetRefs = []vrouterv1.NameRef{{Name: "t1"}, {Name: "t2"}}
	if err := cl.Update(ctx, binding); err != nil {
		t.Fatalf("persist targetRefs eviction: %v", err)
	}

	result, err := r.onChange(ctx, reconcile.Request{}, binding)
	if err != nil {
		t.Fatalf("reconcile 2: onChange returned error: %v", err)
	}
	if _, ok := getConfig(t, cl, "t0"); ok {
		t.Error("reconcile 2: orphan VRouterConfig for evicted t0 was not cleaned up")
	}
	if result.RequeueAfter <= 0 || result.RequeueAfter > waitAfterApplied {
		t.Errorf("reconcile 2: RequeueAfter = %v, want in (0, %v] (time-degraded gate on evicted frontier)", result.RequeueAfter, waitAfterApplied)
	}
	if result.RequeueAfter < waitAfterApplied-5*time.Second {
		t.Errorf("reconcile 2: RequeueAfter = %v, want close to the full waitAfterApplied=%v, not pollInterval=%v (must not health-gate an evicted target)", result.RequeueAfter, waitAfterApplied, pollInterval)
	}
	if _, ok := getConfig(t, cl, "t1"); ok {
		t.Error("reconcile 2: t1 config was written despite the time-degraded gate not having elapsed")
	}
	if binding.Status.Rollout.LastUpdatedTarget != "t0" {
		t.Errorf("reconcile 2: frontier = %q, want it to stay t0 (record kept even though the target/object is gone)", binding.Status.Rollout.LastUpdatedTarget)
	}

	// Backdate the (evicted) frontier's stamp past waitAfterApplied: the
	// time-degraded gate now passes and t1 is written.
	backdateFrontier(t, cl, binding, 2*waitAfterApplied)
	if _, err := r.onChange(ctx, reconcile.Request{}, binding); err != nil {
		t.Fatalf("reconcile 3: onChange returned error: %v", err)
	}
	if _, ok := getConfig(t, cl, "t1"); !ok {
		t.Fatal("reconcile 3: t1 config was not written after the evicted frontier's wait elapsed")
	}
	if binding.Status.Rollout.LastUpdatedTarget != "t1" {
		t.Errorf("reconcile 3: frontier = %q, want t1", binding.Status.Rollout.LastUpdatedTarget)
	}
}
