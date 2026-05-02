# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Read AGENTS.md first

`AGENTS.md` (at the repo root) is kubebuilder's canonical AI guide. It covers project structure, scaffold-marker rules, the `make manifests` / `make generate` cycle, CLI commands for adding APIs/webhooks/controllers, testing, deployment, and distribution. **Do not duplicate that content here.** When working on operator scaffolding, controllers, webhooks, or generated artifacts, AGENTS.md is the source of truth.

## Project context

Kubernetes operator that runs Renovate at scale by sharding repository scans across parallel worker pods. The repo is scaffolded with `kubebuilder` v4 (see `PROJECT`); domain `fartlab.dev`, module `github.com/donaldgifford/renovate-operator`, projectName `renovate-operator` (drives kustomize `namePrefix`, chart name, RBAC role names).

The v0.1.0 spec lives in `docs/`:

- [RFC-0001](docs/rfc/0001-build-kubebuilder-renovate-operator.md) — problem statement, why-not-mogenius, scope.
- [DESIGN-0001](docs/design/0001-renovate-operator-v0-1-0.md) — implementation blueprint (CRD shapes, reconciler logic, Helm values, CI). Includes a **Resolved Open Questions** section with locked-in decisions and a **Future architecture: state DB** section threading the long-term scheduler/Postgres direction.
- [ADRs 0001–0008](docs/adr/) — discrete decisions referenced by RFC/DESIGN.

Key locked decisions surfaced from those docs (so they don't get re-litigated):

- **API group**: `renovate.fartlab.dev`. CRDs: `RenovatePlatform` (cluster, `rp`/`rplatform`), `RenovateScan` (namespaced, `rscan` only — `rs` collides with the built-in `replicasets` shortname), `RenovateRun` (namespaced, ephemeral, owned by Scan, `rr`/`rrun`).
- **Parallelism**: Indexed `batch/v1.Job`, `N = clamp(ceil(repos/reposPerWorker), minWorkers, maxWorkers)`. One Job per Run; one ConfigMap of shards owned by Run; cascade delete via owner refs.
- **Auth**: GitHub App (`installationID` required, one Platform per installation) and Forgejo token. Renovate handles its own JWT minting; the operator only mints tokens for its own discovery API calls.
- **Credentials**: Operator consumes a Secret in its release namespace and mirrors it into Scan namespaces per Run. The "how it gets there" is a deployment concern (1Password Connect for homelab, ESO for production).
- **Status shape**: `[]metav1.Condition` everywhere; Run carries a typed `phase` enum (`Pending|Discovering|Running|Succeeded|Failed`) as state-machine cursor + printer column. See [ADR-0004](docs/adr/0004-use-conditions-and-run-children-for-status.md).
- **Distribution**: kubebuilder Helm plugin output at `dist/chart/`, OCI-pushed to GHCR.

## Tooling

`mise.toml` pins versions. `mise install` materializes them. Notable: `kubebuilder`, `golangci-lint`, `goreleaser`, `helm` 3.19.0 + `helm-cr`/`helm-ct`/`helm-diff`/`helm-docs`, `kind`/`k3d`, `docz` (donaldgifford fork), `syft`, `govulncheck`, `go-licenses`. `GOPRIVATE=github.com/donaldgifford/*` is set via `mise.toml`.

## Lint config

`.golangci.yml` is the kubebuilder default (kube-friendly: `copyloopvar`, `ginkgolinter`, `logcheck`, `modernize`, `revive`, etc.).

## Build / task runner

Two files, both intentional:

- **`Makefile`** — kubebuilder-generated. **Do not edit.** It owns the `manifests` / `generate` / `test` / `build` / `docker-build` / `install` / `deploy` cycle plus envtest, kustomize, and controller-gen tool installation. Regenerated when scaffolding new APIs/webhooks; hand-edits will silently break the next `kubebuilder create ...`.
- **`justfile`** — developer entrypoint. Wraps the Makefile (`@make <target>`) for ergonomics so the kubebuilder logic stays single-sourced, and adds project-specific recipes that aren't in the kubebuilder Makefile: `license-check` / `license-report` (go-licenses), `release-check` / `release-local` / `release <tag>` (goreleaser), `test-pkg <pkg>` / `test-coverage` / `test-report`, plus composite gates (`check` = lint+test, `ci` = lint+test+build+license-check). `just` with no args lists everything grouped.

Both still work directly: `make test` and `just test` are equivalent. Prefer `just` for daily use; reach for `make` for any kubebuilder target the justfile doesn't surface.

## Documentation workflow

`docs/` is managed by the `docz` CLI (`.docz.yaml`). Doc types: `rfc`, `adr`, `design`, `impl`, `plan`, `investigation`. `docz update` regenerates README index tables; `index.auto_update: true` runs it automatically on `docz create`. MkDocs/TechDocs wiki integration is wired in `.docz.yaml` but no `mkdocs.yml` exists yet.

## Kubebuilder plugins enabled

Two plugins are registered in `PROJECT` and shape what gets generated/automated:

- **`helm.kubebuilder.io/v1-alpha`** — owns `dist/chart/` (Chart.yaml, values.yaml, templates for CRDs, RBAC, manager Deployment, metrics service, network policy, optional cert-manager). Re-runs of `make manifests` and `kubebuilder create api ...` keep the chart in sync with `config/`. Hand-edit area for project-specific values lives at `dist/chart/templates/extra/` (per DESIGN-0001 § Helm chart).
- **`autoupdate.kubebuilder.io/v1-alpha`** — owns `.github/workflows/auto_update.yml`. Weekly cron + manual dispatch; runs `kubebuilder alpha update --force --push --restore-path .github/workflows --open-gh-issue` to track upstream kubebuilder releases via a tracking issue + PR. No GitHub Models permission used; re-run `kubebuilder edit --plugins="autoupdate/v1-alpha" --use-gh-models` if AI summaries are wanted later.

## Claude Code plugins active

`.claude/settings.json` enables: `gopls-lsp`, `go-development`, `kubebuilder`, `helm`, `docker`, `makefiles`, `mise`, `shell-scripting`, `docz`, `todo-comments`, `claude-md`. Use the corresponding skills (`kubebuilder:kubebuilder`, `helm:helm`, `docz:docz`, etc.) for their domains.

## v0.1.0 implementation status

Tracked in [`docs/impl/0001-renovate-operator-v010-implementation.md`](docs/impl/0001-renovate-operator-v010-implementation.md). Completed so far:

- Phase 1 (API surface): three CRDs filled in with full schemas, CEL validation rules, printer columns; samples validated against a kind cluster.
- Phase 2 (pure builders): `internal/clock`, `internal/conditions`, `internal/sharding`, `internal/jobspec`, `internal/credentials`. Aggregate coverage ~94%; only unreachable defensive paths (JSON marshal of static structs, gzip writes into bytes.Buffer) are uncovered.
- Phase 3 (platform clients): `internal/platform.Client` interface plus `internal/platform/github` (go-github/v62 + ghinstallation/v2) and `internal/platform/forgejo` (code.gitea.io/sdk/gitea). Per-instance `golang.org/x/time/rate` token bucket per Q2 sizing. classifyErr maps both clients onto shared `ErrTransient`/`ErrPermanent`/`ErrUnauthorized`/`ErrNotFound`/`*RateLimitedError`. Tested with httptest fakes (VCR was dropped — see IMPL-0001 Phase 3.4 note).
- Phase 4 (reconcilers): all three controllers wired up:
  - `RenovatePlatform`: resolves credential Secret in operator namespace, validates PEM/token, surfaces Ready condition. Watches Platform + Secret.
  - `RenovateScan`: parses cron via robfig/cron/v3, resolves Platform, creates Run with frozen snapshots at fire time, GCs old terminal Runs, surfaces Scheduled condition. Watches Scan + Platform (mapped) + Run (owned).
  - `RenovateRun`: state machine Pending → Discovering → Running → {Succeeded, Failed}; mirrors credential Secret, builds shard ConfigMap and worker Job. Watches Run + owned Job + ConfigMap + Secret. Pluggable `PlatformClientFactory` for tests.
  - `cmd/main.go` wires all three with new `--operator-namespace` flag (defaults to `$POD_NAMESPACE`, falls back to `renovate-system`).
- Phase 5 (observability): `internal/observability` ships metrics (7 collectors with `{scan, platform, result}` labels), OTLP gRPC tracing with no-op fallback when `OTEL_EXPORTER_OTLP_ENDPOINT` is empty, log-bridge attaching `trace_id`/`span_id` to logr, and `net/http/pprof` on a configurable bind. Wired in `cmd/main.go` via `--pprof-bind-address`. `InitTracer` builds its resource from `resource.NewSchemaless` (not `NewWithAttributes` with a hard-pinned semconv schema URL) so `resource.Default()`'s SDK-driven schema is preserved without a "conflicting Schema URL" merge error.
- Phase 6.1 (chart values surface): `dist/chart/values.yaml` rewritten to the DESIGN-0001 surface (image, replicaCount/leaderElect, resources, metrics{serviceMonitor,prometheusRule}, tracing, pprof, logging, full `defaultScan` block). The legacy `controllerManager` block is retained for backward compat with the kubebuilder-scaffolded `manager.yaml` template (will be reconciled in 6.3).
- Phase 6.2 (extra templates): `dist/chart/templates/extra/{default-scan,servicemonitor,prometheusrule}.yaml` added with proper gating, `helm.sh/resource-policy: keep` on the default scan, and a fail-fast template guard for `defaultScan.enabled=true && defaultScan.platformRef.name == ""`. `helm lint` and `helm template` both verified.
- Phase 6.3 (cert-manager strip + NOTES): `dist/chart/templates/certmanager/` is gone, all `certmanager.enable && metrics.enable` branches removed from `manager.yaml` and `prometheus/monitor.yaml`. `metrics-service.yaml` was renamed to gate on the new `metrics.enabled` key. `templates/NOTES.txt` documents cert-manager as a future-webhook prerequisite. New Make targets: `chart-regenerate` (wraps `kubebuilder edit --plugins=helm/v1-alpha --force` + `chart-clean`), `chart-clean` (re-strips certmanager dir), `chart-lint` (helm lint with both guard states).
- Phase 6.4+6.6 (contrib tree + metrics coverage lint): four Grafana dashboards (`operator`, `runs`, `traces`, `logs`) declaring their datasource via `__inputs` so Grafana prompts at import. Standalone `PrometheusRule` mirrors of the chart-bundled rules in `contrib/prometheus/`. `contrib/alloy/operator.river` covering metrics scrape + log forward (with JSON extract for `trace_id`/`scan`/`platform`) + OTLP receiver-to-Tempo. `contrib/README.md` indexes everything. New `make metrics-coverage-lint` target runs `scripts/lint-metrics-coverage.sh` to fail when a metric in `internal/observability/metrics.go` is not referenced anywhere under `contrib/` or the chart's PrometheusRule (exempt via `// metric:internal` comment).
- Phase 7.1 (e2e harness refactor): `test/e2e/e2e_suite_test.go` rewritten — cert-manager hook stripped, `BeforeSuite` runs `make docker-build` + kind load + `helm upgrade --install` (with `defaultScan.enabled=false` to skip the chart guard). Three smoke specs in `e2e_test.go`: pod-running, CRDs-registered, manager-started log. Makefile `test-e2e` wraps `CERT_MANAGER_INSTALL_SKIP=true`. New `make test-coverage` prints per-package coverage from `cover.out`.
- Phase 7.4 (controller + platform-client coverage uplift): every gate package clears the IMPL-0001 ≥80% bar. controller 90.0%, platform 100%, platform/forgejo 93.3%, platform/github 92.4%, sharding 92.0%, jobspec 92.7%, observability 88.1%. Lift came from fake.NewClientBuilder() tests covering the IO helpers (mirrorCredential, ensureShardConfigMap, ensureWorkerJob, observeJob, refreshActiveRuns, gcOldRuns, createRun, scansForPlatform, platformsForSecret), client-option tests for the platform SDKs, `WithInterceptorFuncs`-driven Reconcile-wrapper tests for the status-conflict + status-update-error paths in all three reconcilers, exhaustive `classifyErr` tests on both platform clients (typed `*RateLimitError`/`*AbuseRateLimitError` for github; network-failure + unexpected-4xx for forgejo), and real-apiserver envtest specs for all three reconcilers (Platform SecretMissing/SecretArrives, Scan scheduling with Ready Platform, Run discoverAndDispatch happy path).

- Phase 7.3 (manual / homelab acceptance runbook): `test/manual/README.md` documents two scenarios (GitHub.com against `donaldgifford/server-price-tracker` via App auth; homelab Forgejo via token auth) with full kubectl steps, acceptance checks (Scheduled cond, succeeded Run, PR opened, dashboard reconcile rate, runs_total counter), and a troubleshooting matrix.
- Phase 8 (CI/CD reconcile): `.goreleaser.yml` rewritten for kubebuilder layout (`main: ./cmd`, `-trimpath`, `-X main.version={{.Version}}` ldflags, syft SBOM block, `dist: dist-goreleaser` so the dir doesn't collide with `dist/chart/`). `.github/workflows/release.yml` runs the full bump → goreleaser → docker bake + cosign keyless → helm-chart push pipeline; the build job adds a `git checkout -- config/manager/kustomization.yaml` step after `make build-installer` so the kustomize image-edit doesn't dirty the tree before goreleaser. `dist/install.yaml` is gitignored (untracked install-artifact). `docker-bake.hcl` defines a `docker-metadata-action` stub target that the release target inherits from, so `docker/metadata-action`'s `bake-file-tags` actually flows into the bake (HCL inheritance replaces tag lists, doesn't extend them, so the inheritor must omit its own `tags`).
- Phase 8 release pipeline now exercised end-to-end: v0.1.0 published 2026-05-01 with binaries + SBOMs + signed multi-arch image at `ghcr.io/donaldgifford/renovate-operator:0.1.0` + chart at `oci://ghcr.io/donaldgifford/charts/renovate-operator:0.1.0`. Iterating to v0.1.0 took four PRs of fix-forward (dist/ collision, kustomize edit, install.yaml gitignore, bake-action inheritance) — each delete-tag-redo-PR cycle is documented in commits `fix/release-*`.
- Phase 9 (homelab cutover) **in progress** (2026-05-02): operator installed against homelab cluster, both GitHub App and Forgejo Platforms reconciled `Ready=True`, three Scans scheduled. First full Run materializes shortly after — see open INV-0001 and the metrics-auth bug below for incidents found during the install.

### Documentation
- `docs/usage/` ships an operator-user-facing guide (separate from `AGENTS.md` / DESIGN-0001 which target contributors): `README.md` (TOC), `installation.md` (chart values + troubleshooting matrix), `authorization.md` (GitHub App permissions, Forgejo token scopes, Forgejo version compat note — gitea SDK v0.24.1, no specific LTS pin tested), and per-CRD docs `renovate-platform.md` / `renovate-scan.md` / `renovate-run.md`.

### v0.1.0 → v0.1.x bugs surfaced live
- **metrics-auth RBAC silently dead** (PR #10, will ship in v0.1.1): three RBAC templates checked `.Values.metrics.enable` but the values surface uses `metrics.enabled` post-Phase 6.1 rewrite. Operator pod's `/metrics` endpoint 401'd with `tokenreviews.authentication.k8s.io is forbidden` because the auth ClusterRoleBinding never rendered. Fixed by changing the gate in `metrics_auth_role{,_binding}.yaml` and `metrics_reader_role.yaml`. helm lint didn't catch it (the bad gate was syntactically valid, just dead code).
- **`SetupSignalHandler` called twice → `panic: close of closed channel`** at startup (fixed before v0.1.0 actually published). `cmd/main.go` was calling `ctrl.SetupSignalHandler()` once for `InitTracer` and again for `mgr.Start`; the function panics on second call by design. Fix: capture a single `signalCtx` and reuse. Surfaced via the e2e harness's `dumpClusterDiagnostics()` BeforeSuite-failure hook — without that, prior CI runs only reported "helm install timed out."
- **`<invalid>` in `kubectl get rscan` Next Run column** for future timestamps. CRD declares the column `type=date`, which kubectl renders as a *relative-since-now* duration; the formatter has no future-time path and returns the literal `<invalid>` string. Tracked in [INV-0001](docs/investigation/0001-render-renovatescan-next-run-printer-column-accurately-for.md). Fix is `type=date` → `type=string`; data is correct, just rendering. Cosmetic, not a Phase 9 blocker.
- **Renovate v43+ rejects App credentials at platform init** — **Phase 9 acceptance blocker** (surfaced after PodSecurity fix unblocked workers). Operator was passing `RENOVATE_GITHUB_APP_ID/KEY` to the worker, expecting Renovate to mint installation tokens itself. v43+'s `initPlatform` for `platform=github` requires a real, usable token *up front* — period. Hypothesis 1 (autodiscover=true unlocks token minting) was deployed and refuted on the homelab; Renovate failed at config validation in <100ms regardless. Real fix: **operator mints the installation token via `ghinstallation/v2`** (already imported for discovery) and passes it to the worker as `RENOVATE_TOKEN`. Tracked in [INV-0003](docs/investigation/0003-renovate-v43-github-app-auth-requires-autodiscover-not.md) with both hypotheses captured. Architectural touchpoints: new `MintAccessToken(ctx) (string, time.Time, error)` on `platform.Client`; per-Run mirrored Secret now carries only an `access-token` key (`credentials.MirrorAccessTokenKey`); worker env collapses to a single `RENOVATE_TOKEN` for both auth types. Token TTL is ~1h on github.com — Runs longer than ~50 min would 401 mid-scan; v0.2.x will need either token-refresh or shorter shards. Deferred enhancement: `RenovatePlatform.spec.auth.{githubAppFromVault,*FromESO,*FromAWSSM,...}` credential-source abstraction so users can swap K8s Secret for Vault / ESO / cloud secret managers without operator-side changes. Bundled with INV-0001/INV-0002/PodSecurity for v0.1.2.
- **Worker Job's pod template missing PodSecurity restricted fields** — **Phase 9 acceptance blocker**. `internal/jobspec/job_builder.go` set no `SecurityContext` on either the pod or the renovate container, so namespaces enforcing `pod-security.kubernetes.io/enforce: restricted` rejected the worker pod with "would violate PodSecurity ... restricted" — Run got created, Job got created, but pods never started, so Run sat indefinitely with no status. Fix mirrors the operator's own pod surface: pod-level `runAsNonRoot=true` + `seccompProfile.type=RuntimeDefault`; container-level `allowPrivilegeEscalation=false` + `capabilities.drop=[ALL]`. Renovate's official image already runs as non-root, so no extra UID coordination needed. Bundled with INV-0001/INV-0002 for v0.1.2.
- **RenovateScan never fires its first Run when `LastRunTime` is unset** — **Phase 9 acceptance blocker**. `computeFireTimes` (`internal/controller/renovatescan_controller.go:200`) returns `missed=zero` whenever `lastRun==nil`, and `cron.Schedule.Next(t)` returns *strictly* after `t`. The controller wakes at the requeued boundary, `now` is just past it, `Next(now)` jumps to the next-next fire, and the current boundary gets skipped. Repeats forever — no Scan with empty `LastRunTime` ever materializes a Run. Tracked in [INV-0002](docs/investigation/0002-renovatescan-never-fires-first-run-when-lastruntime-is-unset.md). Fix: when `lastRun==nil`, walk back a small grace window (`startFrom = nowLoc.Add(-fireGrace)`) so the just-past boundary lands inside the missed-fire loop. Bundle with INV-0001's column fix and ship as v0.1.2.
- **GitHub Discover bypasses the App installation grant for personal accounts** — surfaced 2026-05-02 after INV-0003 unblocked App auth end-to-end. `internal/platform/github/discover.go`'s org → user fallback hit `/users/{owner}/repos`, which returns *every public repo* for the owner regardless of which repos the installation token was granted. Bug only manifested for personal-account + "Only select repositories" + at-least-one-public-repo-not-in-grant configurations; for org installations or "All repositories" installs the public listing happened to match the grant. Operator processed 5 public repos in `test-gh-hhbc4` while the App actually only granted 4 different (private) repos. Fix: route App-auth Clients through `gh.Apps.ListRepos` (`/installation/repositories`) and intersect with `filter.Owner`; PAT auth keeps the org → user fallback. Tracked in [INV-0004](docs/investigation/0004-github-discover-bypasses-app-installation-grant.md). Bundled with INV-0001/INV-0002/INV-0003/PodSecurity for v0.1.2.

Phase 7 remaining: three full e2e scenarios (GitHub stub, Forgejo container, parallelism) — covered for v0.1.0 acceptance by `test/manual/README.md`.

### Chart regeneration

Running `make chart-regenerate` is the supported entry point — it invokes `kubebuilder edit --plugins=helm/v1-alpha --force` then `make chart-clean` to re-strip the cert-manager scaffold. Manual notes after a regen:

1. Restore `dist/chart/values.yaml` (DESIGN-0001 surface) — kubebuilder resets it.
2. `dist/chart/templates/extra/` is preserved (kubebuilder doesn't touch it).
3. Re-add `namespaced: false` under the RenovatePlatform resource in `PROJECT` (kubebuilder strips it).

## Known stale things to fix when convenient

- `.goreleaser.yml` predates the kubebuilder scaffold; should be reconciled with the kubebuilder `Dockerfile` and `cmd/main.go` build path before any release.
