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

package credentials_test

import (
	"errors"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	v1alpha1 "github.com/donaldgifford/renovate-operator/api/v1alpha1"
	"github.com/donaldgifford/renovate-operator/internal/credentials"
)

func ghAppRun(keyOverride string) *v1alpha1.RenovateRun {
	return &v1alpha1.RenovateRun{
		ObjectMeta: metav1.ObjectMeta{Name: "nightly-1", Namespace: "renovate", UID: types.UID("uid-1")},
		Spec: v1alpha1.RenovateRunSpec{
			ScanRef: v1alpha1.LocalObjectReference{Name: "nightly"},
			PlatformSnapshot: v1alpha1.RenovatePlatformSpec{
				PlatformType: v1alpha1.PlatformTypeGitHub,
				Auth: v1alpha1.PlatformAuth{
					GitHubApp: &v1alpha1.GitHubAppAuth{
						AppID:          1,
						InstallationID: 1,
						PrivateKeyRef:  v1alpha1.SecretKeyReference{Name: "src-app", Key: keyOverride},
					},
				},
			},
		},
	}
}

func tokenRun(keyOverride string) *v1alpha1.RenovateRun {
	return &v1alpha1.RenovateRun{
		ObjectMeta: metav1.ObjectMeta{Name: "nightly-2", Namespace: "renovate", UID: types.UID("uid-2")},
		Spec: v1alpha1.RenovateRunSpec{
			ScanRef: v1alpha1.LocalObjectReference{Name: "forgejo-nightly"},
			PlatformSnapshot: v1alpha1.RenovatePlatformSpec{
				PlatformType: v1alpha1.PlatformTypeForgejo,
				Auth: v1alpha1.PlatformAuth{
					Token: &v1alpha1.TokenAuth{SecretRef: v1alpha1.SecretKeyReference{Name: "src-tok", Key: keyOverride}},
				},
			},
		},
	}
}

func TestMirrorName_Truncation(t *testing.T) {
	t.Parallel()
	short := credentials.MirrorName("r")
	if short != "renovate-creds-r" {
		t.Errorf("MirrorName(short) = %q", short)
	}

	long := strings.Repeat("a", 80)
	got := credentials.MirrorName(long)
	if len(got) > 63 {
		t.Errorf("MirrorName length = %d, must be ≤ 63 (DNS-1123 label)", len(got))
	}
	if !strings.HasPrefix(got, "renovate-creds-") {
		t.Errorf("MirrorName(long) = %q, missing prefix", got)
	}
}

func TestAuthKey_DefaultsAndOverrides(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		run  *v1alpha1.RenovateRun
		want string
	}{
		{"github-default", ghAppRun(""), "private-key.pem"},
		{"github-override", ghAppRun("custom.pem"), "custom.pem"},
		{"github-whitespace-trim", ghAppRun("  spaced.pem  "), "spaced.pem"},
		{"token-default", tokenRun(""), "token"},
		{"token-override", tokenRun("api-key"), "api-key"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := credentials.AuthKey(tt.run)
			if err != nil {
				t.Fatalf("AuthKey err = %v", err)
			}
			if got != tt.want {
				t.Errorf("AuthKey = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestAuthKey_Errors(t *testing.T) {
	t.Parallel()

	if _, err := credentials.AuthKey(nil); !errors.Is(err, credentials.ErrNilRun) {
		t.Errorf("AuthKey(nil) err = %v, want ErrNilRun", err)
	}

	empty := &v1alpha1.RenovateRun{}
	if _, err := credentials.AuthKey(empty); err == nil {
		t.Error("AuthKey on empty Run should error")
	}
}

func TestSourceSecretName(t *testing.T) {
	t.Parallel()

	if got, _ := credentials.SourceSecretName(ghAppRun("")); got != "src-app" {
		t.Errorf("github SourceSecretName = %q", got)
	}
	if got, _ := credentials.SourceSecretName(tokenRun("")); got != "src-tok" {
		t.Errorf("token SourceSecretName = %q", got)
	}
	if _, err := credentials.SourceSecretName(nil); !errors.Is(err, credentials.ErrNilRun) {
		t.Errorf("nil SourceSecretName err = %v", err)
	}
	empty := &v1alpha1.RenovateRun{}
	if _, err := credentials.SourceSecretName(empty); err == nil {
		t.Error("empty SourceSecretName should error")
	}
}

func TestBuildMirror_GitHubApp(t *testing.T) {
	t.Parallel()

	run := ghAppRun("")
	src := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "src-app", Namespace: "renovate-system"},
		Data: map[string][]byte{
			"private-key.pem": []byte("-----BEGIN RSA-----\nfake\n-----END RSA-----"),
			"ca.crt":          []byte("ca-bytes"),
		},
	}

	got, err := credentials.BuildMirror(run, src)
	if err != nil {
		t.Fatalf("BuildMirror err = %v", err)
	}

	if got.Name != "renovate-creds-nightly-1" {
		t.Errorf("Name = %q", got.Name)
	}
	if got.Namespace != "renovate" {
		t.Errorf("Namespace = %q (should be Run's namespace)", got.Namespace)
	}
	if got.Labels[credentials.LabelManaged] != credentials.LabelManagedValue {
		t.Errorf("missing managed label: %+v", got.Labels)
	}
	if got.Labels["renovate.fartlab.dev/run"] != "nightly-1" || got.Labels["renovate.fartlab.dev/scan"] != "nightly" {
		t.Errorf("scan/run labels: %+v", got.Labels)
	}
	if len(got.OwnerReferences) != 1 || got.OwnerReferences[0].UID != "uid-1" {
		t.Errorf("owner ref: %+v", got.OwnerReferences)
	}
	if !*got.OwnerReferences[0].Controller {
		t.Error("owner ref Controller != true")
	}
	if string(got.Data["private-key.pem"]) != string(src.Data["private-key.pem"]) {
		t.Errorf("PEM not copied through")
	}
	if string(got.Data["ca.crt"]) != "ca-bytes" {
		t.Error("non-auth key not preserved")
	}

	// Mutating the source after building must not affect the mirror (deep copy).
	src.Data["private-key.pem"][0] = 'X'
	if got.Data["private-key.pem"][0] == 'X' {
		t.Error("BuildMirror leaked the source byte slice; should DeepCopy")
	}
}

func TestBuildMirror_Token(t *testing.T) {
	t.Parallel()

	run := tokenRun("")
	src := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "src-tok"},
		Data:       map[string][]byte{"token": []byte("supersecret")},
	}
	got, err := credentials.BuildMirror(run, src)
	if err != nil {
		t.Fatalf("BuildMirror err = %v", err)
	}
	if string(got.Data["token"]) != "supersecret" {
		t.Errorf("token not copied")
	}
}

func TestBuildMirror_Errors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		run     *v1alpha1.RenovateRun
		src     *corev1.Secret
		wantErr error
	}{
		{"nil-run", nil, &corev1.Secret{}, credentials.ErrNilRun},
		{"nil-src", ghAppRun(""), nil, nil},
		{
			"missing-key",
			ghAppRun(""),
			&corev1.Secret{Data: map[string][]byte{"other": []byte("x")}},
			credentials.ErrSourceMissingKey,
		},
		{
			"empty-key-value",
			ghAppRun(""),
			&corev1.Secret{Data: map[string][]byte{"private-key.pem": []byte("")}},
			credentials.ErrSourceMissingKey,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := credentials.BuildMirror(tt.run, tt.src)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
				t.Errorf("err = %v, want %v", err, tt.wantErr)
			}
		})
	}
}
