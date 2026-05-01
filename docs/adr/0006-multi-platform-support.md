---
id: ADR-0006
title: "Multi-platform support (GitHub App and Forgejo) in v0.1.0"
status: Accepted
author: donaldgifford
created: 2026-04-26
---
<!-- markdownlint-disable-file MD025 MD041 -->

# 0006. Multi-platform support (GitHub App and Forgejo) in v0.1.0

<!--toc:start-->
- [Status](#status)
- [Context](#context)
- [Decision](#decision)
  - [Single RenovatePlatform CRD with a discriminated union](#single-renovateplatform-crd-with-a-discriminated-union)
  - [Auth flow: pass-through to Renovate](#auth-flow-pass-through-to-renovate)
  - [Discovery uses platform APIs directly, not Renovate](#discovery-uses-platform-apis-directly-not-renovate)
- [Consequences](#consequences)
  - [Positive](#positive)
  - [Negative](#negative)
  - [Neutral](#neutral)
- [Alternatives Considered](#alternatives-considered)
  - [A. Separate CRDs per platform type](#a-separate-crds-per-platform-type)
  - [B. Flat union of auth fields](#b-flat-union-of-auth-fields)
  - [C. Operator mints all tokens, even for workers](#c-operator-mints-all-tokens-even-for-workers)
  - [D. PAT-only support, defer GitHub App to a later release](#d-pat-only-support-defer-github-app-to-a-later-release)
- [References](#references)
<!--toc:end-->

## Status

Proposed

## Context

[RFC-0001](../rfc/0001-build-kubebuilder-renovate-operator.md) requires the v0.1.0 release to support both GitHub (via GitHub App, not PAT) and Forgejo (via token), so the homelab can drive both its personal Forgejo and its GitHub orgs from a single operator install. This ADR records how that's modeled in the API.

The choices are:

1. **One CRD per platform type** — `RenovateGitHubPlatform`, `RenovateForgejoPlatform`, etc. Each has its own typed schema.
2. **One `RenovatePlatform` CRD with a discriminated union** on `spec.platformType` — auth shape varies by type, validated by CEL or admission webhooks.
3. **One `RenovatePlatform` CRD with a flat union** of all auth fields, using `oneOf`-style validation.

There's also an auth-flow question that the CRD shape doesn't fully decide:

- For GitHub App: does the operator generate JWTs and exchange them for installation access tokens itself, or does it pass the App ID + private key through to Renovate and let Renovate handle the exchange?
- For Forgejo: just a long-lived token, no exchange needed.

## Decision

### Single `RenovatePlatform` CRD with a discriminated union

```go
type RenovatePlatformSpec struct {
    // PlatformType is the discriminator.
    // +kubebuilder:validation:Enum=github;forgejo
    PlatformType PlatformType `json:"platformType"`

    // BaseURL is the platform API endpoint.
    // +optional (defaults set per-type by the controller)
    BaseURL string `json:"baseURL,omitempty"`

    // Auth is a discriminated union of the per-platform auth shapes.
    Auth PlatformAuth `json:"auth"`

    // RunnerConfig is the Renovate runner-level config (config.js equivalent),
    // applied to every Job spawned for Scans referencing this Platform.
    // +kubebuilder:pruning:PreserveUnknownFields
    // +optional
    RunnerConfig *runtime.RawExtension `json:"runnerConfig,omitempty"`

    // PresetRepoRef is the Renovate preset repo for shared config
    // (e.g. "github>donaldgifford/renovate-config").
    // +optional
    PresetRepoRef string `json:"presetRepoRef,omitempty"`

    // RenovateImage is the container image used to run Renovate.
    // +kubebuilder:default="ghcr.io/renovatebot/renovate:latest"
    RenovateImage string `json:"renovateImage,omitempty"`
}

type PlatformAuth struct {
    // GitHubApp is required when platformType == "github".
    // +optional
    GitHubApp *GitHubAppAuth `json:"githubApp,omitempty"`

    // Token is required when platformType == "forgejo".
    // +optional
    Token *TokenAuth `json:"token,omitempty"`
}

type GitHubAppAuth struct {
    // AppID is the GitHub App ID.
    AppID int64 `json:"appID"`

    // PrivateKeyRef references a Secret with the App's private key.
    PrivateKeyRef SecretKeyReference `json:"privateKeyRef"`

    // InstallationID scopes auth to a single installation. Required.
    // If the App is installed on multiple orgs, declare one
    // RenovatePlatform per installation.
    InstallationID int64 `json:"installationID"`
}

type TokenAuth struct {
    // SecretRef references a Secret containing the platform token.
    SecretRef SecretKeyReference `json:"secretRef"`
}
```

CEL validation on the spec:

```yaml
x-kubernetes-validations:
  - rule: "self.platformType != 'github' || has(self.auth.githubApp)"
    message: "platformType=github requires auth.githubApp"
  - rule: "self.platformType != 'forgejo' || has(self.auth.token)"
    message: "platformType=forgejo requires auth.token"
  - rule: "!(has(self.auth.githubApp) && has(self.auth.token))"
    message: "exactly one auth method must be set"
```

Adding GitLab/Bitbucket/ADO in v0.3.0 is additive: extend the `PlatformType` enum, add a new field on `PlatformAuth`, add a CEL rule. No CRD version bump required.

### Auth flow: pass-through to Renovate

For both GitHub App and Forgejo Token, the operator passes credentials to Renovate via env vars and lets Renovate handle the auth flow:

- **GitHub App**: mount the private key file via Secret volume; set `RENOVATE_GITHUB_APP_ID` and `RENOVATE_GITHUB_APP_KEY_FILE`. Renovate handles JWT minting and installation token exchange internally.
- **Forgejo**: set `RENOVATE_TOKEN` from a `valueFrom.secretKeyRef`, set `RENOVATE_PLATFORM=forgejo`, set `RENOVATE_ENDPOINT` from `spec.baseURL`.

The operator does **not** mint installation access tokens itself in v0.1.0. We may revisit this if we need to scope tokens narrowly per-shard (a v1.x consideration when one shard fails the org-wide rate limit), or if Renovate's internal handling proves insufficient.

### Discovery uses platform APIs directly, not Renovate

The operator's discovery phase calls platform APIs directly (GitHub REST/GraphQL, Forgejo REST) to enumerate repos and apply the `requireConfig` filter. This is faster than running Renovate's own autodiscover + dry-run, gives us cleaner error reporting, and lets `requireConfig` be a real, testable feature rather than a Renovate-internal flag.

For discovery, the operator **does** mint short-lived installation access tokens for GitHub App auth. This is unavoidable: Renovate would do it for us, but we're calling the API ourselves. The token exchange is implemented in `internal/platform/github/auth.go` using `golang.org/x/oauth2` + `github.com/bradleyfalzon/ghinstallation/v2` or equivalent.

## Consequences

### Positive

- **One CRD, one mental model.** Users see `kubectl get renovateplatforms` and get all platforms in one list, regardless of type.
- **Adding new platforms is additive to one CRD**, not a new CRD. No changes for existing GitOps consumers.
- **CEL validation catches misconfigurations at admit time** without admission webhooks.
- **Renovate handles the GitHub App JWT flow**, so we get its battle-tested implementation for free in worker pods.
- **Credential rotation is per-platform**, not per-scan. One Secret update propagates to every Scan and every future Run referencing that Platform.

### Negative

- **The CRD schema has optional auth fields** that are conditionally required by `platformType`. CEL handles this, but `kubectl explain` doesn't surface the conditionality clearly. Mitigated by clear field comments in the Go types.
- **Two auth flows in the controller code** (App-based for GitHub discovery, plain token for Forgejo discovery). The operator's `internal/platform/` package abstracts this behind an interface, but it's still two implementations to maintain.
- **The operator carries GitHub App auth code** for its own discovery calls, even though Renovate workers handle their own auth. Some duplication; acceptable.

### Neutral

- We commit to the discriminated-union pattern. Future platform additions follow the same shape.

## Alternatives Considered

### A. Separate CRDs per platform type

`RenovateGitHubPlatform`, `RenovateForgejoPlatform`, etc. Each with a tightly-typed schema, no discriminated union. Cleaner per-type validation; clear `kubectl explain` output. **Rejected** because:

- `RenovateScan.spec.platformRef` would need to be a `corev1.ObjectReference` with a `kind` field, complicating validation and discovery.
- 5+ platform types means 5+ CRDs to install, version, document.
- The auth shape difference between platforms is small; the per-type CRDs would mostly differ only in their `auth` field's type. Most fields (BaseURL, RunnerConfig, PresetRepoRef, RenovateImage) are identical.

### B. Flat union of auth fields

```go
type PlatformAuth struct {
    GitHubAppID         *int64              `json:"githubAppID,omitempty"`
    GitHubAppKeyRef     *SecretKeyReference `json:"githubAppKeyRef,omitempty"`
    GitHubInstallationID *int64             `json:"githubInstallationID,omitempty"`
    Token               *SecretKeyReference `json:"token,omitempty"`
    // ... future platforms keep adding fields here
}
```

**Rejected** because the field count grows unboundedly with platform count, and there's no obvious grouping in `kubectl explain` of "these fields are for GitHub App, those for token-based platforms." The discriminated-union shape (`auth.githubApp.{appID,...}`) groups related fields naturally.

### C. Operator mints all tokens, even for workers

The operator generates GitHub App JWTs, exchanges for installation access tokens (with a short TTL — typically 1 hour), and injects them into worker pods. **Rejected** for v0.1.0:

- Renovate's own GitHub App handling is mature and battle-tested.
- Token TTL is shorter than long-running Renovate workers might need on a 30k-repo shard; we'd need rotation logic mid-run.
- We'd need to handle the security implications of operator-minted tokens in worker pod env vars (visible to anyone with `pods/exec`).

Letting Renovate do its own auth, with the operator only minting tokens for its own discovery API calls, is simpler and safer.

### D. PAT-only support, defer GitHub App to a later release

What v0.0.x originally proposed. **Rejected** per RFC-0001 update — the homelab uses GitHub App because PAT-based auth has scaling limits and is being increasingly discouraged by GitHub. Building GitHub App support into v0.1.0 saves a future migration.

## References

- [Renovate platform docs (GitHub App)](https://docs.renovatebot.com/getting-started/running/#github-app)
- [Renovate Forgejo support](https://docs.renovatebot.com/modules/platform/forgejo/)
- [GitHub App authentication](https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/about-authentication-with-a-github-app)
- [`ghinstallation/v2`](https://github.com/bradleyfalzon/ghinstallation) — Go library for App auth
- [Kubernetes API conventions — discriminated unions](https://github.com/kubernetes/community/blob/master/contributors/devel/sig-architecture/api-conventions.md#unions)
- [CEL validation for CRDs](https://kubernetes.io/docs/tasks/extend-kubernetes/custom-resources/custom-resource-definitions/#validation-rules)
- [ADR-0003: Multi-CRD architecture](0003-multi-crd-architecture.md)
