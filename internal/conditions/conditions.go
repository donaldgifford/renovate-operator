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

// Package conditions wraps meta.SetStatusCondition for the operator's
// condition surface. Reconcilers should not call meta.SetStatusCondition
// directly; the helpers here ensure observedGeneration and the reason set
// stay consistent across controllers.
package conditions

import (
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Condition types tracked by the operator. Defined per CRD in ADR-0004:
// RenovatePlatform → Ready; RenovateScan → Ready, Scheduled; RenovateRun →
// Started, Discovered, Succeeded, Failed.
const (
	TypeReady      = "Ready"
	TypeScheduled  = "Scheduled"
	TypeStarted    = "Started"
	TypeDiscovered = "Discovered"
	TypeSucceeded  = "Succeeded"
	TypeFailed     = "Failed"
)

// Reasons used across CRDs. Reasons are CamelCase and documented per
// condition site so kubectl wait / GitOps health checks have stable
// strings to match against.
const (
	// Platform Ready reasons.
	ReasonCredentialsResolved = "CredentialsResolved"
	ReasonSecretNotFound      = "SecretNotFound"
	ReasonKeyMissing          = "KeyMissing"
	ReasonAuthFailed          = "AuthFailed"
	ReasonPlatformUnreachable = "PlatformUnreachable"

	// Scan Ready reasons.
	ReasonInvalidSchedule  = "InvalidSchedule"
	ReasonPlatformNotReady = "PlatformNotReady"
	ReasonSuspended        = "Suspended"

	// Scan Scheduled reasons.
	ReasonNextRunComputed = "NextRunComputed"

	// Run Started / Discovered / Succeeded / Failed reasons.
	ReasonAdmitted          = "Admitted"
	ReasonDiscoveryComplete = "DiscoveryComplete"
	ReasonDiscoveryFailed   = "DiscoveryFailed"
	ReasonJobComplete       = "JobComplete"
	ReasonJobFailed         = "JobFailed"

	// Common.
	ReasonReconcileError = "ReconcileError"
)

// Set merges the supplied condition into conditions, deduplicating on Type
// and stamping LastTransitionTime when Status changes. ObservedGeneration
// is set on every Set call so consumers can tell whether the current
// condition is fresh.
//
// conditions must point at a slice owned by the resource's status (callers
// pass &obj.Status.Conditions). Returns true when the condition was added
// or its Status, Reason, or Message changed; false when it was a no-op.
func Set(conditions *[]metav1.Condition, condition metav1.Condition) bool {
	if conditions == nil {
		return false
	}
	if condition.LastTransitionTime.IsZero() {
		condition.LastTransitionTime = metav1.Now()
	}
	return meta.SetStatusCondition(conditions, condition)
}

// Get returns the condition with the supplied type, or nil if absent.
func Get(conditions []metav1.Condition, conditionType string) *metav1.Condition {
	return meta.FindStatusCondition(conditions, conditionType)
}

// IsTrue returns whether the condition with the supplied type is present
// and has Status == True.
func IsTrue(conditions []metav1.Condition, conditionType string) bool {
	return meta.IsStatusConditionTrue(conditions, conditionType)
}

// MarkTrue is a convenience constructor for a Status=True condition with
// the supplied type, reason, and message.
func MarkTrue(conditions *[]metav1.Condition, conditionType, reason, message string, observedGeneration int64) bool {
	return Set(conditions, metav1.Condition{
		Type:               conditionType,
		Status:             metav1.ConditionTrue,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: observedGeneration,
	})
}

// MarkFalse is a convenience constructor for a Status=False condition.
func MarkFalse(conditions *[]metav1.Condition, conditionType, reason, message string, observedGeneration int64) bool {
	return Set(conditions, metav1.Condition{
		Type:               conditionType,
		Status:             metav1.ConditionFalse,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: observedGeneration,
	})
}

// MarkUnknown is a convenience constructor for a Status=Unknown condition.
// Use sparingly — Kubernetes convention prefers True/False with a clear reason.
func MarkUnknown(conditions *[]metav1.Condition, conditionType, reason, message string, observedGeneration int64) bool {
	return Set(conditions, metav1.Condition{
		Type:               conditionType,
		Status:             metav1.ConditionUnknown,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: observedGeneration,
	})
}
