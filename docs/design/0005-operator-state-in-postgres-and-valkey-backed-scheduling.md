---
id: DESIGN-0005
title: "Operator state in Postgres, tuned Job dispatch, and Valkey worker cache"
status: Draft
author: Donald Gifford
created: 2026-08-01
---
<!-- markdownlint-disable-file MD025 MD041 -->

# DESIGN 0005: Operator state in Postgres, tuned Job dispatch, and Valkey worker cache

**Status:** Draft
**Author:** Donald Gifford
**Date:** 2026-08-01 (revised 2026-08-02)

> **Revision note (2026-08-02).** The first draft scoped Valkey as a
> work-queue scheduling substrate. Review concluded that Kubernetes
> Indexed Jobs with `completions > parallelism` already provide
> dynamic batch assignment — the Job controller hands the next
> unstarted index to whichever slot frees up — so the queue is
> **deferred** behind explicit scale triggers (§Future: Valkey work
> queue). Valkey enters v0.2.0 only as **Renovate's package cache**
> (`RENOVATE_REDIS_URL`), which is the bigger real-world scaling
> lever anyway: repo scan time is dominated by cold dependency
> lookups, not dispatch shape.

<!--toc:start-->
- [Overview](#overview)
- [Goals and Non-Goals](#goals-and-non-goals)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Background](#background)
- [Detailed Design](#detailed-design)
  - [Division of truth](#division-of-truth)
  - [Postgres: the state store](#postgres-the-state-store)
  - [Tuned Job dispatch](#tuned-job-dispatch)
  - [Worker shim and outcome reporting](#worker-shim-and-outcome-reporting)
  - [Valkey as Renovate package cache](#valkey-as-renovate-package-cache)
  - [Ownership enrichment](#ownership-enrichment)
  - [Failure semantics](#failure-semantics)
- [API / Interface Changes](#api--interface-changes)
- [Data Model](#data-model)
- [Testing Strategy](#testing-strategy)
- [Migration / Rollout Plan](#migration--rollout-plan)
- [Release sequencing (supersedes DESIGN-0002 §Decision)](#release-sequencing-supersedes-design-0002-decision)
- [Future: Valkey work queue](#future-valkey-work-queue)
- [Open Questions](#open-questions)
- [References](#references)
<!--toc:end-->

## Overview

The operator gains an optional state store: **Postgres** as the
system of record for run/repo/PR history, written directly by the
Run reconciler (no event pipeline, no consumer). Downstream, a thin
Connect API embedded in the manager (re-drafted
[DESIGN-0004](0004-operator-http-api-consumer-datastore-ui-and-webhook-receiver.md))
is the *only* reader the UI ever talks to — the UI never touches
Postgres or Valkey.

Scaling is addressed by **tuning the existing Indexed-Job model**,
not by new infrastructure: small batches with
`completions > parallelism` (Kubernetes-native work assignment),
`backoffLimitPerIndex` for per-batch retry, a token-refresh loop
resolving [INV-0003](../investigation/0003-renovate-v43-github-app-auth-requires-autodiscover-not.md),
and an optional **Valkey-backed Renovate package cache** so workers
stop paying cold-cache dependency lookups on every pod. A Valkey
work *queue* is documented as a future escalation path with
explicit triggers, not built now.

Everything here is opt-in. With `state` and `workerCache` disabled,
the operator runs exactly as v0.1.x does today — no database, no
cache, no new dependencies.

## Goals and Non-Goals

### Goals

- Operator owns the Postgres schema and migrations (embedded
  `golang-migrate`, applied at manager startup with retry +
  readiness gate), gated behind `state.enabled`.
- Operator writes state at Run lifecycle transitions: Run
  started/finished, per-repo outcomes, PRs opened/updated/closed,
  vulnerabilities, dependency updates, ownership.
- **Batched dispatch**: `completions = ceil(repos/batchSize)`,
  `parallelism = clamp(...)` (existing formula). One code path —
  `batchSize` unset defaults to the legacy per-worker shard size,
  so current installs see identical layout; setting it small
  unlocks Kubernetes-native work-stealing at batch granularity.
- Per-batch retry via `backoffLimitPerIndex` + pod failure policy
  (no whole-shard re-scan).
- **Token-refresh loop**: the Run reconciler re-mints the GitHub
  App installation token into the per-Run mirrored Secret every
  ~40 minutes while the Run is active. Batch pods read env at
  start, so pods launched after a refresh get a fresh token;
  batches are sized to keep any single pod well under the ~1h TTL.
  Resolves INV-0003 without new components. (Forgejo tokens are
  static — no-op there.)
- Worker **shim** wrapping the Renovate invocation to extract
  per-repo outcomes and report them to a small manager endpoint —
  this is what populates Postgres with per-repo data.
- **Valkey as Renovate's package cache**: operator injects
  `RENOVATE_REDIS_URL` / `RENOVATE_REDIS_PREFIX` into worker env
  when enabled. Config-only from Renovate's perspective; the
  operator itself needs **no Redis client dependency**.
- Discovery-time ownership enrichment (custom properties /
  `catalog-info.yaml`, moved here from DESIGN-0003) lands in
  Postgres rows so the UI can filter by owner.
- Graceful degradation: Postgres or cache unavailability degrades
  features, never corrupts CRD reconciliation.

### Non-Goals

- **Valkey as a work queue.** Deferred behind explicit triggers —
  see §Future: Valkey work queue. Nothing in v0.2.0 forecloses it;
  the shim protocol is the only contract that would move.
- Serving queries. That is the Connect API
  ([DESIGN-0004](0004-operator-http-api-consumer-datastore-ui-and-webhook-receiver.md)).
- Outbound integration events. That is the re-scoped EventSink
  ([DESIGN-0003](0003-eventsink-for-renovate-operator-v020.md)).
- Moving Scan cron scheduling anywhere. robfig/cron + requeue in
  the Scan controller is CRD-native and stays.
- Multi-cluster state aggregation.
- Chart-bundled Postgres or Valkey. User-provided, connection
  details via Secret — same posture as everything else.

## Background

DESIGN-0002 (sealed 2026-05-31) scoped v0.2.0 = EventSink and
v0.3.0 = internal REST API + consumer + datastore + UI + webhook
receiver. Three rounds of rethinking restructured that:

1. **The operator writes its own state** (2026-08-01). The
   EventSink → consumer → Postgres pipeline existed only because
   the operator owned no state. Once the operator is the writer,
   the consumer — and the in-memory sink + Fanout machinery built
   to feed it — is dead weight. DESIGN-0003 narrows to outbound
   integration events only.
2. **The thin Go API stays, and it is the boundary** (2026-08-01).
   The UI must never talk to Postgres or Valkey. Transport is
   ConnectRPC (gRPC-compatible), embedded in the manager binary.
   Rationale in the re-drafted DESIGN-0004: the Postgres schema
   stays private to Go, cluster credentials stay off the
   internet-facing app, and the proto contract (enforced by
   `buf breaking`) replaces both the hand-written OpenAPI spec and
   shared-database coupling.
3. **The work queue is premature; the cache is not** (2026-08-02).
   The first draft of this doc assumed static shards were the
   Kubernetes-native ceiling. They aren't: an Indexed Job with
   `completions > parallelism` dynamically assigns the next index
   to the next free slot, which at small batch sizes delivers
   work-stealing, bounded tail latency, and (with
   `backoffLimitPerIndex`) per-batch retry — no queue, no new
   infrastructure, no dual-mode dispatch code. Meanwhile the
   actually-measured cost at scale is Renovate's cold package
   cache in every fresh pod, which `RENOVATE_REDIS_URL` fixes with
   configuration alone.

## Detailed Design

### Division of truth

| Store | Owns | Lifetime |
|---|---|---|
| **CRDs** | Control plane: desired state (`spec`), live status (`[]metav1.Condition`, Run `phase`). Unchanged from v0.1.x. | Kubernetes |
| **Postgres** | History: what happened, per repo, per Run. The UI's query surface. | `eventsRetentionDays` (default 90) |
| **Valkey** | Renovate's package cache. Disposable — losing it costs cold-cache latency on the next Run, nothing else. Not a source of truth. | Cache-managed |

The operator is the sole writer of CRDs and Postgres. Workers talk
to Valkey directly (it's Renovate's cache, not the operator's).
Kubernetes remains authoritative for control: if Postgres and the
CRDs disagree about a Run's phase, the CRD wins and the row is
repaired on the next write.

### Postgres: the state store

- **Connection:** DSN from a Secret (`state.postgres.dsnSecretRef`),
  `pgxpool`, TLS via `sslmode` passthrough.
- **Migrations:** embedded in the manager image via
  `golang-migrate/migrate`, applied on startup, transaction-wrapped,
  retried with backoff; readiness gate fails if migration cannot
  complete.
- **Write sites** in the Run reconciler:
  - Run created → insert `runs` row (`phase=pending`).
  - Discovery complete → update repo count, insert `repo_results`
    stubs with ownership fields.
  - Per-repo outcome received (shim report) → update the
    `repo_results` row, insert `prs` / `vulnerabilities` /
    `dependency_updates` children.
  - Run terminal transition → finalize `runs` row
    (`phase`, `finished_at`).
- **Retention:** nightly goroutine in the manager deletes `runs`
  older than `eventsRetentionDays`; children cascade.

### Tuned Job dispatch

One formula, one code path — no dispatch modes:

```
completions  = ceil(len(repos) / batchSize)
parallelism  = clamp(ceil(len(repos) / reposPerWorker), minWorkers, maxWorkers)
```

- New additive CRD field `RenovateScan.spec.workers.batchSize`
  (optional). **Unset → batchSize = the legacy per-worker shard
  size** (`ceil(repos/parallelism)`), reproducing today's layout
  exactly. Set small (2–5) → `completions > parallelism` and the
  Job controller assigns the next unstarted index to whichever
  worker slot frees up: batch-granular work-stealing, tail latency
  bounded by one batch.
- Shard ConfigMap stays the per-index assignment mechanism (index →
  repo batch + frozen config), keyed by completion index as today.
  If entry count ever pushes the ~1MiB ConfigMap limit, split
  across multiple ConfigMaps — distant at current scale.
- **Per-batch retry:** `backoffLimitPerIndex` (beta since K8s 1.29,
  GA 1.33) + a pod failure policy so a failed batch retries alone
  and a poisoned batch fails only its index, not the Run.
  Job-level `backoffLimit` becomes the aggregate cap.
- Each pod runs one Renovate invocation over its batch's repo list
  (Renovate processes them serially in-process, as today) — pod
  startup and Renovate init are amortized per batch, not per repo.
- Progress: succeeded-index count from Job status drives Run
  status; per-repo granularity comes from shim reports when state
  is enabled.

### Worker shim and outcome reporting

The UI needs per-repo outcomes (PRs, vulns, dep updates) and exit
codes don't carry them, so the worker image gains a small Go
**shim** entrypoint (`cmd/worker-shim/`, thin wrapper image
`FROM renovate/renovate`):

```
read batch assignment (shard ConfigMap, as today)
exec renovate over the batch
parse per-repo outcomes from Renovate's output
POST outcomes to the manager's results endpoint (retry w/ backoff)
exit with renovate's status
```

- **Results endpoint:** a small HTTP listener on the manager
  (`--results-bind-address`, ClusterIP service), one POST per pod
  carrying its batch's outcomes. The manager validates and writes
  Postgres. Auth: lean toward a per-Run bearer token generated at
  dispatch and delivered via the mirrored Secret (see Open
  Questions). This replaces the first draft's Valkey results
  stream and the older termination-log idea (4KiB limit is too
  tight for multi-repo batches).
- The outcome JSON shape is a private contract between shim and
  manager, versioned together (same repo, same release).
- When `state.enabled=false` the shim skips reporting and is a
  passthrough exec — zero behavior change from v0.1.x.

### Valkey as Renovate package cache

Renovate natively supports a Redis-protocol package cache; Valkey
(a Redis 7.2.4 fork — streams and all data types intact, widely
deployed, already operated here for repo-guardian) serves it
unchanged.

- When `workerCache.enabled`, the jobspec builder injects:
  - `RENOVATE_REDIS_URL` — from `workerCache.url`, or from a
    Secret (`workerCache.urlSecretRef`) when the URL embeds auth;
    the Secret is mirrored into the Scan namespace per Run,
    same pattern as credentials. `rediss://` for TLS.
  - `RENOVATE_REDIS_PREFIX` — from `workerCache.prefix` (default
    `renovate`), namespacing keys on a shared instance.
- Shared across all workers, Scans, and Platforms: the cache is
  keyed by package metadata, so cross-repo reuse is the whole
  point — the second repo asking about the same dependency hits
  warm cache.
- The operator itself never dials Valkey — it only wires env. No
  go-redis dependency lands in v0.2.0.

### Ownership enrichment

Moved here from DESIGN-0003 (it feeds Postgres first; events just
carry it). Mechanics unchanged from the original design:
`internal/enrichment/` with `customprops` (GitHub custom
properties) and `catalog` (`catalog-info.yaml`) providers behind a
`Chain`, running at discovery time, cached on the Run snapshot.
Results land in `repo_results.owner_field/system_field/
lifecycle_field/ownership_src`, which is what the Connect API's
owner filter and the UI's per-user view key on.

### Failure semantics

| Failure | Behavior |
|---|---|
| Postgres down at startup (`state.enabled`) | Readiness gate fails (migrations can't run). Deliberate: a state-enabled install that can't reach its state store is not ready. |
| Postgres down mid-flight | State writes retry with bounded backoff, then drop with `renovate_state_write_failures_total{table}` + a `StateRecorded=False` Run condition. CRD reconciliation continues — history gets holes, control does not. |
| Batch pod crash | `backoffLimitPerIndex` retries that index alone. At-least-once per batch; Renovate re-running converges on the same PRs. |
| Shim can't reach results endpoint | Retries with backoff for the pod's lifetime, then exits with Renovate's status anyway — Job completion is never held hostage by reporting. Affected `repo_results` rows stay `pending`; Run gets `StateRecorded=False`. |
| Valkey cache down | Workers degrade to cold-cache behavior *if* Renovate falls back gracefully — verify (Open Questions). Worst case: disable `workerCache` to recover; the cache is never load-bearing. |
| Token refresh fails | Logged + Run condition; pods started after TTL expiry 401 and per-index retry surfaces it. Same blast radius as today, now bounded by refresh cadence. |

## API / Interface Changes

**CRD: one additive field** — `RenovateScan.spec.workers.batchSize`
(optional int). Run.status gains conditions (`StateRecorded`) —
open set, not a schema change.

**Helm values (all default-off / behavior-preserving):**

```yaml
state:
  enabled: false                  # master switch for PG state
  postgres:
    dsnSecretRef: { name: "", key: "dsn" }   # required when enabled
    maxOpenConns: 25
  eventsRetentionDays: 90

workerCache:
  enabled: false                  # RENOVATE_REDIS_URL injection
  url: ""                         # redis://valkey.shared.svc:6379/0 (no-auth)
  urlSecretRef: { name: "", key: "" }  # overrides url when URL embeds auth
  prefix: "renovate"              # RENOVATE_REDIS_PREFIX

defaultScan:
  workers:
    batchSize: 5                  # passthrough to Scan spec; unset = legacy layout
```

Template guards: `state.enabled && dsnSecretRef.name == ""` →
render error; `workerCache.enabled && url == "" &&
urlSecretRef.name == ""` → render error.

**New binary:** `cmd/worker-shim/` (worker image entrypoint).
No new Deployments — everything operator-side lives in the manager.

## Data Model

State-shaped (not event-shaped — the operator records facts, it
doesn't replay an event log):

```sql
CREATE TABLE runs (
  id            BIGSERIAL PRIMARY KEY,
  run_uid       TEXT NOT NULL UNIQUE,          -- CRD UID, repair key
  namespace     TEXT NOT NULL,
  name          TEXT NOT NULL,
  scan_name     TEXT NOT NULL,
  platform_name TEXT NOT NULL,
  phase         TEXT NOT NULL,                 -- mirrors CRD phase enum
  repo_count    INT  NOT NULL DEFAULT 0,
  started_at    TIMESTAMPTZ NOT NULL,
  finished_at   TIMESTAMPTZ
);
CREATE INDEX runs_scan_time_idx ON runs (scan_name, started_at DESC);

CREATE TABLE repo_results (
  id              BIGSERIAL PRIMARY KEY,
  run_id          BIGINT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
  repo_owner      TEXT NOT NULL,
  repo_name       TEXT NOT NULL,
  platform_type   TEXT NOT NULL,               -- github|forgejo
  outcome         TEXT NOT NULL DEFAULT 'pending', -- pending|succeeded|failed|skipped
  owner_field     TEXT NOT NULL DEFAULT '',
  system_field    TEXT NOT NULL DEFAULT '',
  lifecycle_field TEXT NOT NULL DEFAULT '',
  ownership_src   TEXT NOT NULL DEFAULT 'unset',
  completed_at    TIMESTAMPTZ
);
CREATE INDEX repo_results_repo_idx  ON repo_results (repo_owner, repo_name, completed_at DESC);
CREATE INDEX repo_results_owner_idx ON repo_results (owner_field) WHERE owner_field <> '';

CREATE TABLE prs (
  id            BIGSERIAL PRIMARY KEY,
  repo_result_id BIGINT NOT NULL REFERENCES repo_results(id) ON DELETE CASCADE,
  action        TEXT NOT NULL,                 -- opened|updated|closed
  pr_number     INT NOT NULL,
  url           TEXT NOT NULL,
  labels        TEXT[] NOT NULL DEFAULT '{}',
  automerge     BOOLEAN NOT NULL,
  observed_at   TIMESTAMPTZ NOT NULL
);

CREATE TABLE vulnerabilities (
  id             BIGSERIAL PRIMARY KEY,
  repo_result_id BIGINT NOT NULL REFERENCES repo_results(id) ON DELETE CASCADE,
  advisory_id    TEXT NOT NULL,
  severity       TEXT NOT NULL,
  package        TEXT NOT NULL,
  observed_at    TIMESTAMPTZ NOT NULL
);

CREATE TABLE dependency_updates (
  id             BIGSERIAL PRIMARY KEY,
  repo_result_id BIGINT NOT NULL REFERENCES repo_results(id) ON DELETE CASCADE,
  package        TEXT NOT NULL,
  from_version   TEXT NOT NULL,
  to_version     TEXT NOT NULL,
  manager        TEXT NOT NULL
);
```

The schema is **private to this repo**. Consumers query it only
through the Connect API (DESIGN-0004); `buf breaking` on the proto
is the compatibility contract, so these tables can be refactored
freely.

## Testing Strategy

- **Unit:** jobspec batch math (completions/parallelism across
  batchSize set/unset, boundary counts); token-refresh scheduling
  (fake clock — `internal/clock` exists for this); shim
  lease/exec/report loop against a fake `renovate` script + an
  `httptest` results endpoint (happy, endpoint-down retry,
  malformed output); results-endpoint handler (auth, validation,
  write path) with `pgxmock`; state-writer package (every
  lifecycle transition, retry/backoff, drop counter).
- **Controller (envtest):** Run end-to-end with a stub shim
  posting results; assert `repo_results` rows and Run conditions;
  token refresh rotates the mirrored Secret while a Run is active.
- **e2e (kind):** Postgres (single-pod) + Valkey deployed; real
  worker image with shim; `batchSize=2` Run over homelab-style
  fixtures produces correct `repo_results`/`prs` rows; second Run
  observably faster with warm cache (soft assertion, logged not
  gated).
- **Migration tests:** `golang-migrate` up/down against a
  throwaway container in CI.
- Coverage gate ≥80% per package per IMPL-0001.

## Migration / Rollout Plan

Fully additive. Upgrade from v0.1.x:

1. `helm upgrade ... --version 0.2.0` — all new values default
   off/unset; identical dispatch layout, zero behavior change.
2. Tune dispatch: set `workers.batchSize: 5` (per Scan or via
   `defaultScan`). Purely a Job-layout change; observable as more,
   shorter pods.
3. Opt in to state: provision Postgres, create DSN Secret,
   `--set state.enabled=true ...`. Runs now record history.
4. Opt in to the cache: point `workerCache.url` at a Valkey;
   workers warm it on the next Run.
5. Rollback at any step is un-setting the values.

## Release sequencing (supersedes DESIGN-0002 §Decision)

DESIGN-0002 is sealed; per its own rule the re-scope is recorded
here:

| Release | Contents | Design |
|---|---|---|
| **v0.2.0** | Postgres state + worker shim/outcome reporting + tuned Job dispatch (batching, per-index retry, token refresh — resolves DESIGN-0002 deferred item 3 / INV-0003) + Valkey worker cache | this doc |
| **v0.3.0** | Embedded Connect API + Bun BFF/UI + webhook receiver | [DESIGN-0004](0004-operator-http-api-consumer-datastore-ui-and-webhook-receiver.md) (re-drafted 2026-08-01) |
| **Decoupled** | EventSink (outbound integration events over Valkey/Redis streams) — no longer a precondition for anything; lands opportunistically in v0.2.x or v0.3.x | [DESIGN-0003](0003-eventsink-for-renovate-operator-v020.md) (re-scoped 2026-08-01) |

DESIGN-0002's remaining deferred items (credential-source
abstraction, `Replace` semantics, per-Scan RBAC, smaller items) are
unchanged.

## Future: Valkey work queue

Not built in v0.2.0. If tuned dispatch stops being enough, the
queue design from this doc's first draft (per-Run Valkey stream,
consumer groups, `XAUTOCLAIM` lease recovery, shim leasing repos
instead of reading a ConfigMap) is the escalation path. Valkey
remains the right substrate for it: streams are fully supported
(Redis 7.2.4 fork), we already operate it, the cache instance can
double up, and KEDA's `redis-streams` scaler covers queue-depth-
driven worker scaling. Alternatives were considered and passed on:
NATS JetStream (credible semantics, but a second new infra type
with no existing ops familiarity), Kafka (partitions reintroduce
static sharding), CRs/etcd as a queue (per-repo lease+ack write
amplification against the API server — an anti-pattern).

**Triggers — build it when one of these is real, not before:**

- Repo counts in the thousands, where per-batch pod churn is a
  measured (not assumed) bottleneck on Run wall-clock.
- A demonstrated need to add workers mid-Run (batched Jobs freeze
  `parallelism` at dispatch).
- Batch-granular retry proving too coarse in practice (a poisoned
  repo repeatedly failing its whole batch).

The shim is the only contract that moves: its input source changes
from "batch via ConfigMap" to "lease via stream." The results
endpoint, Postgres schema, and Connect API are untouched.

## Open Questions

- **`batchSize` default and floor.** 5 is a guess; validate tail
  latency vs. pod-churn overhead on the homelab (with and without
  the warm cache, which shifts the trade-off).
- **Results-endpoint auth.** Per-Run bearer token in the mirrored
  Secret (lean) vs. ServiceAccount token + TokenReview. Decide
  before implementation.
- **Renovate behavior when the Redis cache is unreachable.** Does
  it fall back to filesystem cache or hard-fail at init? Determines
  whether the cache row in §Failure semantics needs a warning in
  the usage docs. Verify on the homelab before GA.
- **Shim outcome extraction.** What the shim parses from
  Renovate's output — the v43+ reporting surface
  (`RENOVATE_REPORT_TYPE`-style file output) vs. log scraping.
  First implementation question.
- **`repo_results` uniqueness.** Add `UNIQUE (run_id, repo_owner,
  repo_name)` with upsert semantics so a retried batch's duplicate
  report doesn't double-insert.

## References

- [DESIGN-0002](0002-renovate-operator-v020.md) — sealed scope doc
  whose release table this doc supersedes.
- [DESIGN-0003](0003-eventsink-for-renovate-operator-v020.md) —
  EventSink, re-scoped 2026-08-01 to outbound events only.
- [DESIGN-0004](0004-operator-http-api-consumer-datastore-ui-and-webhook-receiver.md) —
  Connect API + UI, re-drafted 2026-08-01; sole reader of this
  doc's Postgres schema.
- [DESIGN-0001 §Future architecture: state DB](0001-renovate-operator-v0-1-0.md) —
  the original thread pointing at a scheduler/Postgres direction.
- [INV-0003](../investigation/0003-renovate-v43-github-app-auth-requires-autodiscover-not.md) —
  token TTL; resolved here via refresh loop + small batches.
- [INV-0006](../investigation/0006-operationalizing-renovate-operator-at-scale-dashboard-risk.md) —
  operationalization question that started the v0.2.x planning.
- Renovate self-hosted config: `redisUrl` / `RENOVATE_REDIS_URL`,
  `redisPrefix` — Redis-protocol package cache.
- Kubernetes: Indexed Jobs (`completions`/`parallelism`),
  `backoffLimitPerIndex` (beta 1.29, GA 1.33), pod failure policy.
- repo-guardian — prior art for the PG + Valkey pattern; source of
  the deferred queue design.
