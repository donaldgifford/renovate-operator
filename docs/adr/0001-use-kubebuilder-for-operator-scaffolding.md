---
id: ADR-0001
title: "Use kubebuilder for operator scaffolding"
status: Accepted
author: donaldgifford
created: 2026-04-26
---
<!-- markdownlint-disable-file MD025 MD041 -->

# 0001. Use kubebuilder for operator scaffolding

<!--toc:start-->
- [Status](#status)
- [Context](#context)
- [Decision](#decision)
- [Consequences](#consequences)
  - [Positive](#positive)
  - [Negative](#negative)
  - [Neutral](#neutral)
- [Alternatives Considered](#alternatives-considered)
  - [operator-sdk (Go mode)](#operator-sdk-go-mode)
  - [Raw controller-runtime + client-go](#raw-controller-runtime--client-go)
  - [kro](#kro)
  - [No scaffolding tool, copy from repo-guardian](#no-scaffolding-tool-copy-from-repo-guardian)
- [References](#references)
<!--toc:end-->

## Status

Accepted — realized in v0.1.0 scaffold (`kubebuilder init --domain fartlab.dev`, cliVersion 4.13.0).

## Context

[RFC-0001](../rfc/0001-build-kubebuilder-renovate-operator.md) commits us to building a new Kubernetes operator. We need a scaffolding tool that handles the boilerplate: API types, deepcopy generators, manifest generation, RBAC markers, controller skeleton, `cmd/main.go`, `Makefile` targets for build/test/deploy, and envtest plumbing.

The mature options today are:

1. **kubebuilder** — maintained under `kubernetes-sigs`, the de facto reference implementation of operator scaffolding. Plugin-based architecture lets us add Helm, Deploy Image, Grafana, etc.
2. **operator-sdk** — Red Hat-led, historically separate, but its Go-mode has been a thin wrapper over kubebuilder for several years. Adds Helm-mode and Ansible-mode for non-Go operators.
3. **Raw `controller-runtime` + `client-go`** — no scaffolding, hand-written everything.
4. **`kro`** (Kube Resource Orchestrator) — declarative composition, no controller-runtime code required.

We have direct prior-art experience with kubebuilder from [`repo-guardian`](https://github.com/donaldgifford/repo-guardian) and the in-flight Wiz operator. The 7-person platform team can read kubebuilder layouts at a glance because the convention is shared across projects.

## Decision

Use **kubebuilder v4** (latest stable plugin set: `go.kubebuilder.io/v4`) as the scaffolding tool for this project.

We will use kubebuilder's plugin system additively:

- `go.kubebuilder.io/v4` (default) — Go API types and controllers.
- `helm.kubebuilder.io/v1-alpha` — Helm chart generation (see [ADR-0002](0002-adopt-kubebuilder-helm-chart-plugin.md)).
- `grafana.kubebuilder.io/v1-alpha` — deferred; revisit when we add custom dashboards.
- `deploy-image.go.kubebuilder.io/v1-alpha` — not relevant; we are not bundling a single managed Deployment.

## Consequences

### Positive

- Project layout matches our other operators; onboarding is "you already know it."
- Generators (`make manifests`, `make generate`) keep CRDs, RBAC, and deepcopy code in sync from kubebuilder markers in the Go source.
- envtest integration is already wired in via `Makefile`; we get `make test` for controller integration tests on day one.
- Standard `cmd/main.go` ships with leader election, metrics server, health probes, and webhook server stubs — all of which we will need by Phase 3.
- Kubebuilder's `PROJECT` file gives us a declarative record of what the codebase contains, which mirrors what `docz` does for our docs.

### Negative

- Coupling to kubebuilder's release cadence and plugin maturity model. The Helm plugin is alpha (see ADR-0002 for that specific risk).
- Some kubebuilder defaults are opinionated in ways we'll override (e.g., the default `Makefile` is large and assumes a particular CI shape). We accept this and document overrides in the project's `CONTRIBUTING.md`.

### Neutral

- We commit to staying within roughly the kubebuilder-blessed project shape. Deviating heavily defeats the "anyone can navigate it" benefit; deviating modestly is fine and expected.

## Alternatives Considered

### operator-sdk (Go mode)

Operator-sdk Go-mode wraps kubebuilder. Picking it would add a layer of indirection (operator-sdk-specific commands and conventions on top of the same generators) without adding capability. The Helm-mode and Ansible-mode flavors are interesting but irrelevant since we're writing Go. **Rejected** for redundancy.

### Raw controller-runtime + client-go

What you write when you want to demonstrate that you understand operators. Not what you write when you want to ship one and maintain it. Hand-rolling deepcopy, manifest generation, RBAC markers, and the `main.go` wiring is throwaway work that kubebuilder solves identically across every operator that uses it. **Rejected** as wasted effort.

### kro

`kro` is the right tool when an "operator" is really a higher-order composition of existing primitives. Renovate-operator has stateful reconciliation logic — schedule computation, child resource lifecycle management, condition transitions — that doesn't fit kro's declarative ResourceGroup model cleanly. **Rejected** as a category mismatch. We may use kro elsewhere; not here.

### No scaffolding tool, copy from `repo-guardian`

A reasonable middle path: bootstrap by copying our existing kubebuilder layout. Rejected because we'd still be inheriting whichever kubebuilder version was current when `repo-guardian` was scaffolded, and because `kubebuilder init` is fast enough that the savings are marginal.

## References

- [kubebuilder book — quick start](https://book.kubebuilder.io/quick-start)
- [kubebuilder PROJECT file](https://book.kubebuilder.io/reference/project-config.html)
- [RFC-0001](../rfc/0001-build-kubebuilder-renovate-operator.md)
- [ADR-0002: Adopt the kubebuilder Helm chart plugin](0002-adopt-kubebuilder-helm-chart-plugin.md)
