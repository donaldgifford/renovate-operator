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
)

// RenovateRunSpec defines the desired state of RenovateRun.
//
// Runs are created by the Scan controller; users do not author them by hand.
// Both PlatformSnapshot and ScanSnapshot are frozen at creation time so a Run
// cannot change behavior because someone edited the parent Scan or Platform
// mid-execution.
type RenovateRunSpec struct {
	// ScanRef points at the parent RenovateScan in the same namespace. Set by
	// the Scan controller; not user-editable.
	ScanRef LocalObjectReference `json:"scanRef"`

	// PlatformSnapshot captures the Platform spec at Run creation. Frozen for
	// the lifetime of the Run.
	PlatformSnapshot RenovatePlatformSpec `json:"platformSnapshot"`

	// ScanSnapshot captures the Scan spec at Run creation. Frozen for the
	// lifetime of the Run.
	ScanSnapshot RenovateScanSpec `json:"scanSnapshot"`
}

// RenovateRunStatus defines the observed state of RenovateRun.
type RenovateRunStatus struct {
	// Conditions track Started, Discovered, Succeeded, and Failed. The Phase
	// field below is a derived cursor for printer columns and quick filtering;
	// conditions remain the source of truth.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// Phase is a typed cursor over the Run state machine.
	// +optional
	Phase RunPhase `json:"phase,omitempty"`

	// StartTime is when the Run controller first observed the Run and began
	// the Pending → Discovering transition.
	// +optional
	StartTime *metav1.Time `json:"startTime,omitempty"`

	// DiscoveryCompletionTime is when discovery and shard ConfigMap creation
	// finished, marking the Discovering → Running transition.
	// +optional
	DiscoveryCompletionTime *metav1.Time `json:"discoveryCompletionTime,omitempty"`

	// WorkersStartTime is when the worker Job became active.
	// +optional
	WorkersStartTime *metav1.Time `json:"workersStartTime,omitempty"`

	// CompletionTime is the terminal timestamp (Succeeded or Failed).
	// +optional
	CompletionTime *metav1.Time `json:"completionTime,omitempty"`

	// DiscoveredRepos is the count of repos that survived requireConfig and
	// the discovery filters.
	// +optional
	DiscoveredRepos int32 `json:"discoveredRepos,omitempty"`

	// ActualWorkers is clamp(ceil(DiscoveredRepos/reposPerWorker), min, max),
	// fixed at the Discovering → Running transition.
	// +optional
	ActualWorkers int32 `json:"actualWorkers,omitempty"`

	// ShardConfigMapRef points at the ConfigMap holding shard-NNNN.json keys
	// (or shard-NNNN.json.gz when the manifest exceeds 900 KiB).
	// +optional
	ShardConfigMapRef *corev1.ObjectReference `json:"shardConfigMapRef,omitempty"`

	// WorkerJobRef points at the owned Indexed Job.
	// +optional
	WorkerJobRef *corev1.ObjectReference `json:"workerJobRef,omitempty"`

	// SucceededShards mirrors batchv1.JobStatus.Succeeded for the owned Job.
	// +optional
	SucceededShards int32 `json:"succeededShards,omitempty"`

	// FailedShards mirrors batchv1.JobStatus.Failed for the owned Job.
	// +optional
	FailedShards int32 `json:"failedShards,omitempty"`

	// ObservedGeneration is the .metadata.generation value last reconciled.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:shortName=rr;rrun
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Scan",type="string",JSONPath=".spec.scanRef.name"
// +kubebuilder:printcolumn:name="Phase",type="string",JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="Repos",type="integer",JSONPath=".status.discoveredRepos"
// +kubebuilder:printcolumn:name="Workers",type="integer",JSONPath=".status.actualWorkers"
// +kubebuilder:printcolumn:name="Started",type="date",JSONPath=".status.startTime"
// +kubebuilder:printcolumn:name="Completed",type="date",JSONPath=".status.completionTime"

// RenovateRun is the Schema for the renovateruns API.
type RenovateRun struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of RenovateRun
	// +required
	Spec RenovateRunSpec `json:"spec"`

	// status defines the observed state of RenovateRun
	// +optional
	Status RenovateRunStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// RenovateRunList contains a list of RenovateRun.
type RenovateRunList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []RenovateRun `json:"items"`
}

func init() {
	SchemeBuilder.Register(&RenovateRun{}, &RenovateRunList{})
}
