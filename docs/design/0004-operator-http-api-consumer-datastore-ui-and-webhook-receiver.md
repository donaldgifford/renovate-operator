---
id: DESIGN-0004
title: "Operator HTTP API, consumer, datastore, UI, and webhook receiver for renovate-operator v0.3.0"
status: Draft
author: Donald Gifford
created: 2026-05-31
---
<!-- markdownlint-disable-file MD025 MD041 -->

# DESIGN 0004: Operator HTTP API, consumer, datastore, UI, and webhook receiver for renovate-operator v0.3.0

**Status:** Draft
**Author:** Donald Gifford
**Date:** 2026-05-31

<!--toc:start-->
- [Overview](#overview)
- [Goals and Non-Goals](#goals-and-non-goals)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Background](#background)
- [Architecture overview](#architecture-overview)
- [Detailed Design](#detailed-design)
  - [Component 1: Internal HTTP API](#component-1-internal-http-api)
  - [Component 2: EventSink consumer](#component-2-eventsink-consumer)
  - [Component 3: Datastore](#component-3-datastore)
  - [Component 4: External UI / API router](#component-4-external-ui--api-router)
  - [Component 5: Webhook receiver](#component-5-webhook-receiver)
- [API / Interface Changes](#api--interface-changes)
- [Data Model](#data-model)
- [Testing Strategy](#testing-strategy)
- [Migration / Rollout Plan](#migration--rollout-plan)
- [Security Considerations](#security-considerations)
- [Open Questions](#open-questions)
- [References](#references)
<!--toc:end-->

## Overview

v0.3.0 ships the operator's customer-facing surface: humans see
what Renovate is doing across their repos, and external systems
can trigger Runs. Five components, all opt-in:

1. **Internal HTTP API** — Go service shipped with the operator
   chart, reads/writes CRDs, consumes EventSink, exposes OpenAPI.
2. **EventSink consumer** — reads the Redis Stream populated by
   the v0.2.0 EventSink and lands records in the datastore.
3. **Datastore** — Postgres. Persists events for queryable
   history.
4. **External UI / API router** — bun + hono + React. Lives in a
   separate repo. Handles auth, TLS, public exposure. Talks to
   the internal API via a generated OpenAPI client.
5. **Webhook receiver** — separate Deployment in the chart.
   Inbound platform `push` events trigger one-shot Runs.

The release theme: **the operator gains an interactive interface.**
The v0.2.0 EventSink is the data feed; v0.3.0 makes that data
useful and adds a symmetric inbound path (webhooks for systems,
HTTP API for humans).

## Goals and Non-Goals

### Goals

- Internal HTTP API as a separate binary in the operator chart,
  off by default. Reads CRDs (list Platforms/Scans/Runs), writes
  a small set of imperative operations (suspend Scan, force Run,
  delete Run), serves the OpenAPI spec.
- Postgres-backed datastore for event history. Schema migrated
  by the API service at startup.
- EventSink consumer running in the API binary (or a sidecar —
  see §Open Questions) reading a Redis Streams consumer group
  and writing normalized rows to Postgres.
- Webhook receiver as a separate binary + Deployment in the
  operator chart, off by default. Verifies signatures, creates
  one-shot Runs. Webhook-triggered Runs flow through EventSink
  identically to scheduled Runs.
- OpenAPI v3 spec generated from Go types; published at
  `/openapi.yaml` on the API service. Used to generate the
  external router's typed client.
- External UI lives in `<separate-repo>` or
  `web/`; this DESIGN reaches only as far as the contract (the
  OpenAPI spec). UI implementation is out of scope for this doc.
- All five components fully opt-in via chart values. A v0.2.x
  install on upgrade sees zero behavior change.

### Non-Goals

- **No auth in the internal API.** Auth is the external router's
  job. The internal API is reachable only from inside the cluster
  on a `ClusterIP` Service and trusts its caller. Network policy
  is the security boundary.
- **No multi-tenancy in the API or datastore.** Single
  operator-namespace install today; multi-tenant isolation is
  v0.4.x+ when a real tenant appears.
- **No UI shipped in this repo's chart.** The chart packages the
  API + consumer + webhook + Postgres connection only. UI is a
  separate deliverable in its own repo, with its own release
  cadence.
- **No replacement for `kubectl`.** The API exposes the small set
  of operations the UI needs; it is not a general K8s proxy.
- **No event replay from Postgres.** The API serves history;
  replaying events to re-trigger something is out of scope.
- **No write-path through the external router that bypasses the
  internal API.** Everything the router can do is something the
  internal API exposes.

## Background

DESIGN-0002 decided to bundle these five components into one
release because they share a theme — "external interactions with
the operator" — and because shipping any of them in isolation
produces something half-finished:

- Webhook receiver alone: just another way to make a Run happen;
  `kubectl` already does that.
- HTTP API alone: nothing on the other end to use it.
- Consumer alone: data lands in Postgres with no UI to surface
  it.
- UI alone: no API to drive it.

Together, they form a coherent feature: **"the operator now has
a UI."** All five are opt-in; an operator that doesn't enable any
of them runs identically to v0.2.0.

The v0.2.0 EventSink is a precondition for this release because
it provides the source of truth for the consumer. The architectural
flow:

```
RenovateRun → EventSink (Redis Streams) → Consumer → Postgres → Internal API → External Router → UI
                                                                                         ↑
                                                          Auth/OIDC, TLS, public exposure
```

And, in the reverse direction:

```
GitHub/Forgejo → Webhook Receiver → RenovateRun (one-shot) → ... → EventSink → Consumer → ...

UI → External Router → Internal API → CRD mutation (suspend, force, delete) → operator reconcile
```

## Architecture overview

```
                    ┌─────────────────────────────────────────────────────┐
                    │ External (DMZ / Ingress / behind OIDC reverse-proxy) │
                    └─────────────────────────────────────────────────────┘
                                            │
                                            │ HTTPS, OIDC-authenticated
                                            ▼
                    ┌─────────────────────────────────────────┐
                    │ External UI/API router (bun + hono)     │  separate repo
                    │  - serves React UI                       │  separate release
                    │  - auth (OIDC)                           │
                    │  - typed OpenAPI client → internal API   │
                    └─────────────────────────────────────────┘
                                            │
                                            │ HTTP, in-cluster
                                            │ (ClusterIP, NetworkPolicy)
                                            ▼
┌──────────────────────────────────────────────────────────────────────┐
│ renovate-operator chart (in-cluster)                                  │
│                                                                       │
│  ┌──────────────────────────┐    ┌──────────────────────────────┐   │
│  │ Internal HTTP API         │    │ Webhook receiver              │   │
│  │  - reads/writes CRDs      │    │  - verifies GH/Forgejo sigs   │   │
│  │  - reads Postgres         │    │  - creates one-shot Runs      │   │
│  │  - serves OpenAPI         │    └──────────────────────────────┘   │
│  └──────────────────────────┘            │                            │
│        │            │                    │                            │
│        │ K8s API    │ SQL                ▼                            │
│        ▼            ▼            ┌──────────────────────┐             │
│  ┌──────────┐  ┌──────────┐      │ RenovateRun (CRD)    │             │
│  │ K8s API  │  │ Postgres │      └──────────────────────┘             │
│  └──────────┘  └──────────┘                │                          │
│                     ▲                       │                          │
│                     │                       ▼                          │
│                     │           ┌─────────────────────┐                │
│                     │ writes    │ Operator reconciler │                │
│                     │           └─────────────────────┘                │
│                     │                       │                          │
│              ┌──────────────┐               │ (v0.2.0 path)            │
│              │ Consumer     │               ▼                          │
│              │  - XREADGROUP│       ┌──────────────┐                  │
│              │  - normalize │◀──────│ EventSink    │                  │
│              │  - upsert    │       │ (Redis)      │                  │
│              └──────────────┘       └──────────────┘                  │
└──────────────────────────────────────────────────────────────────────┘
```

## Detailed Design

### Component 1: Internal HTTP API

New binary: `cmd/api/main.go`. Built and packaged alongside the
existing manager + (new) webhook-receiver binaries. Single
container image, multiple entrypoints via the args.

**Framework:** [`chi`](https://pkg.go.dev/github.com/go-chi/chi/v5)
for routing + middleware. Stdlib `net/http` for everything else.
No gRPC, no Connect, no fancy ORM. Match the operator's
"minimum dependencies" posture.

**OpenAPI generation:** [`oapi-codegen`](https://github.com/oapi-codegen/oapi-codegen)
from hand-written `api/openapi/v1/openapi.yaml`. Hand-written
spec, generated server stubs + client stubs. The spec lives in
the repo and is what the external router code-generates against.

**Endpoints (v0.3.0 surface, all under `/api/v1/`):**

| Method + Path | Purpose |
|---|---|
| `GET /healthz`, `/readyz` | Liveness + readiness. |
| `GET /openapi.yaml` | Serves the spec. |
| `GET /platforms` | List Platforms. |
| `GET /platforms/{name}` | Get one Platform with current status. |
| `GET /scans` | List Scans (cluster-wide, supports `?namespace=` filter). |
| `GET /scans/{ns}/{name}` | Get one Scan. |
| `POST /scans/{ns}/{name}/suspend` | Set `spec.suspend=true`. |
| `POST /scans/{ns}/{name}/resume` | Set `spec.suspend=false`. |
| `POST /scans/{ns}/{name}/runs` | Force a one-shot Run for this Scan. Implementation: create a Run with `spec.target.repos` cleared (i.e., go through discovery), no `parentScanRef`. |
| `GET /runs` | List Runs (cluster-wide, supports `?scan=`, `?platform=`, `?status=` filters). |
| `GET /runs/{ns}/{name}` | Get one Run. |
| `DELETE /runs/{ns}/{name}` | Delete a Run (cascades to its Job). |
| `GET /events` | Query historical events from Postgres. Supports `?owner=`, `?repo=`, `?since=`, `?until=`, `?type=`, pagination. |
| `GET /events/by-owner/{owner}` | Events filtered by `ownership.owner` (Backstage owner field). |
| `GET /repos` | List repos seen in events, with summary (latest Run, open PR count, vuln count). |
| `GET /repos/{owner}/{name}/events` | All events for one repo. |
| `GET /metrics-summary` | Aggregate counts for the UI dashboard (total runs in last 24h, total open PRs, total vulns). Cheap rollup, not a Prom replacement. |

**K8s client:** uses the operator's existing
`sigs.k8s.io/controller-runtime/pkg/client.Client` (cached client
in-process when the API binary runs alongside the manager; direct
client when run separately).

**RBAC:** new ClusterRole `renovate-operator-api` with
`get`/`list`/`watch` on Platforms, Scans, Runs across the cluster;
`patch` on Scans (for suspend/resume); `create` on Runs (for
force-run); `delete` on Runs. Bound to a dedicated ServiceAccount.

### Component 2: EventSink consumer

Runs as a goroutine inside the API binary (single deployable
unit; the consumer needs the same Postgres pool as the read
endpoints anyway). Toggleable to a sidecar if scale requires it
later.

**Stream consumption:**

- `XREADGROUP` from `renovate.events` (configurable), with a
  consumer group named `renovate-operator-consumer` (configurable).
- One in-flight message at a time per worker goroutine; bounded
  worker count (default 4).
- `XACK` after the row is committed to Postgres.
- On parse failure (malformed CloudEvent, schema mismatch,
  unknown event type), the message is `XACK`ed, logged with the
  raw payload, and a counter (`renovate_consumer_skipped_total{reason}`)
  is incremented. Not retried — a malformed event is permanently
  malformed.
- On Postgres failure, the message is **not** `XACK`ed; the
  consumer backs off and retries. Redis's pending-entries-list
  is the retry queue.

**Schema versioning:** consumer reads `dataschema` from the
CloudEvent envelope; dispatches to a per-version handler. v1
handler ships in v0.3.0; v2+ handlers added as the event payload
evolves.

### Component 3: Datastore

**Choice: Postgres 16+.** Why:

- The query patterns (`WHERE owner = $1 AND opened_at > NOW() - $2`)
  are textbook RDBMS. Time-series databases don't help here —
  cardinality is moderate (events per repo per day) and the
  filters are categorical, not numeric.
- The operator's environment is K8s; CloudNativePG and Crunchy
  Postgres Operator are well-understood deploys.
- Backstage ecosystem standardizes on Postgres; if/when the UI
  ends up Backstage-shaped, no impedance mismatch.

**Deployment:** the chart does *not* deploy Postgres. The user
provides connection details via a Secret (same posture as the
EventSink's Redis connection). For the homelab a single-pod
Postgres is fine; for production CloudNativePG or a managed
service.

**Schema migration:** the API binary embeds migrations via
[`golang-migrate/migrate`](https://github.com/golang-migrate/migrate)
and runs them on startup. Idempotent, transaction-wrapped, fails
the pod readiness if migration fails.

**Tables (initial v1):**

```sql
CREATE TABLE events (
  id              BIGSERIAL PRIMARY KEY,
  ce_id           TEXT NOT NULL UNIQUE,           -- CloudEvent id, dedupe key
  ce_type         TEXT NOT NULL,                  -- "dev.fartlab.renovate.run.repo.completed"
  ce_time         TIMESTAMPTZ NOT NULL,
  ce_source       TEXT NOT NULL,                  -- "renovate-operator/<scan-ns>/<scan-name>"
  ce_subject      TEXT NOT NULL,                  -- "<owner>/<name>"
  ce_dataschema   TEXT NOT NULL,
  raw_payload     JSONB NOT NULL,                 -- full event for debugging
  run_uid         TEXT NOT NULL,
  scan_name       TEXT NOT NULL,
  platform_name   TEXT NOT NULL,
  repo_owner      TEXT NOT NULL,
  repo_name       TEXT NOT NULL,
  outcome         TEXT NOT NULL,                  -- succeeded|failed|skipped
  owner_field     TEXT NOT NULL DEFAULT '',       -- ownership.owner
  system_field    TEXT NOT NULL DEFAULT '',
  lifecycle_field TEXT NOT NULL DEFAULT '',
  ownership_src   TEXT NOT NULL DEFAULT 'unset',
  ingested_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),

  CONSTRAINT events_repo_check CHECK (repo_owner <> '' AND repo_name <> '')
);
CREATE INDEX events_repo_idx           ON events (repo_owner, repo_name, ce_time DESC);
CREATE INDEX events_owner_idx          ON events (owner_field, ce_time DESC) WHERE owner_field <> '';
CREATE INDEX events_run_idx            ON events (run_uid);
CREATE INDEX events_type_time_idx      ON events (ce_type, ce_time DESC);

CREATE TABLE prs (
  id            BIGSERIAL PRIMARY KEY,
  event_id      BIGINT NOT NULL REFERENCES events(id) ON DELETE CASCADE,
  action        TEXT NOT NULL,    -- opened|updated|closed
  pr_number     INT NOT NULL,
  url           TEXT NOT NULL,
  labels        TEXT[] NOT NULL DEFAULT '{}',
  automerge     BOOLEAN NOT NULL,
  observed_at   TIMESTAMPTZ NOT NULL
);
CREATE INDEX prs_repo_open_age_idx ON prs (event_id, observed_at)
  WHERE action = 'opened';

CREATE TABLE vulnerabilities (
  id            BIGSERIAL PRIMARY KEY,
  event_id      BIGINT NOT NULL REFERENCES events(id) ON DELETE CASCADE,
  advisory_id   TEXT NOT NULL,
  severity      TEXT NOT NULL,
  package       TEXT NOT NULL,
  observed_at   TIMESTAMPTZ NOT NULL
);
CREATE INDEX vulns_repo_sev_idx ON vulnerabilities (event_id, severity);
```

**Retention:** configurable `eventsRetentionDays` (default 90).
A nightly job (`pg_cron` or a goroutine in the API binary) deletes
rows older than the threshold. Long-term archival is the user's
problem.

### Component 4: External UI / API router

**Not built in this repo.** Lives in `<separate-repo>` (name TBD,
suggested `renovate-operator-ui`).

**Stack (proposed, not binding):**

- **Bun** runtime for speed and built-in TS support.
- **Hono** for HTTP routing on the router side (lightweight,
  edge-friendly).
- **React** + **TanStack Query** + **TanStack Router** for the UI.
- **OpenAPI codegen** (e.g., `openapi-typescript` +
  `openapi-fetch`) to consume the operator's spec.

**Responsibilities:**

- TLS termination at the Ingress.
- Auth: OIDC with whatever provider the user runs (Authentik,
  Dex, Auth0, etc.). The router validates tokens and adds an
  `X-User`-like header that the internal API ignores (the
  internal API trusts everything; auth is the router's job).
- Per-user authorization: filter the events stream by the
  authenticated user's owner-group memberships. This is where
  "developer logs in, sees only their team's repos" happens.
- Serves the React UI as static assets.
- Proxies API calls to the internal API.

**Contract with the internal API:** the OpenAPI spec at
`/openapi.yaml`. Any change there is a coordinated release;
breaking changes bump the spec version and the router pins to
the version it knows.

**What this DESIGN does *not* specify** about the UI: page layout,
component library, the exact "owner" model in the auth provider,
how owner-group → user mapping happens. Those are the UI repo's
DESIGN. This doc reaches as far as the contract.

### Component 5: Webhook receiver

Moved here from DESIGN-0002 because it shares the "external
interactions" theme.

- New `cmd/webhook-receiver/main.go` binary; new
  `dist/chart/templates/webhook/` Deployment + Service +
  optional Ingress (off by default; `webhook.enabled: false`).
- Endpoints:
  - `POST /github/{platform-name}` — HMAC-SHA256 against the App
    webhook secret; acts on `push`, `pull_request`,
    `installation_repositories`.
  - `POST /forgejo/{platform-name}` — token-header verification;
    acts on `push`.
- On a relevant event, creates a one-shot `RenovateRun` in the
  Platform's default namespace (Platform-scoped; new
  `RenovatePlatform.spec.webhookRunNamespace` field, defaults to
  the Platform's source namespace) with `spec.target.repos:
  [owner/name]` and no `parentScanRef`.
- New `RenovateRun.spec.target.repos []string` field (optional;
  set only by webhook receiver, not by humans). Reconciler skips
  discovery when this list is set.
- Webhook receiver does **not** itself run Renovate — it only
  files the Run.
- Webhook-triggered Runs emit through the EventSink identically
  to scheduled Runs; consumer sees the same event shape.
- Dedupe: GitHub fires `push` per branch; coalesce events to
  one Run per (repo, 30s window) via a sliding-window in-memory
  cache. A burst of pushes during a long-running rebase doesn't
  create N Runs.
- Webhook receiver Prometheus collectors:
  `renovate_webhook_received_total{platform, event, result}`,
  `renovate_webhook_dedup_skipped_total{platform}`,
  `renovate_webhook_signature_verify_failed_total{platform}`.

## API / Interface Changes

**CRD additions, all additive:**

- `RenovateRun.spec.target` struct (optional; populated only by
  webhook receiver):
  ```go
  type RunTarget struct {
      Repos []string `json:"repos,omitempty"` // "owner/name"
  }
  ```
- `RenovatePlatform.spec.webhookRunNamespace` (optional, string):
  where webhook-triggered Runs get created. Defaults to the
  Platform's source namespace.

**Helm chart additions (all gated to default-off):**

```yaml
# values.yaml additions
api:
  enabled: false
  replicaCount: 1
  image: { repository: "ghcr.io/donaldgifford/renovate-operator-api", tag: "" }
  resources: { ... }
  service: { type: ClusterIP, port: 8080 }
  serviceAccount: { create: true }
  rbac: { create: true }
  postgres:
    dsnSecretRef: { name: "", key: "dsn" }        # required when api.enabled=true
    maxOpenConns: 25
    migrations: { autoApply: true }
  consumer:
    enabled: true                                  # consumer in same binary
    group: "renovate-operator-consumer"
    workers: 4
  eventsRetentionDays: 90

webhook:
  enabled: false
  replicaCount: 2
  image: { repository: "ghcr.io/donaldgifford/renovate-operator-webhook", tag: "" }
  resources: { ... }
  service: { type: ClusterIP, port: 8080 }
  ingress:
    enabled: false
    className: ""
    hosts: []
    tls: []
  serviceAccount: { create: true }
  rbac: { create: true }                           # create RenovateRun in webhookRunNamespace
  dedupeWindow: "30s"
```

**Binary additions:**

- `cmd/api/` — Internal HTTP API + consumer.
- `cmd/webhook-receiver/` — Webhook receiver.

**OpenAPI spec at `api/openapi/v1/openapi.yaml`.** Committed to
this repo. Generated server + client stubs sit alongside.

## Data Model

See §Component 3 — Datastore. Schema is owned by the API binary;
migrations bundled in the image, applied at startup.

CRDs gain two small additive fields (`RunTarget`,
`Platform.spec.webhookRunNamespace`). No breaking changes.

## Testing Strategy

- **Unit (`*_test.go`)**:
  - API handlers: table-driven tests with a fake K8s client
    (`fake.NewClientBuilder`) and a `sqlmock`-driven Postgres.
  - Consumer: miniredis + sqlmock; covers happy path, parse
    failure, Postgres failure (XACK semantics), schema version
    dispatch.
  - Webhook receiver: HMAC verification (happy + tampered),
    dedupe sliding window, Platform → namespace resolution.
- **Controller (envtest)**:
  - Webhook receiver → Run creation with `spec.target.repos`
    populated; Run reconciler short-circuits discovery; Run
    completes; EventSink emits.
- **e2e (kind)**:
  - Stand up Redis (miniredis), Postgres (CloudNativePG), the
    operator chart with all four components enabled. Drive a
    Run, hit the API for the resulting events, assert webhook
    receiver creates a Run on a simulated GitHub push.
- **Contract tests**: the external router repo
  consumes the OpenAPI spec from a pinned version of this repo
  and runs codegen + a smoke test. CI in *this* repo validates
  the spec is well-formed via `spectral lint`.
- Coverage gate stays at ≥80% per package per IMPL-0001.

## Migration / Rollout Plan

v0.3.0 release is fully additive. Upgrade path from any v0.2.x install:

1. `helm upgrade renovate-operator oci://ghcr.io/donaldgifford/charts/renovate-operator --version 0.3.0`.
2. New CRD field `RenovateRun.spec.target` is additive; existing
   Runs unaffected (field is optional).
3. New CRD field `RenovatePlatform.spec.webhookRunNamespace` is
   additive; existing Platforms unaffected.
4. `helm diff` shows new gated blocks (`api.*`, `webhook.*`);
   nothing renders until opted in.
5. To opt in to the API + UI stack:
   ```bash
   helm upgrade renovate-operator ... \
     --set api.enabled=true \
     --set api.postgres.dsnSecretRef.name=postgres-dsn \
     --set api.postgres.dsnSecretRef.key=dsn
   ```
   Then deploy the external router separately, configured against
   the in-cluster API Service.
6. To opt in to webhooks:
   ```bash
   helm upgrade renovate-operator ... \
     --set webhook.enabled=true \
     --set webhook.ingress.enabled=true \
     --set webhook.ingress.hosts[0].host=renovate-webhooks.example.com \
     ...
   ```
7. EventSink (from v0.2.0) must be enabled and pointing at a
   reachable Redis for the consumer to have anything to read.
   Template guard fails fast if `api.enabled=true && api.consumer.enabled=true && eventSink.enabled=false`.

Acceptance criteria for v0.3.0 GA:

- All five components deployable independently via Helm values.
- API serves a valid OpenAPI spec at `/openapi.yaml`; spec
  passes `spectral lint`.
- Consumer ingests events from the EventSink Redis Stream and
  upserts them into Postgres without dropping.
- Webhook receiver creates a Run on a simulated GitHub `push`
  in the homelab.
- An external router (a reference implementation in
  `<separate-repo>`) can authenticate a user, fetch events
  for the user's owner, and render a usable UI.

## Security Considerations

- **Internal API has no auth.** Reachable only via in-cluster
  `ClusterIP`. NetworkPolicy must restrict ingress to the
  external router's Pod selector. The chart should ship a
  NetworkPolicy template that does this when
  `api.enabled && api.networkPolicy.enabled`.
- **External router owns auth.** This is the only attack surface
  reachable from outside the cluster. The router must:
  - Validate OIDC tokens on every request.
  - Enforce per-user authorization (owner-group filter) before
    proxying to the internal API.
  - Set sensible CSP, CORS, and rate-limit headers.
  - The router is *not* in this repo and is *not* covered by
    this repo's security posture; its repo will have its own.
- **Webhook receiver** verifies signatures (HMAC-SHA256 for
  GitHub, token-header for Forgejo) on every inbound request.
  Unverified requests are dropped with a 401, no Run created,
  counter incremented. Webhook secrets live in a K8s Secret
  referenced from the Platform.
- **Postgres connection** uses a DSN from a Secret; TLS is the
  user's choice (chart values pass through `sslmode`).
- **API → Postgres** uses parameterized queries throughout; no
  string concatenation. `sqlc` or hand-rolled `db.QueryContext`
  with `$1`/`$2` placeholders.
- **Run-creation endpoint (`POST /scans/.../runs`)** is rate-
  limited per Scan to prevent the UI from accidentally triggering
  runaway Runs. Default 1 force-run per Scan per minute,
  configurable.

## Open Questions

- **Consumer in-process vs. sidecar.** Default to in-process for
  v0.3.0; promote to sidecar Deployment if benchmarks show the
  consumer starving the API request handlers. Make this a
  one-line config flip.
- **CloudNativePG vs. user-provided Postgres.** Chart should
  document both paths; default values require a user-provided
  Postgres (DSN Secret). Bundling CloudNativePG would simplify
  the homelab path but adds a heavy dependency we don't otherwise
  carry.
- **External router repo location.** Same org, separate repo?
  Monorepo `web/` directory? Lean toward separate repo so the
  release cadence and language tooling don't bleed into the Go
  operator's CI.
- **Schema migration: API binary on startup, or one-shot Job?**
  Startup is simpler but introduces a startup-order dependency
  (Postgres must be reachable). One-shot Job is cleaner for
  GitOps but adds operational moving parts. Lean toward startup
  with retry + readiness gate.
- **Owner-group membership lookup.** Where does the router get
  "which owner-groups does this user belong to?" Backstage
  Catalog API? OIDC token claims? Out of scope for *this*
  DESIGN; the router repo owns the answer. Internal API only
  needs the filter to be expressible as `?owner=X` query params.
- **Webhook dedupe state.** In-memory sliding window per replica
  works for low-throughput, but two replicas could each create a
  Run for the same `push`. Acceptable for v0.3.0; revisit with
  Redis-backed dedupe if it becomes a real problem.
- **Event payload `outcome` granularity.** v0.2.0 emits one event
  per (Run, repo). For real-time UIs that want progress, we'd
  need per-PR events too. Defer until UI demand surfaces.
- **Force-run authorization** in the API: today the API trusts
  the router. Should the API record *which user* (via header)
  triggered a force-run in an audit table? Lean yes; cheap to
  add and useful for accountability.

## References

- [DESIGN-0002](0002-renovate-operator-v020.md) — v0.2.x scope
  decision; places this design as v0.3.0.
- [DESIGN-0003](0003-eventsink-for-renovate-operator-v020.md) —
  v0.2.0 EventSink; the consumer in this doc is the canonical
  reader of its stream.
- [INV-0006](../investigation/0006-operationalizing-renovate-operator-at-scale-dashboard-risk.md) —
  origin of the "operator emits, doesn't display" cut that put
  the UI/consumer/DB downstream of EventSink in the first place.
- [RFC-0001 §Phase 2](../rfc/0001-build-kubebuilder-renovate-operator.md) —
  original webhook commitment; now fulfilled as Component 5.
- OpenAPI v3.1 spec.
- Bun / Hono / React / TanStack — proposed router stack (subject
  to UI repo's final call).
- CloudNativePG / Crunchy Postgres Operator — sensible in-cluster
  Postgres options.
- Backstage catalog model — informs the `ownership` schema the
  EventSink emits and the API filters on.
