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
  - [Observation 1 — what "low risk" already means in Renovate today](#observation-1--what-low-risk-already-means-in-renovate-today)
  - [Observation 2 — Renovate's per-repo Dep Dashboard does not aggregate](#observation-2--renovates-per-repo-dep-dashboard-does-not-aggregate)
  - [Observation 3 — three operationalization shapes, with tradeoffs](#observation-3--three-operationalization-shapes-with-tradeoffs)
  - [Observation 4 — the operator already emits the right primitives](#observation-4--the-operator-already-emits-the-right-primitives)
  - [Observation 5 — what's missing for fleet-wide PR/dep aging](#observation-5--whats-missing-for-fleet-wide-prdep-aging)
- [Conclusion](#conclusion)
- [Recommendation](#recommendation)
- [References](#references)
<!--toc:end-->

## Question

At 1K+ repos across multiple GitHub orgs, two operational questions become
acute:

1. **Risk classification** — how does the fleet decide *automatically* which
   updates are low-risk (auto-merge / open-now) vs. high-risk (queue for
   human review / dependency-dashboard approval), without per-repo
   bespoke config?
2. **Fleet visibility** — given that approach, how do operators *see* what
   is happening across the fleet? Specifically: how long has a non-auto-
   merge PR been open? How long has a vulnerable dep been waiting? Which
   repos are blocked on review? Which teams have the worst aging?

Renovate itself solves both problems *per repo* (`packageRules` for
classification, the in-repo `Dependency Dashboard` issue for visibility).
Neither composes to fleet scale without additional plumbing.

## Hypothesis

A useful answer to (1) does **not** require new operator-side machinery.
Renovate's existing `packageRules` + `dependencyDashboardApproval` already
cover risk classification cleanly when centralized in a shared preset
(`github>OWNER/renovate-config`). The operator's job is to make sure every
Run picks up that preset, not to re-implement risk logic on top.

A useful answer to (2) **does** require new machinery — but the cheapest
viable shape is a *read model*, not a database-of-record. The operator
already emits structured terminal events (Prometheus metrics, K8s events
on Runs, structured logs). Plus the platform APIs (`/pulls`, `/issues`,
`/dependabot/alerts`) already carry per-PR/per-dep state. A fleet view
is a join + aggregation over those, not a new write path. Three
plausible shapes (Grafana-only / Backstage plugin / dedicated service)
have wildly different cost profiles; we should pick before building.

## Context

The Phase 9 homelab loop is wrapping up with 5 repos under one org. Two
related observations from that loop motivated this investigation:

1. The `dependencyDashboardApproval: true` pattern (discussed during the
   PR #18 doc work) genuinely scales reviewer load — instead of N PRs
   shouting at reviewers, reviewers pull from one queue per repo. But
   "one queue per repo" still doesn't compose: 1K repos = 1K dashboards.
   Each one is a GitHub issue; there's no cross-repo aggregation.
2. The operator already has good per-Run observability surface
   (`internal/observability/metrics.go`, the four Grafana dashboards in
   `contrib/grafana/dashboards/`) but it stops at the Run boundary.
   "Did the Run succeed?" is well-instrumented; "did the PR get merged
   within 14 days?" is not even partially answered.

**Triggered by:** Phase 9 homelab acceptance loop, conversation
2026-05-31 around schedule semantics and global-team operationalization.
Not blocking v0.1.x; informs v0.2.x+ scope.

## Approach

This is a thinking spike. No code. Outcomes:

1. Catalog what Renovate already offers for risk classification and
   per-repo visibility — establish the baseline.
2. Catalog what the operator already emits — establish what we don't
   need to add.
3. Walk through three operationalization shapes (Grafana-only,
   Backstage plugin, dedicated service-with-database) for fleet view.
   For each: what it answers, what infra it needs, what it doesn't
   handle, and rough order-of-magnitude cost.
4. List the open questions that need answers *before* committing to a
   shape — what aggregation queries, what retention, what auth model.

## Findings

### Observation 1 — what "low risk" already means in Renovate today

Renovate's `packageRules` is the existing language for risk classification.
The fleet-level pattern is to put rules in a shared preset
(`github>OWNER/renovate-config:base` etc.) and have every repo extend it.
The typical "automerge-low-risk" stanza looks like:

```jsonc
{
  "packageRules": [
    // Patch updates everywhere → automerge if CI passes.
    {
      "matchUpdateTypes": ["patch", "pin", "digest"],
      "automerge": true,
      "automergeType": "pr",
      "platformAutomerge": true
    },
    // Minor updates for dev-only deps → automerge.
    {
      "matchUpdateTypes": ["minor"],
      "matchDepTypes": ["devDependencies"],
      "automerge": true
    },
    // Major updates → never automerge, always group, always approval.
    {
      "matchUpdateTypes": ["major"],
      "dependencyDashboardApproval": true,
      "labels": ["dependencies", "major"]
    },
    // Workflow files → human review (compliance / supply-chain risk).
    {
      "matchManagers": ["github-actions"],
      "automerge": false,
      "labels": ["dependencies", "ci"]
    },
    // Security advisories → bypass schedule and require approval explicitly.
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
preset. The operator already supports that via
`RenovatePlatform.spec.presetRepoRef` (auto-prepends into `extends` on
every Run; `internal/jobspec/env.go:176-179`). So the risk-classification
problem is largely a discipline + onboarding problem (every repo must
extend), not a missing-feature problem.

Two real gaps that show up at fleet scale:

- **Per-team policy.** A platform team's "low risk" differs from a
  payments team's. Today this is handled by per-repo `extends` lists
  (`:base + :payments` vs `:base + :platform`). Workable but requires
  per-repo discipline. Renovate has no native team-policy abstraction.
- **Risk classification is opaque to humans.** The output of
  `packageRules` resolution is just the PR's automerge flag + labels.
  There's no "why did this PR auto-merge?" audit trail outside the
  config that resolved at evaluation time.

### Observation 2 — Renovate's per-repo Dep Dashboard does not aggregate

The Dependency Dashboard is a single GitHub issue per repo. It lists
pending updates by category (Awaiting Schedule, Pending Approval, Open,
Rate-Limited, etc.). Tick a checkbox → that update jumps the queue on
the next Run.

**At fleet scale, this is the visibility hole.** Operators want to ask
questions like:

- "How many *open* security-labeled PRs across the org are >14 days old?"
- "Which repos have non-empty Awaiting-Approval queues older than 30 days?"
- "What's the median time-to-merge for patch automerges?"
- "Which teams have the worst PR aging?"

None of those are answerable from the Dep Dashboard issues — they live
in N issues across N repos, and the per-repo issue body is markdown
designed for humans, not for parsing. Anyone running Renovate at this
scale ends up writing a scraper or wiring an aggregator (Backstage,
custom service, etc).

### Observation 3 — three operationalization shapes, with tradeoffs

**Shape A: Grafana-only (cheapest)**

Push every signal we can already get into Prometheus / Loki / Tempo and
build dashboards. New collectors needed on the operator side:

- `renovate_pr_open_age_seconds{scan, platform, repo, label}` — gauge,
  one sample per Run, set from the platform `/pulls?state=open` query.
- `renovate_pr_open_total{scan, platform, label}` — counter, set the
  same way.
- `renovate_vuln_alert_open_seconds{scan, platform, severity}` — gauge
  from `/dependabot/alerts`.

Then PromQL gives:
- "PRs >14 days old by label" → `renovate_pr_open_age_seconds{label="security"} > 14*24*3600`
- "Repos with stuck queues" → joins on `scan`/`repo` labels.

| Pros | Cons |
|---|---|
| Reuses existing observability stack (kube-prometheus, Grafana, Loki). | High-cardinality labels (`repo`) blow up Prometheus storage at 1K+ repos. Need recording rules + retention strategy. |
| Operator adds ~3 collectors + a per-Run scrape loop. ~1 week of work. | No drill-down beyond Prometheus aggregations — can't answer "show me the actual PRs that are old." Need to pivot to GitHub UI. |
| No new infra to operate. | No history beyond Prometheus retention (typically 30d). |
| Cardinality cost is real but bounded — drop `repo` label, use external (Loki?) for per-repo detail. | "Why has this PR sat?" requires going to the PR itself. Grafana shows *counts*, not *items*. |

**Best fit for:** ops teams that already live in Grafana and want
alerts/SLO-style thresholds across the fleet, accepting that drill-down
sends them to GitHub.

**Shape B: Backstage plugin (medium)**

If the org already runs Backstage as its developer portal, ship a
Renovate-operator plugin that:

1. Reads `RenovateRun` and `RenovateScan` resources via Kubernetes API
   to get the operator-side state (last Run, schedule, etc.).
2. Reads the platform APIs (GitHub `/pulls`, `/issues`, `/dependabot/alerts`,
   Forgejo equivalents) to get the per-PR/per-dep state.
3. Renders per-entity (Backstage Component) and fleet-wide views.

| Pros | Cons |
|---|---|
| Org-native — devs already use Backstage for service docs, links to dashboards, etc. | Requires Backstage. Hard sell to orgs that don't already run it. |
| Solves the "per-team aggregation" problem naturally via Backstage's entity model. | Plugin maintenance burden (Backstage releases monthly, plugin breakage common). |
| Auth/authz is reused from Backstage. | Doesn't help users who don't have Backstage accounts (oncall in PagerDuty, security team in their own tools). |
| Drill-down works — Backstage links from "this PR" back to the PR. | Cache layer needed — calling GitHub API on every page load won't scale to 1K repos. |

**Best fit for:** orgs that already standardized on Backstage. Cost is
"a plugin and a cache."

**Shape C: Dedicated service (database + API + frontend) (most)**

A new operator-adjacent service that:

1. Subscribes to the operator's terminal-event stream (K8s events or a
   webhook the operator could emit).
2. Polls the platform APIs on a schedule (GitHub, Forgejo) for PR/issue
   state.
3. Stores normalized rows in Postgres: `(repo, pr_number, opened_at,
   merged_at, label, automerge, last_seen_at, ...)`.
4. Exposes a query API (GraphQL or REST) for arbitrary slicing.
5. Renders a frontend purpose-built for "fleet renovate state."

| Pros | Cons |
|---|---|
| Decouples from Prometheus cardinality limits (data lives in Postgres). | New service to operate: deploy, secret, DB, auth, frontend, alerting. ~6-12 weeks build, then ongoing maintenance. |
| Arbitrary queries — "PRs labeled `security` >7 days old, in repos owned by team `payments`, by Renovate version." | Need an org/team data source — has to integrate with Backstage / OCTO repo / a YAML somewhere. |
| Time-series-style history (every snapshot of every PR's state), useful for trend analysis. | Webhook flow (real-time updates) is much better than polling, but adds a webhook receiver and signature verification. |
| Foundation for richer features later: SLO-style "X% of PRs merge within N days" enforcement, weekly digest emails. | If the org's small (<100 repos) this is laughably overbuilt. |
| Maps directly onto the DESIGN-0001 "Future architecture: state DB" section — operator-side scheduler + Postgres has the same primitives. | Hardest to walk back if direction is wrong. |

**Best fit for:** real platform-engineering teams at 1K+ repos with a
budget for a small dedicated service and an existing org/team metadata
source to join against.

**The escalation path** is the interesting observation: Shape A → B → C
is a natural progression. Start with A (low risk, cheap, validates that
the data is even worth slicing). When A's drill-down friction gets
painful, evaluate B vs C based on whether Backstage exists. Don't
start with C unless the org explicitly already wants a write-path
(operator-side state DB, the DESIGN-0001 "Future architecture" path).

### Observation 4 — the operator already emits the right primitives

What's already there as of v0.1.3:

- **Prometheus collectors** (`internal/observability/metrics.go`):
  `renovate_operator_runs_total{scan, platform, result}`,
  `renovate_operator_discovery_errors_total`,
  `renovate_operator_run_duration_seconds`,
  `renovate_operator_active_runs`, etc.
- **Structured JSON logs** with `trace_id`/`scan`/`platform` (Loki-ready
  via the contrib/alloy/operator.river pipeline).
- **OTLP tracing** (no-op fallback) on hot paths.
- **K8s resource state** — every Run is queryable via the K8s API with
  full `status.conditions`, `status.discoveredRepos`, etc. That's
  fleet-visible-by-default *for the operator side of the boundary*.

What's **not** there yet:

- Anything about the PR or dep state *after* Renovate-the-worker emits
  it. The operator stops at "the Job succeeded"; it doesn't know if the
  PR Renovate opened got merged 2 hours later or sat for 90 days.
- Anything about the deps themselves (vuln severities, age of available
  update). That data lives in `/dependabot/alerts` and the package
  registries.

### Observation 5 — what's missing for fleet-wide PR/dep aging

The data needed for "PR aging" / "dep aging" queries is split between:

1. **Operator** — knows which Scans/Platforms exist, what was last run,
   what discovered. Owns the *scheduling* truth.
2. **Renovate workers** — emit per-PR/per-dep updates as side effects
   (open PR, update dependency-dashboard, raise issue). No structured
   emission back; the PR is the artifact.
3. **Platform (GitHub/Forgejo)** — the system of record for PRs, issues,
   vuln alerts, branch protection rules.

To answer "how long has this security PR been open?" we need
**(3) joined to (1)** — the platform tells us the PR state, the operator
tells us "this PR was created by this Scan against this Platform."
Today the join key is implicit (the PR's `body` mentions Renovate, the
branch starts with `renovate/`). For fleet-scale reliability we'd want
either:

- An explicit operator-emitted event "Run R opened PRs [a, b, c]" so
  external consumers know which PRs to track without scraping branch
  names, OR
- A platform-side label or PR-body marker that includes the Scan/Run
  identifier so consumers can attribute back.

The first is operator-side work (~1 day to emit Events on the Run
resource); the second is config in `default.json` (add
`prBodyTemplate` / labels that include `renovate-operator-scan/<name>`).

## Conclusion

**Answer:**

For (1) — **risk classification at scale is a Renovate problem, not an
operator problem.** Centralize `packageRules` in
`github>OWNER/renovate-config`, enforce that every repo extends it
(operator already supports this via `presetRepoRef`). The hard part is
*onboarding* every repo to the preset, not the operator surface.

For (2) — **fleet visibility is a real gap and needs new machinery,
but probably not a new database yet.** The cheapest viable next step is
Shape A: a few extra operator-side metrics that scrape PR/dep state per
Run and expose it for Grafana. That validates whether the queries
operators actually want are even possible to express in PromQL. If they
hit cardinality walls (likely at 1K+ repos) or want drill-down (likely
within weeks), graduate to Shape B (Backstage) if the org has it, or
Shape C (dedicated service) if the org is big enough to justify it.

**Recommendation tree:**

- **Now (v0.2.0 candidate):** Emit operator-side `RenovateRun` events
  listing the PR numbers that resulted from each Run (or have the
  worker write back via a sidecar). Adds the missing join key for any
  downstream consumer. ~1-2 weeks of work; unblocks all three shapes.
- **Next (v0.2.x):** Ship Shape A — three or four new collectors that
  poll the platform API per Run for open-PR age, open-vuln age, queue
  depth. Document the recommended Grafana dashboard and PromQL
  starters. Get operator users to the "I can answer 80% of the
  questions in Grafana" point.
- **Later (v0.3.0+):** Re-evaluate. If Grafana isn't enough, the
  shape of the gap that emerged will pick B vs C for us.

## Recommendation

Do **not** start building Shape C right now. The DESIGN-0001 "Future
architecture: state DB" section already gestures at this; it explicitly
defers it. Use this investigation as the trigger to write the v0.2.x
plan for Shape A + the event-emission prerequisite, and update
DESIGN-0001 to point at this investigation for the rationale.

Concrete v0.2.x scope candidates that fall out of this:

1. **`RenovateRun.status.openedPRs []corev1.ObjectReference`** — Run
   reconciler scrapes worker logs (already structured) for PR creation
   events and surfaces them on the Run's status. K8s events emitted
   alongside.
2. **`renovate_operator_pr_open_age_seconds{scan, platform, repo}`** +
   `renovate_operator_vuln_open_age_seconds{scan, platform, severity}`
   collectors, sampled per Run from the platform API. Includes a
   `--max-repos-scraped` knob so we don't accidentally hammer the
   platform API.
3. **Grafana dashboard:** `contrib/grafana/dashboards/fleet.json`
   showing PR-aging histograms, vuln-by-severity counts, queue depth
   per Scan.
4. **Doc**: a new `docs/usage/fleet-visibility.md` walking through the
   four canonical "is the fleet healthy?" questions and the PromQL
   that answers each.

Open questions to resolve before scoping:

- Do we want PR scraping done by the Run reconciler (one shot per Run)
  or by a separate controller (continuous)? Run-scoped is simpler and
  ties cleanly to the existing reconcile loop; continuous gives
  fresher data but adds a new control loop.
- What's the cardinality budget for the `repo` label in Prometheus?
  At 1K repos × 3 metrics × scrape-every-10-min = ~432K samples/day,
  which Prometheus handles but ServiceMonitor scrape duration matters.
  Recording rules + dropping `repo` for aggregate views may be required.
- Per-team aggregation needs an external mapping (which repo → which
  team). Do we read CODEOWNERS, Backstage catalog, a YAML in the
  operator namespace? Punt to v0.3.x unless an obvious answer emerges.

## References

- DESIGN-0001 § "Future architecture: state DB" — long-term direction
  that Shape C would graduate toward.
- ADR-0008 — `defaultScan` chart-shipped default; relevant for "every
  repo extends the preset" onboarding.
- `internal/observability/metrics.go` — current Prometheus collectors.
- `contrib/grafana/dashboards/` — existing four dashboards
  (operator, runs, traces, logs); fleet view would be a fifth.
- Renovate docs: `packageRules`, `dependencyDashboardApproval`,
  `vulnerabilityAlerts`, `prBodyTemplate`.
- PR #18 — surfaced the operationalization gap during the doc work for
  the two-`requireConfig` collision and the schedule-vs-no-schedule
  conversation that led to this spike.
