package controller

import (
	. "github.com/onsi/ginkgo/v2"
<<<<<<< HEAD
=======
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	renovatev1alpha1 "github.com/donaldgifford/renovate-operator/api/v1alpha1"
	"github.com/donaldgifford/renovate-operator/internal/conditions"
>>>>>>> tmp-original-05-05-26-00-36
)

const operatorTestNamespace = "renovate-system"

var _ = Describe("RenovatePlatform Controller", func() {
	Context("When reconciling a resource", func() {
<<<<<<< HEAD

		It("should successfully reconcile the resource", func() {

			// TODO(user): Add more specific assertions depending on your controller's reconciliation logic.
			// Example: If you expect a certain status condition after reconciliation, verify it here.
=======
		const resourceName = "test-platform"

		ctx := context.Background()

		platformKey := types.NamespacedName{Name: resourceName}

		BeforeEach(func() {
			By("ensuring the operator namespace exists")
			ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: operatorTestNamespace}}
			_ = k8sClient.Create(ctx, ns)

			By("creating a token-auth Platform pointing at a Secret in the operator namespace")
			platform := &renovatev1alpha1.RenovatePlatform{
				ObjectMeta: metav1.ObjectMeta{Name: resourceName},
				Spec: renovatev1alpha1.RenovatePlatformSpec{
					PlatformType: renovatev1alpha1.PlatformTypeForgejo,
					BaseURL:      "https://forgejo.example.com",
					Auth: renovatev1alpha1.PlatformAuth{
						Token: &renovatev1alpha1.TokenAuth{
							SecretRef: renovatev1alpha1.SecretKeyReference{Name: "platform-creds"},
						},
					},
				},
			}
			err := k8sClient.Get(ctx, platformKey, &renovatev1alpha1.RenovatePlatform{})
			if err != nil && errors.IsNotFound(err) {
				Expect(k8sClient.Create(ctx, platform)).To(Succeed())
			}
		})

		AfterEach(func() {
			By("cleanup Platform")
			p := &renovatev1alpha1.RenovatePlatform{}
			if err := k8sClient.Get(ctx, platformKey, p); err == nil {
				Expect(k8sClient.Delete(ctx, p)).To(Succeed())
			}
			By("cleanup credential Secret")
			s := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "platform-creds", Namespace: operatorTestNamespace}}
			_ = k8sClient.Delete(ctx, s)
		})

		It("sets Ready=False/SecretNotFound when the Secret is missing", func() {
			r := &RenovatePlatformReconciler{
				Client:            k8sClient,
				Scheme:            k8sClient.Scheme(),
				OperatorNamespace: operatorTestNamespace,
			}
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: platformKey})
			Expect(err).NotTo(HaveOccurred())

			updated := &renovatev1alpha1.RenovatePlatform{}
			Expect(k8sClient.Get(ctx, platformKey, updated)).To(Succeed())
			ready := conditions.Get(updated.Status.Conditions, conditions.TypeReady)
			Expect(ready).NotTo(BeNil())
			Expect(ready.Status).To(Equal(metav1.ConditionFalse))
			Expect(ready.Reason).To(Equal(conditions.ReasonSecretNotFound))
		})

		It("flips Ready=True once the credential Secret arrives", func() {
			r := &RenovatePlatformReconciler{
				Client:            k8sClient,
				Scheme:            k8sClient.Scheme(),
				OperatorNamespace: operatorTestNamespace,
			}

			By("first reconcile produces Ready=False/SecretNotFound")
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: platformKey})
			Expect(err).NotTo(HaveOccurred())

			By("creating the Secret in the operator namespace")
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: "platform-creds", Namespace: operatorTestNamespace},
				Data:       map[string][]byte{"token": []byte("supersecret")},
				Type:       corev1.SecretTypeOpaque,
			}
			Expect(k8sClient.Create(ctx, secret)).To(Succeed())

			By("second reconcile flips Ready=True")
			_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: platformKey})
			Expect(err).NotTo(HaveOccurred())

			updated := &renovatev1alpha1.RenovatePlatform{}
			Expect(k8sClient.Get(ctx, platformKey, updated)).To(Succeed())
			ready := conditions.Get(updated.Status.Conditions, conditions.TypeReady)
			Expect(ready).NotTo(BeNil())
			Expect(ready.Status).To(Equal(metav1.ConditionTrue))
			Expect(ready.Reason).To(Equal(conditions.ReasonCredentialsResolved))
>>>>>>> tmp-original-05-05-26-00-36
		})
	})
})
