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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	renovatev1alpha1 "github.com/donaldgifford/renovate-operator/api/v1alpha1"
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
	})
})
