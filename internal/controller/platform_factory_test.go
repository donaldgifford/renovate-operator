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
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/donaldgifford/renovate-operator/api/v1alpha1"
)

func TestDefaultPlatformClientFactory_GitHub(t *testing.T) {
	t.Parallel()
	f := DefaultPlatformClientFactory()

	pemBytes := genPKCS1PEM(t)
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "creds"},
		Data:       map[string][]byte{"private-key.pem": pemBytes},
	}
	snap := v1alpha1.RenovatePlatformSpec{
		PlatformType: v1alpha1.PlatformTypeGitHub,
		Auth: v1alpha1.PlatformAuth{
			GitHubApp: &v1alpha1.GitHubAppAuth{
				AppID: 1, InstallationID: 1,
				PrivateKeyRef: v1alpha1.SecretKeyReference{Name: "creds"},
			},
		},
	}
	c, err := f(context.Background(), snap, secret)
	if err != nil {
		t.Fatalf("factory err = %v", err)
	}
	if c == nil {
		t.Fatal("client = nil")
	}
}

func TestDefaultPlatformClientFactory_GitHub_DefaultPEMKey(t *testing.T) {
	t.Parallel()
	f := DefaultPlatformClientFactory()

	pemBytes := genPKCS1PEM(t)
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "creds"},
		Data:       map[string][]byte{"private-key.pem": pemBytes},
	}
	// Empty Key triggers the default-key fallback.
	snap := v1alpha1.RenovatePlatformSpec{
		PlatformType: v1alpha1.PlatformTypeGitHub,
		Auth: v1alpha1.PlatformAuth{
			GitHubApp: &v1alpha1.GitHubAppAuth{
				AppID: 1, InstallationID: 1,
				PrivateKeyRef: v1alpha1.SecretKeyReference{Name: "creds"},
			},
		},
	}
	if _, err := f(context.Background(), snap, secret); err != nil {
		t.Errorf("factory err = %v", err)
	}
}

func TestDefaultPlatformClientFactory_GitHub_MissingKey(t *testing.T) {
	t.Parallel()
	f := DefaultPlatformClientFactory()

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "creds"},
		Data:       map[string][]byte{}, // missing
	}
	snap := v1alpha1.RenovatePlatformSpec{
		PlatformType: v1alpha1.PlatformTypeGitHub,
		Auth: v1alpha1.PlatformAuth{
			GitHubApp: &v1alpha1.GitHubAppAuth{
				AppID: 1, InstallationID: 1,
				PrivateKeyRef: v1alpha1.SecretKeyReference{Name: "creds", Key: "wrong"},
			},
		},
	}
	if _, err := f(context.Background(), snap, secret); err == nil {
		t.Error("missing key: err = nil, want non-nil")
	}
}

func TestDefaultPlatformClientFactory_GitHub_MissingAuth(t *testing.T) {
	t.Parallel()
	f := DefaultPlatformClientFactory()
	snap := v1alpha1.RenovatePlatformSpec{
		PlatformType: v1alpha1.PlatformTypeGitHub,
		// no GitHubApp
	}
	if _, err := f(context.Background(), snap, &corev1.Secret{}); err == nil {
		t.Error("github without githubApp auth: err = nil, want non-nil")
	}
}

// NOTE: a happy-path Forgejo test isn't unit-feasible because the
// gitea.NewClient call reaches out to /api/v1/version at construction
// time (see internal/platform/forgejo). The Forgejo e2e scenario
// exercises that path against a real Forgejo container.

func TestDefaultPlatformClientFactory_Forgejo_MissingTokenAuth(t *testing.T) {
	t.Parallel()
	f := DefaultPlatformClientFactory()
	snap := v1alpha1.RenovatePlatformSpec{
		PlatformType: v1alpha1.PlatformTypeForgejo,
		BaseURL:      "https://forgejo.example.com",
	}
	if _, err := f(context.Background(), snap, &corev1.Secret{}); err == nil {
		t.Error("forgejo without token auth: err = nil, want non-nil")
	}
}

func TestDefaultPlatformClientFactory_Forgejo_MissingTokenKey(t *testing.T) {
	t.Parallel()
	f := DefaultPlatformClientFactory()

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "tok"},
		Data:       map[string][]byte{},
	}
	snap := v1alpha1.RenovatePlatformSpec{
		PlatformType: v1alpha1.PlatformTypeForgejo,
		BaseURL:      "https://forgejo.example.com",
		Auth: v1alpha1.PlatformAuth{
			Token: &v1alpha1.TokenAuth{
				SecretRef: v1alpha1.SecretKeyReference{Name: "tok", Key: "missing"},
			},
		},
	}
	if _, err := f(context.Background(), snap, secret); err == nil {
		t.Error("forgejo missing token key: err = nil, want non-nil")
	}
}

func TestDefaultPlatformClientFactory_UnknownType(t *testing.T) {
	t.Parallel()
	f := DefaultPlatformClientFactory()
	snap := v1alpha1.RenovatePlatformSpec{
		PlatformType: "bogus",
	}
	if _, err := f(context.Background(), snap, &corev1.Secret{}); err == nil {
		t.Error("unknown platform type: err = nil, want non-nil")
	}
}
