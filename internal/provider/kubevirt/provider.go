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

package kubevirt

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	k8sscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
	kubevirtv1 "kubevirt.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/tjjh89017/vrouter-operator/internal/provider/qga"
	providertypes "github.com/tjjh89017/vrouter-operator/internal/provider/types"
)

// Provider implements provider.Provider for KubeVirt via SPDY exec into virt-launcher + QGA.
type Provider struct {
	client    client.Client
	k8sClient kubernetes.Interface
	restCfg   *rest.Config
	vmName    string
	namespace string
}

// New creates a KubeVirt provider bound to the given VM.
func New(cl client.Client, restCfg *rest.Config, vmName, namespace string) (*Provider, error) {
	k8sClient, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		return nil, fmt.Errorf("build kubernetes client: %w", err)
	}
	return &Provider{
		client:    cl,
		k8sClient: k8sClient,
		restCfg:   restCfg,
		vmName:    vmName,
		namespace: namespace,
	}, nil
}

// IsVMRunning returns true if the VirtualMachineInstance exists and is not in a terminal phase.
func (p *Provider) IsVMRunning(ctx context.Context) (bool, error) {
	vmi := &kubevirtv1.VirtualMachineInstance{}
	if err := p.client.Get(ctx, k8stypes.NamespacedName{Namespace: p.namespace, Name: p.vmName}, vmi); err != nil {
		if client.IgnoreNotFound(err) == nil {
			return false, nil // VMI not found = VM is stopped
		}
		return false, err
	}
	switch vmi.Status.Phase {
	case kubevirtv1.Succeeded, kubevirtv1.Failed:
		return false, nil
	}
	return true, nil
}

// CheckReady verifies QGA is responsive and vyos-router.service is active.
func (p *Provider) CheckReady(ctx context.Context) error {
	if _, err := p.runQGA(ctx, qga.CmdPing); err != nil {
		return fmt.Errorf("QGA not responding: %w", err)
	}

	resp, err := p.runQGA(ctx, fmt.Sprintf(qga.CmdExecServiceSubState, qga.VyOSService))
	if err != nil {
		return fmt.Errorf("service substate check: %w", err)
	}
	var execResult struct {
		Return struct {
			PID int64 `json:"pid"`
		} `json:"return"`
	}
	if err := json.Unmarshal([]byte(resp), &execResult); err != nil {
		return fmt.Errorf("service substate check parse: %w", err)
	}

	// Poll until systemctl show exits (completes quickly in practice).
	for {
		statusResp, err := p.runQGA(ctx, fmt.Sprintf(qga.CmdExecStatus, execResult.Return.PID))
		if err != nil {
			return fmt.Errorf("service substate status: %w", err)
		}
		var statusResult struct {
			Return struct {
				Exited   bool   `json:"exited"`
				ExitCode int    `json:"exitcode"`
				OutData  string `json:"out-data"`
			} `json:"return"`
		}
		if err := json.Unmarshal([]byte(statusResp), &statusResult); err != nil {
			return fmt.Errorf("service substate status parse: %w", err)
		}
		if statusResult.Return.Exited {
			if statusResult.Return.ExitCode != 0 {
				return fmt.Errorf("systemctl show failed (exitCode=%d)", statusResult.Return.ExitCode)
			}
			substate, _ := decodeBase64OrEmpty(statusResult.Return.OutData)
			substate = strings.TrimSpace(substate)
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

// ExecScript renders the vbash apply script, writes it to the router via QGA,
// and executes it asynchronously. Returns the guest-exec PID for polling.
func (p *Provider) ExecScript(ctx context.Context, config, commands string, save bool) (int64, error) {
	content, err := qga.RenderScript(config, commands, save)
	if err != nil {
		return 0, fmt.Errorf("render script: %w", err)
	}

	// Write script to router via QGA file operations.
	openResp, err := p.runQGA(ctx, fmt.Sprintf(qga.CmdFileOpen, qga.ScriptPath))
	if err != nil {
		return 0, fmt.Errorf("guest-file-open: %w", err)
	}
	var openResult struct {
		Return int64 `json:"return"`
	}
	if err := json.Unmarshal([]byte(openResp), &openResult); err != nil {
		return 0, fmt.Errorf("guest-file-open parse: %w", err)
	}
	handle := openResult.Return

	b64 := base64.StdEncoding.EncodeToString(content)
	if _, err := p.runQGA(ctx, fmt.Sprintf(qga.CmdFileWrite, handle, b64)); err != nil {
		return 0, fmt.Errorf("guest-file-write: %w", err)
	}
	if _, err := p.runQGA(ctx, fmt.Sprintf(qga.CmdFileClose, handle)); err != nil {
		return 0, fmt.Errorf("guest-file-close: %w", err)
	}

	// Execute the script asynchronously, capturing output.
	resp, err := p.runQGA(ctx, fmt.Sprintf(qga.CmdExecScript, qga.ScriptPath))
	if err != nil {
		return 0, fmt.Errorf("guest-exec: %w", err)
	}
	var result struct {
		Return struct {
			PID int64 `json:"pid"`
		} `json:"return"`
	}
	if err := json.Unmarshal([]byte(resp), &result); err != nil {
		return 0, fmt.Errorf("guest-exec parse: %w", err)
	}
	return result.Return.PID, nil
}

// GetExecStatus polls the result of a previously started script via guest-exec-status.
func (p *Provider) GetExecStatus(ctx context.Context, pid int64) (*providertypes.ExecStatus, error) {
	resp, err := p.runQGA(ctx, fmt.Sprintf(qga.CmdExecStatus, pid))
	if err != nil {
		return nil, fmt.Errorf("guest-exec-status: %w", err)
	}
	var result struct {
		Return struct {
			Exited   bool   `json:"exited"`
			ExitCode int    `json:"exitcode"`
			OutData  string `json:"out-data"`
			ErrData  string `json:"err-data"`
		} `json:"return"`
	}
	if err := json.Unmarshal([]byte(resp), &result); err != nil {
		return nil, fmt.Errorf("guest-exec-status parse: %w", err)
	}
	stdout, _ := decodeBase64OrEmpty(result.Return.OutData)
	stderr, _ := decodeBase64OrEmpty(result.Return.ErrData)
	return &providertypes.ExecStatus{
		Exited:   result.Return.Exited,
		ExitCode: result.Return.ExitCode,
		Stdout:   stdout,
		Stderr:   stderr,
	}, nil
}

// runQGA runs a QGA JSON command via virsh qemu-agent-command inside the virt-launcher pod.
func (p *Provider) runQGA(ctx context.Context, agentCmd string) (string, error) {
	pod, err := p.findVirtLauncherPod(ctx)
	if err != nil {
		return "", err
	}
	domain := p.namespace + "_" + p.vmName
	stdout, stderr, err := p.execInPod(ctx, pod.Namespace, pod.Name, "compute",
		[]string{"virsh", "qemu-agent-command", domain, agentCmd})
	if err != nil {
		return "", fmt.Errorf("virsh exec failed (stderr: %s): %w", stderr, err)
	}

	// QGA errors are returned as JSON {"error": {...}} in stdout.
	var qgaErr struct {
		Error *struct {
			Desc string `json:"desc"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(stdout), &qgaErr); err == nil && qgaErr.Error != nil {
		return "", fmt.Errorf("QGA error: %s", qgaErr.Error.Desc)
	}
	return stdout, nil
}

// findVirtLauncherPod finds the virt-launcher pod for the configured VM.
func (p *Provider) findVirtLauncherPod(ctx context.Context) (*corev1.Pod, error) {
	opts := []client.ListOption{
		client.MatchingLabels{"vm.kubevirt.io/name": p.vmName},
	}
	if p.namespace != "" {
		opts = append(opts, client.InNamespace(p.namespace))
	}
	podList := &corev1.PodList{}
	if err := p.client.List(ctx, podList, opts...); err != nil {
		return nil, fmt.Errorf("list virt-launcher pods: %w", err)
	}
	if len(podList.Items) == 0 {
		return nil, fmt.Errorf("no virt-launcher pod found for VM %q (namespace: %q)", p.vmName, p.namespace)
	}
	return &podList.Items[0], nil
}

// execInPod runs a command inside a pod container via SPDY exec and returns stdout + stderr.
func (p *Provider) execInPod(ctx context.Context, namespace, podName, container string, command []string) (string, string, error) {
	req := p.k8sClient.CoreV1().RESTClient().Post().
		Resource("pods").
		Name(podName).
		Namespace(namespace).
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: container,
			Command:   command,
			Stdout:    true,
			Stderr:    true,
		}, k8sscheme.ParameterCodec)

	exec, err := remotecommand.NewSPDYExecutor(p.restCfg, "POST", req.URL())
	if err != nil {
		return "", "", fmt.Errorf("SPDY executor: %w", err)
	}
	var stdout, stderr bytes.Buffer
	err = exec.StreamWithContext(ctx, remotecommand.StreamOptions{
		Stdout: &stdout,
		Stderr: &stderr,
	})
	return stdout.String(), stderr.String(), err
}

func decodeBase64OrEmpty(s string) (string, error) {
	if s == "" {
		return "", nil
	}
	b, err := base64.StdEncoding.DecodeString(s)
	return string(b), err
}
