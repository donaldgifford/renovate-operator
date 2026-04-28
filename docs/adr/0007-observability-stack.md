---
id: ADR-0007
title: "Observability stack: Prometheus, OTel, structured logging, pprof"
status: Proposed
author: donaldgifford
created: 2026-04-26
---
<!-- markdownlint-disable-file MD025 MD041 -->

# 0007. Observability stack: Prometheus, OTel, structured logging, pprof

<!--toc:start-->
- [Status](#status)
- [Context](#context)
- [Decision](#decision)
  - [Logs: kubebuilder default (logr + zap, structured JSON)](#logs-kubebuilder-default-logr--zap-structured-json)
  - [Metrics: controller-runtime defaults + custom renovate_* metrics](#metrics-controller-runtime-defaults--custom-renovate-metrics)
    - [Free from controller-runtime](#free-from-controller-runtime)
    - [Custom metrics](#custom-metrics)
    - [Cardinality budget](#cardinality-budget)
  - [Traces: OTel SDK on hot paths](#traces-otel-sdk-on-hot-paths)
  - [Profiling: net/http/pprof on a dedicated port, gated](#profiling-nethttppprof-on-a-dedicated-port-gated)
  - [Distribution: contrib/ directory](#distribution-contrib-directory)
- [Consequences](#consequences)
  - [Positive](#positive)
  - [Negative](#negative)
  - [Neutral](#neutral)
- [Alternatives Considered](#alternatives-considered)
  - [A. Skip OTel; Prometheus histograms only](#a-skip-otel-prometheus-histograms-only)
  - [B. Use OTel for everything (metrics, logs, traces)](#b-use-otel-for-everything-metrics-logs-traces)
  - [C. Built-in dashboards baked into the operator binary](#c-built-in-dashboards-baked-into-the-operator-binary)
  - [D. Continuous profiling agent (Pyroscope sidecar) by default](#d-continuous-profiling-agent-pyroscope-sidecar-by-default)
  - [E. No pprof](#e-no-pprof)
- [References](#references)
<!--toc:end-->

## Status

Proposed

## Context

[RFC-0001](../rfc/0001-build-kubebuilder-renovate-operator.md) requires the v0.1.0 release to ship a complete observability surface, with `contrib/` artifacts ready to drop into a Grafana stack. This ADR records the choices for each pillar and the integration shape.

The operator will run in two stacks:

- **Homelab**: Prometheus (vanilla or Mimir), Loki, Grafana, Alloy as the OTel/log ingest agent.
- **Enterprise**: same shape but at scale, with whatever Prometheus-compatible backend the platform team has standardized on. Cardinality matters here — 30k repos × N labels could explode storage cost.

The decision space:

| Pillar | Choices |
|--------|---------|
| Logs | `logr` + zap (kubebuilder default) vs `slog` vs custom |
| Metrics | controller-runtime defaults only vs custom + controller-runtime |
| Traces | OTel SDK vs none vs custom (e.g. plain Prometheus histograms) |
| Profiling | `net/http/pprof` always-on vs gated vs disabled vs continuous-profiling agent |
| Distribution | Built into operator binary vs sidecar containers |

We pick Grafana/Alloy as the example ingest path because it's what the homelab runs, but the choices below are vendor-neutral — anything OTLP-compatible will work for traces, anything Prometheus-scrape-compatible for metrics, anything picking up stdout in JSON for logs.

## Decision

### Logs: kubebuilder default (`logr` + zap, structured JSON)

We use `sigs.k8s.io/controller-runtime/pkg/log` (a `logr.Logger`) backed by `go-logr/zapr` + `uber-go/zap` in JSON output mode. This is the kubebuilder default; we do not override it.

Standard fields written by controller-runtime:

- `level`, `ts`, `msg`
- `controller`, `controllerGroup`, `controllerKind` (per-Reconciler)
- `<kind>` (e.g. `RenovateScan`), `namespace`, `name`
- `reconcileID`

Domain-specific fields we add via `log.WithValues(...)`:

- `platform` — Platform name when relevant
- `scan` — Scan namespace/name when relevant
- `run` — Run namespace/name when relevant
- `repo` — repo full name when in worker context (set by the worker wrapper, not the operator)
- `phase` — Run phase (`Discovering`/`Sharding`/`Running`)

All log lines are JSON to stdout. Loki's default Promtail/Alloy config picks them up via the container's stdout stream and parses JSON labels automatically.

We do **not** use Go 1.21+ `log/slog` directly — controller-runtime is `logr`-based and we follow its convention. If `logr` ever ships a `slog` backend, swapping is internal.

### Metrics: controller-runtime defaults + custom `renovate_*` metrics

#### Free from controller-runtime

The kubebuilder default `cmd/main.go` exposes:

- `controller_runtime_*` — workqueue depth, reconcile duration histograms, reconcile error counts, leader election state
- `go_*` and `process_*` — runtime, GC, fd counts
- `client_go_*` — apiserver request rates, latencies

Served on `:8080/metrics` by default; the chart configures a `ServiceMonitor` (when the Prometheus Operator is detected) or a plain pod-level `prometheus.io/scrape` annotation.

#### Custom metrics

Registered via `controller-runtime`'s `metrics.Registry`:

| Metric | Type | Labels | Use |
|--------|------|--------|-----|
| `renovate_scans_total` | Counter | `platform`, `result` | Scan reconciliation outcomes |
| `renovate_runs_total` | Counter | `platform`, `scan`, `phase` | Runs created, by terminal phase |
| `renovate_run_duration_seconds` | Histogram | `platform`, `scan`, `phase` | Run wall-clock per phase |
| `renovate_run_repos_discovered` | Gauge | `platform`, `scan` | Output of discovery, current Run |
| `renovate_run_shard_count` | Gauge | `platform`, `scan` | N value, current Run |
| `renovate_run_workers_active` | Gauge | `platform`, `scan` | Pods currently running |
| `renovate_run_repos_processed_total` | Counter | `platform`, `scan`, `result` | Per-repo outcomes (parsed from worker logs) |
| `renovate_run_prs_opened_total` | Counter | `platform`, `scan` | New PRs created (parsed from worker output) |
| `renovate_platform_ready` | Gauge (0/1) | `platform`, `platform_type` | Platform readiness |
| `renovate_discovery_duration_seconds` | Histogram | `platform`, `scan` | Discovery API call latency |
| `renovate_discovery_api_calls_total` | Counter | `platform`, `result` | Platform API calls during discovery |

#### Cardinality budget

The above schema deliberately **does not** label by repo name. Per-repo metrics from the operator side would mean `30,000 repos × ~10 metrics × ~5 status combinations` = ~1.5 M unique series per scan. The Grafana dashboards aggregate by scan and platform; per-repo drill-down uses Loki logs (`{repo="..."}`) instead.

Per-repo metrics are available via a feature flag (`enableHighCardinalityMetrics`) for diagnostics — off by default, well-documented in the observability guide.

### Traces: OTel SDK on hot paths

We instrument with `go.opentelemetry.io/otel`, configured via standard OTel env vars (`OTEL_EXPORTER_OTLP_ENDPOINT`, `OTEL_SERVICE_NAME`, etc.). Default exporter is OTLP/gRPC; falls back to no-op if no endpoint configured.

Hot paths instrumented (each gets a span):

- `RenovateScan.Reconcile` — full reconcile span; attributes for `scan`, `platform`, `result`.
- `RenovateRun.Reconcile` — same.
- `discovery.Enumerate` — discovery phase as a whole; sub-spans per platform API call (`github.api.repos.list`, `github.api.contents.get`, etc.).
- `discovery.RequireConfigFilter` — the per-repo config presence check (this is the slow part of discovery at scale).
- `shard.Compute` — shard count computation and assignment.
- `job.Build` — Job spec assembly.

Workers also emit traces (each worker is a Renovate process; we wrap it in a small Go binary that opens a span per repo, then execs Renovate). Renovate's own internal operations are not instrumented by us — Renovate is Node.js and instrumenting it is its problem.

Default sample rate: **parent-based 10%**. Configurable via `OTEL_TRACES_SAMPLER` and `OTEL_TRACES_SAMPLER_ARG`. Documented prominently because high-traffic enterprise installs will want to reduce this.

### Profiling: `net/http/pprof` on a dedicated port, gated

The operator binary registers `net/http/pprof` handlers on `:6060/debug/pprof/*` when started with `--enable-pprof` (default off). This is a separate port from `:8080/metrics` and `:8081/healthz` so it can be RBAC-restricted and not exposed externally even when the others are.

Worker profiling: not provided by us. Renovate is Node.js; if profiling is needed, users enable Node's built-in inspector via `extraEnv` on the Scan and `kubectl port-forward` into a worker pod.

Continuous profiling (Pyroscope, Parca, Grafana Profiles): not built in for v0.1.0. The pprof endpoint is compatible with all of these; pulling a continuous profile is a deployment choice, not an operator-code choice.

### Distribution: `contrib/` directory

```
contrib/
├── grafana/
│   └── dashboards/
│       ├── operator-overview.json     # workqueue, reconcile rate, errors, leader election
│       ├── platform-health.json       # per-platform readiness, API call latency
│       ├── runs.json                  # active runs, durations, PR counts, shard sizes
│       ├── traces-explore.json        # OTel trace exploration via Tempo/Jaeger datasource
│       └── logs-explore.json          # Loki log exploration with derived fields
├── prometheus/
│   ├── alerts.yaml                    # PrometheusRule: alerts
│   ├── recording-rules.yaml           # PrometheusRule: recording rules
│   └── README.md
├── alloy/
│   ├── alloy-config.river             # example Alloy config: scrape, OTLP forward, log forward
│   └── README.md
└── loki/
    └── promtail-config.yaml           # alternative if not using Alloy
```

Dashboards reference the metrics defined in this ADR, plus standard `kube_*` metrics from kube-state-metrics for namespace/Job/Pod context. Each dashboard imports cleanly into a fresh Grafana with the standard Prometheus/Loki/Tempo datasource names; instructions for renaming datasource UIDs are in `contrib/grafana/dashboards/README.md`.

Alerts cover the obvious failure modes: operator pod crash-looping, leader election thrashing, Platform never reaching `Ready`, Runs stuck in `Discovering`/`Sharding` for >threshold, Runs failing repeatedly, discovery API calls 4xx-ing, worker pod OOMKills.

Recording rules pre-aggregate the high-cardinality stuff (per-repo histograms etc.) when the high-cardinality feature flag is enabled, so dashboards don't slam Prometheus on every query.

## Consequences

### Positive

- **Three pillars covered with industry-standard tooling.** No bespoke metrics formats, no custom log shipping, no proprietary tracing.
- **Vendor-neutral.** Anything OTLP-compatible accepts the traces; anything Prometheus-scrape-compatible scrapes the metrics; anything reading container stdout JSON gets the logs.
- **`contrib/` lowers operator-onboarding cost.** Drop-in dashboards and alerts mean a new install is observable from minute zero.
- **Cardinality is consciously managed.** The default metric label set is bounded by `platforms × scans`, not by repos, keeping Prometheus storage sane at enterprise scale.
- **pprof is available when needed, locked when not.** Operating diagnostic flexibility without a permanent attack surface.

### Negative

- **OTel SDK adds binary size** (~5 MB to the operator binary) and a runtime dependency tree. Acceptable.
- **Log parsing for `repos_processed_total` and `prs_opened_total`** couples us to Renovate's log format. If Renovate changes its log lines, our metrics break. Mitigated by behind a feature flag and pinning Renovate image tag at the Platform level. v1.x considers a structured-output Renovate plugin if we get that far.
- **Maintaining the dashboards in `contrib/`** is real ongoing work as metrics evolve. We commit to keeping them in lockstep with the metrics they reference; a CI test (in v0.2.0+) can validate that every metric a dashboard panel queries actually exists.
- **OTel's API has churn**; we pin the SDK version in `go.mod` and update deliberately.

### Neutral

- We commit to OTLP for traces. If a user wants Jaeger directly, OTLP-to-Jaeger is one Alloy/OTel-Collector config away — not our problem.

## Alternatives Considered

### A. Skip OTel; Prometheus histograms only

Plain Prometheus histograms for reconcile duration, discovery latency, etc. Simpler. **Rejected** because the value of distributed traces shows up in the hot-path debugging story: when a Run takes 40 minutes and we want to see *which* of the 50 worker pods was slow, traces with a parent-child relationship are vastly more useful than a histogram telling us "p99 was 40 minutes." The homelab can ignore traces (no OTLP endpoint configured = no-op exporter); enterprise users get them for free.

### B. Use OTel for everything (metrics, logs, traces)

OTel has a metrics SDK and a logs SDK now. Adopting OTel as the single observability framework would unify the pillars under one config. **Rejected** for v0.1.0:

- controller-runtime is built on the Prometheus client lib; replacing it would require also wrapping every controller-runtime metric (workqueue, reconcile duration, etc.) into OTel.
- OTel logs SDK is still maturing.
- OTel-to-Prometheus exporters add a layer.

We can revisit if/when controller-runtime adopts OTel natively (there's an open SIG discussion on this). Until then, mixed stack is pragmatic.

### C. Built-in dashboards baked into the operator binary

Some operators serve their own dashboards (e.g., Argo Workflows). **Rejected** — see RFC-0001 §"Why not mogenius/renovate-operator" #7. UI in the operator binary is a smell. Grafana is the right tool for dashboards; we ship dashboards, not a dashboard server.

### D. Continuous profiling agent (Pyroscope sidecar) by default

Sidecars are heavyweight; not all users want continuous profiles; the pprof endpoint already enables this for users who do. **Rejected as default**, **available via `extraContainers` in the chart** for users who want it.

### E. No pprof

Pprof has an attack surface (memory dump exposes internal state). **Rejected because it's gated** — default off, requires `--enable-pprof` flag, served on a separate port from metrics so it can be NetworkPolicy-restricted independently. The diagnostic value in incident response outweighs the cost when properly gated.

## References

- [controller-runtime metrics](https://book.kubebuilder.io/reference/metrics.html)
- [controller-runtime logging](https://book.kubebuilder.io/reference/log.html)
- [logr](https://github.com/go-logr/logr) and [zapr](https://github.com/go-logr/zapr)
- [OpenTelemetry Go SDK](https://pkg.go.dev/go.opentelemetry.io/otel)
- [OTLP environment variables](https://opentelemetry.io/docs/languages/sdk-configuration/otlp-exporter/)
- [Grafana Alloy](https://grafana.com/docs/alloy/latest/)
- [Loki](https://grafana.com/docs/loki/latest/)
- [Pyroscope continuous profiling](https://grafana.com/docs/pyroscope/latest/)
- [`net/http/pprof`](https://pkg.go.dev/net/http/pprof)
- [Prometheus Operator `PrometheusRule`](https://prometheus-operator.dev/docs/operator/api/#monitoring.coreos.com/v1.PrometheusRule)
- [RFC-0001](../rfc/0001-build-kubebuilder-renovate-operator.md)
