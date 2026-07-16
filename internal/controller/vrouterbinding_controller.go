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
	"fmt"
	"strings"
	"time"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	vrouterv1 "github.com/tjjh89017/vrouter-operator/api/v1"
	vrotemplate "github.com/tjjh89017/vrouter-operator/internal/template"
)

// ReasonRolloutInProgress is the Ready=False condition reason set while a
// rollout mode's serial walk (issue #35) is between the first write and
// completion.
const ReasonRolloutInProgress = "RolloutInProgress"

// ReasonRolloutHalted is the Ready=False condition reason set when
// mode: WaitForApplied halts because some generated VRouterConfig is Failed
// (see issue #35 "Error handling (WaitForApplied)").
const ReasonRolloutHalted = "RolloutHalted"

// VRouterBindingReconciler reconciles a VRouterBinding object.
type VRouterBindingReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	// Now returns the current time; overridable in tests to fast-forward the
	// rollout walk's time gates without sleeping. Defaults to time.Now.
	Now func() time.Time
}

// now returns r.Now() when set, else time.Now(). Rollout gating always reads
// time through this method so tests can inject a clock.
func (r *VRouterBindingReconciler) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// +kubebuilder:rbac:groups=vrouter.kojuro.date,resources=vrouterbindings,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=vrouter.kojuro.date,resources=vrouterbindings/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=vrouter.kojuro.date,resources=vrouterbindings/finalizers,verbs=update
// +kubebuilder:rbac:groups=vrouter.kojuro.date,resources=vroutertemplates,verbs=get;list;watch
// +kubebuilder:rbac:groups=vrouter.kojuro.date,resources=vroutertargets,verbs=get;list;watch
// +kubebuilder:rbac:groups=vrouter.kojuro.date,resources=vrouterparams,verbs=get;list;watch
// +kubebuilder:rbac:groups=vrouter.kojuro.date,resources=vrouterconfigs,verbs=get;list;watch;create;update;patch;delete

func (r *VRouterBindingReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var binding vrouterv1.VRouterBinding
	if err := r.Get(ctx, req.NamespacedName, &binding); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !binding.DeletionTimestamp.IsZero() {
		return r.onDelete(ctx, req, &binding)
	}

	if !controllerutil.ContainsFinalizer(&binding, vrouterv1.FinalizerName) {
		controllerutil.AddFinalizer(&binding, vrouterv1.FinalizerName)
		if err := r.Update(ctx, &binding); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	return r.onChange(ctx, req, &binding)
}

func (r *VRouterBindingReconciler) onDelete(ctx context.Context, _ ctrl.Request, binding *vrouterv1.VRouterBinding) (ctrl.Result, error) {
	controllerutil.RemoveFinalizer(binding, vrouterv1.FinalizerName)
	return ctrl.Result{}, r.Update(ctx, binding)
}

// setReadyCondition patches the Ready condition on the binding status. It
// returns the patch error (matching stampRolloutFrontier's convention)
// rather than swallowing it: a caller on a terminal path with no other
// scheduled requeue (a rollout-completion True, for instance) must propagate
// this error so controller-runtime retries with backoff -- otherwise a
// transient patch failure (409 conflict, apiserver hiccup) leaves Ready
// stuck False/RolloutInProgress forever despite the rollout having actually
// completed. Callers that already have a more specific error to return (the
// ReconcileError paths) should ignore this one rather than mask it; callers
// paired with an explicit RequeueAfter may also ignore it since the
// scheduled requeue re-attempts regardless.
func (r *VRouterBindingReconciler) setReadyCondition(ctx context.Context, binding *vrouterv1.VRouterBinding, status metav1.ConditionStatus, reason, message string) error {
	patch := client.MergeFrom(binding.DeepCopy())
	meta.SetStatusCondition(&binding.Status.Conditions, metav1.Condition{
		Type:               vrouterv1.ConditionReady,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: binding.Generation,
	})
	return r.Status().Patch(ctx, binding, patch)
}

// effectiveTemplateRefs returns the merged template ref list: templateRef (if set) prepended to templateRefs.
func effectiveTemplateRefs(binding *vrouterv1.VRouterBinding) []vrouterv1.NameRef {
	var refs []vrouterv1.NameRef
	if binding.Spec.TemplateRef != nil { //nolint:staticcheck // backward compat
		refs = append(refs, *binding.Spec.TemplateRef) //nolint:staticcheck // backward compat
	}
	refs = append(refs, binding.Spec.TemplateRefs...)
	return refs
}

// desiredConfig is the fully-rendered VRouterConfig for one target, produced
// by renderAll before any writes happen.
type desiredConfig struct {
	// Name is the generated VRouterConfig's name (<binding>.<target>).
	Name string
	// Target is the targetRef this config was rendered for.
	Target vrouterv1.NameRef
	// Spec is the VRouterConfig spec to write.
	Spec vrouterv1.VRouterConfigSpec
}

// renderAll resolves templates, paramsRefs, and targets, then renders one
// desired VRouterConfig per targetRef, in targetRefs order. It performs no
// writes; callers create/update VRouterConfigs from the returned slice.
func (r *VRouterBindingReconciler) renderAll(ctx context.Context, binding *vrouterv1.VRouterBinding) ([]desiredConfig, error) {
	// Step 1: fetch all templates in order.
	templateRefs := effectiveTemplateRefs(binding)
	templates := make([]vrouterv1.VRouterTemplate, 0, len(templateRefs))
	for _, ref := range templateRefs {
		var tmpl vrouterv1.VRouterTemplate
		ns := vrouterv1.ResolveNamespace(ref, binding.Namespace)
		if err := r.Get(ctx, client.ObjectKey{Name: ref.Name, Namespace: ns}, &tmpl); err != nil {
			return nil, fmt.Errorf("get template %q (namespace %q): %w", ref.Name, ns, err)
		}
		templates = append(templates, tmpl)
	}

	// Step 1b: fetch all VRouterParams in order (paramsRefs, lowest-priority
	// params layer; see MergeParamsLayers below).
	paramsRefsJSON := make([]apiextensionsv1.JSON, 0, len(binding.Spec.ParamsRefs))
	for _, ref := range binding.Spec.ParamsRefs {
		var params vrouterv1.VRouterParams
		ns := vrouterv1.ResolveNamespace(ref, binding.Namespace)
		if err := r.Get(ctx, client.ObjectKey{Name: ref.Name, Namespace: ns}, &params); err != nil {
			return nil, fmt.Errorf("get params %q (namespace %q): %w", ref.Name, ns, err)
		}
		paramsRefsJSON = append(paramsRefsJSON, params.Spec.Params)
	}

	// Step 2: resolve targets and render one VRouterConfig per target.
	desired := make([]desiredConfig, 0, len(binding.Spec.TargetRefs))
	for _, ref := range binding.Spec.TargetRefs {
		var target vrouterv1.VRouterTarget
		ns := vrouterv1.ResolveNamespace(ref, binding.Namespace)
		if err := r.Get(ctx, client.ObjectKey{Name: ref.Name, Namespace: ns}, &target); err != nil {
			return nil, fmt.Errorf("get target %q: %w", ref.Name, err)
		}

		// Step 3: merge params — paramsRefs (list order) overridden by
		// binding.Params, overridden by target.Params (highest priority).
		paramsLayers := make([]apiextensionsv1.JSON, 0, len(paramsRefsJSON)+2)
		paramsLayers = append(paramsLayers, paramsRefsJSON...)
		paramsLayers = append(paramsLayers, binding.Spec.Params, target.Spec.Params)
		params, err := vrotemplate.MergeParamsLayers(paramsLayers...)
		if err != nil {
			return nil, fmt.Errorf("merge params for target %q: %w", ref.Name, err)
		}

		// Step 4: render and concatenate config/commands from all templates in order.
		var configParts, commandParts []string
		for i, tmpl := range templates {
			if tmpl.Spec.Config != "" {
				rendered, err := vrotemplate.Render(tmpl.Spec.Config, params)
				if err != nil {
					return nil, fmt.Errorf("render config from template[%d] %q for target %q: %w", i, templateRefs[i].Name, ref.Name, err)
				}
				configParts = append(configParts, rendered)
			}
			if tmpl.Spec.Commands != "" {
				rendered, err := vrotemplate.Render(tmpl.Spec.Commands, params)
				if err != nil {
					return nil, fmt.Errorf("render commands from template[%d] %q for target %q: %w", i, templateRefs[i].Name, ref.Name, err)
				}
				commandParts = append(commandParts, rendered)
			}
		}

		desired = append(desired, desiredConfig{
			Name:   fmt.Sprintf("%s.%s", binding.Name, ref.Name),
			Target: ref,
			Spec: vrouterv1.VRouterConfigSpec{
				TargetRef: vrouterv1.NameRef{Name: ref.Name},
				Save:      binding.Spec.Save,
				Config:    strings.Join(configParts, "\n"),
				Commands:  strings.Join(commandParts, "\n"),
			},
		})
	}

	return desired, nil
}

// cleanupOrphans deletes VRouterConfigs owned by this binding that are no
// longer in the desired set. Deleting a VRouterConfig only removes the K8s
// object (its onDelete just drops the finalizer) and never triggers a commit
// on the router, so this is safe to run every reconcile, independent of
// rollout mode or progress.
func (r *VRouterBindingReconciler) cleanupOrphans(ctx context.Context, binding *vrouterv1.VRouterBinding, desired []desiredConfig) error {
	log := logf.FromContext(ctx)

	desiredNames := make(map[string]bool, len(desired))
	for _, d := range desired {
		desiredNames[d.Name] = true
	}

	var existing vrouterv1.VRouterConfigList
	if err := r.List(ctx, &existing,
		client.InNamespace(binding.Namespace),
		client.MatchingLabels{vrouterv1.LabelBinding: binding.Name},
	); err != nil {
		return fmt.Errorf("list VRouterConfigs: %w", err)
	}
	for i := range existing.Items {
		if !desiredNames[existing.Items[i].Name] {
			if err := r.Delete(ctx, &existing.Items[i]); err != nil {
				return fmt.Errorf("delete orphan VRouterConfig %q: %w", existing.Items[i].Name, err)
			}
			log.Info("deleted orphan VRouterConfig", "name", existing.Items[i].Name)
		}
	}
	return nil
}

// createOrUpdateAll writes every desired VRouterConfig in one pass. This is
// the write step for RolloutModeDisabled only — see the mode dispatch
// comment in onChange.
func (r *VRouterBindingReconciler) createOrUpdateAll(ctx context.Context, binding *vrouterv1.VRouterBinding, desired []desiredConfig) error {
	for _, d := range desired {
		if err := r.createOrUpdateOne(ctx, binding, d); err != nil {
			return err
		}
	}
	return nil
}

// createOrUpdateOne writes a single desired VRouterConfig. Shared by the
// one-pass write (createOrUpdateAll) and the rollout walk, which writes at
// most one config per reconcile.
func (r *VRouterBindingReconciler) createOrUpdateOne(ctx context.Context, binding *vrouterv1.VRouterBinding, d desiredConfig) error {
	log := logf.FromContext(ctx)

	cfg := &vrouterv1.VRouterConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:      d.Name,
			Namespace: binding.Namespace,
		},
	}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, cfg, func() error {
		if cfg.Labels == nil {
			cfg.Labels = map[string]string{}
		}
		cfg.Labels[vrouterv1.LabelBinding] = binding.Name
		cfg.Labels[vrouterv1.LabelTarget] = d.Target.Name
		cfg.Spec = d.Spec
		return controllerutil.SetControllerReference(binding, cfg, r.Scheme)
	})
	if err != nil {
		return fmt.Errorf("sync VRouterConfig %q: %w", d.Name, err)
	}
	log.Info("synced VRouterConfig", "name", d.Name)
	return nil
}

// getExistingConfig fetches the generated VRouterConfig named name in the
// binding's namespace, returning (nil, nil) if it does not exist.
func (r *VRouterBindingReconciler) getExistingConfig(ctx context.Context, binding *vrouterv1.VRouterBinding, name string) (*vrouterv1.VRouterConfig, error) {
	var cfg vrouterv1.VRouterConfig
	err := r.Get(ctx, client.ObjectKey{Name: name, Namespace: binding.Namespace}, &cfg)
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get VRouterConfig %q: %w", name, err)
	}
	return &cfg, nil
}

// inTargetRefs reports whether targetName still appears in binding's
// targetRefs, by name. Used by gateRemaining to detect a frontier target that
// was evicted from targetRefs mid-rollout.
func inTargetRefs(binding *vrouterv1.VRouterBinding, targetName string) bool {
	for _, ref := range binding.Spec.TargetRefs {
		if ref.Name == targetName {
			return true
		}
	}
	return false
}

// appliedConditionForGeneration returns cfg's Applied condition when it is
// currently True for cfg's own generation — i.e. the most recently rendered
// spec was actually applied, not a stale True left over from before a
// re-render bumped the generation. Returns nil for a missing config (cfg ==
// nil), a config with no Applied condition yet, a False/unknown condition, or
// a True condition whose ObservedGeneration lags cfg.Generation.
func appliedConditionForGeneration(cfg *vrouterv1.VRouterConfig) *metav1.Condition {
	if cfg == nil {
		return nil
	}
	cond := meta.FindStatusCondition(cfg.Status.Conditions, vrouterv1.ConditionApplied)
	if cond == nil || cond.Status != metav1.ConditionTrue || cond.ObservedGeneration != cfg.Generation {
		return nil
	}
	return cond
}

// frontierRemaining returns how much longer the walk must wait before
// advancing past the current frontier target. Mirrors the issue #35
// pseudocode's frontierRemaining. A missing frontier config (cfg == nil, e.g.
// deleted while it was the frontier) is treated the same as "not yet Applied"
// in WaitForApplied — it just keeps polling rather than erroring — since the
// config controller will recreate it via createOrUpdateOne on the next write.
func frontierRemaining(mode string, rollout *vrouterv1.RolloutSpec, cfg *vrouterv1.VRouterConfig, frontier *vrouterv1.RolloutStatus, now time.Time) time.Duration {
	switch mode {
	case vrouterv1.RolloutModeFixedInterval:
		if frontier == nil {
			return 0
		}
		return frontier.LastUpdateTime.Time.Add(rollout.WaitInterval.Duration).Sub(now)
	case vrouterv1.RolloutModeWaitForApplied:
		cond := appliedConditionForGeneration(cfg)
		if cond == nil {
			// Not yet Applied for the current generation (or config missing):
			// time-based poll, no health signal to react to yet.
			return rollout.PollInterval.Duration
		}
		// Applied: soak for waitAfterApplied, measured from the Applied
		// condition's own lastTransitionTime (not the frontier stamp), so a
		// slow apply doesn't shortchange the soak period.
		return cond.LastTransitionTime.Time.Add(rollout.WaitAfterApplied.Duration).Sub(now)
	default:
		return 0
	}
}

// gateRemaining implements the "universal write gate" from the issue #35
// pseudocode: before writing ANY first-mismatch target — not only when
// advancing past the frontier — the same wait that would apply to the
// frontier must first be satisfied. This stops a re-render's first write
// from overlapping with a still in-flight previous-render frontier commit.
func (r *VRouterBindingReconciler) gateRemaining(ctx context.Context, binding *vrouterv1.VRouterBinding, mode string, frontier *vrouterv1.RolloutStatus, next desiredConfig, now time.Time) (time.Duration, error) {
	if frontier == nil || frontier.LastUpdatedTarget == "" {
		// No frontier recorded: first rollout ever, nothing to gate on.
		return 0, nil
	}
	if next.Target.Name == frontier.LastUpdatedTarget {
		// First mismatch IS the frontier itself (render changed while it was
		// still applying): overwrite directly. Single-router commits are
		// already serialized by the config controller's execPID handling.
		return 0, nil
	}
	if !inTargetRefs(binding, frontier.LastUpdatedTarget) {
		// Frontier evicted mid-rollout: degrade to a pure time gate. Never
		// health-gate a target the user just removed — it is often removed
		// precisely because it is dead — but still hold the interval so an
		// overlapping commit is not created.
		d := rolloutWaitDuration(mode, binding.Spec.Rollout)
		return frontier.LastUpdateTime.Time.Add(d).Sub(now), nil
	}

	cfgName := fmt.Sprintf("%s.%s", binding.Name, frontier.LastUpdatedTarget)
	cfg, err := r.getExistingConfig(ctx, binding, cfgName)
	if err != nil {
		return 0, err
	}
	return frontierRemaining(mode, binding.Spec.Rollout, cfg, frontier, now), nil
}

// rolloutWaitDuration returns the mode-appropriate wait used by the
// frontier-evicted time-degraded gate. FixedInterval uses waitInterval;
// WaitForApplied uses waitAfterApplied (never health-gate a target the user
// just evicted — see gateRemaining).
func rolloutWaitDuration(mode string, rollout *vrouterv1.RolloutSpec) time.Duration {
	if mode == vrouterv1.RolloutModeWaitForApplied {
		return rollout.WaitAfterApplied.Duration
	}
	return rollout.WaitInterval.Duration
}

// postWriteRequeueInterval returns the RequeueAfter used immediately after
// the rollout walk writes a config. FixedInterval requeues at waitInterval —
// the full stagger gap, since it never re-checks health in between.
// WaitForApplied requeues at pollInterval — a short check of whether the
// just-written config has reached Applied yet, per the pseudocode's
// time-based Applied polling.
func postWriteRequeueInterval(mode string, rollout *vrouterv1.RolloutSpec) time.Duration {
	if mode == vrouterv1.RolloutModeWaitForApplied {
		return rollout.PollInterval.Duration
	}
	return rollout.WaitInterval.Duration
}

// stampRolloutFrontier performs the rollout walk's write-ahead stamp: it
// records the frontier (target, now) in status.rollout and sets the Ready
// condition to False/RolloutInProgress in a single status update, before the
// generated VRouterConfig itself is written. Stamping first means a crash
// between the stamp and the config write leaves the config still mismatched,
// so the next reconcile finds the same first mismatch and retries — there is
// never a state where a config was updated but unrecorded.
func (r *VRouterBindingReconciler) stampRolloutFrontier(ctx context.Context, binding *vrouterv1.VRouterBinding, targetName string, now time.Time, message string) error {
	patch := client.MergeFrom(binding.DeepCopy())
	binding.Status.Rollout = &vrouterv1.RolloutStatus{
		LastUpdatedTarget: targetName,
		LastUpdateTime:    metav1.NewTime(now),
	}
	meta.SetStatusCondition(&binding.Status.Conditions, metav1.Condition{
		Type:               vrouterv1.ConditionReady,
		Status:             metav1.ConditionFalse,
		Reason:             ReasonRolloutInProgress,
		Message:            message,
		ObservedGeneration: binding.Generation,
	})
	return r.Status().Patch(ctx, binding, patch)
}

// firstHaltingFailedConfig scans desired in targetRefs order and returns the
// first generated VRouterConfig that is Failed AND whose existing spec
// already matches the desired render for its target -- the halting
// condition for mode: WaitForApplied (see issue #35 "Error handling
// (WaitForApplied)"). The scan covers every desired target, not just the
// frontier, so a completed earlier target that later flips to Failed halts
// the rollout the moment the walk sees it, exactly like a Failed frontier.
//
// A Failed config whose spec differs from the new render is deliberately
// NOT treated as halting: that is the documented resume path ("a new render
// changes the failed config's spec ... it becomes a first mismatch and is
// overwritten directly"). The normal first-mismatch walk picks it up like
// any other write, gated by gateRemaining exactly as usual -- if it happens
// to be the current frontier, gateRemaining's "first mismatch IS the
// frontier itself" case lets it through immediately.
func (r *VRouterBindingReconciler) firstHaltingFailedConfig(ctx context.Context, binding *vrouterv1.VRouterBinding, desired []desiredConfig) (*vrouterv1.VRouterConfig, error) {
	for _, d := range desired {
		existing, err := r.getExistingConfig(ctx, binding, d.Name)
		if err != nil {
			return nil, err
		}
		if existing == nil || existing.Status.Phase != vrouterv1.PhaseFailed {
			continue
		}
		if !equality.Semantic.DeepEqual(existing.Spec, d.Spec) {
			// Spec changed for this Failed config: resume path, not a halt.
			continue
		}
		return existing, nil
	}
	return nil, nil
}

// firstNotAppliedConfig scans desired in targetRefs order and returns the
// first desiredConfig whose generated VRouterConfig is not Applied for its
// current generation (per appliedConditionForGeneration) -- the walk-
// completion verification for mode: WaitForApplied (see issue #35 "Ready
// becomes a fleet-level wait primitive (WaitForApplied)"). This is a final
// consistency check, not the primary detection path: the any-Failed halt
// above already covers the common failure case; this additionally catches
// e.g. a target that is Pending (never Applied, never Failed) even though
// every desired spec otherwise matches and the frontier itself is Applied.
func (r *VRouterBindingReconciler) firstNotAppliedConfig(ctx context.Context, binding *vrouterv1.VRouterBinding, desired []desiredConfig) (*desiredConfig, error) {
	for i := range desired {
		existing, err := r.getExistingConfig(ctx, binding, desired[i].Name)
		if err != nil {
			return nil, err
		}
		if appliedConditionForGeneration(existing) == nil {
			return &desired[i], nil
		}
	}
	return nil, nil
}

// runRollout implements the serial "update only the first mismatch per
// reconcile" walk shared by mode: FixedInterval and mode: WaitForApplied (see
// issue #35: "Reuses the phase-1 skeleton unchanged" for Phase 2). It visits
// desired targets in order, writes at most one config (the first whose
// rendered spec differs from the existing generated VRouterConfig, once the
// universal write gate is satisfied), and requeues rather than looping in
// memory. The wait is always computed from persisted state — status.rollout,
// or (WaitForApplied) the frontier config's Applied condition — never from
// in-memory state, so the stagger is a hard guarantee across operator
// restarts and unrelated watch events. Mode-specific behavior lives entirely
// in frontierRemaining/gateRemaining/postWriteRequeueInterval; this walk
// itself has no mode branches.
func (r *VRouterBindingReconciler) runRollout(ctx context.Context, binding *vrouterv1.VRouterBinding, desired []desiredConfig) (ctrl.Result, error) {
	mode := binding.Spec.Rollout.EffectiveMode()
	now := r.now()
	frontier := binding.Status.Rollout

	// Any-Failed halt (WaitForApplied only, per issue #35 "Error handling
	// (WaitForApplied)"): checked before the walk writes anything. FixedInterval
	// ignores Failed by definition -- a failed target does not stop that mode's
	// rollout, so this check is skipped entirely there.
	if mode == vrouterv1.RolloutModeWaitForApplied {
		failed, err := r.firstHaltingFailedConfig(ctx, binding, desired)
		if err != nil {
			return ctrl.Result{}, err
		}
		if failed != nil {
			// Halt: no config writes, no frontier stamp, no apply re-dispatch.
			// Requeue at pollInterval purely to observe recovery -- config-level
			// Failed has no auto-retry, so this never re-dispatches the failed
			// apply itself.
			message := fmt.Sprintf("VRouterConfig %q is Failed", failed.Name)
			// Patch error ignored: the scheduled RequeueAfter below re-attempts
			// this same Ready write on the next poll regardless.
			_ = r.setReadyCondition(ctx, binding, metav1.ConditionFalse, ReasonRolloutHalted, message)
			return ctrl.Result{RequeueAfter: binding.Spec.Rollout.PollInterval.Duration}, nil
		}
	}

	for i, d := range desired {
		existing, err := r.getExistingConfig(ctx, binding, d.Name)
		if err != nil {
			return ctrl.Result{}, err
		}

		if existing == nil || !equality.Semantic.DeepEqual(existing.Spec, d.Spec) {
			// First mismatch: the only place that writes a config this reconcile.
			wait, err := r.gateRemaining(ctx, binding, mode, frontier, d, now)
			if err != nil {
				return ctrl.Result{}, err
			}
			if wait > 0 {
				return ctrl.Result{RequeueAfter: wait}, nil
			}

			message := fmt.Sprintf("%d/%d targets updated", i+1, len(desired))
			if err := r.stampRolloutFrontier(ctx, binding, d.Target.Name, now, message); err != nil {
				return ctrl.Result{}, err
			}
			if err := r.createOrUpdateOne(ctx, binding, d); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{RequeueAfter: postWriteRequeueInterval(mode, binding.Spec.Rollout)}, nil
		}

		// Spec matches. If this target isn't the frontier, it either never
		// needed an update or completed earlier — advance.
		if frontier == nil || d.Target.Name != frontier.LastUpdatedTarget {
			continue
		}

		// Spec matches AND this is the frontier: wait out its interval
		// before advancing past it.
		if remaining := frontierRemaining(mode, binding.Spec.Rollout, existing, frontier, now); remaining > 0 {
			return ctrl.Result{RequeueAfter: remaining}, nil
		}
	}

	// Walk completed with no mismatch and the frontier's wait elapsed: the
	// staggered writes are finished. status.rollout is left in place — a
	// stale frontier is harmless since its wait has already elapsed.
	if mode == vrouterv1.RolloutModeWaitForApplied {
		// Final consistency verification, not the primary detection path (the
		// any-Failed halt above is): Ready only becomes the fleet-level
		// "rollout completed" True once every generated config -- not just
		// the frontier the walk just finished waiting on -- is Applied for
		// its current generation. See issue #35 "Ready becomes a fleet-level
		// wait primitive (WaitForApplied)".
		notApplied, err := r.firstNotAppliedConfig(ctx, binding, desired)
		if err != nil {
			return ctrl.Result{}, err
		}
		if notApplied != nil {
			// Patch error ignored: the scheduled RequeueAfter below re-attempts
			// this same Ready write on the next poll regardless.
			_ = r.setReadyCondition(ctx, binding, metav1.ConditionFalse, ReasonRolloutInProgress, "waiting for all configs Applied")
			return ctrl.Result{RequeueAfter: binding.Spec.Rollout.PollInterval.Duration}, nil
		}
		// Terminal path with no other scheduled requeue: propagate a failed
		// patch so controller-runtime retries with backoff, instead of
		// reporting a completed rollout that never actually reached Ready=True.
		message := fmt.Sprintf("All %d VRouterConfigs applied.", len(desired))
		if err := r.setReadyCondition(ctx, binding, metav1.ConditionTrue, "ReconcileSucceeded", message); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	// Terminal path with no other scheduled requeue: same rationale as above.
	if err := r.setReadyCondition(ctx, binding, metav1.ConditionTrue, "ReconcileSucceeded", "All VRouterConfigs reconciled successfully."); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func (r *VRouterBindingReconciler) onChange(ctx context.Context, _ ctrl.Request, binding *vrouterv1.VRouterBinding) (ctrl.Result, error) {
	// Render the desired VRouterConfig for every target first, then clean up
	// orphans, before any writes — see the issue #35 pseudocode. Every
	// reconcile re-renders and walks from the top regardless of mode, which
	// is what lets a mid-rollout spec change "restart" the walk implicitly
	// (the universal write gate then preserves the stagger across renders).
	desired, err := r.renderAll(ctx, binding)
	if err != nil {
		// The render error, not a status patch failure, is the real cause:
		// return it unmasked even if the Ready-condition patch also fails.
		_ = r.setReadyCondition(ctx, binding, metav1.ConditionFalse, "ReconcileError", err.Error())
		return ctrl.Result{}, err
	}

	if err := r.cleanupOrphans(ctx, binding, desired); err != nil {
		_ = r.setReadyCondition(ctx, binding, metav1.ConditionFalse, "ReconcileError", err.Error())
		return ctrl.Result{}, err
	}

	// Mode dispatch: RolloutModeFixedInterval and RolloutModeWaitForApplied
	// both take the shared serial rollout walk (runRollout); only
	// RolloutModeDisabled keeps the one-pass write-everything behavior.
	switch binding.Spec.Rollout.EffectiveMode() {
	case vrouterv1.RolloutModeFixedInterval, vrouterv1.RolloutModeWaitForApplied:
		return r.runRollout(ctx, binding, desired)
	}

	if err := r.createOrUpdateAll(ctx, binding, desired); err != nil {
		_ = r.setReadyCondition(ctx, binding, metav1.ConditionFalse, "ReconcileError", err.Error())
		return ctrl.Result{}, err
	}

	// Terminal path with no other scheduled requeue (mode: Disabled writes
	// everything in one pass, same as the rollout walk's completion above):
	// propagate a failed patch instead of reporting success with the write
	// lost.
	if err := r.setReadyCondition(ctx, binding, metav1.ConditionTrue, "ReconcileSucceeded", "All VRouterConfigs reconciled successfully."); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *VRouterBindingReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&vrouterv1.VRouterBinding{}).
		// Re-reconcile bindings when the template they reference changes.
		Watches(&vrouterv1.VRouterTemplate{}, handler.EnqueueRequestsFromMapFunc(r.bindingsForTemplate)).
		// Re-reconcile bindings when a target they reference changes.
		Watches(&vrouterv1.VRouterTarget{}, handler.EnqueueRequestsFromMapFunc(r.bindingsForTarget)).
		// Re-reconcile bindings when a VRouterParams they reference changes.
		Watches(&vrouterv1.VRouterParams{}, handler.EnqueueRequestsFromMapFunc(r.bindingsForParams)).
		Named("vrouterbinding").
		Complete(r)
}

// bindingsForTemplate returns reconcile requests for all VRouterBindings that
// reference the given VRouterTemplate (via templateRef or templateRefs).
func (r *VRouterBindingReconciler) bindingsForTemplate(ctx context.Context, obj client.Object) []reconcile.Request {
	var list vrouterv1.VRouterBindingList
	if err := r.List(ctx, &list); err != nil {
		return nil
	}
	var reqs []reconcile.Request
	for i := range list.Items {
		for _, ref := range effectiveTemplateRefs(&list.Items[i]) {
			ns := vrouterv1.ResolveNamespace(ref, list.Items[i].Namespace)
			if ref.Name == obj.GetName() && ns == obj.GetNamespace() {
				reqs = append(reqs, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&list.Items[i])})
				break
			}
		}
	}
	return reqs
}

// bindingsForTarget returns reconcile requests for all VRouterBindings that
// reference the given VRouterTarget.
func (r *VRouterBindingReconciler) bindingsForTarget(ctx context.Context, obj client.Object) []reconcile.Request {
	var list vrouterv1.VRouterBindingList
	if err := r.List(ctx, &list); err != nil {
		return nil
	}
	var reqs []reconcile.Request
	for i := range list.Items {
		for _, ref := range list.Items[i].Spec.TargetRefs {
			ns := vrouterv1.ResolveNamespace(ref, list.Items[i].Namespace)
			if ref.Name == obj.GetName() && ns == obj.GetNamespace() {
				reqs = append(reqs, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&list.Items[i])})
				break
			}
		}
	}
	return reqs
}

// bindingsForParams returns reconcile requests for all VRouterBindings that
// reference the given VRouterParams (via paramsRefs).
func (r *VRouterBindingReconciler) bindingsForParams(ctx context.Context, obj client.Object) []reconcile.Request {
	var list vrouterv1.VRouterBindingList
	if err := r.List(ctx, &list); err != nil {
		return nil
	}
	var reqs []reconcile.Request
	for i := range list.Items {
		for _, ref := range list.Items[i].Spec.ParamsRefs {
			ns := vrouterv1.ResolveNamespace(ref, list.Items[i].Namespace)
			if ref.Name == obj.GetName() && ns == obj.GetNamespace() {
				reqs = append(reqs, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&list.Items[i])})
				break
			}
		}
	}
	return reqs
}
