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
	"fmt"

	corev1 "k8s.io/api/core/v1"

	v1alpha1 "github.com/donaldgifford/renovate-operator/api/v1alpha1"
	"github.com/donaldgifford/renovate-operator/internal/platform"
	"github.com/donaldgifford/renovate-operator/internal/platform/forgejo"
	"github.com/donaldgifford/renovate-operator/internal/platform/github"
)

// Default Secret data keys, shared with the Platform controller's resolver.
const (
	defaultGitHubAppPEMKey = "private-key.pem"
	defaultTokenKey        = "token"
)

// PlatformClientFactory builds a platform.Client from a Run's frozen
// snapshot plus the resolved (mirrored) credential Secret. Reconcilers
// take it as a dependency so tests can substitute a stub.
type PlatformClientFactory func(ctx context.Context, snapshot v1alpha1.RenovatePlatformSpec, secret *corev1.Secret) (platform.Client, error)

// DefaultPlatformClientFactory returns the production factory: builds a
// GitHub or Forgejo client per snapshot.PlatformType.
func DefaultPlatformClientFactory() PlatformClientFactory {
	return func(_ context.Context, snapshot v1alpha1.RenovatePlatformSpec, secret *corev1.Secret) (platform.Client, error) {
		switch snapshot.PlatformType {
		case v1alpha1.PlatformTypeGitHub:
			if snapshot.Auth.GitHubApp == nil {
				return nil, fmt.Errorf("controller: github platform requires githubApp auth")
			}
			key := snapshot.Auth.GitHubApp.PrivateKeyRef.Key
			if key == "" {
				key = defaultGitHubAppPEMKey
			}
			pem, ok := secret.Data[key]
			if !ok {
				return nil, fmt.Errorf("controller: secret %s missing key %q", secret.Name, key)
			}
			return github.NewWithApp(github.AppAuth{
				AppID:          snapshot.Auth.GitHubApp.AppID,
				InstallationID: snapshot.Auth.GitHubApp.InstallationID,
				PEM:            pem,
				BaseURL:        snapshot.BaseURL,
			})
		case v1alpha1.PlatformTypeForgejo:
			if snapshot.Auth.Token == nil {
				return nil, fmt.Errorf("controller: forgejo platform requires token auth")
			}
			key := snapshot.Auth.Token.SecretRef.Key
			if key == "" {
				key = defaultTokenKey
			}
			tok, ok := secret.Data[key]
			if !ok {
				return nil, fmt.Errorf("controller: secret %s missing key %q", secret.Name, key)
			}
			return forgejo.New(forgejo.Auth{BaseURL: snapshot.BaseURL, Token: string(tok)})
		default:
			return nil, fmt.Errorf("controller: unknown platformType %q", snapshot.PlatformType)
		}
	}
}
