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
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	renovatev1alpha1 "github.com/donaldgifford/renovate-operator/api/v1alpha1"
	"github.com/donaldgifford/renovate-operator/internal/conditions"
)

var _ = Describe("RenovateScan Controller", func() {
	Context("When reconciling a resource", func() {
		const resourceName = "test-scan"
		const platformName = "scan-test-platform"
		const namespace = "default"

		ctx := context.Background()

		scanKey := types.NamespacedName{Name: resourceName, Namespace: namespace}

		BeforeEach(func() {
			By("creating the parent Platform")
			platform := &renovatev1alpha1.RenovatePlatform{
				ObjectMeta: metav1.ObjectMeta{Name: platformName},
				Spec: renovatev1alpha1.RenovatePlatformSpec{
					PlatformType: renovatev1alpha1.PlatformTypeForgejo,
					BaseURL:      "https://forgejo.example.com",
					Auth: renovatev1alpha1.PlatformAuth{
						Token: &renovatev1alpha1.TokenAuth{
							SecretRef: renovatev1alpha1.SecretKeyReference{Name: "scan-test-creds"},
						},
					},
				},
			}
			err := k8sClient.Get(ctx, types.NamespacedName{Name: platformName}, &renovatev1alpha1.RenovatePlatform{})
			if err != nil && errors.IsNotFound(err) {
				Expect(k8sClient.Create(ctx, platform)).To(Succeed())
			}

			By("creating the Scan")
			scan := &renovatev1alpha1.RenovateScan{
				ObjectMeta: metav1.ObjectMeta{Name: resourceName, Namespace: namespace},
				Spec: renovatev1alpha1.RenovateScanSpec{
					PlatformRef: renovatev1alpha1.LocalObjectReference{Name: platformName},
					Schedule:    "0 2 * * *",
				},
			}
			err = k8sClient.Get(ctx, scanKey, &renovatev1alpha1.RenovateScan{})
			if err != nil && errors.IsNotFound(err) {
				Expect(k8sClient.Create(ctx, scan)).To(Succeed())
			}
		})

		AfterEach(func() {
			By("cleanup Scan")
			scan := &renovatev1alpha1.RenovateScan{}
			if err := k8sClient.Get(ctx, scanKey, scan); err == nil {
				Expect(k8sClient.Delete(ctx, scan)).To(Succeed())
			}
			By("cleanup Platform")
			p := &renovatev1alpha1.RenovatePlatform{ObjectMeta: metav1.ObjectMeta{Name: platformName}}
			_ = k8sClient.Delete(ctx, p)
			By("cleanup Secret")
			s := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "scan-test-creds", Namespace: operatorTestNamespace}}
			_ = k8sClient.Delete(ctx, s)
		})

		It("scaffolded reconcile is a no-op", func() {
			r := &RenovateScanReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: scanKey})
			Expect(err).NotTo(HaveOccurred())
		})

		It("schedules the Scan when its Platform is Ready", func() {
			By("marking the Platform Ready=True via Status update")
			plat := &renovatev1alpha1.RenovatePlatform{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: platformName}, plat)).To(Succeed())
			conditions.MarkTrue(&plat.Status.Conditions,
				conditions.TypeReady, conditions.ReasonCredentialsResolved, "ok",
				plat.Generation)
			Expect(k8sClient.Status().Update(ctx, plat)).To(Succeed())

			By("reconciling the Scan once")
			r := &RenovateScanReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			res, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: scanKey})
			Expect(err).NotTo(HaveOccurred())
			Expect(res.RequeueAfter).To(BeNumerically(">", 0),
				"Scheduled Scan should have a positive RequeueAfter pointing at next fire")

			By("inspecting the Scan's Ready condition")
			updated := &renovatev1alpha1.RenovateScan{}
			Expect(k8sClient.Get(ctx, scanKey, updated)).To(Succeed())
			ready := conditions.Get(updated.Status.Conditions, conditions.TypeReady)
			Expect(ready).NotTo(BeNil())
			Expect(ready.Status).To(Equal(metav1.ConditionTrue))
			Expect(ready.Reason).To(Equal(conditions.ReasonNextRunComputed))

			scheduled := conditions.Get(updated.Status.Conditions, conditions.TypeScheduled)
			Expect(scheduled).NotTo(BeNil())
			Expect(scheduled.Status).To(Equal(metav1.ConditionTrue))
		})
	})
})
