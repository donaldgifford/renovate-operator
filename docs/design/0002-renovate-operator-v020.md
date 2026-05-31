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
  - [3. Token lifecycle for long Runs (INV-0003 follow-up)](#3-token-lifecycle-for-long-runs-inv-0003-follow-up)
  - [4. Credential-source abstraction](#4-credential-source-abstraction)
  - [5. `Replace` concurrency policy](#5-replace-concurrency-policy)
  - [6. Per-Scan credential isolation](#6-per-scan-credential-isolation)
  - [7. Smaller items](#7-smaller-items)
- [API / Interface Changes](#api--interface-changes)
- [Data Model](#data-model)
- [Testing Strategy](#testing-strategy)
- [Migration / Rollout Plan](#migration--rollout-plan)
- [Open Questions](#open-questions)
- [References](#references)
<!--toc:end-->

## Overview

Scope bracket for the second release of `renovate-operator`. v0.2.0
turns the operator from "schedules Runs and emits ops metrics" into
"schedules Runs, *responds to events*, and tells downstream systems
what happened." Three load-bearing additions: a pluggable
**EventSink** for per-repo Run outcomes (Redis first), a **webhook
receiver** for out-of-band on-demand Runs, and the **operational
hardening** the homelab loop turned up — token refresh for long
Runs, alternative credential sources, real `Replace` semantics, and
per-Scan credential isolation.

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
- **GitHub App installation-token refresh** for Runs that exceed
  the ~50-minute safe window ([INV-0003](../investigation/0003-renovate-v43-github-app-auth-requires-autodiscover-not.md)).
- **Pluggable credential sources** for `RenovatePlatform.spec.auth.*`
  beyond raw K8s Secrets — at minimum Vault and ESO (External
  Secrets Operator) reference flows.
- **Real `Replace` concurrency policy** semantics. Today it aliases
  `Forbid` ([ADR-0004](../adr/0004-use-conditions-and-run-children-for-status.md)).
- **Per-Scan credential isolation** path documented and (where
  feasible) wired so a Scan's worker pods cannot read another
  Scan's mirrored Secret ([DESIGN-0001 §multi-tenancy](0001-renovate-operator-v0-1-0.md)).
- Backward compatible: every new feature is opt-in. A v0.1.x
  install on upgrade sees zero behavior change.

### Non-Goals

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

2. **Operational hardening** — several v0.1.x design choices need
   real follow-through: GitHub App tokens expire at ~1h, the
   credential surface is K8s-Secret-only (Vault/ESO users have to
   shim externally), `Replace` aliases `Forbid`, and multi-tenant
   credential isolation is documented but not enforced.

Plus the long-standing RFC-0001 Phase 2 commitment: a webhook
receiver for on-demand Runs.

This design pulls all of that into a single release scope.

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

**Open for impl:** rate-limit / dedupe of bursty webhook traffic
(GitHub will fire `push` per branch); minting the `RenovateRun` in
the right namespace (Platform-scoped vs. webhook-config-scoped);
whether `installation_repositories` should kick a *discovery* Run
or do something cleverer.

### 3. Token lifecycle for long Runs (INV-0003 follow-up)

GitHub App installation tokens have a ~1h TTL on github.com. The
operator mints once at Run start (per INV-0003 fix); Runs longer
than ~50 min hit 401s mid-scan. Two viable approaches:

**A. Tighter shards (operator-side, no extra components).** Cap
shard wall-time by lowering `reposPerWorker` so each worker pod
completes well under 50 min. Document the math in the Scan
sizing table. Trivial — works today, but fragile against repos
with slow networks or huge histories.

**B. Token-refresh helper (worker-side, generic).** A tiny
sidecar or in-image helper that the operator launches alongside
the Renovate container. It holds the App PEM, mints fresh
installation tokens on a ticker, and writes them to a shared
emptyDir volume; Renovate reads `RENOVATE_TOKEN_FILE` (which it
already supports) and re-reads on token expiry.

**Lean toward B**: it's a one-shot infra investment that
permanently removes the wall-time ceiling. A is a workaround.
Cost: another container in the worker pod, +PEM mount to one
more place, more moving parts to test.

**Open for impl:** sidecar vs. init-container wake-loop; whether
to fold the PEM mount into the existing credential-mount path or
add a second; how to detect token-staleness from Renovate's
output and surface it as a Run condition.

### 4. Credential-source abstraction

Today `RenovatePlatform.spec.auth.{githubApp,token}.secretRef`
points at a K8s Secret. Vault and ESO users have to shim
externally (sync to a K8s Secret, then point the operator). v0.2.x
adds first-class alternatives:

```yaml
# Today (v0.1.x):
auth:
  githubApp:
    appID: 123
    installationID: 456
    privateKeyRef: { name: gh-app-key, key: private-key.pem }

# v0.2.x — adds alternatives:
auth:
  githubApp:
    appID: 123
    installationID: 456
    privateKeyFromVault: { mount: kv, path: renovate/gh-app, key: pem }

auth:
  githubApp:
    appID: 123
    installationID: 456
    privateKeyFromESO:
      externalSecretRef: { name: gh-app-pem }   # ESO-managed
```

CRD validation enforces exactly one source per credential field
(CEL `oneOf` rule). The reconciler dispatches to a
`CredentialSource` resolver per type; the existing K8s-Secret
resolver becomes one impl of many.

Initial set: K8s Secret (existing), **Vault KV** (HashiCorp Vault
via approle or kube-auth), **ESO reference** (External Secrets
Operator — operator watches the ESO `ExternalSecret`'s synced K8s
Secret rather than the source). AWS Secrets Manager / GCP Secret
Manager left for v0.3.x.

**Open for impl:** Vault auth method (approle vs.
kubernetes-service-account JWT); whether ESO mode just wraps the
existing Secret resolver with an `ExternalSecret`-aware status
gate, or does something more direct; rotation semantics for
Vault-sourced creds (in-flight Runs use the snapshotted token;
next Run picks up the new value).

### 5. `Replace` concurrency policy

`Scan.spec.concurrencyPolicy: Replace` is accepted today but
behaves as `Forbid` ([ADR-0004](../adr/0004-use-conditions-and-run-children-for-status.md)).
Real semantics:

- If a non-terminal owned Run exists at fire time, the Scan
  reconciler:
  1. Sends `kubectl delete` to the active Run (cascade-deletes
     Job + ConfigMap + mirrored Secret via owner refs).
  2. Waits up to a bounded grace (default 30s) for terminal
     condition or active count to hit 0.
  3. Creates the new Run.
- On grace timeout, the Scan logs a warning and skips the new
  fire-time (same as `Forbid`). Surfaces as a Scan condition.

**Open for impl:** does "delete" propagate to a half-finished
Job's running pods cleanly (the Job controller respects
`PropagationPolicy=Foreground`); should the active Run get a
chance to write a terminal "Cancelled" reason before deletion;
new test cases in the envtest suite.

### 6. Per-Scan credential isolation

v0.1.x mirrors the operator-namespace Secret into the Scan
namespace verbatim. Worker pods in Scan A's namespace can read
Scan B's mirrored Secret if RBAC allows. DESIGN-0001 §multi-tenancy
acknowledged this and deferred to v0.2+.

Two layers in v0.2.x:

- **Per-Run Secret names** (already done in v0.1.x — the
  mirrored Secret is named after the Run UID). Deletion follows
  Run GC.
- **Namespace-scoped RBAC for worker pods**: the chart-shipped
  `RenovateScan` ServiceAccount gets a Role granting `get` on
  just the per-Run Secret name pattern, not `secrets/*`. Workers
  run as that ServiceAccount. Cross-Scan read is blocked at the
  RBAC layer even when Runs cohabit a namespace.

**Open for impl:** whether to generate the Role/RoleBinding per
Run (cleanest) or per Scan (cheaper); how this interacts with
deployments that have ExternalSecret already managing the worker
SA token; making sure `kubectl logs` from operators-with-cluster-RBAC
still works.

### 7. Smaller items

- **Search API discovery optimization** ([IMPL-0001 Phase 3
  note](../impl/0001-renovate-operator-v010-implementation.md)).
  Use GitHub's code-search API for the `requireConfig` probe
  when available — one API call to find all repos with
  `renovate.json` vs. N calls. Forgejo equivalent doesn't exist;
  unaffected.
- **Future-date renderer for `Next Run` printer column**
  ([INV-0001](../investigation/0001-render-renovatescan-next-run-printer-column-accurately-for.md)).
  Today the column is `type=string` (fixed) showing the absolute
  RFC3339 timestamp. v0.2.x can ship a kubectl-friendly relative
  form once we decide whether to do it in the controller (status
  field) or via additional printer columns.
- **CI metrics-coverage Grafana validator**
  ([ADR-0007](../adr/0007-observability-stack.md)). Extend the
  existing `make metrics-coverage-lint` (which checks chart-side
  PrometheusRule + `contrib/`) to also assert every metric
  referenced in `contrib/grafana/dashboards/*.json` is defined in
  `internal/observability/metrics.go`. Catches dashboard rot.

## API / Interface Changes

CRD additions, all additive:

- `RenovatePlatform.spec.auth.githubApp.privateKeyFromVault`
  (struct, optional).
- `RenovatePlatform.spec.auth.githubApp.privateKeyFromESO`
  (struct, optional).
- `RenovatePlatform.spec.auth.token.tokenFromVault`,
  `tokenFromESO` (struct, optional).
- CEL `oneOf` validation across the three sources per credential
  field.
- `RenovateRun.spec.target.repos []string` (optional; set only
  by the webhook receiver, not by humans).
- `RenovateScan.status.conditions` gains `ReplaceTimedOut`
  reason on `Replace` policy grace exhaustion.

Helm chart additions, all gated to default-off:

- `eventSink.*` block.
- `webhook.*` block (Deployment, Service, optional Ingress).
- `defaultScan.workerServiceAccount` and `workerRBAC` blocks for
  per-Scan credential isolation knobs.

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
  (customprops + catalog parsers).
- **Controller (envtest)** for `Replace` policy semantics, webhook
  receiver → Run creation flow, credential-source resolution
  branching, per-Run RBAC creation.
- **e2e (kind)** for the end-to-end happy path of each new
  feature: webhook fires → Run completes; event lands in Redis
  (miniredis container in kind); per-Run Secret RBAC denies
  cross-Scan reads.
- **No new fixtures for the Vault/ESO branches** in v0.2.x — gate
  those on the existing platform-client test pattern with httptest
  fakes. Real backends covered by `test/manual/README.md` updates.
- Coverage gate stays at ≥80% per package per IMPL-0001.

## Migration / Rollout Plan

v0.2.0 is fully additive. Upgrade path from any v0.1.x install:

1. `helm upgrade renovate-operator oci://ghcr.io/donaldgifford/charts/renovate-operator --version 0.2.0`.
2. No CRD-breaking changes; existing Platforms/Scans/Runs unaffected.
3. `helm diff` will show new gated templates (webhook Deployment,
   eventSink config, worker RBAC); none render until opted in.
4. Existing K8s-Secret credential sources keep working — the new
   `*FromVault` / `*FromESO` fields are alternatives, not
   replacements.
5. `concurrencyPolicy: Replace` users will see actual replace
   semantics, not the silent `Forbid` fallback. If anyone is
   relying on the existing (buggy) behavior, the upgrade notes
   need to call this out as the one *behavioral* change.

Release sequence mirrors v0.1.0: feature-complete on `main` →
RC tags for homelab loop → v0.2.0 GA tag → docker bake + cosign
+ helm OCI via existing release.yml pipeline.

## Open Questions

Cross-cutting, beyond the per-section "Open for impl" notes:

- **IMPL-0002 sequencing.** Which of (1)–(6) above ships first?
  Webhook + EventSink are the visible features; the hardening
  items (3)–(6) are debt-reducing. Suggest landing the hardening
  first so the visible features get built on a cleaner foundation.
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
- **`Replace` semantics around credential rotation.** If a Scan
  is configured with `Replace`, an in-flight Run gets killed
  when the next fire-time arrives even if it just minted a fresh
  installation token. Probably fine, but worth documenting.

## References

- [RFC-0001](../rfc/0001-build-kubebuilder-renovate-operator.md) §Phase 2 —
  webhook commitment.
- [DESIGN-0001](0001-renovate-operator-v0-1-0.md) — v0.1.0 design
  baseline; §multi-tenancy and §Future architecture: state DB are
  load-bearing for v0.2.x scope.
- [ADR-0004](../adr/0004-use-conditions-and-run-children-for-status.md) —
  `Replace`-aliases-`Forbid` baseline; v0.2.x makes `Replace`
  real.
- [ADR-0007](../adr/0007-observability-stack.md) — observability
  baseline; v0.2.x extends with sink-level collectors and
  considers Grafana-coverage CI.
- [INV-0001](../investigation/0001-render-renovatescan-next-run-printer-column-accurately-for.md) —
  future-date rendering deferred to v0.2.x.
- [INV-0003](../investigation/0003-renovate-v43-github-app-auth-requires-autodiscover-not.md) —
  GitHub App auth fix; v0.2.x token-refresh follow-up.
- [INV-0006](../investigation/0006-operationalizing-renovate-operator-at-scale-dashboard-risk.md) —
  EventSink design.
- [IMPL-0001](../impl/0001-renovate-operator-v010-implementation.md) —
  v0.1.0 implementation log; Phase 3.4 deprecation/search-API
  note carried forward.
