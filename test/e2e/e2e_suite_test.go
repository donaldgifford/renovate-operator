//go:build e2e
// +build e2e

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
// v0.1.0 ships no webhooks (ADR-0006) so cert-manager is intentionally
// not installed by this suite — kubebuilder's scaffolded cert-manager
// hook was removed.
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
		"--wait", "--timeout=3m",
	)
	out, err := utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred(), "helm install failed: %s", out)
})

var _ = AfterSuite(func() {
	By("uninstalling the operator")
	cmd := exec.Command("helm", "uninstall", releaseName,
		"--namespace", releaseNamespace, "--ignore-not-found")
	_, _ = utils.Run(cmd)

	By("deleting test namespace")
	cmd = exec.Command("kubectl", "delete", "namespace", releaseNamespace, "--ignore-not-found")
	_, _ = utils.Run(cmd)
})
