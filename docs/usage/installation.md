# Installation

The operator ships as an OCI Helm chart at
`oci://ghcr.io/donaldgifford/charts/renovate-operator`. The container image is
at `ghcr.io/donaldgifford/renovate-operator`. Both are tagged in lockstep with
the chart's `appVersion`.

## Prerequisites

- **Kubernetes** ≥ 1.27. The chart uses `Indexed` `batch/v1.Job` with
  `backoffLimitPerIndex` (1.27+; required for shard-level retry).
- **Helm** ≥ 3.8 (OCI registry support).
- **A namespace for the operator.** The chart defaults to `renovate-system`.
- **A namespace per Scan.** Scans are namespaced and the operator mirrors the
  credential Secret into the Scan's namespace at Run time.
- **Credentials.** Either a GitHub App private key (PEM) or a Forgejo
  personal-access-token (PAT) in a Secret in the operator's release namespace.
  See [RenovatePlatform](renovate-platform.md) for the Secret shape.
- **(Optional) Prometheus Operator** for ServiceMonitor + PrometheusRule.
- **(Optional) OTLP collector** if you want tracing.

## Install

### Quick start

```bash
helm install renovate-operator \
  oci://ghcr.io/donaldgifford/charts/renovate-operator \
  --version 0.1.0 \
  --namespace renovate-system \
  --create-namespace \
  --set defaultScan.enabled=false
```

`defaultScan.enabled=false` is important on a fresh install: the chart's
default-scan template fails-fast unless you also provide
`defaultScan.platformRef.name`, and a Platform resource doesn't exist yet on a
brand-new install.

### Production install (with values file)

Create `homelab-values.yaml`:

```yaml
image:
  repository: ghcr.io/donaldgifford/renovate-operator
  tag: "" # falls back to the chart's appVersion

replicaCount: 1
leaderElect: true

resources:
  requests: { cpu: 100m, memory: 128Mi }
  limits: { cpu: 500m, memory: 512Mi }

metrics:
  enabled: true
  serviceMonitor:
    enabled: true # requires Prometheus Operator
    additionalLabels:
      release: kube-prometheus-stack
  prometheusRule:
    enabled: true # ships the bundled recording rules + alerts
    additionalLabels:
      release: kube-prometheus-stack

tracing:
  enabled: true
  otlpEndpoint: "tempo.observability.svc.cluster.local:4317"
  serviceName: renovate-operator

logging:
  level: info
  format: json

# Ship a default Scan via the chart. Requires a RenovatePlatform named below
# to exist (or be created shortly after) — the operator will retry until it
# reconciles cleanly.
defaultScan:
  enabled: true
  name: default
  platformRef:
    name: github
  schedule: "0 4 * * 0" # weekly, Sunday 04:00 UTC
  timeZone: UTC
  workers:
    minWorkers: 1
    maxWorkers: 5
    reposPerWorker: 50
  discovery:
    autodiscover: true
    requireConfig: true
    skipForks: true
    skipArchived: true
```

Apply:

```bash
helm upgrade --install renovate-operator \
  oci://ghcr.io/donaldgifford/charts/renovate-operator \
  --version 0.1.0 \
  --namespace renovate-system \
  --create-namespace \
  -f homelab-values.yaml
```

### Verify

```bash
kubectl -n renovate-system get pods
kubectl -n renovate-system logs deploy/renovate-operator-controller-manager | grep "Starting manager"
kubectl get crds | grep renovate.fartlab.dev
```

You should see one `Running` pod, the manager's startup log line, and three
CRDs:

- `renovateplatforms.renovate.fartlab.dev`
- `renovatescans.renovate.fartlab.dev`
- `renovateruns.renovate.fartlab.dev`

## Configuration reference

The full chart values surface lives at
[`dist/chart/values.yaml`](../../dist/chart/values.yaml). The most-edited keys:

### Operator container

| Key                             | Default                                   | Notes                                                                     |
| ------------------------------- | ----------------------------------------- | ------------------------------------------------------------------------- |
| `image.repository`              | `ghcr.io/donaldgifford/renovate-operator` |                                                                           |
| `image.tag`                     | `""`                                      | Empty = chart's `appVersion`. Override only to pin to an exact image SHA. |
| `image.pullPolicy`              | `IfNotPresent`                            | Set `Never` for kind/local images.                                        |
| `replicaCount`                  | `1`                                       | Leader election lets you go to 2+ for HA.                                 |
| `leaderElect`                   | `true`                                    | Set to `false` only for single-replica dev.                               |
| `resources.requests` / `limits` | 100m / 128Mi → 500m / 512Mi               | Operator itself is small; workers run in their own Jobs.                  |

### Metrics

| Key                                       | Default                          | Notes                                       |
| ----------------------------------------- | -------------------------------- | ------------------------------------------- |
| `metrics.enabled`                         | `true`                           | Exposes Prometheus on port 8443 (HTTPS, authn-gated). |
| `metrics.serviceMonitor.enabled`          | `false`                          | Requires Prometheus Operator.               |
| `metrics.serviceMonitor.additionalLabels` | `release: kube-prometheus-stack` | Match your Prometheus selector.             |
| `metrics.prometheusRule.enabled`          | `false`                          | Ships the bundled alerts + recording rules. |

#### Authorize Prometheus to scrape `/metrics`

The operator's `:8443/metrics` endpoint sits behind controller-runtime's
`WithAuthenticationAndAuthorization` filter — every caller must present a
bearer token whose `tokenreview` resolves to a subject with `get` on the
cluster-scoped `nonResourceURL=/metrics`. The chart bundles the
`renovate-operator-metrics-reader` `ClusterRole` for that purpose, but
it does **not** ship a `ClusterRoleBinding` — the chart can't know
which `ServiceAccount` your Prometheus runs as.

Wire it up at deploy time. For `kube-prometheus-stack` (default
`ServiceAccount` is `kube-prometheus-stack-prometheus` in the
`monitoring` namespace):

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: kube-prometheus-stack-renovate-metrics-reader
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: renovate-operator-metrics-reader
subjects:
  - kind: ServiceAccount
    name: kube-prometheus-stack-prometheus
    namespace: monitoring
```

Confirm your Prom's `ServiceAccount` first:

```bash
kubectl get pod -A -l app.kubernetes.io/name=prometheus \
  -o custom-columns='NS:.metadata.namespace,NAME:.metadata.name,SA:.spec.serviceAccountName'
```

Apply the binding (or PR it into your GitOps source). Within one
scrape interval (~30s), Prometheus will start receiving 200s and
metric series will populate:

```bash
curl -s 'https://<your-prometheus>/api/v1/label/__name__/values' \
  | jq '.data[] | select(. | test("renovate"; "i"))'
# expect: renovate_operator_runs_total, renovate_operator_active_runs, etc.
```

Without the binding, the scrape target shows `health=up` (the TLS
handshake succeeds and the auth filter responds) but every request
returns `403 Forbidden` and no series ever appear in Prometheus's TSDB.
See the *Scrape healthy but no series in Prometheus* troubleshooting
entry below.

### Tracing

| Key                          | Default             | Notes                                                                         |
| ---------------------------- | ------------------- | ----------------------------------------------------------------------------- |
| `tracing.enabled`            | `false`             | OTLP gRPC.                                                                    |
| `tracing.otlpEndpoint`       | `""`                | Empty = no-op fallback (tracing code paths still execute, no spans exported). |
| `tracing.serviceName`        | `renovate-operator` | Resource attribute on every span.                                             |
| `tracing.resourceAttributes` | `{}`                | Extra resource attributes (e.g., `cluster: homelab`).                         |

### Logging

| Key              | Default | Notes                                                  |
| ---------------- | ------- | ------------------------------------------------------ |
| `logging.level`  | `info`  | `debug` / `info` / `warn` / `error`.                   |
| `logging.format` | `json`  | `json` for prod (Loki/Alloy); `console` for local dev. |

### pprof

| Key             | Default | Notes                                                    |
| --------------- | ------- | -------------------------------------------------------- |
| `pprof.enabled` | `false` | Off by default. Bind on a private port and never expose. |
| `pprof.port`    | `8082`  |                                                          |

### Default Scan

The chart can ship a `RenovateScan` resource alongside the operator. See
[ADR-0008](../adr/0008-default-scan-via-helm-chart.md). Useful for a
single-tenant homelab; turn off in multi-tenant clusters.

| Key                            | Default                               | Notes                                                                          |
| ------------------------------ | ------------------------------------- | ------------------------------------------------------------------------------ |
| `defaultScan.enabled`          | `true`                                | Set to `false` on first install or in multi-tenant setups.                     |
| `defaultScan.platformRef.name` | `""`                                  | **Must be set** when `enabled=true`; chart will fail-fast otherwise.           |
| `defaultScan.schedule`         | `0 4 * * 0`                           | Weekly, Sunday 04:00 in `defaultScan.timeZone`.                                |
| `defaultScan.workers.*`        | min=1, max=10, reposPerWorker=50      | Tune by repo count and Renovate runtime.                                       |
| `defaultScan.discovery.*`      | autodiscover=true, requireConfig=true | Keep `requireConfig=true` for org-wide scans to avoid mass onboarding-PR spam. |

## Upgrades

```bash
helm upgrade renovate-operator \
  oci://ghcr.io/donaldgifford/charts/renovate-operator \
  --version 0.2.0 \
  --namespace renovate-system \
  -f homelab-values.yaml
```

CRDs are kept across upgrades (`crd.keep: true` is the chart default). When a
CRD schema changes, the chart's CRD templates ship the new shape — Helm will
patch the existing CRDs. In-flight Runs continue against their _frozen snapshot_
and aren't affected by mid-flight Platform/Scan edits.

## Uninstall

```bash
helm uninstall renovate-operator --namespace renovate-system
kubectl delete crd renovateplatforms.renovate.fartlab.dev \
                  renovatescans.renovate.fartlab.dev \
                  renovateruns.renovate.fartlab.dev
kubectl delete namespace renovate-system
```

CRDs are not deleted by `helm uninstall` (chart sets
`helm.sh/resource-policy: keep` on them) — delete them explicitly if you really
want a clean slate. This will cascade-delete every Platform / Scan / Run on the
cluster.

## Troubleshooting

### Chart install fails with `defaultScan.enabled=true but defaultScan.platformRef.name is empty`

By design. The chart's default-scan template aborts when you've told it to ship
a Scan but haven't told it which Platform the Scan should reference.

Fix: set `defaultScan.platformRef.name` to a `RenovatePlatform` name, or set
`defaultScan.enabled=false`.

### Operator pod is `CrashLoopBackOff`

```bash
kubectl -n renovate-system logs deploy/renovate-operator-controller-manager --previous
kubectl -n renovate-system describe pod -l control-plane=controller-manager
```

Common causes:

| Log signature                                                    | Likely cause                                                               | Fix                                                                                        |
| ---------------------------------------------------------------- | -------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------ |
| `panic: close of closed channel` in `signals.SetupSignalHandler` | Bug in <0.1.0 (pre-release) where the signal handler was registered twice. | Upgrade to ≥ 0.1.0.                                                                        |
| `unable to fetch RenovatePlatform/<name>` (repeating)            | Platform CRD missing or RBAC stripped                                      | Re-`helm upgrade` to restore CRDs + RBAC.                                                  |
| `failed to wait for cache sync`                                  | API server slow / pod scheduled before CRDs registered                     | Operator retries; usually self-resolves. If persistent, check `kubectl get crd`.           |
| OOMKilled in pod events                                          | Operator is reconciling thousands of Scans/Runs and 512Mi isn't enough     | Raise `resources.limits.memory`; check for runaway Run history with `kubectl get rrun -A`. |

### `RenovatePlatform` stuck `Ready=False`

Inspect the condition:

```bash
kubectl get rplatform <name> -o jsonpath='{.status.conditions[?(@.type=="Ready")]}{"\n"}'
```

| `reason`              | Meaning                                         | Fix                                                                                                                                      |
| --------------------- | ----------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------- |
| `SecretNotFound`      | Secret doesn't exist in operator namespace      | Create the Secret in the operator's release namespace (e.g., `renovate-system`), not the Scan's namespace.                               |
| `KeyMissing`          | Secret exists but the named key isn't in `data` | `kubectl get secret <name> -o yaml` and confirm the `key` field matches a `data` entry.                                                  |
| `AuthFailed`          | PEM parse or token format failure               | For App: `openssl rsa -in key.pem -check`. For token: ensure no trailing newline (`tr -d '\n' < token > token-clean`).                   |
| `PlatformUnreachable` | API endpoint doesn't respond                    | Check `baseURL` (Forgejo needs explicit). For GitHub Enterprise behind a proxy, set `HTTPS_PROXY` via `controllerManager.container.env`. |

### `RenovateScan` not scheduling

```bash
kubectl get rscan <name> -o yaml | yq .status
```

| `Ready.reason`                               | Meaning                                                      | Fix                                                                                 |
| -------------------------------------------- | ------------------------------------------------------------ | ----------------------------------------------------------------------------------- |
| `InvalidSchedule`                            | Cron expression won't parse                                  | Use 5-field cron (no seconds). Validate with [crontab.guru](https://crontab.guru/). |
| `PlatformNotReady`                           | Referenced Platform's `Ready != True`                        | Fix the Platform first; the Scan reconciles automatically.                          |
| `Suspended`                                  | `spec.suspend: true`                                         | Toggle off.                                                                         |
| (no `Scheduled` condition after a fire time) | Concurrency policy `Forbid` and an active Run is still going | Wait, or check `kubectl get rrun -n <ns>` for stuck Runs.                           |

### `RenovateRun` stuck `Discovering`

Discovery hits the platform API. Look at operator logs filtered to that Run:

```bash
kubectl -n renovate-system logs deploy/renovate-operator-controller-manager \
  | grep -E '"run":"<run-name>"|"scan":"<scan-name>"'
```

Watch for rate-limit warnings (GitHub App: 4500 req/hr per installation;
Forgejo: per-token configurable). If the discovery is filter-heavy and exhausts
the budget, lower `discovery.filter` cardinality or reduce schedule frequency.

### Worker pods rejected by PodSecurity admission

Symptom: `RenovateRun` sits with no `Status.Phase` and no worker pod ever
appears. Controller logs include lines like:

```text
would violate PodSecurity "restricted:latest": allowPrivilegeEscalation != false (...),
unrestricted capabilities (...), runAsNonRoot != true (...), seccompProfile (...)
```

The Run's namespace has
`pod-security.kubernetes.io/enforce=restricted` (or stricter) and the
operator image is older than v0.1.2. v0.1.0 / v0.1.1 shipped a worker
pod template missing the four PSA `restricted` fields; v0.1.2 added
them ([SECURITY.md](../../SECURITY.md) → "Pod Security"). Upgrade:

```bash
helm upgrade -n renovate-system renovate-operator \
  oci://ghcr.io/donaldgifford/charts/renovate-operator --version 0.1.2 \
  -f values.yaml --reuse-values
kubectl rollout restart deployment/renovate-operator-controller-manager -n renovate-system
```

If you're running the unreleased `dev-ci` image while iterating on a PR,
make sure the deployment's `imagePullPolicy` is `Always` and rollout
restart after each push.

### Worker `Job` `BackoffLimitExceeded`

Renovate CLI itself errored on one or more shards.

```bash
kubectl -n <scan-ns> get jobs                      # find the worker Job
kubectl -n <scan-ns> describe job <run>-worker     # see backoff state
kubectl -n <scan-ns> logs -l job-name=<run>-worker --all-containers --tail=200
```

Common causes: bad `presetRepoRef`, network egress blocked from the worker pod,
`requireConfig: false` + thousands of repos triggering rate limits within the
worker.

### Metrics not scraped

```bash
kubectl -n renovate-system get servicemonitor renovate-operator-controller-manager-metrics-monitor -o yaml
kubectl -n renovate-system get svc renovate-operator-controller-manager-metrics-service
```

If the ServiceMonitor isn't there, `metrics.serviceMonitor.enabled` was `false`
at install time. Re-`helm upgrade` with it set to `true`.

If it is there but Prometheus isn't picking it up, your Prometheus selector
doesn't match — adjust `metrics.serviceMonitor.additionalLabels` to a label your
Prometheus instance watches. Default ships `release: kube-prometheus-stack`.

### Scrape healthy but no series in Prometheus

Symptom: `prometheus.../api/v1/targets` shows the operator scrape target as
`health=up` with no `lastError`, yet querying any `renovate_operator_*` metric
returns an empty result, and `prometheus.../api/v1/label/__name__/values`
contains zero series matching `renovate`.

The auth filter is rejecting Prometheus's bearer token with `403 Forbidden`,
but `kube-prometheus-stack` (and many other Prom flavors) treats any HTTP
response as "up" if the TLS handshake succeeded. The empty TSDB is the real
signal.

Confirm with a direct curl from inside the cluster:

```bash
TOKEN=$(kubectl -n renovate-system create token renovate-operator-controller-manager)
kubectl -n renovate-system port-forward deploy/renovate-operator-controller-manager 8443:8443 &
sleep 1
curl -ski -H "Authorization: Bearer $TOKEN" https://localhost:8443/metrics | head -5
```

If the response starts with `HTTP/1.1 403 Forbidden` and a body like
`Authorization denied for user system:serviceaccount:...`, Prometheus's
`ServiceAccount` lacks the `renovate-operator-metrics-reader` `ClusterRole`.
Bind it — see *Authorize Prometheus to scrape `/metrics`* in the Configuration
reference above.

### Tracing not exporting

`tracing.enabled: true` alone is a no-op when `otlpEndpoint` is empty (the
operator initializes a no-op tracer provider so tracing call sites still work).
Set `tracing.otlpEndpoint` to your collector's gRPC endpoint (`tempo:4317`,
`otel-collector:4317`, …).

If `otlpEndpoint` is set and you still see no spans:

```bash
kubectl -n renovate-system logs deploy/renovate-operator-controller-manager | grep -i otel
```

Look for `failed to dial otlp` (network), `auth required` (collector's TLS
config), or schema-URL conflicts (would have shown up at startup).

### pprof not reachable

`pprof.enabled` is `false` by default. After enabling, port-forward:

```bash
kubectl -n renovate-system port-forward deploy/renovate-operator-controller-manager 8082:8082
go tool pprof http://localhost:8082/debug/pprof/heap
```

Never expose `pprof.port` via Service / Ingress — it's a debug surface, not an
operator API.

## Further reading

- [`test/manual/README.md`](../../test/manual/README.md): full homelab
  acceptance runbook with two scenarios (GitHub App, Forgejo token), kubectl
  commands, and a Repo handoff section covering branch protection + cosign
  verify + helm OCI pull one-off checks.
- [`contrib/`](../../contrib/): Grafana dashboards, standalone Prometheus rules,
  Alloy config — drop-in observability for users who don't ship the chart's
  ServiceMonitor / PrometheusRule.
- [`AGENTS.md`](../../AGENTS.md): kubebuilder development guide if you're
  contributing.
