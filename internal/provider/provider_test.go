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

package provider

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	kubevirtv1 "kubevirt.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	vrouterv1 "github.com/tjjh89017/vrouter-operator/api/v1"
)

func newTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := vrouterv1.AddToScheme(scheme); err != nil {
		t.Fatalf("add vrouterv1 to scheme: %v", err)
	}
	if err := kubevirtv1.AddToScheme(scheme); err != nil {
		t.Fatalf("add kubevirtv1 to scheme: %v", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add corev1 to scheme: %v", err)
	}
	return scheme
}

func runningVMI(name, namespace string) *kubevirtv1.VirtualMachineInstance {
	return &kubevirtv1.VirtualMachineInstance{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Status:     kubevirtv1.VirtualMachineInstanceStatus{Phase: kubevirtv1.Running},
	}
}

// should default the KubeVirt namespace to the VRouterTarget's own namespace
// when provider.kubevirt.namespace is omitted, so the provider looks up the
// VMI in the correct namespace instead of the empty string.
func TestNew_KubeVirtNamespaceDefaultsToTargetNamespace(t *testing.T) {
	ctx := context.Background()
	scheme := newTestScheme(t)

	// The VMI lives in "foo", the same namespace as the VRouterTarget.
	vmi := runningVMI("vm1", "foo")
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(vmi).Build()

	target := &vrouterv1.VRouterTarget{
		ObjectMeta: metav1.ObjectMeta{Name: "target1", Namespace: "foo"},
		Spec: vrouterv1.VRouterTargetSpec{
			Provider: vrouterv1.ProviderConfig{
				Type:     vrouterv1.ProviderType("kubevirt"),
				KubeVirt: &vrouterv1.KubeVirtConfig{Name: "vm1"}, // Namespace intentionally omitted
			},
		},
	}

	p, err := New(ctx, target, cl, &rest.Config{})
	if err != nil {
		t.Fatalf("New() returned error: %v", err)
	}

	running, err := p.IsVMRunning(ctx)
	if err != nil {
		t.Fatalf("IsVMRunning() returned error: %v", err)
	}
	if !running {
		t.Fatalf("IsVMRunning() = false, want true (namespace should default to target namespace %q, not empty string)", target.Namespace)
	}
}

// should respect an explicitly set provider.kubevirt.namespace instead of
// overriding it with the VRouterTarget's own namespace.
func TestNew_KubeVirtExplicitNamespaceIsRespected(t *testing.T) {
	ctx := context.Background()
	scheme := newTestScheme(t)

	// A VMI named "vm1" exists in both namespaces so a namespace mix-up would
	// still return a (wrong) result instead of an obvious NotFound: the one in
	// "foo" (the target's own namespace) is stopped, while the one in the
	// explicitly configured "bar" namespace is running.
	stoppedVMI := &kubevirtv1.VirtualMachineInstance{
		ObjectMeta: metav1.ObjectMeta{Name: "vm1", Namespace: "foo"},
		Status:     kubevirtv1.VirtualMachineInstanceStatus{Phase: kubevirtv1.Succeeded},
	}
	runningInBar := runningVMI("vm1", "bar")
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(stoppedVMI, runningInBar).Build()

	target := &vrouterv1.VRouterTarget{
		ObjectMeta: metav1.ObjectMeta{Name: "target1", Namespace: "foo"},
		Spec: vrouterv1.VRouterTargetSpec{
			Provider: vrouterv1.ProviderConfig{
				Type:     vrouterv1.ProviderType("kubevirt"),
				KubeVirt: &vrouterv1.KubeVirtConfig{Name: "vm1", Namespace: "bar"},
			},
		},
	}

	p, err := New(ctx, target, cl, &rest.Config{})
	if err != nil {
		t.Fatalf("New() returned error: %v", err)
	}

	running, err := p.IsVMRunning(ctx)
	if err != nil {
		t.Fatalf("IsVMRunning() returned error: %v", err)
	}
	if !running {
		t.Fatalf("IsVMRunning() = false, want true (explicit namespace %q should be used, not target namespace %q)", "bar", target.Namespace)
	}
}

// TestNew_NilSubConfigs_Errors is a table test over the factory's three
// "sub-config must be set when type is X" guard clauses. These are the
// factory's own defense in depth: the webhook (validateProviderConfig)
// should already reject a nil sub-config at admission, but New() must not
// silently proceed to a nil-pointer dereference if that ever regresses or a
// resource is constructed directly (e.g. from a test or a future internal
// caller) bypassing admission.
func TestNew_NilSubConfigs_Errors(t *testing.T) {
	tests := []struct {
		name        string
		provider    vrouterv1.ProviderType
		wantErrText string
	}{
		{name: "kubevirt nil sub-config", provider: vrouterv1.ProviderKubeVirt, wantErrText: "provider.kubevirt must be set"},
		{name: "proxmox nil sub-config", provider: vrouterv1.ProviderProxmox, wantErrText: "provider.proxmox must be set"},
		{name: "daemon nil sub-config", provider: vrouterv1.ProviderVRouterDaemon, wantErrText: "provider.daemon must be set"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scheme := newTestScheme(t)
			cl := fake.NewClientBuilder().WithScheme(scheme).Build()
			target := &vrouterv1.VRouterTarget{
				ObjectMeta: metav1.ObjectMeta{Name: "target1", Namespace: "default"},
				Spec:       vrouterv1.VRouterTargetSpec{Provider: vrouterv1.ProviderConfig{Type: tt.provider}},
			}

			_, err := New(context.Background(), target, cl, &rest.Config{})
			if err == nil {
				t.Fatalf("New() returned nil error, want an error for a nil %s sub-config", tt.provider)
			}
			if !strings.Contains(err.Error(), tt.wantErrText) {
				t.Fatalf("error = %q, want substring %q", err.Error(), tt.wantErrText)
			}
		})
	}
}

// TestNew_ProxmoxClusterRefNotFound_ErrorsBeforeConstruction verifies that a
// dangling provider.proxmox.clusterRef (referencing a ProxmoxCluster that
// does not exist) is surfaced as a clear error rather than a nil-pointer
// dereference or an opaque failure deep inside proxmox.New.
func TestNew_ProxmoxClusterRefNotFound_ErrorsBeforeConstruction(t *testing.T) {
	scheme := newTestScheme(t)
	cl := fake.NewClientBuilder().WithScheme(scheme).Build()
	target := &vrouterv1.VRouterTarget{
		ObjectMeta: metav1.ObjectMeta{Name: "target1", Namespace: "default"},
		Spec: vrouterv1.VRouterTargetSpec{
			Provider: vrouterv1.ProviderConfig{
				Type:    vrouterv1.ProviderProxmox,
				Proxmox: &vrouterv1.ProxmoxConfig{VMID: 100, ClusterRef: vrouterv1.NameRef{Name: "does-not-exist"}},
			},
		},
	}

	_, err := New(context.Background(), target, cl, &rest.Config{})
	if err == nil {
		t.Fatalf("New() returned nil error, want an error for a dangling clusterRef")
	}
	if !strings.Contains(err.Error(), "does-not-exist") {
		t.Fatalf("error = %q, want it to name the missing ProxmoxCluster", err.Error())
	}
}

// TestNew_ProxmoxValidConfig_ConstructsProvider verifies the success path of
// the Proxmox branch end-to-end: it resolves the referenced ProxmoxCluster,
// reads its credentials Secret, and passes target.Status.ProxmoxNode through
// as the provider's cached node (avoiding a redundant cluster/resources
// lookup on first use).
func TestNew_ProxmoxValidConfig_ConstructsProvider(t *testing.T) {
	scheme := newTestScheme(t)
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "pve-creds", Namespace: "default"},
		Data: map[string][]byte{
			"api-token-id":     []byte("user@pve!token"),
			"api-token-secret": []byte("secret"),
		},
	}
	cluster := &vrouterv1.ProxmoxCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster1", Namespace: "default"},
		Spec: vrouterv1.ProxmoxClusterSpec{
			Endpoints:      []string{"https://pve.example.com:8006"},
			CredentialsRef: vrouterv1.SecretReference{Name: "pve-creds"},
		},
	}
	target := &vrouterv1.VRouterTarget{
		ObjectMeta: metav1.ObjectMeta{Name: "target1", Namespace: "default"},
		Spec: vrouterv1.VRouterTargetSpec{
			Provider: vrouterv1.ProviderConfig{
				Type:    vrouterv1.ProviderProxmox,
				Proxmox: &vrouterv1.ProxmoxConfig{VMID: 100, ClusterRef: vrouterv1.NameRef{Name: "cluster1"}},
			},
		},
		Status: vrouterv1.VRouterTargetStatus{ProxmoxNode: "nodeA"},
	}
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(secret, cluster).Build()

	p, err := New(context.Background(), target, cl, &rest.Config{})
	if err != nil {
		t.Fatalf("New() returned error: %v", err)
	}
	if p == nil {
		t.Fatalf("New() returned a nil Provider with a nil error")
	}
}

// TestNew_DaemonValidConfig_ConstructsProvider verifies the vrouter-daemon
// branch dispatches to daemon.New with the given config. grpc.NewClient
// connects lazily, so this does not require a live daemon endpoint.
func TestNew_DaemonValidConfig_ConstructsProvider(t *testing.T) {
	scheme := newTestScheme(t)
	cl := fake.NewClientBuilder().WithScheme(scheme).Build()
	target := &vrouterv1.VRouterTarget{
		ObjectMeta: metav1.ObjectMeta{Name: "target1", Namespace: "default"},
		Spec: vrouterv1.VRouterTargetSpec{
			Provider: vrouterv1.ProviderConfig{
				Type:   vrouterv1.ProviderVRouterDaemon,
				Daemon: &vrouterv1.DaemonConfig{Address: "vrouter-daemon.default.svc:9090", AgentID: "agent-1"},
			},
		},
	}

	p, err := New(context.Background(), target, cl, &rest.Config{})
	if err != nil {
		t.Fatalf("New() returned error: %v", err)
	}
	if p == nil {
		t.Fatalf("New() returned a nil Provider with a nil error")
	}
}
