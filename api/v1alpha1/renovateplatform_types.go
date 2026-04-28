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

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// RenovatePlatformSpec defines the desired state of RenovatePlatform.
//
// +kubebuilder:validation:XValidation:rule="self.platformType != 'forgejo' || has(self.auth.token)",message="forgejo platforms require token auth"
// +kubebuilder:validation:XValidation:rule="self.platformType != 'forgejo' || size(self.baseURL) > 0",message="forgejo platforms require baseURL"
type RenovatePlatformSpec struct {
	// PlatformType is the Renovate platform identifier.
	PlatformType PlatformType `json:"platformType"`

	// BaseURL is the platform API endpoint. github defaults to
	// https://api.github.com when empty; forgejo has no default and must be
	// set explicitly.
	// +optional
	BaseURL string `json:"baseURL,omitempty"`

	// Auth is the platform authentication configuration. Exactly one of
	// auth.githubApp or auth.token must be set.
	Auth PlatformAuth `json:"auth"`

	// RunnerConfig is an opaque JSON blob passed to Renovate workers as
	// RENOVATE_CONFIG. Use it for runner-level settings (binarySource, dryRun,
	// hostRules, onboarding, etc.) that should apply across every Scan on this
	// platform. Per-Scan layering happens via RenovateScanSpec.RenovateConfigOverrides.
	// +kubebuilder:pruning:PreserveUnknownFields
	// +optional
	RunnerConfig *runtime.RawExtension `json:"runnerConfig,omitempty"`

	// PresetRepoRef is the Renovate preset reference (e.g.,
	// "github>donaldgifford/renovate-config"). Workers receive it as an
	// extends entry prepended to each repo's own renovate.json.
	// +optional
	PresetRepoRef string `json:"presetRepoRef,omitempty"`

	// RenovateImage is the container image used by worker pods.
	// +kubebuilder:default="ghcr.io/renovatebot/renovate:latest"
	// +optional
	RenovateImage string `json:"renovateImage,omitempty"`
}

// PlatformAuth is a discriminated union over the supported credential shapes.
// +kubebuilder:validation:XValidation:rule="(has(self.githubApp) ? 1 : 0) + (has(self.token) ? 1 : 0) == 1",message="exactly one of githubApp or token must be set"
type PlatformAuth struct {
	// GitHubApp configures GitHub App installation auth. Required when
	// platformType is github and the org uses an App; the operator mints
	// installation tokens on each Run.
	// +optional
	GitHubApp *GitHubAppAuth `json:"githubApp,omitempty"`

	// Token configures personal-access-token / Forgejo-token auth.
	// +optional
	Token *TokenAuth `json:"token,omitempty"`
}

// GitHubAppAuth carries the per-installation pointers needed to mint a
// GitHub App installation token. Each installation should have its own
// RenovatePlatform; the App may be installed in many orgs.
type GitHubAppAuth struct {
	// AppID is the GitHub App's numeric ID.
	// +kubebuilder:validation:Minimum=1
	AppID int64 `json:"appID"`

	// InstallationID scopes auth to a single installation. Required.
	// If the App is installed on multiple orgs, declare one RenovatePlatform
	// per installation rather than overloading a single resource.
	// +kubebuilder:validation:Minimum=1
	InstallationID int64 `json:"installationID"`

	// PrivateKeyRef references a Secret containing the App's PEM private key.
	// Defaults to the "private-key.pem" key when SecretKeyReference.Key is empty.
	PrivateKeyRef SecretKeyReference `json:"privateKeyRef"`
}

// TokenAuth carries the pointer to a token Secret.
type TokenAuth struct {
	// SecretRef references a Secret containing the platform token.
	// Defaults to the "token" key when SecretKeyReference.Key is empty.
	SecretRef SecretKeyReference `json:"secretRef"`
}

// RenovatePlatformStatus defines the observed state of RenovatePlatform.
type RenovatePlatformStatus struct {
	// Conditions represent the current state of the RenovatePlatform resource.
	// The Ready condition is the source of truth; reasons include
	// CredentialsResolved, SecretNotFound, KeyMissing, AuthFailed, and
	// PlatformUnreachable.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// ObservedGeneration is the .metadata.generation value last reconciled.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster,shortName=rp;rplatform
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Type",type="string",JSONPath=".spec.platformType"
// +kubebuilder:printcolumn:name="URL",type="string",JSONPath=".spec.baseURL"
// +kubebuilder:printcolumn:name="Ready",type="string",JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// RenovatePlatform is the Schema for the renovateplatforms API.
type RenovatePlatform struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of RenovatePlatform
	// +required
	Spec RenovatePlatformSpec `json:"spec"`

	// status defines the observed state of RenovatePlatform
	// +optional
	Status RenovatePlatformStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// RenovatePlatformList contains a list of RenovatePlatform.
type RenovatePlatformList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []RenovatePlatform `json:"items"`
}

func init() {
	SchemeBuilder.Register(&RenovatePlatform{}, &RenovatePlatformList{})
}
