---
id: DESIGN-0003
title: "EventSink for renovate-operator"
status: Draft
author: Donald Gifford
created: 2026-05-31
---
<!-- markdownlint-disable-file MD025 MD041 -->

# DESIGN 0003: EventSink for renovate-operator

**Status:** Draft
**Author:** Donald Gifford
**Date:** 2026-05-31 (re-scoped 2026-08-01)

> **Re-scope note (2026-08-01).** The original version of this doc
> shipped two backends day one — in-memory (to feed DESIGN-0004's
> embedded consumer) and Redis Streams — plus a Fanout wrapper.
> With [DESIGN-0005](0005-operator-state-in-postgres-and-valkey-backed-scheduling.md)
> making the operator the direct writer of Postgres state, the
> in-memory backend lost its only consumer and is deleted, along
> with Fanout. The EventSink is now **outbound integration events
> only** ("send events to my Slack bot / audit log / Kafka
> pipeline"), over a single Valkey/Redis Streams backend. It is no
> longer the UI's data feed and no longer a precondition for any
> release — it lands opportunistically (see DESIGN-0005 §Release
> sequencing). Ownership enrichment and the worker→operator outcome
> channel also moved to DESIGN-0005, since they feed Postgres first.

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
  - [Streams implementation](#streams-implementation)
  - [Reconciler integration](#reconciler-integration)
  - [Sink-level Prometheus collectors](#sink-level-prometheus-collectors)
- [API / Interface Changes](#api--interface-changes)
- [Data Model](#data-model)
- [Testing Strategy](#testing-strategy)
- [Migration / Rollout Plan](#migration--rollout-plan)
- [Open Questions](#open-questions)
- [References](#references)
<!--toc:end-->

## Overview

The operator publishes a structured CloudEvents v1.0 event for
every repository it processes in every Run, through a pluggable
`Sink` interface, off by default. One backend ships: **Valkey/Redis
Streams** (`go-redis/v9`, `XADD` to a configurable stream key,
MAXLEN-bounded). Consumers are external systems the operator knows
nothing about — Slack bots, audit pipelines, Kafka connectors.

The bundled UI does **not** consume this stream; it reads Postgres
through the Connect API
([DESIGN-0004](0004-operator-http-api-consumer-datastore-ui-and-webhook-receiver.md)).
The event payload is built from the same per-repo outcome data the
Run reconciler writes to Postgres (DESIGN-0005's worker shim /
outcome reporting), so the sink adds no new collection machinery —
it is a publish-only tap on state the operator already has.

## Goals and Non-Goals

### Goals

- `internal/eventsink/` package with `Sink` interface + typed
  `Event` (CloudEvents v1.0 envelope) + no-op default sink.
- `internal/eventsink/redis/` — `go-redis/v9` Streams
  implementation. Works unchanged against Valkey (Redis-protocol-
  compatible); the homelab points it at the same Valkey instance
  the workers use as a package cache (DESIGN-0005), different
  keyspace.
- Run reconciler publishes once per repo at the Run's terminal
  transition. Failures **never** fail the Run.
- Sink-level Prometheus collectors (`up`, `published_total`,
  `publish_duration_seconds`, `dropped_total`) via a `Sink`
  middleware, so future backends get them for free. `sink` label
  kept for that future (`redis` is the only value today).
- Chart values gated to default-off; fully additive upgrade.

### Non-Goals

- Feeding the bundled UI. That path is operator → Postgres →
  Connect API (DESIGN-0005 / DESIGN-0004).
- In-memory backend, Fanout, `Subscribe()`. Deleted with their
  consumer. A future second backend re-introduces fanout only if a
  deployment actually runs two sinks at once.
- Additional backends (NATS, Knative, HTTP webhook, Kafka). They
  slot in behind `Sink` without reconciler changes; not this scope.
- Consumer-side anything: replay, dead-letter, consumer-group
  management. The operator `XADD`s; everything after that belongs
  to whoever reads the stream.
- Per-Platform sink config. Cluster-wide only until a real
  multi-tenant case appears.
- Ownership enrichment (moved to DESIGN-0005 — events carry the
  `ownership` block already cached on the Run snapshot).

## Background

Driving question and rationale in
[INV-0006](../investigation/0006-operationalizing-renovate-operator-at-scale-dashboard-risk.md).
DESIGN-0002 originally assigned EventSink to v0.2.0 as the
foundation the v0.3.0 consumer would read. The 2026-08-01 rethink
(recorded in DESIGN-0005 §Background) removed the consumer
entirely, which cut this design roughly in half: no in-memory
backend, no Fanout, no embedded-consumer wiring, and no release
dependency. What remains is the third-party integration story,
which was always the Redis backend's job.

## Detailed Design

### Package layout

```
internal/
  eventsink/
    sink.go              # Sink interface, Event types, no-op impl, sentinels
    middleware.go        # Prometheus-emitting Sink wrapper
    middleware_test.go
    sink_test.go
    redis/
      redis.go           # Valkey/Redis Streams impl
      redis_test.go      # miniredis-backed unit tests
```

Wiring in `cmd/main.go`:

```go
// buildSink returns the configured Sink: noop when disabled,
// WithMetrics(redis.New(...), "redis") when enabled.
sink, err := buildSink(cfg.EventSink)
runReconciler.Sink = sink
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

// Disabled returns a no-op sink. Used when the sink is not enabled.
func Disabled() Sink { return noopSink{} }
```

`Publish` errors are wrapped with typed sentinels so the middleware
can label `dropped_total{reason}` cleanly:

```go
var (
    ErrTimeout         = errors.New("eventsink: publish timed out")
    ErrPayloadTooLarge = errors.New("eventsink: payload too large")
    ErrSinkDown        = errors.New("eventsink: sink unreachable")
    ErrEncoding        = errors.New("eventsink: event encoding failed")
)
```

`Close` is called once at controller shutdown to flush in-flight
publishes and close the client cleanly.

### Event type

CloudEvents v1.0 envelope; `Event.Data` is the typed payload built
from the DESIGN-0005 per-repo outcome data:

```go
type EventData struct {
    Run       RunRef      `json:"run"`
    Repo      RepoRef     `json:"repo"`
    Ownership Ownership   `json:"ownership"` // from the Run snapshot (DESIGN-0005 enrichment)
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
so any consumer that already speaks CloudEvents decodes without
custom code.

### Streams implementation

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
  work behind one config flag. Valkey speaks the same protocol.
- `client.Ping` on a 30s ticker drives `renovate_eventsink_up{sink="redis"}`.
- No retry inside `Publish` — the timeout is short, the caller has
  a Run-condition surface, and retries would queue failures.
  Reconnect happens client-internally per go-redis defaults.
- TLS and auth from chart-mounted Secrets; never from values
  directly.
- May share the Valkey instance workers use as a package cache
  (DESIGN-0005) or point at a dedicated one — deployment choice,
  not code.

### Reconciler integration

The Run reconciler's terminal-transition path:

```go
// pseudo-code in renovaterun_controller.go
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
work; failing to publish a notification about it is observational.
`EventsPublished` is the diagnostic surface; `dropped_total` is the
alerting surface.

### Sink-level Prometheus collectors

Registered in `internal/observability/metrics.go` alongside the
existing collectors:

| Metric | Type | Labels | Notes |
|---|---|---|---|
| `renovate_eventsink_up` | Gauge | `{sink}` | 1 after successful publish or Ping; 0 after either fails. Ticker-driven. |
| `renovate_eventsink_published_total` | Counter | `{sink, result}` | `result=success\|failure`. |
| `renovate_eventsink_publish_duration_seconds` | Histogram | `{sink}` | Default buckets + native histogram. |
| `renovate_eventsink_dropped_total` | Counter | `{sink, reason}` | `reason ∈ {timeout, sink_down, payload_too_large, encoding}`. |

The middleware (`internal/eventsink/middleware.go`) wraps any
`Sink` impl and emits these around `Publish`; future backends get
them for free.

PrometheusRule additions in `dist/chart/templates/extra/prometheusrule.yaml`:

- `RenovateEventSinkDown` — `renovate_eventsink_up == 0` for 5m.
- `RenovateEventSinkFailureRate` —
  `rate(renovate_eventsink_published_total{result="failure"}[5m]) /
   rate(renovate_eventsink_published_total[5m]) > 0.05` for 10m.

Grafana panel additions in `contrib/grafana/dashboards/operator.json`:
up, throughput, failure rate, p99 publish latency, drop reasons.

## API / Interface Changes

CRD: **no changes**. Run.status gains the `EventsPublished`
condition — open set, not a schema change.

Helm chart additions (gated to default-off):

```yaml
# values.yaml additions
eventSink:
  enabled: false                              # opt-in
  addr: ""                                    # required when enabled; Valkey or Redis
  db: 0
  stream: "renovate.events"
  maxLen: 100000                              # XADD MAXLEN ~ ; 0 = unbounded
  timeout: "5s"
  tls:
    enabled: false
    caSecretRef: { name: "", key: "" }
  auth:
    secretRef: { name: "", key: "" }
```

Template guard: `eventSink.enabled=true && eventSink.addr==""` →
render error.

No new binaries; the sink lives in the manager.

## Data Model

No operator-owned storage. The stream belongs to its consumers;
MAXLEN bounds it. Payload schema versioning lives in the
CloudEvents `dataschema` field — initial version
`https://fartlab.dev/schemas/renovate-events/v1/run-repo-completed.json`.

## Testing Strategy

- **Unit:** no-op sink contract; middleware emits all four
  collectors on every code path; redis impl against miniredis
  (`XADD` happy path, MAXLEN trim, timeout mapping, TLS config
  wiring, Ping-driven `up` gauge).
- **Controller (envtest):** Run completes → `EventsPublished=True`
  and the sink saw N events with the expected shape; faulting-sink
  spec → Run still `Succeeded`, `EventsPublished=False` with
  `SinkError`, `dropped_total` incremented.
- **e2e (kind):** miniredis deployed, sink enabled; events land in
  the stream with the expected CloudEvents envelope (`XREAD`).
- Coverage gate ≥80% per package per IMPL-0001.

## Migration / Rollout Plan

Fully additive; no release dependency (see DESIGN-0005 §Release
sequencing — lands opportunistically in v0.2.x or v0.3.x):

1. `helm upgrade` to the carrying release; `eventSink.enabled=false`
   default, zero behavior change.
2. Opt in:
   ```bash
   helm upgrade renovate-operator ... \
     --set eventSink.enabled=true \
     --set eventSink.addr=valkey.shared.svc.cluster.local:6379
   ```
   External consumers can start reading the stream immediately.

Acceptance criteria:

- All four collectors visible in `/metrics`.
- A Run completing against a real repo produces a CloudEvents-
  shaped entry in the stream (verified via `XREAD`).
- `ownership` block populated when DESIGN-0005 enrichment is on;
  empty + `source="unset"` when off.

## Open Questions

- **`dataschema` hosting.** Bundle in the chart (`extra/schemas/`)
  or publish via GitHub Pages? Pick before GA so the URL is stable.
- **Stream entry shape.** `ce` field for the full payload + mirrored
  `subject`/`time` for cheap consumer filtering. Validate against
  the first real external consumer.
- **Emission timing.** Terminal-transition batch (current design)
  vs. per-repo as outcomes arrive on the DESIGN-0005 results
  stream. Per-repo would give integrations lower latency for the
  same data; decide when implementing against the shim.

## References

- [DESIGN-0005](0005-operator-state-in-postgres-and-valkey-backed-scheduling.md) —
  state/scheduling design; source of the per-repo outcome data this
  sink publishes, and of the 2026-08-01 re-scope rationale.
- [DESIGN-0002](0002-renovate-operator-v020.md) — original v0.2.x
  scoping (release table superseded by DESIGN-0005).
- [DESIGN-0004](0004-operator-http-api-consumer-datastore-ui-and-webhook-receiver.md) —
  the UI stack, which does *not* consume this sink.
- [INV-0006](../investigation/0006-operationalizing-renovate-operator-at-scale-dashboard-risk.md) —
  origin of the emit-only architecture.
- CloudEvents v1.0 — [`cloudevents/sdk-go/v2`](https://pkg.go.dev/github.com/cloudevents/sdk-go/v2).
- Valkey/Redis Streams — `XADD`, `MAXLEN ~`.
