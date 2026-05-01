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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// RenovateScanSpec defines the desired state of RenovateScan.
//
// Field shape consciously borrows from batch/v1.CronJob (schedule, suspend,
// concurrencyPolicy, timeZone, *HistoryLimit) so that operators familiar with
// CronJob have low cognitive overhead.
type RenovateScanSpec struct {
	// PlatformRef points at the cluster-scoped RenovatePlatform this Scan
	// runs against.
	PlatformRef LocalObjectReference `json:"platformRef"`

	// Schedule is a cron expression evaluated in TimeZone. Standard 5-field
	// cron syntax (robfig/cron/v3 parser).
	// +kubebuilder:validation:MinLength=1
	Schedule string `json:"schedule"`

	// TimeZone is the IANA zone the cron expression is evaluated in.
	// +kubebuilder:default=UTC
	// +optional
	TimeZone string `json:"timeZone,omitempty"`

	// Suspend pauses scheduling without deleting the Scan or its history.
	// +kubebuilder:default=false
	// +optional
	Suspend bool `json:"suspend,omitempty"`

	// ConcurrencyPolicy controls behavior when a scheduled fire-time arrives
	// while a non-terminal Run still exists for this Scan.
	// +kubebuilder:default=Forbid
	// +optional
	ConcurrencyPolicy ConcurrencyPolicy `json:"concurrencyPolicy,omitempty"`

	// Workers controls per-Run parallelism: how many Indexed Job workers run,
	// the repos-per-worker target, and the per-index backoff limit.
	// +optional
	Workers WorkersSpec `json:"workers,omitempty"`

	// Discovery configures repo enumeration: which repos a Run targets.
	// +optional
	Discovery DiscoverySpec `json:"discovery,omitempty"`

	// RenovateConfigOverrides is layered on top of Platform.spec.runnerConfig
	// for Runs of this Scan. Field-by-field merge with Scan winning on
	// collision.
	// +kubebuilder:pruning:PreserveUnknownFields
	// +optional
	RenovateConfigOverrides *runtime.RawExtension `json:"renovateConfigOverrides,omitempty"`

	// ExtraEnv appends environment variables to the worker container, after
	// the operator's RENOVATE_* defaults. Use sparingly; prefer renovateConfig.
	// +optional
	ExtraEnv []corev1.EnvVar `json:"extraEnv,omitempty"`

	// Resources sets the worker container's resource requests and limits.
	// Falls back to chart defaults when nil.
	// +optional
	Resources *corev1.ResourceRequirements `json:"resources,omitempty"`

	// SuccessfulRunsHistoryLimit retains this many terminal-Succeeded Runs
	// before garbage-collecting the oldest.
	// +kubebuilder:default=3
	// +kubebuilder:validation:Minimum=0
	// +optional
	SuccessfulRunsHistoryLimit *int32 `json:"successfulRunsHistoryLimit,omitempty"`

	// FailedRunsHistoryLimit retains this many terminal-Failed Runs before
	// garbage-collecting the oldest.
	// +kubebuilder:default=1
	// +kubebuilder:validation:Minimum=0
	// +optional
	FailedRunsHistoryLimit *int32 `json:"failedRunsHistoryLimit,omitempty"`
}

// WorkersSpec controls Indexed Job sharding for a Run. The actual worker
// count is clamp(ceil(repos/reposPerWorker), minWorkers, maxWorkers).
type WorkersSpec struct {
	// MinWorkers is the lower bound on Indexed Job parallelism. The controller
	// always launches at least this many workers, even for empty discovery.
	// +kubebuilder:default=1
	// +kubebuilder:validation:Minimum=1
	// +optional
	MinWorkers int32 `json:"minWorkers,omitempty"`

	// MaxWorkers is the upper bound on Indexed Job parallelism.
	// +kubebuilder:default=10
	// +kubebuilder:validation:Minimum=1
	// +optional
	MaxWorkers int32 `json:"maxWorkers,omitempty"`

	// ReposPerWorker is the soft target for shard size. The controller picks
	// actualWorkers = clamp(ceil(repos/reposPerWorker), Min, Max).
	// +kubebuilder:default=50
	// +kubebuilder:validation:Minimum=1
	// +optional
	ReposPerWorker int32 `json:"reposPerWorker,omitempty"`

	// BackoffLimitPerIndex bounds retries for each shard before the worker
	// Job fails. Set to 0 for no retries.
	// +kubebuilder:default=2
	// +kubebuilder:validation:Minimum=0
	// +optional
	BackoffLimitPerIndex *int32 `json:"backoffLimitPerIndex,omitempty"`
}

// DiscoverySpec configures repo enumeration for a Run. Defaults are tuned
// for org-wide scans where most repos opt in via a renovate.json file.
type DiscoverySpec struct {
	// Autodiscover enables platform-side repo enumeration. When false the
	// Scan's own renovateConfigOverrides must supply the repository list.
	// +kubebuilder:default=true
	// +optional
	Autodiscover bool `json:"autodiscover,omitempty"`

	// RequireConfig drops repos that lack a Renovate config in their default
	// branch. STRONGLY recommended for org-wide scans to avoid mass
	// onboarding-PR generation.
	// +kubebuilder:default=true
	// +optional
	RequireConfig bool `json:"requireConfig,omitempty"`

	// Filter is a list of Renovate-style autodiscover filters
	// (e.g., "owner/*", "owner/prefix-*"). Empty means no filter — all repos
	// the credentials can see are eligible.
	// +optional
	Filter []string `json:"filter,omitempty"`

	// Topics restricts to repos with at least one of the listed topics.
	// GitHub only; ignored for Forgejo.
	// +optional
	Topics []string `json:"topics,omitempty"`

	// SkipForks drops forked repos from discovery.
	// +kubebuilder:default=true
	// +optional
	SkipForks bool `json:"skipForks,omitempty"`

	// SkipArchived drops archived repos from discovery.
	// +kubebuilder:default=true
	// +optional
	SkipArchived bool `json:"skipArchived,omitempty"`
}

// RenovateScanStatus defines the observed state of RenovateScan.
type RenovateScanStatus struct {
	// Conditions represent the current state of the Scan. Tracked types:
	// Ready (overall scheduling readiness; reasons include InvalidSchedule,
	// PlatformNotReady, Suspended, Scheduled) and Scheduled (set true when
	// the next-fire-time has been computed and a Run has been queued).
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// LastRunTime is the most recent fire time the controller observed.
	// +optional
	LastRunTime *metav1.Time `json:"lastRunTime,omitempty"`

	// LastSuccessfulRunTime is the most recent fire time whose Run reached Succeeded.
	// +optional
	LastSuccessfulRunTime *metav1.Time `json:"lastSuccessfulRunTime,omitempty"`

	// NextRunTime is the next scheduled fire time, computed from spec.schedule
	// in spec.timeZone.
	// +optional
	NextRunTime *metav1.Time `json:"nextRunTime,omitempty"`

	// LastRunRef points at the most recent RenovateRun owned by this Scan.
	// +optional
	LastRunRef *corev1.ObjectReference `json:"lastRunRef,omitempty"`

	// ActiveRuns lists non-terminal Runs the controller currently owns. Used
	// to evaluate ConcurrencyPolicy at fire time.
	// +optional
	ActiveRuns []corev1.ObjectReference `json:"activeRuns,omitempty"`

	// ObservedGeneration is the .metadata.generation value last reconciled.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:shortName=rscan
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Platform",type="string",JSONPath=".spec.platformRef.name"
// +kubebuilder:printcolumn:name="Schedule",type="string",JSONPath=".spec.schedule"
// +kubebuilder:printcolumn:name="Last Run",type="date",JSONPath=".status.lastRunTime"
// +kubebuilder:printcolumn:name="Next Run",type="date",JSONPath=".status.nextRunTime"
// +kubebuilder:printcolumn:name="Ready",type="string",JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// RenovateScan is the Schema for the renovatescans API.
type RenovateScan struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of RenovateScan
	// +required
	Spec RenovateScanSpec `json:"spec"`

	// status defines the observed state of RenovateScan
	// +optional
	Status RenovateScanStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// RenovateScanList contains a list of RenovateScan.
type RenovateScanList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []RenovateScan `json:"items"`
}

func init() {
	SchemeBuilder.Register(&RenovateScan{}, &RenovateScanList{})
}
