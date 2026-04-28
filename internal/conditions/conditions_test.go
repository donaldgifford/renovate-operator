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

package conditions_test

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/donaldgifford/renovate-operator/internal/conditions"
)

func TestSetAddsNewCondition(t *testing.T) {
	t.Parallel()

	var got []metav1.Condition
	changed := conditions.MarkTrue(&got, conditions.TypeReady, conditions.ReasonCredentialsResolved, "ok", 1)

	if !changed {
		t.Fatal("MarkTrue on empty slice should report changed=true")
	}
	if len(got) != 1 || got[0].Type != conditions.TypeReady || got[0].Status != metav1.ConditionTrue {
		t.Fatalf("unexpected conditions: %+v", got)
	}
	if got[0].LastTransitionTime.IsZero() {
		t.Error("LastTransitionTime should be set")
	}
	if got[0].ObservedGeneration != 1 {
		t.Errorf("ObservedGeneration = %d, want 1", got[0].ObservedGeneration)
	}
}

func TestSetIsNoOpWhenStatusUnchanged(t *testing.T) {
	t.Parallel()

	var got []metav1.Condition
	conditions.MarkTrue(&got, conditions.TypeReady, conditions.ReasonCredentialsResolved, "ok", 1)
	first := got[0].LastTransitionTime

	changed := conditions.MarkTrue(&got, conditions.TypeReady, conditions.ReasonCredentialsResolved, "ok", 1)
	if changed {
		t.Fatal("MarkTrue with identical condition should report changed=false")
	}
	if !got[0].LastTransitionTime.Equal(&first) {
		t.Error("LastTransitionTime should not bump on unchanged status")
	}
}

func TestSetUpdatesOnStatusFlip(t *testing.T) {
	t.Parallel()

	var got []metav1.Condition
	conditions.MarkTrue(&got, conditions.TypeReady, conditions.ReasonCredentialsResolved, "ok", 1)

	changed := conditions.MarkFalse(&got, conditions.TypeReady, conditions.ReasonAuthFailed, "bad token", 2)
	if !changed {
		t.Fatal("MarkFalse after MarkTrue should report changed=true")
	}
	if got[0].Status != metav1.ConditionFalse || got[0].Reason != conditions.ReasonAuthFailed {
		t.Errorf("condition not updated: %+v", got[0])
	}
	if got[0].ObservedGeneration != 2 {
		t.Errorf("ObservedGeneration = %d, want 2", got[0].ObservedGeneration)
	}
}

func TestSetIgnoresNilSlicePointer(t *testing.T) {
	t.Parallel()

	if conditions.Set(nil, metav1.Condition{Type: conditions.TypeReady}) {
		t.Error("Set with nil pointer should report changed=false")
	}
}

func TestGetAndIsTrue(t *testing.T) {
	t.Parallel()

	var got []metav1.Condition
	conditions.MarkTrue(&got, conditions.TypeReady, conditions.ReasonCredentialsResolved, "", 1)
	conditions.MarkFalse(&got, conditions.TypeScheduled, conditions.ReasonInvalidSchedule, "", 1)

	if c := conditions.Get(got, conditions.TypeReady); c == nil || c.Status != metav1.ConditionTrue {
		t.Errorf("Get(Ready) = %+v, want True", c)
	}
	if !conditions.IsTrue(got, conditions.TypeReady) {
		t.Error("IsTrue(Ready) = false, want true")
	}
	if conditions.IsTrue(got, conditions.TypeScheduled) {
		t.Error("IsTrue(Scheduled) = true, want false (status is False)")
	}
	if c := conditions.Get(got, conditions.TypeStarted); c != nil {
		t.Errorf("Get(Started) = %+v, want nil", c)
	}
}

func TestMarkUnknown(t *testing.T) {
	t.Parallel()

	var got []metav1.Condition
	if !conditions.MarkUnknown(&got, conditions.TypeStarted, conditions.ReasonReconcileError, "transient", 0) {
		t.Fatal("MarkUnknown should report changed=true on first set")
	}
	if got[0].Status != metav1.ConditionUnknown {
		t.Errorf("Status = %s, want Unknown", got[0].Status)
	}
}
