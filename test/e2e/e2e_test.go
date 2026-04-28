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
