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
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	renovatev1alpha1 "github.com/donaldgifford/renovate-operator/api/v1alpha1"
	"github.com/donaldgifford/renovate-operator/internal/observability"
	"github.com/donaldgifford/renovate-operator/internal/platform"
)

// metric vectors are package-level globals so observations from one test bleed
// into another. The label space is keyed off (scan, platform[, result]); using
// unique scan names per test gives each case its own row, so reads via
// ToFloat64 see only that test's contribution.

func TestMetrics_HappyPathEmitsDiscoveryDurationAndShardCount(t *testing.T) {
	t.Parallel()
	run, src := runFixture("metrics-happy", "metrics-ns-happy", "renovate-system")
	run.Spec.ScanRef.Name = "scan-metrics-happy"
	plat := &stubPlatformClient{
		repos: []platform.Repository{
			{Slug: "metrics-ns-happy/repo-a"},
			{Slug: "metrics-ns-happy/repo-b"},
		},
	}
	r := newRunReconciler(t, plat, run, src)

	if _, err := r.Reconcile(context.Background(),
		reconcile.Request{NamespacedName: types.NamespacedName{Namespace: run.Namespace, Name: run.Name}}); err != nil {
		t.Fatalf("Reconcile err = %v", err)
	}

	// DiscoveryDurationSeconds: histogram has one observation for this label set.
	if got := testutil.CollectAndCount(observability.DiscoveryDurationSeconds, "renovate_operator_discovery_duration_seconds"); got == 0 {
		t.Errorf("DiscoveryDurationSeconds family not exported (count=%d)", got)
	}
	// ShardCount: gauge set to actualWorkers (1 for 2 repos / 50 perWorker, capped at min=1).
	if got := testutil.ToFloat64(observability.ShardCount.WithLabelValues("scan-metrics-happy", "github")); got != 1 {
		t.Errorf("ShardCount = %v, want 1", got)
	}
	// DiscoveryErrorsTotal: not bumped on happy path.
	if got := testutil.ToFloat64(observability.DiscoveryErrorsTotal.WithLabelValues("scan-metrics-happy", "github")); got != 0 {
		t.Errorf("DiscoveryErrorsTotal = %v, want 0 on happy path", got)
	}
}

func TestMetrics_DiscoveryErrorIncrementsCounter(t *testing.T) {
	t.Parallel()
	run, src := runFixture("metrics-discovery-err", "metrics-ns-derr", "renovate-system")
	run.Spec.ScanRef.Name = "scan-metrics-derr"
	plat := &stubPlatformClient{discoverErr: platform.ErrTransient}
	r := newRunReconciler(t, plat, run, src)

	if _, err := r.Reconcile(context.Background(),
		reconcile.Request{NamespacedName: types.NamespacedName{Namespace: run.Namespace, Name: run.Name}}); err != nil {
		t.Fatalf("Reconcile err = %v", err)
	}

	if got := testutil.ToFloat64(observability.DiscoveryErrorsTotal.WithLabelValues("scan-metrics-derr", "github")); got != 1 {
		t.Errorf("DiscoveryErrorsTotal = %v, want 1", got)
	}
}

func TestMetrics_ObserveJobSucceededIncrementsRunsTotal(t *testing.T) {
	t.Parallel()
	run, _ := runFixture("metrics-success", "metrics-ns-ok", "renovate-system")
	run.Spec.ScanRef.Name = "scan-metrics-ok"
	run.Status.Phase = renovatev1alpha1.RunPhaseRunning
	run.Status.StartTime = &metav1.Time{Time: time.Date(2026, 4, 26, 11, 59, 0, 0, time.UTC)}
	run.Status.WorkerJobRef = &corev1.ObjectReference{
		APIVersion: "batch/v1", Kind: "Job",
		Namespace: run.Namespace, Name: "metrics-success-workers",
	}
	completions := int32(2)
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: "metrics-success-workers", Namespace: run.Namespace},
		Spec:       batchv1.JobSpec{Completions: &completions},
		Status:     batchv1.JobStatus{Succeeded: 2},
	}
	r := newRunReconciler(t, nil, run, job)

	if _, err := r.Reconcile(context.Background(),
		reconcile.Request{NamespacedName: types.NamespacedName{Namespace: run.Namespace, Name: run.Name}}); err != nil {
		t.Fatalf("Reconcile err = %v", err)
	}

	if got := testutil.ToFloat64(observability.RunsTotal.WithLabelValues("scan-metrics-ok", "github", observability.ResultSucceeded)); got != 1 {
		t.Errorf("RunsTotal{result=succeeded} = %v, want 1", got)
	}
	if got := testutil.CollectAndCount(observability.RunDurationSeconds, "renovate_operator_run_duration_seconds"); got == 0 {
		t.Error("RunDurationSeconds family not exported on success")
	}
}

func TestMetrics_ObserveJobFailedIncrementsRunsAndShardsFailed(t *testing.T) {
	t.Parallel()
	run, _ := runFixture("metrics-jobfailed", "metrics-ns-jobfail", "renovate-system")
	run.Spec.ScanRef.Name = "scan-metrics-jobfail"
	run.Status.Phase = renovatev1alpha1.RunPhaseRunning
	run.Status.StartTime = &metav1.Time{Time: time.Date(2026, 4, 26, 11, 59, 0, 0, time.UTC)}
	run.Status.WorkerJobRef = &corev1.ObjectReference{
		APIVersion: "batch/v1", Kind: "Job",
		Namespace: run.Namespace, Name: "metrics-jobfailed-workers",
	}
	completions := int32(2)
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: "metrics-jobfailed-workers", Namespace: run.Namespace},
		Spec:       batchv1.JobSpec{Completions: &completions},
		Status: batchv1.JobStatus{
			Failed: 3,
			Conditions: []batchv1.JobCondition{
				{Type: batchv1.JobFailed, Status: corev1.ConditionTrue, Message: "exceeded backoffLimit"},
			},
		},
	}
	r := newRunReconciler(t, nil, run, job)

	if _, err := r.Reconcile(context.Background(),
		reconcile.Request{NamespacedName: types.NamespacedName{Namespace: run.Namespace, Name: run.Name}}); err != nil {
		t.Fatalf("Reconcile err = %v", err)
	}

	if got := testutil.ToFloat64(observability.RunsTotal.WithLabelValues("scan-metrics-jobfail", "github", observability.ResultFailed)); got != 1 {
		t.Errorf("RunsTotal{result=failed} = %v, want 1", got)
	}
	if got := testutil.ToFloat64(observability.ShardsFailedTotal.WithLabelValues("scan-metrics-jobfail", "github")); got != 3 {
		t.Errorf("ShardsFailedTotal = %v, want 3", got)
	}
}

func TestMetrics_ScanReconcileEmitsActiveRunsGauge(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 4, 26, 4, 1, 0, 0, time.UTC)
	scan := mkScan("metrics-active", "metrics-ns-active", "github", "0 4 * * *")
	scan.Status.LastRunTime = &metav1.Time{Time: time.Date(2026, 4, 23, 4, 0, 0, 0, time.UTC)}

	plat := mkPlatform("github", true)
	yes := true
	inflight := &renovatev1alpha1.RenovateRun{
		ObjectMeta: metav1.ObjectMeta{
			Name: "in-flight", Namespace: scan.Namespace,
			OwnerReferences: []metav1.OwnerReference{
				{APIVersion: renovatev1alpha1.GroupVersion.String(), Kind: "RenovateScan",
					Name: scan.Name, UID: scan.UID, Controller: &yes, BlockOwnerDeletion: &yes},
			},
		},
	}
	inflight.Status.Phase = renovatev1alpha1.RunPhaseRunning

	r := newScanReconciler(t, now, scan, plat, inflight)
	if _, err := r.Reconcile(context.Background(),
		reconcile.Request{NamespacedName: types.NamespacedName{Namespace: scan.Namespace, Name: scan.Name}}); err != nil {
		t.Fatalf("Reconcile err = %v", err)
	}

	if got := testutil.ToFloat64(observability.ActiveRuns.WithLabelValues("metrics-active", "github")); got != 1 {
		t.Errorf("ActiveRuns = %v, want 1", got)
	}
}

func TestMetrics_MarkFailedFromDiscoveryEmptyIncrementsRunsTotal(t *testing.T) {
	t.Parallel()
	run, src := runFixture("metrics-empty", "metrics-ns-empty", "renovate-system")
	run.Spec.ScanRef.Name = "scan-metrics-empty"
	plat := &stubPlatformClient{repos: nil}
	r := newRunReconciler(t, plat, run, src)

	if _, err := r.Reconcile(context.Background(),
		reconcile.Request{NamespacedName: types.NamespacedName{Namespace: run.Namespace, Name: run.Name}}); err != nil {
		t.Fatalf("Reconcile err = %v", err)
	}

	if got := testutil.ToFloat64(observability.RunsTotal.WithLabelValues("scan-metrics-empty", "github", observability.ResultFailed)); got != 1 {
		t.Errorf("RunsTotal{result=failed} = %v, want 1 (markFailed path)", got)
	}
}
