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
	"bytes"
	"fmt"
	"os/exec"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/donaldgifford/renovate-operator/test/utils"
)

var _ = Describe("Operator smoke", Ordered, func() {
	SetDefaultEventuallyTimeout(3 * time.Minute)
	SetDefaultEventuallyPollingInterval(2 * time.Second)

	It("rolls out the controller pod", func() {
		verifyPodRunning := func(g Gomega) {
			cmd := exec.Command("kubectl", "-n", releaseNamespace, "get", "pods",
				"-l", "control-plane=controller-manager",
				"-o", "jsonpath={.items[*].status.phase}")
			out, err := utils.Run(cmd)
			g.Expect(err).NotTo(HaveOccurred(), "kubectl get pods: %v", err)
			g.Expect(out).To(Equal("Running"), "controller pod phase = %q, want Running", out)
		}
		Eventually(verifyPodRunning).Should(Succeed())
	})

	It("registers all three CRDs", func() {
		for _, kind := range []string{"renovateplatforms", "renovatescans", "renovateruns"} {
			cmd := exec.Command("kubectl", "get", "crd",
				fmt.Sprintf("%s.renovate.fartlab.dev", kind),
				"-o", "jsonpath={.metadata.name}")
			out, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "%s crd missing: %v", kind, err)
			Expect(out).To(ContainSubstring(kind), "%s crd output: %s", kind, out)
		}
	})

	It("logs that the manager has started", func() {
		verifyManagerStart := func(g Gomega) {
			cmd := exec.Command("kubectl", "-n", releaseNamespace, "logs",
				"-l", "control-plane=controller-manager", "--tail=200")
			out, err := utils.Run(cmd)
			g.Expect(err).NotTo(HaveOccurred(), "kubectl logs: %v", err)
			g.Expect(out).To(ContainSubstring("Starting manager"),
				"manager start log not found in tail")
		}
		Eventually(verifyManagerStart).Should(Succeed())
	})
})

// Platform reconciler smoke — covers the v0.1.0 happy-path acceptance for
// the Platform controller's main contract: a Platform pointing at a
// non-existent Secret reaches Ready=False with reason=SecretMissing. No
// Forgejo, no Renovate worker, no real network — pure operator code paths
// against a real apiserver.
var _ = Describe("Platform reconciler", Ordered, func() {
	SetDefaultEventuallyTimeout(2 * time.Minute)
	SetDefaultEventuallyPollingInterval(2 * time.Second)

	const platformName = "e2e-platform-missing-secret"

	AfterAll(func() {
		cmd := exec.Command("kubectl", "delete", "renovateplatform", platformName, "--ignore-not-found")
		_, _ = utils.Run(cmd)
	})

	It("marks Ready=False when the credential Secret is missing", func() {
		// Apply a token-auth Platform that points at a Secret that doesn't exist.
		// The Platform is cluster-scoped per ADR-0005.
		manifest := fmt.Sprintf(`
apiVersion: renovate.fartlab.dev/v1alpha1
kind: RenovatePlatform
metadata:
  name: %s
spec:
  platformType: forgejo
  baseURL: https://forgejo.example.invalid
  renovateImage: ghcr.io/renovatebot/renovate:latest
  auth:
    token:
      secretRef:
        name: nonexistent-creds
        key: token
`, platformName)

		cmd := exec.Command("kubectl", "apply", "-f", "-")
		cmd.Stdin = bytes.NewReader([]byte(manifest))
		_, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "kubectl apply RenovatePlatform")

		// Operator should observe the Platform, fail to resolve the Secret,
		// and mark Ready=False with reason=SecretMissing.
		verifySecretMissing := func(g Gomega) {
			cmd := exec.Command("kubectl", "get", "renovateplatform", platformName,
				"-o", "jsonpath={.status.conditions[?(@.type=='Ready')].reason}")
			out, err := utils.Run(cmd)
			g.Expect(err).NotTo(HaveOccurred(), "kubectl get platform: %v", err)
			g.Expect(out).To(Equal("SecretMissing"), "Ready reason = %q, want SecretMissing", out)
		}
		Eventually(verifySecretMissing).Should(Succeed())
	})
})
