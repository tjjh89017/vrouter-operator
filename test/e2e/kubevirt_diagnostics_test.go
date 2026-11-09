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

package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	. "github.com/onsi/ginkgo/v2"

	"github.com/tjjh89017/vrouter-operator/test/utils"
)

// dumpKubeVirtDiagnosticsOnFailure writes VM/VMI state, virt-launcher logs,
// controller-manager logs and cluster events to the GinkgoWriter when the
// current spec failed. Call it from a JustAfterEach: Ginkgo runs that node
// before AfterAll and before DeferCleanup, so the VMs still exist. A failed
// BeforeAll also counts as a failure of the first spec.
func dumpKubeVirtDiagnosticsOnFailure() {
	if e2eCluster != "k3s" || !CurrentSpecReport().Failed() {
		return
	}

	By("collecting KubeVirt diagnostics for the failed spec")
	_, _ = fmt.Fprintf(GinkgoWriter, "===== KubeVirt diagnostics =====\n")
	_, _ = fmt.Fprintf(GinkgoWriter, "guest containerDisk: %q, guest version: %q\n",
		os.Getenv("E2E_ROUTER_CONTAINERDISK"), os.Getenv("E2E_ROUTER_VERSION"))

	dumpKubectl("get", "vm,vmi", "-A", "-o", "wide")
	dumpKubectl("describe", "vmi", "-A")

	out, err := utils.Run(exec.Command("kubectl", "get", "pod", "-A",
		"-l", "kubevirt.io=virt-launcher",
		"-o", `jsonpath={range .items[*]}{.metadata.namespace}{" "}{.metadata.name}{"\n"}{end}`))
	if err != nil {
		_, _ = fmt.Fprintf(GinkgoWriter, "failed to list virt-launcher pods: %v\n", err)
	}
	for line := range strings.SplitSeq(strings.TrimSpace(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		dumpKubectl("-n", fields[0], "describe", "pod", fields[1])
		dumpKubectl("-n", fields[0], "logs", fields[1], "--all-containers", "--tail=200")
	}

	dumpKubectl("-n", "kubevirt", "logs", "-l", "kubevirt.io=virt-handler",
		"--all-containers", "--tail=100")
	dumpKubectl("-n", namespace, "logs", "deployment/vrouter-operator-controller-manager",
		"--all-containers", "--tail=300")
	dumpKubectl("get", "events", "-A", "--sort-by=.lastTimestamp")
}

// dumpKubectl runs kubectl with args and writes its output, or its error, to the
// GinkgoWriter.
func dumpKubectl(args ...string) {
	out, err := utils.Run(exec.Command("kubectl", args...))
	_, _ = fmt.Fprintf(GinkgoWriter, "----- kubectl %s -----\n", strings.Join(args, " "))
	if err != nil {
		_, _ = fmt.Fprintf(GinkgoWriter, "command failed: %v\n", err)
		return
	}
	_, _ = fmt.Fprintf(GinkgoWriter, "%s\n", out)
}
