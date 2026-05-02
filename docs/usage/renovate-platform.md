# RenovatePlatform

Cluster-scoped credential bundle. One `RenovatePlatform` per Git platform
_installation_: a GitHub App installed in three orgs needs three
`RenovatePlatform` resources, each pointing at the same App but with a different
`installationID`.

| Property    | Value                                                                                                                    |
| ----------- | ------------------------------------------------------------------------------------------------------------------------ |
| API group   | `renovate.fartlab.dev`                                                                                                   |
| Kind        | `RenovatePlatform`                                                                                                       |
| Scope       | Cluster                                                                                                                  |
| Short names | `rp`, `rplatform`                                                                                                        |
| Sample      | [`config/samples/renovate_v1alpha1_renovateplatform.yaml`](../../config/samples/renovate_v1alpha1_renovateplatform.yaml) |

## Why cluster-scoped?

A Platform represents a credential. Credentials are infrastructure, not
workloads. Multiple Scans in different namespaces reference the same Platform by
name; the operator mirrors the credential Secret into each Scan's namespace at
Run time so worker Pods don't need cross-namespace Secret access.

## Auth

`spec.auth` is a discriminated union — exactly one of `githubApp` or `token`
must be set. CEL validation on the CRD enforces this at admission time.

### GitHub App (`auth.githubApp`)

Mints installation tokens via `bradleyfalzon/ghinstallation/v2`. Renovate itself
does the per-call JWT minting; the operator only mints tokens for its own
discovery API calls.

```yaml
apiVersion: renovate.fartlab.dev/v1alpha1
kind: RenovatePlatform
metadata:
  name: github
spec:
  platformType: github
  baseURL: https://api.github.com # optional; defaults to api.github.com
  auth:
    githubApp:
      appID: 123456
      installationID: 67890123
      privateKeyRef:
        name: renovate-github-app
        key: private-key.pem # optional; default key is "private-key.pem"
```

The Secret must exist in the **operator's release namespace** (typically
`renovate-system`), not in your Scan's namespace:

```bash
kubectl -n renovate-system create secret generic renovate-github-app \
  --from-file=private-key.pem=$HOME/.ssh/renovate-app.pem
```

### Token (`auth.token`)

Personal-access-token / Forgejo-token auth. Used for Forgejo and (rarely) GitHub
PAT setups.

```yaml
apiVersion: renovate.fartlab.dev/v1alpha1
kind: RenovatePlatform
metadata:
  name: forgejo
spec:
  platformType: forgejo
  baseURL: https://forgejo.example.com # required for Forgejo; no default
  auth:
    token:
      secretRef:
        name: renovate-forgejo-token
        key: token # optional; default key is "token"
```

Create the Secret:

```bash
kubectl -n renovate-system create secret generic renovate-forgejo-token \
  --from-literal=token=glpat-...
```

## Optional spec fields

| Field           | Purpose                                                                                                                                                                                                                 |
| --------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `runnerConfig`  | Opaque JSON passed as `RENOVATE_CONFIG` to every worker. Use for runner-level settings (`binarySource`, `dryRun`, `hostRules`, `onboarding`). Layered with per-Scan `renovateConfigOverrides` (Scan wins on collision). |
| `presetRepoRef` | Renovate preset reference (e.g., `github>donaldgifford/renovate-config`). Workers prepend it to each repo's `renovate.json` as an `extends` entry.                                                                      |
| `renovateImage` | Worker container image. Default `ghcr.io/renovatebot/renovate:latest`. Pin to a specific tag for production (see `test/manual/README.md` for the recommended pinning workflow).                                         |

## Status

```bash
kubectl get rplatform <name> -o yaml | yq .status
```

Tracked condition: **`Ready`**. Reasons:

| Reason                | Meaning                                                                                |
| --------------------- | -------------------------------------------------------------------------------------- |
| `CredentialsResolved` | Secret resolved, key validated, ready for use.                                         |
| `SecretNotFound`      | Referenced Secret doesn't exist in the operator's release namespace.                   |
| `KeyMissing`          | Secret exists but `auth.*.privateKeyRef.key` / `auth.*.secretRef.key` isn't in `data`. |
| `AuthFailed`          | PEM parse failed (App) or token format invalid (token).                                |
| `PlatformUnreachable` | API endpoint didn't respond — DNS, proxy, or upstream outage.                          |

The operator watches both the Platform and the referenced Secret — creating or
updating the Secret triggers a re-reconcile within a second.

## Examples

### One Platform, multiple installations

```yaml
# fleet of three GitHub orgs sharing one App
---
apiVersion: renovate.fartlab.dev/v1alpha1
kind: RenovatePlatform
metadata: { name: github-org-a }
spec:
  platformType: github
  auth:
    {
      githubApp:
        { appID: 100, installationID: 1001, privateKeyRef: { name: gh-app } },
    }
---
apiVersion: renovate.fartlab.dev/v1alpha1
kind: RenovatePlatform
metadata: { name: github-org-b }
spec:
  platformType: github
  auth:
    {
      githubApp:
        { appID: 100, installationID: 1002, privateKeyRef: { name: gh-app } },
    }
---
apiVersion: renovate.fartlab.dev/v1alpha1
kind: RenovatePlatform
metadata: { name: github-org-c }
spec:
  platformType: github
  auth:
    {
      githubApp:
        { appID: 100, installationID: 1003, privateKeyRef: { name: gh-app } },
    }
```

Each can be referenced from its own `RenovateScan` by name.

### Both auth modes side by side

```yaml
---
apiVersion: renovate.fartlab.dev/v1alpha1
kind: RenovatePlatform
metadata: { name: github }
spec:
  platformType: github
  auth:
    {
      githubApp:
        {
          appID: 123456,
          installationID: 67890,
          privateKeyRef: { name: gh-app },
        },
    }
  presetRepoRef: github>donaldgifford/renovate-config
---
apiVersion: renovate.fartlab.dev/v1alpha1
kind: RenovatePlatform
metadata: { name: forgejo }
spec:
  platformType: forgejo
  baseURL: https://forgejo.example.com
  auth: { token: { secretRef: { name: forgejo-token } } }
```

A single operator deployment serves both; each Scan picks the Platform it needs
by `platformRef.name`.

## Common pitfalls

- **Putting the Secret in the wrong namespace.** It must live in the operator's
  _release_ namespace. Put it anywhere else and `Ready` stays `SecretNotFound`.
- **Sharing one Platform across installations.** The operator caches
  per-Platform rate-limit budgets and tokens. Sharing a single Platform across
  installations collapses three rate buckets into one and produces ambiguous
  metrics. Use one Platform per installation.
- **Forgejo without `baseURL`.** GitHub defaults to `api.github.com`; Forgejo
  has no default and CEL rejects the manifest at admission.
- **PEM with trailing whitespace.** Some 1Password/ESO renderers add a trailing
  newline; this can pass `openssl rsa -check` but break `ghinstallation`'s
  parser. Inspect with
  `kubectl get secret <name> -o jsonpath='{.data.private-key\.pem}' | base64 -d | xxd | tail -3`.

## See also

- [Installation](installation.md) — operator install / chart config.
- [RenovateScan](renovate-scan.md) — the resource that consumes a Platform.
- [`test/manual/README.md`](../../test/manual/README.md) — Scenario A (GitHub
  App) and Scenario B (Forgejo token) end-to-end runbooks.
