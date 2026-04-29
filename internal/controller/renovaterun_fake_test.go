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
	"strings"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
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
	"github.com/donaldgifford/renovate-operator/internal/platform"
)

// stubPlatformClient is a deterministic platform.Client for fake-client tests.
type stubPlatformClient struct {
	repos       []platform.Repository
	hasConfig   bool
	discoverErr error
	configErr   error
}

func (s *stubPlatformClient) Discover(_ context.Context, _ platform.DiscoveryFilter) ([]platform.Repository, error) {
	if s.discoverErr != nil {
		return nil, s.discoverErr
	}
	return s.repos, nil
}

func (s *stubPlatformClient) HasRenovateConfig(_ context.Context, _ platform.Repository) (bool, error) {
	if s.configErr != nil {
		return false, s.configErr
	}
	return s.hasConfig, nil
}

func newRunScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := renovatev1alpha1.AddToScheme(s); err != nil {
		t.Fatalf("scheme add renovate: %v", err)
	}
	if err := corev1.AddToScheme(s); err != nil {
		t.Fatalf("scheme add core: %v", err)
	}
	if err := batchv1.AddToScheme(s); err != nil {
		t.Fatalf("scheme add batch: %v", err)
	}
	return s
}

// runFixture builds a RenovateRun with valid GitHub-App snapshot + the
// matching source Secret in the operator namespace. ns and opNS are
// fixed across the suite (passed as args for clarity at call sites).
//
//nolint:unparam // ns/opNS are intentionally fixed across this test file
func runFixture(name, ns, opNS string) (*renovatev1alpha1.RenovateRun, *corev1.Secret) {
	run := &renovatev1alpha1.RenovateRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
			UID:       types.UID("run-" + name),
		},
		Spec: renovatev1alpha1.RenovateRunSpec{
			ScanRef: renovatev1alpha1.LocalObjectReference{Name: "scan"},
			PlatformSnapshot: renovatev1alpha1.RenovatePlatformSpec{
				PlatformType:  renovatev1alpha1.PlatformTypeGitHub,
				RenovateImage: "ghcr.io/renovatebot/renovate:latest",
				Auth: renovatev1alpha1.PlatformAuth{
					GitHubApp: &renovatev1alpha1.GitHubAppAuth{
						AppID:          1,
						InstallationID: 1,
						PrivateKeyRef:  renovatev1alpha1.SecretKeyReference{Name: "creds"},
					},
				},
			},
			ScanSnapshot: renovatev1alpha1.RenovateScanSpec{
				PlatformRef: renovatev1alpha1.LocalObjectReference{Name: "scan"},
				Schedule:    "0 4 * * *",
				Workers: renovatev1alpha1.WorkersSpec{
					MinWorkers:     1,
					MaxWorkers:     5,
					ReposPerWorker: 50,
				},
				Discovery: renovatev1alpha1.DiscoverySpec{
					Autodiscover:  true,
					RequireConfig: false,
				},
			},
		},
	}

	src := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "creds",
			Namespace: opNS,
		},
		Data: map[string][]byte{
			"private-key.pem": []byte("-----BEGIN PRIVATE KEY-----\nFAKE\n-----END PRIVATE KEY-----\n"),
		},
	}
	return run, src
}

func newRunReconciler(t *testing.T, plat platform.Client, objs ...client.Object) *RenovateRunReconciler {
	t.Helper()
	scheme := newRunScheme(t)
	cli := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objs...).
		WithStatusSubresource(&renovatev1alpha1.RenovateRun{}).
		Build()

	return &RenovateRunReconciler{
		Client:            cli,
		Scheme:            scheme,
		Clock:             clocktesting.NewFakeClock(time.Date(2026, 4, 26, 12, 0, 0, 0, time.UTC)),
		OperatorNamespace: "renovate-system",
		PlatformClientFactory: func(_ context.Context, _ renovatev1alpha1.RenovatePlatformSpec, _ *corev1.Secret) (platform.Client, error) {
			return plat, nil
		},
	}
}

func TestRunReconcile_DiscoverAndDispatch_HappyPath(t *testing.T) {
	t.Parallel()
	run, src := runFixture("happy", "team-ns", "renovate-system")
	plat := &stubPlatformClient{
		repos: []platform.Repository{
			{Slug: "team-ns/repo-a"},
			{Slug: "team-ns/repo-b"},
		},
	}
	r := newRunReconciler(t, plat, run, src)

	res, err := r.Reconcile(context.Background(),
		reconcile.Request{NamespacedName: types.NamespacedName{Namespace: run.Namespace, Name: run.Name}})
	if err != nil {
		t.Fatalf("Reconcile err = %v", err)
	}
	if res.RequeueAfter != 0 {
		t.Errorf("happy path RequeueAfter = %v, want 0", res.RequeueAfter)
	}

	got := &renovatev1alpha1.RenovateRun{}
	if err := r.Get(context.Background(),
		types.NamespacedName{Namespace: run.Namespace, Name: run.Name}, got); err != nil {
		t.Fatalf("Get post-reconcile: %v", err)
	}
	if got.Status.Phase != renovatev1alpha1.RunPhaseRunning {
		t.Errorf("phase = %q, want Running", got.Status.Phase)
	}
	if got.Status.ActualWorkers != 1 {
		t.Errorf("actualWorkers = %d, want 1 (2 repos / 50 perWorker => 1 capped at min)", got.Status.ActualWorkers)
	}
	if got.Status.DiscoveredRepos != 2 {
		t.Errorf("discoveredRepos = %d, want 2", got.Status.DiscoveredRepos)
	}
	if got.Status.ShardConfigMapRef == nil {
		t.Error("ShardConfigMapRef = nil, want set")
	}
	if got.Status.WorkerJobRef == nil {
		t.Error("WorkerJobRef = nil, want set")
	}

	// Verify the actual ConfigMap + Job got created.
	cm := &corev1.ConfigMap{}
	if err := r.Get(context.Background(),
		types.NamespacedName{Namespace: run.Namespace, Name: run.Name + "-shards"}, cm); err != nil {
		t.Errorf("shard ConfigMap missing: %v", err)
	}
	job := &batchv1.Job{}
	if err := r.Get(context.Background(),
		types.NamespacedName{Namespace: run.Namespace, Name: got.Status.WorkerJobRef.Name}, job); err != nil {
		t.Errorf("worker Job missing: %v", err)
	}
}

func TestRunReconcile_NoReposMatchedMarksFailed(t *testing.T) {
	t.Parallel()
	run, src := runFixture("empty", "team-ns", "renovate-system")
	plat := &stubPlatformClient{repos: nil}
	r := newRunReconciler(t, plat, run, src)

	_, err := r.Reconcile(context.Background(),
		reconcile.Request{NamespacedName: types.NamespacedName{Namespace: run.Namespace, Name: run.Name}})
	if err != nil {
		t.Fatalf("Reconcile err = %v", err)
	}

	got := &renovatev1alpha1.RenovateRun{}
	_ = r.Get(context.Background(),
		types.NamespacedName{Namespace: run.Namespace, Name: run.Name}, got)
	if got.Status.Phase != renovatev1alpha1.RunPhaseFailed {
		t.Errorf("phase = %q, want Failed", got.Status.Phase)
	}
}

func TestRunReconcile_DiscoverTransientErrorRequeues(t *testing.T) {
	t.Parallel()
	run, src := runFixture("transient", "team-ns", "renovate-system")
	plat := &stubPlatformClient{discoverErr: platform.ErrTransient}
	r := newRunReconciler(t, plat, run, src)

	res, err := r.Reconcile(context.Background(),
		reconcile.Request{NamespacedName: types.NamespacedName{Namespace: run.Namespace, Name: run.Name}})
	if err != nil {
		t.Fatalf("Reconcile err = %v", err)
	}
	if res.RequeueAfter == 0 {
		t.Errorf("transient error: RequeueAfter = 0, want non-zero")
	}

	got := &renovatev1alpha1.RenovateRun{}
	_ = r.Get(context.Background(),
		types.NamespacedName{Namespace: run.Namespace, Name: run.Name}, got)
	if got.Status.Phase == renovatev1alpha1.RunPhaseFailed {
		t.Error("transient error should not flip to Failed")
	}
}

func TestRunReconcile_RequireConfigFiltersRepos(t *testing.T) {
	t.Parallel()
	run, src := runFixture("filter", "team-ns", "renovate-system")
	run.Spec.ScanSnapshot.Discovery.RequireConfig = true
	plat := &stubPlatformClient{
		repos:     []platform.Repository{{Slug: "team-ns/repo-a"}},
		hasConfig: true,
	}
	r := newRunReconciler(t, plat, run, src)

	_, err := r.Reconcile(context.Background(),
		reconcile.Request{NamespacedName: types.NamespacedName{Namespace: run.Namespace, Name: run.Name}})
	if err != nil {
		t.Fatalf("Reconcile err = %v", err)
	}
	got := &renovatev1alpha1.RenovateRun{}
	_ = r.Get(context.Background(),
		types.NamespacedName{Namespace: run.Namespace, Name: run.Name}, got)
	if got.Status.DiscoveredRepos != 1 {
		t.Errorf("discoveredRepos = %d, want 1", got.Status.DiscoveredRepos)
	}
}

func TestRunReconcile_Parallelism_200ReposCapsAtMaxWorkers(t *testing.T) {
	t.Parallel()
	// 200 repos / 50 reposPerWorker = 4 workers, well under maxWorkers=5.
	// IMPL-0001 Phase 7 parallelism scenario: actualWorkers == 4, the shard
	// ConfigMap holds all 200, Job parallelism == 4.
	run, src := runFixture("parallelism", "team-ns", "renovate-system")
	run.Spec.ScanSnapshot.Workers = renovatev1alpha1.WorkersSpec{
		MinWorkers:     1,
		MaxWorkers:     5,
		ReposPerWorker: 50,
	}

	repos := make([]platform.Repository, 200)
	for i := range repos {
		repos[i] = platform.Repository{Slug: fmt.Sprintf("team-ns/repo-%03d", i)}
	}
	plat := &stubPlatformClient{repos: repos}
	r := newRunReconciler(t, plat, run, src)

	if _, err := r.Reconcile(context.Background(),
		reconcile.Request{NamespacedName: types.NamespacedName{Namespace: run.Namespace, Name: run.Name}}); err != nil {
		t.Fatalf("Reconcile err = %v", err)
	}

	got := &renovatev1alpha1.RenovateRun{}
	_ = r.Get(context.Background(),
		types.NamespacedName{Namespace: run.Namespace, Name: run.Name}, got)
	if got.Status.DiscoveredRepos != 200 {
		t.Errorf("DiscoveredRepos = %d, want 200", got.Status.DiscoveredRepos)
	}
	if got.Status.ActualWorkers != 4 {
		t.Errorf("ActualWorkers = %d, want 4 (200 repos / 50 perWorker, capped at maxWorkers=5)",
			got.Status.ActualWorkers)
	}

	// Shard ConfigMap should hold every repo across its 4 shards.
	cm := &corev1.ConfigMap{}
	if err := r.Get(context.Background(),
		types.NamespacedName{Namespace: run.Namespace, Name: run.Name + "-shards"}, cm); err != nil {
		t.Fatalf("shard CM: %v", err)
	}
	if len(cm.Data) != 4 {
		t.Errorf("shard CM has %d entries, want 4", len(cm.Data))
	}

	// Job parallelism + completions both equal actualWorkers (Indexed Job).
	job := &batchv1.Job{}
	if err := r.Get(context.Background(),
		types.NamespacedName{Namespace: run.Namespace, Name: got.Status.WorkerJobRef.Name}, job); err != nil {
		t.Fatalf("worker Job: %v", err)
	}
	if job.Spec.Parallelism == nil || *job.Spec.Parallelism != 4 {
		t.Errorf("Job.Parallelism = %v, want 4", job.Spec.Parallelism)
	}
	if job.Spec.Completions == nil || *job.Spec.Completions != 4 {
		t.Errorf("Job.Completions = %v, want 4", job.Spec.Completions)
	}
}

func TestRunReconcile_Parallelism_BelowMinClampsUp(t *testing.T) {
	t.Parallel()
	// 10 repos / 50 reposPerWorker = 1 worker by ceil; minWorkers=2 lifts to 2.
	run, src := runFixture("min-clamp", "team-ns", "renovate-system")
	run.Spec.ScanSnapshot.Workers = renovatev1alpha1.WorkersSpec{
		MinWorkers:     2,
		MaxWorkers:     5,
		ReposPerWorker: 50,
	}
	repos := make([]platform.Repository, 10)
	for i := range repos {
		repos[i] = platform.Repository{Slug: fmt.Sprintf("team-ns/repo-%02d", i)}
	}
	plat := &stubPlatformClient{repos: repos}
	r := newRunReconciler(t, plat, run, src)

	if _, err := r.Reconcile(context.Background(),
		reconcile.Request{NamespacedName: types.NamespacedName{Namespace: run.Namespace, Name: run.Name}}); err != nil {
		t.Fatalf("Reconcile err = %v", err)
	}
	got := &renovatev1alpha1.RenovateRun{}
	_ = r.Get(context.Background(),
		types.NamespacedName{Namespace: run.Namespace, Name: run.Name}, got)
	if got.Status.ActualWorkers != 2 {
		t.Errorf("ActualWorkers = %d, want 2 (clamped to minWorkers)", got.Status.ActualWorkers)
	}
}

func TestRunReconcile_RequireConfigSkipsReposWithoutRenovateJSON(t *testing.T) {
	t.Parallel()
	run, src := runFixture("skip", "team-ns", "renovate-system")
	run.Spec.ScanSnapshot.Discovery.RequireConfig = true
	plat := &stubPlatformClient{
		repos:     []platform.Repository{{Slug: "team-ns/repo-a"}},
		hasConfig: false,
	}
	r := newRunReconciler(t, plat, run, src)

	_, err := r.Reconcile(context.Background(),
		reconcile.Request{NamespacedName: types.NamespacedName{Namespace: run.Namespace, Name: run.Name}})
	if err != nil {
		t.Fatalf("Reconcile err = %v", err)
	}
	got := &renovatev1alpha1.RenovateRun{}
	_ = r.Get(context.Background(),
		types.NamespacedName{Namespace: run.Namespace, Name: run.Name}, got)
	if got.Status.Phase != renovatev1alpha1.RunPhaseFailed {
		t.Errorf("phase = %q, want Failed (no repos with config)", got.Status.Phase)
	}
}

// TestRunReconcile_RequireConfigHasConfigErrorPropagates covers the
// HasRenovateConfig error-path in the discoverRepos batch loop. A
// transient error mid-batch should bubble out so discoverAndDispatch
// classifies it correctly (transient -> requeue, not flip-to-Failed).
func TestRunReconcile_RequireConfigHasConfigErrorPropagates(t *testing.T) {
	t.Parallel()
	run, src := runFixture("config-err", "team-ns", "renovate-system")
	run.Spec.ScanSnapshot.Discovery.RequireConfig = true
	plat := &stubPlatformClient{
		repos:     []platform.Repository{{Slug: "team-ns/a"}, {Slug: "team-ns/b"}},
		configErr: platform.ErrTransient,
	}
	r := newRunReconciler(t, plat, run, src)

	res, err := r.Reconcile(context.Background(),
		reconcile.Request{NamespacedName: types.NamespacedName{Namespace: run.Namespace, Name: run.Name}})
	if err != nil {
		t.Fatalf("Reconcile err = %v", err)
	}
	if res.RequeueAfter == 0 {
		t.Errorf("transient HasRenovateConfig error: RequeueAfter = 0, want non-zero")
	}

	got := &renovatev1alpha1.RenovateRun{}
	_ = r.Get(context.Background(),
		types.NamespacedName{Namespace: run.Namespace, Name: run.Name}, got)
	if got.Status.Phase == renovatev1alpha1.RunPhaseFailed {
		t.Error("transient HasRenovateConfig error should not flip to Failed")
	}
}

func TestRunReconcile_MissingSourceSecretRequeues(t *testing.T) {
	t.Parallel()
	run, _ := runFixture("missing-secret", "team-ns", "renovate-system")
	plat := &stubPlatformClient{repos: []platform.Repository{{Slug: "x/y"}}}
	// Note: no src Secret in the fake client.
	r := newRunReconciler(t, plat, run)

	res, err := r.Reconcile(context.Background(),
		reconcile.Request{NamespacedName: types.NamespacedName{Namespace: run.Namespace, Name: run.Name}})
	if err != nil {
		t.Fatalf("Reconcile err = %v", err)
	}
	if res.RequeueAfter == 0 {
		t.Errorf("missing secret should requeue, got RequeueAfter = 0")
	}
}

func TestObserveJob_TransitionsToSucceeded(t *testing.T) {
	t.Parallel()
	run, _ := runFixture("running", "team-ns", "renovate-system")
	run.Status.Phase = renovatev1alpha1.RunPhaseRunning
	run.Status.WorkerJobRef = &corev1.ObjectReference{
		APIVersion: "batch/v1", Kind: "Job",
		Namespace: run.Namespace, Name: "running-workers",
	}
	completions := int32(2)
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: "running-workers", Namespace: run.Namespace},
		Spec:       batchv1.JobSpec{Completions: &completions},
		Status:     batchv1.JobStatus{Succeeded: 2},
	}
	r := newRunReconciler(t, nil, run, job)

	_, err := r.Reconcile(context.Background(),
		reconcile.Request{NamespacedName: types.NamespacedName{Namespace: run.Namespace, Name: run.Name}})
	if err != nil {
		t.Fatalf("Reconcile err = %v", err)
	}

	got := &renovatev1alpha1.RenovateRun{}
	_ = r.Get(context.Background(),
		types.NamespacedName{Namespace: run.Namespace, Name: run.Name}, got)
	if got.Status.Phase != renovatev1alpha1.RunPhaseSucceeded {
		t.Errorf("phase = %q, want Succeeded", got.Status.Phase)
	}
	if got.Status.SucceededShards != 2 {
		t.Errorf("succeededShards = %d, want 2", got.Status.SucceededShards)
	}
}

func TestObserveJob_TransitionsToFailedOnJobFailed(t *testing.T) {
	t.Parallel()
	run, _ := runFixture("failing", "team-ns", "renovate-system")
	run.Status.Phase = renovatev1alpha1.RunPhaseRunning
	run.Status.WorkerJobRef = &corev1.ObjectReference{
		APIVersion: "batch/v1", Kind: "Job",
		Namespace: run.Namespace, Name: "failing-workers",
	}
	completions := int32(2)
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: "failing-workers", Namespace: run.Namespace},
		Spec:       batchv1.JobSpec{Completions: &completions},
		Status: batchv1.JobStatus{
			Failed: 2,
			Conditions: []batchv1.JobCondition{
				{Type: batchv1.JobFailed, Status: corev1.ConditionTrue, Message: "exceeded backoffLimit"},
			},
		},
	}
	r := newRunReconciler(t, nil, run, job)

	_, err := r.Reconcile(context.Background(),
		reconcile.Request{NamespacedName: types.NamespacedName{Namespace: run.Namespace, Name: run.Name}})
	if err != nil {
		t.Fatalf("Reconcile err = %v", err)
	}

	got := &renovatev1alpha1.RenovateRun{}
	_ = r.Get(context.Background(),
		types.NamespacedName{Namespace: run.Namespace, Name: run.Name}, got)
	if got.Status.Phase != renovatev1alpha1.RunPhaseFailed {
		t.Errorf("phase = %q, want Failed", got.Status.Phase)
	}
}

func TestObserveJob_VanishedJobMarksFailed(t *testing.T) {
	t.Parallel()
	run, _ := runFixture("vanished", "team-ns", "renovate-system")
	run.Status.Phase = renovatev1alpha1.RunPhaseRunning
	run.Status.WorkerJobRef = &corev1.ObjectReference{
		APIVersion: "batch/v1", Kind: "Job",
		Namespace: run.Namespace, Name: "ghost-job",
	}
	r := newRunReconciler(t, nil, run) // no Job

	_, err := r.Reconcile(context.Background(),
		reconcile.Request{NamespacedName: types.NamespacedName{Namespace: run.Namespace, Name: run.Name}})
	if err != nil {
		t.Fatalf("Reconcile err = %v", err)
	}
	got := &renovatev1alpha1.RenovateRun{}
	_ = r.Get(context.Background(),
		types.NamespacedName{Namespace: run.Namespace, Name: run.Name}, got)
	if got.Status.Phase != renovatev1alpha1.RunPhaseFailed {
		t.Errorf("phase = %q, want Failed", got.Status.Phase)
	}
}

func TestReconcile_TerminalPhasesAreNoOp(t *testing.T) {
	t.Parallel()
	run, _ := runFixture("done", "team-ns", "renovate-system")
	run.Status.Phase = renovatev1alpha1.RunPhaseSucceeded
	r := newRunReconciler(t, nil, run)

	res, err := r.Reconcile(context.Background(),
		reconcile.Request{NamespacedName: types.NamespacedName{Namespace: run.Namespace, Name: run.Name}})
	if err != nil {
		t.Fatalf("Reconcile err = %v", err)
	}
	if res.RequeueAfter != 0 {
		t.Errorf("terminal phase RequeueAfter = %v, want 0", res.RequeueAfter)
	}
}

func TestReconcile_NotFoundIsIgnored(t *testing.T) {
	t.Parallel()
	r := newRunReconciler(t, nil)

	res, err := r.Reconcile(context.Background(),
		reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "ns", Name: "missing"}})
	if err != nil {
		t.Fatalf("Reconcile err = %v, want nil (NotFound)", err)
	}
	if res.RequeueAfter != 0 {
		t.Errorf("not-found RequeueAfter = %v, want 0", res.RequeueAfter)
	}
}

func TestReconcile_UnknownPhaseErrors(t *testing.T) {
	t.Parallel()
	run, _ := runFixture("weird", "team-ns", "renovate-system")
	run.Status.Phase = "Bogus"
	r := newRunReconciler(t, nil, run)

	_, err := r.Reconcile(context.Background(),
		reconcile.Request{NamespacedName: types.NamespacedName{Namespace: run.Namespace, Name: run.Name}})
	if err == nil {
		t.Fatal("Reconcile err = nil, want unknown-phase error")
	}
}

// runReconcilerWithInterceptor wires a RenovateRunReconciler whose underlying
// fake client routes every Status().Update through funcs. The terminal phase
// fixture is used so the inner reconcile() is a no-op and the assertion is
// purely about the outer Reconcile wrapper's error/conflict handling.
func runReconcilerWithInterceptor(t *testing.T, funcs interceptor.Funcs, objs ...client.Object) *RenovateRunReconciler {
	t.Helper()
	scheme := newRunScheme(t)
	cli := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objs...).
		WithStatusSubresource(&renovatev1alpha1.RenovateRun{}).
		WithInterceptorFuncs(funcs).
		Build()

	return &RenovateRunReconciler{
		Client:            cli,
		Scheme:            scheme,
		Clock:             clocktesting.NewFakeClock(time.Date(2026, 4, 26, 12, 0, 0, 0, time.UTC)),
		OperatorNamespace: "renovate-system",
	}
}

func TestReconcile_StatusConflictRequeues(t *testing.T) {
	t.Parallel()
	run, _ := runFixture("conflict", "team-ns", "renovate-system")
	run.Status.Phase = renovatev1alpha1.RunPhaseSucceeded

	conflict := apierrors.NewConflict(
		schema.GroupResource{Group: "renovate.fartlab.dev", Resource: "renovateruns"},
		run.Name,
		fmt.Errorf("optimistic concurrency"),
	)

	r := runReconcilerWithInterceptor(t, interceptor.Funcs{
		SubResourceUpdate: func(_ context.Context, _ client.Client, _ string, _ client.Object, _ ...client.SubResourceUpdateOption) error {
			return conflict
		},
	}, run)

	res, err := r.Reconcile(context.Background(),
		reconcile.Request{NamespacedName: types.NamespacedName{Namespace: run.Namespace, Name: run.Name}})
	if err != nil {
		t.Fatalf("Reconcile err = %v, want nil on conflict", err)
	}
	if res.RequeueAfter == 0 {
		t.Errorf("RequeueAfter = 0, want non-zero on status conflict")
	}
}

func TestReconcile_StatusUpdateErrorPropagates(t *testing.T) {
	t.Parallel()
	run, _ := runFixture("update-err", "team-ns", "renovate-system")
	run.Status.Phase = renovatev1alpha1.RunPhaseSucceeded

	r := runReconcilerWithInterceptor(t, interceptor.Funcs{
		SubResourceUpdate: func(_ context.Context, _ client.Client, _ string, _ client.Object, _ ...client.SubResourceUpdateOption) error {
			return fmt.Errorf("apiserver unavailable")
		},
	}, run)

	_, err := r.Reconcile(context.Background(),
		reconcile.Request{NamespacedName: types.NamespacedName{Namespace: run.Namespace, Name: run.Name}})
	if err == nil {
		t.Fatal("Reconcile err = nil, want propagated update error")
	}
}

func TestMirrorCredential_UpdatesExisting(t *testing.T) {
	t.Parallel()
	run, src := runFixture("mirror-update", "team-ns", "renovate-system")

	// Pre-existing mirror with old data — mirrorCredential should update it.
	dst := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      run.Name + "-creds",
			Namespace: run.Namespace,
		},
		Data: map[string][]byte{"stale": []byte("data")},
	}

	r := newRunReconciler(t, nil, run, src, dst)

	updated, err := r.mirrorCredential(context.Background(), run)
	if err != nil {
		t.Fatalf("mirrorCredential err = %v", err)
	}
	if string(updated.Data["private-key.pem"]) == "" {
		t.Error("updated secret missing private-key.pem")
	}
	if _, hasStale := updated.Data["stale"]; hasStale {
		t.Error("update should have replaced data; stale key still present")
	}
}

// ioErrReconciler builds a RenovateRunReconciler whose fake client
// surfaces injected IO errors through interceptors. Used to assert the
// IO-helpers' error-wrapping contract.
func ioErrReconciler(t *testing.T, funcs interceptor.Funcs, objs ...client.Object) *RenovateRunReconciler {
	t.Helper()
	scheme := newRunScheme(t)
	cli := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objs...).
		WithStatusSubresource(&renovatev1alpha1.RenovateRun{}).
		WithInterceptorFuncs(funcs).
		Build()
	return &RenovateRunReconciler{
		Client:            cli,
		Scheme:            scheme,
		Clock:             clocktesting.NewFakeClock(time.Date(2026, 4, 26, 12, 0, 0, 0, time.UTC)),
		OperatorNamespace: "renovate-system",
	}
}

func TestMirrorCredential_GetSourceErrorWrapped(t *testing.T) {
	t.Parallel()
	run, src := runFixture("mirror-get-err", "team-ns", "renovate-system")

	r := ioErrReconciler(t, interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			// Only intercept the source-secret Get. Mirror Get is in another namespace.
			if key.Namespace == "renovate-system" && key.Name == "creds" {
				return fmt.Errorf("apiserver flake")
			}
			return c.Get(ctx, key, obj, opts...)
		},
	}, run, src)

	_, err := r.mirrorCredential(context.Background(), run)
	if err == nil {
		t.Fatal("mirrorCredential err = nil")
	}
	if !strings.Contains(err.Error(), "get source secret") {
		t.Errorf("err = %v, want wrapped 'get source secret'", err)
	}
}

func TestMirrorCredential_CreateErrorWrapped(t *testing.T) {
	t.Parallel()
	run, src := runFixture("mirror-create-err", "team-ns", "renovate-system")

	r := ioErrReconciler(t, interceptor.Funcs{
		Create: func(_ context.Context, _ client.WithWatch, _ client.Object, _ ...client.CreateOption) error {
			return fmt.Errorf("apiserver wedged")
		},
	}, run, src)

	_, err := r.mirrorCredential(context.Background(), run)
	if err == nil {
		t.Fatal("mirrorCredential err = nil")
	}
	if !strings.Contains(err.Error(), "create mirrored secret") {
		t.Errorf("err = %v, want wrapped 'create mirrored secret'", err)
	}
}

func TestEnsureShardConfigMap_GetErrorWrapped(t *testing.T) {
	t.Parallel()
	run, _ := runFixture("shard-get-err", "team-ns", "renovate-system")

	r := ioErrReconciler(t, interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			// Inject error on the shard ConfigMap Get.
			if _, ok := obj.(*corev1.ConfigMap); ok {
				return fmt.Errorf("apiserver flake")
			}
			return c.Get(ctx, key, obj, opts...)
		},
	}, run)

	_, _, err := r.ensureShardConfigMap(context.Background(), run,
		[]platform.Repository{{Slug: "team-ns/a"}})
	if err == nil {
		t.Fatal("ensureShardConfigMap err = nil")
	}
	if !strings.Contains(err.Error(), "get shard CM") {
		t.Errorf("err = %v, want wrapped 'get shard CM'", err)
	}
}

func TestEnsureShardConfigMap_InvalidBoundsError(t *testing.T) {
	t.Parallel()
	run, _ := runFixture("shard-bad-bounds", "team-ns", "renovate-system")
	// MaxWorkers < MinWorkers — both non-zero so the function-local
	// substitutions don't fire. sharding.Build then rejects.
	run.Spec.ScanSnapshot.Workers.MinWorkers = 5
	run.Spec.ScanSnapshot.Workers.MaxWorkers = 2

	r := newRunReconciler(t, nil, run)

	_, _, err := r.ensureShardConfigMap(context.Background(), run,
		[]platform.Repository{{Slug: "team-ns/a"}})
	if err == nil {
		t.Fatal("ensureShardConfigMap err = nil for bad bounds")
	}
	if !strings.Contains(err.Error(), "shard build") {
		t.Errorf("err = %v, want wrapped 'shard build'", err)
	}
}

func TestEnsureWorkerJob_GetErrorWrapped(t *testing.T) {
	t.Parallel()
	run, _ := runFixture("job-get-err", "team-ns", "renovate-system")

	mirrored := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: run.Name + "-creds", Namespace: run.Namespace},
		Data:       map[string][]byte{"private-key.pem": []byte("FAKE")},
	}
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: run.Name + "-shards", Namespace: run.Namespace},
		Data:       map[string]string{"shard-0.json": "{}"},
	}

	r := ioErrReconciler(t, interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if _, ok := obj.(*batchv1.Job); ok {
				return fmt.Errorf("apiserver flake")
			}
			return c.Get(ctx, key, obj, opts...)
		},
	}, run)

	_, err := r.ensureWorkerJob(context.Background(), run, mirrored, cm, 1)
	if err == nil {
		t.Fatal("ensureWorkerJob err = nil")
	}
	if !strings.Contains(err.Error(), "get worker Job") {
		t.Errorf("err = %v, want wrapped 'get worker Job'", err)
	}
}
