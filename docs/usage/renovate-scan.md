# RenovateScan

Namespaced cron-like resource that schedules `RenovateRun`s against a
`RenovatePlatform`. CronJob-shaped on purpose — `schedule`, `timeZone`,
`suspend`, `concurrencyPolicy`, `successfulRunsHistoryLimit`, and
`failedRunsHistoryLimit` mean what they mean on `batch/v1.CronJob`.

| Property    | Value                                                                                                            |
| ----------- | ---------------------------------------------------------------------------------------------------------------- |
| API group   | `renovate.fartlab.dev`                                                                                           |
| Kind        | `RenovateScan`                                                                                                   |
| Scope       | Namespaced                                                                                                       |
| Short names | `rscan`                                                                                                          |
| Sample      | [`config/samples/renovate_v1alpha1_renovatescan.yaml`](../../config/samples/renovate_v1alpha1_renovatescan.yaml) |

> **Note:** the `rs` short name is _not_ registered — it collides with the
> built-in `replicasets` short name. Use `rscan`.

## Minimal manifest

```yaml
apiVersion: renovate.fartlab.dev/v1alpha1
kind: RenovateScan
metadata:
  name: nightly
  namespace: renovate
spec:
  platformRef:
    name: github # cluster-scoped RenovatePlatform
  schedule: "0 2 * * *"
  timeZone: America/New_York
```

That's enough for an end-to-end Run on every fire-time, with all defaults:
worker bounds 1–10, `reposPerWorker=50`, `autodiscover=true`,
`requireConfig=true`, `concurrencyPolicy=Forbid`, history limits 3 successful /
1 failed.

## Scheduling

| Field                    | Default  | Notes                                                                                                                                                                                                                                  |
| ------------------------ | -------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `spec.schedule`          | required | 5-field cron (no seconds). Parsed by [`robfig/cron/v3`](https://pkg.go.dev/github.com/robfig/cron/v3). Validate with [crontab.guru](https://crontab.guru/).                                                                            |
| `spec.timeZone`          | `UTC`    | IANA zone. The cron expression is evaluated in this zone.                                                                                                                                                                              |
| `spec.suspend`           | `false`  | Pause without deleting. The Scan keeps history.                                                                                                                                                                                        |
| `spec.concurrencyPolicy` | `Forbid` | `Allow` (concurrent Runs OK), `Forbid` (skip if a non-terminal Run exists), `Replace` (accepted for CronJob compatibility, behaves as `Forbid` in v0.1.0 — see [ADR-0004](../adr/0004-use-conditions-and-run-children-for-status.md)). |

Rule of thumb: nightly cadence + `Forbid` is the right default. A weekly cadence
is fine for hobbyist orgs (lower PR firehose); switch to daily once you've tuned
the noise.

## Workers (sharding)

```yaml
spec:
  workers:
    minWorkers: 1
    maxWorkers: 10
    reposPerWorker: 50
    backoffLimitPerIndex: 2
```

The controller computes:

```
actualWorkers = clamp(ceil(discoveredRepos / reposPerWorker), minWorkers, maxWorkers)
```

The Run's worker `Job` runs in `Indexed` completion mode with that many parallel
pods. Each shard gets ~`reposPerWorker` repos to process. A shard that fails
retries up to `backoffLimitPerIndex` times before the parent Job marks that
index as failed.

Sizing guidance:

| Repos    | Sensible bounds                                                                         |
| -------- | --------------------------------------------------------------------------------------- |
| < 10     | `min=1 max=1 reposPerWorker=50` (one worker is plenty)                                  |
| 10–200   | `min=1 max=5 reposPerWorker=50` (default-ish, room to scale)                            |
| 200–1000 | `min=2 max=10 reposPerWorker=50`                                                        |
| 1000+    | `min=4 max=20 reposPerWorker=100` (raise reposPerWorker to keep shard count manageable) |

**`reposPerWorker` is a soft target**, not a hard cap. The operator gzips the
shard ConfigMap when serialized JSON exceeds 900 KiB, so very large repo lists
work but slow the worker pod's startup.

## Discovery

```yaml
spec:
  discovery:
    autodiscover: true
    requireConfig: true
    filter:
      - donaldgifford/*
    topics:
      - renovate
    skipForks: true
    skipArchived: true
```

| Field           | Default | Effect                                                                                                                                                                 |
| --------------- | ------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `autodiscover`  | `true`  | Operator enumerates repos via the platform API. Set to `false` and the Scan's own `renovateConfigOverrides.repositories` must supply the list.                         |
| `requireConfig` | `true`  | Drops repos without a Renovate config in their default branch. **Strongly recommended** for org-wide scans — without it, every undecorated repo gets an onboarding PR. |
| `filter`        | `[]`    | Renovate-style glob patterns (`owner/*`, `owner/prefix-*`). Empty = no filter (every repo the credential can see is eligible).                                         |
| `topics`        | `[]`    | GitHub only — restricts to repos with at least one of the listed topics. Ignored on Forgejo.                                                                           |
| `skipForks`     | `true`  | Drops forks.                                                                                                                                                           |
| `skipArchived`  | `true`  | Drops archived repos.                                                                                                                                                  |

### Common discovery shapes

**Single repo, explicit filter (smallest blast radius):**

```yaml
discovery:
  autodiscover: false
  requireConfig: true
  filter:
    - donaldgifford/server-price-tracker
```

**Org-wide with renovate-tagged repos only:**

```yaml
discovery:
  autodiscover: true
  requireConfig: false # opt-in by topic, not config presence
  topics: [renovate]
```

**Everything an installation can see (org-wide, opt-in via config):**

```yaml
discovery:
  autodiscover: true
  requireConfig: true
  skipForks: true
  skipArchived: true
```

## Config layering

`Platform.spec.runnerConfig` and `Scan.spec.renovateConfigOverrides` are both
opaque JSON merged into one `RENOVATE_CONFIG` env var:

1. Start with `Platform.spec.runnerConfig` (e.g., `binarySource`, `dryRun`,
   `hostRules`).
2. Field-by-field merge with `Scan.spec.renovateConfigOverrides` (Scan wins on
   collision).
3. Result is what each worker pod sees.

Use Platform-level config for cross-Scan settings (host auth, runner-level
flags). Use Scan-level overrides for per-Scan policy (PR labels, automerge
strategy, schedule windows).

```yaml
# Platform: cross-Scan defaults
spec:
  runnerConfig:
    binarySource: install
    onboarding: false
    requireConfig: required
---
# Scan A: production repos, no automerge
spec:
  renovateConfigOverrides:
    labels: [dependencies]
    automerge: false
---
# Scan B: dev repos, automerge minor + patch
spec:
  renovateConfigOverrides:
    labels: [dependencies, dev]
    packageRules:
      - matchUpdateTypes: [minor, patch]
        automerge: true
```

## Status

```bash
kubectl get rscan <name> -n <ns> -o yaml | yq .status
```

| Field                   | Meaning                                                                                         |
| ----------------------- | ----------------------------------------------------------------------------------------------- |
| `conditions`            | `Ready` (overall scheduling readiness), `Scheduled` (next-fire-time computed and a Run queued). |
| `lastRunTime`           | Most recent fire time the controller observed (regardless of outcome).                          |
| `lastSuccessfulRunTime` | Most recent fire time whose Run reached `Succeeded`.                                            |
| `nextRunTime`           | Next scheduled fire time, computed from `spec.schedule` in `spec.timeZone`.                     |
| `lastRunRef`            | `ObjectReference` to the most recent owned Run.                                                 |
| `activeRuns`            | Non-terminal owned Runs (used to evaluate `concurrencyPolicy`).                                 |
| `observedGeneration`    | `metadata.generation` last reconciled.                                                          |

Printer columns:

```
$ kubectl get rscan -A
NAMESPACE   NAME      PLATFORM   SCHEDULE      LAST RUN   NEXT RUN   READY   AGE
renovate    nightly   github     0 2 * * *     22h        2h         True    14d
```

`Ready=False` reasons:

| Reason             | Fix                                                             |
| ------------------ | --------------------------------------------------------------- |
| `InvalidSchedule`  | Cron expression doesn't parse — must be 5-field.                |
| `PlatformNotReady` | Referenced Platform isn't `Ready=True`. Fix the Platform first. |
| `Suspended`        | `spec.suspend: true`.                                           |

## History limits

```yaml
spec:
  successfulRunsHistoryLimit: 3 # keep the 3 newest Succeeded Runs
  failedRunsHistoryLimit: 1 # keep the 1 newest Failed Run
```

Older terminal Runs are garbage-collected. Active Runs are never GC'd.

## Common patterns

### Suspending a Scan temporarily

```bash
kubectl patch rscan nightly -n renovate --type merge -p '{"spec":{"suspend":true}}'
# ... do whatever ...
kubectl patch rscan nightly -n renovate --type merge -p '{"spec":{"suspend":false}}'
```

`Ready` flips to `Suspended`/`Scheduled` accordingly. Existing Runs continue to
completion.

### Forcing a one-off Run

The operator doesn't expose a "run now" trigger in v0.1.0 (deliberate scope call
— see [DESIGN-0001 §3.5](../design/0001-renovate-operator-v0-1-0.md)).
Workarounds:

- Edit `spec.schedule` to fire in the next minute, observe the Run, revert.
- Apply a temporary Scan with a single-fire-time schedule.

### Renaming a Scan

CRDs are immutable on `metadata.name`. Delete and recreate; in-flight Runs under
the old name are owned by the old Scan and will be GC'd by deleting the parent.

## See also

- [Installation](installation.md) — operator install + chart config.
- [RenovatePlatform](renovate-platform.md) — the cluster-scoped credential
  resource a Scan references.
- [RenovateRun](renovate-run.md) — the ephemeral child resource a Scan creates.
