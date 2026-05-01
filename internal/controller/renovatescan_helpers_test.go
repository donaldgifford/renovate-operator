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
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	renovatev1alpha1 "github.com/donaldgifford/renovate-operator/api/v1alpha1"
)

// Pure-helper coverage: these tests exercise parseSchedule, computeFireTimes,
// shouldSkipForConcurrency, isTerminal, completionOrCreation, capRequeueAfter,
// and ownedByScan without spinning up envtest. The full reconcile loop is
// covered by suite_test.go's envtest harness.

func TestParseSchedule_Variants(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		expr    string
		tz      string
		wantTZ  string
		wantErr bool
	}{
		{name: "weekly_utc_default", expr: "0 4 * * 0", tz: "", wantTZ: "UTC"},
		{name: "every_minute_explicit_utc", expr: "* * * * *", tz: "UTC", wantTZ: "UTC"},
		{name: "every_minute_la", expr: "* * * * *", tz: "America/Los_Angeles", wantTZ: "America/Los_Angeles"},
		{name: "invalid_tz", expr: "0 4 * * 0", tz: "Mars/Olympus", wantErr: true},
		{name: "invalid_cron", expr: "@every 1h", tz: "UTC", wantErr: true}, // 5-field parser doesn't support @every
		{name: "missing_field", expr: "0 4 * *", tz: "UTC", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			loc, sched, err := parseSchedule(tc.expr, tc.tz)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parseSchedule(%q,%q) err = nil, want non-nil", tc.expr, tc.tz)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseSchedule err = %v", err)
			}
			if loc.String() != tc.wantTZ {
				t.Errorf("loc = %q, want %q", loc.String(), tc.wantTZ)
			}
			if sched == nil {
				t.Errorf("schedule = nil, want non-nil")
			}
		})
	}
}

func TestComputeFireTimes_NoLastRun(t *testing.T) {
	t.Parallel()
	loc, sched, err := parseSchedule("0 4 * * *", "UTC") // daily at 04:00 UTC
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	// "now" is 03:00 UTC — no missed fire from a fresh start, next is 04:00.
	now := time.Date(2026, 4, 26, 3, 0, 0, 0, time.UTC)
	missed, next := computeFireTimes(sched, nil, now, loc)
	if !missed.IsZero() {
		t.Errorf("missed = %v, want zero (no lastRun, no retroactive fires)", missed)
	}
	wantNext := time.Date(2026, 4, 26, 4, 0, 0, 0, time.UTC)
	if !next.Equal(wantNext) {
		t.Errorf("next = %v, want %v", next, wantNext)
	}
}

func TestComputeFireTimes_LastRunWithMissed(t *testing.T) {
	t.Parallel()
	loc, sched, err := parseSchedule("0 4 * * *", "UTC")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	// Last fired Apr 23 04:00. "now" is Apr 26 04:30 — three missed fires
	// (Apr 24, 25, 26). The most recent missed should be Apr 26 04:00.
	lastRun := metav1.NewTime(time.Date(2026, 4, 23, 4, 0, 0, 0, time.UTC))
	now := time.Date(2026, 4, 26, 4, 30, 0, 0, time.UTC)
	missed, next := computeFireTimes(sched, &lastRun, now, loc)

	wantMissed := time.Date(2026, 4, 26, 4, 0, 0, 0, time.UTC)
	if !missed.Equal(wantMissed) {
		t.Errorf("missed = %v, want %v", missed, wantMissed)
	}
	wantNext := time.Date(2026, 4, 27, 4, 0, 0, 0, time.UTC)
	if !next.Equal(wantNext) {
		t.Errorf("next = %v, want %v", next, wantNext)
	}
}

func TestShouldSkipForConcurrency(t *testing.T) {
	t.Parallel()

	mkScan := func(policy renovatev1alpha1.ConcurrencyPolicy, active int) *renovatev1alpha1.RenovateScan {
		s := &renovatev1alpha1.RenovateScan{}
		s.Spec.ConcurrencyPolicy = policy
		s.Status.ActiveRuns = make([]corev1.ObjectReference, active)
		return s
	}

	cases := []struct {
		name string
		scan *renovatev1alpha1.RenovateScan
		want bool
	}{
		{"no_active_forbid", mkScan(renovatev1alpha1.ForbidConcurrent, 0), false},
		{"no_active_allow", mkScan(renovatev1alpha1.AllowConcurrent, 0), false},
		{"active_forbid", mkScan(renovatev1alpha1.ForbidConcurrent, 1), true},
		{"active_allow", mkScan(renovatev1alpha1.AllowConcurrent, 2), false},
		{"active_replace_degrades_to_forbid", mkScan(renovatev1alpha1.ReplaceConcurrent, 1), true},
		{"active_default_forbid", mkScan("", 1), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := shouldSkipForConcurrency(tc.scan)
			if got != tc.want {
				t.Errorf("shouldSkipForConcurrency = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestIsTerminal(t *testing.T) {
	t.Parallel()
	cases := []struct {
		phase renovatev1alpha1.RunPhase
		want  bool
	}{
		{renovatev1alpha1.RunPhasePending, false},
		{renovatev1alpha1.RunPhaseDiscovering, false},
		{renovatev1alpha1.RunPhaseRunning, false},
		{renovatev1alpha1.RunPhaseSucceeded, true},
		{renovatev1alpha1.RunPhaseFailed, true},
		{renovatev1alpha1.RunPhase(""), false},
	}
	for _, tc := range cases {
		t.Run(string(tc.phase), func(t *testing.T) {
			t.Parallel()
			if got := isTerminal(tc.phase); got != tc.want {
				t.Errorf("isTerminal(%q) = %v, want %v", tc.phase, got, tc.want)
			}
		})
	}
}

func TestCompletionOrCreation_PrefersCompletion(t *testing.T) {
	t.Parallel()
	created := metav1.NewTime(time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC))
	completed := metav1.NewTime(time.Date(2026, 4, 2, 0, 0, 0, 0, time.UTC))
	run := renovatev1alpha1.RenovateRun{}
	run.CreationTimestamp = created
	run.Status.CompletionTime = &completed
	got := completionOrCreation(run)
	if !got.Equal(completed.Time) {
		t.Errorf("got %v, want %v (completion)", got, completed.Time)
	}
}

func TestCompletionOrCreation_FallsBackToCreation(t *testing.T) {
	t.Parallel()
	created := metav1.NewTime(time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC))
	run := renovatev1alpha1.RenovateRun{}
	run.CreationTimestamp = created
	got := completionOrCreation(run)
	if !got.Equal(created.Time) {
		t.Errorf("got %v, want %v (creation)", got, created.Time)
	}
}

func TestCapRequeueAfter(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   time.Duration
		want time.Duration
	}{
		{-1 * time.Second, time.Second},
		{0, time.Second},
		{30 * time.Second, 30 * time.Second},
		{requeueAfterMax, requeueAfterMax},
		{2 * requeueAfterMax, requeueAfterMax},
	}
	for _, tc := range cases {
		t.Run(tc.in.String(), func(t *testing.T) {
			t.Parallel()
			if got := capRequeueAfter(tc.in); got != tc.want {
				t.Errorf("capRequeueAfter(%v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestOwnedByScan(t *testing.T) {
	t.Parallel()
	scan := &renovatev1alpha1.RenovateScan{}
	scan.UID = types.UID("scan-uid")

	owned := &renovatev1alpha1.RenovateRun{}
	owned.OwnerReferences = []metav1.OwnerReference{{UID: scan.UID}}
	if !ownedByScan(owned, scan) {
		t.Errorf("ownedByScan(matching uid) = false, want true")
	}

	notOwned := &renovatev1alpha1.RenovateRun{}
	notOwned.OwnerReferences = []metav1.OwnerReference{{UID: types.UID("other")}}
	if ownedByScan(notOwned, scan) {
		t.Errorf("ownedByScan(different uid) = true, want false")
	}

	noOwners := &renovatev1alpha1.RenovateRun{}
	if ownedByScan(noOwners, scan) {
		t.Errorf("ownedByScan(no owner refs) = true, want false")
	}
}
