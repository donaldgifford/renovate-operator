---
id: INV-0001
title: "Render RenovateScan Next Run printer column accurately for future timestamps"
status: Open
author: Donald Gifford
created: 2026-05-02
---
<!-- markdownlint-disable-file MD025 MD041 -->

# INV 0001: Render RenovateScan Next Run printer column accurately for future timestamps

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
- [Conclusion](#conclusion)
- [Recommendation](#recommendation)
- [References](#references)
<!--toc:end-->

## Question

`kubectl get rscan -A` prints `<invalid>` in the **Next Run** column even
though `.status.nextRunTime` contains a perfectly valid future RFC3339
timestamp. What's the smallest change that makes the column display the
information operators actually want to see (e.g., "5m", "in 2h", or — if
relative future times aren't supported — at least the literal timestamp)?

## Hypothesis

The CRD declares **Next Run** as `type=date`, which kubectl renders as a
*relative duration since now* (`5m`, `2h`). That formatter has only the
"since" form built in — it returns the literal string `<invalid>` for any
timestamp in the future. The column type is wrong for a forward-looking
field.

Switching the column to `type=string` will surface the literal RFC3339
timestamp (`2026-05-02T11:10:00Z`) instead of `<invalid>`. Less ergonomic
than `5m`, but correct.

A more ambitious fix — adding a custom *duration-until-now* formatter —
isn't available through the CRD `+kubebuilder:printcolumn` marker today;
it would require either a custom kubectl plugin or a derived status field
holding the rendered duration string at reconcile time.

## Context

Surfaced live during the homelab acceptance run on 2026-05-02. The
operator was working correctly: every Scan had a populated `Ready=True`
condition, a populated `Scheduled=True` condition with a human-readable
"next run at <time>" message, and a valid `.status.nextRunTime` field.
But `kubectl get rscan` showed `<invalid>` for every Scan's Next Run
column, which suggested (at first glance) a broken controller. The Last
Run column behaved correctly — empty for never-fired Scans, relative
"5m" / "2h" for fired ones.

All three Scans on the homelab cluster, both platforms, every cadence:

```text
NAMESPACE   NAME              PLATFORM   SCHEDULE      LAST RUN   NEXT RUN    READY   AGE
renovate    nightly-forgejo   forgejo    0 2 * * *                <invalid>   True    6h36m
renovate    nightly-gh        github     0 2 * * *                <invalid>   True    6h37m
renovate    test-gh           github     */5 * * * *              <invalid>   True    10m
```

The status YAML showed `nextRunTime: "2026-05-02T11:10:00Z"` for `test-gh`
— a real timestamp ~6 minutes in the future from the snapshot. So the
data is fine; the *column rendering* is the issue. The bug reproduces
deterministically across:

- **Both platform kinds** (forgejo + github)
- **Both schedule cadences** (nightly `0 2 * * *` and frequent `*/5 * * * *`)
- **Both never-fired and uptime-aged Scans** (AGE 10m through 6h+)

**Triggered by:** First homelab acceptance run for v0.1.0 (IMPL-0001 Phase
9). Not a v0.1.0 acceptance blocker — the data is correct and operators
can read `.status.nextRunTime` directly via `-o yaml`. But it's a
papercut bad enough to mislead a reader into thinking the controller is
broken.

## Approach

1. **Confirm the kubectl behavior** by reading the kubectl source for
   `type=date` printer-column rendering. Specifically:
   - `staging/src/k8s.io/kubectl/pkg/cmd/get/customcolumn.go` and
     `pkg/printers/internalversion/printers.go` — locate the `dateFormat`
     /`translateTimestampSince` paths.
   - Confirm there is no built-in `type=duration-until` or equivalent for
     CRD printer columns.
2. **Enumerate alternatives** for surfacing future timestamps in CRD
   printer columns:
   - `type=string` with the raw RFC3339 timestamp (current preferred fix).
   - `type=string` with a controller-rendered duration field
     (`.status.nextRunInDuration: "5m"`) populated at reconcile time.
   - Drop the **Next Run** column from the default printer set; surface
     it only via `kubectl get -o wide` or `kubectl describe`.
   - Custom kubectl plugin / krew tool — out of scope for v0.1.x.
3. **Spot-check the other date-typed columns** on our CRDs to make sure
   none of them carry future-timestamp values that would have the same
   bug:
   - `RenovateScan`: Last Run (past, fine), **Next Run (future, broken)**, Age (past, fine).
   - `RenovateRun`: Started (past, fine), Completed (past, fine).
   - `RenovatePlatform`: Age (past, fine).
4. **Implement the smallest viable fix** behind a feature-style PR:
   change `type=date` → `type=string` on Next Run, regenerate CRDs via
   `make manifests`, refresh the v0.1.x release notes.
5. **Document the column rendering** in `docs/usage/renovate-scan.md` so
   readers know what they're looking at when the column shows a literal
   RFC3339 timestamp instead of a relative duration.

## Environment

| Component | Version / Value |
|-----------|----------------|
| Operator chart / image | `v0.1.0` (will be `v0.1.1` after PR #10 lands) |
| Kubernetes apiserver | homelab cluster, K8s ≥ 1.27 (per IMPL-0001 prereq) |
| `kubectl` | client running on the operator's host (version not yet captured; will note in Findings) |
| Affected CRD field | `RenovateScan.status.nextRunTime` (`*metav1.Time`) |
| Affected printer marker | `// +kubebuilder:printcolumn:name="Next Run",type="date",JSONPath=".status.nextRunTime"` in `api/v1alpha1/renovatescan_types.go:210` |

## Findings

> **In progress.** Filling these in as the investigation proceeds.

### Observation 1 — `<invalid>` reproduces deterministically

Every `RenovateScan` whose `.status.nextRunTime` is in the future renders
as `<invalid>` in the Next Run column. Every `RenovateScan` whose
`.status.nextRunTime` is unset renders as the empty string. No Scan in
the homelab has rendered a duration in the Next Run column.

### Observation 2 — `kubectl get -o yaml` shows correct data

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

The `Scheduled` condition's `message` field already carries a
human-readable "next run at <ISO timestamp>" string in the Scan's local
zone — operators who go to YAML get a usable answer.

### Observation 3 — kubectl source path (TBD)

Need to confirm by reading kubectl source. Expected path:
`staging/src/k8s.io/kubectl/pkg/printers/internalversion/printers.go` →
look for `translateTimestampSince`. Hypothesis: function returns
`<invalid>` whenever `now.Before(t)`. If confirmed, the fix is purely on
the CRD side; nothing in kubectl needs to change.

### Observation 4 — Other date-typed columns audited (TBD)

`RenovateRun` columns Started + Completed are both past-only; Age on
every CRD is past-only. Last Run on Scan is past-only. So **Next Run is
the only column carrying a future timestamp** — fixing it in isolation
is sufficient.

## Conclusion

**Answer:** <!-- Yes / No / Inconclusive — fill in once Findings 3 + 4 are complete. -->

Tentatively: yes, the column type is wrong. The minimal, in-scope fix is
to switch Next Run from `type=date` to `type=string` so the literal
RFC3339 timestamp renders. Anything fancier (relative-future formatting,
custom plugin, derived duration field) is over-scoped for v0.1.x.

## Recommendation

Once Findings 3 + 4 confirm the hypothesis:

1. **Edit** `api/v1alpha1/renovatescan_types.go:210`:
   ```go
   // +kubebuilder:printcolumn:name="Next Run",type="string",JSONPath=".status.nextRunTime"
   ```
2. **Run** `make manifests` to regenerate the CRDs under
   `dist/chart/templates/crd/` and `config/crd/bases/`.
3. **Add** a short note to `docs/usage/renovate-scan.md` explaining why
   Next Run shows an RFC3339 timestamp rather than a duration (kubectl
   limitation, not an operator design choice).
4. **Bundle** the change with the next routine PR (no need for its own
   release; ship in `v0.1.x` whenever the next merge fires the release
   pipeline).

If at any point a future v0.2.x release wants ergonomic relative-future
rendering, the right path is **a controller-rendered string field**
(e.g., `.status.nextRunIn: "5m"` recomputed each reconcile) that the
printer column reads as `type=string`. Tracked as a follow-up rather
than expanding this investigation's scope.

## References

- [IMPL-0001 Phase 9](../impl/0001-renovate-operator-v010-implementation.md#phase-9-homelab-deploy-and-v010-cutover) — the homelab acceptance run that surfaced this.
- `api/v1alpha1/renovatescan_types.go:210` — the printer-column marker to change.
- `internal/controller/renovatescan_controller.go:148-149` — confirms `.status.nextRunTime` is correctly populated; the bug is purely in column rendering.
- [kubectl printer column reference](https://book.kubebuilder.io/reference/generating-crd.html#additional-printer-columns) (kubebuilder docs).
