---
id: INV-0005
title: "Discovery bool fields silently default back to true on Run-snapshot copy"
status: Open
author: Donald Gifford
created: 2026-05-02
---
<!-- markdownlint-disable-file MD025 MD041 -->

# INV 0005: Discovery bool fields silently default back to true on Run-snapshot copy

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
  - [Observation 1 — live Scan vs snapshotted Run disagree](#observation-1--live-scan-vs-snapshotted-run-disagree)
  - [Observation 2 — bool + omitempty + default=true is the trap](#observation-2--bool--omitempty--defaulttrue-is-the-trap)
- [Conclusion](#conclusion)
- [Recommendation](#recommendation)
- [References](#references)
<!--toc:end-->

## Question

A `RenovateScan` with `discovery.requireConfig: false` set in its spec
results in a `RenovateRun` whose `scanSnapshot.discovery.requireConfig`
is `true`. The user-set `false` is silently flipped on the snapshot
copy, producing the wrong runtime behavior (every Run probes for
`renovate.json` even when the operator was told not to). What's
flipping the value, and how do we make a user-set `false` actually
stick?

## Hypothesis

The four boolean fields on `DiscoverySpec` (`Autodiscover`,
`RequireConfig`, `SkipForks`, `SkipArchived`) all share the same shape
in `api/v1alpha1/renovatescan_types.go`:

```go
// +kubebuilder:default=true
// +optional
RequireConfig bool `json:"requireConfig,omitempty"`
```

Combined: a non-pointer `bool` + `json:",omitempty"` + a kubebuilder
default of `true` produces a silent flip whenever the value is
serialized:

1. Controller has the in-memory Scan with `RequireConfig=false`.
2. `r.Create(ctx, run)` marshals `run.Spec.ScanSnapshot.Discovery` to
   JSON. `false` is the zero value for `bool`, so `omitempty` drops
   the field entirely.
3. The API server receives `{...}` (no `requireConfig` key) and
   applies the CRD default → `true`.
4. The Run controller reads back the Run; `RequireConfig` is now
   `true`.

The fix is to switch all four fields to `*bool`. `nil` is then the
zero value (preserved through `omitempty`), `false` and `true` are
distinguishable on the wire, and the kubebuilder default still
applies when the field is genuinely unset by the user.

## Context

Surfaced 2026-05-02 during the Phase 9 homelab acceptance run, while
validating INV-0004's discovery fix. After INV-0004 made `Apps.ListRepos`
return the 4 grant'd repos, every Run still failed with `DiscoveryFailed:
no repositories matched discovery filter` — because all 4 repos were
fresh (no `renovate.json`) and `requireConfig=true` filtered them all
out. The user had set `requireConfig: false` on the Scan to bypass that
probe for testing, and verified live: `kubectl get rscan ... -o
jsonpath='{.spec.discovery.requireConfig}'` returned `false`. But the
just-fired Run's snapshot showed `requireConfig: true`. Different value,
same Scan, ~2 seconds apart in time.

**Triggered by:** Setting any of the four boolean discovery fields to
`false` on a RenovateScan, regardless of whether the Scan is managed by
argo or applied directly. The bug is server-side (kube-API defaulting),
not argo-side. Argo simply re-pushes the user's intent and is innocent.

## Approach

1. **Inspect the live Scan vs the latest Run snapshot** to confirm the
   flip happens on snapshot copy, not somewhere upstream.
2. **Re-read the four field declarations** in
   `api/v1alpha1/renovatescan_types.go` and confirm all share the
   `bool + omitempty + default=true` shape.
3. **Trace the snapshot copy path**:
   `internal/controller/renovatescan_controller.go` → `createRun` →
   `run.Spec.ScanSnapshot = *scan.Spec.DeepCopy()` → `r.Create(ctx, run)`.
   `DeepCopy` preserves `false`. The flip happens on the
   serialize-and-API-server-default round-trip.
4. **Switch all four fields to `*bool`**. Add accessor helpers that
   return the effective value with the documented default applied for
   `nil`.
5. **Update consumers** in `internal/controller/renovaterun_controller.go`
   and `internal/jobspec/env.go`.
6. **Regenerate manifests + deepcopy** (`make manifests`, `make generate`).
7. **Update unit tests** that build `DiscoverySpec` literals; replace
   `Autodiscover: true` with `Autodiscover: ptr.To(true)` etc.
8. **Verify on homelab**: a Scan with `requireConfig: false` in source
   produces a Run whose snapshot also reads `requireConfig: false`,
   and the requireConfig probe is correctly skipped.

## Environment

| Component | Version / Value |
|-----------|----------------|
| Kubernetes | homelab cluster, current |
| Operator | `dev-ci` of branch `docs/inv-0001-next-run-column` (post-INV-0004) |
| Affected file | `api/v1alpha1/renovatescan_types.go:131-160` |
| Surface | RenovateScan ↦ RenovateRun snapshot copy via `r.Create(ctx, run)` |

## Findings

### Observation 1 — live Scan vs snapshotted Run disagree

```text
$ kubectl -n renovate get rscan test-gh -o yaml | yq '.spec.discovery.requireConfig'
false

$ kubectl -n renovate get rrun test-gh-smxn5 -o yaml | yq '.spec.scanSnapshot.discovery.requireConfig'
true
```

Same Scan, fresh Run (~2s old), different value. The live Scan has the
user's `false`; the Run snapshot has the kubebuilder default `true`.

The Scan controller's deep-copy preserves `false` in memory. The flip
happens on `r.Create(ctx, run)` — go-client marshals the spec, omits
the false-valued `requireConfig` key (because `omitempty`), and the
API server's CRD-default mechanism fills in `true` for the apparently-
absent field.

### Observation 2 — bool + omitempty + default=true is the trap

```go
// api/v1alpha1/renovatescan_types.go:131-160
type DiscoverySpec struct {
    // +kubebuilder:default=true
    // +optional
    Autodiscover bool `json:"autodiscover,omitempty"`

    // +kubebuilder:default=true
    // +optional
    RequireConfig bool `json:"requireConfig,omitempty"`

    // +optional
    Filter []string `json:"filter,omitempty"`

    // +optional
    Topics []string `json:"topics,omitempty"`

    // +kubebuilder:default=true
    // +optional
    SkipForks bool `json:"skipForks,omitempty"`

    // +kubebuilder:default=true
    // +optional
    SkipArchived bool `json:"skipArchived,omitempty"`
}
```

Four fields in this struct exhibit the bug. Slices (`Filter`, `Topics`)
are immune because `nil` and `[]string{}` are distinguishable from
`[]string{"x"}` even with `omitempty`. Booleans aren't.

This is a documented kube-API-go gotcha: `bool` + `omitempty` makes
`false` indistinguishable from "field not set" on the wire. With CRD
defaulting flipping unset → true, every user-set `false` collapses
into the default on the next serialize round-trip.

## Conclusion

**Answer:** The four boolean fields' `bool + omitempty +
default=true` declaration causes user-set `false` values to be erased
on the JSON marshal step inside `r.Create(ctx, run)`, then re-defaulted
to `true` by the API server. The fix is to use `*bool` so `nil` is the
zero value and explicit `false` survives the round-trip.

## Recommendation

Implement on PR #11 alongside INV-0004's discovery fix.

1. **`api/v1alpha1/renovatescan_types.go`** — flip the four fields:
   ```go
   Autodiscover  *bool `json:"autodiscover,omitempty"`
   RequireConfig *bool `json:"requireConfig,omitempty"`
   SkipForks     *bool `json:"skipForks,omitempty"`
   SkipArchived  *bool `json:"skipArchived,omitempty"`
   ```
   The kubebuilder defaults still apply for genuinely-unset fields.
2. **Add accessor helpers** on `DiscoverySpec` so consumers don't
   have to nil-check inline:
   ```go
   func (d *DiscoverySpec) RequireConfigOrDefault() bool { … }
   ```
   Or use `ptr.Deref(d.RequireConfig, true)` from `k8s.io/utils/ptr`.
3. **Consumer updates**:
   - `internal/jobspec/env.go:89` — `if scan.Discovery.RequireConfig` →
     dereferenced check.
   - `internal/controller/renovaterun_controller.go:307-308, 316` —
     same for `SkipForks`, `SkipArchived`, `RequireConfig`.
4. **Test updates**: every literal that says `RequireConfig: true` /
   `Autodiscover: true` etc. switches to `ptr.To(true)`.
5. **Regenerate**: `make manifests` (CRD schema) + `make generate`
   (deepcopy).
6. **Verify on homelab**: deploy `:dev-ci`, observe that the next
   Run's `scanSnapshot.discovery.requireConfig` matches the live Scan
   (both `false`).

Bundle with INV-0004 / INV-0003 / PodSecurity / INV-0001 / INV-0002 for
v0.1.2.

## References

- [INV-0004](0004-github-discover-bypasses-app-installation-grant.md) — sibling discovery bug; this one was unmasked while validating that fix.
- `api/v1alpha1/renovatescan_types.go:131-160` — affected DiscoverySpec.
- `internal/controller/renovatescan_controller.go` — Scan controller's `createRun` path that triggers the marshal flip.
- [Kubernetes API conventions: optional fields](https://github.com/kubernetes/community/blob/master/contributors/devel/sig-architecture/api-conventions.md#optional-vs-required) — pointer-vs-value rationale.
- [k8s.io/utils/ptr](https://pkg.go.dev/k8s.io/utils/ptr) — `ptr.To` / `ptr.Deref` helpers.
