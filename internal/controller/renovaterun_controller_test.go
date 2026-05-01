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

package controller

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	clocktesting "k8s.io/utils/clock/testing"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	renovatev1alpha1 "github.com/donaldgifford/renovate-operator/api/v1alpha1"
	"github.com/donaldgifford/renovate-operator/internal/platform"
)

var _ = Describe("RenovateRun Controller", func() {
	Context("When reconciling a resource", func() {
		const runName = "test-run"
		const namespace = "default"

		ctx := context.Background()
		runKey := types.NamespacedName{Name: runName, Namespace: namespace}

		BeforeEach(func() {
			By("creating a minimal Run snapshot")
			run := &renovatev1alpha1.RenovateRun{
				ObjectMeta: metav1.ObjectMeta{Name: runName, Namespace: namespace},
				Spec: renovatev1alpha1.RenovateRunSpec{
					ScanRef: renovatev1alpha1.LocalObjectReference{Name: "scan"},
					PlatformSnapshot: renovatev1alpha1.RenovatePlatformSpec{
						PlatformType:  renovatev1alpha1.PlatformTypeGitHub,
						RenovateImage: "ghcr.io/renovatebot/renovate:latest",
						Auth: renovatev1alpha1.PlatformAuth{
							GitHubApp: &renovatev1alpha1.GitHubAppAuth{
								AppID:          1,
								InstallationID: 1,
								PrivateKeyRef:  renovatev1alpha1.SecretKeyReference{Name: "creds"},
							},
						},
					},
					ScanSnapshot: renovatev1alpha1.RenovateScanSpec{
						PlatformRef: renovatev1alpha1.LocalObjectReference{Name: "scan"},
						Schedule:    "0 2 * * *",
					},
				},
			}
			err := k8sClient.Get(ctx, runKey, &renovatev1alpha1.RenovateRun{})
			if err != nil && errors.IsNotFound(err) {
				Expect(k8sClient.Create(ctx, run)).To(Succeed())
			}
		})

		AfterEach(func() {
			By("cleanup Run")
			run := &renovatev1alpha1.RenovateRun{}
			if err := k8sClient.Get(ctx, runKey, run); err == nil {
				Expect(k8sClient.Delete(ctx, run)).To(Succeed())
			}
		})

		It("scaffolded reconcile is a no-op", func() {
			r := &RenovateRunReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: runKey})
			Expect(err).NotTo(HaveOccurred())
		})

		It("transitions Pending -> Running and creates mirror Secret + shard CM + Job", func() {
			// Use a unique run name + namespace so the no-op spec's BeforeEach
			// can't share state with this spec via envtest's slow Delete.
			const happyRunName = "happy-run"
			const happyNS = "default"
			happyKey := types.NamespacedName{Name: happyRunName, Namespace: happyNS}

			By("ensuring the operator namespace exists")
			ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: operatorTestNamespace}}
			_ = k8sClient.Create(ctx, ns)

			By("creating the source credential Secret")
			src := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: "happy-creds", Namespace: operatorTestNamespace},
				Data: map[string][]byte{
					"private-key.pem": []byte("-----BEGIN PRIVATE KEY-----\nFAKE\n-----END PRIVATE KEY-----\n"),
				},
			}
			Expect(k8sClient.Create(ctx, src)).To(Succeed())
			DeferCleanup(func() {
				_ = k8sClient.Delete(ctx, src)
			})

			By("creating a fresh Run snapshot (own name, own credential ref)")
			happyRun := &renovatev1alpha1.RenovateRun{
				ObjectMeta: metav1.ObjectMeta{Name: happyRunName, Namespace: happyNS},
				Spec: renovatev1alpha1.RenovateRunSpec{
					ScanRef: renovatev1alpha1.LocalObjectReference{Name: "scan"},
					PlatformSnapshot: renovatev1alpha1.RenovatePlatformSpec{
						PlatformType:  renovatev1alpha1.PlatformTypeGitHub,
						RenovateImage: "ghcr.io/renovatebot/renovate:latest",
						Auth: renovatev1alpha1.PlatformAuth{
							GitHubApp: &renovatev1alpha1.GitHubAppAuth{
								AppID: 1, InstallationID: 1,
								PrivateKeyRef: renovatev1alpha1.SecretKeyReference{Name: "happy-creds"},
							},
						},
					},
					ScanSnapshot: renovatev1alpha1.RenovateScanSpec{
						PlatformRef: renovatev1alpha1.LocalObjectReference{Name: "scan"},
						Schedule:    "0 2 * * *",
					},
				},
			}
			Expect(k8sClient.Create(ctx, happyRun)).To(Succeed())
			DeferCleanup(func() {
				h := &renovatev1alpha1.RenovateRun{}
				if err := k8sClient.Get(ctx, happyKey, h); err == nil {
					_ = k8sClient.Delete(ctx, h)
				}
			})

			By("reconciling the Run with a stubbed platform-client factory")
			// envtest's apiserver applies CRD defaults — discovery.requireConfig
			// defaults to true — so the stub must report hasConfig=true to keep
			// repos in the discovery batch. The fake.NewClientBuilder tests in
			// renovaterun_fake_test.go skip CRD defaulting and so don't need this.
			stub := &stubPlatformClient{
				repos: []platform.Repository{
					{Slug: happyNS + "/repo-a"},
					{Slug: happyNS + "/repo-b"},
				},
				hasConfig: true,
			}
			r := &RenovateRunReconciler{
				Client:            k8sClient,
				Scheme:            k8sClient.Scheme(),
				Clock:             clocktesting.NewFakeClock(time.Date(2026, 4, 26, 12, 0, 0, 0, time.UTC)),
				OperatorNamespace: operatorTestNamespace,
				PlatformClientFactory: func(_ context.Context, _ renovatev1alpha1.RenovatePlatformSpec, _ *corev1.Secret) (platform.Client, error) {
					return stub, nil
				},
			}
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: happyKey})
			Expect(err).NotTo(HaveOccurred())

			By("Run advanced to Running")
			updated := &renovatev1alpha1.RenovateRun{}
			Expect(k8sClient.Get(ctx, happyKey, updated)).To(Succeed())
			Expect(updated.Status.Phase).To(Equal(renovatev1alpha1.RunPhaseRunning))
			Expect(updated.Status.DiscoveredRepos).To(BeEquivalentTo(2))

			By("mirror Secret created in the run namespace")
			mirror := &corev1.Secret{}
			Expect(k8sClient.Get(ctx,
				types.NamespacedName{Name: "renovate-creds-" + happyRunName, Namespace: happyNS}, mirror)).To(Succeed())

			By("shard ConfigMap created")
			cm := &corev1.ConfigMap{}
			Expect(k8sClient.Get(ctx,
				types.NamespacedName{Name: happyRunName + "-shards", Namespace: happyNS}, cm)).To(Succeed())

			By("worker Job created with parallelism = completions")
			job := &batchv1.Job{}
			Expect(k8sClient.Get(ctx,
				types.NamespacedName{Name: happyRunName + "-worker", Namespace: happyNS}, job)).To(Succeed())
			Expect(job.Spec.Parallelism).NotTo(BeNil())
			Expect(job.Spec.Completions).NotTo(BeNil())
			Expect(*job.Spec.Parallelism).To(Equal(*job.Spec.Completions))

			By("cleanup downstream objects")
			DeferCleanup(func() {
				_ = k8sClient.Delete(ctx, job)
				_ = k8sClient.Delete(ctx, cm)
				_ = k8sClient.Delete(ctx, mirror)
			})
		})
	})
})
