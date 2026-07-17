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
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/tjjh89017/vrouter-operator/test/utils"
)

// KubeVirt provider real-data-path e2e (issue #38, item E2 — happy path only).
//
// This suite exercises the KubeVirt provider end to end against a live
// k3s+KubeVirt cluster with hardware virtualization: it boots a real
// virtual-router guest VM, points a VRouterConfig at it (via a VRouterTarget),
// and asserts the operator drives the guest to Applied/Ready, then verifies the
// applied configuration INSIDE the guest via QGA.
//
// CR wiring (minimal path that genuinely exercises CheckReady -> apply ->
// status): a single VRouterTarget + VRouterConfig pair. VRouterConfig is the CR
// the VRouterController reconciles; its spec.targetRef points at a VRouterTarget
// that carries the KubeVirt ProviderConfig (spec.provider.kubevirt.name = the
// VM name). A VRouterBinding+Template+Params chain is NOT used: it would only
// *generate* VRouterConfigs, which then reconcile through the exact same KubeVirt
// data path — adding indirection without exercising more of the provider. The
// validating webhook (internal/webhook/v1/vrouterconfig_webhook.go) requires
// spec.targetRef to resolve to an existing VRouterTarget in the SAME namespace,
// so both CRs live in the VM's namespace and the Target is created first.
//
// Guard: the whole suite is skipped unless E2E_CLUSTER=k3s, because it needs a
// real KubeVirt + KVM node. The default Kind suite must never run it.
//
// Shared timing budgets. Package-level (rather than per-Describe-local) so
// both this suite and the rollout-e2e suite (test/e2e/rollout_e2e_test.go),
// which boots VMs and drives applies the same way, use identical, generous
// windows instead of drifting copies. NEVER assert once against a live
// cluster — every check that reads these uses Eventually/Consistently.
const (
	// vmReadyTimeout is the generous first-boot budget: guest boot +
	// qemu-guest-agent coming up on hardware-accelerated KVM.
	vmReadyTimeout = 10 * time.Minute
	// applyTimeout is the generous apply budget. The guest's router unit is a
	// oneshot that only settles to active(exited) ~15s AFTER AgentConnected,
	// and CheckReady polls SubState until "exited" before dispatching — so the
	// operator may legitimately spend minutes before Applied.
	applyTimeout = 10 * time.Minute
	applyPoll    = 15 * time.Second
	// failTimeout is the failure-path budget. A genuine commit-time rejection
	// surfaces within seconds of the operator dispatching the exec, so the
	// generous 10m applyTimeout only wastes CI time here — 4m still absorbs
	// scheduling/QGA latency while failing fast if the config is wrong.
	failTimeout = 4 * time.Minute
)

// Structured so item E3 can append a failure-path It(...) to the same Describe.
var _ = Describe("KubeVirt provider data path", Ordered, Label("kubevirt-e2e"), func() {
	const (
		// The VM, VRouterTarget and VRouterConfig all live here. The KubeVirt
		// provider resolves kubevirt.namespace to the VRouterTarget's own
		// namespace when omitted; keeping everything in one namespace also
		// satisfies the same-namespace targetRef webhook rule.
		kvNamespace = "default"
		vmName      = "vrouter-e2e-kubevirt"
		targetName  = "vrouter-e2e-kubevirt"
		configName  = "vrouter-e2e-kubevirt-config"
		// failConfigName is the E3 failure-path VRouterConfig. It targets the
		// same VRouterTarget/VM as the happy path but applies a config that
		// fails to commit in-guest; it is created and cleaned up inside the
		// failure-path It so it never perturbs the happy-path config CR.
		failConfigName = "vrouter-e2e-kubevirt-fail"

		// appliedHostname is the deterministic system host-name the config sets.
		// It is observable inside the guest: after `set system host-name` is
		// committed, the guest's live hostname reflects it. Kept purely
		// alphanumeric to stay within the router's host-name validation.
		appliedHostname = "vre2ehost"
	)

	var (
		// routerContainerDisk is the containerDisk image booted as the guest
		// virtual router. A LATER CI item (the k3s workflow) is responsible for
		// building and importing this image into the node's containerd; this
		// spec only references it by tag.
		routerContainerDisk string
		// launcherPod is the virt-launcher pod hosting the VMI; QGA commands are
		// issued via `virsh qemu-agent-command` inside its `compute` container.
		launcherPod string
		// qgaDomain is the libvirt domain name virsh addresses: "<ns>_<vm>".
		qgaDomain string
	)

	BeforeAll(func() {
		if e2eCluster != "k3s" {
			Skip("KubeVirt provider e2e requires E2E_CLUSTER=k3s (real KubeVirt + KVM); skipping on the default Kind suite")
		}

		routerContainerDisk = os.Getenv("E2E_ROUTER_CONTAINERDISK")
		if routerContainerDisk == "" {
			routerContainerDisk = "dozenos-e2e:latest"
		}
		qgaDomain = fmt.Sprintf("%s_%s", kvNamespace, vmName)

		// This suite is designed to be run on its own (the k3s CI job focuses
		// --label=kubevirt-e2e), so it deploys the operator itself rather than
		// depending on the Kind-oriented "Manager" suite's ordering.
		deployOperator()

		launcherPod = bootRouterVM(kvNamespace, vmName, routerContainerDisk)
	})

	AfterAll(func() {
		if e2eCluster != "k3s" {
			return
		}
		By("deleting the VRouterConfig")
		_, _ = utils.Run(exec.Command(
			"kubectl", "-n", kvNamespace,
			"delete", "vrouterconfig", configName, "--ignore-not-found"))
		By("deleting the VRouterTarget")
		_, _ = utils.Run(exec.Command(
			"kubectl", "-n", kvNamespace,
			"delete", "vroutertarget", targetName, "--ignore-not-found"))
		By("deleting the VirtualMachine")
		_, _ = utils.Run(exec.Command("kubectl", "-n", kvNamespace, "delete", "vm", vmName, "--ignore-not-found"))
	})

	It("drives the guest to Applied/Ready and applies the config in-guest", func() {
		By("creating the VRouterTarget that identifies the VM by name")
		Expect(applyManifest(vrouterTargetManifest(targetName, kvNamespace, vmName))).
			To(Succeed(), "Failed to create the VRouterTarget")

		By("creating the VRouterConfig that sets a deterministic system host-name")
		Expect(applyManifest(vrouterConfigManifest(configName, kvNamespace, targetName, appliedHostname))).
			To(Succeed(), "Failed to create the VRouterConfig")

		By("waiting for the VRouterConfig Applied condition to become True and observedGeneration to catch up")
		Eventually(func(g Gomega) {
			generation := getJSONPath(g, configName, "{.metadata.generation}")
			observedGeneration := getJSONPath(g, configName, "{.status.observedGeneration}")
			appliedStatus := getJSONPath(g, configName,
				`{.status.conditions[?(@.type=="Applied")].status}`)

			g.Expect(observedGeneration).NotTo(BeEmpty(), "observedGeneration not set yet")
			g.Expect(observedGeneration).To(Equal(generation),
				"status.observedGeneration has not caught up with metadata.generation")
			g.Expect(appliedStatus).To(Equal("True"), "Applied condition is not True yet")
		}, applyTimeout, applyPoll).Should(Succeed())

		By("verifying the applied host-name is present in the guest via QGA")
		Eventually(func(g Gomega) {
			out, err := qgaGuestExec(kvNamespace, launcherPod, qgaDomain, "/bin/hostname")
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(strings.TrimSpace(out)).To(Equal(appliedHostname),
				"guest hostname does not reflect the applied config yet")
		}, applyTimeout, applyPoll).Should(Succeed())
	})

	// Item E3 appends the failure-path It(...) here, reusing the VM booted in
	// BeforeAll and the qgaGuestExec/getJSONPath helpers below.
	It("marks the config Failed with Applied=False on a commit failure and does not auto-retry", func() {
		By("creating a VRouterConfig whose commands pass `set` but fail at `commit` in-guest")
		Expect(applyManifest(vrouterFailingConfigManifest(failConfigName, kvNamespace, targetName))).
			To(Succeed(), "Failed to create the failing VRouterConfig")
		DeferCleanup(func() {
			_, _ = utils.Run(exec.Command(
				"kubectl", "-n", kvNamespace,
				"delete", "vrouterconfig", failConfigName, "--ignore-not-found"))
		})

		By("waiting for the VRouterConfig to reach phase=Failed with Applied=False")
		Eventually(func(g Gomega) {
			phase := getJSONPath(g, failConfigName, "{.status.phase}")
			appliedStatus := getJSONPath(g, failConfigName,
				`{.status.conditions[?(@.type=="Applied")].status}`)
			// Capture the live reason/message too so a timeout log shows the
			// config's ACTUAL observed state (e.g. it wrongly reached Applied)
			// instead of a bare "timed out" — the last poll's diag is what
			// Gomega prints on failure.
			appliedReason := getJSONPath(g, failConfigName,
				`{.status.conditions[?(@.type=="Applied")].reason}`)
			appliedMessage := getJSONPath(g, failConfigName,
				`{.status.conditions[?(@.type=="Applied")].message}`)
			diag := fmt.Sprintf(
				"last observed: phase=%q Applied.status=%q Applied.reason=%q Applied.message=%q",
				phase, appliedStatus, appliedReason, appliedMessage)
			g.Expect(phase).To(Equal("Failed"), "phase has not reached Failed yet; %s", diag)
			g.Expect(appliedStatus).To(Equal("False"), "Applied condition is not False; %s", diag)
		}, failTimeout, applyPoll).Should(Succeed())

		By("recording the observedGeneration stamped for the failed dispatch")
		var failedObservedGeneration string
		Eventually(func(g Gomega) {
			failedObservedGeneration = getJSONPath(g, failConfigName, "{.status.observedGeneration}")
			g.Expect(failedObservedGeneration).NotTo(BeEmpty(), "observedGeneration not set yet")
		}).Should(Succeed())

		// Per SPEC §7.2, a Failed config has NO auto-retry: the controller only
		// re-dispatches on a new generation or a fresh target reboot, neither of
		// which happens here. A retry would flip phase back to Applying and set a
		// fresh execPID; on failure execPID is cleared (empty) and
		// observedGeneration is left at the generation the exec was dispatched
		// for. So the invariant of "no retry" is: phase stays Failed, execPID
		// stays empty, observedGeneration does not advance.
		By("verifying the operator does not auto-retry the Failed config over a stable window")
		Consistently(func(g Gomega) {
			phase := getJSONPath(g, failConfigName, "{.status.phase}")
			execPID := getJSONPath(g, failConfigName, "{.status.execPID}")
			observedGeneration := getJSONPath(g, failConfigName, "{.status.observedGeneration}")
			g.Expect(phase).To(Equal("Failed"), "phase left Failed — operator re-dispatched")
			g.Expect(execPID).To(BeEmpty(), "execPID re-populated — operator re-dispatched an exec")
			g.Expect(observedGeneration).To(Equal(failedObservedGeneration),
				"observedGeneration advanced — operator re-dispatched")
		}, 45*time.Second, 5*time.Second).Should(Succeed())
	})
})

// deployOperator installs the CRDs and deploys the controller-manager, then
// waits for its Deployment to report condition=Available. Shared by every
// k3s-only e2e suite's BeforeAll (kubevirt-e2e and rollout-e2e): each suite is
// designed to run standalone (the k3s CI job focuses one ginkgo label at a
// time), so each deploys the operator itself rather than depending on the
// Kind-oriented "Manager" suite's ordering. make install / make deploy are
// idempotent `kubectl apply`s (make deploy also creates the operator
// namespace), so calling this from more than one suite in the same run is
// safe.
func deployOperator() {
	By("installing CRDs")
	_, err := utils.Run(exec.Command("make", "install"))
	Expect(err).NotTo(HaveOccurred(), "Failed to install CRDs")

	By("deploying the controller-manager")
	_, err = utils.Run(exec.Command("make", "deploy", fmt.Sprintf("IMG=%s", projectImage)))
	Expect(err).NotTo(HaveOccurred(), "Failed to deploy the controller-manager")

	By("waiting for the controller-manager deployment to be Available")
	Eventually(func(g Gomega) {
		cmd := exec.Command("kubectl", "wait",
			"deployment.apps/vrouter-operator-controller-manager",
			"--for", "condition=Available",
			"--namespace", namespace,
			"--timeout", "60s",
		)
		_, err := utils.Run(cmd)
		g.Expect(err).NotTo(HaveOccurred())
	}, 5*time.Minute, 10*time.Second).Should(Succeed())
}

// bootRouterVM creates a VirtualMachine named vmName (booting image) in ns,
// waits for it to reach condition=Ready and its VMI to reach
// condition=AgentConnected, then locates and returns its virt-launcher pod
// name for QGA use. Shared by kubevirt-e2e's single-VM BeforeAll,
// rollout-e2e's two-VM BeforeAll, and rollout-e2e's delete+recreate
// reboot-re-dispatch spec, so the (generous, hardware-KVM-sized) boot budget
// and polling behavior can never drift between callers.
func bootRouterVM(ns, vmName, image string) string { //nolint:unparam // ns kept explicit for reuse
	By(fmt.Sprintf("creating the guest virtual-router VirtualMachine %s", vmName))
	Expect(applyManifest(virtualMachineManifest(vmName, ns, image))).
		To(Succeed(), fmt.Sprintf("Failed to create the VirtualMachine %s", vmName))

	By(fmt.Sprintf("waiting for %s to become Ready (VMI created + running)", vmName))
	Eventually(func(g Gomega) {
		cmd := exec.Command("kubectl", "-n", ns, "wait",
			fmt.Sprintf("vm/%s", vmName),
			"--for", "condition=Ready",
			"--timeout", "60s",
		)
		_, err := utils.Run(cmd)
		g.Expect(err).NotTo(HaveOccurred())
	}, vmReadyTimeout, 15*time.Second).Should(Succeed())

	By(fmt.Sprintf("waiting for the qemu-guest-agent to connect on %s (VMI AgentConnected)", vmName))
	Eventually(func(g Gomega) {
		cmd := exec.Command("kubectl", "-n", ns, "wait",
			fmt.Sprintf("vmi/%s", vmName),
			"--for", "condition=AgentConnected",
			"--timeout", "60s",
		)
		_, err := utils.Run(cmd)
		g.Expect(err).NotTo(HaveOccurred())
	}, vmReadyTimeout, 15*time.Second).Should(Succeed())

	var launcherPod string
	By(fmt.Sprintf("locating the virt-launcher pod for %s (QGA verification)", vmName))
	Eventually(func(g Gomega) {
		cmd := exec.Command("kubectl", "-n", ns, "get", "pod",
			"-l", fmt.Sprintf("vm.kubevirt.io/name=%s", vmName),
			"-o", "jsonpath={.items[0].metadata.name}",
		)
		out, err := utils.Run(cmd)
		g.Expect(err).NotTo(HaveOccurred())
		launcherPod = strings.TrimSpace(out)
		g.Expect(launcherPod).NotTo(BeEmpty(), "virt-launcher pod not found yet")
	}).Should(Succeed())
	return launcherPod
}

// virtualMachineManifest renders a KubeVirt VirtualMachine (runStrategy=Always)
// booting the given containerDisk image as the guest virtual router. A real VM
// (not a bare VMI) so it exercises the same object the operator targets by name.
func virtualMachineManifest(name, ns, image string) string {
	return fmt.Sprintf(`apiVersion: kubevirt.io/v1
kind: VirtualMachine
metadata:
  name: %[1]s
  namespace: %[2]s
spec:
  runStrategy: Always
  template:
    metadata:
      labels:
        kubevirt.io/vm: %[1]s
    spec:
      terminationGracePeriodSeconds: 0
      domain:
        cpu:
          cores: 2
        machine:
          type: q35
        resources:
          requests:
            memory: 1Gi
        devices:
          disks:
            - name: containerdisk
              disk:
                bus: virtio
      volumes:
        - name: containerdisk
          containerDisk:
            image: %[3]s
            imagePullPolicy: IfNotPresent
`, name, ns, image)
}

// vrouterTargetManifest renders a VRouterTarget whose KubeVirt ProviderConfig
// identifies the VM by name (spec.provider.kubevirt.name, per SPEC §9.6).
func vrouterTargetManifest(name, ns, vmName string) string {
	return fmt.Sprintf(`apiVersion: vrouter.kojuro.date/v1
kind: VRouterTarget
metadata:
  name: %[1]s
  namespace: %[2]s
spec:
  provider:
    type: kubevirt
    kubevirt:
      name: %[3]s
      namespace: %[2]s
`, name, ns, vmName)
}

// vrouterTargetWithParamsManifest renders a VRouterTarget like
// vrouterTargetManifest, plus spec.params.hostname — the highest-priority
// params layer (SPEC §7.1/§4.2), for use with a VRouterBinding whose template
// renders `{{ .hostname }}` (vrouterTemplateManifest below). Kept as a
// separate helper rather than changing vrouterTargetManifest's signature, so
// existing kubevirt-e2e callers are unaffected.
//
//nolint:unparam // ns mirrors vrouterTargetManifest's signature
func vrouterTargetWithParamsManifest(name, ns, vmName, hostname string) string {
	return fmt.Sprintf(`apiVersion: vrouter.kojuro.date/v1
kind: VRouterTarget
metadata:
  name: %[1]s
  namespace: %[2]s
spec:
  provider:
    type: kubevirt
    kubevirt:
      name: %[3]s
      namespace: %[2]s
  params:
    hostname: "%[4]s"
`, name, ns, vmName, hostname)
}

// vrouterTemplateManifest renders a VRouterTemplate whose commands render a
// system host-name from a `hostname` param — the binding-driven counterpart
// to vrouterConfigManifest's direct `commands`, used by the rollout-e2e specs
// (test/e2e/rollout_e2e_test.go) together with vrouterTargetWithParamsManifest
// and vrouterBindingManifest.
func vrouterTemplateManifest(name, ns string) string {
	return fmt.Sprintf(`apiVersion: vrouter.kojuro.date/v1
kind: VRouterTemplate
metadata:
  name: %[1]s
  namespace: %[2]s
spec:
  commands: |
    set system host-name '{{ .hostname }}'
`, name, ns)
}

// vrouterBindingManifest renders a VRouterBinding referencing templateName
// (spec.templateRef) and targetNames in order (spec.targetRefs). rollout, if
// non-empty, is a pre-indented YAML fragment starting with "  rollout:\n"
// that is inserted verbatim after targetRefs — letting callers express any of
// the three rollout modes (SPEC §7.1) without this helper needing mode-
// specific parameters. An empty rollout omits spec.rollout entirely, i.e.
// RolloutModeDisabled (the zero value). See rolloutWaitForAppliedFragment and
// rolloutFixedIntervalFragment for the two thin wrappers rollout-e2e uses.
func vrouterBindingManifest(name, ns, templateName string, targetNames []string, rollout string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "apiVersion: vrouter.kojuro.date/v1\n")
	fmt.Fprintf(&b, "kind: VRouterBinding\n")
	fmt.Fprintf(&b, "metadata:\n  name: %s\n  namespace: %s\n", name, ns)
	fmt.Fprintf(&b, "spec:\n")
	fmt.Fprintf(&b, "  templateRef:\n    name: %s\n", templateName)
	fmt.Fprintf(&b, "  targetRefs:\n")
	for _, t := range targetNames {
		fmt.Fprintf(&b, "    - name: %s\n", t)
	}
	b.WriteString(rollout)
	return b.String()
}

// rolloutWaitForAppliedFragment renders the spec.rollout YAML fragment for
// mode: WaitForApplied (SPEC §7.1), for use with vrouterBindingManifest.
func rolloutWaitForAppliedFragment(pollInterval, waitAfterApplied string) string {
	return fmt.Sprintf("  rollout:\n    mode: WaitForApplied\n    pollInterval: %s\n    waitAfterApplied: %s\n",
		pollInterval, waitAfterApplied)
}

// rolloutFixedIntervalFragment renders the spec.rollout YAML fragment for
// mode: FixedInterval (SPEC §7.1), for use with vrouterBindingManifest.
func rolloutFixedIntervalFragment(waitInterval string) string {
	return fmt.Sprintf("  rollout:\n    mode: FixedInterval\n    waitInterval: %s\n", waitInterval)
}

// vrouterConfigManifest renders a VRouterConfig that sets a deterministic system
// host-name via the commands field (mirroring config/samples). The commands
// block is injected into the provider's internal vbash template (SPEC §7.4),
// committed (and saved) in-guest; the resulting live hostname is what the QGA
// check asserts.
func vrouterConfigManifest(name, ns, targetName, hostname string) string {
	return fmt.Sprintf(`apiVersion: vrouter.kojuro.date/v1
kind: VRouterConfig
metadata:
  name: %[1]s
  namespace: %[2]s
spec:
  targetRef:
    name: %[3]s
  commands: |
    set system host-name '%[4]s'
`, name, ns, targetName, hostname)
}

// vrouterFailingConfigManifest renders a VRouterConfig that is accepted by the
// K8s schema/webhook but fails inside the guest, driving the config to
// phase=Failed / Applied=False (SPEC §7.2).
//
// The command assigns an invalid value ('not-an-ip') to eth0's interface address.
// The router's config validator rejects this at SET time — configure mode prints
// "Value validation failed / Set failed" and the `set` returns non-zero. The apply
// template (SPEC §7.4) has NO `set -e`, so the script keeps running through to
// `commit`. Crucially, because a preceding `load` already left a committable diff,
// `commit` still exits 0 — the guest exit code MASKS the set-time failure. This
// exit-0-on-set-failure was confirmed empirically by running this exact config
// through the real guest apply path in CI. The operator therefore detects the
// failure by scanning the apply output for the "Set failed" marker (SPEC §7.4) and
// reports phase=Failed / Applied=False even though the exit code is 0.
//
// save is false on purpose: it keeps the apply path minimal (no post-commit
// `save` step) so the failure surfaces purely as the "Set failed" marker in the
// configure-mode output that the operator scans for.
func vrouterFailingConfigManifest(name, ns, targetName string) string {
	return fmt.Sprintf(`apiVersion: vrouter.kojuro.date/v1
kind: VRouterConfig
metadata:
  name: %[1]s
  namespace: %[2]s
spec:
  targetRef:
    name: %[3]s
  save: false
  commands: |
    set interfaces ethernet eth0 address 'not-an-ip'
`, name, ns, targetName)
}

// applyManifest pipes a rendered manifest into `kubectl apply -f -`.
func applyManifest(manifest string) error {
	cmd := exec.Command("kubectl", "apply", "-f", "-")
	cmd.Stdin = strings.NewReader(manifest)
	_, err := utils.Run(cmd)
	return err
}

// getJSONPath reads a jsonpath expression off the named VRouterConfig, failing
// the enclosing Eventually block (via the supplied Gomega) on error.
func getJSONPath(g Gomega, configName, jsonpath string) string {
	cmd := exec.Command("kubectl", "-n", "default", "get", "vrouterconfig", configName,
		"-o", fmt.Sprintf("jsonpath=%s", jsonpath))
	out, err := utils.Run(cmd)
	g.Expect(err).NotTo(HaveOccurred())
	return strings.TrimSpace(out)
}

// getJSONPathTolerant behaves like getJSONPath but returns "" instead of
// failing the enclosing Eventually/Consistently block when the named
// VRouterConfig does not exist (yet, or ever) — used by rollout-e2e's
// Consistently windows that sample a downstream target's generated config
// before/while asserting the rollout walk has not written it (SPEC §7.1's
// "at most one target's VRouterConfig is being written" invariant).
// --ignore-not-found makes kubectl exit 0 with empty output for a missing
// object instead of erroring, so a genuine kubectl failure (bad flag,
// unreachable apiserver) still fails the assertion via g.Expect(err).
func getJSONPathTolerant(g Gomega, configName, jsonpath string) string {
	cmd := exec.Command("kubectl", "-n", "default", "get", "vrouterconfig", configName,
		"--ignore-not-found", "-o", fmt.Sprintf("jsonpath=%s", jsonpath))
	out, err := utils.Run(cmd)
	g.Expect(err).NotTo(HaveOccurred())
	return strings.TrimSpace(out)
}

// getBindingField reads a jsonpath expression off the named VRouterBinding,
// failing the enclosing Eventually/Consistently block (via the supplied
// Gomega) on error. Sibling of getJSONPath, which reads VRouterConfigs.
func getBindingField(g Gomega, name, jsonpath string) string {
	cmd := exec.Command("kubectl", "-n", "default", "get", "vrouterbinding", name,
		"-o", fmt.Sprintf("jsonpath=%s", jsonpath))
	out, err := utils.Run(cmd)
	g.Expect(err).NotTo(HaveOccurred())
	return strings.TrimSpace(out)
}

// guestExecCommand is a qemu-guest-agent guest-exec request.
type guestExecCommand struct {
	Execute   string             `json:"execute"`
	Arguments guestExecArguments `json:"arguments"`
}

type guestExecArguments struct {
	Path          string   `json:"path"`
	Arg           []string `json:"arg,omitempty"`
	CaptureOutput bool     `json:"capture-output"`
}

// qgaGuestExec runs a command inside the guest via QGA and returns its decoded
// stdout. It mirrors the operator's own runQGA: dispatch guest-exec inside the
// virt-launcher `compute` container with virsh, poll guest-exec-status until the
// process exits, then base64-decode out-data.
func qgaGuestExec(ns, launcher, domain, path string, args ...string) (string, error) {
	payload, err := json.Marshal(guestExecCommand{
		Execute: "guest-exec",
		Arguments: guestExecArguments{
			Path:          path,
			Arg:           args,
			CaptureOutput: true,
		},
	})
	if err != nil {
		return "", fmt.Errorf("marshal guest-exec command: %w", err)
	}

	resp, err := virshQGA(ns, launcher, domain, string(payload))
	if err != nil {
		return "", fmt.Errorf("guest-exec dispatch: %w", err)
	}
	var pidResp struct {
		Return struct {
			Pid int64 `json:"pid"`
		} `json:"return"`
	}
	if err := json.Unmarshal([]byte(resp), &pidResp); err != nil {
		return "", fmt.Errorf("parse guest-exec pid from %q: %w", resp, err)
	}
	if pidResp.Return.Pid == 0 {
		return "", fmt.Errorf("no pid in guest-exec response: %s", resp)
	}

	for range 120 {
		statusPayload := fmt.Sprintf(`{"execute":"guest-exec-status","arguments":{"pid":%d}}`, pidResp.Return.Pid)
		statusResp, err := virshQGA(ns, launcher, domain, statusPayload)
		if err != nil {
			return "", fmt.Errorf("guest-exec-status: %w", err)
		}
		var status struct {
			Return struct {
				Exited   bool   `json:"exited"`
				ExitCode int    `json:"exitcode"`
				OutData  string `json:"out-data"`
				ErrData  string `json:"err-data"`
			} `json:"return"`
		}
		if err := json.Unmarshal([]byte(statusResp), &status); err != nil {
			return "", fmt.Errorf("parse guest-exec-status from %q: %w", statusResp, err)
		}
		if status.Return.Exited {
			if status.Return.ExitCode != 0 {
				stderr, _ := base64.StdEncoding.DecodeString(status.Return.ErrData)
				return "", fmt.Errorf("guest-exec %s exited non-zero (exitCode=%d stderr=%s)",
					path, status.Return.ExitCode, strings.TrimSpace(string(stderr)))
			}
			if status.Return.OutData == "" {
				return "", nil
			}
			decoded, err := base64.StdEncoding.DecodeString(status.Return.OutData)
			if err != nil {
				return "", fmt.Errorf("decode guest-exec out-data: %w", err)
			}
			return string(decoded), nil
		}
		time.Sleep(time.Second)
	}
	return "", fmt.Errorf("guest-exec %s timed out waiting for exit", path)
}

// virshQGA issues a raw qemu-guest-agent JSON command via `virsh
// qemu-agent-command` inside the virt-launcher `compute` container — exactly how
// the operator's KubeVirt provider talks to the guest.
func virshQGA(ns, launcher, domain, agentJSON string) (string, error) {
	cmd := exec.Command("kubectl", "-n", ns, "exec", launcher, "-c", "compute", "--",
		"virsh", "qemu-agent-command", domain, agentJSON)
	return utils.Run(cmd)
}
