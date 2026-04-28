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

// SecretKeyReference points to a single key inside a Secret in the operator's
// release namespace. The Platform controller resolves the Secret in its own
// namespace; cross-namespace references are not supported.
type SecretKeyReference struct {
	// Name is the Secret's metadata.name.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// Key is the data key within the Secret. When empty, callers fall back to
	// the field-specific default (e.g., "token" for token auth, "private-key.pem"
	// for GitHub App auth).
	// +optional
	Key string `json:"key,omitempty"`
}

// LocalObjectReference points to another resource by name. The referenced
// resource lives in a scope determined by the field's documentation: a
// PlatformRef on a Scan resolves cluster-scoped (RenovatePlatform), while a
// ScanRef on a Run resolves within the Run's own namespace.
type LocalObjectReference struct {
	// Name of the referent.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
}

// ConcurrencyPolicy mirrors batch/v1.CronJob's concurrency semantics for
// RenovateScan. v0.1.0 honors Allow and Forbid; Replace is accepted but
// degrades to Forbid behavior with a warning event.
// +kubebuilder:validation:Enum=Allow;Forbid;Replace
type ConcurrencyPolicy string

const (
	// AllowConcurrent permits concurrent Runs for the same Scan.
	AllowConcurrent ConcurrencyPolicy = "Allow"

	// ForbidConcurrent skips a scheduled Run while another Run for the same
	// Scan is non-terminal.
	ForbidConcurrent ConcurrencyPolicy = "Forbid"

	// ReplaceConcurrent is accepted for CronJob compatibility; in v0.1.0 it
	// behaves identically to Forbid (a warning event documents the divergence).
	ReplaceConcurrent ConcurrencyPolicy = "Replace"
)

// PlatformType is the Renovate platform identifier.
// +kubebuilder:validation:Enum=github;forgejo
type PlatformType string

const (
	// PlatformTypeGitHub targets github.com or a GitHub Enterprise instance.
	PlatformTypeGitHub PlatformType = "github"

	// PlatformTypeForgejo targets a Forgejo or Gitea instance.
	PlatformTypeForgejo PlatformType = "forgejo"
)

// RunPhase is a typed cursor over the Run state machine. Conditions remain
// the source of truth; phase exists for printer columns and quick filtering.
// +kubebuilder:validation:Enum=Pending;Discovering;Running;Succeeded;Failed
type RunPhase string

const (
	// RunPhasePending is the initial phase before the controller observes the Run.
	RunPhasePending RunPhase = "Pending"

	// RunPhaseDiscovering covers credential mirroring, repo enumeration,
	// requireConfig filtering, shard ConfigMap creation, and worker Job creation.
	RunPhaseDiscovering RunPhase = "Discovering"

	// RunPhaseRunning is set once the worker Job exists and shards are executing.
	RunPhaseRunning RunPhase = "Running"

	// RunPhaseSucceeded is terminal: every shard completed successfully.
	RunPhaseSucceeded RunPhase = "Succeeded"

	// RunPhaseFailed is terminal: the worker Job exhausted its backoffLimitPerIndex
	// or discovery hit a permanent error.
	RunPhaseFailed RunPhase = "Failed"
)
