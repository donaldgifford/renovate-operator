---
id: INV-0004
title: "GitHub Discover bypasses the App installation grant for personal accounts"
status: Open
author: Donald Gifford
created: 2026-05-02
---
<!-- markdownlint-disable-file MD025 MD041 -->

# INV 0004: GitHub Discover bypasses the App installation grant for personal accounts

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
  - [Observation 1 — installation token grants exactly 4 repos](#observation-1--installation-token-grants-exactly-4-repos)
  - [Observation 2 — operator processed a disjoint set of 5 repos](#observation-2--operator-processed-a-disjoint-set-of-5-repos)
  - [Observation 3 — Discover falls back to /users/{owner}/repos](#observation-3--discover-falls-back-to-usersownerrepos)
  - [Observation 4 — public-vs-private split explains the disjoint set](#observation-4--public-vs-private-split-explains-the-disjoint-set)
  - [Observation 5 — explicit `baseURL: https://api.github.com` doubles the bug surface](#observation-5--explicit-baseurl-httpsapigithubcom-doubles-the-bug-surface)
- [Conclusion](#conclusion)
- [Recommendation](#recommendation)
- [References](#references)
<!--toc:end-->

## Question

A `RenovateScan` configured against a `RenovatePlatform` whose GitHub
App grants only 4 specific repos (`homelab`, `server-price-tracker`,
`nix-config`, `renovate-config`) materialized a Run that processed 5
*completely different* repos (`docz`, `hlsa`, `mdp`,
`one-click-hugo-cms`, `webhookd`). How is the operator finding repos
the App was never granted access to, and what's the smallest change
that makes Discover honor the App's installation grant?

## Hypothesis

`internal/platform/github/discover.go`'s `Discover` calls
`/orgs/{owner}/repos` first; for a personal account that endpoint
404s, and the existing fallback hits `/users/{owner}/repos`. The
public user-repos endpoint returns every public repo for the user
**regardless** of which repos a given installation token was granted.
The App's installation grant is enforced only at *write* time
(opening PRs, modifying refs); read-only listing of public repos
works with any token.

So Discover happily returns the union of "every public repo for
`donaldgifford`" and ignores the App's "Only select repositories"
configuration entirely. The 4 grant'd repos are private, so they
don't appear in the user-public listing; the 5 processed repos are
public, so they do.

The fix is to route App-auth Clients through `GET
/installation/repositories` (go-github's
`gh.Apps.ListRepos`), which returns exactly what the installation
was granted — public *and* private — in one paginated call. PAT
auth keeps the existing org → user fallback (a PAT has no
"installation" concept).

## Context

Surfaced live during the Phase 9 homelab acceptance run on
2026-05-02, **after** the INV-0003 operator-side token-minting fix
got the GitHub App auth path end-to-end working. Run
`test-gh-kgzmw` reached `Phase=Succeeded` on 5 repos; user noticed
those 5 repos didn't match the screenshot of the App's "Only select
repositories" configuration (which listed 4 different ones). Initial
hypothesis ("the user installed the official Renovate App too,
overlap explains it") was disproved by re-running after that App was
removed — `test-gh-hhbc4` processed the same 5 public repos.

Severity is real but not data-loss-grade: Renovate succeeds at
*reading* the wrong repos, but PR creation against them would
silently fail (the operator-minted token has no write permission
outside the grant). The user wastes API budget and clones, but
nothing is published to repos the App wasn't authorized for.

**Triggered by:** Phase 9 homelab acceptance — first end-to-end
Run against a GitHub App installed in single-account mode with
"Only select repositories" enabled. The bug would not have
reproduced if the App was installed with "All repositories" or if
all of the user's repos were private.

## Approach

1. **Confirm what the App actually grants** by minting a token
   from the operator's mirrored Secret and hitting
   `GET /installation/repositories` directly. This bypasses the
   operator's discovery code path and reveals the true grant.
2. **Confirm what the operator discovers** by reading the
   processed-repo list from a fresh worker pod's stdout
   (Renovate's `Repository started` lines are 1:1 with what the
   operator handed it as `RENOVATE_REPOSITORIES`).
3. **Read `internal/platform/github/discover.go`** to see which
   endpoint Discover hits.
4. **Cross-reference go-github's `Apps.ListRepos`**, which wraps
   `GET /installation/repositories`. Method signature returns
   `(*ListRepositories, *Response, error)` where
   `ListRepositories.Repositories` is the granted set.
5. **Implement** an App-auth branch in Discover that calls
   `Apps.ListRepos` and intersects with `filter.Owner`. PAT path
   unchanged.
6. **Add tests** covering both the App-auth `/installation/repositories`
   path and the existing PAT org → user fallback.
7. **Verify on homelab** by deploying `:dev-ci` and re-running the
   Scan; the next Run should discover only the 4 grant'd repos.

## Environment

| Component | Version / Value |
|-----------|----------------|
| Renovate worker image | `ghcr.io/renovatebot/renovate:latest` (resolved to v43.160.5) |
| Operator | `dev-ci` of branch `docs/inv-0001-next-run-column` (post-INV-0003 Hypothesis 2 fix) |
| Auth | `RenovatePlatform.spec.auth.githubApp` with `installationID=128819531` |
| App grant | 4 private repos: `donaldgifford/{homelab, server-price-tracker, nix-config, renovate-config}` |
| Operator discovered | 5 public repos: `donaldgifford/{docz, hlsa, mdp, one-click-hugo-cms, webhookd}` |
| Affected file | `internal/platform/github/discover.go:35-65` |

## Findings

### Observation 1 — installation token grants exactly 4 repos

```text
$ TOKEN=$(kubectl -n renovate get secret renovate-creds-test-gh-kgzmw \
    -o jsonpath='{.data.access-token}' | base64 -d)
$ curl -s -H "Authorization: Bearer $TOKEN" \
    https://api.github.com/installation/repositories | jq '.repositories[].full_name'
"donaldgifford/homelab"
"donaldgifford/server-price-tracker"
"donaldgifford/nix-config"
"donaldgifford/renovate-config"
```

The same operator-minted token used by the worker for Renovate
returns exactly 4 repos when asked through the installation-scoped
endpoint. This is the App's authoritative grant — public + private,
intersected with the installation's "Only select repositories"
configuration.

### Observation 2 — operator processed a disjoint set of 5 repos

Worker pod `test-gh-hhbc4-worker-0` (Run `test-gh-hhbc4`,
2026-05-02 17:33Z, fired *after* the unrelated official Renovate App
was uninstalled) processed:

```text
donaldgifford/docz
donaldgifford/hlsa
donaldgifford/mdp
donaldgifford/one-click-hugo-cms
donaldgifford/webhookd
```

Each repo reached `Repository finished, status onboarded, result
done`. None of those 5 repos are in the installation grant from
Observation 1. Therefore the operator's discovery is reading a
wider set than the App actually authorizes — the operator's
discovery, not Renovate's, is the source of those 5 slugs (worker
env has `RENOVATE_AUTODISCOVER=false` and a fixed
`RENOVATE_REPOSITORIES` array set from the shard JSON).

### Observation 3 — Discover falls back to `/users/{owner}/repos`

`internal/platform/github/discover.go:35-65`:

```go
func (c *Client) Discover(ctx context.Context, filter platform.DiscoveryFilter) ([]platform.Repository, error) {
    if filter.Owner == "" {
        return nil, fmt.Errorf("github: DiscoveryFilter.Owner required")
    }

    repos, err := c.listOrgRepos(ctx, filter.Owner)
    if err != nil {
        // /orgs/{owner}/repos 404s for personal accounts; fall back.
        var notFound bool
        if isNotFound(err) {
            notFound = true
        }
        if !notFound {
            return nil, err
        }
        repos, err = c.listUserRepos(ctx, filter.Owner)
        // ...
    }
    // ...
}
```

`donaldgifford` is a personal account, so `/orgs/donaldgifford/repos`
404s, and Discover falls back to `/users/donaldgifford/repos`. That
endpoint returns *all* public repos for the user — it has no
notion of which installation token is making the request, and the
installation grant doesn't filter the response.

### Observation 4 — public-vs-private split explains the disjoint set

The 4 repos the App actually grants (`homelab`,
`server-price-tracker`, `nix-config`, `renovate-config`) are
private. They don't appear in the public user-repos listing.

The 5 repos Renovate processed (`docz`, `hlsa`, `mdp`,
`one-click-hugo-cms`, `webhookd`) are public. They appear in the
public user-repos listing because they're public — completely
independent of the App's installation grant.

The disjoint set is the natural consequence: Discover returns
`(public ∩ owner=donaldgifford)` while the App grants
`(installation-selected ∩ private)`. The two sets happen to not
overlap.

If the App had been installed with "All repositories", or if the
user had any *public* repos in the selected set, the bug would have
been masked.

## Conclusion

**Answer:** `Discover` for App-auth Clients ignores the App's
installation grant entirely. The org → user fallback path lists
public repos for the owner regardless of which installation
authorized the request. The bug only manifests for personal-account
+ "Only select repositories" + at-least-one-public-repo-not-in-grant
configurations; it would be invisible for org installations or
"All repositories" installations.

The fix is to route App-auth Clients through
`/installation/repositories` (go-github `Apps.ListRepos`). PAT
auth has no installation concept, so it keeps the existing org →
user fallback.

### Observation 5 — explicit `baseURL: https://api.github.com` doubles the bug surface

After deploying the `Apps.ListRepos` switch on the homelab, the next Run
still failed with `DiscoveryFailed: no repositories matched discovery
filter`. Direct curl with the operator's minted token returned the
expected 4 grant'd repos. Operator code, however, returned 0.

The user's `RenovatePlatform.spec.baseURL` was set to
`https://api.github.com` — copy-pasted from the example in
`docs/usage/renovate-platform.md`. This triggered
`gh.WithEnterpriseURLs(baseURL, baseURL)` in
`internal/platform/github/client.go:191`, which prepends `/api/v3/` to
every request path. For the legacy org/user listings,
`https://api.github.com/api/v3/users/{owner}/repos` *happens* to redirect
or alias to the right place — the prior 5-repo Run worked. But
`https://api.github.com/api/v3/installation/repositories` does not
behave the same: api.github.com responds with a shape go-github happily
parses but with empty `Repositories`. Discover's loop returned 0.

Fix layered on top of Observation 1-4's recommendation: detect
`api.github.com` in the constructor and skip both
`itr.BaseURL = baseURL` (ghinstallation override) and
`gh.WithEnterpriseURLs` (go-github GHE prefix). New helper
`isPublicGitHub(baseURL)` returns true for `https://api.github.com` /
`http://api.github.com` (with or without trailing slash). Test
`TestDiscover_AppAuth_PublicGitHubBaseURLDoesNotUseEnterprisePrefix`
asserts no request issued under that misconfiguration carries `/api/v3/`.

The doc example in `renovate-platform.md` no longer suggests setting
`baseURL: https://api.github.com` — that field is now reserved for
actual GitHub Enterprise Server URLs.

## Recommendation

Implement the fix on PR #11 alongside INV-0001 / INV-0002 /
PodSecurity / INV-0003. Bundle into v0.1.2.

1. **`internal/platform/github/discover.go`** — split Discover by
   auth type. App auth (`c.appTransport != nil`) calls a new
   `listInstallationRepos` helper that hits `Apps.ListRepos`,
   paginates, and intersects with `filter.Owner` (defensive — the
   API is already installation-scoped, but a Client could in
   principle be reused across owners). PAT auth keeps the
   existing `listOrgRepos` → `listUserRepos` fallback.
2. **Tests in `internal/platform/github/discover_test.go`** (new
   file) cover the App-auth path: httptest server mocks
   `/app/installations/{id}/access_tokens` (for token mint) and
   `/installation/repositories` (for the grant list). Assert the
   returned repos match the granted set, and that out-of-owner
   repos in the response are filtered out. Existing PAT tests in
   `client_test.go` continue to pass unchanged.
3. **Document** in `docs/usage/authorization.md` that the operator
   discovers exactly the repos in the App's installation grant —
   no surprise reads of public repos outside the grant.
4. **No CRD or env-var changes**; this is purely a Discover
   internals fix.

The token TTL caveat from INV-0003 still applies — installation
tokens minted by `ghinstallation/v2` are ~1h, so `Apps.ListRepos`
must complete (alongside discovery + scan) within that budget.

## References

- [INV-0003](0003-renovate-v43-github-app-auth-requires-autodiscover-not.md) — the operator-side token-minting fix that made it possible to verify the grant directly via `/installation/repositories`.
- `internal/platform/github/discover.go:35-65` — Discover with the org → user fallback.
- [go-github `Apps.ListRepos`](https://pkg.go.dev/github.com/google/go-github/v62/github#AppsService.ListRepos) — wraps `GET /installation/repositories`.
- [GitHub REST API: List repositories accessible to the app installation](https://docs.github.com/en/rest/apps/installations#list-repositories-accessible-to-the-app-installation).
- [IMPL-0001 Phase 9](../impl/0001-renovate-operator-v010-implementation.md#phase-9-homelab-deploy-and-v010-cutover) — homelab acceptance that surfaced this.
