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

// Package credentials provides pure helpers for mirroring a credential
// Secret from the operator's release namespace into a Run's namespace.
// The Run reconciler does the I/O — this package only constructs the
// target Secret object so the construction is testable in isolation.
package credentials

import (
	"errors"
	"fmt"
	"maps"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apierrutil "k8s.io/apimachinery/pkg/util/validation"

	v1alpha1 "github.com/donaldgifford/renovate-operator/api/v1alpha1"
)

// LabelManaged marks a Secret as a per-Run mirror so the operator can
// confidently overwrite or delete it without colliding with user Secrets.
const LabelManaged = "renovate.fartlab.dev/managed"

// LabelManagedValue is the matching value.
const LabelManagedValue = "true"

const mirrorPrefix = "renovate-creds-"

// MirrorName returns the deterministic name of the mirrored Secret for a
// Run. Truncated to fit DNS-1123 limits when the Run's name is long.
func MirrorName(runName string) string {
	const maxLen = apierrutil.DNS1123LabelMaxLength
	budget := maxLen - len(mirrorPrefix)
	if len(runName) > budget {
		runName = runName[:budget]
	}
	return mirrorPrefix + runName
}

// ErrNilRun is returned when BuildMirror is called with a nil Run.
var ErrNilRun = errors.New("credentials: nil Run")

// ErrSourceMissingKey is returned when the source Secret lacks the auth
// key (PEM for GitHubApp, token for Token auth).
var ErrSourceMissingKey = errors.New("credentials: source Secret missing required key")

// AuthKey resolves the data key for a Run's snapshotted auth. Defaults to
// "private-key.pem" for GitHubApp and "token" for Token when the user-supplied
// SecretKeyReference.Key is empty. Returns ("", error) for malformed auth.
func AuthKey(run *v1alpha1.RenovateRun) (string, error) {
	if run == nil {
		return "", ErrNilRun
	}
	auth := run.Spec.PlatformSnapshot.Auth
	switch {
	case auth.GitHubApp != nil:
		key := strings.TrimSpace(auth.GitHubApp.PrivateKeyRef.Key)
		if key == "" {
			key = "private-key.pem"
		}
		return key, nil
	case auth.Token != nil:
		key := strings.TrimSpace(auth.Token.SecretRef.Key)
		if key == "" {
			key = "token"
		}
		return key, nil
	default:
		return "", fmt.Errorf("credentials: PlatformAuth has neither githubApp nor token set")
	}
}

// SourceSecretName returns the name of the upstream credential Secret in
// the operator's release namespace, derived from the Run's auth snapshot.
func SourceSecretName(run *v1alpha1.RenovateRun) (string, error) {
	if run == nil {
		return "", ErrNilRun
	}
	auth := run.Spec.PlatformSnapshot.Auth
	switch {
	case auth.GitHubApp != nil:
		return auth.GitHubApp.PrivateKeyRef.Name, nil
	case auth.Token != nil:
		return auth.Token.SecretRef.Name, nil
	default:
		return "", fmt.Errorf("credentials: PlatformAuth has neither githubApp nor token set")
	}
}

// BuildMirror constructs the destination Secret in the Run's namespace,
// copying the auth key out of the source Secret's Data. The caller is
// expected to client.Create or Patch the result.
//
// The Run owns the mirror via OwnerReference so cascade delete cleans up
// when the Run is GC'd by its parent Scan's history-limit policy.
func BuildMirror(run *v1alpha1.RenovateRun, source *corev1.Secret) (*corev1.Secret, error) {
	if run == nil {
		return nil, ErrNilRun
	}
	if source == nil {
		return nil, fmt.Errorf("credentials: nil source Secret")
	}
	key, err := AuthKey(run)
	if err != nil {
		return nil, err
	}
	val, ok := source.Data[key]
	if !ok || len(val) == 0 {
		return nil, fmt.Errorf("%w: %s", ErrSourceMissingKey, key)
	}

	yes := true
	dst := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      MirrorName(run.Name),
			Namespace: run.Namespace,
			Labels: map[string]string{
				LabelManaged:                LabelManagedValue,
				"renovate.fartlab.dev/run":  run.Name,
				"renovate.fartlab.dev/scan": run.Spec.ScanRef.Name,
			},
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion:         v1alpha1.GroupVersion.String(),
					Kind:               "RenovateRun",
					Name:               run.Name,
					UID:                run.UID,
					Controller:         &yes,
					BlockOwnerDeletion: &yes,
				},
			},
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{key: append([]byte(nil), val...)},
	}

	// Preserve any non-auth keys from the source (e.g., a CA bundle alongside
	// the App PEM). Skip if it's the same key — auth value takes precedence.
	for k, v := range maps.All(source.Data) {
		if k == key {
			continue
		}
		dst.Data[k] = append([]byte(nil), v...)
	}

	return dst, nil
}
