---
id: IMPL-0001
title: "renovate-operator v0.1.0 implementation"
status: In Progress
author: Donald Gifford
created: 2026-04-27
---
<!-- markdownlint-disable-file MD025 MD041 -->

# IMPL 0001: renovate-operator v0.1.0 implementation

**Status:** In Progress
**Author:** Donald Gifford
**Date:** 2026-04-27

<!--toc:start-->
- [Objective](#objective)
- [Scope](#scope)
  - [In Scope](#in-scope)
  - [Out of Scope](#out-of-scope)
- [Implementation Phases](#implementation-phases)
  - [Phase 1: API surface](#phase-1-api-surface)
    - [Tasks](#tasks)
    - [Success Criteria](#success-criteria)
  - [Phase 2: Pure builders and shared utilities](#phase-2-pure-builders-and-shared-utilities)
    - [Tasks](#tasks-1)
    - [Success Criteria](#success-criteria-1)
  - [Phase 3: Platform clients and discovery](#phase-3-platform-clients-and-discovery)
    - [Tasks](#tasks-2)
    - [Success Criteria](#success-criteria-2)
  - [Phase 4: Reconcilers](#phase-4-reconcilers)
    - [Tasks](#tasks-3)
    - [Success Criteria](#success-criteria-3)
  - [Phase 5: Observability](#phase-5-observability)
    - [Tasks](#tasks-4)
    - [Success Criteria](#success-criteria-4)
  - [Phase 6: Helm chart, samples, and contrib tree](#phase-6-helm-chart-samples-and-contrib-tree)
    - [Tasks](#tasks-5)
    - [Success Criteria](#success-criteria-5)
  - [Phase 7: Testing](#phase-7-testing)
    - [Tasks](#tasks-6)
    - [Success Criteria](#success-criteria-6)
  - [Phase 8: CI/CD and release](#phase-8-cicd-and-release)
    - [Tasks](#tasks-7)
    - [Success Criteria](#success-criteria-7)
  - [Phase 9: Homelab deploy and v0.1.0 cutover](#phase-9-homelab-deploy-and-v010-cutover)
    - [Tasks](#tasks-8)
    - [Success Criteria](#success-criteria-8)
- [File Changes](#file-changes)
- [Testing Plan](#testing-plan)
- [Dependencies](#dependencies)
- [Resolved Open Questions](#resolved-open-questions)
  - [Q1 — Renovate image version pin](#q1--renovate-image-version-pin)
  - [Q2 — Rate limiter sizing](#q2--rate-limiter-sizing)
  - [Q3 — Metric label cardinality](#q3--metric-label-cardinality)
  - [Q4 — GitHub discovery: REST list vs Search API](#q4--github-discovery-rest-list-vs-search-api)
  - [Q5 — e2e GitHub fidelity](#q5--e2e-github-fidelity)
  - [Q6 — cert-manager template](#q6--cert-manager-template)
  - [Q7 — Image registry path and image build mechanism](#q7--image-registry-path-and-image-build-mechanism)
  - [Q8 — ServiceMonitor / PrometheusRule label defaults](#q8--servicemonitor--prometheusrule-label-defaults)
  - [Q9 — Worker entrypoint shell content](#q9--worker-entrypoint-shell-content)
  - [Q10 — Default chart appVersion behavior](#q10--default-chart-appversion-behavior)
- [References](#references)
<!--toc:end-->

## Objective

Take `renovate-operator` from its current scaffolded state to a tagged v0.1.0 release deployed against the homelab Talos cluster, producing real Renovate PRs against `donaldgifford/server-price-tracker` (GitHub) and one Forgejo repo. v0.1.0 satisfies every goal locked in [DESIGN-0001](../design/0001-renovate-operator-v0-1-0.md): three CRDs end-to-end, GitHub App + Forgejo platforms, parallel workers via Indexed Jobs, full observability surface, signed multi-arch image, OCI Helm chart.

**Implements:** [RFC-0001](../rfc/0001-build-kubebuilder-renovate-operator.md), [DESIGN-0001](../design/0001-renovate-operator-v0-1-0.md), and [ADRs 0001–0008](../adr/).

## Scope

### In Scope

- All three CRDs (`RenovatePlatform`, `RenovateScan`, `RenovateRun`) implemented and reconciled end-to-end.
- Two platforms: GitHub App auth (per-installation) and Forgejo token auth.
- Indexed Job worker sharding with `clamp(ceil(repos/reposPerWorker), minWorkers, maxWorkers)`.
- Default `RenovateScan` shipped via the Helm chart with `requireConfig: true` ([ADR-0008](../adr/0008-default-scan-via-helm-chart.md)).
- `[]metav1.Condition` status, `observedGeneration`, printer columns, typed `phase` cursor on Run.
- Prometheus metrics, OTel tracing on hot paths, pprof endpoint, structured logs with trace_id/span_id.
- `contrib/` tree: four Grafana dashboards, Prometheus alerts + recording rules, Alloy snippet.
- CI: lint, unit, envtest, kind-based e2e, multi-arch image build, cosign signing, syft SBOM, Helm OCI push.
- Homelab deploy producing real Renovate PRs on GitHub and Forgejo.

### Out of Scope

Carried verbatim from DESIGN-0001's Non-Goals: webhook-triggered runs, additional platforms (GitLab/Bitbucket/ADO), built-in UI, mid-run worker rescaling, conversion webhooks, worker-side pprof, multi-cluster fan-out. Validation webhooks for cross-resource references are also out of scope (soft validation via conditions is the v0.1.0 answer).

## Implementation Phases

Each phase is a coherent chunk that can land as one or two PRs. Phases roughly stack: types must exist before reconcilers, reconcilers before e2e, e2e before release.

---

### Phase 1: API surface

Translate [DESIGN-0001 § Type definitions](../design/0001-renovate-operator-v0-1-0.md#type-definitions) into Go types under `api/v1alpha1/`. No reconciler code yet — pure schema work.

#### Tasks

- [x] Create `api/v1alpha1/shared_types.go` with `SecretKeyReference`, `LocalObjectReference`, `ConcurrencyPolicy`, `PlatformType` (constants `github`, `forgejo`), `RunPhase` (constants `Pending`/`Discovering`/`Running`/`Succeeded`/`Failed`).
- [x] Replace placeholder fields in `renovateplatform_types.go`:
  - [x] `RenovatePlatformSpec`: `platformType`, `baseURL`, `auth` (`PlatformAuth` discriminated union), `runnerConfig` (`*runtime.RawExtension` with `+kubebuilder:pruning:PreserveUnknownFields`), `presetRepoRef`, `renovateImage` (default per [Resolved Q1](#q1--renovate-image-version-pin)).
  - [x] `PlatformAuth` with `GitHubApp` (App ID, required `installationID`, PEM secret ref) and `Token` (token secret ref).
  - [x] `RenovatePlatformStatus`: conditions, `observedGeneration`.
  - [x] CEL `XValidation` markers on `Spec`/`Auth`: exactly-one-of `auth.{githubApp,token}`; `forgejo` ⇒ `token`; `forgejo` ⇒ `baseURL` non-empty.
  - [x] Printer columns: `Type`, `URL`, `Ready`, `Age`.
- [x] Replace placeholder fields in `renovatescan_types.go`:
  - [x] `RenovateScanSpec`: `platformRef`, `schedule`, `timeZone` (default UTC), `suspend`, `concurrencyPolicy` (default `Forbid`), `Workers{Min=1,Max=10,ReposPer=50,BackoffLimitPerIndex=2}`, `Discovery{Autodiscover=true,RequireConfig=true,Filter,Topics,SkipForks=true,SkipArchived=true}`, `renovateConfigOverrides` (preserved JSON), `extraEnv`, `resources`, `successfulRunsHistoryLimit=3`, `failedRunsHistoryLimit=1`.
  - [x] `RenovateScanStatus`: conditions (`Ready`, `Scheduled`), `lastRunTime`, `lastSuccessfulRunTime`, `nextRunTime`, `lastRunRef`, `activeRuns`, `observedGeneration`.
  - [x] Printer columns: `Platform`, `Schedule`, `Last Run`, `Next Run`, `Ready`, `Age`.
- [x] Replace placeholder fields in `renovaterun_types.go`:
  - [x] `RenovateRunSpec`: `scanRef`, `platformSnapshot RenovatePlatformSpec`, `scanSnapshot RenovateScanSpec`.
  - [x] `RenovateRunStatus`: conditions (`Started`, `Discovered`, `Succeeded`, `Failed`), `phase RunPhase`, lifecycle timestamps (`startTime`, `discoveryCompletionTime`, `workersStartTime`, `completionTime`), `discoveredRepos`, `actualWorkers`, `succeededShards`, `failedShards`, `shardConfigMapRef`, `workerJobRef`, `observedGeneration`.
  - [x] Printer columns: `Scan`, `Phase`, `Repos`, `Workers`, `Started`, `Completed`.
- [x] Run `make manifests generate`; resolve any controller-gen warnings.
- [x] Add realistic example CRs to `config/samples/` (GitHub Platform, Forgejo Platform, a Scan, replacing the kubebuilder defaults). Verify `kubectl apply --dry-run=server -f config/samples/` succeeds against the installed CRDs.
- [x] `just lint` clean.

#### Success Criteria

- `kubectl apply -f config/crd/bases` installs three CRDs on a fresh kind cluster.
- `kubectl explain renovateplatform.spec.auth` returns useful field docs with the discriminated union surfaced.
- `kubectl apply` of a malformed Platform (e.g., both `githubApp` and `token` set, or `forgejo` without `baseURL`) is rejected with the expected CEL message.
- `kubectl get rp/rs/rr` printer columns match the design.
- DeepCopy methods generated; `go build ./...` clean.

---

### Phase 2: Pure builders and shared utilities

Side-effect-free helpers that the reconcilers will compose with. Should be 100%-coverage table-tested; everything stays in `internal/`.

#### Tasks

- [x] `internal/clock/clock.go`: thin wrapper around `k8s.io/utils/clock` so reconcilers and tests can swap implementations (`clock.Clock` interface, real + fake).
- [x] `internal/conditions/conditions.go`: thin helpers around `meta.SetStatusCondition` for the condition types this project uses (so reconcilers don't repeat the same boilerplate). Lint should reject direct `append` to condition slices elsewhere.
- [x] `internal/sharding/shard_builder.go` (pure): given `[]Repository` + `WorkersSpec`, produce `actualWorkers` and the shard ConfigMap data (`shard-NNNN.json` keys, optional gzip+base64 above 900 KiB). Contract:
  - [x] Round-robin assignment across `actualWorkers`.
  - [x] `actualWorkers = clamp(ceil(len/reposPerWorker), min, max)` with `min,max ≥ 1`.
  - [x] Stable across runs given the same input ordering (sort first).
- [x] `internal/jobspec/job_builder.go` (pure): given `*RenovateRun` + shard `*ConfigMap` → `*batchv1.Job`. Implements every detail from [DESIGN-0001 § Job builder](../design/0001-renovate-operator-v0-1-0.md#job-builder-internalcontrollerjob_buildergo), including the env-var assembly order and the inline shell entrypoint (locked single-container shape per [Resolved Q9](#q9--worker-entrypoint-shell-content); no init container).
- [x] `internal/credentials/mirror.go`: pure-ish helpers to construct the mirrored Secret (name, owner ref, labels, data copy); the I/O happens in the Run controller.
- [x] Table-driven tests for each builder under `*_test.go`. Aim for 100% branch coverage.

#### Success Criteria

- `go test ./internal/sharding/... ./internal/jobspec/... ./internal/credentials/... ./internal/conditions/... ./internal/clock/...` passes with `-race -coverprofile=...` and prints `coverage: 100.0% of statements` (or with explicitly annotated exclusions for unreachable branches).
- The shard builder's gzip path triggers above 900 KiB and not below; verified by table case.
- The job builder produces an Indexed Job with `parallelism = completions = actualWorkers`, correct env-var order (Platform → auth → discovery → repos → RENOVATE_CONFIG → tracing → extraEnv), correct owner refs, and the labels documented in DESIGN-0001.

---

### Phase 3: Platform clients and discovery

Implements the `Client` interface for GitHub (App auth) and Forgejo (token), including `Discover` and `HasRenovateConfig`. This is where the rate-limit token bucket lives.

#### Tasks

- [x] `internal/platform/platform.go`: `Client` interface, `Repository`, `DiscoveryFilter`, error types (transient vs permanent).
- [x] `internal/platform/github/`:
  - [x] `client.go`: `go-github/v62` + `bradleyfalzon/ghinstallation/v2`; constructs an installation-scoped client from `GitHubAppAuth`. PAT auth via custom `tokenTransport`. Per-installation `golang.org/x/time/rate` token bucket sized per [Resolved Q2](#q2--rate-limiter-sizing); GitHub's primary `RateLimitError`, secondary `AbuseRateLimitError`, and 429 responses all classify to `*platform.RateLimitedError` (which unwraps to `ErrTransient`). 401/403 → `ErrUnauthorized`; 404 → `ErrNotFound`; 5xx → `ErrTransient`.
  - [x] `discover.go`: list repos via `/orgs/{org}/repos` paginated (Search API path is a future optimization — see [Resolved Q4](#q4--github-discovery-rest-list-vs-search-api)); falls back to `/users/{user}/repos` on 404 for personal accounts. Filter/Topics/SkipForks/SkipArchived applied client-side (matches Renovate autodiscover semantics).
  - [x] `has_config.go`: contents-API check across the five `platform.ConfigPaths`; first 200 wins, 404s fall through, any other error short-circuits.
- [x] `internal/platform/forgejo/`:
  - [x] `client.go`: `code.gitea.io/sdk/gitea`; token-authenticated. 30 req/sec rate limiter per Resolved Q2; same classifyErr → ErrTransient/ErrPermanent/ErrUnauthorized/ErrNotFound mapping as the GitHub client.
  - [x] `discover.go`: `/api/v1/orgs/{owner}/repos` paginated with /users/{user}/repos fallback on 404. Skip-forks/skip-archived + glob patterns applied client-side; topics deferred (Forgejo SDK doesn't surface topics on the repo struct).
  - [x] `has_config.go`: contents API probe across `platform.ConfigPaths`; same first-hit-wins shape as GitHub.
- [x] ~~`dnaeon/go-vcr` fixtures~~ → **httptest fake servers** under `internal/platform/{github,forgejo}/client_test.go` covering: happy path (60 repos paginated for GitHub), 404 on `HasRenovateConfig`, 401/403 auth failure, 429 rate limit (verifying `*RateLimitedError` and `errors.Is(err, ErrTransient)`), malformed JSON (verifying it doesn't classify as transient), 5xx → ErrTransient, /orgs/{owner} 404 → /users/{owner} fallback.
  - **Decision:** dropped VCR for unit tests. VCR's value is *replaying recorded real-API responses*, but we have no real Forgejo or GitHub Enterprise instance to record against during dev, and recording against github.com pollutes the recordings with installation tokens and live rate-limit headers. httptest fakes write the API shape inline in the test body — they're deterministic, easier to read, and don't require a fixtures regeneration story. VCR may still be added in Phase 7 e2e if we need to replay recorded fixtures from the homelab Forgejo instance.
- [x] Unit tests against the httptest fakes for both clients.

#### Success Criteria

- `go test ./internal/platform/...` passes against recorded fixtures.
- A discovery against an org with 1k repos in fixture form completes in `< 30s` simulated wall-clock with the rate limiter engaged.
- The rate limiter degrades gracefully under simulated 429s (sleeps the indicated retry-after, retries once, surfaces the error if the second attempt also rate-limits).
- Both clients implement the same `Client` interface; reconcilers should not import platform-specific packages.

---

### Phase 4: Reconcilers

The bulk of the operator. Each controller gets its own file and envtest suite. Built on top of phases 1–3.

#### Tasks

- [x] **Platform controller (`internal/controller/renovateplatform_controller.go`)**:
  - [x] Resolve Secret in operator namespace (driven by `auth.{githubApp.privateKeyRef,token.secretRef}`).
  - [x] For GitHubApp: PEM-parse the private key (PKCS1 or PKCS8); JWT minting + `/app` health-check deferred to a v0.2 hardening pass — the Run controller's actual API calls catch a bad key.
  - [x] For Token: read token, validate non-empty.
  - [x] Set `Ready=True/False` with `Reason ∈ {CredentialsResolved, SecretNotFound, KeyMissing, AuthFailed}`.
  - [x] Watch Platform + Secret (operator-namespace predicate, mapped to Platforms whose auth refs match by name).
- [x] **Scan controller (`internal/controller/renovatescan_controller.go`)**:
  - [x] Parse cron via `robfig/cron/v3` against `spec.timeZone`; surface invalid as `Ready=False/Reason=InvalidSchedule`.
  - [x] Resolve `platformRef` → Platform; require `Ready=True`. Else `Ready=False/Reason=PlatformNotReady`, requeue 60s.
  - [x] Honor `suspend`; honor `concurrencyPolicy` against active Runs (`Forbid` → skip+requeue at next fire, `Allow` → always create, `Replace` → equivalent to Forbid + warning log per [DESIGN-0001 resolution #7](../design/0001-renovate-operator-v0-1-0.md#resolved-open-questions)).
  - [x] Create `RenovateRun` at fire time, snapshotting Platform spec + Scan spec into `spec.{platformSnapshot,scanSnapshot}`.
  - [x] GC old terminal Runs per `successfulRunsHistoryLimit`/`failedRunsHistoryLimit`.
  - [x] Set `Scheduled=True`, `RequeueAfter = nextRunTime - now` capped at 5m.
  - [x] Watches: Scan + Platform (mapped) + Run (owned).
- [x] **Run controller (`internal/controller/renovaterun_controller.go`)** — state machine per [DESIGN-0001 § Reconciler: RenovateRun](../design/0001-renovate-operator-v0-1-0.md#reconciler-renovaterun):
  - [x] `Pending` → `Discovering`: set startTime, set `Started=True`, instantiate platform client from snapshot via `PlatformClientFactory`, mirror credential Secret into Run's namespace via `internal/credentials.BuildMirror`.
  - [x] `Discovering`: call `platform.Discover`, apply `requireConfig` filter (sequential for v0.1.0 — `errgroup` is a perf hardening; correctness comes first), compute `actualWorkers` via `internal/sharding.Build`, create shard ConfigMap (owner-ref'd to Run), build worker Job via `internal/jobspec.BuildWorkerJob`, transition to `Running` with `Discovered=True`. Idempotent — re-Get on each step short-circuits when the resource already exists.
  - [x] `Running`: read owned Job's `Status.{Succeeded,Failed}`, update `succeededShards`/`failedShards`, transition to `Succeeded` (`succeeded == completions`) or `Failed` (`JobFailed` condition).
  - [x] Terminal phases: no further work; rely on parent Scan's history-limit GC.
  - [x] Watches: Run + owned Job + owned ConfigMap + owned Secret.
- [x] Cluster RBAC markers (`+kubebuilder:rbac:...`) per controller; `make manifests` regenerates `config/rbac/role.yaml` with secrets / configmaps / batch.jobs full verbs for the Run controller's mirror + ConfigMap + Job ops.
- [x] Wire the three controllers into `cmd/main.go`'s manager, with the existing kubebuilder leader-election defaults. New `--operator-namespace` flag (defaults to `$POD_NAMESPACE`, fallback `renovate-system`) feeds Platform + Run reconcilers' source-Secret resolution.

#### Success Criteria

- `kubectl apply -f` of Platform → Scan with a cron `* * * * *` produces a Run within one minute, and the Run reaches `phase=Succeeded` against a stub platform.
- A Platform with a missing Secret transitions to `Ready=False/SecretNotFound`; replacing the Secret transitions back to `Ready=True` without operator restart.
- `kubectl delete renovatescan ...` cascades to all child Runs, owned Jobs, owned ConfigMaps, and mirrored Secrets within 30 seconds (no orphans).
- envtest suite green for all three controllers; coverage for `internal/controller/...` ≥ 80%.

---

### Phase 5: Observability

Metrics, tracing, logging bridge, pprof. Wired to the manager so they're alive from process start.

#### Tasks

- [x] `internal/observability/metrics.go`: register custom collectors per [DESIGN-0001 § Metrics](../design/0001-renovate-operator-v0-1-0.md#metrics) on controller-runtime's `metrics.Registry`. Includes:
  - [x] Counters: `renovate_operator_runs_total{scan,platform,result}`, `renovate_operator_discovery_errors_total{scan,platform}`, `renovate_operator_shards_failed_total{scan,platform}`.
  - [x] Histograms: `renovate_operator_run_duration_seconds{scan,platform}`, `renovate_operator_discovery_duration_seconds{scan,platform}`.
  - [x] Gauges: `renovate_operator_active_runs{scan,platform}`, `renovate_run_shard_count{scan,platform}`.
  - [x] Label set is `{scan, platform, result}` only — **no `scan_namespace`** per [Resolved Q3](#q3--metric-label-cardinality).
- [x] `internal/observability/tracing.go`: `InitTracer(ctx, version)` that returns a no-op shutdown when `OTEL_EXPORTER_OTLP_ENDPOINT` is unset; otherwise builds an OTLP gRPC exporter + sdktrace TracerProvider.
- [x] `internal/observability/logbridge.go`: `LogrFromContext` wrapper that pulls the active span from the reconcile context and adds `trace_id` / `span_id` keys.
- [x] `internal/observability/pprof.go`: `net/http/pprof` mux on `--pprof-bind-address` (empty disables); off by default.
- [x] Wire all four into `cmd/main.go`. Health/readiness endpoints (kubebuilder defaults at `:8081`) stay as-is.
- [x] Add tracing spans to hot paths: `RenovateRun.Reconcile` (with `scan`, `platform`, `phase.in`/`phase.out` attrs and `codes.Error` on failure), `RenovateRun.DiscoverAndDispatch`, `platform.Discover` (carrying `platform`/`owner` and `repos.found`), `platform.HasRenovateConfig.batch` (with `repos.candidates`/`repos.with_config`), `RenovateRun.EnsureWorkerJob` (with `workers` count covering BuildWorkerJob), and `RenovateRun.ObserveJob` (with `job.succeeded`/`job.failed`). All routed through `observability.Tracer()`; the default no-op TracerProvider keeps these zero-cost when InitTracer hasn't been called.

#### Success Criteria

- `curl localhost:8080/metrics` returns the project metrics alongside controller-runtime's defaults.
- A test envtest run with `OTEL_EXPORTER_OTLP_ENDPOINT=...` surfaces spans for at least one full reconcile.
- Log lines emitted during a Run carry `trace_id` / `span_id` keys when tracing is enabled.
- pprof endpoint reachable on `:8082` only when `--pprof-bind-address=:8082` is set.

---

### Phase 6: Helm chart, samples, and contrib tree

Polish the kubebuilder-scaffolded chart into the values surface DESIGN-0001 specifies, ship a default Scan, and land the dashboards/alerts.

#### Tasks

- [x] Replace `dist/chart/values.yaml` with the surface in [DESIGN-0001 § values.yaml](../design/0001-renovate-operator-v0-1-0.md#valuesyaml-top-level-surface): image, replicas+leaderElect, resources, metrics + ServiceMonitor + PrometheusRule (gated), tracing, pprof, logging level/format, `defaultScan` block, worker resources defaults. The legacy `controllerManager` block is kept alongside for backward compat with the kubebuilder-scaffolded `manager.yaml` template.
- [x] `dist/chart/templates/extra/default-scan.yaml` gated by `.Values.defaultScan.enabled` per [ADR-0008](../adr/0008-default-scan-via-helm-chart.md). Use `helm.sh/resource-policy: keep`.
- [x] `dist/chart/templates/extra/servicemonitor.yaml` gated by `.Values.metrics.serviceMonitor.enabled`. Default `additionalLabels: {release: kube-prometheus-stack}` per [Resolved Q8](#q8--servicemonitor--prometheusrule-label-defaults).
- [x] `dist/chart/templates/extra/prometheusrule.yaml` gated by `.Values.metrics.prometheusRule.enabled`. Recording rules + four conservative alerts (failure ratio, discovery errors, shard failures, stuck run).
- [x] Pre-install validation: template guard in `default-scan.yaml` calls `fail` when `defaultScan.enabled=true && defaultScan.platformRef.name == ""`. Verified by `helm lint dist/chart` (fails) and `helm lint dist/chart --set defaultScan.enabled=false` (clean).
- [x] Strip the kubebuilder-scaffolded `dist/chart/templates/certmanager/` per [Resolved Q6](#q6--cert-manager-template); `make chart-regenerate` wraps `kubebuilder edit --plugins=helm/v1-alpha --force` + `make chart-clean` so the cert-manager template is re-stripped automatically. NOTES.txt + values.yaml comment document cert-manager as a future-webhook prerequisite. The `metrics-service.yaml` and `manager.yaml` templates were also de-cert-manager-ed; legacy `prometheus/monitor.yaml` falls back to insecureSkipVerify.
- [x] `contrib/grafana/dashboards/{operator,runs,traces,logs}.json` per [ADR-0007](../adr/0007-observability-stack.md). All four dashboards declare their datasource via `__inputs` so Grafana prompts at import.
- [x] `contrib/prometheus/{alerts,recording-rules}.yaml` — standalone `PrometheusRule` resources mirroring the chart-bundled copy. Includes a `RenovateOperatorPodNotReady` alert keyed off `kube-state-metrics` that the chart copy omits (chart copy is keyed on operator-emitted metrics only).
- [x] `contrib/alloy/operator.river` — Kubernetes pod scrape + Loki log forwarding (with JSON `level`/`trace_id`/`scan`/`platform` extraction) + OTLP receiver-to-Tempo block.
- [x] `contrib/README.md` indexing how to import each, including the inter-dashboard pivot path (runs → logs via scan/platform; logs → traces via `trace_id`).
- [x] Custom lint: `scripts/lint-metrics-coverage.sh` (wired as `make metrics-coverage-lint`) parses `Name: "..."` lines from `internal/observability/metrics.go` and greps `contrib/` + the chart PrometheusRule. Exempt via `// metric:internal` on the same line. All 7 current metrics covered.

#### Success Criteria

- `helm lint dist/chart` clean.
- `helm template dist/chart --set defaultScan.enabled=true --set defaultScan.platformRef.name=github` renders without errors and includes a `RenovateScan/default`.
- `helm template dist/chart --set defaultScan.enabled=true` (no platformRef) **fails** with the expected message.
- A homelab `helm install ... --set ...` produces a running operator pod, a default Scan, and dashboards visible in Grafana once imported.

---

### Phase 7: Testing

Unit + envtest layered tests are landed throughout phases 1–5. This phase is the e2e + coverage round.

#### Tasks

- [x] kind-based e2e harness skeleton in `test/e2e/` is refactored: cert-manager hook stripped (v0.1.0 ships no webhooks per ADR-0006); `BeforeSuite` builds the operator image, loads it into kind, and `helm upgrade --install`s the chart with `defaultScan.enabled=false`; `AfterSuite` runs `helm uninstall` + namespace delete. `make test-e2e` now wraps `CERT_MANAGER_INSTALL_SKIP=true`. Smoke specs verify pod runs, all three CRDs register, the manager logs `Starting manager`, and a Platform pointing at a non-existent Secret reaches `Ready=False, reason=SecretMissing` (real reconciler against a real apiserver — no Forgejo or worker stack required).
- [ ] kind-based e2e scenarios on top of the harness (deferred to a follow-up; manual runbook in `test/manual/README.md` covers the v0.1.0 acceptance path against real GitHub.com + homelab Forgejo):
  - [ ] **GitHub stub e2e** (per [Resolved Q5](#q5--e2e-github-fidelity)): apply Platform → Scan with `* * * * *`; assert Run reaches `Succeeded` within 5 minutes; assert metrics increment.
  - [ ] **Forgejo e2e**: real Forgejo container in the kind cluster (image is small); assert end-to-end run.
  - [x] **Parallelism (integration)**: covered as fake-client tests in `internal/controller` — `TestRunReconcile_Parallelism_200ReposCapsAtMaxWorkers` exercises 200 stub repos / maxWorkers=5 / reposPerWorker=50 → `actualWorkers == 4`, shard ConfigMap holds 4 entries, Job parallelism+completions == 4. `TestRunReconcile_Parallelism_BelowMinClampsUp` covers the `minWorkers` clamp (10 repos / 50 perWorker → ceil to 1 → clamped up to `min=2`). The full kind-based parallelism scenario stays open for the follow-up.
- [x] `test/manual/README.md` with the steps for the homelab `donaldgifford/server-price-tracker` and Forgejo manual runs. Includes Scenario A (GitHub.com App auth → spt) and Scenario B (homelab Forgejo token auth), each with Secret creation, Platform/Scan YAML, watch commands, acceptance checks (Scheduled cond, succeeded Run, PR opened, dashboard reconcile rate, runs_total counter), and a troubleshooting matrix.
- [x] `just ci` composite gate stays green: lint + test + build + license-check. Verified locally on 2026-04-26 — lint clean, all tests pass, `go build -o bin/manager cmd/main.go` succeeds, `go-licenses check` against the Apache-2.0/MIT/BSD/ISC/MPL-2.0 allow list passes (the warnings about non-Go code in `modern-go/reflect2`, `prometheus/client_golang`, `cespare/xxhash`, `golang.org/x/sys`, `golang.org/x/crypto/chacha20` are inspector-driven and don't represent license findings).
- [x] Coverage gate: target ≥ 80% on `internal/controller/...`, `internal/platform/...`, `internal/sharding/...`, `internal/jobspec/...`. Latest from `make test-coverage` (2026-04-29): `clock 100%`, `conditions 100%`, `controller 89.6%` ✓, `credentials 97.6%`, `jobspec 92.7%` ✓, `observability 88.1%`, `platform 100%` ✓, `platform/forgejo 93.3%` ✓, `platform/github 92.4%` ✓, `sharding 92.0%` ✓. Lifted by fake.NewClientBuilder() tests for the IO helpers (mirrorCredential, ensureShardConfigMap, ensureWorkerJob, observeJob, refreshActiveRuns, gcOldRuns, createRun, scansForPlatform, platformsForSecret) plus targeted client-option tests for the platform SDKs. Observability lifted via funcr-based logbridge tests + an `OTEL_EXPORTER_OTLP_ENDPOINT=localhost:4317` happy-path test for `InitTracer` (the latter surfaced a real schema-URL conflict bug, fixed in the same change). All three Reconcile wrappers' status-conflict + status-update-error paths exercised via `WithInterceptorFuncs` fake-client interceptors. `classifyErr` paths exhaustively covered for both clients: github now hits the typed `*RateLimitError` and `*AbuseRateLimitError` branches in addition to status-code branches; forgejo hits the network-failure (no resp) branch and the unexpected-4xx default branch.

#### Success Criteria

- `just test-e2e` runs all three e2e scenarios on a fresh kind cluster and exits 0.
- `just test-coverage` reports ≥ 80% per the listed packages.
- CI on a PR runs the full gate (unit + envtest + e2e + lint) under 15 minutes.

---

### Phase 8: CI/CD and release

Workflows wired through `just`, multi-arch image with cosign + SBOM, OCI Helm chart push, semantic-release-style tag-driven flow.

#### Tasks

- [x] `.github/workflows/ci.yml` runs `make test-coverage` (replaces `just test`) plus golangci-lint, govulncheck, trivy, goreleaser-snapshot build, and a docker bake (`ci` target). Tightened the metadata-action `images:` ref from `ghcr.io/donaldgifford/` (broken trailing slash) to the canonical `ghcr.io/donaldgifford/renovate-operator`.
- [x] `.github/workflows/test-e2e.yml` gated on `paths:` filter — `api/**`, `internal/**`, `cmd/**`, `dist/chart/**`, `test/e2e/**`, Dockerfile, go.mod/sum, Makefile. Helm setup added so the chart-deployment harness step works.
- [x] `.goreleaser.yml` reconciled — `main: ./cmd` (kubebuilder layout), `-trimpath` + `-X main.version={{.Version}}` ldflags, SBOM block via syft (anchore/sbom-action). The build job in release.yml downloads syft so the goreleaser block can find it.
- [x] `.github/workflows/release.yml`: goreleaser block now also uploads `dist/install.yaml` (kustomize artifact) via `gh release upload`. Docker job runs `cosign sign` (keyless / OIDC) per pushed tag. New `helm-chart` job stamps the release tag onto Chart.yaml and `helm push`es to `oci://ghcr.io/donaldgifford/charts` so the resulting OCI ref is `ghcr.io/donaldgifford/charts/renovate-operator:<version>`, matching the Phase 9 install path.
- [x] `docker-bake.hcl` at the repo root with `default` (single-arch local), `ci` (multi-arch verify, no push), and `release` (multi-arch push to GHCR) targets. Variables: `REGISTRY` (defaults to ghcr.io/donaldgifford/renovate-operator), `TAG`, `VERSION`. CI's metadata-action merges in tag refs via the `bake-file-tags` output.
- [x] `make build-installer` artifact attached to the release via `gh release upload`. The release job builds it pre-goreleaser so a single workflow run covers both flows.
- [x] `dist/chart/values.yaml` `image.repository: ghcr.io/donaldgifford/renovate-operator` confirmed per [Resolved Q7](#q7--image-registry-path-and-image-build-mechanism). Chart.yaml hardened with home/sources/maintainers/keywords/icon and ArtifactHub annotations.
- [ ] Branch protection on `main`: require PR reviews, require `ci` workflow passing. (Repo-side configuration, not committed — note in homelab handoff.)

#### Success Criteria

- A tag push (`v0.1.0-rc.1` first, then `v0.1.0`) drives the full release pipeline and produces: signed multi-arch image, signed checksums, SBOM, OCI Helm chart, GitHub release with notes.
- `cosign verify ghcr.io/donaldgifford/renovate-operator:v0.1.0` succeeds with the keyless verifier.
- `helm pull oci://ghcr.io/donaldgifford/charts/renovate-operator --version 0.1.0` succeeds from a fresh machine.

---

### Phase 9: Homelab deploy and v0.1.0 cutover

Real cluster, real PRs.

#### Tasks

- [ ] Apply `RenovatePlatform/github` (App auth) — credential Secret arrives via the existing 1Password Connect operator in the operator namespace.
- [ ] Apply `RenovatePlatform/forgejo` (token auth) for the homelab Forgejo instance.
- [ ] `helm install renovate-operator oci://ghcr.io/donaldgifford/charts/renovate-operator --version 0.1.0 -f homelab-values.yaml` with `defaultScan.enabled=true, defaultScan.platformRef.name=github`.
- [ ] Watch the first scheduled Run reach `phase=Succeeded`.
- [ ] Verify a real Renovate PR is opened on `donaldgifford/server-price-tracker`.
- [ ] Repeat for the Forgejo platform with a Scan against one Forgejo repo.
- [ ] Import the four `contrib/grafana/dashboards/*.json` into the homelab Grafana; smoke-check operator + run dashboards.
- [ ] Apply the `contrib/prometheus/alerts.yaml` to homelab Prometheus.
- [ ] Status flips: RFC-0001 → Accepted; ADRs 0004–0008 → Accepted; DESIGN-0001 → Implemented; this IMPL doc → Completed.
- [ ] Pin Renovate image to the version that just shipped a real PR successfully (resolution from DESIGN-0001 — flip from `:latest` to a specific tag) before announcing externally.

#### Success Criteria

- Two real Renovate PRs visible (one GitHub, one Forgejo) and recognizable as produced by the operator.
- Homelab Grafana dashboards populated with non-zero data.
- One full week of weekly Scan runs without operator pod restarts or stuck Runs.
- Doc tree statuses reflect reality.

---

## File Changes

The repo is small enough that a per-file table is more noise than signal. The high-level shape:

| Tree | Action | Notes |
|------|--------|-------|
| `api/v1alpha1/` | Modify | Three `*_types.go` filled in; new `shared_types.go`; `zz_generated.deepcopy.go` regenerated. |
| `internal/clock/`, `internal/conditions/` | Create | Tiny utility packages. |
| `internal/sharding/`, `internal/jobspec/` | Create | Pure builders, exhaustively tested. |
| `internal/platform/{,github,forgejo}/` | Create | Client interface + two implementations + VCR fixtures. |
| `internal/credentials/` | Create | Mirror Secret helpers. |
| `internal/observability/` | Create | metrics, tracing, log-bridge, pprof. |
| `internal/controller/` | Modify | Three reconcilers fleshed out; envtest suite expanded; `cmd/main.go` wires manager flags. |
| `dist/chart/values.yaml` + `templates/extra/` | Modify | DESIGN-0001 values surface, default Scan, ServiceMonitor, PrometheusRule. |
| `config/samples/` | Modify | Realistic Platform/Scan examples replacing kubebuilder defaults. |
| `test/e2e/` | Modify | Three e2e scenarios. |
| `contrib/` | Create | Grafana dashboards, Prometheus rules, Alloy config, README index. |
| `.github/workflows/` | Modify | `ci.yml` rewired through `just`; `test-e2e.yml` added; `release.yml` reconciled with goreleaser. |
| `.goreleaser.yml` | Modify | Reconciled with kubebuilder Dockerfile and `cmd/main.go`. |

## Testing Plan

- **Unit (`*_test.go`)**: every pure builder under `internal/{sharding,jobspec,credentials,conditions,clock}` gets table-driven tests with 100% branch coverage. Platform clients use `dnaeon/go-vcr` recorded fixtures.
- **Controller (envtest)**: each reconciler gets a focused suite using stub platform clients (registered via the `Client` interface). Scenarios: each `Ready=False` reason, full Run state machine, concurrency policy matrix, cascade delete.
- **e2e (kind)**: three scenarios per Phase 7.
- **Manual**: documented in `test/manual/README.md` for the homelab cutover.

## Dependencies

- Kubebuilder 4.13.0 / Go 1.26.1 toolchain (pinned via `mise.toml`).
- Renovate image: `ghcr.io/renovatebot/renovate:latest` for v0.1.0 (per [Resolved Q1](#q1--renovate-image-version-pin)).
- cert-manager installed in the cluster as a prerequisite for any future webhook-bearing release (per [Resolved Q6](#q6--cert-manager-template)); not required for v0.1.0 itself.
- Homelab requires: Talos cluster reachable, 1Password Connect operator delivering the credential Secret to the operator namespace, Forgejo instance with API token, GitHub App installed on `donaldgifford` with read+PR scopes.
- Production-shape deploys: External Secrets Operator with a `ClusterSecretStore` (out of v0.1.0 scope but keep `internal/credentials/mirror.go` modular so the source-of-Secret swap is local).

## Resolved Open Questions

All ten questions have been answered. Decisions captured below; tasks above already reference the resolved answers.

### Q1 — Renovate image version pin

**Resolved: ship `:latest` for v0.1.0.** No version pin in the chart default. Revisit before any external announce / v0.1.x.

### Q2 — Rate limiter sizing

**Resolved: 4500 req/hr sustained + 100 burst per GitHub App installation; 30 req/sec for Forgejo.** GitHub `Retry-After` always honored on 429/secondary rate limits. Both knobs exposed in the operator's flag/values surface for override.

### Q3 — Metric label cardinality

**Resolved: drop `scan_namespace`; keep only `scan_name`.** Scans either share names across namespaces or all live in the same namespace, so the namespace label is dead weight. Final label set:

- Counters: `{scan_name, platform, result}`
- Histograms: `{scan_name, platform}`
- Gauges: `{scan_name, platform}` (or just `{platform}` for the cluster-wide ones)

Documented in [DESIGN-0001 § Metrics](../design/0001-renovate-operator-v0-1-0.md#metrics).

### Q4 — GitHub discovery: REST list vs Search API

**Resolved: REST `/orgs/{org}/repos` paginated.** The deprecation concern was checked — REST repo-list endpoints are **not deprecated**, no sunset notice, GraphQL is an alternative not a replacement. Search API stays as a v0.2.x optimization.

### Q5 — e2e GitHub fidelity

**Resolved: VCR fixture replay only for v0.1.0.** A `mock-github` container in CI is deferred. Forgejo e2e uses a real Forgejo container in the kind cluster as planned.

### Q6 — cert-manager template

**Resolved: strip the kubebuilder-scaffolded `dist/chart/templates/certmanager/certificate.yaml`.** v0.1.0 ships no webhooks. cert-manager is documented as a **deployment prerequisite** (not bundled by the chart) for future webhook-bearing releases. The helm plugin will re-emit the template on regeneration; the strip needs to happen post-regen via a chart-build script or a `make`/`just` target.

### Q7 — Image registry path and image build mechanism

**Resolved**:

- **Image** pushed to `ghcr.io/donaldgifford/renovate-operator:<tag>`.
- **Chart** pushed to the same repo's OCI namespace under a `charts/` subpath: `oci://ghcr.io/donaldgifford/renovate-operator/charts/renovate-operator:<chart-version>`.
- **Image build uses `docker-bake.hcl`** for both local and CI builds (multi-arch via `docker buildx bake`). Replaces the kubebuilder Makefile's `docker-buildx` flow (which uses `Dockerfile.cross`); the bake file is the single source of truth for build args, platforms, and tags. CI calls `docker buildx bake ci` (or equivalent target) instead of `make docker-buildx`.

### Q8 — ServiceMonitor / PrometheusRule label defaults

**Resolved: chart defaults `metrics.{serviceMonitor,prometheusRule}.additionalLabels: {release: kube-prometheus-stack}`** so the kube-prometheus-stack installer picks them up out of the box. Users on a non-default Prometheus Operator setup override the label.

### Q9 — Worker entrypoint shell content

**Resolved: bake in the proposed inline shell** (with a gzip-decode branch for the compressed shard path). Final shape:

```sh
#!/bin/sh
set -eu
INDEX="${JOB_COMPLETION_INDEX:?missing JOB_COMPLETION_INDEX}"
SHARD_FILE="/etc/shards/shard-$(printf '%04d' "$INDEX").json"
GZ_FILE="${SHARD_FILE}.gz"
if   [ -f "$SHARD_FILE" ]; then DATA="$(cat "$SHARD_FILE")";
elif [ -f "$GZ_FILE"    ]; then DATA="$(gunzip -c "$GZ_FILE")";
else echo "shard $INDEX not present (looked at $SHARD_FILE and $GZ_FILE)" >&2; exit 1; fi
RENOVATE_REPOSITORIES="$(printf '%s' "$DATA" | jq -c '.repos')"
export RENOVATE_REPOSITORIES
exec renovate
```

Stays under 12 lines. No Go binary / no sidecar image.

### Q10 — Default chart `appVersion` behavior

**Resolved: chart `appVersion` matches the operator image tag** (bumped together on every release). The Renovate image pin (currently `:latest`) is documented in `dist/chart/values.yaml` and the chart NOTES.txt rather than encoded in `appVersion`.

## References

- [RFC-0001](../rfc/0001-build-kubebuilder-renovate-operator.md)
- [DESIGN-0001](../design/0001-renovate-operator-v0-1-0.md)
- [ADR-0001](../adr/0001-use-kubebuilder-for-operator-scaffolding.md), [ADR-0002](../adr/0002-adopt-kubebuilder-helm-chart-plugin.md), [ADR-0003](../adr/0003-multi-crd-architecture.md), [ADR-0004](../adr/0004-use-conditions-and-run-children-for-status.md), [ADR-0005](../adr/0005-indexed-jobs-for-parallelism.md), [ADR-0006](../adr/0006-multi-platform-support.md), [ADR-0007](../adr/0007-observability-stack.md), [ADR-0008](../adr/0008-default-scan-via-helm-chart.md)
- `AGENTS.md` (kubebuilder canonical guide)
- `CLAUDE.md` (project context, locked decisions, build/task-runner notes)
