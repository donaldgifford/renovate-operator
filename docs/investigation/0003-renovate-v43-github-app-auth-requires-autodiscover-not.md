---
id: INV-0003
title: "Renovate v43 GitHub App auth requires autodiscover, not RENOVATE_REPOSITORIES"
status: Open
author: Donald Gifford
created: 2026-05-02
---
<!-- markdownlint-disable-file MD025 MD041 -->

# INV 0003: Renovate v43 GitHub App auth requires autodiscover, not RENOVATE_REPOSITORIES

**Status:** Open
**Author:** Donald Gifford
**Date:** 2026-05-02

<!--toc:start-->
- [Question](#question)
- [Hypothesis](#hypothesis)
- [Context](#context)
- [Approach](#approach)
- [Environment](#environment)
- [Findings](#findings)
  - [Observation 1 — exact Renovate failure message](#observation-1--exact-renovate-failure-message)
  - [Observation 2 — operator-side env wiring](#observation-2--operator-side-env-wiring)
  - [Observation 3 — Renovate's GitHub App auth model](#observation-3--renovates-github-app-auth-model)
  - [Observation 4 — autodiscoverFilter preserves sharding](#observation-4--autodiscoverfilter-preserves-sharding)
- [Conclusion](#conclusion)
- [Recommendation](#recommendation)
- [References](#references)
<!--toc:end-->

## Question

The worker pod runs Renovate v43.160.5 with `RENOVATE_PLATFORM=github`,
`RENOVATE_GITHUB_APP_ID`, `RENOVATE_GITHUB_APP_KEY` (from a mounted
Secret), `RENOVATE_AUTODISCOVER=false`, and `RENOVATE_REPOSITORIES`
(a JSON array of slugs from the shard). Renovate exits immediately at
init with `"You must configure a GitHub token"`. Why doesn't it use the
App credentials it was given, and what's the smallest change that
preserves the operator's sharding model while making Renovate happy?

## Hypothesis

Renovate v43.x's platform initialization for `platform=github` checks
for either `RENOVATE_TOKEN` (PAT) or **autodiscover-mode** App
credentials. When `autodiscover=false` is paired with App credentials,
Renovate has nowhere to mint installation tokens from at platform-init
time (it doesn't yet know which installation owns each repo in
`RENOVATE_REPOSITORIES`), and falls through to the
"need-a-token" error.

The fix is to bifurcate worker env wiring by auth type:

- **GitHub App auth** → set `RENOVATE_AUTODISCOVER=true` plus
  `RENOVATE_AUTODISCOVER_FILTER=<shard's repo slugs as JSON array>`.
  Renovate enumerates installations, mints a token per installation
  via `ghinstallation`-equivalent logic, and narrows to exactly the
  repos the operator's sharding decided.
- **Token auth (PAT or Forgejo)** → keep the current
  `RENOVATE_REPOSITORIES + RENOVATE_AUTODISCOVER=false` path. Token
  auth doesn't have the platform-init-needs-a-token problem.

The shard's repo list isn't known to the controller at env-build time
(controller builds env once, all shards reuse it), so the
`RENOVATE_AUTODISCOVER_FILTER` must be set at runtime from the
entrypoint shell — same place we currently read `.repos` and export
`RENOVATE_REPOSITORIES`.

## Context

Surfaced live during the Phase 9 homelab acceptance run on 2026-05-02,
**after** [INV-0001](0001-render-renovatescan-next-run-printer-column-accurately-for.md) and [INV-0002](0002-renovatescan-never-fires-first-run-when-lastruntime-is-unset.md)
unblocked Run materialization and the PodSecurity-restricted fix
unblocked worker pod admission. Once the operator was actually getting
worker pods to start, every Run reached `Phase=Failed` within seconds,
with the worker Job's pod logs showing:

```json
{"errorMessage":"You must configure a GitHub token","msg":"Initialization error","time":"2026-05-02T13:03:31.033Z"}
```

The Run reached `Discovered` (operator's discovery API call worked
fine — App credentials are valid) and a Job materialized; the Job's
single worker pod admitted under PodSecurity restricted, mounted the
shard ConfigMap, started the renovate binary, and exited with code 1
during platform init.

**Triggered by:** Phase 9 homelab acceptance — first end-to-end Run
attempt against a real GitHub App + repo, with all prior blockers
fixed. Confirms the operator's discovery + sharding + scheduling work
end-to-end; only the worker's auth env is wrong for Renovate v43+.

## Approach

1. **Confirm the failure path** by reading the worker pod's logs and
   the failed Run's `Status.Conditions`. The Job hit
   `BackoffLimitExceeded` after the renovate binary exited 1 during
   platform init — `Started=True/Admitted`, `Discovered=True/5 repos`,
   `Failed=True/JobFailed`.
2. **Read the operator's worker env wiring** in
   `internal/jobspec/env.go`. Today: `RENOVATE_PLATFORM=github`,
   `RENOVATE_GITHUB_APP_ID/KEY`, `RENOVATE_AUTODISCOVER=false`,
   `RENOVATE_REPOSITORIES` (set in entrypoint).
3. **Cross-reference Renovate's behavior** for `platform=github` +
   App auth + explicit repositories. Renovate's docs explicitly say
   "When using `githubAppId`, you should also use `autodiscover:
   true`" — the autodiscover code path is what owns the
   App-installations-token-minting flow.
4. **Identify the minimum env-wiring change** to preserve sharding:
   `RENOVATE_AUTODISCOVER_FILTER` accepts a list of glob/exact
   patterns and is consulted by the autodiscover loop after
   enumerating installations. Setting it to the shard's repo list
   gives us exact-match shard scoping with App auth.
5. **Implement** the bifurcation:
   - `internal/jobspec/env.go` → `RENOVATE_AUTODISCOVER` value depends
     on auth type (`true` for App, `false` for token).
   - `internal/jobspec/entrypoint.go` (EntrypointShell) → if
     `RENOVATE_GITHUB_APP_ID` is set, export
     `RENOVATE_AUTODISCOVER_FILTER` from `.repos`; otherwise export
     `RENOVATE_REPOSITORIES`.
   - Tests covering both branches.
6. **Verify on homelab** with a `dev-ci` push that the worker pod
   completes a Run (Renovate's `Repository started` / `Repository
   finished` lines visible) and the Run flips to `Phase=Succeeded`.

## Environment

| Component | Version / Value |
|-----------|----------------|
| Renovate worker image | `ghcr.io/renovatebot/renovate:latest` (resolved to v43.160.5 at the time of the failure) |
| Operator | `dev-ci` of branch `docs/inv-0001-next-run-column` (post-INV-0002 + post-PodSecurity fixes) |
| Auth | `RenovatePlatform.spec.auth.githubApp` with `appID=3566947`, `installationID=128819531`, `privateKeyRef.name=renovate-operator-github-app` / `key=private-key.pem` |
| Discovered repos | 5 (App's `donaldgifford/*` filter on the Scan, ~5 matching repos) |
| Failure point | Renovate `initPlatform` for `platform=github` |
| Affected files | `internal/jobspec/env.go:65-122`, `internal/jobspec/entrypoint.go:26-37` |

## Findings

### Observation 1 — exact Renovate failure message

The worker pod's stdout (single shard, index 0) shows:

```json
{"name":"renovate","level":30,"msg":"Renovate started","renovateVersion":"43.160.5","time":"2026-05-02T13:03:30.939Z"}
{"name":"renovate","level":60,"errorMessage":"You must configure a GitHub token","msg":"Initialization error","time":"2026-05-02T13:03:31.033Z"}
{"name":"renovate","level":30,"msg":"Renovate is exiting with a non-zero code due to the following logged errors","loggerErrors":[{"errorMessage":"You must configure a GitHub token","msg":"Initialization error"}]}
```

Failure is at platform init (~100 ms after start), before any repo is
touched. The Job hits `BackoffLimitExceeded` because
`backoffLimit=0` + `backoffLimitPerIndex=2` is the chart default and
the same init error repeats deterministically.

### Observation 2 — operator-side env wiring

`internal/jobspec/env.go:65-122` walks a fixed order:

1. `RENOVATE_PLATFORM`, `LOG_LEVEL`, `LOG_FORMAT`, `RENOVATE_ENDPOINT`
2. Auth env (`RENOVATE_GITHUB_APP_ID/KEY` for App;
   `RENOVATE_TOKEN` for token)
3. **`RENOVATE_AUTODISCOVER=false`** ← hardcoded
4. `RENOVATE_REQUIRE_CONFIG=required` if applicable
5. `RENOVATE_CONFIG` (the merged JSON)
6. OTel env

`internal/jobspec/entrypoint.go` exports
`RENOVATE_REPOSITORIES=$(jq -c '.repos')` from the mounted shard JSON
unconditionally, regardless of auth type.

### Observation 3 — Renovate's GitHub App auth model

Renovate's `platform=github` initialization expects a token at hand.
The autodiscover code path is where it sets up that token: it walks
`/app/installations` (signing a JWT with `githubAppKey`), and for each
installation that owns the App, mints a per-installation access token.
That token is then used for platform calls.

When `autodiscover=false` and `githubAppId/githubAppKey` are set,
Renovate has no installation-id pre-known and no token pre-supplied;
platform init fails before it gets to the repo loop where token
minting could happen lazily.

### Observation 4 — `autodiscoverFilter` preserves sharding

`RENOVATE_AUTODISCOVER_FILTER` accepts either a glob (`org/*`) or a
JSON array of exact slugs. With `autodiscover=true` and a filter
populated from the shard's `.repos`, Renovate:

1. Enumerates installations (gets the App's installation list).
2. For each installation, lists its repos.
3. Intersects with the filter — only the shard's exact repos pass.
4. Mints an installation token per matched installation, processes
   each repo with that token.

The operator's existing sharding logic still decides which repos go to
which worker; Renovate just has a different plumbing primitive for
hearing the same answer.

## Conclusion

**Answer:** Yes, confirmed. Renovate v43.x with `platform=github` +
App credentials cannot init unless `autodiscover=true`. The fix is
purely worker-env wiring; no operator-side token minting is needed
because Renovate already has the App credentials and does its own
installation-token minting along the autodiscover code path.

The bifurcation is auth-type-driven and small:

- App auth → autodiscover=true + `autodiscoverFilter` from shard
- Token auth → repositories from shard, autodiscover=false

## Recommendation

1. **Patch `internal/jobspec/env.go`**: drive `RENOVATE_AUTODISCOVER`
   off the platform's auth type — `"true"` for `Auth.GitHubApp`, `"false"` for
   `Auth.Token`. Co-locate it with the auth env in `buildAuthEnv` so the
   coupling is local and obvious.
2. **Patch `internal/jobspec/entrypoint.go` (EntrypointShell)**: branch
   on `RENOVATE_GITHUB_APP_ID` being set — if present, export
   `RENOVATE_AUTODISCOVER_FILTER` from `.repos`; otherwise export
   `RENOVATE_REPOSITORIES`.
3. **Add tests**:
   - `TestBuildAuthEnv_GitHubAppSetsAutodiscoverTrue`
   - `TestBuildAuthEnv_TokenKeepsAutodiscoverFalse`
   - `TestEntrypointShell_BranchesOnAppEnv` — assert the shell snippet
     contains both branches and the right env-var names.
4. **Update `docs/usage/renovate-platform.md`** with a note that the
   App-auth path uses Renovate's autodiscover internally; the
   operator's discovery + sharding still owns the *which-repos*
   decision via `autodiscoverFilter`.
5. **Verify on homelab**: push to `dev-ci`, restart deployment, watch
   `kubectl get rrun -A` flip to `Succeeded` for the next */5
   boundary.
6. **Bundle with INV-0001 + INV-0002 + PodSecurity** as PR #11 →
   v0.1.2.

## References

- [INV-0001](0001-render-renovatescan-next-run-printer-column-accurately-for.md) — sibling printer-column rendering bug.
- [INV-0002](0002-renovatescan-never-fires-first-run-when-lastruntime-is-unset.md) — sibling never-fires-first-Run scheduling bug.
- [IMPL-0001 Phase 9](../impl/0001-renovate-operator-v010-implementation.md#phase-9-homelab-deploy-and-v010-cutover) — homelab acceptance that surfaced this.
- `internal/jobspec/env.go:65-122` — env build order + autodiscover hardcode.
- `internal/jobspec/entrypoint.go:26-37` — entrypoint shell that exports `RENOVATE_REPOSITORIES`.
- [Renovate self-hosted-config: `githubAppId` / `autodiscover`](https://docs.renovatebot.com/self-hosted-configuration/#githubappid).
- [Renovate config: `autodiscoverFilter`](https://docs.renovatebot.com/configuration-options/#autodiscoverfilter).
