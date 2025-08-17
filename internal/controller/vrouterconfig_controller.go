/*
Copyright 2025.

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
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	corev1 "k8s.io/api/core/v1"

	vrouterv1 "github.com/tjjh89017/vrouter-operator/api/v1"
	"github.com/tjjh89017/vrouter-operator/constants"
)

// VRouterConfigReconciler reconciles a VRouterConfig object
type VRouterConfigReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	Namespace string
	Name      string
}

type QGAExecRequest struct {
	Execute   string `json:"execute"`
	Arguments struct {
		Path          string   `json:"path"`
		Arg           []string `json:"arg,omitempty"`
		Env           []string `json:"env,omitempty"`
		CaptureOutput bool     `json:"capture-output"`
	} `json:"arguments"`
}

type QGAExecResponse struct {
	Return struct {
		Pid int64 `json:"pid"`
	} `json:"return"`
}

type QGAExecStatusRequest struct {
	Execute   string `json:"execute"`
	Arguments struct {
		Pid int64 `json:"pid"`
	} `json:"arguments"`
}

type QGAExecStatusResponse struct {
	Return struct {
		ExitCode int    `json:"exitcode"`
		Exited   bool   `json:"exited"`
		OutData  string `json:"out-data"`
	} `json:"return"`
}

type QGAFileOpenRequest struct {
	Execute   string `json:"execute"`
	Arguments struct {
		Path string `json:"path"`
		Mode string `json:"mode,omitempty"`
	} `json:"arguments"`
}

type QGAFileOpenResponse struct {
	Return int64 `json:"return"`
}

type QGAFileWriteRequest struct {
	Execute   string `json:"execute"`
	Arguments struct {
		Handle int64  `json:"handle"`
		BufB64 string `json:"buf-b64"`
	} `json:"arguments"`
}

type QGAFileCloseRequest struct {
	Execute   string `json:"execute"`
	Arguments struct {
		Handle int64 `json:"handle"`
	} `json:"arguments"`
}

const (
	SuccessExitOutData = "QWN0aXZlU3RhdGU9YWN0aXZlClN1YlN0YXRlPWV4aXRlZAo=" // Base64 encoded "ActiveState=active\nSubState=exited\n"

	ExecScriptName = "/tmp/exec.sh"
	ExecScript     = `
if [ "$(id -g -n)" != 'vyattacfg' ] ; then
    exec sg vyattacfg -c "/bin/vbash $(readlink -f $0) $@"
fi

%s

%s

source /opt/vyatta/etc/functions/script-template
configure
if [ -s /tmp/config.boot ]
then
    load /tmp/config.boot
else
    load /config/config.boot
fi

if [ -s /tmp/config.command ]
then
    source /tmp/config.command
fi
commit

# clean up
rm -f /tmp/config.boot
rm -f /tmp/config.command
exit
`
)

// +kubebuilder:rbac:groups=vrouter.kojuro.date,resources=vrouterconfigs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=vrouter.kojuro.date,resources=vrouterconfigs/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=vrouter.kojuro.date,resources=vrouterconfigs/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the VRouterConfig object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.21.0/pkg/reconcile
func (r *VRouterConfigReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := logf.FromContext(ctx)

	logger.Info("Reconciling VRouterConfig", "name", req.Name, "namespace", req.Namespace)

	// TODO(user): your logic here
	pod := &corev1.Pod{}
	if err := r.Get(ctx, client.ObjectKey{Namespace: r.Namespace, Name: r.Name}, pod); err != nil {
		logger.Error(err, "Failed to get Pod", "name", r.Name, "namespace", r.Namespace)
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	vrouterConfigName := pod.Annotations[constants.VRouterConfigAnnotation]

	if vrouterConfigName != req.Name {
		logger.Info("This is not my config, skip", "expected", req.Name, "found", vrouterConfigName)
		return ctrl.Result{}, nil
	}

	logger.Info("VRouterConfig found, processing", "name", vrouterConfigName)
	vrouterConfig := &vrouterv1.VRouterConfig{}
	if err := r.Get(ctx, client.ObjectKey{Namespace: r.Namespace, Name: vrouterConfigName}, vrouterConfig); err != nil {
		logger.Error(err, "Failed to get VRouterConfig", "name", vrouterConfigName, "namespace", r.Namespace)
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// TODO put config to VyOS

	var (
		id  int64
		err error
	)

	if id, err = r.CheckIfVM(ctx); err != nil {
		logger.Error(err, "Failed to check if VM is running, retrying")
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}
	logger.Info("VM ID found", "id", id)

	// Wait for QEMU-GA to be ready
	if err = r.CheckQEMUGA(ctx, id); err != nil {
		logger.Error(err, "QEMU-GA is not ready, retrying")
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}
	logger.Info("QEMU-GA is ready, proceeding with configuration")

	// Wait for vyos-router to be ready
	if err = r.CheckVyOSRouterReady(ctx, id); err != nil {
		logger.Error(err, "vyos-router is not ready, retrying")
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}
	logger.Info("vyos-router is ready")

	// generate script with config
	config, err := r.ConfigRenderer(ctx, vrouterConfig)
	if err != nil {
		logger.Error(err, "Failed to render config")
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}

	logger.Info("rendered Config:", "config", config)

	// push script to vyos
	if err = r.PushConfig(ctx, id, config); err != nil {
		logger.Error(err, "push config failed")
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}
	logger.Info("Config is ready in VyOS")

	// execute script on vyos
	if err = r.ExecuteScript(ctx, id); err != nil {
		logger.Error(err, "Failed to execute script on VyOS")
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}
	logger.Info("Script executed successfully on VyOS")

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *VRouterConfigReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&vrouterv1.VRouterConfig{}).
		Named("vrouterconfig").
		Complete(r)
}

func (r *VRouterConfigReconciler) CheckIfVM(ctx context.Context) (int64, error) {
	logger := logf.FromContext(ctx)
	cmd := exec.Command("virsh", "list", "--id", "--state-running")
	output, err := cmd.CombinedOutput()
	if err != nil {
		logger.Error(err, "Failed to execute virsh command", "output", string(output))
		return -1, err
	}
	logger.Info("virsh command output", "output", string(output))

	i, err := strconv.ParseInt(strings.TrimSpace(string(output)), 10, 32)
	if err != nil {
		logger.Error(err, "Failed to parse virsh output", "output", string(output))
		return -1, err
	}

	return i, nil
}

func (r *VRouterConfigReconciler) CheckQEMUGA(ctx context.Context, id int64) error {
	logger := logf.FromContext(ctx)
	cmd := exec.Command("virsh", "qemu-agent-command", strconv.FormatInt(id, 10), `{"execute":"guest-ping"}`)
	output, err := cmd.CombinedOutput()
	if err != nil {
		logger.Error(err, "Failed to execute virsh command", "output", string(output))
		return err
	}
	logger.Info("virsh command output", "output", string(output))

	if string(output) == "" {
		return fmt.Errorf("QEMU-GA is not responding, output is empty")
	}

	return nil
}

func (r *VRouterConfigReconciler) CheckVyOSRouterReady(ctx context.Context, id int64) error {
	logger := logf.FromContext(ctx)

	req := QGAExecRequest{
		Execute: "guest-exec",
		Arguments: struct {
			Path          string   `json:"path"`
			Arg           []string `json:"arg,omitempty"`
			Env           []string `json:"env,omitempty"`
			CaptureOutput bool     `json:"capture-output"`
		}{
			Path:          "/bin/vbash",
			Arg:           []string{"-c", `systemctl show vyos-router | grep -E "ActiveState=active|SubState=exited"`},
			CaptureOutput: true,
		},
	}

	reqJSON, err := json.Marshal(req)
	if err != nil {
		logger.Error(err, "Failed to marshal QGAExecRequest")
		return err
	}

	cmd := exec.Command("virsh", "qemu-agent-command", strconv.FormatInt(id, 10), string(reqJSON))
	output, err := cmd.CombinedOutput()
	if err != nil {
		logger.Error(err, "Failed to execute virsh command", "output", string(output))
		return err
	}
	logger.Info("virsh command output", "output", string(output))

	var response QGAExecResponse
	if err := json.Unmarshal(output, &response); err != nil {
		logger.Error(err, "Failed to unmarshal QGAExecResponse", "output", string(output))
		return err
	}

	pid := response.Return.Pid

	time.Sleep(500 * time.Millisecond)

	// Get pid status
	reqStatus := QGAExecStatusRequest{
		Execute: "guest-exec-status",
		Arguments: struct {
			Pid int64 `json:"pid"`
		}{
			Pid: pid,
		},
	}
	reqStatusJSON, err := json.Marshal(reqStatus)
	if err != nil {
		logger.Error(err, "Failed to marshal QGAExecStatusRequest")
		return err
	}
	cmd = exec.Command("virsh", "qemu-agent-command", strconv.FormatInt(id, 10), string(reqStatusJSON))
	output, err = cmd.CombinedOutput()
	if err != nil {
		logger.Error(err, "Failed to execute virsh command", "output", string(output))
		return err
	}
	logger.Info("virsh command output", "output", string(output))

	var responseStatus QGAExecStatusResponse
	if err := json.Unmarshal(output, &responseStatus); err != nil {
		logger.Error(err, "Failed to unmarshal QGAExecStatusResponse", "output", string(output))
		return err
	}

	if responseStatus.Return.Exited && responseStatus.Return.ExitCode == 0 && responseStatus.Return.OutData == SuccessExitOutData {
		return nil
	}

	return fmt.Errorf("vyos-router is not ready")
}

func (r *VRouterConfigReconciler) PushConfig(ctx context.Context, id int64, config string) error {
	logger := logf.FromContext(ctx)

	fileOpenReq := QGAFileOpenRequest{
		Execute: "guest-file-open",
		Arguments: struct {
			Path string `json:"path"`
			Mode string `json:"mode,omitempty"`
		}{
			Path: ExecScriptName,
			Mode: "w",
		},
	}
	fileOpenReqJSON, err := json.Marshal(fileOpenReq)
	if err != nil {
		logger.Error(err, "file open req json marshal failed")
		return err
	}
	cmd := exec.Command("virsh", "qemu-agent-command", strconv.FormatInt(id, 10), string(fileOpenReqJSON))
	output, err := cmd.CombinedOutput()
	if err != nil {
		logger.Error(err, "Failed to execute virsh command (open file)", "output", string(output))
		return err
	}
	var fileOpenRes QGAFileOpenResponse
	if err = json.Unmarshal(output, &fileOpenRes); err != nil {
		logger.Error(err, "JSON unmarshal fd failed")
		return err
	}
	fd := fileOpenRes.Return
	logger.Info("open exec.sh success", "handle", fd)

	fileWriteReq := QGAFileWriteRequest{
		Execute: "guest-file-write",
		Arguments: struct {
			Handle int64  `json:"handle"`
			BufB64 string `json:"buf-b64"`
		}{
			Handle: fd,
			BufB64: base64.StdEncoding.EncodeToString([]byte(config)),
		},
	}
	fileWriteReqJSON, err := json.Marshal(fileWriteReq)
	if err != nil {
		logger.Error(err, "file write req json marshal failed")
		return err
	}
	cmd = exec.Command("virsh", "qemu-agent-command", strconv.FormatInt(id, 10), string(fileWriteReqJSON))
	output, err = cmd.CombinedOutput()
	if err != nil {
		logger.Error(err, "Failed to execute virsh command (write file)", "output", string(output))
		return err
	}

	fileCloseReq := QGAFileCloseRequest{
		Execute: "guest-file-close",
		Arguments: struct {
			Handle int64 `json:"handle"`
		}{
			Handle: fd,
		},
	}
	fileCloseReqJSON, err := json.Marshal(fileCloseReq)
	if err != nil {
		logger.Error(err, "file close req json marshal failed")
		return err
	}
	cmd = exec.Command("virsh", "qemu-agent-command", strconv.FormatInt(id, 10), string(fileCloseReqJSON))
	output, err = cmd.CombinedOutput()
	if err != nil {
		logger.Error(err, "Failed to execute virsh command (close file)", "output", string(output))
		return err
	}

	return nil
}

func (r *VRouterConfigReconciler) ConfigRenderer(ctx context.Context, vrouterConfig *vrouterv1.VRouterConfig) (string, error) {
	logger := logf.FromContext(ctx)

	/*
	   cat <<EOF > /tmp/config.boot
	   %s
	   EOF

	   cat <<EOF > /tmp/config.command
	   %s
	   EOF
	*/

	configBoot := ""
	if vrouterConfig.Spec.Config != "" {
		configBoot = fmt.Sprintf("cat <<EOF > /tmp/config.boot\n%s\nEOF", vrouterConfig.Spec.Config)
	}
	configCommand := ""
	if len(vrouterConfig.Spec.Command) > 0 {
		configCommand = fmt.Sprintf("cat <<EOF > /tmp/config.command\n%s\nEOF", strings.Join(vrouterConfig.Spec.Command, "\n"))
	}

	config := fmt.Sprintf(ExecScript, configBoot, configCommand)
	logger.Info("Rendered Config", "config", config)

	return config, nil
}

func (r *VRouterConfigReconciler) ExecuteScript(ctx context.Context, id int64) error {
	logger := logf.FromContext(ctx)

	req := QGAExecRequest{
		Execute: "guest-exec",
		Arguments: struct {
			Path          string   `json:"path"`
			Arg           []string `json:"arg,omitempty"`
			Env           []string `json:"env,omitempty"`
			CaptureOutput bool     `json:"capture-output"`
		}{
			Path:          "/bin/vbash",
			Arg:           []string{"/tmp/exec.sh"},
			CaptureOutput: true,
		},
	}

	reqJSON, err := json.Marshal(req)
	if err != nil {
		logger.Error(err, "Failed to marshal QGAExecRequest")
		return err
	}

	cmd := exec.Command("virsh", "qemu-agent-command", strconv.FormatInt(id, 10), string(reqJSON))
	output, err := cmd.CombinedOutput()
	if err != nil {
		logger.Error(err, "Failed to execute virsh command", "output", string(output))
		return err
	}
	logger.Info("virsh command output", "output", string(output))

	return nil
}
