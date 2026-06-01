---
id: DESIGN-0002
title: "Landscape: candidates for renovate-operator v0.2.x"
status: Approved
author: Donald Gifford
created: 2026-05-31
---
<!-- markdownlint-disable-file MD025 MD041 -->

# DESIGN 0002: Landscape — candidates for renovate-operator v0.2.x

**Status:** Approved (decisions captured below; implementation in DESIGN-0003 / DESIGN-0004)
**Author:** Donald Gifford
**Date:** 2026-05-31

<!--toc:start-->
- [Overview](#overview)
- [Why this doc exists](#why-this-doc-exists)
- [Candidates considered](#candidates-considered)
  - [1. EventSink](#1-eventsink)
  - [2. Internal HTTP API + consumer + datastore + UI + webhook receiver](#2-internal-http-api--consumer--datastore--ui--webhook-receiver)
  - [3. GitHub App token refresh for long Runs](#3-github-app-token-refresh-for-long-runs)
  - [4. Credential-source abstraction (Vault / ESO / cloud SM)](#4-credential-source-abstraction-vault--eso--cloud-sm)
  - [5. Real Replace concurrency-policy semantics](#5-real-replace-concurrency-policy-semantics)
  - [6. Per-Scan credential isolation via per-Run RBAC](#6-per-scan-credential-isolation-via-per-run-rbac)
  - [7. Smaller items](#7-smaller-items)
- [Decision](#decision)
- [Why the split](#why-the-split)
- [References](#references)
<!--toc:end-->

## Overview

Planning artifact that surveys every candidate for the second
release line of `renovate-operator` and assigns each to a release
(v0.2.0, v0.3.0) or to the deferred backlog. This doc does *not*
specify implementation; once a candidate is assigned to a release,
its detailed design lives in its own DESIGN doc.

**Outcome:**

- **v0.2.0** — EventSink only. Detailed design: [DESIGN-0003](0003-eventsink-for-renovate-operator-v020.md).
- **v0.3.0** — Internal HTTP API + consumer + datastore + UI + webhook receiver, all opt-in. Detailed design: [DESIGN-0004](0004-operator-http-api-consumer-datastore-ui-and-webhook-receiver.md).
- **Deferred** (no release assignment yet) — token refresh, credential-source abstraction, real `Replace` semantics, per-Scan RBAC, smaller items.

## Why this doc exists

By the end of the v0.1.x homelab loop, the v0.2.x "scope" had
accreted into a list of seven mostly-unrelated workstreams pulled
from different source docs (RFC-0001 Phase 2, INV-0003, ADR-0004,
DESIGN-0001 §multi-tenancy, INV-0006, ADR-0007, IMPL-0001 notes).
Treating them as one release would have produced a feature-grab
release with no coherent theme — exactly what RFC-0001's phased
plan was trying to avoid.

This doc does the work of:

1. Enumerating every candidate that was on the table at planning time.
2. Stating what each *is* and what release it belongs in.
3. Recording the rationale for the v0.2.x / v0.3.x split so future
   contributors can re-litigate it if their context changes.

It is then sealed (`Approved`). New ideas don't get retrofitted
into DESIGN-0002 — they get their own DESIGN doc and reference
this one if a re-scoping is warranted.

## Candidates considered

### 1. EventSink

Operator emits one structured CloudEvents-shaped event per repo
per Run through a pluggable `Sink` interface. Two implementations
ship day one: **in-memory** (Go channel; for in-process consumers
like the v0.3.0 embedded API) and **Redis Streams** (for
cross-process consumers and external integrations). A `Fanout`
wrapper lets both be active simultaneously. Optional ownership
enrichment from GitHub repo custom properties or
`catalog-info.yaml`. Sink-level Prometheus collectors cover the
"I published" contract, labeled per backend.

**Source:** [INV-0006](../investigation/0006-operationalizing-renovate-operator-at-scale-dashboard-risk.md).
**Assigned to:** v0.2.0. Detailed design: [DESIGN-0003](0003-eventsink-for-renovate-operator-v020.md).

### 2. Internal HTTP API + consumer + datastore + UI + webhook receiver

The whole "customer-facing surface" stack:

- **Internal HTTP API** (Go, shipped with the operator chart, off
  by default). Reads/writes CRDs on behalf of the external router.
  Consumes the EventSink. Provides an OpenAPI spec as the contract
  for the external router. Stays K8s-API-aware so it can do things
  `kubectl` can do (suspend a Scan, force a Run) without the
  external UI needing a kubeconfig.
- **Datastore** (probably Postgres). Persists EventSink events
  long enough to serve queries like "all PRs in my repos older
  than 30 days." The operator does not own this state today; v0.3.x
  is when it does, gated on the API being enabled.
- **External UI / API router** (bun + hono + React, separate
  repo or `web/` directory). Handles auth (OIDC), TLS, public
  exposure, and serves the React UI. Talks to the internal API
  via the generated OpenAPI client.
- **Webhook receiver**. Originally a v0.2.0 candidate (RFC-0001
  Phase 2). Bundled here because all four pieces share a theme:
  "external interactions with the operator." A webhook receiver
  shipping without the consumer/UI would emit events nothing
  ingests except the EventSink stream, which is fine but uninspiring.

**Source:** this conversation (2026-05-31), extends RFC-0001 Phase 2
and INV-0006.
**Assigned to:** v0.3.0. Detailed design: [DESIGN-0004](0004-operator-http-api-consumer-datastore-ui-and-webhook-receiver.md).

### 3. GitHub App token refresh for long Runs

GitHub App installation tokens have a ~1h TTL on github.com. Runs
longer than ~50 min hit 401s mid-scan. Two viable approaches:
tighter shards (operator-side, no extra components) or a
token-refresh sidecar/helper (worker-side, generic).

**Source:** [INV-0003](../investigation/0003-renovate-v43-github-app-auth-requires-autodiscover-not.md).
**Assigned to:** deferred. Workaround ("tighter shards" sizing
guidance, documented in [RenovateScan §workers](../usage/renovate-scan.md))
remains acceptable. Real fix is meaningful infra work that should
get its own design pass when long-Runs become a felt problem.

### 4. Credential-source abstraction (Vault / ESO / cloud SM)

Today `RenovatePlatform.spec.auth.{githubApp,token}.secretRef`
points at a K8s Secret. Vault / ESO / AWS-SM / GCP-SM users have
to shim externally. First-class `*FromVault`, `*FromESO` etc.
fields would remove that shim.

**Source:** CLAUDE.md (INV-0003 deferred enhancement).
**Assigned to:** deferred. Users shim today; demand will come from
production users who'll bring concrete shape requirements.

### 5. Real `Replace` concurrency-policy semantics

`Scan.spec.concurrencyPolicy: Replace` is accepted today but
silently aliases `Forbid`. Real semantics need careful Job
cascade-deletion + grace-window handling + `Cancelled` condition
writeback.

**Source:** [ADR-0004](../adr/0004-use-conditions-and-run-children-for-status.md).
**Assigned to:** deferred. Documented limitation; nothing breaks.

### 6. Per-Scan credential isolation via per-Run RBAC

Worker pods in Scan A's namespace can read Scan B's mirrored
Secret if cluster RBAC allows. The chart-shipped ServiceAccount
should be scoped via a per-Run Role granting `get` on only the
relevant Secret.

**Source:** [DESIGN-0001 §multi-tenancy](0001-renovate-operator-v0-1-0.md).
**Assigned to:** deferred. Multi-tenant pressure isn't here yet;
current per-Run Secret naming gives reasonable hygiene.

### 7. Smaller items

- **Search API discovery optimization** ([IMPL-0001 Phase 3 note](../impl/0001-renovate-operator-v010-implementation.md)) — use GitHub's code-search API for the `requireConfig` probe. Performance, not features.
- **Future-date renderer for `Next Run` printer column** ([INV-0001](../investigation/0001-render-renovatescan-next-run-printer-column-accurately-for.md)) — cosmetic; column shows absolute RFC3339 today.
- **CI metrics-coverage validator for Grafana panels** ([ADR-0007](../adr/0007-observability-stack.md)) — catches dashboard rot.

**Source:** various.
**Assigned to:** deferred. None are release-defining.

## Decision

| Candidate | Release | Detailed design |
|---|---|---|
| EventSink | **v0.2.0** | [DESIGN-0003](0003-eventsink-for-renovate-operator-v020.md) |
| Internal HTTP API + consumer + datastore + UI + webhook receiver | **v0.3.0** | [DESIGN-0004](0004-operator-http-api-consumer-datastore-ui-and-webhook-receiver.md) |
| GitHub App token refresh for long Runs | Deferred | (in [INV-0003](../investigation/0003-renovate-v43-github-app-auth-requires-autodiscover-not.md)) |
| Credential-source abstraction | Deferred | (in CLAUDE.md, INV-0003) |
| Real `Replace` semantics | Deferred | (in [ADR-0004](../adr/0004-use-conditions-and-run-children-for-status.md)) |
| Per-Scan credential isolation | Deferred | (in [DESIGN-0001 §multi-tenancy](0001-renovate-operator-v0-1-0.md)) |
| Search API discovery / future-date renderer / Grafana coverage CI | Deferred | (in source docs) |

## Why the split

**v0.2.0 = EventSink only.** Coherent theme: the operator
acquires a *publication contract* — it tells the outside world
what it did. Implementation is bounded (one Go package, one Redis
client, a Sink wrapper for metrics, an enrichment helper). Lands
the foundation that v0.3.0's consumer needs.

**v0.3.0 = the full customer-facing stack.** Coherent theme: the
operator gains an *interactive interface* — humans can see what
it's doing and tell it what to do, through a UI. This pulls
together:

- The HTTP API + datastore + UI as the visible part.
- The webhook receiver as the "outside world tells the operator
  to do something" path (RFC-0001 Phase 2's original intent),
  symmetric with the HTTP API's "human tells the operator to do
  something" path.
- The EventSink consumer as the data-feed for the UI.

Originally the webhook receiver was a v0.2.0 candidate (RFC-0001
Phase 2). The decision to move it: a webhook receiver shipping
without a consumer to act on the resulting events is just
"another way to make a Run happen," which `kubectl` already does.
Bundled with the UI it becomes "external systems and humans, both
through real interfaces." Worth the wait.

**Deferred items** all share one property: they're hardening or
ergonomics work that doesn't need a feature theme to ship. They
land opportunistically — either as point releases (v0.2.1, v0.3.1)
or rolled into a future v0.4.x "hardening" release if enough of
them accumulate.

## References

- [RFC-0001 §Phase 2](../rfc/0001-build-kubebuilder-renovate-operator.md) —
  original webhook commitment.
- [DESIGN-0001](0001-renovate-operator-v0-1-0.md) — v0.1.0 baseline.
- [DESIGN-0003](0003-eventsink-for-renovate-operator-v020.md) — v0.2.0 EventSink.
- [DESIGN-0004](0004-operator-http-api-consumer-datastore-ui-and-webhook-receiver.md) — v0.3.0 customer-facing stack.
- [INV-0006](../investigation/0006-operationalizing-renovate-operator-at-scale-dashboard-risk.md) —
  EventSink design.
- [INV-0003](../investigation/0003-renovate-v43-github-app-auth-requires-autodiscover-not.md) —
  source of the token-refresh and credential-source-abstraction deferrals.
