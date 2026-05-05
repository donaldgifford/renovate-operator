//go:build e2e
// +build e2e

package e2e

import (
	"fmt"
	"os/exec"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/donaldgifford/renovate-operator/test/utils"
)

const (
	// managerImage is the operator image built and loaded into kind for the suite.
	managerImage = "renovate-operator:e2e"
	// releaseName is the helm release used by the suite.
	releaseName = "renovate-operator-e2e"
	// releaseNamespace is where the operator gets installed.
	releaseNamespace = "renovate-system"
)

// TestE2E is the entrypoint for `go test -tags=e2e ./test/e2e/`.
//
<<<<<<< HEAD
// To enable kubectl kuberc (use custom kubectl configurations), set: KUBECTL_KUBERC=true
// By default, kuberc is disabled to ensure consistent test behavior across different environments.
// To skip CertManager installation, set: CERT_MANAGER_INSTALL_SKIP=true
=======
// v0.1.0 ships no webhooks (ADR-0006) so cert-manager is intentionally
// not installed by this suite — kubebuilder's scaffolded cert-manager
// hook was removed.
>>>>>>> tmp-original-05-05-26-00-36
func TestE2E(t *testing.T) {
	RegisterFailHandler(Fail)
	_, _ = fmt.Fprintf(GinkgoWriter, "Starting renovate-operator e2e suite\n")
	RunSpecs(t, "renovate-operator e2e suite")
}

var _ = BeforeSuite(func() {
	By("building the operator image")
	cmd := exec.Command("make", "docker-build", fmt.Sprintf("IMG=%s", managerImage))
	_, err := utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred(), "Failed to build the operator image")

	By("loading the operator image into the kind cluster")
	err = utils.LoadImageToKindClusterWithName(managerImage)
	Expect(err).NotTo(HaveOccurred(), "Failed to load image into kind")

<<<<<<< HEAD
	configureKubectlKubeRC()
	setupCertManager()
=======
	By(fmt.Sprintf("creating namespace %s", releaseNamespace))
	cmd = exec.Command("kubectl", "create", "namespace", releaseNamespace)
	_, _ = utils.Run(cmd) // ignore "already exists"

	By("installing the operator via the Helm chart")
	// Disable defaultScan so the chart doesn't fail-guard on a missing platformRef.
	// Individual specs create their own Platforms/Scans.
	cmd = exec.Command("helm", "upgrade", "--install", releaseName, "dist/chart",
		"--namespace", releaseNamespace,
		"--set", "image.repository=renovate-operator",
		"--set", "image.tag=e2e",
		"--set", "image.pullPolicy=Never",
		"--set", "controllerManager.container.image.repository=renovate-operator",
		"--set", "controllerManager.container.image.tag=e2e",
		"--set", "controllerManager.container.imagePullPolicy=Never",
		"--set", "defaultScan.enabled=false",
		"--wait", "--timeout=5m",
	)
	out, err := utils.Run(cmd)
	if err != nil {
		dumpClusterDiagnostics()
		Fail(fmt.Sprintf("helm install failed: %s", out))
	}
>>>>>>> tmp-original-05-05-26-00-36
})

// dumpClusterDiagnostics prints kubectl describe + logs for the operator
// namespace so that a BeforeSuite failure on a remote runner is debuggable
// from the CI log alone, without needing to re-run interactively.
func dumpClusterDiagnostics() {
	for _, args := range [][]string{
		{"kubectl", "-n", releaseNamespace, "get", "all"},
		{"kubectl", "-n", releaseNamespace, "describe", "pods"},
		{"kubectl", "-n", releaseNamespace, "logs", "-l", "control-plane=controller-manager", "--tail=200", "--all-containers"},
		{"kubectl", "-n", releaseNamespace, "get", "events", "--sort-by=.lastTimestamp"},
	} {
		out, err := utils.Run(exec.Command(args[0], args[1:]...))
		_, _ = fmt.Fprintf(GinkgoWriter, "\n--- diagnostics: %v ---\n%s\n(err: %v)\n", args, out, err)
	}
}

var _ = AfterSuite(func() {
	By("uninstalling the operator")
	cmd := exec.Command("helm", "uninstall", releaseName,
		"--namespace", releaseNamespace, "--ignore-not-found")
	_, _ = utils.Run(cmd)

	By("deleting test namespace")
	cmd = exec.Command("kubectl", "delete", "namespace", releaseNamespace, "--ignore-not-found")
	_, _ = utils.Run(cmd)
})
<<<<<<< HEAD

// Disable kubectl kuberc by default for test isolation.
// This prevents local kubectl configurations from affecting test behavior.
// To enable kuberc, set: KUBECTL_KUBERC=true
func configureKubectlKubeRC() {
	if os.Getenv("KUBECTL_KUBERC") != "true" {
		By("disabling kubectl kuberc for test isolation")
		err := os.Setenv("KUBECTL_KUBERC", "false")
		ExpectWithOffset(1, err).NotTo(HaveOccurred(), "Failed to disable kubectl kuberc")
		_, _ = fmt.Fprintf(GinkgoWriter,
			"kubectl kuberc disabled for consistent test behavior (override with KUBECTL_KUBERC=true)\n")
	} else {
		_, _ = fmt.Fprintf(GinkgoWriter, "kubectl kuberc enabled (KUBECTL_KUBERC=true)\n")
	}
}

// setupCertManager installs CertManager if needed for webhook tests.
// Skips installation if CERT_MANAGER_INSTALL_SKIP=true or if already present.
func setupCertManager() {
	if os.Getenv("CERT_MANAGER_INSTALL_SKIP") == "true" {
		_, _ = fmt.Fprintf(GinkgoWriter, "Skipping CertManager installation (CERT_MANAGER_INSTALL_SKIP=true)\n")
		return
	}

	By("checking if CertManager is already installed")
	if utils.IsCertManagerCRDsInstalled() {
		_, _ = fmt.Fprintf(GinkgoWriter, "CertManager is already installed. Skipping installation.\n")
		return
	}

	// Mark for cleanup before installation to handle interruptions and partial installs.
	shouldCleanupCertManager = true

	By("installing CertManager")
	Expect(utils.InstallCertManager()).To(Succeed(), "Failed to install CertManager")
}

// teardownCertManager uninstalls CertManager if it was installed by setupCertManager.
// This ensures we only remove what we installed.
func teardownCertManager() {
	if !shouldCleanupCertManager {
		_, _ = fmt.Fprintf(GinkgoWriter, "Skipping CertManager cleanup (not installed by this suite)\n")
		return
	}

	By("uninstalling CertManager")
	utils.UninstallCertManager()
}
=======
>>>>>>> tmp-original-05-05-26-00-36
