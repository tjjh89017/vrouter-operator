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
	"testing"

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
