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
	"sort"
	"time"

	"github.com/robfig/cron/v3"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	renovatev1alpha1 "github.com/donaldgifford/renovate-operator/api/v1alpha1"
	"github.com/donaldgifford/renovate-operator/internal/clock"
	"github.com/donaldgifford/renovate-operator/internal/conditions"
)

// requeueAfterMax caps a Scan's RequeueAfter so the controller never goes
// dark for hours. Cron next-fire times beyond this cap get clamped down.
const requeueAfterMax = 5 * time.Minute

// requeueAfterPlatformPending is the cadence used while the parent Platform
// is not Ready.
const requeueAfterPlatformPending = 60 * time.Second

// cronParser parses standard 5-field cron expressions in the tz the Scan declares.
var cronParser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)

// RenovateScanReconciler reconciles a RenovateScan object.
//
// At each reconcile the Scan controller:
//  1. Resolves the parent Platform; surfaces Ready=False/PlatformNotReady
//     until it's ready.
//  2. Parses the cron expression in the configured time zone.
//  3. If a fire time has passed since lastRunTime and concurrency policy
//     allows, creates a child RenovateRun snapshotting both specs.
//  4. GCs old terminal Runs per spec.{successful,failed}RunsHistoryLimit.
//  5. Sets next-run-time and Scheduled=True; requeues at the next fire time.
type RenovateScanReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	// Clock is injected for tests; production wires clock.RealClock().
	Clock clock.Clock
}

// +kubebuilder:rbac:groups=renovate.fartlab.dev,resources=renovatescans,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=renovate.fartlab.dev,resources=renovatescans/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=renovate.fartlab.dev,resources=renovatescans/finalizers,verbs=update
// +kubebuilder:rbac:groups=renovate.fartlab.dev,resources=renovateplatforms,verbs=get;list;watch
// +kubebuilder:rbac:groups=renovate.fartlab.dev,resources=renovateruns,verbs=get;list;watch;create;update;patch;delete

func (r *RenovateScanReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var scan renovatev1alpha1.RenovateScan
	if err := r.Get(ctx, req.NamespacedName, &scan); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	result, err := r.reconcile(ctx, &scan)
	if updateErr := r.Status().Update(ctx, &scan); updateErr != nil {
		if apierrors.IsConflict(updateErr) {
			log.V(1).Info("status conflict, requeueing", "scan", req.String())
			return ctrl.Result{RequeueAfter: requeueAfterStatusConflict}, nil
		}
		log.Error(updateErr, "status update failed", "scan", req.String())
		if err == nil {
			err = updateErr
		}
	}
	return result, err
}

func (r *RenovateScanReconciler) reconcile(ctx context.Context, scan *renovatev1alpha1.RenovateScan) (ctrl.Result, error) {
	scan.Status.ObservedGeneration = scan.Generation
	if r.Clock == nil {
		r.Clock = clock.RealClock()
	}
	now := r.Clock.Now()

	if scan.Spec.Suspend {
		conditions.MarkFalse(&scan.Status.Conditions,
			conditions.TypeReady, conditions.ReasonSuspended,
			"scan is suspended", scan.Generation)
		return ctrl.Result{}, nil
	}

	platform := &renovatev1alpha1.RenovatePlatform{}
	platformKey := types.NamespacedName{Name: scan.Spec.PlatformRef.Name}
	if err := r.Get(ctx, platformKey, platform); err != nil {
		if apierrors.IsNotFound(err) {
			conditions.MarkFalse(&scan.Status.Conditions,
				conditions.TypeReady, conditions.ReasonPlatformNotReady,
				fmt.Sprintf("RenovatePlatform %q not found", platformKey.Name), scan.Generation)
			return ctrl.Result{RequeueAfter: requeueAfterPlatformPending}, nil
		}
		return ctrl.Result{}, fmt.Errorf("get platform: %w", err)
	}
	if !conditions.IsTrue(platform.Status.Conditions, conditions.TypeReady) {
		conditions.MarkFalse(&scan.Status.Conditions,
			conditions.TypeReady, conditions.ReasonPlatformNotReady,
			fmt.Sprintf("RenovatePlatform %q is not Ready", platform.Name), scan.Generation)
		return ctrl.Result{RequeueAfter: requeueAfterPlatformPending}, nil
	}

	loc, schedule, err := parseSchedule(scan.Spec.Schedule, scan.Spec.TimeZone)
	if err != nil {
		conditions.MarkFalse(&scan.Status.Conditions,
			conditions.TypeReady, conditions.ReasonInvalidSchedule,
			err.Error(), scan.Generation)
		return ctrl.Result{}, nil
	}

	if err := r.refreshActiveRuns(ctx, scan); err != nil {
		return ctrl.Result{}, fmt.Errorf("refresh active runs: %w", err)
	}

	conditions.MarkTrue(&scan.Status.Conditions,
		conditions.TypeReady, conditions.ReasonNextRunComputed,
		"scan is ready to fire", scan.Generation)

	missed, nextFire := computeFireTimes(schedule, scan.Status.LastRunTime, now, loc)
	scan.Status.NextRunTime = &metav1.Time{Time: nextFire}

	if missed.IsZero() {
		conditions.MarkTrue(&scan.Status.Conditions,
			conditions.TypeScheduled, conditions.ReasonNextRunComputed,
			fmt.Sprintf("next run at %s", nextFire.Format(time.RFC3339)),
			scan.Generation)
		return ctrl.Result{RequeueAfter: capRequeueAfter(nextFire.Sub(now))}, nil
	}

	if shouldSkipForConcurrency(scan) {
		conditions.MarkTrue(&scan.Status.Conditions,
			conditions.TypeScheduled, conditions.ReasonNextRunComputed,
			"skipping due to concurrencyPolicy=Forbid; active run in flight", scan.Generation)
		return ctrl.Result{RequeueAfter: capRequeueAfter(nextFire.Sub(now))}, nil
	}

	if err := r.createRun(ctx, scan, platform, missed); err != nil {
		return ctrl.Result{}, err
	}
	if err := r.gcOldRuns(ctx, scan); err != nil {
		return ctrl.Result{}, fmt.Errorf("gc runs: %w", err)
	}

	conditions.MarkTrue(&scan.Status.Conditions,
		conditions.TypeScheduled, conditions.ReasonNextRunComputed,
		fmt.Sprintf("created Run for fire %s; next at %s", missed.Format(time.RFC3339), nextFire.Format(time.RFC3339)),
		scan.Generation)

	return ctrl.Result{RequeueAfter: capRequeueAfter(nextFire.Sub(now))}, nil
}

// parseSchedule decodes the cron expression in the supplied IANA TZ. Empty
// TZ defaults to UTC.
func parseSchedule(expr, tz string) (*time.Location, cron.Schedule, error) {
	if tz == "" {
		tz = "UTC"
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return nil, nil, fmt.Errorf("invalid time zone %q: %w", tz, err)
	}
	schedule, err := cronParser.Parse(expr)
	if err != nil {
		return nil, nil, fmt.Errorf("invalid cron %q: %w", expr, err)
	}
	return loc, schedule, nil
}

// computeFireTimes returns the most recent missed fire time (zero if none
// since lastRunTime) and the next fire time after now.
func computeFireTimes(schedule cron.Schedule, lastRun *metav1.Time, now time.Time, loc *time.Location) (time.Time, time.Time) {
	nowLoc := now.In(loc)
	var startFrom time.Time
	if lastRun != nil {
		startFrom = lastRun.In(loc)
	} else {
		// No prior run: only fire forward — never retroactively schedule.
		startFrom = nowLoc
	}

	var missed time.Time
	t := schedule.Next(startFrom)
	for !t.After(nowLoc) {
		missed = t
		t = schedule.Next(t)
	}
	return missed, t
}

// shouldSkipForConcurrency returns true when concurrencyPolicy=Forbid (or the
// v0.1.0-degraded Replace) and there's an active Run for this Scan.
func shouldSkipForConcurrency(scan *renovatev1alpha1.RenovateScan) bool {
	if len(scan.Status.ActiveRuns) == 0 {
		return false
	}
	switch scan.Spec.ConcurrencyPolicy {
	case renovatev1alpha1.AllowConcurrent:
		return false
	default: // Forbid (default), Replace (degrades to Forbid in v0.1.0)
		return true
	}
}

// refreshActiveRuns rebuilds Status.ActiveRuns from the current Run list,
// retaining only non-terminal Runs. Also updates LastRunTime / LastSuccessfulRunTime
// based on completed Runs.
func (r *RenovateScanReconciler) refreshActiveRuns(ctx context.Context, scan *renovatev1alpha1.RenovateScan) error {
	var runs renovatev1alpha1.RenovateRunList
	if err := r.List(ctx, &runs, client.InNamespace(scan.Namespace)); err != nil {
		return err
	}

	active := make([]corev1.ObjectReference, 0)
	var (
		lastRun           *metav1.Time
		lastSuccessfulRun *metav1.Time
	)
	for i := range runs.Items {
		run := &runs.Items[i]
		if !ownedByScan(run, scan) {
			continue
		}
		if isTerminal(run.Status.Phase) {
			if run.Status.CompletionTime != nil {
				if lastRun == nil || run.Status.CompletionTime.After(lastRun.Time) {
					lastRun = run.Status.CompletionTime
				}
				if run.Status.Phase == renovatev1alpha1.RunPhaseSucceeded {
					if lastSuccessfulRun == nil || run.Status.CompletionTime.After(lastSuccessfulRun.Time) {
						lastSuccessfulRun = run.Status.CompletionTime
					}
				}
			}
			continue
		}
		active = append(active, corev1.ObjectReference{
			APIVersion: run.APIVersion,
			Kind:       run.Kind,
			Name:       run.Name,
			Namespace:  run.Namespace,
			UID:        run.UID,
		})
	}
	scan.Status.ActiveRuns = active
	if lastRun != nil {
		scan.Status.LastRunTime = lastRun
	}
	if lastSuccessfulRun != nil {
		scan.Status.LastSuccessfulRunTime = lastSuccessfulRun
	}
	return nil
}

func ownedByScan(run *renovatev1alpha1.RenovateRun, scan *renovatev1alpha1.RenovateScan) bool {
	for _, owner := range run.OwnerReferences {
		if owner.UID == scan.UID {
			return true
		}
	}
	return false
}

func isTerminal(p renovatev1alpha1.RunPhase) bool {
	return p == renovatev1alpha1.RunPhaseSucceeded || p == renovatev1alpha1.RunPhaseFailed
}

// createRun materializes a new RenovateRun owned by this Scan, snapshotting
// both Platform and Scan specs at the fire time.
func (r *RenovateScanReconciler) createRun(ctx context.Context, scan *renovatev1alpha1.RenovateScan, platform *renovatev1alpha1.RenovatePlatform, fireTime time.Time) error {
	yes := true
	run := &renovatev1alpha1.RenovateRun{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:    scan.Namespace,
			GenerateName: scan.Name + "-",
			Labels: map[string]string{
				"renovate.fartlab.dev/scan":     scan.Name,
				"renovate.fartlab.dev/platform": string(platform.Spec.PlatformType),
			},
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion:         renovatev1alpha1.GroupVersion.String(),
					Kind:               "RenovateScan",
					Name:               scan.Name,
					UID:                scan.UID,
					Controller:         &yes,
					BlockOwnerDeletion: &yes,
				},
			},
			Annotations: map[string]string{
				"renovate.fartlab.dev/scheduled-for": fireTime.Format(time.RFC3339),
			},
		},
		Spec: renovatev1alpha1.RenovateRunSpec{
			ScanRef:          renovatev1alpha1.LocalObjectReference{Name: scan.Name},
			PlatformSnapshot: *platform.Spec.DeepCopy(),
			ScanSnapshot:     *scan.Spec.DeepCopy(),
		},
	}
	if err := r.Create(ctx, run); err != nil {
		return fmt.Errorf("create run: %w", err)
	}
	scan.Status.LastRunRef = &corev1.ObjectReference{
		APIVersion: run.APIVersion,
		Kind:       run.Kind,
		Name:       run.Name,
		Namespace:  run.Namespace,
		UID:        run.UID,
	}
	scan.Status.LastRunTime = &metav1.Time{Time: fireTime}
	return nil
}

// gcOldRuns deletes terminal Runs beyond the configured history limits.
func (r *RenovateScanReconciler) gcOldRuns(ctx context.Context, scan *renovatev1alpha1.RenovateScan) error {
	var runs renovatev1alpha1.RenovateRunList
	if err := r.List(ctx, &runs, client.InNamespace(scan.Namespace)); err != nil {
		return err
	}

	var (
		succeeded []renovatev1alpha1.RenovateRun
		failed    []renovatev1alpha1.RenovateRun
	)
	for _, run := range runs.Items {
		if !ownedByScan(&run, scan) {
			continue
		}
		switch run.Status.Phase {
		case renovatev1alpha1.RunPhaseSucceeded:
			succeeded = append(succeeded, run)
		case renovatev1alpha1.RunPhaseFailed:
			failed = append(failed, run)
		}
	}

	successLimit := int32(3)
	if scan.Spec.SuccessfulRunsHistoryLimit != nil {
		successLimit = *scan.Spec.SuccessfulRunsHistoryLimit
	}
	failLimit := int32(1)
	if scan.Spec.FailedRunsHistoryLimit != nil {
		failLimit = *scan.Spec.FailedRunsHistoryLimit
	}

	if err := r.deleteOldest(ctx, succeeded, int(successLimit)); err != nil {
		return err
	}
	return r.deleteOldest(ctx, failed, int(failLimit))
}

func (r *RenovateScanReconciler) deleteOldest(ctx context.Context, runs []renovatev1alpha1.RenovateRun, keep int) error {
	if len(runs) <= keep {
		return nil
	}
	sort.Slice(runs, func(i, j int) bool {
		// Most recent first; deleting from the tail prunes oldest.
		ai, aj := completionOrCreation(runs[i]), completionOrCreation(runs[j])
		return ai.After(aj)
	})
	for _, run := range runs[keep:] {
		if err := r.Delete(ctx, &run); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("delete old run %s: %w", run.Name, err)
		}
	}
	return nil
}

func completionOrCreation(run renovatev1alpha1.RenovateRun) time.Time {
	if run.Status.CompletionTime != nil {
		return run.Status.CompletionTime.Time
	}
	return run.CreationTimestamp.Time
}

func capRequeueAfter(d time.Duration) time.Duration {
	if d <= 0 {
		return time.Second
	}
	if d > requeueAfterMax {
		return requeueAfterMax
	}
	return d
}

// SetupWithManager wires the controller with watches on Scan + Platform + Run.
func (r *RenovateScanReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.Clock == nil {
		r.Clock = clock.RealClock()
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&renovatev1alpha1.RenovateScan{}).
		Owns(&renovatev1alpha1.RenovateRun{}).
		Watches(
			&renovatev1alpha1.RenovatePlatform{},
			handler.EnqueueRequestsFromMapFunc(r.scansForPlatform),
			builder.WithPredicates(),
		).
		Named("renovatescan").
		Complete(r)
}

// scansForPlatform returns reconcile requests for every Scan whose
// platformRef matches the supplied Platform. Used so a Platform Ready flip
// kicks the dependent Scans without polling.
func (r *RenovateScanReconciler) scansForPlatform(ctx context.Context, obj client.Object) []reconcile.Request {
	platform, ok := obj.(*renovatev1alpha1.RenovatePlatform)
	if !ok {
		return nil
	}
	var list renovatev1alpha1.RenovateScanList
	if err := r.List(ctx, &list); err != nil {
		return nil
	}
	out := make([]reconcile.Request, 0)
	for _, s := range list.Items {
		if s.Spec.PlatformRef.Name == platform.Name {
			out = append(out, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: s.Namespace, Name: s.Name}})
		}
	}
	return out
}
