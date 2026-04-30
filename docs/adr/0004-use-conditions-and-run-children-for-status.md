---
id: ADR-0004
title: "Use metav1.Condition and Run child resources for status"
status: Accepted
author: donaldgifford
created: 2026-04-26
---
<!-- markdownlint-disable-file MD025 MD041 -->

# 0004. Use metav1.Condition and Run child resources for status

<!--toc:start-->
- [Status](#status)
- [Context](#context)
- [Decision](#decision)
  - [Use []metav1.Condition for all status conditions](#use-metav1condition-for-all-status-conditions)
  - [Define a small, stable condition type vocabulary per CRD](#define-a-small-stable-condition-type-vocabulary-per-crd)
  - [Keep aggregate state on the Scan, granular state on the Run](#keep-aggregate-state-on-the-scan-granular-state-on-the-run)
  - [Drive everything through observedGeneration](#drive-everything-through-observedgeneration)
  - [+kubebuilder:printcolumn for kubectl ergonomics](#kubebuilderprintcolumn-for-kubectl-ergonomics)
- [Consequences](#consequences)
  - [Positive](#positive)
  - [Negative](#negative)
  - [Neutral](#neutral)
- [Alternatives Considered](#alternatives-considered)
  - [Custom string status fields (the mogenius approach)](#custom-string-status-fields-the-mogenius-approach)
  - [Phase enum derived from conditions only](#phase-enum-derived-from-conditions-only)
  - [Run history as .status.history[] on Scan](#run-history-as-statushistory-on-scan)
  - [Use Job objects directly, no RenovateRun indirection](#use-job-objects-directly-no-renovaterun-indirection)
- [References](#references)
<!--toc:end-->

## Status

Proposed

## Context

[ADR-0003](0003-multi-crd-architecture.md) established that we model execution state with a `RenovateRun` child resource owned by `RenovateScan`. This ADR is about the field-level shape of the status on each CRD: how transitions are represented, how to make `kubectl wait` work, how to produce useful printed columns.

Two concrete decisions to make:

1. **What goes in `.status.conditions`?** The Kubernetes-wide convention since 1.19+ is `[]metav1.Condition`. Mogenius's CRD uses bare string enums (`"Scheduled"`, `"Running"`, `"Failed"`, `"Succeeded"`) at the resource level and again per-project inside an array. This conflicts with the standard convention and breaks `kubectl wait --for=condition=...` semantics.
2. **Where does run-level state live — on the Scan, or on a separate Run resource?** Mogenius keeps a `.status.projects[]` and `.status.history[]` array on the parent `RenovateJob`. We argued for child resources in ADR-0003; this ADR records the corollary: the Scan's status reflects *aggregate* state (last run, next run, current condition summary) and individual runs carry their own granular conditions.

## Decision

### Use `[]metav1.Condition` for all status conditions

All three CRDs (`RenovatePlatform`, `RenovateScan`, `RenovateRun`) carry a `Conditions []metav1.Condition` field on their status, with kubebuilder markers:

```go
// +listType=map
// +listMapKey=type
// +optional
Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type" protobuf:"bytes,1,rep,name=conditions"`
```

We use the `apimachinery/pkg/api/meta` helpers (`meta.SetStatusCondition`, `meta.IsStatusConditionTrue`, `meta.FindStatusCondition`) for all condition manipulation — never direct slice mutation.

### Define a small, stable condition type vocabulary per CRD

| CRD | Condition Type | Meaning when `True` |
|-----|----------------|---------------------|
| `RenovatePlatform` | `Ready` | Credentials secret resolves, platform reachable on a no-op API call |
| `RenovateScan` | `Ready` | Referenced `RenovatePlatform` exists and is `Ready`, schedule parses |
| `RenovateScan` | `Scheduled` | Next run time has been computed and stored in `.status.nextRunTime` |
| `RenovateRun` | `Started` | Run was admitted by the controller; reconcile loop entered. |
| `RenovateRun` | `Discovered` | Discovery phase complete; shard ConfigMap and worker Job created |
| `RenovateRun` | `Succeeded` | Owned Job reached `JobComplete` (all shards finished successfully) |
| `RenovateRun` | `Failed` | Owned Job reached `JobFailed` (mutually exclusive with `Succeeded`) |

`RenovateRun.status.phase` is a typed enum (`Pending`/`Discovering`/`Running`/`Succeeded`/`Failed`) — it serves both as the reconciler's state-machine cursor and as a `kubectl`-friendly printer column. `phase` and `conditions` are not duplicate sources of truth: `phase` records *which step of the state machine the controller is on*, and `conditions` record *what has been confirmed*. Both are written only by the Run reconciler, so they cannot drift. (Sharding does not get its own phase or condition — it happens inline during the `Pending → Discovering` transition and is signalled by `Discovered=True`.)

Condition types added in later versions are additive; existing types are not removed without a deprecation period.

### Keep aggregate state on the Scan, granular state on the Run

`RenovateScan.status` carries:

- `conditions []metav1.Condition` — `Ready`, `Scheduled` types above.
- `lastRunTime *metav1.Time`
- `lastSuccessfulRunTime *metav1.Time`
- `nextRunTime *metav1.Time`
- `lastRunRef *corev1.ObjectReference` — pointer to the most recent `RenovateRun`.
- `observedGeneration int64`

`RenovateScan.status` does **not** carry a list of historical runs, nor a list of discovered projects, nor a list of in-flight project executions. Those live on `RenovateRun`.

`RenovateRun.status` carries:

- `conditions []metav1.Condition` — `Started`, `Discovered`, `Succeeded`, `Failed`.
- `phase RunPhase` — typed enum (`Pending`/`Discovering`/`Running`/`Succeeded`/`Failed`); state-machine cursor and printer column.
- `startTime *metav1.Time`
- `discoveryCompletionTime *metav1.Time`
- `workersStartTime *metav1.Time`
- `completionTime *metav1.Time`
- `discoveredRepos int32` — how many repos discovery returned (after `requireConfig` filter).
- `actualWorkers int32` — final `N` (parallelism factor) chosen for the Job.
- `succeededShards int32` / `failedShards int32` — running tally read from the owned Job's index counters.
- `workerJobRef *corev1.ObjectReference` — owned Indexed `batch/v1.Job`.
- `shardConfigMapRef *corev1.ObjectReference` — owned ConfigMap with the per-shard repo lists.
- `observedGeneration int64`

Note: `RenovateRun.status` does **not** carry a per-repo result list. That information is in worker pod logs (Loki) and aggregated in metrics (Prometheus). Putting per-repo state in CRD status would re-create the mogenius problem at the Run level.

### Drive everything through `observedGeneration`

Every reconciler writes `status.observedGeneration = obj.Generation` on every successful reconcile. Consumers checking "is the controller caught up?" use the standard idiom: compare `status.observedGeneration` to `metadata.generation`.

### `+kubebuilder:printcolumn` for kubectl ergonomics

Each CRD declares printer columns so `kubectl get` is informative without `-o yaml`:

```go
// +kubebuilder:printcolumn:name="Platform",type="string",JSONPath=".spec.platformRef.name"
// +kubebuilder:printcolumn:name="Schedule",type="string",JSONPath=".spec.schedule"
// +kubebuilder:printcolumn:name="Last Run",type="date",JSONPath=".status.lastRunTime"
// +kubebuilder:printcolumn:name="Next Run",type="date",JSONPath=".status.nextRunTime"
// +kubebuilder:printcolumn:name="Ready",type="string",JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
```

## Consequences

### Positive

- **`kubectl wait --for=condition=Ready renovatescan/foo` works** out of the box. So does `--for=condition=Succeeded renovaterun/...`. This unblocks GitOps health checks (ArgoCD, Flux) without custom health rules.
- **Condition reasons and messages are observable in `kubectl describe`** without parsing custom enums. `Reason: PlatformNotFound` / `Message: RenovatePlatform "github-com" not found in cluster` is self-explanatory.
- **`observedGeneration` makes "did the controller see my change yet?" answerable** without timing assumptions.
- **Aggregate vs. granular split keeps the Scan's status small.** Long-running Scans don't accumulate status bloat; etcd stays happy.
- **Standard semantics interop with Kubernetes tooling.** `kstatus` (used by `kustomize status` and ArgoCD's health framework) recognizes the convention; no custom health checks required.

### Negative

- **Two places to look during debugging.** "Why hasn't my scan run?" requires checking the Scan's conditions *and* the most recent Run's conditions. We mitigate with clear `Reason` strings on Scan conditions that point at Run names.
- **Condition arrays require careful merge logic.** Direct `append` is wrong; `meta.SetStatusCondition` is right. We add a lint rule (or at minimum, code review checklist) to catch direct manipulation.

### Neutral

- We commit to never reusing condition types across CRDs with different semantics. `Ready` on `RenovatePlatform` ≠ `Ready` on `RenovateScan`, but both follow the same K8s-wide meaning of "all preconditions are met."

## Alternatives Considered

### Custom string status fields (the mogenius approach)

```go
type Status string
const (
    Scheduled Status = "Scheduled"
    Running   Status = "Running"
    Failed    Status = "Failed"
    Succeeded Status = "Succeeded"
)
```

Rejected because it loses interop with `kubectl wait`, with `kstatus`, with ArgoCD's health framework, and with Kubernetes' own pattern across every modern controller. Custom string status fields are a mid-2010s pattern that the community has moved past.

### Phase enum derived from conditions only

A purer take where `phase` is computed read-only from the `conditions` list rather than carried as a separate field. **Rejected** because the Run reconciler is a state machine and needs a serialized cursor to switch on without re-deriving from conditions every reconcile. Carrying both is fine in practice as long as a single writer (the reconciler) owns both — which it does. See the Decision section for the non-drift argument.

### Run history as `.status.history[]` on Scan

The mogenius approach. Rejected in [ADR-0003](0003-multi-crd-architecture.md). Listed here for completeness.

### Use Job objects directly, no `RenovateRun` indirection

A reasonable simplification, attractive for an MVP. **Rejected** for v0.1.0 because the parallelism story (the headline feature of this operator) requires per-run state that has no home on `Job` itself: discovered repo count, actual worker count, shard ConfigMap reference, and the `Pending → Discovering → Running` phase transitions. Stuffing this into `Job` annotations works mechanically but breaks `kubectl get` ergonomics and observability. `RenovateRun` carries this state in a typed, queryable shape from day one.

## References

- [Kubernetes API conventions — typical status properties](https://github.com/kubernetes/community/blob/master/contributors/devel/sig-architecture/api-conventions.md#typical-status-properties)
- [`metav1.Condition` definition](https://pkg.go.dev/k8s.io/apimachinery/pkg/apis/meta/v1#Condition)
- [`apimachinery/pkg/api/meta` condition helpers](https://pkg.go.dev/k8s.io/apimachinery/pkg/api/meta)
- [`kstatus` — generic status computation](https://github.com/kubernetes-sigs/cli-utils/tree/master/pkg/kstatus)
- [ADR-0003: Multi-CRD architecture](0003-multi-crd-architecture.md)
