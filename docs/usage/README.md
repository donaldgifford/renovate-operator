# Usage Guide

Operator-side usage documentation for `renovate-operator` v0.1.0+. Complements
[RFC-0001](../rfc/0001-build-kubebuilder-renovate-operator.md) /
[DESIGN-0001](../design/0001-renovate-operator-v0-1-0.md) (which describe the
_why_ and the design) and [`test/manual/README.md`](../../test/manual/README.md)
(which is a full end-to-end runbook for homelab acceptance).

## Contents

| Doc                                      | Use it when                                                                                                                                 |
| ---------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------- |
| [Installation](installation.md)          | Installing or upgrading the operator. Includes the full Helm values surface and a troubleshooting matrix for common install/runtime issues. |
| [Authorization](authorization.md)        | Granting credentials. GitHub App permissions to set, Forgejo token scopes, Forgejo version compatibility notes, and rotation behavior.      |
| [RenovatePlatform](renovate-platform.md) | Defining the GitHub or Forgejo platform the operator authenticates as. One Platform per credential; cluster-scoped.                         |
| [RenovateScan](renovate-scan.md)         | Defining a scheduled Renovate run against a Platform. Cron-like; namespaced; controls discovery, sharding, and per-Run config layering.     |
| [RenovateRun](renovate-run.md)           | Inspecting an active or terminal Run. Created automatically by Scans; not authored by hand.                                                 |

## Quick orientation

- **Three CRDs.** `RenovatePlatform` (cluster-scoped, holds creds),
  `RenovateScan` (namespaced, schedules Runs), `RenovateRun` (namespaced,
  ephemeral, one per fire-time, owned by its Scan).
- **One Platform per installation.** A GitHub App installed in three orgs needs
  three `RenovatePlatform` resources, each pointing at the same App but with a
  different `installationID`.
- **Scans schedule, Runs execute.** Each scheduled fire-time materializes a Run
  with a _frozen snapshot_ of the parent Platform + Scan spec. Editing the Scan
  mid-execution does not retroactively change in-flight Runs.
- **Sharding is automatic.** Each Run discovers eligible repos, then runs an
  `Indexed` `batch/v1.Job` with
  `actualWorkers = clamp(ceil(repos/reposPerWorker), min, max)`.
- **Conditions are the source of truth.** `Ready` on every CRD; `Scheduled` on
  Scan; `Started` / `Discovered` / `Succeeded` / `Failed` on Run. The Run's
  `phase` field is a derived cursor, useful for printer columns / quick filters.

## Where to start

- **First-time install on a homelab cluster:** [Installation](installation.md),
  then [RenovatePlatform](renovate-platform.md) for credential setup.
- **Wiring a new repo / org into an existing operator:** create or edit a
  [RenovateScan](renovate-scan.md). The operator picks it up on the next
  reconcile.
- **A Run failed and you want to know why:** [RenovateRun](renovate-run.md)
  walks the phase machine and lists the relevant kubectl incantations.
- **End-to-end manual acceptance against real Git platforms:**
  [`test/manual/README.md`](../../test/manual/README.md) — homelab runbook with
  credential bootstrap and dashboard import.
