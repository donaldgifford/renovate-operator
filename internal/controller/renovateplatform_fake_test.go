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
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	schema "k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	renovatev1alpha1 "github.com/donaldgifford/renovate-operator/api/v1alpha1"
	"github.com/donaldgifford/renovate-operator/internal/conditions"
)

const operatorNS = "renovate-system"

func newPlatformScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := renovatev1alpha1.AddToScheme(s); err != nil {
		t.Fatalf("scheme add renovate: %v", err)
	}
	if err := corev1.AddToScheme(s); err != nil {
		t.Fatalf("scheme add core: %v", err)
	}
	return s
}

func newPlatformReconciler(t *testing.T, objs ...client.Object) *RenovatePlatformReconciler {
	t.Helper()
	scheme := newPlatformScheme(t)
	cli := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objs...).
		WithStatusSubresource(&renovatev1alpha1.RenovatePlatform{}).
		Build()
	return &RenovatePlatformReconciler{
		Client:            cli,
		Scheme:            scheme,
		OperatorNamespace: operatorNS,
	}
}

func mkGitHubAppPlatform(name, secretName string) *renovatev1alpha1.RenovatePlatform {
	return &renovatev1alpha1.RenovatePlatform{
		ObjectMeta: metav1.ObjectMeta{Name: name, UID: types.UID("plat-" + name)},
		Spec: renovatev1alpha1.RenovatePlatformSpec{
			PlatformType:  renovatev1alpha1.PlatformTypeGitHub,
			RenovateImage: "ghcr.io/renovatebot/renovate:latest",
			Auth: renovatev1alpha1.PlatformAuth{
				GitHubApp: &renovatev1alpha1.GitHubAppAuth{
					AppID: 1, InstallationID: 1,
					PrivateKeyRef: renovatev1alpha1.SecretKeyReference{Name: secretName},
				},
			},
		},
	}
}

func mkTokenPlatform(name, secretName string) *renovatev1alpha1.RenovatePlatform {
	return &renovatev1alpha1.RenovatePlatform{
		ObjectMeta: metav1.ObjectMeta{Name: name, UID: types.UID("plat-" + name)},
		Spec: renovatev1alpha1.RenovatePlatformSpec{
			PlatformType:  renovatev1alpha1.PlatformTypeForgejo,
			BaseURL:       "https://forgejo.example.com",
			RenovateImage: "ghcr.io/renovatebot/renovate:latest",
			Auth: renovatev1alpha1.PlatformAuth{
				Token: &renovatev1alpha1.TokenAuth{
					SecretRef: renovatev1alpha1.SecretKeyReference{Name: secretName},
				},
			},
		},
	}
}

// genPKCS1PEM returns a valid PKCS1 RSA private key in PEM form (1024-bit
// is plenty for unit tests).
func genPKCS1PEM(t *testing.T) []byte {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatalf("rsa.GenerateKey: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})
}

func TestPlatformReconcile_GitHubApp_HappyPath(t *testing.T) {
	t.Parallel()
	plat := mkGitHubAppPlatform("github", "creds")
	pemBytes := genPKCS1PEM(t)
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "creds", Namespace: operatorNS},
		Data:       map[string][]byte{"private-key.pem": pemBytes},
	}
	r := newPlatformReconciler(t, plat, secret)

	res, err := r.Reconcile(context.Background(),
		reconcile.Request{NamespacedName: types.NamespacedName{Name: plat.Name}})
	if err != nil {
		t.Fatalf("Reconcile err = %v", err)
	}
	if res.RequeueAfter != 0 {
		t.Errorf("happy RequeueAfter = %v, want 0", res.RequeueAfter)
	}

	got := &renovatev1alpha1.RenovatePlatform{}
	_ = r.Get(context.Background(), types.NamespacedName{Name: plat.Name}, got)
	cond := findCondition(got.Status.Conditions, conditions.TypeReady)
	if cond == nil || cond.Status != metav1.ConditionTrue {
		t.Errorf("Ready cond = %v, want True", cond)
	}
	if cond != nil && cond.Reason != conditions.ReasonCredentialsResolved {
		t.Errorf("Ready reason = %q, want %q", cond.Reason, conditions.ReasonCredentialsResolved)
	}
}

func TestPlatformReconcile_TokenAuth_HappyPath(t *testing.T) {
	t.Parallel()
	plat := mkTokenPlatform("forgejo", "tok")
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "tok", Namespace: operatorNS},
		Data:       map[string][]byte{"token": []byte("opaque-token-value")},
	}
	r := newPlatformReconciler(t, plat, secret)

	_, err := r.Reconcile(context.Background(),
		reconcile.Request{NamespacedName: types.NamespacedName{Name: plat.Name}})
	if err != nil {
		t.Fatalf("Reconcile err = %v", err)
	}
	got := &renovatev1alpha1.RenovatePlatform{}
	_ = r.Get(context.Background(), types.NamespacedName{Name: plat.Name}, got)
	cond := findCondition(got.Status.Conditions, conditions.TypeReady)
	if cond == nil || cond.Status != metav1.ConditionTrue {
		t.Errorf("Ready = %v, want True", cond)
	}
}

func TestPlatformReconcile_SecretMissingRequeues(t *testing.T) {
	t.Parallel()
	plat := mkGitHubAppPlatform("github", "creds")
	r := newPlatformReconciler(t, plat) // no secret

	res, err := r.Reconcile(context.Background(),
		reconcile.Request{NamespacedName: types.NamespacedName{Name: plat.Name}})
	if err != nil {
		t.Fatalf("Reconcile err = %v", err)
	}
	if res.RequeueAfter != requeueAfterPlatformError {
		t.Errorf("RequeueAfter = %v, want %v", res.RequeueAfter, requeueAfterPlatformError)
	}

	got := &renovatev1alpha1.RenovatePlatform{}
	_ = r.Get(context.Background(), types.NamespacedName{Name: plat.Name}, got)
	cond := findCondition(got.Status.Conditions, conditions.TypeReady)
	if cond == nil || cond.Reason != conditions.ReasonSecretNotFound {
		t.Errorf("Ready reason = %v, want SecretNotFound", cond)
	}
}

func TestPlatformReconcile_KeyMissingRequeues(t *testing.T) {
	t.Parallel()
	plat := mkGitHubAppPlatform("github", "creds")
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "creds", Namespace: operatorNS},
		Data:       map[string][]byte{"wrong-key": []byte("garbage")},
	}
	r := newPlatformReconciler(t, plat, secret)

	res, err := r.Reconcile(context.Background(),
		reconcile.Request{NamespacedName: types.NamespacedName{Name: plat.Name}})
	if err != nil {
		t.Fatalf("Reconcile err = %v", err)
	}
	if res.RequeueAfter != requeueAfterPlatformError {
		t.Errorf("RequeueAfter = %v, want %v", res.RequeueAfter, requeueAfterPlatformError)
	}
	got := &renovatev1alpha1.RenovatePlatform{}
	_ = r.Get(context.Background(), types.NamespacedName{Name: plat.Name}, got)
	cond := findCondition(got.Status.Conditions, conditions.TypeReady)
	if cond == nil || cond.Reason != conditions.ReasonKeyMissing {
		t.Errorf("Ready reason = %v, want KeyMissing", cond)
	}
}

func TestPlatformReconcile_InvalidPEMMarksAuthFailed(t *testing.T) {
	t.Parallel()
	plat := mkGitHubAppPlatform("github", "creds")
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "creds", Namespace: operatorNS},
		Data:       map[string][]byte{"private-key.pem": []byte("not a real pem")},
	}
	r := newPlatformReconciler(t, plat, secret)

	_, err := r.Reconcile(context.Background(),
		reconcile.Request{NamespacedName: types.NamespacedName{Name: plat.Name}})
	if err != nil {
		t.Fatalf("Reconcile err = %v", err)
	}
	got := &renovatev1alpha1.RenovatePlatform{}
	_ = r.Get(context.Background(), types.NamespacedName{Name: plat.Name}, got)
	cond := findCondition(got.Status.Conditions, conditions.TypeReady)
	if cond == nil || cond.Reason != conditions.ReasonAuthFailed {
		t.Errorf("Ready reason = %v, want AuthFailed", cond)
	}
}

func TestPlatformReconcile_PEMValidButUnparseableMarksAuthFailed(t *testing.T) {
	t.Parallel()
	plat := mkGitHubAppPlatform("github", "creds")
	// PEM-shaped envelope but non-key payload.
	junk := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: []byte("not-a-key")})
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "creds", Namespace: operatorNS},
		Data:       map[string][]byte{"private-key.pem": junk},
	}
	r := newPlatformReconciler(t, plat, secret)

	_, err := r.Reconcile(context.Background(),
		reconcile.Request{NamespacedName: types.NamespacedName{Name: plat.Name}})
	if err != nil {
		t.Fatalf("Reconcile err = %v", err)
	}
	got := &renovatev1alpha1.RenovatePlatform{}
	_ = r.Get(context.Background(), types.NamespacedName{Name: plat.Name}, got)
	cond := findCondition(got.Status.Conditions, conditions.TypeReady)
	if cond == nil || cond.Reason != conditions.ReasonAuthFailed {
		t.Errorf("Ready reason = %v, want AuthFailed", cond)
	}
}

func TestPlatformReconcile_NoAuthSet(t *testing.T) {
	t.Parallel()
	plat := &renovatev1alpha1.RenovatePlatform{
		ObjectMeta: metav1.ObjectMeta{Name: "lonely", UID: types.UID("plat-lonely")},
		Spec: renovatev1alpha1.RenovatePlatformSpec{
			PlatformType:  renovatev1alpha1.PlatformTypeGitHub,
			RenovateImage: "ghcr.io/renovatebot/renovate:latest",
			// Auth deliberately empty
		},
	}
	r := newPlatformReconciler(t, plat)

	_, err := r.Reconcile(context.Background(),
		reconcile.Request{NamespacedName: types.NamespacedName{Name: plat.Name}})
	if err != nil {
		t.Fatalf("Reconcile err = %v", err)
	}
	got := &renovatev1alpha1.RenovatePlatform{}
	_ = r.Get(context.Background(), types.NamespacedName{Name: plat.Name}, got)
	cond := findCondition(got.Status.Conditions, conditions.TypeReady)
	if cond == nil || cond.Reason != conditions.ReasonAuthFailed {
		t.Errorf("Ready reason = %v, want AuthFailed (no auth configured)", cond)
	}
}

func TestPlatformReconcile_NotFoundIgnored(t *testing.T) {
	t.Parallel()
	r := newPlatformReconciler(t)
	res, err := r.Reconcile(context.Background(),
		reconcile.Request{NamespacedName: types.NamespacedName{Name: "ghost"}})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if res.RequeueAfter != 0 {
		t.Errorf("RequeueAfter = %v, want 0", res.RequeueAfter)
	}
}

func TestResolveAuthRef_DefaultKeys(t *testing.T) {
	t.Parallel()
	r := &RenovatePlatformReconciler{}
	gh := mkGitHubAppPlatform("p", "s")
	gh.Spec.Auth.GitHubApp.PrivateKeyRef.Key = ""
	_, key, err := r.resolveAuthRef(gh)
	if err != nil {
		t.Fatalf("github default key: %v", err)
	}
	if key != defaultGitHubAppPEMKey {
		t.Errorf("github default = %q, want %q", key, defaultGitHubAppPEMKey)
	}

	tok := mkTokenPlatform("p", "s")
	tok.Spec.Auth.Token.SecretRef.Key = ""
	_, key, err = r.resolveAuthRef(tok)
	if err != nil {
		t.Fatalf("token default key: %v", err)
	}
	if key != defaultTokenKey {
		t.Errorf("token default = %q, want %q", key, defaultTokenKey)
	}
}

func TestResolveAuthRef_NameRequired(t *testing.T) {
	t.Parallel()
	r := &RenovatePlatformReconciler{}
	gh := mkGitHubAppPlatform("p", "")
	if _, _, err := r.resolveAuthRef(gh); err == nil {
		t.Error("github with empty name: err = nil, want non-nil")
	}
	tok := mkTokenPlatform("p", "")
	if _, _, err := r.resolveAuthRef(tok); err == nil {
		t.Error("token with empty name: err = nil, want non-nil")
	}
}

func TestPlatformsForSecret_MatchesByName(t *testing.T) {
	t.Parallel()
	p1 := mkGitHubAppPlatform("p1", "shared")
	p2 := mkGitHubAppPlatform("p2", "shared")
	p3 := mkGitHubAppPlatform("p3", "other")
	r := newPlatformReconciler(t, p1, p2, p3)

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "shared", Namespace: operatorNS},
	}
	reqs := r.platformsForSecret(context.Background(), secret)
	if len(reqs) != 2 {
		t.Fatalf("got %d requests, want 2", len(reqs))
	}
	got := map[string]bool{
		reqs[0].String(): true,
		reqs[1].String(): true,
	}
	for _, want := range []string{"/p1", "/p2"} {
		if !got[want] {
			t.Errorf("missing request %q (got %+v)", want, reqs)
		}
	}
}

func TestPlatformsForSecret_IgnoresOtherNamespaces(t *testing.T) {
	t.Parallel()
	p1 := mkGitHubAppPlatform("p1", "shared")
	r := newPlatformReconciler(t, p1)

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "shared", Namespace: "elsewhere"},
	}
	reqs := r.platformsForSecret(context.Background(), secret)
	if len(reqs) != 0 {
		t.Errorf("expected 0 requests, got %d", len(reqs))
	}
}

func TestPlatformsForSecret_NonSecretReturnsNil(t *testing.T) {
	t.Parallel()
	r := newPlatformReconciler(t)
	notSecret := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "cm", Namespace: operatorNS},
	}
	reqs := r.platformsForSecret(context.Background(), notSecret)
	if reqs != nil {
		t.Errorf("expected nil, got %+v", reqs)
	}
}

func TestSecretInOperatorNamespacePredicate(t *testing.T) {
	t.Parallel()
	p := secretInOperatorNamespacePredicate(operatorNS)

	in := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "x", Namespace: operatorNS},
	}
	if !p.Generic(event.GenericEvent{Object: in}) {
		t.Error("predicate.Generic in-NS = false, want true")
	}

	out := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "x", Namespace: "elsewhere"},
	}
	if p.Generic(event.GenericEvent{Object: out}) {
		t.Error("predicate.Generic out-of-NS = true, want false")
	}
}

// platformReconcilerWithInterceptor wires a RenovatePlatformReconciler
// whose underlying fake client routes Status().Update through funcs. Used
// to exercise the outer Reconcile wrapper's conflict + non-conflict
// update-error paths.
func platformReconcilerWithInterceptor(t *testing.T, funcs interceptor.Funcs, objs ...client.Object) *RenovatePlatformReconciler {
	t.Helper()
	scheme := newPlatformScheme(t)
	cli := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objs...).
		WithStatusSubresource(&renovatev1alpha1.RenovatePlatform{}).
		WithInterceptorFuncs(funcs).
		Build()
	return &RenovatePlatformReconciler{
		Client:            cli,
		Scheme:            scheme,
		OperatorNamespace: operatorNS,
	}
}

func TestPlatformReconcile_StatusConflictRequeues(t *testing.T) {
	t.Parallel()
	plat := mkGitHubAppPlatform("conflict", "creds")
	conflict := apierrors.NewConflict(
		schema.GroupResource{Group: "renovate.fartlab.dev", Resource: "renovateplatforms"},
		plat.Name,
		fmt.Errorf("optimistic concurrency"),
	)

	r := platformReconcilerWithInterceptor(t, interceptor.Funcs{
		SubResourceUpdate: func(_ context.Context, _ client.Client, _ string, _ client.Object, _ ...client.SubResourceUpdateOption) error {
			return conflict
		},
	}, plat)

	res, err := r.Reconcile(context.Background(),
		reconcile.Request{NamespacedName: types.NamespacedName{Name: plat.Name}})
	if err != nil {
		t.Fatalf("Reconcile err = %v, want nil on conflict", err)
	}
	if res.RequeueAfter == 0 {
		t.Errorf("RequeueAfter = 0, want non-zero on status conflict")
	}
}

func TestPlatformReconcile_StatusUpdateErrorPropagates(t *testing.T) {
	t.Parallel()
	plat := mkGitHubAppPlatform("update-err", "creds")

	r := platformReconcilerWithInterceptor(t, interceptor.Funcs{
		SubResourceUpdate: func(_ context.Context, _ client.Client, _ string, _ client.Object, _ ...client.SubResourceUpdateOption) error {
			return fmt.Errorf("apiserver unavailable")
		},
	}, plat)

	_, err := r.Reconcile(context.Background(),
		reconcile.Request{NamespacedName: types.NamespacedName{Name: plat.Name}})
	if err == nil {
		t.Fatal("Reconcile err = nil, want propagated update error")
	}
}
