---
id: DESIGN-0003
title: "EventSink for renovate-operator v0.2.0"
status: Draft
author: Donald Gifford
created: 2026-05-31
---
<!-- markdownlint-disable-file MD025 MD041 -->

# DESIGN 0003: EventSink for renovate-operator v0.2.0

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
  - [Package layout](#package-layout)
  - [Sink interface](#sink-interface)
  - [Event type](#event-type)
  - [Redis Streams implementation](#redis-streams-implementation)
  - [Reconciler integration](#reconciler-integration)
  - [Ownership enrichment](#ownership-enrichment)
  - [Sink-level Prometheus collectors](#sink-level-prometheus-collectors)
  - [Worker → operator: how the outcome data gets back](#worker--operator-how-the-outcome-data-gets-back)
- [API / Interface Changes](#api--interface-changes)
- [Data Model](#data-model)
- [Testing Strategy](#testing-strategy)
- [Migration / Rollout Plan](#migration--rollout-plan)
- [Open Questions](#open-questions)
- [References](#references)
<!--toc:end-->

## Overview

v0.2.0 ships a single feature: the operator publishes a structured
event for every repository it processes in every Run, through a
pluggable `Sink` interface, off by default. Redis Streams is the
first implementation. Optional ownership enrichment from GitHub
repo custom properties or `catalog-info.yaml`. A small set of
platform-ops Prometheus collectors covers the operator's
"I published" contract.

This is the implementation design for the EventSink decided in
[DESIGN-0002](0002-renovate-operator-v020.md) and explored in
[INV-0006](../investigation/0006-operationalizing-renovate-operator-at-scale-dashboard-risk.md).

## Goals and Non-Goals

### Goals

- `internal/eventsink/` package with `Sink` interface + typed
  `Event` (CloudEvents v1.0 envelope) + no-op default sink.
- `internal/eventsink/redis/` impl using `redis/go-redis/v9`,
  `XADD` to a configurable stream key, MAXLEN bounded, reconnect
  with backoff, per-publish timeout.
- Run reconciler calls `Publish` once per repo at the Run's
  terminal transition. Failures **do not** fail the Run.
- Optional ownership enrichment (`internal/enrichment/customprops/`
  and `internal/enrichment/catalog/`), off by default, empty-string
  fallback when both sources miss.
- Sink-level Prometheus collectors (`up`, `published_total`,
  `publish_duration_seconds`, `dropped_total`) wired through a
  `Sink` middleware so every future impl gets them automatically.
- Chart values surface gated to default-off (`eventSink.enabled: false`,
  `eventSink.enrichment.enabled: false`).
- Backward compatible: v0.1.x install upgrading to v0.2.0 sees zero
  behavior change.

### Non-Goals

- Anything consumer-side. The operator publishes; it does not
  subscribe, query, or render. Consumer-side is DESIGN-0004 (v0.3.0)
  or any user-built tooling.
- Additional sink backends (NATS, Knative Eventing, plain HTTP
  webhook, Kafka). Future impls slot in behind the same `Sink`
  interface without reconciler changes; not v0.2.0 scope.
- Per-Platform sink config. v0.2.0 ships cluster-wide config only;
  per-Platform deferred until a real multi-tenant case appears.
- Webhook receiver. Moved to v0.3.0 (DESIGN-0004); see DESIGN-0002
  §Why the split.
- Event replay, dead-letter handling, consumer-group management.
  All consumer concerns.
- New collectors with per-repo or per-PR cardinality on the
  platform-ops Prometheus surface. Only the four sink-scoped
  collectors above land in v0.2.0.

## Background

Driving question and rationale are in
[INV-0006](../investigation/0006-operationalizing-renovate-operator-at-scale-dashboard-risk.md).
Summary of the load-bearing decisions that landed there:

- Operator emits, doesn't display. Consumer joins to ownership.
- Per-repo grain (not per-PR, not per-Run-as-a-whole).
- Sink is pluggable; first impl is Redis Streams; consumer brings
  the broker.
- Off by default for both emission and enrichment.
- Platform-ops Prometheus surface gets a *contract* check (four
  sink-scoped collectors), not consumer data.

DESIGN-0002 then scoped the v0.2.x release: only this feature
ships; webhook receiver and customer-facing UI move to v0.3.0
because they share a "customer-facing" theme that benefits from
being designed together.

## Detailed Design

### Package layout

```
internal/
  eventsink/
    sink.go              # Sink interface, Event types, no-op impl
    middleware.go        # Sink wrapper that emits Prom collectors
    middleware_test.go
    sink_test.go
    redis/
      redis.go           # Redis Streams impl
      redis_test.go      # miniredis-backed unit tests
  enrichment/
    enrichment.go        # Ownership type, Source enum, Provider interface
    enrichment_test.go
    customprops/
      customprops.go     # GitHub custom-properties provider
      customprops_test.go
    catalog/
      catalog.go         # catalog-info.yaml provider
      catalog_test.go
```

Wiring in `cmd/main.go`:

```go
sink, err := buildSink(cfg.EventSink)   // returns no-op when disabled
mw := eventsink.WithMetrics(sink, prometheus.DefaultRegisterer)
runReconciler.Sink = mw
```

### `Sink` interface

```go
package eventsink

type Event struct {
    SpecVersion string    // "1.0"
    Type        string    // "dev.fartlab.renovate.run.repo.completed"
    Source      string    // "renovate-operator/<scan-ns>/<scan-name>"
    ID          string    // "<run-uid>/<owner>/<name>"
    Time        time.Time
    Subject     string    // "<owner>/<name>"
    Data        EventData
}

type Sink interface {
    Publish(ctx context.Context, ev Event) error
    Close(ctx context.Context) error
}

// Disabled returns a no-op sink. Used when eventSink.enabled=false.
func Disabled() Sink { return noopSink{} }
```

`Publish` errors are wrapped with typed sentinels so the
middleware can label `dropped_total{reason}` cleanly:

```go
var (
    ErrTimeout    = errors.New("eventsink: publish timed out")
    ErrPayloadTooLarge = errors.New("eventsink: payload too large")
    ErrSinkDown   = errors.New("eventsink: sink unreachable")
    ErrEncoding   = errors.New("eventsink: event encoding failed")
)
```

`Close` is called once at controller shutdown so backends can
flush in-flight publishes and close client connections cleanly.
No-op for the noop sink.

### `Event` type

CloudEvents v1.0 envelope. `Event.Data` is the typed payload:

```go
type EventData struct {
    Run                 RunRef         `json:"run"`
    Repo                RepoRef        `json:"repo"`
    Ownership           Ownership      `json:"ownership"`
    Outcome             RepoOutcome    `json:"outcome"`
}

type RunRef struct {
    UID         string    `json:"uid"`
    Scan        string    `json:"scan"`
    Platform    string    `json:"platform"`
    StartedAt   time.Time `json:"started_at"`
    FinishedAt  time.Time `json:"finished_at"`
    Outcome     string    `json:"outcome"` // succeeded|failed|skipped
}

type RepoRef struct {
    Owner         string `json:"owner"`
    Name          string `json:"name"`
    DefaultBranch string `json:"default_branch"`
    PlatformType  string `json:"platform_type"` // github|forgejo
}

type Ownership struct {
    Owner     string `json:"owner"`      // "" when unset
    System    string `json:"system"`     // "" when unset
    Lifecycle string `json:"lifecycle"`  // "" when unset
    Source    string `json:"source"`     // catalog-info.yaml | custom-properties | unset
}

type RepoOutcome struct {
    PRsOpened           []PRRef   `json:"prs_opened"`
    PRsUpdated          []PRRef   `json:"prs_updated"`
    PRsClosed           []PRRef   `json:"prs_closed"`
    Vulnerabilities     []VulnRef `json:"vulnerabilities"`
    DependenciesUpdated []DepRef  `json:"dependencies_updated"`
}

type PRRef   struct { Number int; URL string; Labels []string; Automerge bool }
type VulnRef struct { AdvisoryID string; Severity string; Package string }
type DepRef  struct { Package string; From string; To string; Manager string }
```

JSON encoding goes through the CloudEvents SDK
([`cloudevents/sdk-go/v2`](https://pkg.go.dev/github.com/cloudevents/sdk-go/v2))
so any consumer that already speaks CloudEvents can decode without
custom code. The SDK is also what produces the `dataschema` URL
that future schema versions can advance.

### Redis Streams implementation

```go
package redis

import (
    "github.com/redis/go-redis/v9"
)

type Sink struct {
    client redis.UniversalClient
    stream string
    maxLen int64
    timeout time.Duration
}

func New(cfg Config) (*Sink, error) { /* ... */ }

func (s *Sink) Publish(ctx context.Context, ev eventsink.Event) error {
    ctx, cancel := context.WithTimeout(ctx, s.timeout)
    defer cancel()

    payload, err := cloudevents.MarshalJSON(ev)
    if err != nil { return fmt.Errorf("%w: %v", eventsink.ErrEncoding, err) }

    return s.client.XAdd(ctx, &redis.XAddArgs{
        Stream: s.stream,
        MaxLen: s.maxLen,
        Approx: true,
        Values: map[string]any{
            "ce":      payload,
            "subject": ev.Subject,
            "time":    ev.Time.UTC().Format(time.RFC3339Nano),
        },
    }).Err()
}
```

Connection lifecycle:

- `redis.UniversalClient` so single-node + Sentinel + Cluster all
  work behind one config flag.
- `client.Ping` on a 30s ticker drives `renovate_eventsink_up`.
- No exponential-backoff retry inside `Publish` — the timeout is
  short, the caller already has a Run-condition surface, and
  retries would just queue up failures. Reconnect happens
  client-internally per go-redis defaults.
- TLS and auth driven from chart-mounted Secret(s); never from
  values directly.

### Reconciler integration

The Run reconciler's terminal-transition path becomes:

```go
// pseudo-code in renovaterun_controller.go observeJob
func (r *Runs) finish(ctx context.Context, run *Run, repos []RepoResult) error {
    for _, repo := range repos {
        ev := buildEvent(run, repo)
        if err := r.Sink.Publish(ctx, ev); err != nil {
            log.FromContext(ctx).Error(err, "eventsink publish failed",
                "repo", repo.NameWithOwner)
            conditions.MarkFalse(&run.Status.Conditions,
                conditions.TypeEventsPublished, conditions.ReasonSinkError,
                err.Error(), run.Generation)
        }
    }
    // ... existing terminal-condition logic continues unchanged
    return nil
}
```

Critical: **a sink failure never fails the Run.** Renovate did its
work; the operator failing to publish a notification about that
work is observational. The new `EventsPublished` condition is the
diagnostic surface; the `dropped_total` counter is the alerting
surface.

### Ownership enrichment

```go
package enrichment

type Ownership struct {
    Owner, System, Lifecycle, Source string
}

type Provider interface {
    Lookup(ctx context.Context, owner, name string) (Ownership, bool, error)
}

// Chain returns the first provider hit (or empty Ownership + Source="unset"
// when all providers miss).
func Chain(providers ...Provider) Provider { /* ... */ }
```

Two providers ship in v0.2.0:

- `customprops.Provider` calls
  `GET /repos/{owner}/{repo}/properties/values` on the GitHub
  client (App-auth or PAT). Maps the configured property names
  (default: `owner`, `system`, `lifecycle`) to `Ownership`.
  Returns `(_, false, nil)` on 404 or when no recognized
  properties are present.
- `catalog.Provider` does `GET /repos/{owner}/{repo}/contents/catalog-info.yaml`
  via the platform client, parses with `sigs.k8s.io/yaml`, pulls
  `spec.owner`, `spec.system`, `spec.lifecycle`. Returns
  `(_, false, nil)` on 404 or parse error.

Wiring: enrichment runs at Discovery time, results are cached on
the Run snapshot so the terminal-transition emission doesn't
re-fetch. Cache shape: `map[RepoNameWithOwner]Ownership` on the
in-memory Run state.

Cost: 1-2 extra API calls per repo per Run when enrichment is on.
At 1K repos nightly with a 4500 req/hr GitHub App budget, that's
1-2K extra reqs vs. 108K available — well clear. Enrichment is
off by default precisely because not every install needs it.

Forgejo: `customProperties` provider returns
`(_, false, nil)` always. `catalog` provider works the same way
since it's just a Contents API call.

### Sink-level Prometheus collectors

Registered in `internal/observability/metrics.go` alongside the
existing operator collectors:

| Metric | Type | Labels | Notes |
|---|---|---|---|
| `renovate_eventsink_up` | Gauge | `{sink}` | 1 after successful publish or Ping; 0 after either fails. Set in the middleware. |
| `renovate_eventsink_published_total` | Counter | `{sink, result}` | `result=success\|failure`. Failure rate over throughput is the SLO. |
| `renovate_eventsink_publish_duration_seconds` | Histogram | `{sink}` | Default buckets + native histogram. |
| `renovate_eventsink_dropped_total` | Counter | `{sink, reason}` | `reason ∈ {timeout, sink_down, payload_too_large, encoding}`. |

The `Sink` middleware (`internal/eventsink/middleware.go`) wraps
any `Sink` impl and emits these around `Publish`. Future sink impls
get the metrics for free. Labels are bounded — `sink` is one
value in v0.2.0 (`redis`), `result` has two, `reason` has four.

PrometheusRule additions in `dist/chart/templates/extra/prometheusrule.yaml`:

- `RenovateEventSinkDown` — alerts on `renovate_eventsink_up == 0`
  for 5m. Page-worthy.
- `RenovateEventSinkFailureRate` — alerts on
  `rate(renovate_eventsink_published_total{result="failure"}[5m]) /
   rate(renovate_eventsink_published_total[5m]) > 0.05` for 10m.
  Warning, not page.

Grafana dashboard panel additions in `contrib/grafana/dashboards/operator.json`:
sink up, throughput, failure rate, p99 publish latency, drop reasons stacked bar.

### Worker → operator: how the outcome data gets back

The operator does not see Renovate's per-repo decisions today;
the worker pod runs the CLI and the operator only sees the Job's
exit code. We need a way to surface per-repo outcomes.

Two options:

**A. Parse the worker's structured JSON logs in the reconciler.**
Pro: no worker-side change. Con: brittle to Renovate's log
format changes; needs to grep across N pods' logs which the
operator may not have access to in all RBAC setups.

**B. Worker writes `result.json` to a known path; the operator
reads it after Job completion.** Cleaner contract, requires a
small wrapper script in the worker image (or a Renovate
post-run hook). Path: a per-shard emptyDir volume the
operator's status-collector init pattern can read via the Job's
pod-template terminationMessagePath, OR a ConfigMap the worker
writes to via the K8s API.

**Lean toward B with terminationMessagePath.** Renovate's CLI
already emits a structured summary at the end of its run; a
trivial wrapper (or a `prCommands` hook) can write the
relevant fields to `/dev/termination-log` as JSON. K8s preserves
that on Pod status indefinitely after termination. No extra
RBAC, no ConfigMap proliferation, no log scraping.

The exact wrapper script and JSON shape are the first
implementation question to nail down. Whatever the answer, the
event payload's `Outcome` struct is the *operator-internal* shape;
the wrapper's format is a private contract between the worker
image and the Run reconciler.

## API / Interface Changes

CRD: **no changes**. Run.status gains a new condition type
(`EventsPublished`) but conditions are an open set — not a CRD
schema change.

Helm chart additions (all gated to default-off):

```yaml
# values.yaml additions
eventSink:
  enabled: false                              # opt-in
  type: redis                                 # only "redis" in v0.2.0
  redis:
    addr: ""                                  # required when enabled
    db: 0
    stream: "renovate.events"
    maxLen: 100000                            # XADD MAXLEN ~ ; 0 = unbounded
    timeout: "5s"
    tls:
      enabled: false
      caSecretRef: { name: "", key: "" }
    auth:
      secretRef: { name: "", key: "" }        # password or full URL
  enrichment:
    enabled: false
    sources: [customProperties, catalogInfo]  # ordered; first hit wins
    customProperties:
      keys:
        owner: "owner"
        system: "system"
        lifecycle: "lifecycle"
    catalogInfo:
      path: "catalog-info.yaml"
```

Template guard: when `eventSink.enabled=true && eventSink.redis.addr==""`,
fail-fast at template-render time (same pattern as the
`defaultScan` guard).

No new binaries. EventSink lives inside the existing manager
binary.

## Data Model

No new operator-owned persistent storage. The Sink writes to a
Redis Stream the operator does not own; storage lifecycle is the
consumer's concern (the operator only sets `MAXLEN ~` for soft
backpressure).

Schema versioning for the event payload lives in the CloudEvents
`dataschema` field — initial version `https://fartlab.dev/schemas/renovate-events/v1/run-repo-completed.json`.
A schema file is published at that URL (or as part of the chart's
extra/ tree to start) so consumers can validate.

Backwards-incompatible payload changes bump the `dataschema`
version; the operator continues emitting the old version until
the next major. Forward-compatible additions (new optional
fields) don't bump.

## Testing Strategy

- **Unit (`*_test.go`)**:
  - `eventsink/sink_test.go`: no-op sink contract.
  - `eventsink/middleware_test.go`: every Sink impl wrapped by
    `WithMetrics` emits the four collectors on every code path.
  - `eventsink/redis/redis_test.go`: miniredis-backed; covers
    `XADD` happy path, MAXLEN trim, timeout error mapping, TLS
    config wiring, Ping-driven `up` gauge.
  - `enrichment/customprops/customprops_test.go`: httptest fake
    GitHub; covers properties happy path, 404, missing-keys,
    PAT vs. App auth.
  - `enrichment/catalog/catalog_test.go`: covers `contents/`
    happy path, 404, malformed YAML, missing fields.
- **Controller (envtest)**:
  - New spec in `internal/controller/renovaterun_controller_test.go`:
    a Run completes, `EventsPublished` condition is set, the
    configured Sink saw N events with the expected shape.
  - Sink-failure spec: Run reconciler installs a faulting Sink,
    Run still reaches `Succeeded`, `EventsPublished=False` with
    reason `SinkError`, `dropped_total{reason}` counter
    increments.
- **e2e (kind)**:
  - New `test/e2e/eventsink_test.go`: kind cluster with miniredis
    deployed, EventSink enabled, fire a Scan, assert events
    land in the Redis stream with the expected CloudEvents
    envelope and `subject` field.
- **No backend-specific fixtures** for Vault/ESO/etc. — those
  paths don't exist in v0.2.0.
- Coverage gate stays at ≥80% per package per IMPL-0001.

## Migration / Rollout Plan

v0.2.0 release is fully additive. Upgrade path from any v0.1.x install:

1. `helm upgrade renovate-operator oci://ghcr.io/donaldgifford/charts/renovate-operator --version 0.2.0`.
2. No CRD-breaking changes; existing Platforms/Scans/Runs unaffected.
3. `helm diff` shows the new `eventSink.*` value defaults
   (`enabled: false`) and the optional PrometheusRule additions
   (gated on `eventSink.enabled`).
4. No behavioral changes for v0.1.x feature paths. The Run
   reconciler's terminal path gains a Sink call, but the default
   Sink is a no-op.
5. To opt in:
   ```bash
   helm upgrade renovate-operator ... \
     --set eventSink.enabled=true \
     --set eventSink.redis.addr=redis.shared.svc.cluster.local:6379
   ```
6. To opt in to enrichment, add `--set eventSink.enrichment.enabled=true`.

Release sequence mirrors v0.1.0: feature-complete on `main` →
RC tags for homelab loop validation against a real Redis (already
deployed for other workloads in the homelab cluster) → v0.2.0 GA
tag → docker bake + cosign + helm OCI via existing release.yml.

Acceptance criteria for v0.2.0 GA:

- All four sink-level collectors visible in `/metrics` when Sink
  enabled, absent (zero series) when disabled.
- A Run completing against a real repo produces a CloudEvents-
  shaped entry in the configured Redis Stream that decodes
  cleanly with the official SDK.
- Enrichment off → all `ownership.*` fields empty + `source="unset"`.
- Enrichment on → at least one repo's events carry a non-empty
  `ownership` block sourced from `catalog-info.yaml` in the
  homelab test repos.

## Open Questions

- **Worker → operator outcome data shape.** Pick option A or B
  from §"Worker → operator: how the outcome data gets back"
  before implementation. Strong lean to B with
  `terminationMessagePath`.
- **`dataschema` hosting.** Bundle in the chart (`extra/schemas/`)?
  Publish to a GitHub Pages site under the repo? Both are fine;
  pick one before GA so the URL doesn't change.
- **Stream entry shape.** `ce` field for the full CloudEvent
  payload + a couple of mirrored top-level fields (`subject`,
  `time`) for cheap consumer filtering. Confirm this is what
  the v0.3.0 consumer prefers (DESIGN-0004 should call out the
  read pattern).
- **Per-Platform vs. cluster-wide sink config.** v0.2.0 ships
  cluster-wide only. If a real multi-tenant case appears during
  homelab loop, may need to retrofit per-Platform; lean toward
  not preemptively designing for it.
- **Forgejo `customProperties` parity.** Forgejo doesn't have an
  equivalent concept today. Document the silent-fallback; revisit
  if Forgejo ships one.

## References

- [DESIGN-0002](0002-renovate-operator-v020.md) — v0.2.x scope
  decision; places this design in the larger release plan.
- [INV-0006](../investigation/0006-operationalizing-renovate-operator-at-scale-dashboard-risk.md) —
  origin of the emit-only architecture and audience separation.
- CloudEvents v1.0 spec —
  [`cloudevents/sdk-go/v2`](https://pkg.go.dev/github.com/cloudevents/sdk-go/v2).
- Redis Streams — `XADD` semantics, `MAXLEN ~`.
- GitHub REST:
  [`GET /repos/{owner}/{repo}/properties/values`](https://docs.github.com/en/rest/repos/custom-properties).
- Backstage `catalog-info.yaml` schema.
- [DESIGN-0004](0004-operator-http-api-consumer-datastore-ui-and-webhook-receiver.md) —
  v0.3.0 consumer that reads this stream.
