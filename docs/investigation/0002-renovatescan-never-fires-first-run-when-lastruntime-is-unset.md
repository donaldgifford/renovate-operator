---
id: INV-0002
title: "RenovateScan never fires first Run when LastRunTime is unset"
status: Open
author: Donald Gifford
created: 2026-05-02
---
<!-- markdownlint-disable-file MD025 MD041 -->

# INV 0002: RenovateScan never fires first Run when LastRunTime is unset

**Status:** Open
**Author:** Donald Gifford
**Date:** 2026-05-02

<!--toc:start-->
- [Question](#question)
- [Hypothesis](#hypothesis)
- [Context](#context)
- [Approach](#approach)
- [Environment](#environment)
- [Findings](#findings)
  - [Observation 1 — */5 Scan idle for 10 minutes](#observation-1--5-scan-idle-for-10-minutes)
  - [Observation 2 — controller logic walkthrough](#observation-2--controller-logic-walkthrough)
  - [Observation 3 — computeFireTimes walk](#observation-3--computefiretimes-walk)
  - [Observation 4 — existing test enshrines the bug](#observation-4--existing-test-enshrines-the-bug)
- [Conclusion](#conclusion)
- [Recommendation](#recommendation)
- [References](#references)
<!--toc:end-->

## Question

A `RenovateScan` with `*/5 * * * *` schedule sits for 10+ minutes with
`lastRunTime` empty and zero `RenovateRun` resources created — i.e., it
**never fires a Run** even though the controller is reconciling and
`Status.Conditions` show `Ready=True`, `Scheduled=True`. What does it
take in the controller for the first scheduled fire-time of a never-run
Scan to actually materialize a Run, instead of being skipped forever?

## Hypothesis

`computeFireTimes` (`internal/controller/renovatescan_controller.go:200`)
returns `missed=zero` whenever `lastRun==nil`, regardless of whether
`now` is past a recent fire boundary. Combined with
`cron.Schedule.Next(t)` returning the next fire **strictly after** `t`,
this means:

1. **First reconcile** at, say, 06:25 UTC for a `*/5` Scan: `startFrom =
   now = 06:25`. `schedule.Next(06:25) = 06:30`. Loop exits because
   06:30 > now → `missed=zero`, `nextFire=06:30`. Requeue at 06:30.
2. **Second reconcile** at ≈06:30:00.x: `lastRun` is *still* nil
   (nothing fired). `startFrom = now = 06:30:00.x`.
   `schedule.Next(06:30:00.x) = 06:35` (strictly after). Loop exits
   immediately → `missed=zero`, `nextFire=06:35`. Requeue at 06:35.
3. Repeat forever. Every fire boundary lands ~50 ms past `now`, every
   `Next(now)` jumps to the next-next boundary, and the controller
   never reaches the `createRun` branch (line 166).

The fix is small and entirely inside `computeFireTimes`: when computing
the missed fire, **don't use `now` itself as the `startFrom` for a
never-fired Scan** — back off by a small grace window (≥ controller
queue jitter, e.g. one minute) so the most-recent boundary
at-or-just-before `now` gets captured as `missed`. That preserves the
"never retroactively schedule" guarantee for Scans newly created
*before* their first boundary, while letting the **first boundary
on-or-after creation** actually fire.

## Context

Surfaced live during the homelab acceptance run on 2026-05-02 alongside
[INV-0001](0001-render-renovatescan-next-run-printer-column-accurately-for.md).
A test Scan was deliberately set to `*/5 * * * *` to verify the
operator's end-to-end path on a short cycle. After 10 minutes the Scan
showed:

```text
NAMESPACE   NAME      PLATFORM   SCHEDULE      LAST RUN   NEXT RUN    READY   AGE
renovate    test-gh   github     */5 * * * *              <invalid>   True    10m
```

`LAST RUN` empty, `kubectl get rrun -A` returned nothing. The two
nightly Scans (`0 2 * * *`) showed the same shape but with AGE=6h+ —
they would also miss their 02:00 fire when it arrives, by the same
mechanism.

`Status.Conditions` reported a happy path — `Ready=True
NextRunComputed`, `Scheduled=True NextRunComputed "next run at
<future time>"` — which is exactly what the controller logs when it
takes the early-return branch at line 156 (no missed fire, just
requeue). The state-transition that *should* happen at line 166
(`createRun(ctx, scan, platform, missed)`) never executes for a Scan
whose `LastRunTime` was never set.

This is the v0.1.0 behavior for the **first** fire only. Once a Run has
fired (somehow — e.g., manually creating one, or a clock skew that
makes "now" land before the boundary), `LastRunTime` populates,
`startFrom = lastRun`, and subsequent fires work correctly because the
loop in `computeFireTimes` walks forward from a known prior boundary.

**Triggered by:** Phase 9 homelab acceptance — first attempt to verify
end-to-end Run creation against a real GitHub App + repo. No Run ever
fired despite scheduling logic appearing healthy.

## Approach

1. **Confirm the trace** by reading `RenovateScanReconciler.Reconcile`
   (lines 130–178) and `computeFireTimes` (lines 200–217). Note the
   explicit comment on line 206 ("No prior run: only fire forward —
   never retroactively schedule") and confirm the loop at lines
   211–215 cannot capture a missed fire when `startFrom = nowLoc`.
2. **Confirm `cron.Schedule.Next` semantics** — it returns the next
   activation **strictly** after its input. Two ways to verify:
   a. Read robfig/cron/v3 source.
   b. Write a one-line Go test asserting
      `sched.Next(boundary) != boundary`.
3. **Check existing test coverage**:
   `internal/controller/renovatescan_helpers_test.go::TestComputeFireTimes_NoLastRun`
   exists and *asserts the buggy behavior* (`missed.IsZero()` for a
   `now` before the next boundary). What's missing is a test for
   `now == boundary + ε` with `lastRun=nil` — that's where the bug
   bites.
4. **Enumerate fixes**:
   a. **Grace window** — `startFrom = nowLoc.Add(-fireGrace)` when
      `lastRun==nil`. Smallest blast radius. `fireGrace` should be
      generous enough to cover controller-runtime queue jitter +
      requeue scheduling slop (1 minute is plenty; even 10 seconds
      would work in practice).
   b. **Persist creation time as a synthetic `LastRunTime`** on first
      reconcile. Treats Scan creation as the t-zero for cron walking.
      Slightly more code; no behavior change once a real Run has fired.
   c. **Use a "last expected fire" derivation** —
      `startFrom = nowLoc.Truncate(scheduleInterval)`. Doesn't
      generalize to arbitrary cron expressions (no easy "interval"
      for `0 9-17 * * MON-FRI`).
5. **Add regression test coverage** — at minimum:
   - `TestComputeFireTimes_NoLastRunPastBoundary` — `now` is one minute
     past a boundary, `lastRun=nil`, expect `missed=boundary`.
   - Reconciler-level test (envtest or fake) creating a Scan timed so
     the next reconcile lands at the boundary, asserting a Run is
     created.
6. **Implement the fix + tests + bump v0.1.x**, ship as `v0.1.2` (or
   bundle with INV-0001's printer-column fix as a single
   bug-fix PR).

## Environment

| Component | Version / Value |
|-----------|----------------|
| Operator chart / image | `v0.1.0` (running on homelab cluster) |
| Kubernetes apiserver | homelab cluster, K8s ≥ 1.27 (per IMPL-0001 prereq) |
| Cron library | `github.com/robfig/cron/v3` |
| Affected function | `computeFireTimes` in `internal/controller/renovatescan_controller.go:200` |
| Affected reconcile branch | early-return at `internal/controller/renovatescan_controller.go:156` (skips line 166 `createRun`) |
| Test enshrining current behavior | `internal/controller/renovatescan_helpers_test.go:75` (`TestComputeFireTimes_NoLastRun`) |

## Findings

### Observation 1 — `*/5` Scan idle for 10 minutes

`test-gh` was created at the homelab cluster with schedule
`*/5 * * * *`. After 10 minutes wall-clock, `kubectl get rscan -A`
showed `LAST RUN` empty and `kubectl get rrun -A -n renovate` returned
no resources. Status YAML looked clean otherwise:

```yaml
status:
  conditions:
    - type: Ready
      status: "True"
      reason: NextRunComputed
      message: scan is ready to fire
    - type: Scheduled
      status: "True"
      reason: NextRunComputed
      message: next run at 2026-05-02T07:10:00-04:00
  nextRunTime: "2026-05-02T11:10:00Z"
  observedGeneration: 1
```

`nextRunTime` advances on every requeue; `lastRunTime` never appears.

### Observation 2 — controller logic walkthrough

`Reconcile` body (`internal/controller/renovatescan_controller.go:148-178`):

```go
missed, nextFire := computeFireTimes(schedule, scan.Status.LastRunTime, now, loc)
scan.Status.NextRunTime = &metav1.Time{Time: nextFire}

if missed.IsZero() {
    // ... mark Scheduled=True ...
    return ctrl.Result{RequeueAfter: capRequeueAfter(nextFire.Sub(now))}, nil
}
// only reached if missed != zero
if err := r.createRun(ctx, scan, platform, missed); err != nil { ... }
```

The early-return at the `missed.IsZero()` branch is the only path
exercised when `LastRunTime` is unset.

### Observation 3 — `computeFireTimes` walk

`internal/controller/renovatescan_controller.go:200-217`:

```go
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
```

When `lastRun==nil`, `startFrom = nowLoc`. `schedule.Next(nowLoc)`
returns a `t` strictly after `nowLoc`, so `t.After(nowLoc)` is always
true, the loop never executes, and `missed` stays zero. **By
construction, this branch can never produce a non-zero `missed`** —
regardless of how long the Scan has existed or how recently a boundary
just passed.

### Observation 4 — existing test enshrines the bug

`internal/controller/renovatescan_helpers_test.go:75`
(`TestComputeFireTimes_NoLastRun`) explicitly asserts
`missed.IsZero()` for `lastRun=nil, now=03:00, schedule=daily-at-04:00`.
That's correct for the case-the-test-covers (now is *before* any
boundary). What's missing is the case-the-bug-bites:
`now=04:00:00.05, lastRun=nil` — which never lands in any test.

The bug isn't in test logic; it's that `computeFireTimes` was designed
around the implicit assumption that a Scan's *first* reconcile happens
*before* its first scheduled boundary. That holds only briefly after
creation; once the boundary passes (with `lastRun` still nil), the
function silently can't fire it.

## Conclusion

**Answer:** Yes, confirmed. `computeFireTimes` cannot return a non-zero
`missed` when `lastRun==nil`, by construction. Combined with
`cron.Schedule.Next` returning strictly-after, every fire boundary on a
never-run Scan is skipped. The bug is reproducible 100% of the time on
any Scan whose first reconcile after a scheduled boundary still has
`LastRunTime==nil` — which is **always**, because nothing else can set
`LastRunTime` for a never-fired Scan.

This is a v0.1.0 bug that blocks Phase 9 acceptance: no Scan ever fires
its first Run unless an operator manually populates `Status.LastRunTime`
out-of-band.

## Recommendation

1. **Fix `computeFireTimes`** (`internal/controller/renovatescan_controller.go:200-217`):

    ```go
    const fireGrace = 1 * time.Minute

    if lastRun != nil {
        startFrom = lastRun.In(loc)
    } else {
        // No prior run: walk back one fire-grace window so a boundary
        // at-or-just-before now can be captured as a missed fire. This
        // preserves the "never retroactively schedule" guarantee for
        // Scans newly created *before* their first boundary, while
        // letting the first on-or-after-creation boundary actually fire.
        startFrom = nowLoc.Add(-fireGrace)
    }
    ```

   `fireGrace` of one minute is conservative — it covers worst-case
   controller-runtime queue jitter and requeue scheduling slop.
2. **Add regression tests** in
   `internal/controller/renovatescan_helpers_test.go`:
   - `TestComputeFireTimes_NoLastRun_PastBoundary`: `now =
     boundary+1s`, `lastRun=nil`, expect `missed=boundary`,
     `next=boundary+interval`.
   - `TestComputeFireTimes_NoLastRun_BeforeBoundary` (existing
     `TestComputeFireTimes_NoLastRun` covers this): unchanged
     behavior, `missed=zero`.
3. **Add an envtest or fake-client reconciler test** that creates a
   Scan with a schedule timed so the next reconcile lands past the
   boundary, and asserts a `RenovateRun` materializes.
4. **Bundle the fix** with INV-0001's printer-column fix as a single
   bug-fix PR, ship as `v0.1.2`.

## References

- [INV-0001](0001-render-renovatescan-next-run-printer-column-accurately-for.md) — sibling printer-column rendering bug surfaced in the same homelab debug session.
- [IMPL-0001 Phase 9](../impl/0001-renovate-operator-v010-implementation.md#phase-9-homelab-deploy-and-v010-cutover) — the homelab acceptance run that surfaced this.
- `internal/controller/renovatescan_controller.go:200-217` — `computeFireTimes`, the function to change.
- `internal/controller/renovatescan_controller.go:148-156` — the `missed.IsZero()` early-return that this fix unblocks.
- `internal/controller/renovatescan_helpers_test.go:75` — `TestComputeFireTimes_NoLastRun`, the existing test to extend.
