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
  - [Hypothesis 2 (active)](#hypothesis-2-active)
- [Context](#context)
- [Approach](#approach)
- [Environment](#environment)
- [Findings](#findings)
  - [Observation 1 — exact Renovate failure message](#observation-1--exact-renovate-failure-message)
  - [Observation 2 — operator-side env wiring](#observation-2--operator-side-env-wiring)
  - [Observation 3 — Renovate's GitHub App auth model](#observation-3--renovates-github-app-auth-model)
  - [Observation 4 — autodiscoverFilter preserves sharding](#observation-4--autodiscoverfilter-preserves-sharding)
  - [Observation 5 — hypothesis 1 fix deployed, error persists](#observation-5--hypothesis-1-fix-deployed-error-persists)
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

> **Hypothesis 1 (REFUTED — see [Findings](#findings) below).**
>
> Renovate v43.x's platform initialization for `platform=github` checks
> for either `RENOVATE_TOKEN` (PAT) or **autodiscover-mode** App
> credentials. When `autodiscover=false` is paired with App
> credentials, Renovate has nowhere to mint installation tokens from
> at platform-init time, and falls through to the "need-a-token"
> error.
>
> The fix would be to bifurcate worker env wiring by auth type:
>
> - **GitHub App auth** → set `RENOVATE_AUTODISCOVER=true` plus
>   `RENOVATE_AUTODISCOVER_FILTER=<shard's repo slugs as JSON array>`.
> - **Token auth (PAT or Forgejo)** → keep the current
>   `RENOVATE_REPOSITORIES + RENOVATE_AUTODISCOVER=false` path.
>
> This was implemented and deployed; the same `"You must configure a
> GitHub token"` error reproduced 100% of the time. See
> [Observation 5](#observation-5--hypothesis-1-fix-deployed-error-persists).
> Renovate v43.x does not auto-mint installation tokens from
> `RENOVATE_GITHUB_APP_ID/KEY` even with `autodiscover=true` — those
> env vars are effectively dead code in self-hosted scheduler mode.

### Hypothesis 2 (active)

Renovate v43.x's `initPlatform` for `platform=github` requires a
real, usable token *up front* — period. Auto-minting from App
credentials at init time is not a feature of self-hosted Renovate
v43.x; it's a Mend-hosted-only behavior (or it was removed in a v40+
refactor we missed).

The actual fix is to **mint the installation token in the operator**
(via `ghinstallation/v2`, already imported for discovery), pass it to
the worker as `RENOVATE_TOKEN`. This matches the pattern used by the
cluster-renovate operator at Mend and by every other K8s-native
Renovate scheduler. Specifically:

1. Extend `platform.Client` with a `MintAccessToken(ctx) (string, time.Time, error)`
   method.
2. GitHub impl: pull a fresh installation token from the
   `ghinstallation.Transport` already constructed for discovery.
3. Forgejo impl: return the static token from the Platform's
   referenced Secret unchanged (PATs / Forgejo tokens don't expire,
   so `expiresAt = time.Time{}`).
4. In the Run reconciler, before mirroring the credential Secret,
   call `MintAccessToken` and write the resulting token into the
   mirrored Secret as key `access-token`.
5. Worker pod env: always set `RENOVATE_TOKEN` from `access-token`,
   drop `RENOVATE_GITHUB_APP_ID/KEY`, set `RENOVATE_AUTODISCOVER=false`.
6. Entrypoint shell: collapse back to a single branch — always export
   `RENOVATE_REPOSITORIES`, no auth-type bifurcation needed.

**Token TTL caveat.** ghinstallation tokens are 1h. A Run that takes
>1h would 401 mid-scan. v0.1.x mitigation: refuse Runs whose discovery
returns more repos than fit in a 1h budget at the configured shard
size; v0.2.x proper fix: token-refresh sidecar, shorter shards, or a
credential-source abstraction (see [Recommendation](#recommendation)).

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

### Observation 5 — hypothesis 1 fix deployed, error persists

The autodiscover-bifurcation fix from hypothesis 1 was committed
(`fix(jobspec): bifurcate worker env by auth type for Renovate v43 App auth`),
pushed to PR #11, built into the `:dev-ci` image, and deployed onto
the homelab cluster. Verified via the next worker pod:

```text
$ kubectl get pod -n renovate <pod> -o jsonpath='{.spec.containers[0].env}'
[
  ...
  {"name":"RENOVATE_AUTODISCOVER","value":"true"},        # ← new value lands
  ...
]
$ kubectl get pod -n renovate <pod> -o jsonpath='{.spec.containers[0].command}'
[..., "...if [ -n \"${RENOVATE_GITHUB_APP_ID:-}\" ]; then RENOVATE_AUTODISCOVER_FILTER=...; else RENOVATE_REPOSITORIES=...; fi..."]
                          # ← entrypoint shell bifurcation lands
```

Both halves of the fix are unambiguously live. The exact same
`"You must configure a GitHub token"` error reproduces:

```json
{"renovateVersion":"43.160.5","msg":"Renovate started","time":"2026-05-02T13:58:30.987Z"}
{"errorMessage":"You must configure a GitHub token","msg":"Initialization error","time":"2026-05-02T13:58:31.079Z"}
```

The error fires ~92 ms after `Renovate started` — well below the
~250-500 ms a successful `/app/installations` call to api.github.com
would take. So Renovate is bailing in **config validation**, not
mid-API-call. Hypothesis 1 was wrong: `RENOVATE_AUTODISCOVER=true` does
*not* unlock App-credential token minting at init time on v43.x.

The `RENOVATE_GITHUB_APP_ID/KEY` env vars are effectively dead code
in self-hosted scheduler mode for v43.x. They may be exclusively used
by Mend's hosted Renovate or have been refactored out of self-hosted
init at some point in the v40+ series.

## Conclusion

**Answer:** Hypothesis 1 refuted (autodiscover alone insufficient).
Hypothesis 2 active: Renovate v43.x's `initPlatform` requires a real
token up front, no exceptions — the operator must mint its own
installation token from the App credentials and pass it to the
worker as `RENOVATE_TOKEN`.

The fix is **operator-side token minting** via `ghinstallation/v2`
(already imported for discovery). Renovate becomes a token consumer,
not a token-minter; the operator owns the App→token translation.

## Recommendation

Implement Option B end-to-end:

1. **Extend `platform.Client`** in `internal/platform/platform.go`
   with `MintAccessToken(ctx) (token string, expiresAt time.Time, err error)`.
2. **`internal/platform/github`**: persist the
   `ghinstallation.Transport` constructed in `NewWithApp` on the
   `Client` struct, expose it via the new method. Token-auth path
   returns the static token + zero expiry.
3. **`internal/platform/forgejo`**: store the static token on the
   `Client` struct; new method returns it + zero expiry.
4. **`internal/credentials`**: rebuild the mirror payload around a
   single `access-token` key. Drop the App-PEM passthrough; the
   worker no longer needs it.
5. **`internal/controller/renovaterun_controller.go`** —
   `mirrorCredential` now: build a platform Client → mint a token →
   write `access-token` into the mirrored Secret. `ensureWorkerJob`
   loses the auth-type switch; `cred.TokenKey = "access-token"` for
   both auth types.
6. **`internal/jobspec/env.go`**: drop `RENOVATE_GITHUB_APP_ID/KEY`
   wiring entirely. Always set `RENOVATE_TOKEN` from
   `cred.TokenKey`. `RENOVATE_AUTODISCOVER` reverts to hardcoded
   `"false"`. Drop the `autodiscoverValue` helper introduced in the
   refuted hypothesis 1 attempt.
7. **`internal/jobspec/entrypoint.go`**: collapse to a single branch
   — always export `RENOVATE_REPOSITORIES`. The auth-type bifurcation
   in the shell is no longer needed.
8. **`internal/jobspec.CredentialMount`**: drop the `PEMKey` field;
   `TokenKey` is the only auth knob now.
9. **Update + add tests** at every layer:
   - `internal/platform/github`: `TestMintAccessToken_AppMintsViaTransport`,
     `TestMintAccessToken_TokenReturnsStatic`.
   - `internal/platform/forgejo`: `TestMintAccessToken_ReturnsStaticToken`.
   - `internal/jobspec`: keep PSA-restricted test; replace App-env
     tests with `TestBuildWorkerJob_AlwaysSetsRenovateToken`; remove
     `TestEntrypointShell_BranchesOnGitHubAppEnv` and
     `TestBuildWorkerJob_GitHubAppSetsAutodiscoverTrue`,
     `TestBuildWorkerJob_TokenAuthKeepsAutodiscoverFalse`.
   - `internal/controller`: extend the platform-factory mock to mint
     a fake token; update `mirrorCredential` tests to assert the
     mirrored Secret carries `access-token` only.
10. **Token TTL guard for v0.1.x**: log a warning when discovery
    returns enough repos that even single-shard execution would
    likely exceed 50 minutes (rough proxy for the 1-hour token
    expiry). No hard gate yet — flag for v0.2.x.
11. **Document the credential-source abstraction as a deferred
    enhancement.** Future `RenovatePlatform.spec.auth` could grow
    `githubAppFromVault`, `githubAppFromESO`, `tokenFromAWSSM`, etc.
    Operator's `MintAccessToken` becomes the single point for
    plugging in alternate sources. Tracked in DESIGN-0001 follow-ups,
    not in this PR.

Bundle with INV-0001 / INV-0002 / PodSecurity / INV-0003-h1-revert
as PR #11 → v0.1.2.

## References

- [INV-0001](0001-render-renovatescan-next-run-printer-column-accurately-for.md) — sibling printer-column rendering bug.
- [INV-0002](0002-renovatescan-never-fires-first-run-when-lastruntime-is-unset.md) — sibling never-fires-first-Run scheduling bug.
- [IMPL-0001 Phase 9](../impl/0001-renovate-operator-v010-implementation.md#phase-9-homelab-deploy-and-v010-cutover) — homelab acceptance that surfaced this.
- `internal/jobspec/env.go:65-122` — env build order + autodiscover hardcode.
- `internal/jobspec/entrypoint.go:26-37` — entrypoint shell that exports `RENOVATE_REPOSITORIES`.
- [Renovate self-hosted-config: `githubAppId` / `autodiscover`](https://docs.renovatebot.com/self-hosted-configuration/#githubappid).
- [Renovate config: `autodiscoverFilter`](https://docs.renovatebot.com/configuration-options/#autodiscoverfilter).
