//go:build e2e
// +build e2e

package e2e

import (
	"bytes"
	"fmt"
	"net"
	"net/http"
	"os/exec"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/circa10a/getoffyoursaas/test/utils"
)

// ingestServiceName is the Service fronting the step ingest endpoint.
const ingestServiceName = "getoffyoursaas-ingest-service"

// localIngestPort is the host-side port used for the port-forward. Deliberately
// not 30080: that is the NodePort a developer's local kind cluster publishes, and
// reusing it here would clash when both clusters exist at once.
const localIngestPort = 18080

// sampleName is the StepScaler and demo Deployment created by config/samples.
const sampleName = "lazy-app"

// postSteps sends a reading to the ingest endpoint and returns the status code.
// It POSTs through a port-forward to the *Service*, not to the pod: kubectl
// resolves a Service port-forward via the Service's selector and targetPort, so a
// broken selector or a mismatched targetPort fails here rather than passing
// silently the way a pod-targeted forward would.
func postSteps(body string) int {
	resp, err := http.Post(
		fmt.Sprintf("http://127.0.0.1:%d/v1/steps", localIngestPort),
		"application/json",
		bytes.NewBufferString(body),
	)
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "Failed to POST to the ingest endpoint")
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode
}

// jsonPath returns a single field from a resource via kubectl jsonpath.
func jsonPath(resource, name, path string, args ...string) string {
	base := []string{"get", resource, name, "-o", fmt.Sprintf("jsonpath=%s", path)}
	cmd := exec.Command("kubectl", append(base, args...)...)
	out, err := utils.Run(cmd)
	if err != nil {
		return ""
	}
	return out
}

// minAllowedCPU reads the CPU floor the controller wrote to the owned VPA.
func minAllowedCPU() string {
	return jsonPath("vpa", sampleName, "{.spec.resourcePolicy.containerPolicies[0].minAllowed.cpu}")
}

var _ = Describe("StepScaler", Ordered, func() {
	var portForward *exec.Cmd

	BeforeAll(func() {
		By("creating manager namespace")
		cmd := exec.Command("kubectl", "create", "ns", namespace)
		_, _ = utils.Run(cmd)

		By("installing CRDs")
		cmd = exec.Command("make", "install")
		_, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to install CRDs")

		By("deploying the controller-manager")
		cmd = exec.Command("make", "deploy", fmt.Sprintf("IMG=%s", managerImage))
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to deploy the controller-manager")

		By("waiting for the controller-manager to become available")
		cmd = exec.Command("kubectl", "wait", "deployment.apps/getoffyoursaas-controller-manager",
			"--for", "condition=Available", "--namespace", namespace, "--timeout", "150s")
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "controller-manager never became Available")

		By("applying the sample StepScaler and demo workload")
		cmd = exec.Command("kubectl", "apply", "-k", "config/samples/")
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to apply config/samples")

		By("port-forwarding the ingest Service")
		// #nosec G204 -- ports and names are compile-time constants
		portForward = exec.Command("kubectl", "port-forward",
			fmt.Sprintf("service/%s", ingestServiceName),
			fmt.Sprintf("%d:8080", localIngestPort),
			"--namespace", namespace)
		Expect(portForward.Start()).To(Succeed(), "Failed to start port-forward")

		By("waiting for the forwarded port to accept connections")
		Eventually(func() error {
			conn, err := net.DialTimeout("tcp",
				fmt.Sprintf("127.0.0.1:%d", localIngestPort), time.Second)
			if err != nil {
				return err
			}
			return conn.Close()
		}, 60*time.Second, time.Second).Should(Succeed(), "port-forward never became reachable")
	})

	AfterAll(func() {
		if portForward != nil && portForward.Process != nil {
			By("stopping the port-forward")
			_ = portForward.Process.Kill()
			_ = portForward.Wait()
		}

		By("deleting the sample StepScaler and demo workload")
		cmd := exec.Command("kubectl", "delete", "-k", "config/samples/", "--ignore-not-found")
		_, _ = utils.Run(cmd)

		By("undeploying the controller-manager")
		cmd = exec.Command("make", "undeploy")
		_, _ = utils.Run(cmd)

		By("uninstalling CRDs")
		cmd = exec.Command("make", "uninstall")
		_, _ = utils.Run(cmd)

		By("removing manager namespace")
		cmd = exec.Command("kubectl", "delete", "ns", namespace, "--ignore-not-found")
		_, _ = utils.Run(cmd)
	})

	AfterEach(func() {
		if CurrentSpecReport().Failed() {
			By("Fetching controller manager pod logs")
			cmd := exec.Command("kubectl", "logs",
				"deployment/getoffyoursaas-controller-manager", "-n", namespace, "--tail", "60")
			if out, err := utils.Run(cmd); err == nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "Manager logs:\n%s\n", out)
			}

			By("Fetching the StepScaler and VPA state")
			cmd = exec.Command("kubectl", "get", "stepscaler,vpa", sampleName, "-o", "yaml")
			if out, err := utils.Run(cmd); err == nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "Resource state:\n%s\n", out)
			}
		}
	})

	It("creates an owned VPA at the minimum floor before any steps are posted", func() {
		By("the controller reconciling the sample into a VPA")
		Eventually(minAllowedCPU, 90*time.Second, 2*time.Second).Should(Equal("100m"),
			"the VPA should start at minCPU with no steps recorded")

		By("the VPA being owned by the StepScaler so it is garbage collected")
		Eventually(func() string {
			return jsonPath("vpa", sampleName, "{.metadata.ownerReferences[0].kind}")
		}, 30*time.Second, time.Second).Should(Equal("StepScaler"))

		By("never setting maxAllowed, which the VPA webhook would reject against a higher min")
		Expect(jsonPath("vpa", sampleName,
			"{.spec.resourcePolicy.containerPolicies[0].maxAllowed}")).To(BeEmpty())
	})

	It("raises the CPU floor in response to a posted step count", func() {
		By("posting 7431 steps")
		Expect(postSteps(`{"steps": 7431}`)).To(Equal(http.StatusNoContent))

		By("recording the reading on the StepScaler")
		Eventually(func() string {
			return jsonPath("stepscaler", sampleName, "{.status.steps}")
		}, 60*time.Second, time.Second).Should(Equal("7431"))

		By("computing 100m + 7431/10000 * 900m = 768m")
		Eventually(func() string {
			return jsonPath("stepscaler", sampleName, "{.status.currentCPU}")
		}, 60*time.Second, time.Second).Should(Equal("768m"))

		By("propagating that floor to the owned VPA")
		Eventually(minAllowedCPU, 60*time.Second, time.Second).Should(Equal("768m"))
	})

	It("lowers the floor again on a lazy day", func() {
		By("posting 0 steps, which is a valid reading rather than an error")
		Expect(postSteps(`{"steps": 0}`)).To(Equal(http.StatusNoContent))

		By("falling back to minCPU")
		Eventually(minAllowedCPU, 60*time.Second, time.Second).Should(Equal("100m"))
	})

	It("rejects an out-of-range reading without changing the recorded steps", func() {
		By("establishing a known good reading")
		Expect(postSteps(`{"steps": 1000}`)).To(Equal(http.StatusNoContent))
		Eventually(func() string {
			return jsonPath("stepscaler", sampleName, "{.status.steps}")
		}, 60*time.Second, time.Second).Should(Equal("1000"))

		By("rejecting a negative reading")
		Expect(postSteps(`{"steps": -1}`)).To(Equal(http.StatusBadRequest))

		By("rejecting a reading above the accepted bound")
		Expect(postSteps(`{"steps": 200001}`)).To(Equal(http.StatusBadRequest))

		By("rejecting a body with no steps field")
		Expect(postSteps(`{}`)).To(Equal(http.StatusBadRequest))

		By("leaving the previously recorded reading untouched")
		Consistently(func() string {
			return jsonPath("stepscaler", sampleName, "{.status.steps}")
		}, 5*time.Second, time.Second).Should(Equal("1000"))
	})
})
