//go:build e2e
// +build e2e

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

<<<<<<< HEAD
	SetDefaultEventuallyTimeout(2 * time.Minute)
	SetDefaultEventuallyPollingInterval(time.Second)

	Context("Manager", func() {
		It("should run successfully", func() {
			By("validating that the controller-manager pod is running as expected")
			verifyControllerUp := func(g Gomega) {
				By("getting the name of the controller-manager pod")
				cmd := exec.Command("kubectl", "get",
					"pods", "-l", "control-plane=controller-manager",
					"-o", "go-template={{ range .items }}"+
						"{{ if not .metadata.deletionTimestamp }}"+
						"{{ .metadata.name }}"+
						"{{ \"\\n\" }}{{ end }}{{ end }}",
					"-n", namespace,
				)

				podOutput, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred(), "Failed to retrieve controller-manager pod information")
				podNames := utils.GetNonEmptyLines(podOutput)
				g.Expect(podNames).To(HaveLen(1), "expected 1 controller pod running")
				controllerPodName = podNames[0]
				g.Expect(controllerPodName).To(ContainSubstring("controller-manager"))

				By("validating the pod's status")
				cmd = exec.Command("kubectl", "get",
					"pods", controllerPodName, "-o", "jsonpath={.status.phase}",
					"-n", namespace,
				)
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("Running"), "Incorrect controller-manager pod status")
			}
			Eventually(verifyControllerUp).Should(Succeed())
		})

		It("should ensure the metrics endpoint is serving metrics", func() {
			By("creating a ClusterRoleBinding for the service account to allow access to metrics")
			cmd := exec.Command("kubectl", "create", "clusterrolebinding", metricsRoleBindingName,
				"--clusterrole=renovate-operator-metrics-reader",
				fmt.Sprintf("--serviceaccount=%s:%s", namespace, serviceAccountName),
			)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to create ClusterRoleBinding")

			By("validating that the metrics service is available")
			cmd = exec.Command("kubectl", "get", "service", metricsServiceName, "-n", namespace)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Metrics service should exist")

			By("getting the service account token")
			token, err := serviceAccountToken()
			Expect(err).NotTo(HaveOccurred())
			Expect(token).NotTo(BeEmpty())

			By("ensuring the controller pod is ready")
			verifyControllerPodReady := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "pod", controllerPodName, "-n", namespace,
					"-o", "jsonpath={.status.conditions[?(@.type=='Ready')].status}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("True"), "Controller pod not ready")
			}
			Eventually(verifyControllerPodReady, 3*time.Minute, time.Second).Should(Succeed())

			By("verifying that the controller manager is serving the metrics server")
			verifyMetricsServerStarted := func(g Gomega) {
				cmd := exec.Command("kubectl", "logs", controllerPodName, "-n", namespace)
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(ContainSubstring("Serving metrics server"),
					"Metrics server not yet started")
			}
			Eventually(verifyMetricsServerStarted, 3*time.Minute, time.Second).Should(Succeed())

			// +kubebuilder:scaffold:e2e-metrics-webhooks-readiness

			By("creating the curl-metrics pod to access the metrics endpoint")
			cmd = exec.Command("kubectl", "run", "curl-metrics", "--restart=Never",
				"--namespace", namespace,
				"--image=curlimages/curl:latest",
				"--overrides",
				fmt.Sprintf(`{
					"spec": {
						"containers": [{
							"name": "curl",
							"image": "curlimages/curl:latest",
							"command": ["/bin/sh", "-c"],
							"args": [
								"for i in $(seq 1 30); do curl -v -k -H 'Authorization: Bearer %s' https://%s.%s.svc.cluster.local:8443/metrics && exit 0 || sleep 2; done; exit 1"
							],
							"securityContext": {
								"readOnlyRootFilesystem": true,
								"allowPrivilegeEscalation": false,
								"capabilities": {
									"drop": ["ALL"]
								},
								"runAsNonRoot": true,
								"runAsUser": 1000,
								"seccompProfile": {
									"type": "RuntimeDefault"
								}
							}
						}],
						"serviceAccountName": "%s"
					}
				}`, token, metricsServiceName, namespace, serviceAccountName))
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to create curl-metrics pod")

			By("waiting for the curl-metrics pod to complete.")
			verifyCurlUp := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "pods", "curl-metrics",
					"-o", "jsonpath={.status.phase}",
					"-n", namespace)
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("Succeeded"), "curl pod in wrong status")
			}
			Eventually(verifyCurlUp, 5*time.Minute).Should(Succeed())

			By("getting the metrics by checking curl-metrics logs")
			verifyMetricsAvailable := func(g Gomega) {
				metricsOutput, err := getMetricsOutput()
				g.Expect(err).NotTo(HaveOccurred(), "Failed to retrieve logs from curl pod")
				g.Expect(metricsOutput).NotTo(BeEmpty())
				g.Expect(metricsOutput).To(ContainSubstring("< HTTP/1.1 200 OK"))
			}
			Eventually(verifyMetricsAvailable, 2*time.Minute).Should(Succeed())
		})

		// +kubebuilder:scaffold:e2e-webhooks-checks

		// TODO: Customize the e2e test suite with scenarios specific to your project.
		// Consider applying sample/CR(s) and check their status and/or verifying
		// the reconciliation by using the metrics, i.e.:
		// metricsOutput, err := getMetricsOutput()
		// Expect(err).NotTo(HaveOccurred(), "Failed to retrieve logs from curl pod")
		// Expect(metricsOutput).To(ContainSubstring(
		//    fmt.Sprintf(`controller_runtime_reconcile_total{controller="%s",result="success"} 1`,
		//    strings.ToLower(<Kind>),
		// ))
=======
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
>>>>>>> tmp-original-05-05-26-00-36
	})
})

// Platform reconciler smoke — covers the v0.1.0 happy-path acceptance for
// the Platform controller's main contract: a Platform pointing at a
// non-existent Secret reaches Ready=False with reason=SecretNotFound. No
// Forgejo, no Renovate worker, no real network — pure operator code paths
// against a real apiserver.
var _ = Describe("Platform reconciler", Ordered, func() {
	SetDefaultEventuallyTimeout(2 * time.Minute)
	SetDefaultEventuallyPollingInterval(2 * time.Second)

<<<<<<< HEAD
	By("creating temporary file to store the token request")
	secretName := fmt.Sprintf("%s-token-request", serviceAccountName)
	tokenRequestFile := filepath.Join("/tmp", secretName)
	err := os.WriteFile(tokenRequestFile, []byte(tokenRequestRawString), os.FileMode(0o644))
	if err != nil {
		return "", err
	}

	var out string
	verifyTokenCreation := func(g Gomega) {
		By("executing kubectl command to create the token")
		cmd := exec.Command("kubectl", "create", "--raw", fmt.Sprintf(
			"/api/v1/namespaces/%s/serviceaccounts/%s/token",
			namespace,
			serviceAccountName,
		), "-f", tokenRequestFile)
=======
	const platformName = "e2e-platform-missing-secret"

	AfterAll(func() {
		cmd := exec.Command("kubectl", "delete", "renovateplatform", platformName, "--ignore-not-found")
		_, _ = utils.Run(cmd)
	})
>>>>>>> tmp-original-05-05-26-00-36

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

<<<<<<< HEAD
		By("parsing the JSON output to extract the token")
		var token tokenRequest
		err = json.Unmarshal(output, &token)
		g.Expect(err).NotTo(HaveOccurred())
=======
		cmd := exec.Command("kubectl", "apply", "-f", "-")
		cmd.Stdin = bytes.NewReader([]byte(manifest))
		_, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "kubectl apply RenovatePlatform")
>>>>>>> tmp-original-05-05-26-00-36

		// Operator should observe the Platform, fail to resolve the Secret,
		// and mark Ready=False with reason=SecretNotFound.
		verifySecretNotFound := func(g Gomega) {
			cmd := exec.Command("kubectl", "get", "renovateplatform", platformName,
				"-o", "jsonpath={.status.conditions[?(@.type=='Ready')].reason}")
			out, err := utils.Run(cmd)
			g.Expect(err).NotTo(HaveOccurred(), "kubectl get platform: %v", err)
			g.Expect(out).To(Equal("SecretNotFound"), "Ready reason = %q, want SecretNotFound", out)
		}
		Eventually(verifySecretNotFound).Should(Succeed())
	})
})
