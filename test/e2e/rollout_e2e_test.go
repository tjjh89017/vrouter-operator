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
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/tjjh89017/vrouter-operator/test/utils"
)

// VRouterBinding serial rollout (spec.rollout / status.rollout, PR #40) e2e,
// exercising the BindingController rollout walk against two real KubeVirt
// guest VMs on a live k3s+KubeVirt cluster. PR #40 shipped this feature with
// UNIT tests only (internal/controller/vrouterbinding_controller.go and its
// _test.go); this suite is the missing real-data-path coverage, sibling to
// kubevirt_e2e_test.go's single-VM provider suite.
//
// Two VMs (vm-a, vm-b) are booted once in BeforeAll and shared read-mostly
// across the four specs below; each spec creates and DeferCleanup-removes its
// own VRouterTemplate/VRouterTarget(s)/VRouterBinding/generated VRouterConfigs
// so the specs never leak state into each other, while paying the ~10m VM
// boot cost only once for the whole suite (Ordered, like kubevirt_e2e_test.go).
//
// Guard: skipped unless E2E_CLUSTER=k3s, same rationale as kubevirt_e2e_test.go
// — this needs a real KubeVirt + KVM node, never the default Kind suite.
var _ = Describe("VRouterBinding serial rollout", Ordered, Label("rollout-e2e"), func() {
	const (
		// Both VMs and every generated CR for this suite live in the default
		// namespace — same rationale as kubevirt_e2e_test.go's kvNamespace:
		// the KubeVirt provider resolves kubevirt.namespace to the
		// VRouterTarget's own namespace when omitted, and the validating
		// webhook requires same-namespace targetRefs.
		kvNamespace = "default"
		vmNameA     = "rollout-e2e-vm-a"
		vmNameB     = "rollout-e2e-vm-b"

		// Spec 3: WaitForApplied serial happy path.
		happyTemplateName = "rollout-e2e-happy-template"
		happyTargetAName  = "rollout-e2e-happy-target-a"
		happyTargetBName  = "rollout-e2e-happy-target-b"
		happyBindingName  = "rollout-e2e-happy-binding"
		happyHostnameA    = "rollouta"
		happyHostnameB    = "rolloutb"

		// Spec 4: WaitForApplied halt + resume. The template sets eth0's
		// address (not hostname) from a param, mirroring
		// vrouterFailingConfigManifest's proven set-time-failure pattern
		// (kubevirt_e2e_test.go) rather than reusing the hostname template, so
		// this spec never touches the hostname the other specs assert on.
		haltTemplateName = "rollout-e2e-halt-template"
		haltTargetAName  = "rollout-e2e-halt-target-a"
		haltTargetBName  = "rollout-e2e-halt-target-b"
		haltBindingName  = "rollout-e2e-halt-binding"
		// haltInvalidAddr trips the same "set-time validation failure that
		// still lets commit exit 0" behavior vrouterFailingConfigManifest
		// documents and that CI has verified empirically for this exact guest
		// image (kubevirt_e2e_test.go). UNCERTAIN: this test does not attempt
		// a different invalid value (e.g. a malformed host-name); 'not-an-ip'
		// is reused here specifically because it is the one value already
		// proven, in this repo's own CI, to trip the router's "Set failed"
		// marker without aborting the script.
		haltInvalidAddr = "not-an-ip"
		haltValidAddr   = "192.0.2.10/24"

		// Spec 5: FixedInterval stagger.
		fixedTemplateName = "rollout-e2e-fixed-template"
		fixedTargetAName  = "rollout-e2e-fixed-target-a"
		fixedTargetBName  = "rollout-e2e-fixed-target-b"
		fixedBindingName  = "rollout-e2e-fixed-binding"
		fixedHostnameA    = "fixeda"
		fixedHostnameB    = "fixedb"
		// fixedWaitInterval is spec 5's spec.rollout.waitInterval.
		// fixedIntervalTolerance is how far short of fixedWaitInterval the
		// observed frontier-advance delta is allowed to fall and still pass.
		// UNCERTAIN: this tolerance is a guess — it has not been validated
		// against an actual k3s+KubeVirt CI timing distribution, only reasoned
		// about from the reconcile/requeue latencies documented in SPEC §7.1.
		// If CI shows flakes here, widen it rather than the wait interval.
		fixedWaitInterval      = 60 * time.Second
		fixedIntervalTolerance = 5 * time.Second

		// Spec 6: VM delete+recreate self-heal — a direct (non-binding)
		// VRouterConfig against vm-a. See the comment on that It for why a
		// fresh KubeVirt boot re-applies via the CheckReady-not-ready reset
		// (not SPEC §7.2's reboot-forced path, which is Proxmox-only).
		rebootTargetName = "rollout-e2e-reboot-target"
		rebootConfigName = "rollout-e2e-reboot-config"
		rebootHostname   = "rebootself"
	)

	var (
		// routerContainerDisk is the containerDisk image booted as both guest
		// virtual routers — see kubevirt_e2e_test.go's identically-named var.
		routerContainerDisk string
		// launcherPodA/B are the virt-launcher pods hosting vm-a/vm-b's VMIs;
		// launcherPodA is reassigned by the reboot-re-dispatch spec once vm-a
		// is recreated (a fresh VM gets a fresh virt-launcher pod).
		launcherPodA, launcherPodB string
		// qgaDomainA/B are the libvirt domain names virsh addresses:
		// "<ns>_<vm>" — stable across vm-a's delete+recreate since the VM name
		// is reused.
		qgaDomainA, qgaDomainB string
	)

	BeforeAll(func() {
		if e2eCluster != "k3s" {
			Skip("rollout-e2e requires E2E_CLUSTER=k3s (real KubeVirt + KVM); skipping on the default Kind suite")
		}

		routerContainerDisk = os.Getenv("E2E_ROUTER_CONTAINERDISK")
		if routerContainerDisk == "" {
			routerContainerDisk = "dozenos-e2e:latest"
		}
		qgaDomainA = fmt.Sprintf("%s_%s", kvNamespace, vmNameA)
		qgaDomainB = fmt.Sprintf("%s_%s", kvNamespace, vmNameB)

		// This suite is designed to run standalone (the k3s CI job focuses
		// "kubevirt-e2e || rollout-e2e" as a whole, but nothing here depends on
		// spec execution order across files), so it deploys the operator
		// itself, exactly like kubevirt_e2e_test.go's BeforeAll. make install /
		// make deploy are idempotent kubectl applies, so this is harmless even
		// if the kubevirt-e2e spec already deployed the operator in the same
		// job run.
		deployOperator()

		launcherPodA = bootRouterVM(kvNamespace, vmNameA, routerContainerDisk)
		launcherPodB = bootRouterVM(kvNamespace, vmNameB, routerContainerDisk)
	})

	AfterAll(func() {
		if e2eCluster != "k3s" {
			return
		}
		By("sweeping up any rollout-e2e bindings/templates/targets/configs left behind")
		// Primary cleanup happens per-spec via DeferCleanup below; this is a
		// belt-and-suspenders sweep so a spec that panics before registering
		// its DeferCleanup can't leak CRs past the suite.
		deleteBindingResources(kvNamespace, happyBindingName, happyTemplateName,
			[]string{happyTargetAName, happyTargetBName})
		deleteBindingResources(kvNamespace, haltBindingName, haltTemplateName,
			[]string{haltTargetAName, haltTargetBName})
		deleteBindingResources(kvNamespace, fixedBindingName, fixedTemplateName,
			[]string{fixedTargetAName, fixedTargetBName})
		_, _ = utils.Run(exec.Command(
			"kubectl", "-n", kvNamespace, "delete", "vrouterconfig", rebootConfigName, "--ignore-not-found"))
		_, _ = utils.Run(exec.Command(
			"kubectl", "-n", kvNamespace, "delete", "vroutertarget", rebootTargetName, "--ignore-not-found"))

		By("deleting vm-a and vm-b")
		_, _ = utils.Run(exec.Command("kubectl", "-n", kvNamespace, "delete", "vm", vmNameA, "--ignore-not-found"))
		_, _ = utils.Run(exec.Command("kubectl", "-n", kvNamespace, "delete", "vm", vmNameB, "--ignore-not-found"))
	})

	// Item 3: WaitForApplied serial happy path.
	It("rolls out serially under mode: WaitForApplied and converges with both hostnames applied", func() {
		cfgA := fmt.Sprintf("%s.%s", happyBindingName, happyTargetAName)
		cfgB := fmt.Sprintf("%s.%s", happyBindingName, happyTargetBName)

		DeferCleanup(func() {
			deleteBindingResources(kvNamespace, happyBindingName, happyTemplateName,
				[]string{happyTargetAName, happyTargetBName})
		})

		By("creating the template, two targets (hostA/hostB), and a WaitForApplied binding over both")
		Expect(applyManifest(vrouterTemplateManifest(happyTemplateName, kvNamespace))).
			To(Succeed(), "Failed to create the VRouterTemplate")
		Expect(applyManifest(vrouterTargetWithParamsManifest(happyTargetAName, kvNamespace, vmNameA, happyHostnameA))).
			To(Succeed(), "Failed to create target-a")
		Expect(applyManifest(vrouterTargetWithParamsManifest(happyTargetBName, kvNamespace, vmNameB, happyHostnameB))).
			To(Succeed(), "Failed to create target-b")
		Expect(applyManifest(vrouterBindingManifest(happyBindingName, kvNamespace, happyTemplateName,
			[]string{happyTargetAName, happyTargetBName},
			rolloutWaitForAppliedFragment("10s", "0s")))).
			To(Succeed(), "Failed to create the binding")

		// Single Consistently window proving three invariants at once, sampled
		// every 2s across the whole rollout: (a) SPEC §7.1's "at most one
		// target's VRouterConfig is being written" — approximated here as "at
		// most one generated config is Applying" since Applying is the
		// observable window a write is in flight; (b) the frontier never
		// advances to target-b while target-a is not yet Applied=True; and (c)
		// along the way, the binding did pass through Ready=False/
		// RolloutInProgress "1/2 targets updated" (captured into
		// sawProgressMessage) rather than jumping straight to done.
		By("watching the rollout: at most one config Applying at once, frontier never outruns target-a's Applied status")
		var sawProgressMessage bool
		Consistently(func(g Gomega) {
			phaseA := getJSONPathTolerant(g, cfgA, "{.status.phase}")
			phaseB := getJSONPathTolerant(g, cfgB, "{.status.phase}")
			applyingCount := 0
			if phaseA == "Applying" {
				applyingCount++
			}
			if phaseB == "Applying" {
				applyingCount++
			}
			g.Expect(applyingCount).To(BeNumerically("<=", 1),
				"more than one generated config Applying at once: phaseA=%q phaseB=%q", phaseA, phaseB)

			frontier := getBindingField(g, happyBindingName, "{.status.rollout.lastUpdatedTarget}")
			if frontier == happyTargetBName {
				appliedA := getJSONPath(g, cfgA, `{.status.conditions[?(@.type=="Applied")].status}`)
				g.Expect(appliedA).To(Equal("True"),
					"rollout frontier advanced to target-b while target-a's config is not yet Applied=True")
			}

			readyMsg := getBindingField(g, happyBindingName, `{.status.conditions[?(@.type=="Ready")].message}`)
			if readyMsg == "1/2 targets updated" {
				sawProgressMessage = true
			}
		}, 6*time.Minute, 2*time.Second).Should(Succeed())
		Expect(sawProgressMessage).To(BeTrue(),
			`never observed the binding Ready message "1/2 targets updated" during the rollout`)

		By("verifying binding Ready settles to True/ReconcileSucceeded once both configs are applied")
		Eventually(func(g Gomega) {
			status := getBindingField(g, happyBindingName, `{.status.conditions[?(@.type=="Ready")].status}`)
			reason := getBindingField(g, happyBindingName, `{.status.conditions[?(@.type=="Ready")].reason}`)
			message := getBindingField(g, happyBindingName, `{.status.conditions[?(@.type=="Ready")].message}`)
			g.Expect(status).To(Equal("True"), "Ready reason=%q message=%q", reason, message)
			g.Expect(reason).To(Equal("ReconcileSucceeded"))
			g.Expect(message).To(Equal("All 2 VRouterConfigs applied."))
		}, applyTimeout, applyPoll).Should(Succeed())

		By("verifying both guests reflect their applied hostnames via QGA")
		expectGuestHostname(kvNamespace, launcherPodA, qgaDomainA, happyHostnameA)
		expectGuestHostname(kvNamespace, launcherPodB, qgaDomainB, happyHostnameB)
	})

	// Item 4: WaitForApplied halt + resume.
	It("halts a WaitForApplied rollout on a Failed config and resumes once it is fixed", func() {
		cfgA := fmt.Sprintf("%s.%s", haltBindingName, haltTargetAName)
		cfgB := fmt.Sprintf("%s.%s", haltBindingName, haltTargetBName)

		DeferCleanup(func() {
			deleteBindingResources(kvNamespace, haltBindingName, haltTemplateName,
				[]string{haltTargetAName, haltTargetBName})
		})

		By("creating a template that sets eth0's address from a param, target-a with an INVALID address, target-b valid")
		Expect(applyManifest(addressTemplateManifest(haltTemplateName, kvNamespace))).
			To(Succeed(), "Failed to create the VRouterTemplate")
		Expect(applyManifest(targetWithAddrParamManifest(haltTargetAName, kvNamespace, vmNameA, haltInvalidAddr))).
			To(Succeed(), "Failed to create target-a")
		Expect(applyManifest(targetWithAddrParamManifest(haltTargetBName, kvNamespace, vmNameB, haltValidAddr))).
			To(Succeed(), "Failed to create target-b")

		By("creating the binding (rollout WaitForApplied, targetRefs [target-a, target-b])")
		Expect(applyManifest(vrouterBindingManifest(haltBindingName, kvNamespace, haltTemplateName,
			[]string{haltTargetAName, haltTargetBName},
			rolloutWaitForAppliedFragment("10s", "0s")))).
			To(Succeed(), "Failed to create the binding")

		By("waiting for the binding to halt: Ready=False/RolloutHalted naming target-a's generated config")
		Eventually(func(g Gomega) {
			status := getBindingField(g, haltBindingName, `{.status.conditions[?(@.type=="Ready")].status}`)
			reason := getBindingField(g, haltBindingName, `{.status.conditions[?(@.type=="Ready")].reason}`)
			message := getBindingField(g, haltBindingName, `{.status.conditions[?(@.type=="Ready")].message}`)
			g.Expect(status).To(Equal("False"), "Ready reason=%q message=%q", reason, message)
			g.Expect(reason).To(Equal("RolloutHalted"))
			g.Expect(message).To(ContainSubstring(cfgA))
		}, failTimeout, applyPoll).Should(Succeed())

		By("verifying target-b's generated config is never written while the rollout is halted")
		Consistently(func(g Gomega) {
			name := getJSONPathTolerant(g, cfgB, "{.metadata.name}")
			g.Expect(name).To(BeEmpty(), "downstream VRouterConfig %q was written while the rollout is halted", cfgB)
		}, 45*time.Second, 5*time.Second).Should(Succeed())

		By("resuming: patching target-a's addr param to a valid value")
		Expect(applyManifest(targetWithAddrParamManifest(haltTargetAName, kvNamespace, vmNameA, haltValidAddr))).
			To(Succeed(), "Failed to patch target-a")

		By("verifying the rollout resumes: both generated configs reach Applied")
		Eventually(func(g Gomega) {
			appliedA := getJSONPath(g, cfgA, `{.status.conditions[?(@.type=="Applied")].status}`)
			appliedB := getJSONPath(g, cfgB, `{.status.conditions[?(@.type=="Applied")].status}`)
			g.Expect(appliedA).To(Equal("True"))
			g.Expect(appliedB).To(Equal("True"))
		}, applyTimeout, applyPoll).Should(Succeed())

		By("verifying the binding settles Ready=True/ReconcileSucceeded")
		Eventually(func(g Gomega) {
			status := getBindingField(g, haltBindingName, `{.status.conditions[?(@.type=="Ready")].status}`)
			reason := getBindingField(g, haltBindingName, `{.status.conditions[?(@.type=="Ready")].reason}`)
			g.Expect(status).To(Equal("True"), "Ready reason=%q", reason)
			g.Expect(reason).To(Equal("ReconcileSucceeded"))
		}, applyTimeout, applyPoll).Should(Succeed())
	})

	// Item 5: FixedInterval stagger.
	It("staggers writes by waitInterval under mode: FixedInterval", func() {
		DeferCleanup(func() {
			deleteBindingResources(kvNamespace, fixedBindingName, fixedTemplateName,
				[]string{fixedTargetAName, fixedTargetBName})
		})

		By(fmt.Sprintf("creating template + targets + FixedInterval binding (waitInterval=%s)", fixedWaitInterval))
		Expect(applyManifest(vrouterTemplateManifest(fixedTemplateName, kvNamespace))).
			To(Succeed(), "Failed to create the VRouterTemplate")
		Expect(applyManifest(vrouterTargetWithParamsManifest(fixedTargetAName, kvNamespace, vmNameA, fixedHostnameA))).
			To(Succeed(), "Failed to create target-a")
		Expect(applyManifest(vrouterTargetWithParamsManifest(fixedTargetBName, kvNamespace, vmNameB, fixedHostnameB))).
			To(Succeed(), "Failed to create target-b")
		Expect(applyManifest(vrouterBindingManifest(fixedBindingName, kvNamespace, fixedTemplateName,
			[]string{fixedTargetAName, fixedTargetBName},
			rolloutFixedIntervalFragment(fixedWaitInterval.String())))).
			To(Succeed(), "Failed to create the binding")

		By("waiting for the rollout frontier to reach target-a")
		var timeA string
		Eventually(func(g Gomega) {
			frontier := getBindingField(g, fixedBindingName, "{.status.rollout.lastUpdatedTarget}")
			g.Expect(frontier).To(Equal(fixedTargetAName))
			timeA = getBindingField(g, fixedBindingName, "{.status.rollout.lastUpdateTime}")
			g.Expect(timeA).NotTo(BeEmpty())
		}, applyTimeout, applyPoll).Should(Succeed())

		By("waiting for the rollout frontier to advance to target-b after waitInterval")
		var timeB string
		Eventually(func(g Gomega) {
			frontier := getBindingField(g, fixedBindingName, "{.status.rollout.lastUpdatedTarget}")
			g.Expect(frontier).To(Equal(fixedTargetBName))
			timeB = getBindingField(g, fixedBindingName, "{.status.rollout.lastUpdateTime}")
			g.Expect(timeB).NotTo(BeEmpty())
		}, applyTimeout, applyPoll).Should(Succeed())

		By("asserting the stagger honored waitInterval, allowing a small scheduling tolerance")
		tA, err := time.Parse(time.RFC3339, timeA)
		Expect(err).NotTo(HaveOccurred(), "failed to parse status.rollout.lastUpdateTime for target-a: %q", timeA)
		tB, err := time.Parse(time.RFC3339, timeB)
		Expect(err).NotTo(HaveOccurred(), "failed to parse status.rollout.lastUpdateTime for target-b: %q", timeB)
		delta := tB.Sub(tA)
		Expect(delta).To(BeNumerically(">=", fixedWaitInterval-fixedIntervalTolerance),
			"frontier advanced from target-a to target-b after only %s, less than waitInterval %s minus tolerance %s",
			delta, fixedWaitInterval, fixedIntervalTolerance)

		By("verifying both hostnames eventually land in-guest")
		expectGuestHostname(kvNamespace, launcherPodA, qgaDomainA, fixedHostnameA)
		expectGuestHostname(kvNamespace, launcherPodB, qgaDomainB, fixedHostnameB)
	})

	// Item 6: VM delete+recreate self-heal — after a KubeVirt guest is torn
	// down and rebooted from its stateless containerDisk, the operator
	// re-applies the previously-Applied config to the fresh guest.
	//
	// WHY this re-applies WITHOUT KubeVirt reboot detection (verified against
	// the controller source, not assumed): SPEC §7.2's reboot-forced re-apply
	// is driven by VRouterTarget.Status.LastRebootTime, which only
	// ProxmoxClusterController ever writes — so shouldForceReapplyForReboot is
	// always false for a KubeVirt target
	// (internal/controller/vrouterconfig_controller.go:204). Re-apply instead
	// comes from the CheckReady reset path:
	//  1. Recreating the VM fires a VMI event that re-enqueues this config (the
	//     VirtualMachineInstance watch, vrouterconfig_controller.go:517).
	//  2. KubeVirt IsVMRunning returns true as soon as the VMI is in a running
	//     phase — well BEFORE the guest OS/QGA is up (kubevirt/provider.go's
	//     IsVMRunning only excludes Succeeded/Failed) — so onChange does not
	//     short-circuit; it proceeds to CheckReady.
	//  3. CheckReady transiently FAILS on the still-booting guest (QGA not
	//     responding, or the router unit not yet "exited"), and
	//     handleRouterNotReady resets a previously-Applied config to
	//     phase=Pending / Applied=False,RouterNotReady
	//     (vrouterconfig_controller.go:257), re-opening the generation gate in
	//     dispatchOrPoll so the next ready reconcile re-dispatches the apply.
	// The containerDisk is stateless, so the recreated guest boots WITHOUT the
	// applied host-name; observing it reappear is therefore proof the operator
	// re-applied it, not residue from before. (Delete+recreate is the spec that
	// exercises this real reboot-recovery path end to end.)
	It("re-applies a previously-Applied config to a freshly recreated guest VM", func() {
		DeferCleanup(func() {
			_, _ = utils.Run(exec.Command(
				"kubectl", "-n", kvNamespace, "delete", "vrouterconfig", rebootConfigName, "--ignore-not-found"))
			_, _ = utils.Run(exec.Command(
				"kubectl", "-n", kvNamespace, "delete", "vroutertarget", rebootTargetName, "--ignore-not-found"))
		})

		By("creating a direct VRouterTarget + VRouterConfig against vm-a")
		Expect(applyManifest(vrouterTargetManifest(rebootTargetName, kvNamespace, vmNameA))).
			To(Succeed(), "Failed to create the VRouterTarget")
		Expect(applyManifest(vrouterConfigManifest(rebootConfigName, kvNamespace, rebootTargetName, rebootHostname))).
			To(Succeed(), "Failed to create the VRouterConfig")

		By("waiting for the config to reach Applied and recording the Applied transition time")
		var appliedTransitionBefore time.Time
		Eventually(func(g Gomega) {
			appliedStatus := getJSONPath(g, rebootConfigName, `{.status.conditions[?(@.type=="Applied")].status}`)
			g.Expect(appliedStatus).To(Equal("True"))
			ts := getJSONPath(g, rebootConfigName, `{.status.conditions[?(@.type=="Applied")].lastTransitionTime}`)
			g.Expect(ts).NotTo(BeEmpty())
			parsed, err := time.Parse(time.RFC3339, ts)
			g.Expect(err).NotTo(HaveOccurred())
			appliedTransitionBefore = parsed
		}, applyTimeout, applyPoll).Should(Succeed())
		By("verifying the host-name landed in-guest before the VM is torn down")
		expectGuestHostname(kvNamespace, launcherPodA, qgaDomainA, rebootHostname)

		By("deleting vm-a and waiting for it to be fully gone")
		_, err := utils.Run(exec.Command("kubectl", "-n", kvNamespace, "delete", "vm", vmNameA,
			"--ignore-not-found", "--wait=true", "--timeout=120s"))
		Expect(err).NotTo(HaveOccurred(), "Failed to delete vm-a")

		By("recreating vm-a from its stateless containerDisk (fresh boot resets the host-name to the image default)")
		launcherPodA = bootRouterVM(kvNamespace, vmNameA, routerContainerDisk)

		// The re-apply is proven two ways that must agree: (a) the Applied
		// condition re-transitions (its lastTransitionTime advances past the
		// pre-teardown value — it can only do so by going True->False->True
		// through the RouterNotReady reset), and (b) the host-name reappears in
		// the fresh guest, which a stateless boot could only get from a genuine
		// re-apply. If the guest booted fast enough that no reconcile ever caught
		// CheckReady failing, neither would hold and this spec would (correctly)
		// fail — surfacing that the reboot-recovery window was missed.
		By("verifying the operator re-applies to the fresh guest: Applied re-transitions")
		Eventually(func(g Gomega) {
			appliedStatus := getJSONPath(g, rebootConfigName, `{.status.conditions[?(@.type=="Applied")].status}`)
			g.Expect(appliedStatus).To(Equal("True"))
			ts := getJSONPath(g, rebootConfigName, `{.status.conditions[?(@.type=="Applied")].lastTransitionTime}`)
			g.Expect(ts).NotTo(BeEmpty())
			parsed, err := time.Parse(time.RFC3339, ts)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(parsed.After(appliedTransitionBefore)).To(BeTrue(),
				"Applied condition never re-transitioned after vm-a was recreated "+
					"(was %s, still %s) — the config was not re-applied",
				appliedTransitionBefore, parsed)
		}, applyTimeout, applyPoll).Should(Succeed())

		By("verifying the host-name is re-applied into the freshly booted guest")
		expectGuestHostname(kvNamespace, launcherPodA, qgaDomainA, rebootHostname)
	})
})

// deleteBindingResources deletes a binding-driven resource set: the
// generated VRouterConfigs (named "<binding>.<target>" for each target in
// targets), the binding itself, the targets, and the template. Shared by
// every rollout-e2e spec's DeferCleanup, and by the suite's AfterAll safety
// net, so a spec's resources never leak into the next one.
// ns is always "default" today; kept explicit for reuse (unparam false positive).
func deleteBindingResources(ns, binding, template string, targets []string) { //nolint:unparam
	cfgArgs := make([]string, 0, 4+len(targets)+1)
	cfgArgs = append(cfgArgs, "-n", ns, "delete", "vrouterconfig")
	for _, t := range targets {
		cfgArgs = append(cfgArgs, fmt.Sprintf("%s.%s", binding, t))
	}
	cfgArgs = append(cfgArgs, "--ignore-not-found")
	_, _ = utils.Run(exec.Command("kubectl", cfgArgs...))

	_, _ = utils.Run(exec.Command("kubectl", "-n", ns, "delete", "vrouterbinding", binding, "--ignore-not-found"))

	targetArgs := append([]string{"-n", ns, "delete", "vroutertarget"}, targets...)
	targetArgs = append(targetArgs, "--ignore-not-found")
	_, _ = utils.Run(exec.Command("kubectl", targetArgs...))

	_, _ = utils.Run(exec.Command("kubectl", "-n", ns, "delete", "vroutertemplate", template, "--ignore-not-found"))
}

// expectGuestHostname asserts, via a generous Eventually (never a single
// assert against a live cluster), that the guest reachable through
// launcher/domain reports hostname via QGA's /bin/hostname.
//
//nolint:unparam // ns kept explicit like qgaGuestExec's own signature
func expectGuestHostname(ns, launcher, domain, hostname string) {
	Eventually(func(g Gomega) {
		out, err := qgaGuestExec(ns, launcher, domain, "/bin/hostname")
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(strings.TrimSpace(out)).To(Equal(hostname))
	}, applyTimeout, applyPoll).Should(Succeed())
}

// addressTemplateManifest renders a VRouterTemplate that sets eth0's
// interface address from a params.addr value. Used only by the halt+resume
// spec to trip a set-time validation failure the same way
// vrouterFailingConfigManifest does (kubevirt_e2e_test.go), without touching
// system host-name — which the other rollout specs sharing these two VMs
// assert on.
func addressTemplateManifest(name, ns string) string {
	return fmt.Sprintf(`apiVersion: vrouter.kojuro.date/v1
kind: VRouterTemplate
metadata:
  name: %[1]s
  namespace: %[2]s
spec:
  commands: |
    set interfaces ethernet eth0 address '{{ .addr }}'
`, name, ns)
}

// targetWithAddrParamManifest renders a VRouterTarget identifying vmName,
// with spec.params.addr set to addr, for use with addressTemplateManifest.
func targetWithAddrParamManifest(name, ns, vmName, addr string) string {
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
    addr: "%[4]s"
`, name, ns, vmName, addr)
}
