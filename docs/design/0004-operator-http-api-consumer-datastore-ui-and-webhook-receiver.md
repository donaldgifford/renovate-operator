---
id: DESIGN-0004
title: "Embedded Connect API, Bun UI, and webhook receiver for renovate-operator v0.3.0"
status: Draft
author: Donald Gifford
created: 2026-05-31
---
<!-- markdownlint-disable-file MD025 MD041 -->

# DESIGN 0004: Embedded Connect API, Bun UI, and webhook receiver for renovate-operator v0.3.0

**Status:** Draft
**Author:** Donald Gifford
**Date:** 2026-05-31 (re-drafted 2026-08-01)

> **Re-draft note (2026-08-01).** The original version of this doc
> specified five components: a standalone REST internal API
> (chi + oapi-codegen), an EventSink consumer, a Postgres datastore,
> an external Bun router/UI, and a webhook receiver. With
> [DESIGN-0005](0005-operator-state-in-postgres-and-valkey-backed-scheduling.md)
> making the operator the direct writer of Postgres state, the
> consumer is gone and the datastore moved to DESIGN-0005. The API
> survived the rethink but changed shape: **ConnectRPC embedded in
> the manager binary** instead of a standalone REST service. Three
> components remain. Rationale for the layering decision is in
> DESIGN-0005 §Background.

<!--toc:start-->
- [Overview](#overview)
- [Goals and Non-Goals](#goals-and-non-goals)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Background](#background)
- [Architecture overview](#architecture-overview)
- [Detailed Design](#detailed-design)
  - [Component 1: Embedded Connect API](#component-1-embedded-connect-api)
  - [Component 2: External UI / BFF](#component-2-external-ui--bff)
  - [Component 3: Webhook receiver](#component-3-webhook-receiver)
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
what Renovate is doing across their repos, and external systems can
trigger Runs. Three components, all opt-in:

1. **Embedded Connect API** — a [ConnectRPC](https://connectrpc.com)
   service mounted as an `http.Handler` inside the existing manager
   process. Reads Postgres (DESIGN-0005's state) through the
   manager's in-process pool, reads/writes CRDs through the
   manager's cached client. Speaks Connect + gRPC + gRPC-Web on one
   port. The proto package in this repo is the contract; `buf
   breaking` in CI enforces it.
2. **External UI / BFF** — Bun + Hono + React. Owns OIDC auth, TLS,
   public exposure, per-user filtering, and the browser experience.
   Talks to the Connect API via `connect-es` (fetch-based, no
   grpc-js). **Never touches Postgres, Valkey, or the Kubernetes
   API.**
3. **Webhook receiver** — separate binary + Deployment in the
   chart. Inbound platform `push` events create one-shot Runs.

The thin-API principle, stated once: the Connect API is a
translation layer over state the operator already owns. Query
logic that exists to serve one UI page lives behind one RPC; no
business logic accretes in the API that belongs in the reconcilers,
and no datastore access ever moves client-side.

## Goals and Non-Goals

### Goals

- Connect service definition in `proto/renovate/v1/`, served from
  the manager process on a dedicated port, off by default.
- Read RPCs over Postgres (repo history, PRs, vulns, summaries) and
  over CRDs (Platforms, Scans, Runs).
- The four imperative operations — suspend Scan, resume Scan, force
  Run, delete Run — as RPCs, executed with the manager's own client.
- Server-streaming RPC for live Run progress (feeds the BFF's SSE
  to the browser).
- `buf generate` produces Go server stubs (this repo) and TS client
  stubs (consumed by the UI repo); `buf breaking` gates CI.
- Webhook receiver: signature-verified inbound events → one-shot
  Runs, dedupe window, off by default.
- All components fully opt-in; a v0.2.x install upgrading sees zero
  behavior change.

### Non-Goals

- **No auth in the Connect API.** Auth is the BFF's job. The API is
  ClusterIP-only; NetworkPolicy restricts ingress to the BFF pods.
- **No REST/OpenAPI surface.** The proto is the contract. (Connect's
  JSON-over-POST protocol means `curl` still works for debugging.)
- **No consumer, no event pipeline.** The operator writes its own
  state (DESIGN-0005).
- **No UI implementation detail in this doc.** Page layout,
  component library, owner-group → user mapping are the UI repo's
  concern. This doc reaches as far as the proto contract and the
  BFF's responsibilities.
- **No multi-tenancy.** Single operator-namespace install; v0.4.x+
  question.
- **No general K8s proxy.** The RPC surface is exactly what the UI
  needs, nothing more.

## Background

The original five-component design assumed the operator owned no
state, so a standalone API had to exist to aggregate CRDs + an
event-fed Postgres. Two rounds of rethinking (2026-08-01) collapsed
this:

- Operator writes Postgres directly → consumer deleted, datastore
  design moved to DESIGN-0005.
- The remaining question — should the UI's Bun server just query
  Postgres itself? — resolved to **no**: keeping a thin Go API in
  front means the schema stays private to this repo (refactor
  freely; `buf breaking` is the compatibility gate), cluster
  credentials never reach the internet-facing app, and future
  non-UI consumers (a CLI, repo-guardian integration) get a gRPC
  surface for free from the same server.
- Embedding the API in the manager (connect-go is just an
  `http.Handler`; the manager already serves metrics/healthz
  listeners) removes the standalone-Deployment cost that made the
  layer feel heavy. Splitting it out later is a `main.go` wiring
  change, not a redesign.

## Architecture overview

```
                 ┌─────────────────────────────────────────────┐
                 │ External (Ingress, OIDC, TLS)               │
                 └─────────────────────────────────────────────┘
                                     │ HTTPS
                                     ▼
                 ┌─────────────────────────────────────────────┐
                 │ BFF/UI (bun + hono + React)   separate repo │
                 │  - OIDC auth, per-user owner filtering      │
                 │  - serves React static assets               │
                 │  - connect-es client → Connect API          │
                 │  - SSE to browser from WatchRuns stream     │
                 └─────────────────────────────────────────────┘
                                     │ Connect (HTTP), in-cluster
                                     │ ClusterIP + NetworkPolicy
                                     ▼
┌───────────────────────────────────────────────────────────────────┐
│ manager pod (renovate-operator chart)                             │
│                                                                   │
│  ┌─────────────────────────────┐   ┌──────────────────────────┐  │
│  │ Connect API (:9444)         │   │ Reconcilers (v0.1.x +    │  │
│  │  - reads PG (shared pool)   │   │  DESIGN-0005 state)      │  │
│  │  - reads CRDs (cached cli)  │   └──────────────────────────┘  │
│  │  - 4 imperative ops         │        │                        │
│  └─────────────────────────────┘        │ SQL                    │
│              │       │                  ▼                        │
│              │ SQL   │ K8s API   ┌──────────┐                    │
│              └───────┼──────────▶│ Postgres │                    │
│                      ▼           └──────────┘                    │
│               ┌──────────┐   (workers → Valkey is cache-only,    │
│               │ K8s API  │    outside the manager: DESIGN-0005)  │
│               └──────────┘                                       │
└───────────────────────────────────────────────────────────────────┘

┌──────────────────────────┐
│ Webhook receiver (own    │   GitHub/Forgejo → verify sig →
│ Deployment, off default) │   one-shot RenovateRun → reconcile
└──────────────────────────┘
```

## Detailed Design

### Component 1: Embedded Connect API

**Framework:** [`connectrpc.com/connect`](https://connectrpc.com)
(connect-go). Handlers mount on a dedicated listener (default
`:9444`) started by the manager when `api.enabled=true`, alongside
the existing metrics/healthz/pprof listeners. One implementation
serves three protocols (Connect, gRPC, gRPC-Web) — a future Go CLI
dials gRPC against the same port with zero server work.

**Contract:** `proto/renovate/v1/*.proto`, owned by this repo.
`buf.gen.yaml` generates Go stubs in-repo; the UI repo runs its own
`buf generate` against a pinned tag of this repo (or BSR later —
see Open Questions). `buf lint` + `buf breaking --against` main in
CI here.

**Service surface (v1):**

```protobuf
service RenovateService {
  // CRD reads (manager's cached client)
  rpc ListPlatforms(ListPlatformsRequest) returns (ListPlatformsResponse);
  rpc GetPlatform(GetPlatformRequest) returns (GetPlatformResponse);
  rpc ListScans(ListScansRequest) returns (ListScansResponse);       // namespace filter
  rpc GetScan(GetScanRequest) returns (GetScanResponse);
  rpc ListRuns(ListRunsRequest) returns (ListRunsResponse);          // scan/platform/status filters
  rpc GetRun(GetRunRequest) returns (GetRunResponse);

  // Imperative ops (manager's client; rate-limited)
  rpc SuspendScan(SuspendScanRequest) returns (SuspendScanResponse);
  rpc ResumeScan(ResumeScanRequest) returns (ResumeScanResponse);
  rpc ForceRun(ForceRunRequest) returns (ForceRunResponse);          // 1/Scan/min default
  rpc DeleteRun(DeleteRunRequest) returns (DeleteRunResponse);

  // State reads (Postgres, DESIGN-0005 schema)
  rpc ListRepos(ListReposRequest) returns (ListReposResponse);       // summary: latest run, open PRs, vulns
  rpc GetRepoHistory(GetRepoHistoryRequest) returns (GetRepoHistoryResponse);
  rpc QueryResults(QueryResultsRequest) returns (QueryResultsResponse); // owner/repo/since/until/outcome, paginated
  rpc GetSummary(GetSummaryRequest) returns (GetSummaryResponse);    // dashboard rollups

  // Live progress (server-streaming; BFF relays as SSE)
  rpc WatchRuns(WatchRunsRequest) returns (stream WatchRunsEvent);
}
```

Owner filtering (`owner` field on `QueryResults`/`ListRepos`) is a
plain request field — the API trusts its caller; *which* owners a
user may pass is the BFF's authorization decision.

**Clients, credentials, RBAC:** the API runs in-process, so it uses
the manager's controller-runtime cached client and the DESIGN-0005
`pgxpool` directly. No new ServiceAccount, no new RBAC objects, no
second Postgres connection surface. The imperative RPCs are
verb-for-verb what the manager's Role already allows. (Accepted
trade-off vs. a least-privilege split — noted in Security
Considerations.)

**Observability:** connect-go interceptors emit
`renovate_api_requests_total{rpc, code}` and
`renovate_api_request_duration_seconds{rpc}`; OTEL tracing reuses
the manager's existing tracer (`connectrpc.com/otelconnect`).

### Component 2: External UI / BFF

**Not built in this repo.** Lives in a separate repo (suggested
`renovate-operator-ui`) — with the proto as the contract, cross-repo
coupling is a pinned tag, not a shared schema.

**Stack:** Bun runtime, Hono for the BFF routes, React + TanStack
Query/Router for the UI, `@connectrpc/connect` +
`@connectrpc/connect-web` for the typed client (fetch-based; no
grpc-js, no HTTP/2 shenanigans in Bun).

**Responsibilities:**

- OIDC against the user's provider (Authentik, Dex, Auth0, ...);
  validates tokens on every request.
- Per-user authorization: maps the authenticated user to
  owner-groups and constrains the `owner` fields it passes to the
  Connect API. This is where "developer sees only their team's
  repos" happens.
- TLS/Ingress exposure; CSP/CORS/rate-limit headers.
- Serves the React bundle; relays `WatchRuns` as SSE/WebSocket to
  the browser.

**Contract discipline:** the BFF pins a proto tag and regenerates
via `buf generate` in its CI. Breaking proto changes require a
coordinated bump — which `buf breaking` here makes deliberate
rather than accidental.

### Component 3: Webhook receiver

Carried over from the original draft; unchanged in substance.

- New `cmd/webhook-receiver/` binary; `dist/chart/templates/webhook/`
  Deployment + Service + optional Ingress (`webhook.enabled: false`).
- Endpoints:
  - `POST /github/{platform-name}` — HMAC-SHA256 against the App
    webhook secret; acts on `push`, `pull_request`,
    `installation_repositories`.
  - `POST /forgejo/{platform-name}` — token-header verification;
    acts on `push`.
- On a relevant event, creates a one-shot `RenovateRun` in
  `RenovatePlatform.spec.webhookRunNamespace` (new optional field,
  defaults to the Platform's source namespace) with
  `spec.target.repos: [owner/name]` and no parent Scan.
- New `RenovateRun.spec.target.repos []string` (optional; set by
  the receiver, not humans). Reconciler skips discovery when set.
- Webhook-triggered Runs flow through DESIGN-0005 state (and the
  EventSink, if enabled) identically to scheduled Runs.
- Dedupe: coalesce to one Run per (repo, 30s window) via in-memory
  sliding-window cache.
- Collectors: `renovate_webhook_received_total{platform, event, result}`,
  `renovate_webhook_dedup_skipped_total{platform}`,
  `renovate_webhook_signature_verify_failed_total{platform}`.

## API / Interface Changes

**CRD additions (additive):**

- `RenovateRun.spec.target.repos []string` (optional).
- `RenovatePlatform.spec.webhookRunNamespace` (optional string).

**Helm chart additions (default off):**

```yaml
api:
  enabled: false                 # requires state.enabled (DESIGN-0005)
  port: 9444
  service: { type: ClusterIP, port: 9444 }
  networkPolicy:
    enabled: true                # restrict to BFF pod selector
    allowedPodSelector: {}
  forceRun:
    rateLimitPerScan: "1m"       # min interval between force-runs per Scan

webhook:
  enabled: false
  replicaCount: 2
  image: { repository: "ghcr.io/donaldgifford/renovate-operator-webhook", tag: "" }
  resources: { }
  service: { type: ClusterIP, port: 8080 }
  ingress: { enabled: false, className: "", hosts: [], tls: [] }
  serviceAccount: { create: true }
  rbac: { create: true }         # create RenovateRun in webhookRunNamespace
  dedupeWindow: "30s"
```

Template guard: `api.enabled && !state.enabled` → render error
(the API's state reads need DESIGN-0005's Postgres).

**Binary additions:** `cmd/webhook-receiver/` only. The API is not
a binary — it is a listener in the manager.

**Proto at `proto/renovate/v1/`** with `buf.yaml` / `buf.gen.yaml`
at the repo root. `buf lint` + `buf breaking` join the CI gates.

## Data Model

Owned by [DESIGN-0005](0005-operator-state-in-postgres-and-valkey-backed-scheduling.md).
The API is a reader; it adds no tables. If the force-run audit
question (below) resolves yes, the audit table lands as a
DESIGN-0005 migration.

## Testing Strategy

- **Unit:** Connect handlers via in-memory
  `connect.NewUnaryHandler` round-trips with
  `fake.NewClientBuilder` for CRD paths and `pgxmock` for state
  paths; table-driven per RPC (happy, not-found, filter
  combinations, rate-limit rejection).
- **Controller (envtest):** imperative RPCs against a real
  apiserver — SuspendScan flips `spec.suspend` and the Scan
  controller reacts; ForceRun creates a Run the Run controller
  picks up. Webhook receiver → Run with `target.repos` populated →
  reconciler short-circuits discovery.
- **e2e (kind):** chart with `state`, `api`, `webhook` enabled;
  drive a Run; assert `ListRepos`/`GetRepoHistory` return the
  DESIGN-0005 rows; simulated GitHub `push` creates a deduped Run;
  `WatchRuns` streams phase transitions.
- **Contract:** `buf lint` + `buf breaking --against '.git#branch=main'`
  in this repo's CI; the UI repo's CI regenerates from its pinned
  tag and type-checks.
- Coverage gate ≥80% per package per IMPL-0001.

## Migration / Rollout Plan

Fully additive on top of a v0.2.x (DESIGN-0005-enabled) install:

1. `helm upgrade ... --version 0.3.0` — nothing renders until
   opted in.
2. API: `--set api.enabled=true` (requires `state.enabled=true`
   from v0.2.x; template guard enforces). Connect listener comes up
   on `:9444` behind a ClusterIP Service + NetworkPolicy.
3. Deploy the BFF/UI from its repo, pointed at the in-cluster
   Service, with its OIDC config.
4. Webhooks: `--set webhook.enabled=true` + Ingress values; add the
   webhook URL + secret to the GitHub App / Forgejo repo settings.

Acceptance criteria for v0.3.0 GA:

- All three components independently deployable via values.
- `buf breaking` green against the tag the shipped BFF pins.
- BFF reference implementation authenticates a user, renders that
  user's repos/PRs from `QueryResults`, and live-updates a Run via
  the `WatchRuns` stream.
- Simulated GitHub `push` produces exactly one Run in the homelab.

## Security Considerations

- **Connect API has no auth.** ClusterIP + chart-shipped
  NetworkPolicy restricting ingress to the BFF pod selector. Anyone
  who can reach the port can do what the UI can do — the network is
  the boundary, same posture as the original draft.
- **In-process API shares the manager's identity.** A vulnerability
  in an API handler has the manager's full RBAC, which is broader
  than the API's RPC surface. Accepted for v0.3.0 (the alternative
  — a separate Deployment with a scoped SA — is the documented
  split-out path if this ever matters; the server code is identical
  either way).
- **BFF owns the entire external attack surface**: OIDC validation,
  per-user authorization *before* proxying, CSP/CORS/rate limits.
  Covered by its own repo's security posture.
- **Webhook receiver** verifies HMAC-SHA256 (GitHub) / token header
  (Forgejo) on every request; failures → 401, counter, no Run.
- **ForceRun rate limit** (default 1/Scan/min) bounds UI-triggered
  runaway Runs.
- **Postgres access** stays parameterized (`$1`/`$2`), pooled, and
  entirely server-side; the schema is unreachable from outside the
  manager pod.

## Open Questions

- **TS stub distribution.** UI repo generating against a pinned git
  tag is the zero-infra default. Buf Schema Registry would give
  versioned packages + remote plugins; adopt only if tag-pinning
  chafes.
- **`WatchRuns` fan-out.** One K8s watch (manager cache) multiplexed
  to N streaming clients — needs a small broadcast hub. Bound
  clients (the BFF aggregates browsers; expected N≈BFF replicas).
- **Force-run audit.** Record which user (BFF-supplied header)
  triggered a ForceRun in an audit table? Lean yes — cheap, useful;
  needs the header contract defined with the BFF.
- **Webhook dedupe across replicas.** In-memory window per replica
  means two replicas can double-fire. Acceptable at v0.3.0
  throughput; a shared Valkey (e.g. the worker-cache instance from
  DESIGN-0005, if deployed) is the obvious shared-window store if
  it becomes real.
- **Pagination convention.** Cursor (opaque token) vs. offset for
  `QueryResults`. Lean cursor from day one — cheap now, painful to
  retrofit.

## References

- [DESIGN-0005](0005-operator-state-in-postgres-and-valkey-backed-scheduling.md) —
  the state this API reads; §Background records why the thin-API
  layering was kept and the REST/consumer/datastore version of this
  doc was retired.
- [DESIGN-0002](0002-renovate-operator-v020.md) — original v0.2.x/
  v0.3.x scoping (release table superseded by DESIGN-0005).
- [DESIGN-0003](0003-eventsink-for-renovate-operator-v020.md) —
  outbound EventSink; decoupled from this stack.
- [RFC-0001 §Phase 2](../rfc/0001-build-kubebuilder-renovate-operator.md) —
  original webhook commitment; fulfilled as Component 3.
- [ConnectRPC](https://connectrpc.com) — connect-go, connect-es,
  otelconnect.
- [Buf](https://buf.build) — `buf lint`, `buf breaking`,
  `buf generate`.
- Backstage catalog model — informs the ownership fields the API
  filters on.
