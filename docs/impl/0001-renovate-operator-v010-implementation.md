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

- [ ] `internal/clock/clock.go`: thin wrapper around `k8s.io/utils/clock` so reconcilers and tests can swap implementations (`clock.Clock` interface, real + fake).
- [ ] `internal/conditions/conditions.go`: thin helpers around `meta.SetStatusCondition` for the condition types this project uses (so reconcilers don't repeat the same boilerplate). Lint should reject direct `append` to condition slices elsewhere.
- [ ] `internal/sharding/shard_builder.go` (pure): given `[]Repository` + `WorkersSpec`, produce `actualWorkers` and the shard ConfigMap data (`shard-NNNN.json` keys, optional gzip+base64 above 900 KiB). Contract:
  - [ ] Round-robin assignment across `actualWorkers`.
  - [ ] `actualWorkers = clamp(ceil(len/reposPerWorker), min, max)` with `min,max ≥ 1`.
  - [ ] Stable across runs given the same input ordering (sort first).
- [ ] `internal/jobspec/job_builder.go` (pure): given `*RenovateRun` + shard `*ConfigMap` → `*batchv1.Job`. Implements every detail from [DESIGN-0001 § Job builder](../design/0001-renovate-operator-v0-1-0.md#job-builder-internalcontrollerjob_buildergo), including the env-var assembly order and the `shard-loader` init container shell script (locked inline; see [Resolved Q9](#q9--worker-entrypoint-shell-content)).
- [ ] `internal/credentials/mirror.go`: pure-ish helpers to construct the mirrored Secret (name, owner ref, labels, data copy); the I/O happens in the Run controller.
- [ ] Table-driven tests for each builder under `*_test.go`. Aim for 100% branch coverage.

#### Success Criteria

- `go test ./internal/sharding/... ./internal/jobspec/... ./internal/credentials/... ./internal/conditions/... ./internal/clock/...` passes with `-race -coverprofile=...` and prints `coverage: 100.0% of statements` (or with explicitly annotated exclusions for unreachable branches).
- The shard builder's gzip path triggers above 900 KiB and not below; verified by table case.
- The job builder produces an Indexed Job with `parallelism = completions = actualWorkers`, correct env-var order (Platform → auth → discovery → repos → RENOVATE_CONFIG → tracing → extraEnv), correct owner refs, and the labels documented in DESIGN-0001.

---

### Phase 3: Platform clients and discovery

Implements the `Client` interface for GitHub (App auth) and Forgejo (token), including `Discover` and `HasRenovateConfig`. This is where the rate-limit token bucket lives.

#### Tasks

- [ ] `internal/platform/platform.go`: `Client` interface, `Repository`, `DiscoveryFilter`, error types (transient vs permanent).
- [ ] `internal/platform/github/`:
  - [ ] `client.go`: `go-github/v62` + `bradleyfalzon/ghinstallation/v2`; constructs an installation-scoped client from `GitHubAppAuth`.
  - [ ] `discover.go`: list repos via `/orgs/{org}/repos` paginated (Search API path is a future optimization — see [Resolved Q4](#q4--github-discovery-rest-list-vs-search-api)); apply `Filter`/`Topics`/`SkipForks`/`SkipArchived` server-side where possible, client-side otherwise.
  - [ ] `has_config.go`: contents-API check across the five known config paths; result memoized per Run.
  - [ ] `ratelimit.go`: per-installation token bucket sized per [Resolved Q2](#q2--rate-limiter-sizing); honors GitHub's `Retry-After` and primary/secondary rate-limit responses.
- [ ] `internal/platform/forgejo/`:
  - [ ] `client.go`: `code.gitea.io/sdk/gitea`; token-authenticated.
  - [ ] `discover.go`: `/orgs/{org}/repos` paginated; same filter handling.
  - [ ] `has_config.go`: contents API equivalent.
- [ ] `dnaeon/go-vcr` fixtures under `internal/platform/{github,forgejo}/testdata/` covering: happy path (≥ 50 repos paginated), 404 on `HasRenovateConfig`, 401/403 auth failure, 429/secondary rate limit with retry-after, malformed JSON.
- [ ] Unit tests against the VCR fixtures for both clients.

#### Success Criteria

- `go test ./internal/platform/...` passes against recorded fixtures.
- A discovery against an org with 1k repos in fixture form completes in `< 30s` simulated wall-clock with the rate limiter engaged.
- The rate limiter degrades gracefully under simulated 429s (sleeps the indicated retry-after, retries once, surfaces the error if the second attempt also rate-limits).
- Both clients implement the same `Client` interface; reconcilers should not import platform-specific packages.

---

### Phase 4: Reconcilers

The bulk of the operator. Each controller gets its own file and envtest suite. Built on top of phases 1–3.

#### Tasks

- [ ] **Platform controller (`internal/controller/renovateplatform_controller.go`)**:
  - [ ] Resolve Secret in operator namespace (driven by `auth.{githubApp.privateKeyRef,token.secretRef}`).
  - [ ] For GitHubApp: parse PEM, mint JWT, hit `/app` for a health check.
  - [ ] For Token: read token, optionally hit a no-op API call.
  - [ ] Set `Ready=True/False` with `Reason ∈ {CredentialsResolved, SecretNotFound, KeyMissing, AuthFailed, PlatformUnreachable}`.
  - [ ] Watch Platform + Secret (mapped to Platforms whose auth refs match).
- [ ] **Scan controller (`internal/controller/renovatescan_controller.go`)**:
  - [ ] Parse cron via `robfig/cron/v3` against `spec.timeZone`; surface invalid as `Ready=False/Reason=InvalidSchedule`.
  - [ ] Resolve `platformRef` → Platform; require `Ready=True`. Else `Ready=False/Reason=PlatformNotReady`, requeue 60s.
  - [ ] Honor `suspend`; honor `concurrencyPolicy` against active Runs (`Forbid` → skip+requeue at next fire, `Allow` → always create, `Replace` → equivalent to Forbid + warning log per [DESIGN-0001 resolution #7](../design/0001-renovate-operator-v0-1-0.md#resolved-open-questions)).
  - [ ] Create `RenovateRun` at fire time, snapshotting Platform spec + Scan spec into `spec.{platformSnapshot,scanSnapshot}`.
  - [ ] GC old terminal Runs per `successfulRunsHistoryLimit`/`failedRunsHistoryLimit`.
  - [ ] Set `Scheduled=True`, `RequeueAfter = nextRunTime - now` capped at 5m.
  - [ ] Watches: Scan + Platform (mapped) + Run (owned).
- [ ] **Run controller (`internal/controller/renovaterun_controller.go`)** — state machine per [DESIGN-0001 § Reconciler: RenovateRun](../design/0001-renovate-operator-v0-1-0.md#reconciler-renovaterun):
  - [ ] `Pending` → `Discovering`: set startTime, set `Started=True`, instantiate platform client from snapshot, mirror credential Secret into Run's namespace.
  - [ ] `Discovering`: call `platform.Discover`, apply `requireConfig` filter (concurrency-bounded `errgroup`), compute `actualWorkers`, build shard ConfigMap, build worker Job, transition to `Running` with `Discovered=True`. Idempotent — survives controller crash mid-step.
  - [ ] `Running`: read owned Job's index counters, update `succeededShards`/`failedShards`, transition to `Succeeded` (`succeeded == completions`) or `Failed` (terminal Job failure, exhausted `backoffLimitPerIndex`).
  - [ ] Terminal phases: no further work; rely on parent Scan's history-limit GC.
  - [ ] Watches: Run + owned Job + owned ConfigMap.
- [ ] Cluster RBAC markers (`+kubebuilder:rbac:...`) per controller; verify `make manifests` regenerates `config/rbac/role.yaml` to include `secrets get/list/watch/create` for the Run controller's mirror operation.
- [ ] Wire the three controllers into `cmd/main.go`'s manager, with the existing kubebuilder leader-election defaults.

#### Success Criteria

- `kubectl apply -f` of Platform → Scan with a cron `* * * * *` produces a Run within one minute, and the Run reaches `phase=Succeeded` against a stub platform.
- A Platform with a missing Secret transitions to `Ready=False/SecretNotFound`; replacing the Secret transitions back to `Ready=True` without operator restart.
- `kubectl delete renovatescan ...` cascades to all child Runs, owned Jobs, owned ConfigMaps, and mirrored Secrets within 30 seconds (no orphans).
- envtest suite green for all three controllers; coverage for `internal/controller/...` ≥ 80%.

---

### Phase 5: Observability

Metrics, tracing, logging bridge, pprof. Wired to the manager so they're alive from process start.

#### Tasks

- [ ] `internal/observability/metrics.go`: register custom collectors per [DESIGN-0001 § Metrics](../design/0001-renovate-operator-v0-1-0.md#metrics) on controller-runtime's `metrics.Registry`. Includes:
  - [ ] Counters: `renovate_operator_runs_total{scan,platform,result}`, `renovate_operator_discovery_errors_total{scan,platform}`, `renovate_operator_shards_failed_total{scan,platform}`.
  - [ ] Histograms: `renovate_operator_run_duration_seconds{scan,platform}`, `renovate_operator_discovery_duration_seconds{scan,platform}`.
  - [ ] Gauges: `renovate_operator_active_runs{scan,platform}`, `renovate_run_shard_count{scan,platform}`.
  - [ ] Label set is `{scan, platform, result}` only — **no `scan_namespace`** per [Resolved Q3](#q3--metric-label-cardinality).
- [ ] `internal/observability/tracing.go`: `InitTracer(ctx, version)` that returns a no-op shutdown when `OTEL_EXPORTER_OTLP_ENDPOINT` is unset; otherwise builds an OTLP gRPC exporter + sdktrace TracerProvider.
- [ ] `internal/observability/logbridge.go`: `logr.LogSink` wrapper that pulls the active span from the reconcile context and adds `trace_id` / `span_id` keys.
- [ ] `internal/observability/pprof.go`: `net/http/pprof` mux on `:8082` behind a `--pprof-bind-address` flag; off by default.
- [ ] Wire all four into `cmd/main.go`. Health/readiness endpoints (kubebuilder defaults at `:8081`) stay as-is.
- [ ] Add tracing spans to hot paths: `Discover`, `HasRenovateConfig` batch, `BuildWorkerJob`, the Run state transitions.

#### Success Criteria

- `curl localhost:8080/metrics` returns the project metrics alongside controller-runtime's defaults.
- A test envtest run with `OTEL_EXPORTER_OTLP_ENDPOINT=...` surfaces spans for at least one full reconcile.
- Log lines emitted during a Run carry `trace_id` / `span_id` keys when tracing is enabled.
- pprof endpoint reachable on `:8082` only when `--pprof-bind-address=:8082` is set.

---

### Phase 6: Helm chart, samples, and contrib tree

Polish the kubebuilder-scaffolded chart into the values surface DESIGN-0001 specifies, ship a default Scan, and land the dashboards/alerts.

#### Tasks

- [ ] Replace `dist/chart/values.yaml` with the surface in [DESIGN-0001 § values.yaml](../design/0001-renovate-operator-v0-1-0.md#valuesyaml-top-level-surface): image, replicas+leaderElect, resources, metrics + ServiceMonitor + PrometheusRule (gated), tracing, pprof, logging level/format, `defaultScan` block, worker resources defaults.
- [ ] `dist/chart/templates/extra/default-scan.yaml` gated by `.Values.defaultScan.enabled` per [ADR-0008](../adr/0008-default-scan-via-helm-chart.md). Use `helm.sh/resource-policy: keep`.
- [ ] `dist/chart/templates/extra/servicemonitor.yaml` gated by `.Values.metrics.serviceMonitor.enabled`. Default `additionalLabels: {release: kube-prometheus-stack}` per [Resolved Q8](#q8--servicemonitor--prometheusrule-label-defaults).
- [ ] `dist/chart/templates/extra/prometheusrule.yaml` gated by `.Values.metrics.prometheusRule.enabled`.
- [ ] Pre-install validation: lint hook (or template guard) that fails when `defaultScan.enabled=true && defaultScan.platformRef.name == ""`.
- [ ] Strip the kubebuilder-scaffolded `dist/chart/templates/certmanager/` per [Resolved Q6](#q6--cert-manager-template); add a post-regen `just chart-clean` (or similar) step that re-strips after `kubebuilder edit --plugins helm/v1-alpha` re-emits it. Document cert-manager as an installation prerequisite in `dist/chart/README.md` / NOTES.txt for future webhook-bearing releases.
- [ ] `contrib/grafana/dashboards/{operator,runs,traces,logs}.json` per [ADR-0007](../adr/0007-observability-stack.md).
- [ ] `contrib/prometheus/{alerts,recording-rules}.yaml`.
- [ ] `contrib/alloy/operator.river`.
- [ ] `contrib/README.md` indexing how to import each.
- [ ] Custom lint: every metric in `internal/observability/metrics.go` is referenced in at least one dashboard or alert (or excluded via `// metric:internal`).

#### Success Criteria

- `helm lint dist/chart` clean.
- `helm template dist/chart --set defaultScan.enabled=true --set defaultScan.platformRef.name=github` renders without errors and includes a `RenovateScan/default`.
- `helm template dist/chart --set defaultScan.enabled=true` (no platformRef) **fails** with the expected message.
- A homelab `helm install ... --set ...` produces a running operator pod, a default Scan, and dashboards visible in Grafana once imported.

---

### Phase 7: Testing

Unit + envtest layered tests are landed throughout phases 1–5. This phase is the e2e + coverage round.

#### Tasks

- [ ] kind-based e2e harness in `test/e2e/` (extending the kubebuilder-scaffolded skeleton):
  - [ ] **GitHub stub e2e** (per [Resolved Q5](#q5--e2e-github-fidelity)): apply Platform → Scan with `* * * * *`; assert Run reaches `Succeeded` within 5 minutes; assert metrics increment.
  - [ ] **Forgejo e2e**: real Forgejo container in the kind cluster (image is small); assert end-to-end run.
  - [ ] **Parallelism e2e**: 200 stub repos, `maxWorkers: 5`, `reposPerWorker: 50` ⇒ assert `actualWorkers == 4`, all 200 in shard ConfigMap, Job parallelism 4.
- [ ] `test/manual/README.md` with the steps for the homelab `donaldgifford/server-price-tracker` and Forgejo manual runs.
- [ ] `just ci` composite gate stays green: lint + test + build + license-check.
- [ ] Coverage gate: target ≥ 80% on `internal/controller/...`, `internal/platform/...`, `internal/sharding/...`, `internal/jobspec/...`.

#### Success Criteria

- `just test-e2e` runs all three e2e scenarios on a fresh kind cluster and exits 0.
- `just test-coverage` reports ≥ 80% per the listed packages.
- CI on a PR runs the full gate (unit + envtest + e2e + lint) under 15 minutes.

---

### Phase 8: CI/CD and release

Workflows wired through `just`, multi-arch image with cosign + SBOM, OCI Helm chart push, semantic-release-style tag-driven flow.

#### Tasks

- [ ] Replace/refresh `.github/workflows/ci.yml` to call `just lint`, `just test`, `just license-check`. Verify it runs against the kubebuilder Makefile-backed targets.
- [ ] Add `.github/workflows/test-e2e.yml` calling `just test-e2e` on PRs that touch `api/`, `internal/`, `dist/chart/`, or e2e files.
- [ ] Reconcile `.goreleaser.yml` with the kubebuilder `Dockerfile` (build the manager from `cmd/main.go`, multi-arch linux/amd64 + linux/arm64).
- [ ] `.github/workflows/release.yml` on tag push: goreleaser run, push image to `ghcr.io/donaldgifford/renovate-operator`, cosign sign artifacts (`signs.artifacts: checksum + manifests`), syft SBOM attached to the release.
- [ ] Helm OCI push: `helm package dist/chart && helm push *.tgz oci://ghcr.io/donaldgifford/charts` step, gated on tag.
- [ ] `make build-installer` artifact (`dist/install.yaml`) attached to the GitHub release for kustomize users.
- [ ] Branch protection on `main`: require PR reviews, require `ci` workflow passing. (Repo-side, not committed — note in homelab handoff.)
- [ ] Set `dist/chart/values.yaml` `image.repository: ghcr.io/donaldgifford/renovate-operator` per [Resolved Q7](#q7--image-registry-path-and-image-build-mechanism).
- [ ] Create `docker-bake.hcl` at the repo root with `default`, `ci`, and `release` targets covering linux/amd64 + linux/arm64; reference it from `.github/workflows/release.yml` and `ci.yml` instead of `make docker-buildx`.
- [ ] Helm OCI push target: `helm package dist/chart && helm push *.tgz oci://ghcr.io/donaldgifford/renovate-operator/charts` (the `charts/` subpath is part of the push URL).

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
