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
  - [In-memory implementation](#in-memory-implementation)
  - [Redis Streams implementation](#redis-streams-implementation)
  - [Fanout: publishing to multiple sinks](#fanout-publishing-to-multiple-sinks)
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

v0.2.0 ships the EventSink: the operator publishes a structured
event for every repository it processes in every Run, through a
pluggable `Sink` interface, off by default. Two implementations
ship on day one — **in-memory** (Go channel, for in-process
consumers) and **Redis Streams** (`go-redis/v9`, for cross-process
consumers and external integrations) — combined via a Fanout
wrapper so both can be active at once. Optional ownership
enrichment from GitHub repo custom properties or `catalog-info.yaml`.
A small set of platform-ops Prometheus collectors covers the
operator's "I published" contract.

This is the implementation design for the EventSink decided in
[DESIGN-0002](0002-renovate-operator-v020.md) and explored in
[INV-0006](../investigation/0006-operationalizing-renovate-operator-at-scale-dashboard-risk.md).

## Goals and Non-Goals

### Goals

- `internal/eventsink/` package with `Sink` interface + typed
  `Event` (CloudEvents v1.0 envelope) + no-op default sink.
- **Two production implementations on day one:**
  - `internal/eventsink/memory/` — bounded Go channel; the
    in-process consumer (DESIGN-0004's API binary, when embedded)
    reads via a `Subscribe()` method.
  - `internal/eventsink/redis/` — `go-redis/v9`, `XADD` to a
    configurable stream key, MAXLEN bounded, reconnect with
    backoff, per-publish timeout.
- `Fanout` wrapper that publishes to N sinks in sequence. Used
  when both backends are enabled (in-memory for the bundled UI
  consumer, Redis for external integration, both at once).
- Run reconciler calls `Publish` once per repo at the Run's
  terminal transition. Failures **do not** fail the Run.
- Optional ownership enrichment (`internal/enrichment/customprops/`
  and `internal/enrichment/catalog/`), off by default, empty-string
  fallback when both sources miss.
- Sink-level Prometheus collectors (`up`, `published_total`,
  `publish_duration_seconds`, `dropped_total`) wired through a
  `Sink` middleware so every impl gets them automatically. The
  `sink` label distinguishes `memory` from `redis`.
- Chart values surface gated to default-off; each backend
  independently togglable.
- Backward compatible: v0.1.x install upgrading to v0.2.0 sees zero
  behavior change.

### Non-Goals

- Anything consumer-side beyond the in-memory `Subscribe` channel.
  The actual consumer goroutine that reads from `Subscribe` and
  writes to Postgres lives in DESIGN-0004 (v0.3.0).
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
DESIGN-0002 then scoped v0.2.x and assigned EventSink to v0.2.0
while moving the consumer/API/UI/webhook stack to v0.3.0.

A late refinement in conversation: with the v0.3.0 API in-cluster
anyway, requiring Redis for the bundled-UI use case is unnecessary
infrastructure overhead. But the third-party integration story
("send events to my Slack bot / audit log / Kafka pipeline") still
matters. The resolution: **ship both backends day one** so the
deployment shape can be either:

- **In-memory only** (bundled UI use case) — no external infra
  beyond Postgres. API binary runs as a goroutine in the manager
  process and reads from the in-memory Sink directly.
- **Redis only** (external integration use case) — operator emits
  to Redis; external consumers read the stream; in-process consumer
  reads Redis too if API is enabled.
- **Both** — Fanout to in-memory (cheap, for the embedded API) and
  Redis (for external systems). Operator publishes once per
  backend; both consumers get the same payload.

The Sink interface stays as the abstraction so future backends
(NATS, webhook, Kafka) slot in without reconciler change.

## Detailed Design

### Package layout

```
internal/
  eventsink/
    sink.go              # Sink interface, Event types, no-op impl, sentinels
    fanout.go            # Fanout wrapper (multi-sink publish)
    fanout_test.go
    middleware.go        # Prometheus-emitting Sink wrapper
    middleware_test.go
    sink_test.go
    memory/
      memory.go          # In-memory (channel) impl + Subscribe()
      memory_test.go
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
// buildSink returns the configured Sink. May be:
//   - noop sink (eventSink.enabled=false, no backends enabled)
//   - single backend (memory or redis enabled, the other disabled)
//   - Fanout over multiple backends (both enabled)
// Each backend is wrapped in WithMetrics so the {sink} label
// distinguishes contributions.
sink, err := buildSink(cfg.EventSink)
runReconciler.Sink = sink

// When memory backend is enabled, also expose its Subscribe channel
// to the embedded consumer (if api.deployment.mode=embedded).
if cfg.EventSink.Memory.Enabled && cfg.API.Embedded {
    consumer := api.NewConsumer(memSink.Subscribe(), db)
    go consumer.Run(ctx)
}
```

### Sink interface

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

// Disabled returns a no-op sink. Used when no backend is enabled.
func Disabled() Sink { return noopSink{} }
```

`Publish` errors are wrapped with typed sentinels so the
middleware can label `dropped_total{reason}` cleanly:

```go
var (
    ErrTimeout         = errors.New("eventsink: publish timed out")
    ErrPayloadTooLarge = errors.New("eventsink: payload too large")
    ErrSinkDown        = errors.New("eventsink: sink unreachable")
    ErrSinkFull        = errors.New("eventsink: sink buffer full") // memory backend
    ErrEncoding        = errors.New("eventsink: event encoding failed")
)
```

`Close` is called once at controller shutdown so backends can
flush in-flight publishes and close client connections cleanly.

### Event type

CloudEvents v1.0 envelope. `Event.Data` is the typed payload:

```go
type EventData struct {
    Run       RunRef      `json:"run"`
    Repo      RepoRef     `json:"repo"`
    Ownership Ownership   `json:"ownership"`
    Outcome   RepoOutcome `json:"outcome"`
}

type RunRef struct {
    UID        string    `json:"uid"`
    Scan       string    `json:"scan"`
    Platform   string    `json:"platform"`
    StartedAt  time.Time `json:"started_at"`
    FinishedAt time.Time `json:"finished_at"`
    Outcome    string    `json:"outcome"` // succeeded|failed|skipped
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
custom code. Both the in-memory and Redis backends carry the same
encoded payload.

### In-memory implementation

```go
package memory

type Sink struct {
    ch       chan eventsink.Event
    capacity int
    closed   atomic.Bool
}

func New(capacity int) *Sink {
    return &Sink{
        ch:       make(chan eventsink.Event, capacity),
        capacity: capacity,
    }
}

// Publish drops the event if the buffer is full. The caller
// (Run reconciler) sees ErrSinkFull, surfaces it as a Run
// condition + dropped_total{reason="sink_full"} counter, and
// moves on. Backpressure into the reconciler is by design rejected
// — we never let a slow consumer wedge reconciliation.
func (s *Sink) Publish(ctx context.Context, ev eventsink.Event) error {
    if s.closed.Load() {
        return eventsink.ErrSinkDown
    }
    select {
    case s.ch <- ev:
        return nil
    case <-ctx.Done():
        return ctx.Err()
    default:
        return eventsink.ErrSinkFull
    }
}

func (s *Sink) Subscribe() <-chan eventsink.Event { return s.ch }

func (s *Sink) Close(ctx context.Context) error {
    if s.closed.CompareAndSwap(false, true) {
        close(s.ch)
    }
    return nil
}
```

**Capacity:** default 1024. Configurable via chart values. Sized
large enough that a Run completing 50 repos and the consumer
being briefly busy doesn't cause drops; small enough that a stuck
consumer doesn't pin unbounded memory.

**When to use:** v0.3.0 embedded-API mode. The API binary runs as
a goroutine in the manager process and reads from `Subscribe()`.
Single-consumer; the channel is point-to-point.

**Not for:** cross-process delivery (use Redis), external systems
(use Redis), or scenarios where the operator might restart while
the consumer is offline (events in the buffer are lost — use
Redis if durability matters).

### Redis Streams implementation

```go
package redis

import "github.com/redis/go-redis/v9"

type Sink struct {
    client  redis.UniversalClient
    stream  string
    maxLen  int64
    timeout time.Duration
}

func New(cfg Config) (*Sink, error) { /* ... */ }

func (s *Sink) Publish(ctx context.Context, ev eventsink.Event) error {
    ctx, cancel := context.WithTimeout(ctx, s.timeout)
    defer cancel()

    payload, err := cloudevents.MarshalJSON(ev)
    if err != nil {
        return fmt.Errorf("%w: %v", eventsink.ErrEncoding, err)
    }

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
- `client.Ping` on a 30s ticker drives `renovate_eventsink_up{sink="redis"}`.
- No exponential-backoff retry inside `Publish` — the timeout is
  short, the caller already has a Run-condition surface, and
  retries would just queue up failures. Reconnect happens
  client-internally per go-redis defaults.
- TLS and auth driven from chart-mounted Secret(s); never from
  values directly.

**When to use:** cross-process consumers (separate API
Deployment), external integration (Slack bots, audit logs, Kafka
connectors), or when durability across operator restarts matters.

### Fanout: publishing to multiple sinks

```go
package eventsink

// Fanout publishes to multiple sinks. Errors from each are recorded
// in the returned MultiError but do not block subsequent sinks —
// every Publish attempts every backend.
type Fanout struct {
    sinks []Sink
}

func NewFanout(sinks ...Sink) *Fanout { return &Fanout{sinks: sinks} }

func (f *Fanout) Publish(ctx context.Context, ev Event) error {
    var errs []error
    for _, s := range f.sinks {
        if err := s.Publish(ctx, ev); err != nil {
            errs = append(errs, err)
        }
    }
    return errors.Join(errs...)
}

func (f *Fanout) Close(ctx context.Context) error {
    var errs []error
    for _, s := range f.sinks {
        if err := s.Close(ctx); err != nil { errs = append(errs, err) }
    }
    return errors.Join(errs...)
}
```

Wiring decision tree in `buildSink`:

```
eventSink.enabled = false                              → Disabled (noop)
eventSink.enabled = true, only memory                  → memory.New(...)
eventSink.enabled = true, only redis                   → redis.New(...)
eventSink.enabled = true, both memory + redis enabled  → NewFanout(memory, redis)
```

Each backend, before joining the Fanout, is wrapped in
`WithMetrics(sink, sinkLabel)` so the `sink` Prometheus label
distinguishes contributions (`{sink="memory"}` vs `{sink="redis"}`).

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
surface. Partial-fanout failures (memory succeeded, redis failed)
surface as a `MultiError` and increment `dropped_total{sink="redis", reason="..."}`
specifically — the memory `published_total` counter still ticks.

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
re-fetch.

Cost: 1-2 extra API calls per repo per Run when enrichment is on.
At 1K repos nightly with a 4500 req/hr GitHub App budget, 1-2K
extra reqs vs. 108K available — well clear.

Forgejo: `customProperties` provider returns `(_, false, nil)`
always. `catalog` provider works the same way since it's just a
Contents API call.

### Sink-level Prometheus collectors

Registered in `internal/observability/metrics.go` alongside the
existing operator collectors:

| Metric | Type | Labels | Notes |
|---|---|---|---|
| `renovate_eventsink_up` | Gauge | `{sink}` | 1 after successful publish or Ping; 0 after either fails. Memory: always 1 unless `Close` called. Redis: ticker-driven. |
| `renovate_eventsink_published_total` | Counter | `{sink, result}` | `result=success\|failure`. |
| `renovate_eventsink_publish_duration_seconds` | Histogram | `{sink}` | Default buckets + native histogram. |
| `renovate_eventsink_dropped_total` | Counter | `{sink, reason}` | `reason ∈ {timeout, sink_down, sink_full, payload_too_large, encoding}`. `sink_full` is memory-specific; `timeout`/`sink_down` are redis-specific. |

The `Sink` middleware (`internal/eventsink/middleware.go`) wraps
any `Sink` impl and emits these around `Publish`. Future sink
impls get the metrics for free. The `sink` label is set per
backend at wrap time (`memory`, `redis`).

PrometheusRule additions in `dist/chart/templates/extra/prometheusrule.yaml`:

- `RenovateEventSinkDown` — alerts on
  `renovate_eventsink_up{sink="redis"} == 0` for 5m. Memory is
  not page-worthy (its "down" condition is operator-internal).
- `RenovateEventSinkFailureRate` — alerts on
  `rate(renovate_eventsink_published_total{result="failure"}[5m]) /
   rate(renovate_eventsink_published_total[5m]) > 0.05` for 10m,
  per `sink` label.
- `RenovateEventSinkMemoryFull` — alerts on
  `rate(renovate_eventsink_dropped_total{sink="memory", reason="sink_full"}[5m]) > 0`
  for 5m. Indicates consumer is wedged or undersized buffer.

Grafana dashboard panel additions in `contrib/grafana/dashboards/operator.json`:
per-sink up, throughput, failure rate, p99 publish latency, drop
reasons stacked bar.

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
reads it after Job completion** — specifically to
`/dev/termination-log` as JSON. K8s preserves that on Pod status
indefinitely after termination. No extra RBAC, no ConfigMap
proliferation, no log scraping.

**Lean toward B with `terminationMessagePath`.** The exact wrapper
script and JSON shape are the first implementation question to
nail down. Whatever the answer, the event payload's `Outcome`
struct is the *operator-internal* shape; the wrapper's format is
a private contract between the worker image and the Run
reconciler.

## API / Interface Changes

CRD: **no changes**. Run.status gains a new condition type
(`EventsPublished`) but conditions are an open set — not a CRD
schema change.

Helm chart additions (all gated to default-off):

```yaml
# values.yaml additions
eventSink:
  enabled: false                              # opt-in master switch
  memory:
    enabled: false                            # in-process channel
    bufferSize: 1024
  redis:
    enabled: false                            # XADD to Redis stream
    addr: ""                                  # required when redis.enabled=true
    db: 0
    stream: "renovate.events"
    maxLen: 100000                            # XADD MAXLEN ~ ; 0 = unbounded
    timeout: "5s"
    tls:
      enabled: false
      caSecretRef: { name: "", key: "" }
    auth:
      secretRef: { name: "", key: "" }
  enrichment:
    enabled: false
    sources: [customProperties, catalogInfo]
    customProperties:
      keys:
        owner: "owner"
        system: "system"
        lifecycle: "lifecycle"
    catalogInfo:
      path: "catalog-info.yaml"
```

Template guards (fail-fast at render time):

- `eventSink.enabled=true && !memory.enabled && !redis.enabled` →
  error: "eventSink.enabled requires at least one backend (memory or redis)".
- `eventSink.redis.enabled=true && eventSink.redis.addr==""` →
  error: "eventSink.redis.enabled requires eventSink.redis.addr".

No new binaries. EventSink lives inside the existing manager
binary.

## Data Model

No new operator-owned persistent storage. The in-memory backend
buffers events in a Go channel (lost on operator restart — caller
beware). The Redis backend writes to a stream the operator does
not own; storage lifecycle is the consumer's concern.

Schema versioning for the event payload lives in the CloudEvents
`dataschema` field — initial version
`https://fartlab.dev/schemas/renovate-events/v1/run-repo-completed.json`.
Both backends carry the same envelope, so consumers don't branch
on transport.

## Testing Strategy

- **Unit (`*_test.go`)**:
  - `eventsink/sink_test.go`: no-op sink contract.
  - `eventsink/fanout_test.go`: multi-sink publish, partial-failure
    semantics, Close fanout.
  - `eventsink/middleware_test.go`: every Sink impl wrapped by
    `WithMetrics` emits the four collectors on every code path.
  - `eventsink/memory/memory_test.go`: buffer-full → `ErrSinkFull`,
    Close → `ErrSinkDown` on subsequent Publish, Subscribe channel
    delivers in order.
  - `eventsink/redis/redis_test.go`: miniredis-backed; covers
    `XADD` happy path, MAXLEN trim, timeout error mapping, TLS
    config wiring, Ping-driven `up` gauge.
  - `enrichment/customprops/customprops_test.go`: httptest fake
    GitHub; properties happy path, 404, missing-keys, PAT vs App.
  - `enrichment/catalog/catalog_test.go`: `contents/` happy path,
    404, malformed YAML, missing fields.
- **Controller (envtest)**:
  - New spec in `internal/controller/renovaterun_controller_test.go`:
    Run completes, `EventsPublished=True`, the configured Sink saw
    N events with the expected shape (covers both memory-only and
    fanout-with-memory+redis configurations).
  - Sink-failure spec: Run reconciler installs a faulting Sink,
    Run still reaches `Succeeded`, `EventsPublished=False` with
    reason `SinkError`, `dropped_total{reason}` counter
    increments.
- **e2e (kind)**:
  - `test/e2e/eventsink_memory_test.go`: in-memory only; consumer
    is a test helper goroutine that reads `Subscribe()`. Asserts
    events arrive in order.
  - `test/e2e/eventsink_redis_test.go`: kind cluster with
    miniredis deployed, Redis backend enabled. Asserts events
    land in the Redis stream with the expected CloudEvents
    envelope.
- Coverage gate stays at ≥80% per package per IMPL-0001.

## Migration / Rollout Plan

v0.2.0 release is fully additive. Upgrade path from any v0.1.x install:

1. `helm upgrade renovate-operator oci://ghcr.io/donaldgifford/charts/renovate-operator --version 0.2.0`.
2. No CRD-breaking changes; existing Platforms/Scans/Runs unaffected.
3. `helm diff` shows the new `eventSink.*` value defaults
   (`enabled: false`, both backends disabled).
4. No behavioral changes for v0.1.x feature paths. The Run
   reconciler's terminal path gains a Sink call, but the default
   Sink is a no-op.
5. To opt in to **memory only** (preparing for v0.3.0 embedded API):
   ```bash
   helm upgrade renovate-operator ... \
     --set eventSink.enabled=true \
     --set eventSink.memory.enabled=true
   ```
   Useful in v0.2.0 only for testing; no consumer exists yet. The
   buffer accepts events, the consumer-side will land in v0.3.0.
6. To opt in to **Redis only** (external integration today):
   ```bash
   helm upgrade renovate-operator ... \
     --set eventSink.enabled=true \
     --set eventSink.redis.enabled=true \
     --set eventSink.redis.addr=redis.shared.svc.cluster.local:6379
   ```
   External consumers can start reading the Redis stream
   immediately.
7. To opt in to **both** (Fanout): set both `memory.enabled` and
   `redis.enabled`.

Acceptance criteria for v0.2.0 GA:

- All four sink-level collectors visible in `/metrics` with the
  `sink` label distinguishing `memory` from `redis`.
- A Run completing against a real repo produces a CloudEvents-
  shaped entry in:
  - the in-memory channel (verified via a test consumer), and/or
  - the configured Redis Stream (verified via `XREAD`).
- Fanout config produces an event in BOTH backends per Run-repo.
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
  Publish to a GitHub Pages site under the repo? Pick one before
  GA so the URL doesn't change.
- **Memory backend buffer size default.** 1024 is a guess.
  Validate against a real workload during homelab acceptance —
  a Scan completing 50 repos should buffer comfortably even if
  the consumer is briefly stalled.
- **Stream entry shape (Redis).** `ce` field for the full
  CloudEvent payload + a couple of mirrored top-level fields
  (`subject`, `time`) for cheap consumer filtering. Confirm this
  is what the v0.3.0 consumer prefers (DESIGN-0004 should call
  out the read pattern).
- **Forgejo `customProperties` parity.** Forgejo doesn't have an
  equivalent concept today. Document the silent-fallback; revisit
  if Forgejo ships one.
- **Multi-consumer semantics on the memory backend.** The current
  design is single-consumer (channel is point-to-point). If a
  second consumer needs to read from the same in-memory stream,
  we'd need to fan-out internally. Defer — only one consumer in
  v0.3.0.

## References

- [DESIGN-0002](0002-renovate-operator-v020.md) — v0.2.x scope
  decision; places this design in the larger release plan.
- [DESIGN-0004](0004-operator-http-api-consumer-datastore-ui-and-webhook-receiver.md) —
  v0.3.0 consumer that reads from this Sink (via Subscribe when
  embedded, via XREADGROUP when API is a separate Deployment).
- [INV-0006](../investigation/0006-operationalizing-renovate-operator-at-scale-dashboard-risk.md) —
  origin of the emit-only architecture and audience separation.
- CloudEvents v1.0 spec —
  [`cloudevents/sdk-go/v2`](https://pkg.go.dev/github.com/cloudevents/sdk-go/v2).
- Redis Streams — `XADD` semantics, `MAXLEN ~`.
- GitHub REST:
  [`GET /repos/{owner}/{repo}/properties/values`](https://docs.github.com/en/rest/repos/custom-properties).
- Backstage `catalog-info.yaml` schema.
