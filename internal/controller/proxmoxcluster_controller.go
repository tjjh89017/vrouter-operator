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
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8stypes "k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	vrouterv1 "github.com/tjjh89017/vrouter-operator/api/v1"
)

// ProxmoxClusterReconciler reconciles a ProxmoxCluster object
type ProxmoxClusterReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=vrouter.kojuro.date,resources=proxmoxclusters,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=vrouter.kojuro.date,resources=proxmoxclusters/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=vrouter.kojuro.date,resources=proxmoxclusters/finalizers,verbs=update
// +kubebuilder:rbac:groups=vrouter.kojuro.date,resources=vroutertargets,verbs=get;list;watch
// +kubebuilder:rbac:groups=vrouter.kojuro.date,resources=vroutertargets/status,verbs=get;update;patch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch

func (r *ProxmoxClusterReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var cluster vrouterv1.ProxmoxCluster
	if err := r.Get(ctx, req.NamespacedName, &cluster); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !cluster.DeletionTimestamp.IsZero() {
		return r.onDelete(ctx, req, &cluster)
	}

	if !controllerutil.ContainsFinalizer(&cluster, vrouterv1.FinalizerName) {
		controllerutil.AddFinalizer(&cluster, vrouterv1.FinalizerName)
		if err := r.Update(ctx, &cluster); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	return r.onChange(ctx, req, &cluster)
}

func (r *ProxmoxClusterReconciler) onDelete(ctx context.Context, _ ctrl.Request, cluster *vrouterv1.ProxmoxCluster) (ctrl.Result, error) {
	controllerutil.RemoveFinalizer(cluster, vrouterv1.FinalizerName)
	return ctrl.Result{}, r.Update(ctx, cluster)
}

func (r *ProxmoxClusterReconciler) onChange(ctx context.Context, _ ctrl.Request, cluster *vrouterv1.ProxmoxCluster) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	syncInterval := cluster.Spec.SyncInterval.Duration
	if syncInterval == 0 {
		syncInterval = 60 * time.Second
	}

	// Check if it is time to sync.
	if cluster.Status.LastSyncTime != nil {
		elapsed := time.Since(cluster.Status.LastSyncTime.Time)
		if elapsed < syncInterval {
			return ctrl.Result{RequeueAfter: syncInterval - elapsed}, nil
		}
	}

	// Read credentials secret.
	secret := &corev1.Secret{}
	if err := r.Get(ctx, k8stypes.NamespacedName{
		Name:      cluster.Spec.CredentialsRef.Name,
		Namespace: cluster.Namespace,
	}, secret); err != nil {
		r.setSyncedCondition(ctx, cluster, metav1.ConditionFalse, "CredentialsNotFound", err.Error())
		return ctrl.Result{}, fmt.Errorf("read credentials secret: %w", err)
	}
	tokenID := strings.TrimSpace(string(secret.Data["api-token-id"]))
	tokenSecret := strings.TrimSpace(string(secret.Data["api-token-secret"]))
	if tokenID == "" || tokenSecret == "" {
		msg := fmt.Sprintf("secret %q must contain api-token-id and api-token-secret keys", cluster.Spec.CredentialsRef.Name)
		r.setSyncedCondition(ctx, cluster, metav1.ConditionFalse, "InvalidCredentials", msg)
		return ctrl.Result{}, fmt.Errorf("%s", msg)
	}

	// Query Proxmox cluster resources.
	vmMap, err := r.fetchClusterResources(ctx, cluster, tokenID, tokenSecret)
	if err != nil {
		log.Info("Proxmox unreachable, marking all targets as stopped", "reason", err.Error())
		r.setSyncedCondition(ctx, cluster, metav1.ConditionFalse, "FetchFailed", err.Error())
		r.markTargetsStopped(ctx, cluster)
		return ctrl.Result{RequeueAfter: syncInterval}, nil
	}

	// List matching VRouterTargets and update their status.
	var targetList vrouterv1.VRouterTargetList
	if err := r.List(ctx, &targetList, client.InNamespace(cluster.Namespace)); err != nil {
		return ctrl.Result{}, fmt.Errorf("list VRouterTargets: %w", err)
	}

	threshold := 1.5 * syncInterval.Seconds()
	now := metav1.Now()

	for i := range targetList.Items {
		t := &targetList.Items[i]
		if t.Spec.Provider.Type != vrouterv1.ProviderProxmox {
			continue
		}
		px := t.Spec.Provider.Proxmox
		if px == nil {
			continue
		}
		ref := px.ClusterRef
		refNS := ref.Namespace
		if refNS == "" {
			refNS = t.Namespace
		}
		if refNS != cluster.Namespace || ref.Name != cluster.Name {
			continue
		}

		info, ok := vmMap[px.VMID]
		if !ok {
			continue
		}

		patch := client.MergeFrom(t.DeepCopy())
		changed := false

		if t.Status.ProxmoxNode != info.node {
			t.Status.ProxmoxNode = info.node
			changed = true
		}
		// Reboot detection via Proxmox-level uptime (detects hard restart only).
		if info.uptime > 0 && info.uptime <= threshold {
			t.Status.LastRebootTime = &now
			changed = true
		}
		// Reboot detection via guest OS uptime through QGA (detects soft reboot too).
		if cluster.Spec.CheckGuestUptime && info.node != "" {
			guestUptime, err := r.fetchGuestUptime(ctx, cluster, info.node, px.VMID, tokenID, tokenSecret)
			if err != nil {
				log.Info("fetch guest uptime skipped", "target", t.Name, "reason", err.Error())
			} else if guestUptime > 0 && guestUptime <= threshold {
				t.Status.LastRebootTime = &now
				changed = true
			}
		}

		if changed {
			if err := r.Status().Patch(ctx, t, patch); err != nil {
				log.Error(err, "patch VRouterTarget status", "target", t.Name)
			}
		}
	}

	// Update cluster status.
	patch := client.MergeFrom(cluster.DeepCopy())
	cluster.Status.LastSyncTime = &now
	meta.SetStatusCondition(&cluster.Status.Conditions, metav1.Condition{
		Type:    "Synced",
		Status:  metav1.ConditionTrue,
		Reason:  "SyncSucceeded",
		Message: "Cluster resources polled successfully.",
	})
	if err := r.Status().Patch(ctx, cluster, patch); err != nil {
		return ctrl.Result{}, err
	}

	log.Info("sync complete", "vms", len(vmMap))
	return ctrl.Result{RequeueAfter: syncInterval}, nil
}

type vmInfo struct {
	node   string
	uptime float64
}

// fetchClusterResources calls /api2/json/cluster/resources?type=vm and returns a map of VMID → vmInfo.
func (r *ProxmoxClusterReconciler) fetchClusterResources(ctx context.Context, cluster *vrouterv1.ProxmoxCluster, tokenID, tokenSecret string) (map[int]vmInfo, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: cluster.Spec.InsecureSkipTLSVerify} //nolint:gosec
	httpClient := &http.Client{Transport: transport, Timeout: 30 * time.Second}
	authHeader := fmt.Sprintf("PVEAPIToken=%s=%s", tokenID, tokenSecret)

	var lastErr error
	for _, ep := range cluster.Spec.Endpoints {
		ep = strings.TrimRight(ep, "/")
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, ep+"/api2/json/cluster/resources?type=vm", nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", authHeader)

		resp, err := httpClient.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		body, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			lastErr = err
			continue
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			lastErr = fmt.Errorf("HTTP %d: %s", resp.StatusCode, bytes.TrimSpace(body))
			continue
		}

		var result struct {
			Data []struct {
				VMID   int     `json:"vmid"`
				Node   string  `json:"node"`
				Uptime float64 `json:"uptime"`
			} `json:"data"`
		}
		if err := json.Unmarshal(body, &result); err != nil {
			return nil, fmt.Errorf("cluster resources decode: %w", err)
		}
		m := make(map[int]vmInfo, len(result.Data))
		for _, vm := range result.Data {
			m[vm.VMID] = vmInfo{node: vm.Node, uptime: vm.Uptime}
		}
		return m, nil
	}
	return nil, fmt.Errorf("all endpoints failed: %w", lastErr)
}

// fetchGuestUptime queries the guest OS uptime (seconds) via QEMU Guest Agent
// by reading /proc/uptime inside the VM. Returns 0 on any error.
func (r *ProxmoxClusterReconciler) fetchGuestUptime(ctx context.Context, cluster *vrouterv1.ProxmoxCluster, node string, vmid int, tokenID, tokenSecret string) (float64, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: cluster.Spec.InsecureSkipTLSVerify} //nolint:gosec
	httpClient := &http.Client{Transport: transport, Timeout: 30 * time.Second}
	authHeader := fmt.Sprintf("PVEAPIToken=%s=%s", tokenID, tokenSecret)

	// Start async exec: cat /proc/uptime
	execPath := fmt.Sprintf("/api2/json/nodes/%s/qemu/%d/agent/exec", node, vmid)
	payload, _ := json.Marshal(map[string]any{"command": []string{"cat", "/proc/uptime"}})

	var pid int64
	var lastErr error
	for _, ep := range cluster.Spec.Endpoints {
		ep = strings.TrimRight(ep, "/")
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, ep+execPath, bytes.NewReader(payload))
		if err != nil {
			return 0, err
		}
		req.Header.Set("Authorization", authHeader)
		req.Header.Set("Content-Type", "application/json")
		resp, err := httpClient.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			lastErr = fmt.Errorf("HTTP %d: %s", resp.StatusCode, bytes.TrimSpace(body))
			continue
		}
		var execResult struct {
			Data struct {
				PID int64 `json:"pid"`
			} `json:"data"`
		}
		if err := json.Unmarshal(body, &execResult); err != nil {
			return 0, fmt.Errorf("guest exec decode: %w", err)
		}
		pid = execResult.Data.PID
		lastErr = nil
		break
	}
	if lastErr != nil {
		return 0, fmt.Errorf("all endpoints failed: %w", lastErr)
	}

	// Poll exec-status until exited.
	statusPath := fmt.Sprintf("/api2/json/nodes/%s/qemu/%d/agent/exec-status?pid=%d", node, vmid, pid)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		for _, ep := range cluster.Spec.Endpoints {
			ep = strings.TrimRight(ep, "/")
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, ep+statusPath, nil)
			if err != nil {
				return 0, err
			}
			req.Header.Set("Authorization", authHeader)
			resp, err := httpClient.Do(req)
			if err != nil {
				continue
			}
			body, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				continue
			}
			var statusResult struct {
				Data struct {
					Exited  int    `json:"exited"`
					OutData string `json:"out-data"`
				} `json:"data"`
			}
			if err := json.Unmarshal(body, &statusResult); err != nil {
				return 0, fmt.Errorf("guest exec-status decode: %w", err)
			}
			if statusResult.Data.Exited == 0 {
				break // not yet done, try again
			}
			// /proc/uptime format: "12345.67 23456.78"
			var uptime float64
			_, _ = fmt.Sscanf(statusResult.Data.OutData, "%f", &uptime)
			return uptime, nil
		}
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	return 0, fmt.Errorf("timeout waiting for guest uptime exec")
}

// markTargetsStopped sets VMRunning=false on all VRouterTargets referencing this cluster.
func (r *ProxmoxClusterReconciler) markTargetsStopped(ctx context.Context, cluster *vrouterv1.ProxmoxCluster) {
	log := logf.FromContext(ctx)
	var targetList vrouterv1.VRouterTargetList
	if err := r.List(ctx, &targetList, client.InNamespace(cluster.Namespace)); err != nil {
		log.Error(err, "list VRouterTargets for markTargetsStopped")
		return
	}
	for i := range targetList.Items {
		t := &targetList.Items[i]
		if t.Spec.Provider.Type != vrouterv1.ProviderProxmox || t.Spec.Provider.Proxmox == nil {
			continue
		}
		ref := t.Spec.Provider.Proxmox.ClusterRef
		refNS := ref.Namespace
		if refNS == "" {
			refNS = t.Namespace
		}
		if refNS != cluster.Namespace || ref.Name != cluster.Name {
			continue
		}
		if !t.Status.VMRunning {
			continue
		}
		patch := client.MergeFrom(t.DeepCopy())
		t.Status.VMRunning = false
		if err := r.Status().Patch(ctx, t, patch); err != nil {
			log.Error(err, "patch VRouterTarget VMRunning=false", "target", t.Name)
		}
	}
}

// setSyncedCondition patches the cluster status with a Synced condition.
func (r *ProxmoxClusterReconciler) setSyncedCondition(ctx context.Context, cluster *vrouterv1.ProxmoxCluster, status metav1.ConditionStatus, reason, msg string) {
	patch := client.MergeFrom(cluster.DeepCopy())
	meta.SetStatusCondition(&cluster.Status.Conditions, metav1.Condition{
		Type:    "Synced",
		Status:  status,
		Reason:  reason,
		Message: msg,
	})
	_ = r.Status().Patch(ctx, cluster, patch)
}

// clusterForTarget maps a VRouterTarget event to the ProxmoxCluster it references.
func (r *ProxmoxClusterReconciler) clusterForTarget(ctx context.Context, obj client.Object) []reconcile.Request {
	target, ok := obj.(*vrouterv1.VRouterTarget)
	if !ok {
		return nil
	}
	if target.Spec.Provider.Type != vrouterv1.ProviderProxmox || target.Spec.Provider.Proxmox == nil {
		return nil
	}
	ref := target.Spec.Provider.Proxmox.ClusterRef
	ns := ref.Namespace
	if ns == "" {
		ns = target.Namespace
	}
	return []reconcile.Request{{
		NamespacedName: k8stypes.NamespacedName{Name: ref.Name, Namespace: ns},
	}}
}

// SetupWithManager sets up the controller with the Manager.
func (r *ProxmoxClusterReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&vrouterv1.ProxmoxCluster{}).
		Watches(&vrouterv1.VRouterTarget{}, handler.EnqueueRequestsFromMapFunc(r.clusterForTarget)).
		Named("proxmoxcluster").
		Complete(r)
}
