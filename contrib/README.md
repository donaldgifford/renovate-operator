# `contrib/` — drop-in observability artifacts

Dashboards, alerts, and ingest snippets for running renovate-operator under
the Grafana stack (Prometheus + Loki + Tempo). Everything here is optional;
nothing in `contrib/` is bundled by the Helm chart.

```
contrib/
├── grafana/dashboards/
│   ├── operator.json   — operator-internal health (workqueue, leader, memory)
│   ├── runs.json       — RenovateRun lifecycle (active, durations, failures)
│   ├── traces.json     — Tempo-driven trace exploration
│   └── logs.json       — Loki log exploration with scan/platform filters
├── prometheus/
│   ├── recording-rules.yaml — pre-aggregated runs:rate5m, runs:failure_ratio5m, ...
│   └── alerts.yaml          — RunsFailing, DiscoveryErrors, ShardsFailing, RunStuck, PodNotReady
└── alloy/
    └── operator.river  — Alloy config snippet: scrape metrics, tail logs, accept OTLP traces
```

## Importing the Grafana dashboards

The dashboards declare their datasource inputs (`DS_PROMETHEUS`, `DS_LOKI`,
`DS_TEMPO`) so Grafana prompts you at import time. The standard
kube-prometheus-stack / loki-stack / tempo-distributed datasource UIDs work
out of the box.

**Via Grafana UI**

1. Dashboards → New → Import.
2. Upload `contrib/grafana/dashboards/<name>.json`.
3. Pick the matching datasource for each `__inputs` prompt.

**Via Grafana provisioning** (recommended for kube-prometheus-stack)

Mount the dashboards into the Grafana sidecar config-reloader:

```yaml
# helm values for kube-prometheus-stack
grafana:
  dashboards:
    renovate-operator:
      operator: { json: |
        # paste contents of operator.json here, or use sidecar.dashboards
      }
```

Or use the sidecar-provisioning ConfigMap pattern (search for
`grafana_dashboard: "1"` label).

## Applying the Prometheus rules

Both files are `PrometheusRule` resources scoped at `namespace: monitoring`
and labeled `release: kube-prometheus-stack` so the kube-prometheus-stack
Prometheus picks them up. Apply with:

```bash
kubectl apply -f contrib/prometheus/recording-rules.yaml
kubectl apply -f contrib/prometheus/alerts.yaml
```

If your `release:` label is different, edit the metadata.labels block in
each file or override via kustomize.

The Helm chart embeds the same recording rules and alerts in
`dist/chart/templates/extra/prometheusrule.yaml`, gated by
`metrics.prometheusRule.enabled`. Use the chart-managed copy if you're
installing via Helm; use these files if you manage Prometheus rules
out-of-band.

## Wiring Alloy

`alloy/operator.river` covers all three pillars:

- **Metrics**: Kubernetes pod discovery + scrape of the operator's `/metrics`
  endpoint, forwarded via remote_write. Adjust the
  `prometheus.remote_write.default` endpoint to your Prometheus / Mimir.
- **Logs**: Kubernetes log discovery + JSON parsing (extracts `level`,
  `trace_id`, `scan`, `platform` as Loki labels) + push to Loki gateway.
- **Traces**: OTLP gRPC receiver on `:4317`, forwarded to Tempo. Set the
  operator's `tracing.otlpEndpoint` to `alloy.<namespace>.svc:4317` to use
  Alloy as the trace forwarder; or point the operator directly at Tempo
  and skip this block.

The chart's `metrics.serviceMonitor.enabled=true` covers the metrics path
for kube-prometheus-stack users automatically — only use Alloy for
metrics if you're not running the Prometheus Operator.

## Pivoting between dashboards

- **runs.json → logs.json**: copy the `scan` and `platform` values from
  a struggling Run, paste into the matching variables on the logs
  dashboard.
- **logs.json → traces.json**: every operator log line emitted while a
  span was recording carries `trace_id`. Copy that into the Trace ID
  textbox on traces.json.
- **traces.json → operator.json**: when a trace shows abnormal duration,
  cross-check operator memory / goroutine pressure on the operator
  dashboard for the same time window.
