# Architecture Decision Records (ADRs)

This directory contains Architecture Decision Records documenting significant
technical decisions.

## What are ADRs?

ADRs document **technical implementation decisions** for specific architectural
components. Each ADR focuses on a single decision and includes:

- **Context**: The problem or constraint that led to this decision
- **Decision**: What was chosen and why
- **Consequences**: Trade-offs, pros, and cons
- **Alternatives**: Other options that were considered

## Creating a New ADR

```bash
docz create adr "Your ADR Title"
```

## ADR Status

- **Proposed**: Under discussion, not yet approved
- **Accepted**: Approved and being implemented or already implemented
- **Deprecated**: No longer relevant or superseded
- **Superseded by ADR-XXXX**: Replaced by another ADR

<!-- BEGIN DOCZ AUTO-GENERATED -->
## All ADRs

| ID | Title | Status | Date | Author | Link |
|----|-------|--------|------|--------|------|
| ADR-0001 | Use kubebuilder for operator scaffolding | Accepted | 2026-04-26 | donaldgifford | [0001-use-kubebuilder-for-operator-scaffolding.md](0001-use-kubebuilder-for-operator-scaffolding.md) |
| ADR-0002 | Adopt the kubebuilder Helm chart plugin | Accepted | 2026-04-26 | donaldgifford | [0002-adopt-kubebuilder-helm-chart-plugin.md](0002-adopt-kubebuilder-helm-chart-plugin.md) |
| ADR-0003 | Multi-CRD architecture (Platform, Scan, Run) | Accepted | 2026-04-26 | donaldgifford | [0003-multi-crd-architecture.md](0003-multi-crd-architecture.md) |
| ADR-0004 | Use metav1.Condition and Run child resources for status | Accepted | 2026-04-26 | donaldgifford | [0004-use-conditions-and-run-children-for-status.md](0004-use-conditions-and-run-children-for-status.md) |
| ADR-0005 | Use Indexed Jobs for parallel Run workers | Accepted | 2026-04-26 | donaldgifford | [0005-indexed-jobs-for-parallelism.md](0005-indexed-jobs-for-parallelism.md) |
| ADR-0006 | Multi-platform support (GitHub App and Forgejo) in v0.1.0 | Accepted | 2026-04-26 | donaldgifford | [0006-multi-platform-support.md](0006-multi-platform-support.md) |
| ADR-0007 | Observability stack: Prometheus, OTel, structured logging, pprof | Accepted | 2026-04-26 | donaldgifford | [0007-observability-stack.md](0007-observability-stack.md) |
| ADR-0008 | Ship a default RenovateScan via the Helm chart | Accepted | 2026-04-26 | donaldgifford | [0008-default-scan-via-helm-chart.md](0008-default-scan-via-helm-chart.md) |
<!-- END DOCZ AUTO-GENERATED -->
