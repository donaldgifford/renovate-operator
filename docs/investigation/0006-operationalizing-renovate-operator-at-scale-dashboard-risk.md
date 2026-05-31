---
id: INV-0006
title: "Operationalizing Renovate-operator at scale: dashboard, risk classification, and aging queues"
status: Open
author: Donald Gifford
created: 2026-05-31
---
<!-- markdownlint-disable-file MD025 MD041 -->

# INV 0006: Operationalizing Renovate-operator at scale: dashboard, risk classification, and aging queues

**Status:** Open
**Author:** Donald Gifford
**Date:** 2026-05-31

<!--toc:start-->
- [Question](#question)
- [Hypothesis](#hypothesis)
- [Context](#context)
- [Approach](#approach)
- [Findings](#findings)
  - [Observation 1 — two distinct audiences, often conflated](#observation-1--two-distinct-audiences-often-conflated)
  - [Observation 2 — risk classification is already a Renovate problem](#observation-2--risk-classification-is-already-a-renovate-problem)
  - [Observation 3 — the per-repo Dep Dashboard does not aggregate](#observation-3--the-per-repo-dep-dashboard-does-not-aggregate)
  - [Observation 4 — the operator should emit, not display](#observation-4--the-operator-should-emit-not-display)
  - [Observation 5 — ownership join is already solved by repo-guardian](#observation-5--ownership-join-is-already-solved-by-repo-guardian)
  - [Observation 6 — emission surface options](#observation-6--emission-surface-options)
- [Conclusion](#conclusion)
- [Recommendation](#recommendation)
- [References](#references)
<!--toc:end-->

## Question

At 1K+ repos across multiple GitHub orgs, two operational questions become
acute:

1. **Risk classification** — how does the fleet decide *automatically*
   which updates are low-risk (auto-merge / open-now) vs. high-risk
   (queue for human review / dep-dashboard approval), without per-repo
   bespoke config?
2. **Customer-facing fleet visibility** — given that approach, how do
   *dev teams* (the owners of those 1K repos) see, for *their* repos,
   how long updates have sat, which PRs need review, which deps are
   vulnerable? This is distinct from platform-ops visibility ("is the
   operator healthy") — both audiences need different surfaces.

Renovate itself solves both per repo (`packageRules` for classification,
the in-repo Dep Dashboard issue for visibility). Neither composes to
fleet scale without additional plumbing — and the plumbing for the two
audiences is different.

## Hypothesis

**(1)** A useful answer does not require new operator-side machinery.
Renovate's existing `packageRules` + `dependencyDashboardApproval`
already cover risk classification cleanly when centralized in a shared
preset (`github>OWNER/renovate-config`). The operator already supports
this via `RenovatePlatform.spec.presetRepoRef`. The hard part is
onboarding discipline (every repo extends), not a missing feature.

**(2)** The operator's correct role is to **emit structured events**
about Run outcomes (PRs opened, vulns discovered, deps updated).
Display is a *consumer* concern, and a consumer already exists in the
adjacent tooling: `repo-guardian` enforces `catalog-info.yaml` on every
repo, and `backstage-api` knows how to map a repo → owner/team. A new
or extended consumer service can join "renovate Run outcomes" to "who
owns this repo" without the operator ever knowing about teams or
displaying anything. Crucially, the existing **platform-ops**
observability (Prometheus / Grafana / OTel / Loki) stays unchanged and
purpose-built for "is the operator healthy" — it should not be
contaminated with per-PR/per-repo cardinality.

## Context

The Phase 9 homelab loop is wrapping up. Two observations motivated
this investigation:

1. The `dependencyDashboardApproval: true` pattern scales reviewer
   load *per repo* — but doesn't compose: 1K repos = 1K Dep Dashboards,
   and there's no aggregation.
2. The operator already has correctly-scoped platform-ops observability
   (`internal/observability/metrics.go` + four Grafana dashboards in
   `contrib/grafana/dashboards/`). That surface is for the platform
   team. It is not, and should not become, the customer-facing surface
   for dev teams asking "what's the state of my repos?"

**Triggered by:** Phase 9 homelab acceptance loop, conversation
2026-05-31 around schedule semantics and global-team operationalization.
Not blocking v0.1.x; informs v0.2.x+ scope.

## Approach

This is a thinking spike. No code. Outcomes:

1. Establish the audience separation (platform ops vs. dev teams) so
   architectural decisions don't conflate them.
2. Catalog what Renovate already provides for risk classification —
   establish the baseline so we don't reinvent it.
3. Catalog what already exists in the adjacent tooling ecosystem
   (`repo-guardian`, `backstage-api`) so we don't reinvent that either.
4. Enumerate the *emission* options for the operator (status fields,
   K8s events, webhooks, NATS/Kafka, OTLP-to-a-second-pipeline) and
   pick a recommended shape.
5. Identify the join keys needed so any downstream consumer can
   correlate operator emissions to PRs, deps, and owners.

## Findings

### Observation 1 — two distinct audiences, often conflated

Two audiences want very different surfaces:

| Audience | Question they ask | Right surface |
|---|---|---|
| **Platform team** (owns the operator) | "Is the operator healthy? Are Runs succeeding? Is discovery degrading?" | Prometheus / Grafana / Loki / OTel (what exists today). Low cardinality — `scan`, `platform`, `result`. Never `repo` or `pr_number`. |
| **Dev teams** (own the repos Renovate runs on) | "How many PRs in my repos are waiting? Are any of my deps vulnerable? How long has this PR sat?" | A customer-facing UI fed by operator emissions joined to ownership data. Per-repo / per-PR / per-dep granularity. Definitively **not** Prometheus. |

This separation is load-bearing. Pushing customer-facing data into
Prometheus blows cardinality at 1K+ repos and pollutes the
platform-ops surface with data nobody on that team cares about.
Pushing platform-ops data into the customer UI buries dev teams in
operator internals they shouldn't need to see.

The previous draft of this investigation got this wrong by proposing
"Shape A: Grafana-only" as a customer-facing solution. That was a
category error; it's deleted.

### Observation 2 — risk classification is already a Renovate problem

Renovate's `packageRules` is the existing language. The fleet-level
pattern is a shared preset (`github>OWNER/renovate-config:base` etc.)
that every repo extends:

```jsonc
{
  "packageRules": [
    // Patch / pin / digest → automerge if CI passes.
    {
      "matchUpdateTypes": ["patch", "pin", "digest"],
      "automerge": true,
      "automergeType": "pr",
      "platformAutomerge": true
    },
    // Minor for dev deps → automerge.
    {
      "matchUpdateTypes": ["minor"],
      "matchDepTypes": ["devDependencies"],
      "automerge": true
    },
    // Major → never automerge, require dashboard approval.
    {
      "matchUpdateTypes": ["major"],
      "dependencyDashboardApproval": true,
      "labels": ["dependencies", "major"]
    },
    // Workflow files → human review (supply-chain risk).
    {
      "matchManagers": ["github-actions"],
      "automerge": false,
      "labels": ["dependencies", "ci"]
    },
    // Security advisories → bypass schedule.
    {
      "matchPackagePatterns": ["*"],
      "vulnerabilityAlerts": {
        "schedule": ["at any time"],
        "labels": ["security", "dependencies"]
      }
    }
  ]
}
```

This composes cleanly across the fleet **iff** every repo extends the
preset. The operator already supports it via `presetRepoRef` (auto-
prepends into `extends` on every Run; `internal/jobspec/env.go:176-179`).
The hard work is *onboarding* every repo to the preset — which is
exactly what `repo-guardian` already enforces for `catalog-info.yaml`.
A natural extension of `repo-guardian`'s policy set would be "every
repo's `renovate.json` must `extends` the org preset" — solving the
discipline gap with existing tooling.

Two gaps that remain at fleet scale, neither addressable by the operator
alone:

- **Per-team policy.** Platform team's "low risk" ≠ payments team's.
  Today handled by per-repo `extends` (`:base + :payments` vs `:base + :platform`).
  No native team-policy abstraction in Renovate.
- **Why-did-this-automerge audit trail.** `packageRules` resolution is
  opaque — output is just the PR's flags. To answer "why did this
  merge?" you'd need to capture the resolved config at Run time.

### Observation 3 — the per-repo Dep Dashboard does not aggregate

The Dep Dashboard is a single GitHub issue per repo. Useful per-repo;
opaque at fleet scale. The questions dev teams want answered cross
repos:

- "How many open security-labeled PRs in *my* repos are >14 days old?"
- "Which of *my* repos have non-empty Awaiting-Approval queues older
  than 30 days?"
- "What's the median time-to-merge for patch automerges in *my* repos?"

These join across N repos and need an ownership filter. None are
answerable from N independent markdown issues.

### Observation 4 — the operator should emit, not display

The architectural cut that falls out of (1)+(3):

- **Operator's job:** be a well-behaved *producer*. After every Run,
  emit a structured event listing: which repos were processed, which
  PRs were opened/updated/closed, which vulns were surfaced, which
  deps were updated, with timestamps and labels.
- **Consumer's job:** subscribe to those events, normalize/store them,
  join to ownership data, render a UI. The operator never knows about
  teams, ownership, or UIs. The consumer never knows about reconciler
  loops, K8s resources, or shard layouts.

This separation:
- Keeps the operator small and focused (its job is to *run Renovate
  reliably*, not to be a dependency-management portal).
- Lets the consumer evolve independently — start as an extension of
  `backstage-api`, graduate to a dedicated service if/when the surface
  outgrows that, all without changing the operator.
- Trivially supports multiple consumers (`backstage-api`, a Slack
  digest bot, a compliance audit log, etc.) all subscribing to the
  same event stream.

### Observation 5 — ownership join is already solved by repo-guardian

`repo-guardian` enforces that every repo carries a `catalog-info.yaml`
declaring its owner. `backstage-api` exposes lookups by component or
owner-group. The implications for this investigation:

- The "who owns this repo?" question is **already a solved problem**
  in this ecosystem. We don't need to invent a CODEOWNERS scraper or
  a YAML mapping in the operator namespace.
- The customer-facing consumer service can call `backstage-api` (or
  hit the Backstage catalog directly) to translate
  `repo → owner-group → user list` for filtering and display.
- The operator emits the *repo* identifier; the consumer joins to
  ownership. The operator stays ownership-agnostic.

Optionally, the consumer might not even be a new service. If
`backstage-api` already has a generic "events about entities" surface,
the operator's emissions could plug into it directly and the UI lives
in Backstage. That's a sequencing question (does
`backstage-api` already do this, or does it need extension?) — out of
scope for this investigation but worth checking before scoping the
consumer.

### Observation 6 — emission surface options

The operator needs an emission surface for the consumer to subscribe
to. Five plausible options:

| Option | Pros | Cons |
|---|---|---|
| **`RenovateRun.status` enrichment** — add `openedPRs`, `updatedPRs`, `vulnerabilities`, `dependenciesUpdated` to the existing status. Consumer watches the K8s API. | Persistent. Already-watched resource. K8s API is the natural integration point. No new endpoints to operate. | K8s API watch is the consumer's problem to scale across N runs. Status fields are bounded by etcd object size (~1.5 MiB per Run). |
| **K8s events on the Run** — emit a structured Event per Run terminal transition listing the same data. | Native Kubernetes pattern. Consumers familiar with `kubectl get events`. | Events are ephemeral (1h default retention). Consumer must be running continuously. Event size limited. |
| **Webhook (POST to configurable URL)** — emit on terminal transition. | Simple. Decoupled. Consumer can be anything that accepts HTTP. | Delivery is best-effort — need retry/DLQ on the operator side or a retry-tolerant consumer. Signature verification needed for security. |
| **Cloud-event to a stream (NATS / Kafka / Redis Stream)** — durable pub/sub. | Robust, scales to many consumers, durable, replay-friendly. | Requires the broker to be operational infrastructure. Heaviest dependency. |
| **OTLP to a second pipeline** — emit "outcome" events as OTel logs/spans to a dedicated endpoint separate from the ops pipeline. | Reuses OTel infrastructure if it already exists. Strongly-typed via OTel semantic conventions. | Conflates "monitoring" and "domain events" semantically. OTel consumers (e.g., Tempo, Grafana) aren't built for "give me all the open PRs in my repos." |

**Recommended combination:** status enrichment (option 1) **plus**
webhook (option 3) as opt-in.

- Status enrichment gives every consumer (including ad-hoc
  `kubectl`/`yq` scripts) a queryable surface with zero new infra.
- Webhook lets the canonical consumer (`backstage-api` or a new
  service) subscribe to the *event* shape and not have to poll the
  K8s API.
- Streams (NATS/Kafka) are an obvious graduation path if/when multiple
  long-lived consumers exist, but we should not require a broker just
  to ship v0.2.

## Conclusion

**Answer:**

**(1)** Risk classification is solved by centralizing `packageRules`
in a preset and using `repo-guardian` to enforce that every repo
extends it. No operator-side feature needed beyond what's already
shipped (`presetRepoRef`).

**(2)** Customer-facing fleet visibility is a real gap. The operator's
correct role is to emit structured Run-outcome events, *not* to
display anything. A consumer system (likely an extension of
`backstage-api`) joins those emissions to ownership data already
maintained by `repo-guardian` enforcement of `catalog-info.yaml`, and
exposes per-owner views. The platform-ops observability stack
(Prometheus / Grafana / OTel / Loki) is correctly scoped today and
stays unchanged — adding per-PR data there would blow cardinality
and conflate audiences.

## Recommendation

**v0.2.x operator scope** (this repo):

1. **`RenovateRun.status` enrichment**. Add typed fields:
   ```go
   OpenedPRs    []PRRef          // repo, number, url, label, automerge
   UpdatedPRs   []PRRef
   ClosedPRs    []PRRef          // superseded / no-longer-needed
   Vulnerabilities []VulnRef     // repo, advisory id, severity, dep
   DependenciesUpdated []DepRef  // repo, package, from, to, manager
   ```
   Populated by the Run reconciler from worker output (parse the
   already-structured JSON logs, or have the worker write a result
   file we read back). Bounded — a Run that opens 1000 PRs is itself
   a problem; we'd cap and surface the overflow as a condition.
2. **Optional webhook sink**. New `Platform.spec.eventSink` config:
   ```yaml
   eventSink:
     type: webhook
     url: https://backstage-api.internal/renovate/events
     secretRef: { name: webhook-signing-key, key: secret }
   ```
   Operator POSTs a signed JSON envelope per terminal Run. Best-effort
   delivery with bounded retries; failures surface as a Run condition.
   Don't block the reconcile loop on delivery.
3. **Don't** add per-PR / per-repo collectors to the Prometheus
   surface. The audience cut from Observation 1 is the load-bearing
   rationale; capture it explicitly in DESIGN-0001's observability
   section so the next contributor doesn't undo it.

**Consumer scope** (out of this repo, plausibly an extension of
`backstage-api`):

1. Receive operator webhooks. Normalize and store
   `(run_id, repo, owner_from_catalog_info, pr_number, label,
   opened_at, merged_at, ...)`.
2. Join `repo → owner` via existing `backstage-api` ownership lookup.
3. Periodically reconcile with platform APIs (`/pulls`, `/issues`,
   `/dependabot/alerts`) for state Renovate doesn't emit — e.g., a
   PR Renovate opened a week ago but a reviewer just merged manually
   without a subsequent Run. Reconciliation closes the loop.
4. Expose per-owner views. UI lives wherever Backstage / the
   consumer surfaces them.

**Onboarding** (operational, not code):

- Extend `repo-guardian`'s policy set to require `renovate.json`
  exists and extends the org preset. Bonus: have it warn on `extends`
  lists that don't include the base preset.

### Open questions to resolve before scoping

- **Does `backstage-api` already have an "events about entities"
  ingress?** If yes, the operator's webhook target IS `backstage-api`
  and the consumer work is "extend the schema." If no, the consumer
  is more substantial.
- **What's the right webhook payload format?** CloudEvents v1.0
  envelope around a typed payload would interoperate well with the
  rest of the ecosystem and leave a graduation path to a stream.
- **How does the operator know which webhook to call?**
  Per-Platform? Per-Scan? Cluster-wide via env? Per-Platform is the
  least surprising — different Platforms might belong to different
  orgs with different ownership systems.
- **Failure modes for webhook delivery.** Bounded retries + DLQ
  surfaced as a Run condition. Don't introduce an outbox table; if
  the receiver is down for hours, surface it loudly via the existing
  Ready condition and let the consumer's reconcile-with-platform-API
  loop catch up the missed events.
- **Backward compatibility for existing manual users.** Status
  enrichment is additive (no breaking change). Webhook is opt-in
  (no-op when not configured). Both should be invisible to current
  v0.1.x users on upgrade.

## References

- DESIGN-0001 § "Future architecture: state DB" — *now reframed*: that
  section anticipated operator-side state for scheduler reasons; this
  investigation argues the *visibility* state lives in the consumer,
  not the operator. Both can coexist.
- ADR-0008 — `defaultScan` chart-shipped default; relevant for "every
  repo extends the preset" onboarding alongside `repo-guardian`.
- `internal/observability/metrics.go` — current Prometheus collectors;
  this investigation argues they're correctly scoped and should not
  grow per-repo cardinality.
- `contrib/grafana/dashboards/` — existing four dashboards; *not*
  the right place for customer-facing fleet view.
- `repo-guardian` (external tooling) — enforces `catalog-info.yaml`
  per repo; ownership-truth-of-record.
- `backstage-api` (external tooling) — owner/component lookup;
  candidate consumer for operator emissions.
- Renovate docs: `packageRules`, `dependencyDashboardApproval`,
  `vulnerabilityAlerts`, `prBodyTemplate`.
- PR #18 — surfaced the operationalization gap during the doc work
  for the two-`requireConfig` collision and the schedule-vs-no-schedule
  conversation that led to this spike.
