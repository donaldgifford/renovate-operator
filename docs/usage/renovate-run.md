# RenovateRun

Ephemeral, namespaced child of a `RenovateScan`. **Created by the controller,
not authored by hand.** One Run per scheduled fire-time. Owned by the parent
Scan via `ownerReferences`, so deleting a Scan cascades to all its Runs.

| Property    | Value                  |
| ----------- | ---------------------- |
| API group   | `renovate.fartlab.dev` |
| Kind        | `RenovateRun`          |
| Scope       | Namespaced             |
| Short names | `rr`, `rrun`           |

## Why frozen snapshots?

`spec.platformSnapshot` and `spec.scanSnapshot` are full copies of the Platform
spec and Scan spec at Run creation time. Once a Run exists, editing the Platform
or Scan does **not** retroactively change in-flight behavior.

This matters when:

- Someone bumps `discovery.filter` mid-execution — the in-flight Run keeps its
  original filter set; the next scheduled Run picks up the new one.
- A Platform's credential rotates — already-discovered Runs use the snapshotted
  credential reference (the operator still resolves the live Secret, so a
  rotated Secret value flows through, but a _changed_ `secretRef` doesn't).
- A Scan's `workers.maxWorkers` shrinks — the in-flight Run's worker count is
  fixed at the Discovering → Running transition.

## Phase machine

```
Pending  →  Discovering  →  Running  →  Succeeded
                                    ↘   Failed
```

The `status.phase` field is a typed cursor. Conditions remain the source of
truth.

| Phase         | What's happening                                                                                                                                                      | Typical duration                                             |
| ------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------ |
| `Pending`     | Run created; controller has not yet observed it.                                                                                                                      | < 1 s                                                        |
| `Discovering` | Mirroring credential Secret, enumerating repos via platform API, applying `requireConfig` and discovery filters, building shard ConfigMap, creating the worker `Job`. | seconds to minutes (depends on org size + rate-limit budget) |
| `Running`     | Worker Job exists, shards are executing.                                                                                                                              | minutes to hours (depends on package manager mix)            |
| `Succeeded`   | Every shard exited 0. Terminal.                                                                                                                                       | —                                                            |
| `Failed`      | Worker Job exhausted `backoffLimitPerIndex`, OR discovery hit a permanent error. Terminal.                                                                            | —                                                            |

Conditions tracked: `Started`, `Discovered`, `Succeeded`, `Failed`.

## Status

```bash
kubectl get rrun <name> -n <ns> -o yaml | yq .status
```

| Field                     | Meaning                                                                                    |
| ------------------------- | ------------------------------------------------------------------------------------------ |
| `phase`                   | Cursor (see table above).                                                                  |
| `conditions`              | Authoritative state.                                                                       |
| `startTime`               | First observation by the Run controller — `Pending → Discovering`.                         |
| `discoveryCompletionTime` | Discovery + sharding + Job creation done — `Discovering → Running`.                        |
| `workersStartTime`        | Worker Job became active.                                                                  |
| `completionTime`          | Terminal time (`Succeeded` or `Failed`).                                                   |
| `discoveredRepos`         | Count of repos that survived `requireConfig` and discovery filters.                        |
| `actualWorkers`           | `clamp(ceil(discoveredRepos / reposPerWorker), min, max)`. Fixed at Discovering → Running. |
| `shardConfigMapRef`       | The `ConfigMap` holding `shard-NNNN.json` (or `.json.gz` when serialized > 900 KiB).       |
| `workerJobRef`            | The owned Indexed `batch/v1.Job`.                                                          |
| `succeededShards`         | Mirror of `Job.status.succeeded`.                                                          |
| `failedShards`            | Mirror of `Job.status.failed`.                                                             |

Printer columns:

```
$ kubectl get rrun -A
NAMESPACE   NAME              SCAN      PHASE       REPOS   WORKERS   STARTED   COMPLETED
renovate    nightly-1746...   nightly   Succeeded   42      1         1h        59m
```

## Inspecting a Run

```bash
RUN=$(kubectl get rrun -n renovate -o name | tail -1)

# Status snapshot
kubectl get $RUN -n renovate -o yaml | yq .status

# Owned shard ConfigMap (one entry per shard)
kubectl get cm -n renovate -l app.kubernetes.io/name=renovate-operator,renovate.fartlab.dev/run=${RUN##*/}
# or directly via the ref:
kubectl get cm $(kubectl get $RUN -n renovate -o jsonpath='{.status.shardConfigMapRef.name}') -n renovate -o yaml

# Owned worker Job
kubectl get job $(kubectl get $RUN -n renovate -o jsonpath='{.status.workerJobRef.name}') -n renovate

# Worker pod logs (one pod per shard index)
kubectl logs -n renovate -l job-name=$(kubectl get $RUN -n renovate -o jsonpath='{.status.workerJobRef.name}') --tail=200 --all-containers
```

## Authoring a Run by hand?

Don't. The CRD allows it (no admission rejection), but a hand-authored Run:

- Has no parent Scan to GC it via history limits — it sticks around forever
  unless you delete it.
- Has no `ownerReferences` set, so cascade-deletion via the Scan won't apply.
- Is missing the credential mirroring side effect (the Run controller's
  reconcile creates the mirror Secret, but only when it observes a fresh Run
  with the right snapshots — anomalous specs may misbehave).

If you want a one-off Renovate run, see
[RenovateScan §"Forcing a one-off Run"](renovate-scan.md#forcing-a-one-off-run).

## GC

Terminal Runs are kept per the parent Scan's `successfulRunsHistoryLimit` and
`failedRunsHistoryLimit`. Defaults: 3 successful, 1 failed.

Active (non-terminal) Runs are never GC'd by the operator. If a Run gets stuck
(controller paused, manual edits broke the state machine), delete it explicitly:

```bash
kubectl delete rrun <name> -n <ns>
```

This cascades to the owned shard ConfigMap and worker Job.

## Common signals

### Phase stays `Pending` forever

The Run controller hasn't observed it. Either the operator pod isn't running, or
its watches haven't established. Check operator logs.

### Phase stuck on `Discovering`

Discovery is hitting the platform API. Look for rate-limit warnings in operator
logs scoped to this Run:

```bash
kubectl -n renovate-system logs deploy/renovate-operator-controller-manager \
  | grep -E '"run":"<run-name>"|rate.?limit'
```

Workarounds: lower `discovery.filter` cardinality, reduce schedule frequency, or
split a single broad Scan into multiple narrow ones.

### Phase stuck on `Running`

Worker pods are executing. Check the Job's pod state:

```bash
kubectl get pods -n <ns> -l job-name=<run>-worker
kubectl logs -n <ns> -l job-name=<run>-worker --tail=200 --all-containers
```

A Renovate run typically completes in minutes. Hours-long Runs usually mean one
shard is stuck on a single repo (slow lockfile, hung HTTP call). The
`RenovateOperatorRunStuck` alert fires after 2h.

### Phase = `Failed`

Inspect the conditions and the worker Job state:

```bash
kubectl get rrun <name> -n <ns> -o jsonpath='{.status.conditions}' | yq -P
kubectl describe job <run>-worker -n <ns>
```

Common terminal failures:

| Signature                                                               | Cause                                                         | Fix                                                                                                     |
| ----------------------------------------------------------------------- | ------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------- |
| `failedShards > 0` and `Failed` condition reason `BackoffLimitExceeded` | Renovate CLI errored on shards beyond the index retry budget. | Check worker pod logs; common causes are bad presets, network egress blocks, package-manager auth gaps. |
| `Failed` condition reason `DiscoveryError`                              | Platform API returned a non-retryable error.                  | Inspect operator logs; verify Platform `Ready=True` _now_, then re-run via the next schedule fire.      |
| `Failed` condition reason `CredentialsError`                            | Mirror Secret creation failed.                                | Check operator namespace RBAC, especially `secrets:create` in the Scan's namespace.                     |

## See also

- [RenovateScan](renovate-scan.md) — the parent that creates a Run.
- [RenovatePlatform](renovate-platform.md) — credential resource snapshotted
  into the Run.
- [Installation §"`RenovateRun` stuck `Discovering`"](installation.md#renovaterun-stuck-discovering)
  for the full troubleshooting matrix.
