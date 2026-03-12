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

// Package proxmox provides a Proxmox VE provider that uses the Proxmox REST
// API to execute QEMU Guest Agent commands on a target VM.
package proxmox

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	vrouterv1 "github.com/tjjh89017/vrouter-operator/api/v1"
	"github.com/tjjh89017/vrouter-operator/internal/provider/qga"
	providertypes "github.com/tjjh89017/vrouter-operator/internal/provider/types"
)

// Provider implements provider.Provider for Proxmox VE via the REST API + QGA.
type Provider struct {
	httpClient  *http.Client
	endpoints   []string
	tokenID     string
	tokenSecret string
	vmid        int
	cachedNode  string
}

// New creates a Proxmox provider bound to the given VM.
// It reads API credentials from the Kubernetes Secret named by cluster.Spec.CredentialsRef.
// cachedNode is the pre-resolved node from VRouterTarget.Status.ProxmoxNode; pass "" to
// force a live lookup via /cluster/resources on the first operation.
func New(ctx context.Context, cfg *vrouterv1.ProxmoxConfig, cluster *vrouterv1.ProxmoxCluster, cachedNode string, cl client.Client) (*Provider, error) {
	secret := &corev1.Secret{}
	secretNS := cluster.Namespace
	if err := cl.Get(ctx, k8stypes.NamespacedName{Name: cluster.Spec.CredentialsRef.Name, Namespace: secretNS}, secret); err != nil {
		return nil, fmt.Errorf("read credentials secret %q: %w", cluster.Spec.CredentialsRef.Name, err)
	}
	tokenID := strings.TrimSpace(string(secret.Data["api-token-id"]))
	tokenSecret := strings.TrimSpace(string(secret.Data["api-token-secret"]))
	if tokenID == "" || tokenSecret == "" {
		return nil, fmt.Errorf("secret %q must contain api-token-id and api-token-secret keys", cluster.Spec.CredentialsRef.Name)
	}

	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: cluster.Spec.InsecureSkipTLSVerify} //nolint:gosec

	endpoints := make([]string, len(cluster.Spec.Endpoints))
	for i, e := range cluster.Spec.Endpoints {
		endpoints[i] = strings.TrimRight(e, "/")
	}
	return &Provider{
		httpClient:  &http.Client{Transport: transport, Timeout: 30 * time.Second},
		endpoints:   endpoints,
		tokenID:     tokenID,
		tokenSecret: tokenSecret,
		vmid:        cfg.VMID,
		cachedNode:  cachedNode,
	}, nil
}

// authHeader returns the Proxmox API token authorization header value.
func (p *Provider) authHeader() string {
	return fmt.Sprintf("PVEAPIToken=%s=%s", p.tokenID, p.tokenSecret)
}

// tryDo sends method+path+payload to each endpoint in order, stopping at the
// first endpoint that responds (even with an HTTP error). Network-level errors
// cause the next endpoint to be tried. Returns body and HTTP status code.
func (p *Provider) tryDo(ctx context.Context, method, path string, payload []byte) ([]byte, int, error) {
	var lastErr error
	for _, ep := range p.endpoints {
		var body io.Reader
		if payload != nil {
			body = bytes.NewReader(payload)
		}
		req, err := http.NewRequestWithContext(ctx, method, ep+path, body)
		if err != nil {
			return nil, 0, err
		}
		req.Header.Set("Authorization", p.authHeader())
		if payload != nil {
			req.Header.Set("Content-Type", "application/json")
		}

		resp, err := p.httpClient.Do(req)
		if err != nil {
			lastErr = err
			continue // network error — try next endpoint
		}
		respBody, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			lastErr = err
			continue
		}
		return respBody, resp.StatusCode, nil
	}
	return nil, 0, fmt.Errorf("all endpoints failed: %w", lastErr)
}

// get is a convenience wrapper around tryDo for GET requests.
// Non-2xx responses are returned as errors.
func (p *Provider) get(ctx context.Context, path string) ([]byte, error) {
	body, status, err := p.tryDo(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("HTTP %d: %s", status, bytes.TrimSpace(body))
	}
	return body, nil
}

// post is a convenience wrapper around tryDo for POST requests.
// Non-2xx responses are returned as errors.
func (p *Provider) post(ctx context.Context, path string, payload []byte) ([]byte, error) {
	body, status, err := p.tryDo(ctx, http.MethodPost, path, payload)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("HTTP %d: %s", status, bytes.TrimSpace(body))
	}
	return body, nil
}

// resolveNode discovers which Proxmox node hosts the VM.
// If cachedNode is set it is returned immediately without an API call.
func (p *Provider) resolveNode(ctx context.Context) (string, error) {
	if p.cachedNode != "" {
		return p.cachedNode, nil
	}
	body, err := p.get(ctx, "/api2/json/cluster/resources?type=vm")
	if err != nil {
		return "", fmt.Errorf("cluster resources: %w", err)
	}
	var result struct {
		Data []struct {
			VMID int    `json:"vmid"`
			Node string `json:"node"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("cluster resources decode: %w", err)
	}
	for _, vm := range result.Data {
		if vm.VMID == p.vmid {
			return vm.Node, nil
		}
	}
	return "", fmt.Errorf("VMID %d not found in cluster", p.vmid)
}

// IsVMRunning returns true if the VM is in running state.
func (p *Provider) IsVMRunning(ctx context.Context) (bool, error) {
	node, err := p.resolveNode(ctx)
	if err != nil {
		return false, err
	}

	path := fmt.Sprintf("/api2/json/nodes/%s/qemu/%d/status/current", node, p.vmid)
	body, status, err := p.tryDo(ctx, http.MethodGet, path, nil)
	if err != nil {
		return false, fmt.Errorf("VM status: %w", err)
	}
	if status == http.StatusNotFound {
		return false, nil
	}
	if status < 200 || status >= 300 {
		return false, fmt.Errorf("VM status: HTTP %d: %s", status, bytes.TrimSpace(body))
	}

	var result struct {
		Data struct {
			Status string `json:"status"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return false, fmt.Errorf("VM status decode: %w", err)
	}
	return result.Data.Status == "running", nil
}

// CheckReady verifies QGA is responsive and vyos-router.service is active.
func (p *Provider) CheckReady(ctx context.Context) error {
	node, err := p.resolveNode(ctx)
	if err != nil {
		return err
	}

	if err := p.agentPing(ctx, node); err != nil {
		return fmt.Errorf("QGA not responding: %w", err)
	}

	pid, err := p.agentExec(ctx, node, []string{
		"/usr/bin/systemctl", "show", "-p", "SubState", "--value", qga.VyOSService,
	})
	if err != nil {
		return fmt.Errorf("service substate check: %w", err)
	}

	for {
		status, err := p.agentExecStatus(ctx, node, pid)
		if err != nil {
			return fmt.Errorf("service substate status: %w", err)
		}
		if status.Exited {
			if status.ExitCode != 0 {
				return fmt.Errorf("systemctl show failed (exitCode=%d)", status.ExitCode)
			}
			substate := strings.TrimSpace(status.Stdout)
			if substate != "exited" {
				return fmt.Errorf("%s not ready (substate=%s)", qga.VyOSService, substate)
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// WriteFile writes the apply script content to the router via the Proxmox file-write agent endpoint.
func (p *Provider) WriteFile(ctx context.Context, content []byte) error {
	node, err := p.resolveNode(ctx)
	if err != nil {
		return err
	}
	return p.agentFileWrite(ctx, node, qga.ScriptPath, content)
}

// ExecScript executes the apply script asynchronously via agent exec, returns PID.
func (p *Provider) ExecScript(ctx context.Context) (int64, error) {
	node, err := p.resolveNode(ctx)
	if err != nil {
		return 0, err
	}
	return p.agentExec(ctx, node, []string{"/bin/vbash", qga.ScriptPath})
}

// GetExecStatus polls the result of a previously started script.
func (p *Provider) GetExecStatus(ctx context.Context, pid int64) (*providertypes.ExecStatus, error) {
	node, err := p.resolveNode(ctx)
	if err != nil {
		return nil, err
	}
	return p.agentExecStatus(ctx, node, pid)
}

// agentPing pings the QEMU guest agent to verify it is responsive.
func (p *Provider) agentPing(ctx context.Context, node string) error {
	_, err := p.post(ctx, fmt.Sprintf("/api2/json/nodes/%s/qemu/%d/agent/ping", node, p.vmid), nil)
	return err
}

// agentFileWrite writes content to path on the guest via the agent file-write endpoint.
func (p *Provider) agentFileWrite(ctx context.Context, node, path string, content []byte) error {
	payload, err := json.Marshal(map[string]string{
		"file":    path,
		"content": string(content),
	})
	if err != nil {
		return err
	}
	_, err = p.post(ctx, fmt.Sprintf("/api2/json/nodes/%s/qemu/%d/agent/file-write", node, p.vmid), payload)
	return err
}

// agentExec runs a command asynchronously on the guest and returns the PID.
func (p *Provider) agentExec(ctx context.Context, node string, command []string) (int64, error) {
	payload, err := json.Marshal(map[string]any{
		"command": command,
	})
	if err != nil {
		return 0, err
	}
	body, err := p.post(ctx, fmt.Sprintf("/api2/json/nodes/%s/qemu/%d/agent/exec", node, p.vmid), payload)
	if err != nil {
		return 0, err
	}
	var result struct {
		Data struct {
			PID int64 `json:"pid"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return 0, fmt.Errorf("agent exec decode: %w", err)
	}
	return result.Data.PID, nil
}

// agentExecStatus polls the execution status of a guest PID.
func (p *Provider) agentExecStatus(ctx context.Context, node string, pid int64) (*providertypes.ExecStatus, error) {
	body, err := p.get(ctx, fmt.Sprintf("/api2/json/nodes/%s/qemu/%d/agent/exec-status?pid=%d", node, p.vmid, pid))
	if err != nil {
		return nil, err
	}
	var result struct {
		Data struct {
			Exited   int    `json:"exited"`
			ExitCode int    `json:"exitcode"`
			OutData  string `json:"out-data"`
			ErrData  string `json:"err-data"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("exec-status decode: %w", err)
	}
	stdout, _ := decodeBase64OrRaw(result.Data.OutData)
	stderr, _ := decodeBase64OrRaw(result.Data.ErrData)
	return &providertypes.ExecStatus{
		Exited:   result.Data.Exited != 0,
		ExitCode: result.Data.ExitCode,
		Stdout:   stdout,
		Stderr:   stderr,
	}, nil
}

// decodeBase64OrRaw decodes a base64 string; returns the raw string if decoding fails.
func decodeBase64OrRaw(s string) (string, error) {
	if s == "" {
		return "", nil
	}
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return s, err
	}
	return string(b), nil
}
