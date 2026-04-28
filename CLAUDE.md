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

## Known stale things to fix when convenient

- `.goreleaser.yml` predates the kubebuilder scaffold; should be reconciled with the kubebuilder `Dockerfile` and `cmd/main.go` build path before any release.
