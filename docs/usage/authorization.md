# Authorization

Two parties need credentials at runtime:

1. **The operator itself** — for repo _discovery_: listing repos that match a
   Scan's filter, and probing each repo for a Renovate config file before
   committing it to a shard.
2. **Renovate workers** — the actual renovatebot CLI running inside the
   per-shard worker pods. Workers do the heavy lifting: clone, branch, commit,
   push, open PRs, leave Dependency Dashboard issues.

Both consume the same credential (the one referenced by the `RenovatePlatform`'s
`auth.*.privateKeyRef` / `auth.*.secretRef`). So the credential needs the
_union_ of operator-side and Renovate-side permissions.

This doc covers what to grant for each platform.

## GitHub App (`platformType: github`)

### Permissions

When you register the GitHub App at **Settings → Developer settings → GitHub
Apps → New GitHub App**, set:

| Category                     | Permission     | Level        | Why                                                                                                                                                                                     |
| ---------------------------- | -------------- | ------------ | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **Repository permissions**   | Metadata       | Read         | Auto-required by GitHub for any repo access.                                                                                                                                            |
|                              | Contents       | Read + Write | Operator reads `renovate.json` (read); Renovate creates branches and commits (write).                                                                                                   |
|                              | Pull requests  | Read + Write | Renovate opens, updates, and (optionally) closes PRs.                                                                                                                                   |
|                              | Issues         | Read + Write | Renovate creates the Dependency Dashboard issue and posts updates.                                                                                                                      |
|                              | Workflows      | Read + Write | Required if any tracked package manager produces updates to `.github/workflows/*.yml`. Without this, those updates fail with `refusing to allow a GitHub App to update workflow files`. |
|                              | Checks         | Read         | Renovate inspects PR check status when deciding whether to auto-merge. Skip if you never use `automerge: true`.                                                                         |
|                              | Administration | Read         | Optional. Lets Renovate detect default-branch protection settings. Skip if you don't customize per-repo automerge behavior based on protection.                                         |
| **Organization permissions** | Members        | Read         | Required for `assignees` / `reviewers` config to resolve org membership. Skip if you only assign individual users by login.                                                             |

| Category                | Permission | Level                                                                                                                                               |
| ----------------------- | ---------- | --------------------------------------------------------------------------------------------------------------------------------------------------- |
| **User permissions**    | none       | —                                                                                                                                                   |
| **Subscribe to events** | none       | v0.1.0 has no webhook flow. Phase-2 webhook-triggered runs (Non-Goal in DESIGN-0001) would add `pull_request`, `push`, `installation_repositories`. |

### Where to find the values

After creating the App and installing it on your org(s):

| Field on `RenovatePlatform.spec.auth.githubApp` | Where in GitHub UI                                                                                                                                                                                      |
| ----------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `appID`                                         | App settings page → "App ID" near the top.                                                                                                                                                              |
| `installationID`                                | Org settings → Integrations → GitHub Apps → your App → "Configure" → URL ends in `/installations/<id>`.                                                                                                 |
| `privateKeyRef.name`                            | The private key is downloaded as a `.pem` file from the App settings page (one-time download). Store in a Kubernetes Secret in the operator's release namespace; the `name` here points at that Secret. |
| `privateKeyRef.key`                             | The data key inside the Secret (default `private-key.pem`).                                                                                                                                             |

### Discovery scope follows the installation grant

The operator discovers repos through GitHub's installation-scoped
`/installation/repositories` endpoint, which returns *exactly* the repos
the App was granted — public **and** private. Repos outside the
installation grant (e.g., other public repos owned by the same user)
are never enumerated, never cloned, never touched. This is enforced
client-side in `internal/platform/github/discover.go` and verified in
INV-0004; see that doc for the bug history.

In practice: if your App is installed with **"Only select repositories"**,
only those repos can ever be discovered. Switch to **"All repositories"**
on the installation if you want broader coverage — adding a new repo to
GitHub will then automatically pick it up on the next Scan.

### Multiple installations of one App

A GitHub App registered once can be installed on many orgs. Each installation
gets a different `installationID`. For each installation you want the operator
to scan, declare a separate `RenovatePlatform`:

```yaml
---
apiVersion: renovate.fartlab.dev/v1alpha1
kind: RenovatePlatform
metadata: { name: github-org-a }
spec:
  platformType: github
  auth:
    githubApp:
      appID: 100 # same App
      installationID: 1001 # org-a's installation
      privateKeyRef: { name: gh-app-key }
---
apiVersion: renovate.fartlab.dev/v1alpha1
kind: RenovatePlatform
metadata: { name: github-org-b }
spec:
  platformType: github
  auth:
    githubApp:
      appID: 100 # same App
      installationID: 1002 # org-b's installation
      privateKeyRef: { name: gh-app-key } # same Secret — only the installation differs
```

The PEM in the Secret stays the same; only the `installationID` differs. Each
Platform gets its own per-installation rate-limit budget (4500 req/hr per
installation per IMPL-0001 Q2).

### Rate limits

GitHub Apps get a per-installation primary rate-limit budget that scales with
repo count, with a floor of 5000 requests/hour. Renovate-operator's default
discovery client targets 4500 req/hr to stay well clear of the ceiling. The
operator emits per-Platform rate-limit metrics; if you see
`renovate_operator:rate_limit:remaining` trending toward zero, lower scan
frequency or split a single broad Scan into multiple narrower ones.

## Forgejo / Gitea (`platformType: forgejo`)

### Token scopes

Forgejo personal-access-tokens use a coarser scope model than GitHub. Create the
token at **User settings → Applications → Generate new token** and select:

| Scope                                              | Why                                                                                                                            |
| -------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------ |
| `repo` (or `read:repository` + `write:repository`) | Operator lists org/user repos and reads `renovate.json` (read); Renovate clones, branches, commits, pushes, opens PRs (write). |
| `write:issue` (or `repo` covers it)                | Renovate creates the Dependency Dashboard issue.                                                                               |
| `read:user`                                        | Required so the token's username is resolvable for `assignees` / `reviewers`. Usually included with `repo`.                    |

If your Forgejo PAT UI only exposes a single `repo` toggle, that's enough — that
scope encompasses contents read+write, PR read+write, and issue create.

### Service-account vs. user token

The token can come from either a real user account or a dedicated service
account. **Strongly recommended: use a service account.** PRs Renovate opens
will be authored by the token's owner; commits will appear under that identity.
A service account keeps the noise out of human contributors' profiles and makes
it easy to revoke without disrupting an actual person's access.

### Forgejo version compatibility

**Honest answer: not formally pinned.** v0.1.0 uses
[`code.gitea.io/sdk/gitea`](https://pkg.go.dev/code.gitea.io/sdk/gitea) v0.24.1,
which is API-compatible with both Gitea and Forgejo. The only Forgejo-specific
endpoints the operator hits are:

- `GET /api/v1/orgs/{owner}/repos` (paginated repo listing)
- `GET /api/v1/users/{user}/repos` (fallback when `/orgs/{owner}` 404s)
- `GET /api/v1/repos/{owner}/{name}/contents/{path}` (config-presence probe)

These have been in the Gitea/Forgejo API since Gitea 1.13 / Forgejo 1.18 — so in
practice anything modern works. We have not done conformance runs against
specific Forgejo LTS lines (e.g., 7.x, 10.x) for v0.1.0. The first homelab
acceptance run (Phase 9 in IMPL-0001) is against the maintainer's own Forgejo
instance; if you're running an older or unusual fork and discovery misbehaves,
file an issue with your Forgejo version and we'll add a compatibility note.

What we **do** rely on:

- The `/orgs/{owner}/repos` paginated endpoint
- The `/users/{user}/repos` paginated endpoint
- The `/repos/{owner}/{name}/contents/{path}` endpoint returning 404 for missing
  files (used by the `requireConfig` probe)
- Standard Bearer-token auth on `/api/v1/*`

What we **don't** rely on (Forgejo-specific or version-specific features that
might drift):

- Webhooks (no webhook flow in v0.1.0)
- `topics` on repo objects (Forgejo SDK doesn't surface them on the repo struct,
  so the `discovery.topics` field on `RenovateScan` is **silently ignored** for
  Forgejo platforms — see `internal/platform/forgejo/discover.go:matchesFilter`)
- Branch protection introspection
- `actions` / Forgejo Actions metadata

### Rate limits

Forgejo's rate-limit configuration is per-instance and per-token. The operator
uses a fixed 30 req/sec client-side limiter (see
`internal/platform/forgejo/client.go`'s `defaultRateLimit`) which is
conservative for self-hosted instances. If your Forgejo's `[security] LIMIT_*`
config is more restrictive, lower the operator's effective scan frequency rather
than the hardcoded limiter.

## What about the worker pods?

The operator mounts the **same** Secret into each worker pod as
`/etc/renovate/credentials/...`. Renovate's CLI reads the GitHub App PEM (and
the operator's pre-minted installation token, which is _also_ mirrored) or the
Forgejo token directly from there.

This means:

- You don't grant Renovate workers separate permissions — they reuse the
  Platform's credential.
- The mirrored Secret in the Scan namespace has the same data as the source
  Secret in the operator namespace. RBAC on the Scan namespace controls who can
  read it; treat that namespace as security-sensitive.
- Rotating the credential = updating the source Secret in the operator
  namespace. The Platform reconciler watches the Secret and re-mirrors on the
  next Run. In-flight Runs use their snapshotted Secret reference (the live
  data), so rotation propagates within one scan cycle.

## Quick checklist

### GitHub App

- [ ] App registered at **Settings → Developer settings → GitHub Apps**.
- [ ] Repository permissions: Metadata (R), Contents (RW), Pull requests (RW),
      Issues (RW), Workflows (RW). Plus Checks (R) if using `automerge`.
- [ ] App installed on the target org(s).
- [ ] Private key downloaded as `.pem`.
- [ ] Secret with `private-key.pem` key created in the **operator's release
      namespace** (e.g., `renovate-system`).
- [ ] One `RenovatePlatform` resource per installation, each with the org's
      `installationID`.

### Forgejo

- [ ] Service account created (preferred over a personal account).
- [ ] PAT generated with `repo` scope (and `write:issue` if granular).
- [ ] Secret with `token` key created in the **operator's release namespace**.
- [ ] `RenovatePlatform` with `platformType: forgejo`, `baseURL` set to your
      instance, and `auth.token.secretRef.name` pointing at the Secret.

## See also

- [RenovatePlatform](renovate-platform.md) — full CRD reference and Secret shape
  requirements.
- [Installation §"`RenovatePlatform` stuck `Ready=False`"](installation.md#renovateplatform-stuck-readyfalse)
  — troubleshooting matrix for credential-resolution failures.
- [Renovate documentation: Self-hosting Renovate](https://docs.renovatebot.com/self-hosted-configuration/)
  for the upstream view on what the worker process needs.
