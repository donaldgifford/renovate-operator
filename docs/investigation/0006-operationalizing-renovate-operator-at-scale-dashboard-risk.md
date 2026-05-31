---
id: INV-0006
title: "Operator emits per-repo Run events to a pluggable sink (Redis first)"
status: Open
author: Donald Gifford
created: 2026-05-31
---
<!-- markdownlint-disable-file MD025 MD041 -->

# INV 0006: Operator emits per-repo Run events to a pluggable sink (Redis first)

**Status:** Open
**Author:** Donald Gifford
**Date:** 2026-05-31

<!--toc:start-->
- [Question](#question)
- [Hypothesis](#hypothesis)
- [Context](#context)
- [Approach](#approach)
- [Findings](#findings)
  - [Observation 1 — the event is "one repo, one Run"](#observation-1--the-event-is-one-repo-one-run)
  - [Observation 2 — sink is pluggable; Redis is the first impl](#observation-2--sink-is-pluggable-redis-is-the-first-impl)
  - [Observation 3 — off by default; consumer owns the queue](#observation-3--off-by-default-consumer-owns-the-queue)
  - [Observation 4 — optional ownership enrichment from two sources](#observation-4--optional-ownership-enrichment-from-two-sources)
  - [Observation 5 — Prometheus stays platform-ops-only, but grows to cover the new contract](#observation-5--prometheus-stays-platform-ops-only-but-grows-to-cover-the-new-contract)
- [Conclusion](#conclusion)
- [Recommendation](#recommendation)
  - [Open questions to resolve during implementation](#open-questions-to-resolve-during-implementation)
- [References](#references)
<!--toc:end-->

## Question

At 1K+ repos across multiple GitHub orgs, dev teams want to see what
Renovate is doing in *their* repos — open PRs, vulnerabilities, dep
updates, age — without having to scrape N Dep Dashboard issues. How
do we surface that **without** expanding the operator's scope into
storage, UI, or ownership resolution?

## Hypothesis

The operator's correct role is to **emit one structured event per repo
per Run** to a pluggable sink. Anything downstream — queue, store,
UI, per-team views — is the consumer's responsibility. The operator
stays a producer.

Concretely:

- Emission is **off by default**. Opt-in per Platform (or globally
  via chart values).
- Emission goes through a Go interface (`EventSink`) with one
  implementation in v0.2.x: **Redis**. Consumers configure host/port,
  TLS, auth via chart values.
- Future backends (NATS, Knative Eventing, Kafka, plain HTTP webhook)
  slot in behind the same interface without touching reconcilers.
- The consumer provides and operates the Redis instance/cluster. The
  operator does not deploy, manage, or own its lifecycle.
- Optional, *also* off by default: each event carries a small
  ownership block populated by reading either GitHub repo **custom
  properties** or the repo's **`catalog-info.yaml`**. If neither is
  present, the ownership fields are empty strings — never errors,
  never missing keys.

## Context

The Phase 9 homelab loop surfaced two real questions for fleet-scale
operation. Risk classification turns out to already be Renovate's
problem (centralized `packageRules` preset + `dependencyDashboardApproval`,
disciplined via existing onboarding tools) — no operator change needed.
The remaining gap is **visibility**: a dev team owning 30 repos has
no aggregated view across them.

**Triggered by:** Phase 9 homelab acceptance loop, conversation
2026-05-31 around schedule semantics and global-team operationalization.
Not blocking v0.1.x; informs v0.2.x scope.

## Approach

Thinking spike. No code. Outcomes:

1. Define the event shape — what's *in* one event.
2. Define the sink interface — what an `EventSink` implementation
   must do.
3. Define the configuration surface — how a user opts in and points
   the operator at their Redis.
4. Define the optional enrichment surface — what an enriched event
   looks like, what happens when enrichment is off or sources are
   missing.
5. Explicitly state what is **out of scope** for the operator
   (queue lifecycle, UI, ownership resolution beyond the two simple
   sources above, per-team views).

## Findings

### Observation 1 — the event is "one repo, one Run"

The grain is deliberate: one Renovate Run processes N repos in
parallel shards; we emit N events, one per repo per Run. Not one
event per PR, not one event per Run-as-a-whole.

Per-repo grain is what consumers actually want to query against
("show me all events for `owner/repo` in the last 7 days"), and it
keeps the payload bounded — a single repo's PR list is small even
in worst-case repos.

**Event payload (draft):**

```json
{
  "specversion": "1.0",
  "type": "dev.fartlab.renovate.run.repo.completed",
  "source": "renovate-operator/<scan-namespace>/<scan-name>",
  "id": "<run-uid>/<repo-owner>/<repo-name>",
  "time": "2026-05-31T14:22:09Z",
  "subject": "<repo-owner>/<repo-name>",
  "data": {
    "run": {
      "uid": "<run-uid>",
      "scan": "<scan-name>",
      "platform": "<platform-name>",
      "started_at": "...",
      "finished_at": "...",
      "outcome": "succeeded | failed | skipped"
    },
    "repo": {
      "owner": "donaldgifford",
      "name": "server-price-tracker",
      "default_branch": "main",
      "platform_type": "github"
    },
    "ownership": {
      "owner": "team-platform",
      "system": "fartlab-infra",
      "lifecycle": "production",
      "source": "catalog-info.yaml | custom-properties | unset"
    },
    "outcome": {
      "prs_opened":   [{"number": 42, "url": "...", "labels": ["dependencies"], "automerge": true}],
      "prs_updated":  [...],
      "prs_closed":   [...],
      "vulnerabilities": [{"advisory_id": "GHSA-...", "severity": "high", "package": "lodash"}],
      "dependencies_updated": [{"package": "lodash", "from": "4.17.20", "to": "4.17.21", "manager": "npm"}]
    }
  }
}
```

A [CloudEvents v1.0](https://cloudevents.io/) envelope is the
default because it interops with everything (Knative, NATS, Kafka
connectors, generic webhook receivers) for free and makes the
graduation path to other backends frictionless.

### Observation 2 — sink is pluggable; Redis is the first impl

Define a thin interface in `internal/eventsink/`:

```go
type Event struct {
    SpecVersion string
    Type        string
    Source      string
    ID          string
    Time        time.Time
    Subject     string
    Data        EventData
}

type Sink interface {
    Publish(ctx context.Context, ev Event) error
    Close(ctx context.Context) error
}
```

Reconcilers depend only on `Sink`. The Run reconciler, at the
terminal transition for each repo, builds the `Event` and calls
`Publish`. Failures from `Publish` are logged + surfaced as a Run
condition but **do not fail the Run** — emission is observational,
not load-bearing on Renovate having actually done its work.

The first implementation: `internal/eventsink/redis/`.

- Use [`github.com/redis/go-redis/v9`](https://pkg.go.dev/github.com/redis/go-redis/v9).
- Publish via `XADD` to a configurable stream key (default
  `renovate.events`). Streams (not pub/sub) so consumers can be
  offline and catch up; consumer-group semantics are the consumer's
  call.
- Optional sentinel/cluster client based on chart values.
- Reconnect with backoff; per-event publish has a tight timeout
  (default 5s) so a wedged Redis doesn't slow Run reconciliation.

Future impls slot in (`internal/eventsink/nats/`,
`internal/eventsink/webhook/`, `internal/eventsink/knative/`)
without reconciler changes. Each chosen by chart-values
`eventSink.type`.

### Observation 3 — off by default; consumer owns the queue

The Helm chart adds (sketch):

```yaml
# values.yaml
eventSink:
  enabled: false               # opt-in
  type: redis                  # "redis" only in v0.2.x
  redis:
    addr: "redis.example.svc.cluster.local:6379"
    db: 0
    stream: "renovate.events"
    maxLen: 100000             # XADD MAXLEN ~ ; 0 = unbounded
    tls:
      enabled: false
      caSecretRef: { name: "", key: "" }
    auth:
      secretRef: { name: "", key: "" }   # contains "password" or full URL
  enrichment:
    enabled: false             # opt-in
    sources:                   # ordered; first hit wins
      - customProperties
      - catalogInfo
```

When `eventSink.enabled=false` (default), the reconciler wires a
no-op sink and the code path is dead — no Redis dependency, no
connection attempts, no log noise.

The operator does **not**:

- Deploy Redis. Users bring their own (managed service, self-hosted,
  cluster from another chart).
- Manage retention beyond the configurable `MAXLEN` on `XADD`. Long-
  term retention is a consumer concern.
- Provide consumer groups, dead-letter handling, or replay. Consumers
  implement their own consumer-group reads against the stream.

This is the architectural cut that keeps the operator small.

### Observation 4 — optional ownership enrichment from two sources

To make events useful to per-team consumers without forcing the
consumer to do another round of platform-API lookups, the operator
*can* enrich each event with a small ownership block. Two sources,
both optional, configurable order:

1. **GitHub repo custom properties** —
   [`GET /repos/{owner}/{repo}/properties/values`](https://docs.github.com/en/rest/repos/custom-properties).
   Available since GitHub Enterprise rolled out repo-level custom
   properties; org admins define keys like `owner`, `system`,
   `lifecycle`. Operator reads at discovery time (or once at first
   touch and caches per Run). Forgejo equivalent: not surfaced in
   v0.2.x — Forgejo doesn't have an analogous concept yet.
2. **`catalog-info.yaml`** in the repo's default branch —
   Backstage's de-facto component descriptor. Operator does a
   single `GET contents/catalog-info.yaml`, parses the YAML,
   pulls `spec.owner`, `spec.system`, `spec.lifecycle`. If the
   file doesn't exist (404) or doesn't parse, fall back to next
   source or empty.

**If both sources are off or both return nothing, the ownership
block's string fields are `""` — never absent keys, never errors.**
This keeps consumer parsing trivial: no missing-key branches, no
"is this enrichment on?" checks.

Source attribution is part of the payload (`ownership.source`) so
the consumer can debug "why is this empty" without re-running
discovery.

Cost note: enrichment adds 1 or 2 extra platform API calls per repo
per Run. For GitHub App auth with 4500 req/hr budget at 1K repos,
that's still well under the ceiling for nightly cadence (1K-2K
extra reqs vs. 4500/hr * 24 = 108K available). For high-frequency
schedules or instances near rate-limit pressure, enrichment is off
by default precisely because it's not free.

### Observation 5 — Prometheus stays platform-ops-only, but grows to cover the new contract

The existing Prometheus/Grafana/OTel/Loki surface is for the
**platform team** running the operator. It is *not* for the dev
teams owning the repos. This investigation preserves that:

- No per-repo / per-PR collectors get added to
  `internal/observability/metrics.go`. The cardinality alone (1K
  repos × N labels) is disqualifying, even before the audience
  argument.
- Customer-facing data flows through the event sink, not Prometheus.
- The current operator metrics (RunsTotal, DiscoveryErrorsTotal,
  ActiveRuns, etc.) stay scoped to `{scan, platform, result}` and
  remain the platform team's debugging surface.

**However**, once the operator takes on "publish an event for every
repo in every Run" as part of its contract, it owes the platform
team a way to verify that contract is being honored. That is
unambiguously platform-ops — "is the operator doing what it says
it does?" — and belongs in Prometheus, scoped to the *sink*, not
the repos:

| Metric | Type | Labels | What it answers |
|---|---|---|---|
| `renovate_eventsink_up` | Gauge (0/1) | `{sink}` | Is the sink reachable? (For Redis: PING success on a ticker; default off, set 1 when last publish or ping succeeded, 0 when last attempt failed.) |
| `renovate_eventsink_published_total` | Counter | `{sink, result}` | Throughput. `result=success\|failure`. Failure rate over throughput is the SLO. |
| `renovate_eventsink_publish_duration_seconds` | Histogram | `{sink}` | Delivery latency. Native histogram or default buckets — captures Redis tail latency separately from reconciler latency. |
| `renovate_eventsink_dropped_total` | Counter | `{sink, reason}` | When the sink fails *and* we decide not to retry (e.g., timeout, payload too large, sink wedged past a threshold). `reason` is a small enum. |

Cardinality stays bounded: `sink` is one value in v0.2.x (`redis`),
`result` has two, `reason` has a handful. Nothing per-repo, nothing
per-Run.

`renovate_eventsink_up` is the page-worthy one — if the operator
can't reach its sink, the contract is broken and the platform team
needs to know before the consumer notices a gap.

This separation (consumer data → event sink; operator's own
delivery telemetry → Prometheus) is load-bearing. Capture it
explicitly in DESIGN-0001's observability section before v0.2.x
lands so future contributors don't conflate the two.

## Conclusion

**Answer:** The operator should ship one structured CloudEvents-shaped
event per repo per Run, through a pluggable `Sink` interface,
defaulting to disabled. The first implementation is Redis streams.
The consumer provides Redis, reads the stream, joins to whatever
ownership/UI system makes sense for them. The operator does not
operate the queue, does not provide a UI, and does not resolve
ownership beyond two simple opt-in reads (GitHub custom properties,
`catalog-info.yaml`).

Risk classification is solved separately by centralized `packageRules`
in a preset and operational discipline ensuring every repo extends
it — no operator change needed.

## Recommendation

**v0.2.x scope** (this repo):

1. **`internal/eventsink/` package** with `Sink` interface +
   `Event` types + no-op sink. Reconcilers depend on `Sink`.
2. **`internal/eventsink/redis/` implementation** using
   `redis/go-redis/v9`. `XADD` to a configurable stream, MAXLEN
   bounded by config, reconnect with backoff, per-publish timeout.
3. **`RenovateRun` terminal-transition emission**. For each repo
   processed by a Run, build an `Event`, call `Publish`. Failures
   log + Run condition, do **not** fail the Run.
4. **Optional ownership enrichment**:
   - `internal/enrichment/customprops/` (GitHub only) calls
     `/repos/{owner}/{repo}/properties/values`.
   - `internal/enrichment/catalog/` does a `GET contents/catalog-info.yaml`
     and parses the standard Backstage fields.
   - Both return `Ownership{owner, system, lifecycle, source}`;
     empty strings on miss.
   - Wired in by Discovery (with results cached on the Run snapshot
     so reconciliation doesn't re-fetch).
5. **Chart values surface** under `eventSink` and `eventSink.enrichment`
   as sketched above. Default both `enabled: false`.
6. **Sink-level Prometheus collectors** in
   `internal/observability/metrics.go`:
   `renovate_eventsink_up{sink}`,
   `renovate_eventsink_published_total{sink, result}`,
   `renovate_eventsink_publish_duration_seconds{sink}`,
   `renovate_eventsink_dropped_total{sink, reason}`. Wired by the
   `Sink` wrapper so every implementation gets them for free.
   Default Grafana panels + a PrometheusRule alert on `up == 0` or
   `failure_rate > X%`.
7. **Do not** add per-PR / per-repo collectors to the Prometheus
   surface. The new sink-level metrics in (6) cover the operator's
   contract; the consumer is responsible for everything downstream.
   Capture the audience split in DESIGN-0001 so it survives future
   contributors.

**Out of scope for the operator** (forever, not just v0.2.x):

- Redis (or any sink backend) deployment / lifecycle / monitoring.
- Consumer-group management, dead-letter queues, replay logic.
- Any UI, dashboard, or per-team view.
- Resolving `owner-group → user-list` (that's whoever consumes the
  stream, not the operator).
- Persisting events beyond what the configured sink does itself.

### Open questions to resolve during implementation

- **What's the worker's contribution to the event payload?** Two
  options: (a) parse the worker's structured JSON logs in the
  reconciler (no worker-side change, brittle to log-shape changes),
  or (b) have the worker write a small `result.json` to a known
  path and the reconciler reads it (cleaner, requires worker
  cooperation). Lean toward (b).
- **What's the timeout/retry policy for `Publish`?** Tight (5s per
  publish, no retry, log + condition on failure) keeps the
  reconciler responsive. Consumers are expected to be reliable; if
  Redis is wedged, the operator should not pile up.
- **Per-Platform sink vs. cluster-wide?** Cluster-wide (single chart
  values block) is simpler and matches the typical "one operator,
  one event stream" pattern. Per-Platform is more flexible but adds
  surface area to the CRD with no clear v0.2.x demand. Start
  cluster-wide; revisit if a real multi-tenant case appears.
- **CloudEvents transport binding for Redis?** Use
  [CloudEvents Go SDK](https://pkg.go.dev/github.com/cloudevents/sdk-go/v2)'s
  JSON event format as the payload; the Redis stream entry stores
  it as a single field (`ce`) plus mirror a couple of fields
  (`subject`, `time`) at the top level for quick filtering.
- **Backward compat?** Both `eventSink.enabled` and
  `eventSink.enrichment.enabled` are off by default, so v0.1.x users
  see zero change on upgrade. Reconciler with no-op sink is a
  no-op.

## References

- DESIGN-0001 § "Future architecture: state DB" — *now reframed*:
  visibility state lives in the consumer of the event stream, not
  inside the operator.
- CloudEvents v1.0 spec — payload shape.
- Redis Streams — `XADD`, `XREAD`, consumer groups (consumer-side,
  not operator concern).
- GitHub REST: `GET /repos/{owner}/{repo}/properties/values` —
  custom-properties source for ownership enrichment.
- Backstage `catalog-info.yaml` — alternate ownership source; widely
  adopted, parses with stdlib YAML.
- `internal/observability/metrics.go` — current Prometheus
  collectors; stay scoped to platform-ops, no per-repo growth.
- PR #18 — surfaced the operationalization gap during doc work for
  the two-`requireConfig` collision and the schedule-vs-no-schedule
  conversation that led to this spike.
