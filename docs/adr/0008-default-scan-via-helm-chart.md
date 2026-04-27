---
id: ADR-0008
title: "Ship a default RenovateScan via the Helm chart"
status: Proposed
author: donaldgifford
created: 2026-04-26
---
<!-- markdownlint-disable-file MD025 MD041 -->

# 0008. Ship a default RenovateScan via the Helm chart

<!--toc:start-->
- [Status](#status)
- [Context](#context)
- [Decision](#decision)
  - [Ship a RenovateScan named default in the chart](#ship-a-renovatescan-named-default-in-the-chart)
  - [Default values](#default-values)
  - [Critical default: requireConfig: true](#critical-default-requireconfig-true)
  - [Helm resource policy: helm.sh/resource-policy: keep](#helm-resource-policy-helmshresource-policy-keep)
  - [Migration path: opting out](#migration-path-opting-out)
- [Consequences](#consequences)
  - [Positive](#positive)
  - [Negative](#negative)
  - [Neutral](#neutral)
- [Alternatives Considered](#alternatives-considered)
  - [A. No default Scan; users always create one](#a-no-default-scan-users-always-create-one)
  - [B. Implicit default Scan, synthesized by the controller from Platform spec](#b-implicit-default-scan-synthesized-by-the-controller-from-platform-spec)
  - [C. Default Scan as a separate optional sub-chart](#c-default-scan-as-a-separate-optional-sub-chart)
  - [D. Default requireConfig: false](#d-default-requireconfig-false)
  - [E. Default Scan in a separate "samples" chart](#e-default-scan-in-a-separate-samples-chart)
- [References](#references)
<!--toc:end-->

## Status

Proposed

## Context

[RFC-0001](../rfc/0001-build-kubebuilder-renovate-operator.md) requires v0.1.0 to provide a zero-config experience: install the chart, point a `RenovatePlatform` at GitHub or Forgejo, and any repo with a Renovate config in its default branch starts getting weekly PRs without further user action.

The mechanism for this is a `RenovateScan` shipped inside the Helm chart, named `default`, enabled by default in `values.yaml`. This is the [`StorageClass`](https://kubernetes.io/docs/concepts/storage/storage-classes/) / [`IngressClass`](https://kubernetes.io/docs/concepts/services-networking/ingress-controllers/) pattern: a sane out-of-the-box resource that users can opt out of, edit, or replace.

Two important questions:

1. What goes in the default Scan?
2. How do we surface the knobs in `values.yaml` without dumping the entire CRD schema into chart values?

## Decision

### Ship a `RenovateScan` named `default` in the chart

Templated in `dist/chart/templates/default-scan.yaml`:

```yaml
{{- if .Values.defaultScan.enabled }}
apiVersion: renovate.fartlab.dev/v1alpha1
kind: RenovateScan
metadata:
  name: {{ .Values.defaultScan.name | default "default" }}
  namespace: {{ .Release.Namespace }}
  labels:
    {{- include "renovate-operator.labels" . | nindent 4 }}
    renovate.fartlab.dev/default-scan: "true"
  annotations:
    helm.sh/resource-policy: keep
spec:
  platformRef:
    name: {{ .Values.defaultScan.platformRef.name | required ".Values.defaultScan.platformRef.name is required when defaultScan.enabled" }}
  schedule: {{ .Values.defaultScan.schedule | quote }}
  timeZone: {{ .Values.defaultScan.timeZone | quote }}
  suspend: {{ .Values.defaultScan.suspend }}
  concurrencyPolicy: {{ .Values.defaultScan.concurrencyPolicy }}
  parallelism:
    minPods: {{ .Values.defaultScan.parallelism.minPods }}
    maxPods: {{ .Values.defaultScan.parallelism.maxPods }}
    reposPerPod: {{ .Values.defaultScan.parallelism.reposPerPod }}
  discovery:
    autodiscover: {{ .Values.defaultScan.discovery.autodiscover }}
    requireConfig: {{ .Values.defaultScan.discovery.requireConfig }}
    {{- with .Values.defaultScan.discovery.filter }}
    filter:
      {{- toYaml . | nindent 6 }}
    {{- end }}
    {{- with .Values.defaultScan.discovery.topics }}
    topics:
      {{- toYaml . | nindent 6 }}
    {{- end }}
  {{- with .Values.defaultScan.renovateConfig }}
  renovateConfig:
    {{- toYaml . | nindent 4 }}
  {{- end }}
  {{- with .Values.defaultScan.resources }}
  resources:
    {{- toYaml . | nindent 4 }}
  {{- end }}
{{- end }}
```

### Default values

```yaml
defaultScan:
  # Whether to ship a default RenovateScan with the chart.
  # Set to false if you manage Scans entirely via your own GitOps repo.
  enabled: true

  # Name of the default Scan resource.
  name: default

  # Required when enabled: name of the RenovatePlatform this default Scan targets.
  # Must be set explicitly; we do not default this because there is no sensible default.
  platformRef:
    name: ""

  # Cron expression. Default: weekly on Monday at 02:00.
  schedule: "0 2 * * 1"
  timeZone: "UTC"

  # Whether the Scan is paused.
  suspend: false

  # Concurrency policy if the previous run is still active.
  concurrencyPolicy: Forbid

  # Parallelism bounds for the Run.
  # See ADR-0005 for how N is computed.
  parallelism:
    minPods: 1
    maxPods: 10
    reposPerPod: 50

  discovery:
    # Use Renovate's autodiscovery to find repos.
    autodiscover: true

    # Only adopt repos that already have a Renovate config in their default branch.
    # See ADR-0008 — this is a critical default that prevents spam PRs against
    # unprepared repos.
    requireConfig: true

    # Optional autodiscover filter passed to Renovate.
    # Examples: ["org/*"], ["!archived/*"]
    filter: []

    # Optional GitHub topic filter (GitHub-only).
    topics: []

  # Optional Renovate config overrides for this Scan, layered on top of the
  # Platform's runnerConfig.
  renovateConfig: {}

  # Resources for the worker pods.
  resources:
    requests:
      cpu: 500m
      memory: 1Gi
    limits:
      cpu: 2000m
      memory: 4Gi
```

### Critical default: `requireConfig: true`

The single most important default. Without it, autodiscovery against an org-wide GitHub App would adopt every repo in the org — including archived repos, abandoned repos, repos owned by teams who never asked for Renovate. Each adopted repo gets a "Configure Renovate" onboarding PR, which is at best annoying and at worst a small organizational incident.

With `requireConfig: true`, the discovery phase only includes repos that have one of:

- `renovate.json` in the default branch
- `renovate.json5` in the default branch
- `.renovaterc` in the default branch
- `.renovaterc.json` in the default branch
- `.github/renovate.json` in the default branch
- `.github/renovate.json5` in the default branch
- A `renovate` key in `package.json` in the default branch

This makes Renovate adoption opt-in per-repo: a team adopts Renovate by adding a config file to their repo, no operator changes required. This is also exactly how Mend Renovate's hosted SaaS approaches the problem.

The flag is on `RenovateScan.spec.discovery.requireConfig` (per-Scan), not just a chart value. Power users with their own custom Scans can flip it off if they want.

### Helm resource policy: `helm.sh/resource-policy: keep`

The default Scan annotation prevents `helm uninstall` from deleting it. Rationale: deleting the Scan deletes its owned `RenovateRun` history and any in-flight worker Job. If a user is uninstalling, they probably want to inspect what was running first. The Scan persists; user can `kubectl delete renovatescan/default` explicitly if they want it gone.

### Migration path: opting out

Users with their own GitOps-managed Scans set `defaultScan.enabled=false` in their `values.yaml`. The chart no longer renders the default Scan template. Existing default Scan (if previously installed) persists due to the `keep` annotation; manual cleanup is documented.

## Consequences

### Positive

- **Zero-config homelab experience.** `helm install`, create one Platform, opt into Renovate by adding `renovate.json` to a repo. No CRD authoring required.
- **`requireConfig: true` prevents spam-PR incidents** at the homelab and enterprise scales alike.
- **The chart is the obvious surface for "I want to tweak the default."** Users with mild customization needs (different cron, different parallelism) edit `values.yaml` rather than learning the CRD.
- **GitOps-friendly via the opt-out flag.** Teams that manage Scans in their own repo set `enabled=false` and ignore the default.
- **The `default-scan` label** lets the operator (or a future tool) detect whether a default Scan is present; useful for "is the chart configured correctly" health checks.

### Negative

- **`values.yaml` mirrors a subset of the `RenovateScan` CRD schema.** When the CRD evolves, the chart values surface needs to follow. This is a maintenance burden that grows over time. We mitigate by deliberately exposing only the most-tweaked fields in `values.yaml`; rare/advanced fields require users to disable the default and create their own Scan.
- **Two ways to do something** (Helm value vs `kubectl edit renovatescan/default`) can confuse users. Documented prominently: "if you `kubectl edit`, your changes survive Helm upgrades because `helm.sh/resource-policy: keep`, but they will be overwritten if you `helm install --force` or pass `--reset-values`."
- **`platformRef.name` has no default.** Users will hit a chart-render error on first install if they don't set it. This is intentional — there is no sensible default — but the error message must be helpful (`required` Helm function provides this).

### Neutral

- We commit to keeping the chart values schema in lockstep with the CRD schema. CI checks that `values.yaml` keys map to valid CRD fields (a static check, not running Helm).

## Alternatives Considered

### A. No default Scan; users always create one

Conservative. Rejected because it makes the homelab onboarding story significantly worse: install chart → look up CRD schema → write your own Scan. The "five-minute homelab install" story is part of the project's appeal.

### B. Implicit default Scan, synthesized by the controller from Platform spec

The controller detects a Platform with no referencing Scan and synthesizes one in memory (no actual `RenovateScan` resource exists). **Rejected** because:

- "I see this Run was triggered, but there's no Scan that explains it" is a debugging nightmare.
- It makes the Platform CRD schema bigger (default-Scan fields creep onto Platform).
- It violates the single-CRD-per-concern principle in [ADR-0003](0003-multi-crd-architecture.md).

### C. Default Scan as a separate optional sub-chart

A `renovate-operator-defaults` sub-chart enabled via `dependencies` in `Chart.yaml`. **Rejected** because it adds a layer for what's a single resource; conditional rendering inside the main chart is simpler.

### D. Default `requireConfig: false`

Equivalent to "every repo in the org gets onboarded automatically." **Strongly rejected** — see the "critical default" rationale above. This default would generate one onboarding PR per repo, every cycle, against repos that didn't ask for it.

### E. Default Scan in a separate "samples" chart

Some operators ship example resources in a `samples/` directory the user is meant to apply manually. **Rejected** because the goal is *automatic* zero-config behavior, not "look at this example and adapt it."

## References

- [Helm `helm.sh/resource-policy`](https://helm.sh/docs/howto/charts_tips_and_tricks/#tell-helm-not-to-uninstall-a-resource)
- [Renovate `requireConfig` option](https://docs.renovatebot.com/configuration-options/#requireconfig)
- [Renovate config file locations](https://docs.renovatebot.com/configuration-options/)
- [Kubernetes `IngressClass` default pattern](https://kubernetes.io/docs/concepts/services-networking/ingress/#default-ingress-class)
- [RFC-0001](../rfc/0001-build-kubebuilder-renovate-operator.md)
- [ADR-0002: Adopt the kubebuilder Helm chart plugin](0002-adopt-kubebuilder-helm-chart-plugin.md)
- [ADR-0003: Multi-CRD architecture](0003-multi-crd-architecture.md)
