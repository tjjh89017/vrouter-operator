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
	"bytes"
	"context"
	"fmt"
	"strings"
	"text/template"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	vrouterv1 "github.com/tjjh89017/vrouter-operator/api/v1"
	"github.com/tjjh89017/vrouter-operator/internal/provider"
)

// VRouterConfigReconciler reconciles a VRouterConfig object.
type VRouterConfigReconciler struct {
	client.Client
	Scheme     *runtime.Scheme
	RestConfig *rest.Config
}

// +kubebuilder:rbac:groups=vrouter.kojuro.date,resources=vrouterconfigs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=vrouter.kojuro.date,resources=vrouterconfigs/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=vrouter.kojuro.date,resources=vrouterconfigs/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=pods/exec,verbs=create
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups=kubevirt.io,resources=virtualmachines;virtualmachineinstances,verbs=get;list;watch

var applyScriptTmpl = template.Must(template.New("apply").Parse(`#!/bin/vbash
source /opt/vyatta/etc/functions/script-template
configure

# --- config section ---
{{- if .Config }}
load /dev/stdin <<'VYOS_CONFIG_EOF'
{{ .Config }}
VYOS_CONFIG_EOF
{{- else }}
load /opt/vyatta/etc/config.boot.default
{{- end }}

# --- commands section (optional) ---
{{- if .Commands }}
{{ .Commands }}
{{- end }}

commit
{{- if .Save }}
save
{{- end }}
`))

type scriptData struct {
	Config   string
	Commands string
	Save     bool
}

const requeueAfter = 3 * time.Second

func (r *VRouterConfigReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var cfg vrouterv1.VRouterConfig
	if err := r.Get(ctx, req.NamespacedName, &cfg); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Build provider bound to this config's target.
	prov, err := provider.New(cfg.Spec.Provider, r.Client, r.RestConfig)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("build provider: %w", err)
	}

	// Step 1: pre-check — QGA ping + vyos-router.service is-active.
	if err := prov.CheckReady(ctx); err != nil {
		log.Info("router not ready", "reason", err.Error())
		return ctrl.Result{}, fmt.Errorf("router not ready: %w", err)
	}

	// Step 2: if exec already dispatched for this generation, poll its result.
	if cfg.Generation == cfg.Status.ObservedGeneration {
		if cfg.Status.ExecPID > 0 {
			return r.pollExecStatus(ctx, &cfg, prov)
		}
		// Already done (Applied or Failed) for this generation.
		return ctrl.Result{}, nil
	}

	// Step 3: new spec — render and apply.
	return r.applyConfig(ctx, &cfg, prov)
}

// pollExecStatus checks the running script and updates status accordingly.
func (r *VRouterConfigReconciler) pollExecStatus(ctx context.Context, cfg *vrouterv1.VRouterConfig, prov provider.Provider) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	status, err := prov.GetExecStatus(ctx, cfg.Status.ExecPID)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("get exec status: %w", err)
	}

	if !status.Exited {
		patch := client.MergeFrom(cfg.DeepCopy())
		cfg.Status.Phase = vrouterv1.PhaseApplying
		if err := r.Status().Patch(ctx, cfg, patch); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: requeueAfter}, nil
	}

	// Script exited — update status.
	patch := client.MergeFrom(cfg.DeepCopy())
	cfg.Status.ExecPID = 0
	if status.ExitCode == 0 {
		now := metav1.Now()
		cfg.Status.Phase = vrouterv1.PhaseApplied
		cfg.Status.LastAppliedTime = &now
		cfg.Status.Message = ""
		log.Info("config applied successfully")
	} else {
		cfg.Status.Phase = vrouterv1.PhaseFailed
		cfg.Status.Message = fmt.Sprintf("exitCode=%d stderr=%s", status.ExitCode, strings.TrimSpace(status.Stderr))
		log.Info("config apply failed", "exitCode", status.ExitCode, "stderr", status.Stderr)
	}
	return ctrl.Result{}, r.Status().Patch(ctx, cfg, patch)
}

// applyConfig renders the script and dispatches execution.
func (r *VRouterConfigReconciler) applyConfig(ctx context.Context, cfg *vrouterv1.VRouterConfig, prov provider.Provider) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// Render internal script template.
	var buf bytes.Buffer
	if err := applyScriptTmpl.Execute(&buf, scriptData{
		Config:   cfg.Spec.Config,
		Commands: cfg.Spec.Commands,
		Save:     cfg.Spec.Save,
	}); err != nil {
		return ctrl.Result{}, fmt.Errorf("render script: %w", err)
	}
	script := buf.Bytes()

	// Write script to router.
	if err := prov.WriteFile(ctx, script); err != nil {
		return ctrl.Result{}, fmt.Errorf("write script: %w", err)
	}

	// Execute script asynchronously.
	pid, err := prov.ExecScript(ctx)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("exec script: %w", err)
	}
	log.Info("script dispatched", "pid", pid)

	// Record PID and observedGeneration.
	patch := client.MergeFrom(cfg.DeepCopy())
	cfg.Status.ExecPID = pid
	cfg.Status.ObservedGeneration = cfg.Generation
	cfg.Status.Phase = vrouterv1.PhaseApplying
	cfg.Status.Message = ""
	if err := r.Status().Patch(ctx, cfg, patch); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: requeueAfter}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *VRouterConfigReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&vrouterv1.VRouterConfig{}).
		Named("vrouterconfig").
		Complete(r)
}
