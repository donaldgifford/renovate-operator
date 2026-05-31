---
id: DESIGN-0002
title: "renovate-operator v0.2.0"
status: Draft
author: Donald Gifford
created: 2026-05-31
---
<!-- markdownlint-disable-file MD025 MD041 -->

# DESIGN 0002: renovate-operator v0.2.0

**Status:** Draft
**Author:** Donald Gifford
**Date:** 2026-05-31

<!--toc:start-->
- [Overview](#overview)
- [Goals and Non-Goals](#goals-and-non-goals)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Background](#background)
- [Detailed Design](#detailed-design)
  - [1. Event sink (INV-0006)](#1-event-sink-inv-0006)
  - [2. Webhook receiver (RFC-0001 Phase 2)](#2-webhook-receiver-rfc-0001-phase-2)
- [API / Interface Changes](#api--interface-changes)
- [Data Model](#data-model)
- [Testing Strategy](#testing-strategy)
- [Migration / Rollout Plan](#migration--rollout-plan)
- [Open Questions](#open-questions)
- [Deferred to v0.3.x or later](#deferred-to-v03x-or-later)
- [References](#references)
<!--toc:end-->

## Overview

Scope bracket for the second release of `renovate-operator`. v0.2.0
turns the operator from "schedules Runs and emits ops metrics" into
"schedules Runs, *responds to events*, and tells downstream systems
what happened." Two load-bearing additions, both opt-in:

- A pluggable **EventSink** (Redis Streams first) that emits one
  structured event per repo per Run, with optional ownership
  enrichment and matching platform-ops Prometheus collectors.
- A **webhook receiver** Deployment that turns inbound platform
  `push` events into one-shot Runs.

Everything else previously considered for v0.2.x — token refresh,
credential-source abstraction, real `Replace` semantics, per-Scan
credential isolation, smaller items — is captured in
[Deferred to v0.3.x or later](#deferred-to-v03x-or-later) and
remains tracked in its source doc (INV-0003, ADR-0004, etc.).

This document is the planning artifact, not yet an implementation
plan. Each section ends with the open decisions the implementation
needs to resolve before any code lands; IMPL-0002 will sequence the
work once the shapes here are agreed.

## Goals and Non-Goals

### Goals

- **EventSink** package with `Sink` interface + Redis Streams
  implementation, off by default ([INV-0006](../investigation/0006-operationalizing-renovate-operator-at-scale-dashboard-risk.md)).
- **Ownership enrichment** from GitHub repo custom properties or
  `catalog-info.yaml`, also off by default, empty-string fallback.
- **Sink-level Prometheus collectors** (`up`, `published_total`,
  `publish_duration_seconds`, `dropped_total`) — operator's
  delivery contract, not consumer data.
- **Webhook receiver** as a separate Deployment in the chart, off
  by default ([RFC-0001 §Phase 2](../rfc/0001-build-kubebuilder-renovate-operator.md)).
  Inbound GitHub/Forgejo `push` events trigger a one-shot Run
  against a single repo.
- Backward compatible: every new feature is opt-in. A v0.1.x
  install on upgrade sees zero behavior change.

### Non-Goals

- **GitHub App token refresh for long Runs.** Tracked in
  [INV-0003](../investigation/0003-renovate-v43-github-app-auth-requires-autodiscover-not.md).
  Workaround stays "tighter shards" until v0.3.x.
- **Credential-source abstraction** (`*FromVault` / `*FromESO`).
  Users continue to shim Vault/ESO → K8s Secret externally.
- **Real `Replace` concurrency-policy semantics.** Still aliases
  `Forbid`; tracked in
  [ADR-0004](../adr/0004-use-conditions-and-run-children-for-status.md).
- **Per-Scan credential isolation via per-Run RBAC.** DESIGN-0001
  §multi-tenancy guidance unchanged.
- Additional platforms (GitLab, Bitbucket, Azure DevOps) — Phase 3 /
  v0.3.0.
- Conversion webhooks. Stay on `v1alpha1`; no API stability promises
  until v1beta1+.
- Built-in UI. Customer-facing visibility is downstream-of-EventSink
  by design (INV-0006 Observation 1 + 4).
- Per-team policy primitives in the CRD. Renovate's `packageRules`
  + shared presets remain the policy surface.
- Multi-cluster fan-out / mid-run worker rescaling — still
  ArgoCD-layer concerns.
- Operator-owned state DB. Anticipated direction in DESIGN-0001
  §Future architecture; not v0.2.x scope.

## Background

v0.1.0 published 2026-05-01. v0.1.1 → v0.1.3 fixed Phase-9 homelab
acceptance bugs (metrics-auth RBAC, PodSecurity worker pods, App
auth, discovery scope, schedule first-fire, `RENOVATE_PLATFORM`
mapping, discovery bool serialization, log-level override). After
that loop closed, two adjacent gaps emerged:

1. **Operationalization** — dev teams owning 1K+ repos need a
   per-team view of what Renovate is doing. The operator's existing
   Prometheus surface is for platform-ops; pushing per-repo data
   there blows cardinality. The right shape is to emit structured
   events and let a consumer system join them to ownership data and
   render whatever UI fits. INV-0006 captured this with a
   pluggable-sink, Redis-first design and an explicit "operator
   emits, does not display" cut.

2. **On-demand runs** — the RFC-0001 Phase 2 commitment for webhook
   receivers is overdue. Same release is the natural home.

Operational hardening items (token refresh, credential sources,
real `Replace`, per-Scan RBAC) deferred to a later release to keep
v0.2.0's surface coherent: it's the "operator now talks to the
outside world" release, not the "operator hardens its existing
surfaces" release. Those land in v0.3.x.

## Detailed Design

### 1. Event sink (INV-0006)

Full proposal lives in [INV-0006](../investigation/0006-operationalizing-renovate-operator-at-scale-dashboard-risk.md).
Summary of what lands in v0.2.0:

- `internal/eventsink/` package with `Sink` interface, typed
  `Event` (CloudEvents v1.0 envelope), no-op default sink.
- `internal/eventsink/redis/` impl using `redis/go-redis/v9`,
  `XADD` to a configurable stream key, MAXLEN bounded by config,
  reconnect with backoff, per-publish timeout (default 5s).
- Run reconciler calls `Publish` once per repo at the Run's
  terminal transition. Failures **do not** fail the Run; they
  surface as a Run condition and an emitted Prom counter
  (`renovate_eventsink_dropped_total`).
- Optional ownership enrichment:
  - `internal/enrichment/customprops/` — GitHub only, reads
    `/repos/{owner}/{repo}/properties/values`.
  - `internal/enrichment/catalog/` — `GET contents/catalog-info.yaml`,
    parse `spec.owner`/`spec.system`/`spec.lifecycle`.
  - Both off by default. Empty-string fields when both sources
    miss; `ownership.source` attributes which (or `"unset"`).
- Sink-level Prometheus collectors (`up`, `published_total`,
  `publish_duration_seconds`, `dropped_total`) wired through the
  `Sink` wrapper so every future impl gets them automatically.
- Chart values surface:

```yaml
eventSink:
  enabled: false
  type: redis
  redis:
    addr: ""
    db: 0
    stream: "renovate.events"
    maxLen: 100000
    tls: { enabled: false, caSecretRef: { name: "", key: "" } }
    auth: { secretRef: { name: "", key: "" } }
  enrichment:
    enabled: false
    sources: [customProperties, catalogInfo]
```

**Open for impl:** worker `result.json` vs. log-parse for the event
payload source of truth; cluster-wide vs. per-Platform sink config;
CloudEvents transport binding shape on the stream entry.

### 2. Webhook receiver (RFC-0001 Phase 2)

Inbound platform webhooks trigger out-of-band `RenovateRun`s.
Shape:

- New `cmd/webhook-receiver/main.go` binary; new `dist/chart/templates/webhook/`
  Deployment + Service + Ingress (off by default; `webhook.enabled: false`).
- Endpoints:
  - `POST /github/{platform-name}` — verifies HMAC-SHA256 against
    the App's webhook secret; acts on `push`, `pull_request`,
    `installation_repositories`.
  - `POST /forgejo/{platform-name}` — verifies via token header;
    acts on `push`.
- On a relevant event, creates a one-shot `RenovateRun` in the
  Platform's default namespace (configurable per Platform) with
  a `spec.target.repos: [owner/name]` field and no `parentScanRef`.
  The Run reconciler treats it as a normal Run with a discovery
  step that's already complete.
- New `RenovateRun.spec.target.repos []string` field (no
  validator; trusted from operator-internal callers only).
  Reconciler skips discovery when this list is set.
- Webhook receiver does **not** itself execute Renovate — it only
  files the Run and lets the Run reconciler do its job. Keeps the
  fan-in surface trivially correct.
- Webhook-triggered Runs flow through the same EventSink as
  scheduled Runs; consumers see the same event shape regardless
  of trigger source.

**Open for impl:** rate-limit / dedupe of bursty webhook traffic
(GitHub will fire `push` per branch); minting the `RenovateRun` in
the right namespace (Platform-scoped vs. webhook-config-scoped);
whether `installation_repositories` should kick a *discovery* Run
or do something cleverer.

## API / Interface Changes

CRD additions, all additive:

- `RenovateRun.spec.target.repos []string` (optional; set only
  by the webhook receiver, not by humans).

Helm chart additions, all gated to default-off:

- `eventSink.*` block.
- `webhook.*` block (Deployment, Service, optional Ingress).

Binary additions:

- `cmd/webhook-receiver/` (new Deployment image; same
  container-image build pipeline, multi-binary layout).

## Data Model

No new persistent storage. The operator stays Kubernetes-API-only
for state.

The event sink writes to a Redis Stream the operator does not
own — its shape is part of the public contract (CloudEvents v1.0
envelope around the typed payload in INV-0006), but its storage
is the consumer's concern. Schema versioning lives in the
CloudEvents `dataschema` field for forward compatibility.

## Testing Strategy

- **Unit (`*_test.go`)** for the new pure packages: `eventsink`
  (Sink interface + no-op + redis with miniredis), `enrichment`
  (customprops + catalog parsers), webhook signature verification.
- **Controller (envtest)** for the webhook receiver → Run creation
  flow and the Run reconciler's "discovery short-circuited by
  spec.target.repos" branch.
- **e2e (kind)** for the end-to-end happy path of each new
  feature: webhook fires → Run completes; event lands in Redis
  (miniredis container in kind).
- **No new fixtures for Vault/ESO branches** (out of v0.2.x scope).
- Coverage gate stays at ≥80% per package per IMPL-0001.

## Migration / Rollout Plan

v0.2.0 is fully additive. Upgrade path from any v0.1.x install:

1. `helm upgrade renovate-operator oci://ghcr.io/donaldgifford/charts/renovate-operator --version 0.2.0`.
2. No CRD-breaking changes; existing Platforms/Scans/Runs unaffected.
3. `helm diff` will show new gated templates (webhook Deployment,
   eventSink config); none render until opted in.
4. Existing K8s-Secret credential sources keep working unchanged.
5. No behavioral changes for any v0.1.x feature. `concurrencyPolicy: Replace`
   continues to alias `Forbid` (deferred to v0.3.x).

Release sequence mirrors v0.1.0: feature-complete on `main` →
RC tags for homelab loop → v0.2.0 GA tag → docker bake + cosign
+ helm OCI via existing release.yml pipeline.

## Open Questions

Cross-cutting, beyond the per-section "Open for impl" notes:

- **IMPL-0002 sequencing.** EventSink and webhook receiver are
  largely independent; either can ship first. Webhook receiver is
  the older commitment (RFC-0001 Phase 2); EventSink has more
  upstream design work in INV-0006. Likely interleave them by
  package boundary (eventsink package → webhook package → wiring →
  e2e) rather than serializing.
- **Webhook + EventSink overlap.** A webhook-triggered Run still
  emits to the EventSink. Are there event types we need beyond
  "Run for repo X completed"? E.g., "webhook received but no Run
  fired because of dedupe."
- **OTel for the new components.** The webhook receiver and the
  event sink wrapper both warrant their own tracing spans. Reuse
  the existing `internal/observability/tracing.go` setup or
  separate exporter config?
- **Worker pod's contribution to event payload.** Confirm the
  `result.json` approach (over log-parsing) before INV-0006
  implementation locks in. Requires Renovate-side cooperation or
  a wrapper script.

## Deferred to v0.3.x or later

Captured here so the v0.2.x scope cut is transparent and the
deferred items don't fall off the radar. Each remains tracked in
its source doc; v0.3.x DESIGN doc will pick them up.

| Item | Source | Why deferred |
|---|---|---|
| **GitHub App token refresh** for Runs > ~50 min | [INV-0003](../investigation/0003-renovate-v43-github-app-auth-requires-autodiscover-not.md) | Workaround ("tighter shards" sizing guidance) is acceptable while the EventSink + webhook surface lands. Real fix is a sidecar — a meaningful infra investment best done with its own design pass. |
| **Credential-source abstraction** — `*FromVault`, `*FromESO`, etc. on `RenovatePlatform.spec.auth.*` | CLAUDE.md (INV-0003 deferred enhancement) | Users shim externally today. Demand will come from production users; homelab path doesn't need it yet. |
| **Real `Replace` concurrency-policy semantics** | [ADR-0004](../adr/0004-use-conditions-and-run-children-for-status.md) | Cancellation needs careful grace-window handling (Job propagation, worker pod termination, Cancelled condition writeback). Not blocking — `Forbid` aliasing is documented. |
| **Per-Scan credential isolation** via per-Run RBAC | [DESIGN-0001 §multi-tenancy](0001-renovate-operator-v0-1-0.md) | Multi-tenant pressure isn't here yet; current per-Run Secret naming already gives reasonable hygiene. Real RBAC isolation lands when a real tenant appears. |
| **Search API discovery optimization** | [IMPL-0001 Phase 3 note](../impl/0001-renovate-operator-v010-implementation.md) | Performance optimization, not a feature gap. |
| **Future-date renderer for `Next Run` printer column** | [INV-0001](../investigation/0001-render-renovatescan-next-run-printer-column-accurately-for.md) | Cosmetic. Today shows absolute RFC3339 — readable, just not relative. |
| **CI metrics-coverage validator for Grafana panels** | [ADR-0007](../adr/0007-observability-stack.md) | Catches dashboard rot. Worth doing; not a release-defining feature. |

## References

- [RFC-0001](../rfc/0001-build-kubebuilder-renovate-operator.md) §Phase 2 —
  webhook commitment.
- [DESIGN-0001](0001-renovate-operator-v0-1-0.md) — v0.1.0 design
  baseline.
- [INV-0006](../investigation/0006-operationalizing-renovate-operator-at-scale-dashboard-risk.md) —
  EventSink design.
- [IMPL-0001](../impl/0001-renovate-operator-v010-implementation.md) —
  v0.1.0 implementation log.
