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
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	schema "k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	clocktesting "k8s.io/utils/clock/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	renovatev1alpha1 "github.com/donaldgifford/renovate-operator/api/v1alpha1"
	"github.com/donaldgifford/renovate-operator/internal/conditions"
)

func newScanScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := renovatev1alpha1.AddToScheme(s); err != nil {
		t.Fatalf("scheme add renovate: %v", err)
	}
	if err := corev1.AddToScheme(s); err != nil {
		t.Fatalf("scheme add core: %v", err)
	}
	return s
}

func newScanReconciler(t *testing.T, now time.Time, objs ...client.Object) *RenovateScanReconciler {
	t.Helper()
	scheme := newScanScheme(t)
	cli := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objs...).
		WithStatusSubresource(&renovatev1alpha1.RenovateScan{}, &renovatev1alpha1.RenovateRun{}).
		Build()
	return &RenovateScanReconciler{
		Client: cli,
		Scheme: scheme,
		Clock:  clocktesting.NewFakeClock(now),
	}
}

// mkPlatform constructs a fixture RenovatePlatform. name is parameterized
// even though most tests use "github" — the ScansForPlatform mapper test
// relies on it varying.
//
//nolint:unparam // intentional flexibility for future test additions
func mkPlatform(name string, ready bool) *renovatev1alpha1.RenovatePlatform {
	p := &renovatev1alpha1.RenovatePlatform{
		ObjectMeta: metav1.ObjectMeta{Name: name, UID: types.UID("plat-" + name)},
		Spec: renovatev1alpha1.RenovatePlatformSpec{
			PlatformType:  renovatev1alpha1.PlatformTypeGitHub,
			RenovateImage: "ghcr.io/renovatebot/renovate:latest",
			Auth: renovatev1alpha1.PlatformAuth{
				GitHubApp: &renovatev1alpha1.GitHubAppAuth{
					AppID: 1, InstallationID: 1,
					PrivateKeyRef: renovatev1alpha1.SecretKeyReference{Name: "creds"},
				},
			},
		},
	}
	if ready {
		conditions.MarkTrue(&p.Status.Conditions,
			conditions.TypeReady, conditions.ReasonCredentialsResolved, "ok", 0)
	}
	return p
}

func mkScan(name, ns, platformName, schedule string) *renovatev1alpha1.RenovateScan {
	return &renovatev1alpha1.RenovateScan{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: ns,
			UID: types.UID("scan-" + name),
		},
		Spec: renovatev1alpha1.RenovateScanSpec{
			PlatformRef: renovatev1alpha1.LocalObjectReference{Name: platformName},
			Schedule:    schedule,
			TimeZone:    "UTC",
			Workers: renovatev1alpha1.WorkersSpec{
				MinWorkers: 1, MaxWorkers: 5, ReposPerWorker: 50,
			},
		},
	}
}

func TestScanReconcile_PlatformNotFoundRequeues(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 4, 26, 12, 0, 0, 0, time.UTC)
	scan := mkScan("orphan", "team-ns", "missing-platform", "0 4 * * *")
	r := newScanReconciler(t, now, scan)

	res, err := r.Reconcile(context.Background(),
		reconcile.Request{NamespacedName: types.NamespacedName{Namespace: scan.Namespace, Name: scan.Name}})
	if err != nil {
		t.Fatalf("Reconcile err = %v", err)
	}
	if res.RequeueAfter != requeueAfterPlatformPending {
		t.Errorf("RequeueAfter = %v, want %v", res.RequeueAfter, requeueAfterPlatformPending)
	}

	got := &renovatev1alpha1.RenovateScan{}
	_ = r.Get(context.Background(),
		types.NamespacedName{Namespace: scan.Namespace, Name: scan.Name}, got)
	cond := findCondition(got.Status.Conditions, conditions.TypeReady)
	if cond == nil || cond.Status != metav1.ConditionFalse {
		t.Errorf("Ready cond = %v, want False", cond)
	}
	if cond != nil && cond.Reason != conditions.ReasonPlatformNotReady {
		t.Errorf("Ready reason = %q, want %q", cond.Reason, conditions.ReasonPlatformNotReady)
	}
}

func TestScanReconcile_PlatformNotReadyRequeues(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 4, 26, 12, 0, 0, 0, time.UTC)
	scan := mkScan("waiting", "team-ns", "github", "0 4 * * *")
	plat := mkPlatform("github", false)
	r := newScanReconciler(t, now, scan, plat)

	res, err := r.Reconcile(context.Background(),
		reconcile.Request{NamespacedName: types.NamespacedName{Namespace: scan.Namespace, Name: scan.Name}})
	if err != nil {
		t.Fatalf("Reconcile err = %v", err)
	}
	if res.RequeueAfter != requeueAfterPlatformPending {
		t.Errorf("RequeueAfter = %v, want %v", res.RequeueAfter, requeueAfterPlatformPending)
	}
}

func TestScanReconcile_SuspendShortCircuits(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 4, 26, 12, 0, 0, 0, time.UTC)
	scan := mkScan("paused", "team-ns", "github", "0 4 * * *")
	scan.Spec.Suspend = true
	r := newScanReconciler(t, now, scan)

	res, err := r.Reconcile(context.Background(),
		reconcile.Request{NamespacedName: types.NamespacedName{Namespace: scan.Namespace, Name: scan.Name}})
	if err != nil {
		t.Fatalf("Reconcile err = %v", err)
	}
	if res.RequeueAfter != 0 {
		t.Errorf("suspended RequeueAfter = %v, want 0", res.RequeueAfter)
	}

	got := &renovatev1alpha1.RenovateScan{}
	_ = r.Get(context.Background(),
		types.NamespacedName{Namespace: scan.Namespace, Name: scan.Name}, got)
	cond := findCondition(got.Status.Conditions, conditions.TypeReady)
	if cond == nil || cond.Reason != conditions.ReasonSuspended {
		t.Errorf("Ready reason = %v, want Suspended", cond)
	}
}

func TestScanReconcile_InvalidScheduleMarksFailed(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 4, 26, 12, 0, 0, 0, time.UTC)
	scan := mkScan("bad-cron", "team-ns", "github", "not a cron")
	plat := mkPlatform("github", true)
	r := newScanReconciler(t, now, scan, plat)

	_, err := r.Reconcile(context.Background(),
		reconcile.Request{NamespacedName: types.NamespacedName{Namespace: scan.Namespace, Name: scan.Name}})
	if err != nil {
		t.Fatalf("Reconcile err = %v", err)
	}
	got := &renovatev1alpha1.RenovateScan{}
	_ = r.Get(context.Background(),
		types.NamespacedName{Namespace: scan.Namespace, Name: scan.Name}, got)
	cond := findCondition(got.Status.Conditions, conditions.TypeReady)
	if cond == nil || cond.Reason != conditions.ReasonInvalidSchedule {
		t.Errorf("Ready reason = %v, want InvalidSchedule", cond)
	}
}

func TestScanReconcile_FiresMissedAndCreatesRun(t *testing.T) {
	t.Parallel()
	// "now" is 04:01 UTC; cron is 04:00 UTC daily — fires once.
	now := time.Date(2026, 4, 26, 4, 1, 0, 0, time.UTC)
	scan := mkScan("daily", "team-ns", "github", "0 4 * * *")
	// LastRunTime well in the past so we have a missed fire to act on.
	scan.Status.LastRunTime = &metav1.Time{Time: time.Date(2026, 4, 23, 4, 0, 0, 0, time.UTC)}

	plat := mkPlatform("github", true)
	r := newScanReconciler(t, now, scan, plat)

	res, err := r.Reconcile(context.Background(),
		reconcile.Request{NamespacedName: types.NamespacedName{Namespace: scan.Namespace, Name: scan.Name}})
	if err != nil {
		t.Fatalf("Reconcile err = %v", err)
	}
	if res.RequeueAfter == 0 {
		t.Error("expected non-zero RequeueAfter for next-fire")
	}

	// Confirm a Run was created in the scan's namespace.
	var runs renovatev1alpha1.RenovateRunList
	if err := r.List(context.Background(), &runs, client.InNamespace(scan.Namespace)); err != nil {
		t.Fatalf("list runs: %v", err)
	}
	if len(runs.Items) != 1 {
		t.Fatalf("got %d runs, want 1", len(runs.Items))
	}
	created := runs.Items[0]
	if created.Spec.PlatformSnapshot.PlatformType != renovatev1alpha1.PlatformTypeGitHub {
		t.Errorf("snapshot platformType = %q", created.Spec.PlatformSnapshot.PlatformType)
	}
	if len(created.OwnerReferences) != 1 || created.OwnerReferences[0].UID != scan.UID {
		t.Errorf("created run owner ref = %v, want scan uid %s", created.OwnerReferences, scan.UID)
	}
}

func TestScanReconcile_ConcurrencyForbidSkipsCreate(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 4, 26, 4, 1, 0, 0, time.UTC)
	scan := mkScan("forbid", "team-ns", "github", "0 4 * * *")
	scan.Spec.ConcurrencyPolicy = renovatev1alpha1.ForbidConcurrent
	scan.Status.LastRunTime = &metav1.Time{Time: time.Date(2026, 4, 23, 4, 0, 0, 0, time.UTC)}
	scan.Status.ActiveRuns = []corev1.ObjectReference{
		{Kind: "RenovateRun", Name: "in-flight", Namespace: scan.Namespace},
	}

	plat := mkPlatform("github", true)
	// Pre-existing in-flight run so refreshActiveRuns keeps it active.
	yes := true
	inflight := &renovatev1alpha1.RenovateRun{
		ObjectMeta: metav1.ObjectMeta{
			Name: "in-flight", Namespace: scan.Namespace,
			OwnerReferences: []metav1.OwnerReference{
				{APIVersion: renovatev1alpha1.GroupVersion.String(), Kind: "RenovateScan",
					Name: scan.Name, UID: scan.UID, Controller: &yes, BlockOwnerDeletion: &yes},
			},
		},
		Spec: renovatev1alpha1.RenovateRunSpec{
			ScanRef:          renovatev1alpha1.LocalObjectReference{Name: scan.Name},
			PlatformSnapshot: plat.Spec,
			ScanSnapshot:     scan.Spec,
		},
	}
	inflight.Status.Phase = renovatev1alpha1.RunPhaseRunning

	r := newScanReconciler(t, now, scan, plat, inflight)

	_, err := r.Reconcile(context.Background(),
		reconcile.Request{NamespacedName: types.NamespacedName{Namespace: scan.Namespace, Name: scan.Name}})
	if err != nil {
		t.Fatalf("Reconcile err = %v", err)
	}

	var runs renovatev1alpha1.RenovateRunList
	if err := r.List(context.Background(), &runs, client.InNamespace(scan.Namespace)); err != nil {
		t.Fatalf("list runs: %v", err)
	}
	if len(runs.Items) != 1 {
		t.Errorf("got %d runs, want 1 (only the in-flight one; concurrency=Forbid blocks new)", len(runs.Items))
	}
}

func TestScanReconcile_NotFoundIsIgnored(t *testing.T) {
	t.Parallel()
	r := newScanReconciler(t, time.Now())
	res, err := r.Reconcile(context.Background(),
		reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "x", Name: "ghost"}})
	if err != nil {
		t.Fatalf("Reconcile err = %v, want nil", err)
	}
	if res.RequeueAfter != 0 {
		t.Errorf("not-found RequeueAfter = %v, want 0", res.RequeueAfter)
	}
}

func TestRefreshActiveRuns_FiltersTerminalRuns(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 4, 26, 12, 0, 0, 0, time.UTC)
	scan := mkScan("filter", "team-ns", "github", "0 4 * * *")

	yes := true
	mk := func(name string, phase renovatev1alpha1.RunPhase, completion *time.Time) *renovatev1alpha1.RenovateRun {
		run := &renovatev1alpha1.RenovateRun{
			ObjectMeta: metav1.ObjectMeta{
				Name: name, Namespace: scan.Namespace,
				UID: types.UID("run-" + name),
				OwnerReferences: []metav1.OwnerReference{
					{APIVersion: renovatev1alpha1.GroupVersion.String(), Kind: "RenovateScan",
						Name: scan.Name, UID: scan.UID, Controller: &yes, BlockOwnerDeletion: &yes},
				},
			},
			Spec: renovatev1alpha1.RenovateRunSpec{
				ScanRef: renovatev1alpha1.LocalObjectReference{Name: scan.Name},
				PlatformSnapshot: renovatev1alpha1.RenovatePlatformSpec{
					PlatformType:  renovatev1alpha1.PlatformTypeGitHub,
					RenovateImage: "ghcr.io/renovatebot/renovate:latest",
					Auth: renovatev1alpha1.PlatformAuth{
						GitHubApp: &renovatev1alpha1.GitHubAppAuth{
							AppID: 1, InstallationID: 1,
							PrivateKeyRef: renovatev1alpha1.SecretKeyReference{Name: "creds"},
						},
					},
				},
			},
		}
		run.Status.Phase = phase
		if completion != nil {
			run.Status.CompletionTime = &metav1.Time{Time: *completion}
		}
		return run
	}
	t1 := now.Add(-2 * time.Hour)
	t2 := now.Add(-1 * time.Hour)
	succ := mk("succ", renovatev1alpha1.RunPhaseSucceeded, &t1)
	fail := mk("fail", renovatev1alpha1.RunPhaseFailed, &t2)
	active := mk("active", renovatev1alpha1.RunPhaseRunning, nil)

	r := newScanReconciler(t, now, succ, fail, active)

	if err := r.refreshActiveRuns(context.Background(), scan); err != nil {
		t.Fatalf("refreshActiveRuns: %v", err)
	}
	if len(scan.Status.ActiveRuns) != 1 || scan.Status.ActiveRuns[0].Name != "active" {
		t.Errorf("ActiveRuns = %v, want [active]", scan.Status.ActiveRuns)
	}
	if scan.Status.LastRunTime == nil || !scan.Status.LastRunTime.Equal(&metav1.Time{Time: t2}) {
		t.Errorf("LastRunTime = %v, want %v", scan.Status.LastRunTime, t2)
	}
	if scan.Status.LastSuccessfulRunTime == nil || !scan.Status.LastSuccessfulRunTime.Equal(&metav1.Time{Time: t1}) {
		t.Errorf("LastSuccessfulRunTime = %v, want %v", scan.Status.LastSuccessfulRunTime, t1)
	}
}

func TestGCOldRuns_TrimsToHistoryLimits(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 4, 26, 12, 0, 0, 0, time.UTC)
	scan := mkScan("gc", "team-ns", "github", "0 4 * * *")
	successLimit := int32(2)
	failLimit := int32(1)
	scan.Spec.SuccessfulRunsHistoryLimit = &successLimit
	scan.Spec.FailedRunsHistoryLimit = &failLimit

	yes := true
	mk := func(name string, phase renovatev1alpha1.RunPhase, age time.Duration) *renovatev1alpha1.RenovateRun {
		t := now.Add(-age)
		return &renovatev1alpha1.RenovateRun{
			ObjectMeta: metav1.ObjectMeta{
				Name: name, Namespace: scan.Namespace,
				UID:               types.UID("run-" + name),
				CreationTimestamp: metav1.Time{Time: t},
				OwnerReferences: []metav1.OwnerReference{
					{APIVersion: renovatev1alpha1.GroupVersion.String(), Kind: "RenovateScan",
						Name: scan.Name, UID: scan.UID, Controller: &yes, BlockOwnerDeletion: &yes},
				},
			},
			Status: renovatev1alpha1.RenovateRunStatus{
				Phase:          phase,
				CompletionTime: &metav1.Time{Time: t},
			},
		}
	}

	objs := []client.Object{
		mk("succ-old", renovatev1alpha1.RunPhaseSucceeded, 5*time.Hour),
		mk("succ-mid", renovatev1alpha1.RunPhaseSucceeded, 3*time.Hour),
		mk("succ-new", renovatev1alpha1.RunPhaseSucceeded, 1*time.Hour),
		mk("fail-old", renovatev1alpha1.RunPhaseFailed, 4*time.Hour),
		mk("fail-new", renovatev1alpha1.RunPhaseFailed, 2*time.Hour),
	}
	r := newScanReconciler(t, now, objs...)

	if err := r.gcOldRuns(context.Background(), scan); err != nil {
		t.Fatalf("gcOldRuns: %v", err)
	}

	var runs renovatev1alpha1.RenovateRunList
	if err := r.List(context.Background(), &runs, client.InNamespace(scan.Namespace)); err != nil {
		t.Fatalf("list runs: %v", err)
	}
	names := make(map[string]bool, len(runs.Items))
	for _, run := range runs.Items {
		names[run.Name] = true
	}
	// Keep 2 newest succeeded, 1 newest failed.
	want := []string{"succ-mid", "succ-new", "fail-new"}
	for _, w := range want {
		if !names[w] {
			t.Errorf("expected %q to remain", w)
		}
	}
	for _, deleted := range []string{"succ-old", "fail-old"} {
		if names[deleted] {
			t.Errorf("expected %q to be deleted", deleted)
		}
	}
}

func TestScansForPlatform_MapsCorrectScans(t *testing.T) {
	t.Parallel()
	scan1 := mkScan("a", "ns-1", "github", "0 4 * * *")
	scan2 := mkScan("b", "ns-2", "github", "0 4 * * *")
	scan3 := mkScan("c", "ns-3", "forgejo", "0 4 * * *")
	plat := mkPlatform("github", true)

	r := newScanReconciler(t, time.Now(), scan1, scan2, scan3)
	reqs := r.scansForPlatform(context.Background(), plat)
	if len(reqs) != 2 {
		t.Fatalf("got %d requests, want 2", len(reqs))
	}

	got := map[string]bool{
		reqs[0].String(): true,
		reqs[1].String(): true,
	}
	for _, want := range []string{"ns-1/a", "ns-2/b"} {
		if !got[want] {
			t.Errorf("missing request for %q", want)
		}
	}
}

// scanReconcilerWithInterceptor builds a RenovateScanReconciler whose
// fake client routes Status().Update through funcs. Used to exercise
// the outer Reconcile wrapper's conflict + non-conflict update-error
// paths without standing up an envtest.
func scanReconcilerWithInterceptor(t *testing.T, funcs interceptor.Funcs, objs ...client.Object) *RenovateScanReconciler {
	t.Helper()
	scheme := newScanScheme(t)
	cli := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objs...).
		WithStatusSubresource(&renovatev1alpha1.RenovateScan{}, &renovatev1alpha1.RenovateRun{}).
		WithInterceptorFuncs(funcs).
		Build()
	return &RenovateScanReconciler{
		Client: cli,
		Scheme: scheme,
		Clock:  clocktesting.NewFakeClock(time.Date(2026, 4, 26, 12, 0, 0, 0, time.UTC)),
	}
}

func TestScanReconcile_StatusConflictRequeues(t *testing.T) {
	t.Parallel()
	scan := mkScan("conflict", "team-ns", "missing", "0 4 * * *")
	conflict := apierrors.NewConflict(
		schema.GroupResource{Group: "renovate.fartlab.dev", Resource: "renovatescans"},
		scan.Name,
		fmt.Errorf("optimistic concurrency"),
	)

	r := scanReconcilerWithInterceptor(t, interceptor.Funcs{
		SubResourceUpdate: func(_ context.Context, _ client.Client, _ string, _ client.Object, _ ...client.SubResourceUpdateOption) error {
			return conflict
		},
	}, scan)

	res, err := r.Reconcile(context.Background(),
		reconcile.Request{NamespacedName: types.NamespacedName{Namespace: scan.Namespace, Name: scan.Name}})
	if err != nil {
		t.Fatalf("Reconcile err = %v, want nil on conflict", err)
	}
	if res.RequeueAfter == 0 {
		t.Errorf("RequeueAfter = 0, want non-zero on status conflict")
	}
}

func TestScanReconcile_StatusUpdateErrorPropagates(t *testing.T) {
	t.Parallel()
	scan := mkScan("update-err", "team-ns", "missing", "0 4 * * *")

	r := scanReconcilerWithInterceptor(t, interceptor.Funcs{
		SubResourceUpdate: func(_ context.Context, _ client.Client, _ string, _ client.Object, _ ...client.SubResourceUpdateOption) error {
			return fmt.Errorf("apiserver unavailable")
		},
	}, scan)

	_, err := r.Reconcile(context.Background(),
		reconcile.Request{NamespacedName: types.NamespacedName{Namespace: scan.Namespace, Name: scan.Name}})
	if err == nil {
		t.Fatal("Reconcile err = nil, want propagated update error")
	}
}
