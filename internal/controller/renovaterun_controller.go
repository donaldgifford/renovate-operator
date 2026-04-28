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
	"errors"
	"fmt"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	renovatev1alpha1 "github.com/donaldgifford/renovate-operator/api/v1alpha1"
	"github.com/donaldgifford/renovate-operator/internal/clock"
	"github.com/donaldgifford/renovate-operator/internal/conditions"
	"github.com/donaldgifford/renovate-operator/internal/credentials"
	"github.com/donaldgifford/renovate-operator/internal/jobspec"
	"github.com/donaldgifford/renovate-operator/internal/platform"
	"github.com/donaldgifford/renovate-operator/internal/sharding"
)

// requeueAfterRunTransient is the requeue cadence for transient errors
// (rate-limits, network blips, missing source Secret).
const requeueAfterRunTransient = 30 * time.Second

// RenovateRunReconciler reconciles a RenovateRun through its state machine.
//
//	Pending → Discovering → Running → {Succeeded, Failed}
//
// Each step is idempotent so a controller crash mid-step is safe to resume.
type RenovateRunReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	// Clock is injected for tests.
	Clock clock.Clock

	// OperatorNamespace is where the source credential Secret lives.
	OperatorNamespace string

	// PlatformClientFactory builds platform clients from a Run's snapshot;
	// tests substitute a stub.
	PlatformClientFactory PlatformClientFactory
}

// +kubebuilder:rbac:groups=renovate.fartlab.dev,resources=renovateruns,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=renovate.fartlab.dev,resources=renovateruns/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=renovate.fartlab.dev,resources=renovateruns/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;update;patch;delete

func (r *RenovateRunReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	if r.Clock == nil {
		r.Clock = clock.RealClock()
	}
	if r.PlatformClientFactory == nil {
		r.PlatformClientFactory = DefaultPlatformClientFactory()
	}

	var run renovatev1alpha1.RenovateRun
	if err := r.Get(ctx, req.NamespacedName, &run); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	result, err := r.reconcile(ctx, &run)
	if updateErr := r.Status().Update(ctx, &run); updateErr != nil {
		if apierrors.IsConflict(updateErr) {
			log.V(1).Info("status conflict, requeueing", "run", req.String())
			return ctrl.Result{Requeue: true}, nil
		}
		log.Error(updateErr, "status update failed", "run", req.String())
		if err == nil {
			err = updateErr
		}
	}
	return result, err
}

func (r *RenovateRunReconciler) reconcile(ctx context.Context, run *renovatev1alpha1.RenovateRun) (ctrl.Result, error) {
	run.Status.ObservedGeneration = run.Generation
	if run.Status.Phase == "" {
		run.Status.Phase = renovatev1alpha1.RunPhasePending
	}

	switch run.Status.Phase {
	case renovatev1alpha1.RunPhasePending, renovatev1alpha1.RunPhaseDiscovering:
		return r.discoverAndDispatch(ctx, run)
	case renovatev1alpha1.RunPhaseRunning:
		return r.observeJob(ctx, run)
	case renovatev1alpha1.RunPhaseSucceeded, renovatev1alpha1.RunPhaseFailed:
		return ctrl.Result{}, nil
	default:
		return ctrl.Result{}, fmt.Errorf("unknown phase %q", run.Status.Phase)
	}
}

// discoverAndDispatch handles Pending and Discovering. It mirrors the source
// Secret, instantiates the platform client, runs Discover (and HasRenovateConfig
// when requireConfig), computes ActualWorkers, creates the shard ConfigMap +
// worker Job, and transitions to Running.
func (r *RenovateRunReconciler) discoverAndDispatch(ctx context.Context, run *renovatev1alpha1.RenovateRun) (ctrl.Result, error) {
	now := r.Clock.Now()
	if run.Status.StartTime == nil {
		run.Status.StartTime = &metav1.Time{Time: now}
	}
	conditions.MarkTrue(&run.Status.Conditions,
		conditions.TypeStarted, conditions.ReasonAdmitted,
		"run admitted by controller", run.Generation)
	run.Status.Phase = renovatev1alpha1.RunPhaseDiscovering

	mirrored, err := r.mirrorCredential(ctx, run)
	if err != nil {
		return r.markTransient(run, err, conditions.ReasonReconcileError)
	}

	srcSecret := mirrored
	plat, err := r.PlatformClientFactory(ctx, run.Spec.PlatformSnapshot, srcSecret)
	if err != nil {
		return r.markFailed(run, "platform client init: "+err.Error())
	}

	repos, err := r.discoverRepos(ctx, run, plat)
	if err != nil {
		return r.handleDiscoverErr(run, err)
	}

	if len(repos) == 0 {
		return r.markFailed(run, "no repositories matched discovery filter")
	}

	cm, actualWorkers, err := r.ensureShardConfigMap(ctx, run, repos)
	if err != nil {
		return r.markTransient(run, err, conditions.ReasonReconcileError)
	}
	run.Status.DiscoveredRepos = int32(len(repos))
	run.Status.ActualWorkers = actualWorkers
	run.Status.ShardConfigMapRef = &corev1.ObjectReference{
		APIVersion: "v1", Kind: "ConfigMap",
		Name: cm.Name, Namespace: cm.Namespace, UID: cm.UID,
	}

	job, err := r.ensureWorkerJob(ctx, run, mirrored, cm, actualWorkers)
	if err != nil {
		return r.markTransient(run, err, conditions.ReasonReconcileError)
	}
	run.Status.WorkerJobRef = &corev1.ObjectReference{
		APIVersion: "batch/v1", Kind: "Job",
		Name: job.Name, Namespace: job.Namespace, UID: job.UID,
	}

	if run.Status.DiscoveryCompletionTime == nil {
		t := metav1.Time{Time: r.Clock.Now()}
		run.Status.DiscoveryCompletionTime = &t
	}
	if run.Status.WorkersStartTime == nil {
		t := metav1.Time{Time: r.Clock.Now()}
		run.Status.WorkersStartTime = &t
	}
	conditions.MarkTrue(&run.Status.Conditions,
		conditions.TypeDiscovered, conditions.ReasonDiscoveryComplete,
		fmt.Sprintf("discovered %d repos across %d workers", len(repos), actualWorkers),
		run.Generation)
	run.Status.Phase = renovatev1alpha1.RunPhaseRunning
	return ctrl.Result{}, nil
}

// observeJob handles Running: reads the owned Job and transitions to Succeeded
// or Failed when terminal.
func (r *RenovateRunReconciler) observeJob(ctx context.Context, run *renovatev1alpha1.RenovateRun) (ctrl.Result, error) {
	if run.Status.WorkerJobRef == nil {
		return r.markFailed(run, "running phase without WorkerJobRef")
	}
	job := &batchv1.Job{}
	key := types.NamespacedName{Namespace: run.Status.WorkerJobRef.Namespace, Name: run.Status.WorkerJobRef.Name}
	if err := r.Get(ctx, key, job); err != nil {
		if apierrors.IsNotFound(err) {
			return r.markFailed(run, "worker Job vanished before completion")
		}
		return ctrl.Result{}, fmt.Errorf("get job: %w", err)
	}

	run.Status.SucceededShards = job.Status.Succeeded
	run.Status.FailedShards = job.Status.Failed

	completions := int32(1)
	if job.Spec.Completions != nil {
		completions = *job.Spec.Completions
	}

	if job.Status.Succeeded >= completions {
		t := metav1.Time{Time: r.Clock.Now()}
		run.Status.CompletionTime = &t
		run.Status.Phase = renovatev1alpha1.RunPhaseSucceeded
		conditions.MarkTrue(&run.Status.Conditions,
			conditions.TypeSucceeded, conditions.ReasonJobComplete,
			"all shards completed successfully", run.Generation)
		return ctrl.Result{}, nil
	}

	for _, c := range job.Status.Conditions {
		if c.Type == batchv1.JobFailed && c.Status == corev1.ConditionTrue {
			t := metav1.Time{Time: r.Clock.Now()}
			run.Status.CompletionTime = &t
			run.Status.Phase = renovatev1alpha1.RunPhaseFailed
			conditions.MarkTrue(&run.Status.Conditions,
				conditions.TypeFailed, conditions.ReasonJobFailed,
				"worker Job failed: "+c.Message, run.Generation)
			return ctrl.Result{}, nil
		}
	}
	return ctrl.Result{}, nil
}

func (r *RenovateRunReconciler) discoverRepos(ctx context.Context, run *renovatev1alpha1.RenovateRun, plat platform.Client) ([]platform.Repository, error) {
	owner := r.discoveryOwner(run)
	filter := platform.DiscoveryFilter{
		Owner:        owner,
		Patterns:     run.Spec.ScanSnapshot.Discovery.Filter,
		Topics:       run.Spec.ScanSnapshot.Discovery.Topics,
		SkipForks:    run.Spec.ScanSnapshot.Discovery.SkipForks,
		SkipArchived: run.Spec.ScanSnapshot.Discovery.SkipArchived,
	}
	all, err := plat.Discover(ctx, filter)
	if err != nil {
		return nil, err
	}
	if !run.Spec.ScanSnapshot.Discovery.RequireConfig {
		return all, nil
	}
	out := make([]platform.Repository, 0, len(all))
	for _, repo := range all {
		ok, err := plat.HasRenovateConfig(ctx, repo)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, repo)
		}
	}
	return out, nil
}

// discoveryOwner extracts the org/user the Run should enumerate. v0.1.0
// derives it from the Run's metadata.namespace (homelab convention) or the
// first path segment of any provided filter glob. A future field on Scan
// (spec.discovery.owner) can override.
func (r *RenovateRunReconciler) discoveryOwner(run *renovatev1alpha1.RenovateRun) string {
	for _, f := range run.Spec.ScanSnapshot.Discovery.Filter {
		if i := indexOf(f, '/'); i > 0 {
			return f[:i]
		}
	}
	return run.Namespace
}

func indexOf(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}

func (r *RenovateRunReconciler) handleDiscoverErr(run *renovatev1alpha1.RenovateRun, err error) (ctrl.Result, error) {
	if errors.Is(err, platform.ErrTransient) {
		return r.markTransient(run, err, conditions.ReasonDiscoveryFailed)
	}
	return r.markFailed(run, "discovery: "+err.Error())
}

func (r *RenovateRunReconciler) markFailed(run *renovatev1alpha1.RenovateRun, msg string) (ctrl.Result, error) {
	t := metav1.Time{Time: r.Clock.Now()}
	run.Status.CompletionTime = &t
	run.Status.Phase = renovatev1alpha1.RunPhaseFailed
	conditions.MarkTrue(&run.Status.Conditions,
		conditions.TypeFailed, conditions.ReasonDiscoveryFailed, msg, run.Generation)
	return ctrl.Result{}, nil
}

func (r *RenovateRunReconciler) markTransient(run *renovatev1alpha1.RenovateRun, err error, reason string) (ctrl.Result, error) {
	conditions.MarkFalse(&run.Status.Conditions,
		conditions.TypeStarted, reason, err.Error(), run.Generation)
	return ctrl.Result{RequeueAfter: requeueAfterRunTransient}, nil
}

// mirrorCredential ensures the per-Run mirrored Secret exists in the Run's
// namespace, copied from the source Secret in OperatorNamespace.
func (r *RenovateRunReconciler) mirrorCredential(ctx context.Context, run *renovatev1alpha1.RenovateRun) (*corev1.Secret, error) {
	srcName, err := credentials.SourceSecretName(run)
	if err != nil {
		return nil, err
	}
	src := &corev1.Secret{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: r.OperatorNamespace, Name: srcName}, src); err != nil {
		return nil, fmt.Errorf("get source secret: %w", err)
	}

	dst, err := credentials.BuildMirror(run, src)
	if err != nil {
		return nil, err
	}

	existing := &corev1.Secret{}
	err = r.Get(ctx, types.NamespacedName{Namespace: dst.Namespace, Name: dst.Name}, existing)
	switch {
	case apierrors.IsNotFound(err):
		if err := r.Create(ctx, dst); err != nil {
			return nil, fmt.Errorf("create mirrored secret: %w", err)
		}
		return dst, nil
	case err != nil:
		return nil, fmt.Errorf("get mirrored secret: %w", err)
	default:
		existing.Data = dst.Data
		existing.Labels = dst.Labels
		if err := r.Update(ctx, existing); err != nil {
			return nil, fmt.Errorf("update mirrored secret: %w", err)
		}
		return existing, nil
	}
}

func (r *RenovateRunReconciler) ensureShardConfigMap(ctx context.Context, run *renovatev1alpha1.RenovateRun, repos []platform.Repository) (*corev1.ConfigMap, int32, error) {
	bounds := sharding.WorkerBounds{
		MinWorkers:     run.Spec.ScanSnapshot.Workers.MinWorkers,
		MaxWorkers:     run.Spec.ScanSnapshot.Workers.MaxWorkers,
		ReposPerWorker: run.Spec.ScanSnapshot.Workers.ReposPerWorker,
	}
	if bounds.MinWorkers == 0 {
		bounds.MinWorkers = 1
	}
	if bounds.MaxWorkers == 0 {
		bounds.MaxWorkers = 10
	}
	if bounds.ReposPerWorker == 0 {
		bounds.ReposPerWorker = 50
	}

	shardRepos := make([]sharding.Repository, len(repos))
	for i, r := range repos {
		shardRepos[i] = sharding.Repository{Slug: r.Slug}
	}
	result, err := sharding.Build(shardRepos, bounds)
	if err != nil {
		return nil, 0, fmt.Errorf("shard build: %w", err)
	}

	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      run.Name + "-shards",
			Namespace: run.Namespace,
			Labels:    jobspec.WorkerLabels(run),
			OwnerReferences: []metav1.OwnerReference{
				ownerRefForRun(run),
			},
		},
		Data: result.Data,
	}

	existing := &corev1.ConfigMap{}
	err = r.Get(ctx, types.NamespacedName{Namespace: cm.Namespace, Name: cm.Name}, existing)
	switch {
	case apierrors.IsNotFound(err):
		if err := r.Create(ctx, cm); err != nil {
			return nil, 0, fmt.Errorf("create shard CM: %w", err)
		}
		return cm, result.ActualWorkers, nil
	case err != nil:
		return nil, 0, fmt.Errorf("get shard CM: %w", err)
	default:
		return existing, result.ActualWorkers, nil
	}
}

func (r *RenovateRunReconciler) ensureWorkerJob(ctx context.Context, run *renovatev1alpha1.RenovateRun, mirrored *corev1.Secret, cm *corev1.ConfigMap, actualWorkers int32) (*batchv1.Job, error) {
	cred := jobspec.CredentialMount{SecretName: mirrored.Name}
	switch {
	case run.Spec.PlatformSnapshot.Auth.GitHubApp != nil:
		key := run.Spec.PlatformSnapshot.Auth.GitHubApp.PrivateKeyRef.Key
		if key == "" {
			key = defaultGitHubAppPEMKey
		}
		cred.PEMKey = key
	case run.Spec.PlatformSnapshot.Auth.Token != nil:
		key := run.Spec.PlatformSnapshot.Auth.Token.SecretRef.Key
		if key == "" {
			key = defaultTokenKey
		}
		cred.TokenKey = key
	}

	job, err := jobspec.BuildWorkerJob(jobspec.BuildInput{
		Run: run, ShardConfigMap: cm, ActualWorkers: actualWorkers, Credential: cred,
	})
	if err != nil {
		return nil, err
	}

	existing := &batchv1.Job{}
	err = r.Get(ctx, types.NamespacedName{Namespace: job.Namespace, Name: job.Name}, existing)
	switch {
	case apierrors.IsNotFound(err):
		if err := r.Create(ctx, job); err != nil {
			return nil, fmt.Errorf("create worker Job: %w", err)
		}
		return job, nil
	case err != nil:
		return nil, fmt.Errorf("get worker Job: %w", err)
	default:
		return existing, nil
	}
}

func ownerRefForRun(run *renovatev1alpha1.RenovateRun) metav1.OwnerReference {
	yes := true
	return metav1.OwnerReference{
		APIVersion:         renovatev1alpha1.GroupVersion.String(),
		Kind:               "RenovateRun",
		Name:               run.Name,
		UID:                run.UID,
		Controller:         &yes,
		BlockOwnerDeletion: &yes,
	}
}

func (r *RenovateRunReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.Clock == nil {
		r.Clock = clock.RealClock()
	}
	if r.PlatformClientFactory == nil {
		r.PlatformClientFactory = DefaultPlatformClientFactory()
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&renovatev1alpha1.RenovateRun{}).
		Owns(&batchv1.Job{}).
		Owns(&corev1.ConfigMap{}).
		Owns(&corev1.Secret{}).
		Named("renovaterun").
		Complete(r)
}
