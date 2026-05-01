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
	"errors"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	clocktesting "k8s.io/utils/clock/testing"

	renovatev1alpha1 "github.com/donaldgifford/renovate-operator/api/v1alpha1"
	"github.com/donaldgifford/renovate-operator/internal/conditions"
	"github.com/donaldgifford/renovate-operator/internal/platform"
)

func TestIndexOf(t *testing.T) {
	t.Parallel()
	cases := []struct {
		s    string
		b    byte
		want int
	}{
		{"", '/', -1},
		{"foo", '/', -1},
		{"foo/bar", '/', 3},
		{"/leading", '/', 0},
		{"trailing/", '/', 8},
		{"a/b/c", '/', 1},
	}
	for _, tc := range cases {
		t.Run(tc.s, func(t *testing.T) {
			t.Parallel()
			if got := indexOf(tc.s, tc.b); got != tc.want {
				t.Errorf("indexOf(%q, %q) = %d, want %d", tc.s, tc.b, got, tc.want)
			}
		})
	}
}

func TestDiscoveryOwner(t *testing.T) {
	t.Parallel()
	r := &RenovateRunReconciler{}

	cases := []struct {
		name      string
		filter    []string
		namespace string
		want      string
	}{
		{
			name:      "no_filter_falls_back_to_namespace",
			filter:    nil,
			namespace: "default",
			want:      "default",
		},
		{
			name:      "first_filter_with_owner_prefix",
			filter:    []string{"my-org/repo-*"},
			namespace: "default",
			want:      "my-org",
		},
		{
			name:      "filter_without_slash_falls_back_to_namespace",
			filter:    []string{"plain-repo"},
			namespace: "default",
			want:      "default",
		},
		{
			name:      "first_with_slash_wins_over_later",
			filter:    []string{"plain-repo", "second-org/x"},
			namespace: "default",
			want:      "second-org",
		},
		{
			name:      "leading_slash_treated_as_no_owner",
			filter:    []string{"/foo"},
			namespace: "default",
			want:      "default",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			run := &renovatev1alpha1.RenovateRun{}
			run.Namespace = tc.namespace
			run.Spec.ScanSnapshot.Discovery.Filter = tc.filter
			if got := r.discoveryOwner(run); got != tc.want {
				t.Errorf("discoveryOwner = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestOwnerRefForRun(t *testing.T) {
	t.Parallel()
	run := &renovatev1alpha1.RenovateRun{}
	run.Name = "run-1"
	run.UID = types.UID("run-uid")

	ref := ownerRefForRun(run)
	if ref.Name != run.Name || ref.UID != run.UID {
		t.Errorf("ownerRef = {%s,%s}, want {%s,%s}", ref.Name, ref.UID, run.Name, run.UID)
	}
	if ref.Controller == nil || !*ref.Controller {
		t.Errorf("Controller = %v, want true", ref.Controller)
	}
	if ref.BlockOwnerDeletion == nil || !*ref.BlockOwnerDeletion {
		t.Errorf("BlockOwnerDeletion = %v, want true", ref.BlockOwnerDeletion)
	}
	if ref.Kind != "RenovateRun" {
		t.Errorf("Kind = %q, want RenovateRun", ref.Kind)
	}
}

func TestMarkFailed_SetsPhaseAndCondition(t *testing.T) {
	t.Parallel()
	fixed := time.Date(2026, 4, 26, 12, 0, 0, 0, time.UTC)
	r := &RenovateRunReconciler{Clock: clocktesting.NewFakeClock(fixed)}

	run := &renovatev1alpha1.RenovateRun{}
	res, err := r.markFailed(run, "test failure")
	if err != nil {
		t.Fatalf("markFailed err = %v", err)
	}
	if res.RequeueAfter != 0 {
		t.Errorf("RequeueAfter = %v, want 0 (terminal)", res.RequeueAfter)
	}
	if run.Status.Phase != renovatev1alpha1.RunPhaseFailed {
		t.Errorf("phase = %q, want %q", run.Status.Phase, renovatev1alpha1.RunPhaseFailed)
	}
	if run.Status.CompletionTime == nil || !run.Status.CompletionTime.Time.Equal(fixed) {
		t.Errorf("CompletionTime = %v, want %v", run.Status.CompletionTime, fixed)
	}
	cond := findCondition(run.Status.Conditions, conditions.TypeFailed)
	if cond == nil || cond.Status != metav1.ConditionTrue {
		t.Errorf("Failed condition = %v, want True", cond)
	}
}

func TestMarkTransient_RequeuesNotTerminal(t *testing.T) {
	t.Parallel()
	r := &RenovateRunReconciler{Clock: clocktesting.NewFakeClock(time.Now())}
	run := &renovatev1alpha1.RenovateRun{}

	res, err := r.markTransient(run, errors.New("upstream 503"), conditions.ReasonDiscoveryFailed)
	if err != nil {
		t.Fatalf("markTransient err = %v", err)
	}
	if res.RequeueAfter == 0 {
		t.Error("RequeueAfter = 0, want non-zero")
	}
	if run.Status.Phase != "" {
		t.Errorf("phase = %q, want empty (transient should not flip phase)", run.Status.Phase)
	}
	cond := findCondition(run.Status.Conditions, conditions.TypeStarted)
	if cond == nil || cond.Status != metav1.ConditionFalse {
		t.Errorf("Started condition = %v, want False", cond)
	}
}

func TestHandleDiscoverErr_ClassifiesTransient(t *testing.T) {
	t.Parallel()
	r := &RenovateRunReconciler{Clock: clocktesting.NewFakeClock(time.Now())}
	run := &renovatev1alpha1.RenovateRun{}

	res, err := r.handleDiscoverErr(run, platform.ErrTransient)
	if err != nil {
		t.Fatalf("handleDiscoverErr err = %v", err)
	}
	if res.RequeueAfter == 0 {
		t.Error("transient error should requeue")
	}
	if run.Status.Phase == renovatev1alpha1.RunPhaseFailed {
		t.Error("transient error should not mark Failed")
	}
}

func TestHandleDiscoverErr_PermanentMarksFailed(t *testing.T) {
	t.Parallel()
	r := &RenovateRunReconciler{Clock: clocktesting.NewFakeClock(time.Now())}
	run := &renovatev1alpha1.RenovateRun{}

	_, err := r.handleDiscoverErr(run, platform.ErrPermanent)
	if err != nil {
		t.Fatalf("handleDiscoverErr err = %v", err)
	}
	if run.Status.Phase != renovatev1alpha1.RunPhaseFailed {
		t.Errorf("phase = %q, want %q", run.Status.Phase, renovatev1alpha1.RunPhaseFailed)
	}
}

// findCondition is a local helper to avoid pulling apimeta in test code.
func findCondition(conds []metav1.Condition, t string) *metav1.Condition {
	for i := range conds {
		if conds[i].Type == t {
			return &conds[i]
		}
	}
	return nil
}

// silenceUnused is a compile-time guard so editing imports doesn't trip
// goimports if the helper above gets removed during refactors.
var _ = corev1.ConditionTrue
