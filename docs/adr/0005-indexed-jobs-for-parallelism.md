---
id: ADR-0005
title: "Use Indexed Jobs for parallel Run workers"
status: Proposed
author: donaldgifford
created: 2026-04-26
---
<!-- markdownlint-disable-file MD025 MD041 -->

# 0005. Use Indexed Jobs for parallel Run workers

<!--toc:start-->
- [Status](#status)
- [Context](#context)
- [Decision](#decision)
- [Consequences](#consequences)
  - [Positive](#positive)
  - [Negative](#negative)
  - [Neutral](#neutral)
- [Alternatives Considered](#alternatives-considered)
  - [A. JobSet (jobset.x-k8s.io)](#a-jobset-jobsetx-k8sio)
  - [B. N independent Jobs, controller-managed pool](#b-n-independent-jobs-controller-managed-pool)
  - [C. One Job per repo (the mogenius approach)](#c-one-job-per-repo-the-mogenius-approach)
  - [D. Custom worker pool (no Job at all)](#d-custom-worker-pool-no-job-at-all)
  - [E. Run inside a single pod with Renovate's own concurrency](#e-run-inside-a-single-pod-with-renovates-own-concurrency)
- [Implementation notes](#implementation-notes)
- [References](#references)
<!--toc:end-->

## Status

Proposed

## Context

[RFC-0001](../rfc/0001-build-kubebuilder-renovate-operator.md) commits us to scaling Renovate from 30 to 30,000 repos by sharding the discovered repo list across N worker pods within a single Run. This ADR records *how* those N workers are orchestrated.

The shape of the workload is:

1. **Discovery phase** — short-running, single pod (or in-process call from the controller). Outputs a list of repos to scan.
2. **Worker phase** — N pods running in parallel, each receiving an index `i ∈ [0, N)`. Each pod reads its slice of the repo list and runs Renovate against just those repos.

This is the shape that Kubernetes' batch API was extended to handle natively. Specifically, `batch/v1.Job` with `completionMode: Indexed` and `parallelism: N` gives each pod a `JOB_COMPLETION_INDEX` env var, and the Job is considered complete when all N indices report success. This was introduced as alpha in 1.21, beta in 1.22, GA in 1.24.

There are real alternatives. The decision matters because it constrains:

- How the controller computes and adjusts N.
- How worker pods receive their assignment (env var? mounted file? watching the Run resource?).
- Retry semantics on pod failure.
- Resource lifecycle (one Job to garbage-collect vs N).
- How `kubectl logs -l renovate.../run=foo` and Loki's job/pod selectors look from a debugging perspective.

## Decision

The `RenovateRun` controller orchestrates a two-phase lifecycle:

1. **Discovery** — the controller calls the platform API directly to enumerate repos matching the Scan's discovery filter, applies the `requireConfig` filter, and writes the resulting repo list to a ConfigMap owned by the Run.
2. **Workers** — the controller creates a single `batch/v1.Job` with:
   - `spec.completionMode: Indexed`
   - `spec.parallelism: N`
   - `spec.completions: N`
   - `spec.backoffLimit: 0` (per-pod)
   - `spec.template` mounting the discovery ConfigMap and using `JOB_COMPLETION_INDEX` to read its slice

Where N is computed from Scan policy and discovery output:

```
N = clamp(ceil(repoCount / scan.spec.parallelism.reposPerPod),
          scan.spec.parallelism.minPods,
          scan.spec.parallelism.maxPods)
```

Each worker pod is the same Renovate image used elsewhere, run with a small wrapper script (~30 lines of shell or a tiny Go program) that:

1. Reads the ConfigMap-mounted `repos.json`.
2. Parses `JOB_COMPLETION_INDEX` and computes its slice via stable round-robin: `myRepos = [repos[i] for i in range(JOB_COMPLETION_INDEX, len(repos), N)]`.
3. Sets `RENOVATE_REPOSITORIES=<csv>` and other env vars from the Platform/Scan-derived config.
4. Execs `renovate`.

Round-robin over a sorted repo list (rather than contiguous slicing) distributes slow repos across shards, mitigating the straggler risk where one shard gets several large repos in a row.

## Consequences

### Positive

- **Native parallelism semantics for free.** `parallelism`, `completions`, retry behavior, completion accounting, owner references — Kubernetes already implements all of this for Indexed Jobs. We don't write a custom pool manager.
- **One Job per Run.** Cascade delete is trivial. `kubectl describe renovaterun/<n>` → owned Job → owned pods. Standard mental model.
- **Worker assignment is deterministic and reproducible.** Given the same input repo list and the same N, the same `JOB_COMPLETION_INDEX` always handles the same repos. Useful for "rerun with the same shard layout to reproduce a failure" workflows.
- **Easy log selection.** `kubectl logs -l job-name=<run-job> --all-containers` or `{job_name="<run-job>"}` in Loki gives all worker output. Pod naming (`<job>-<index>-<hash>`) makes it easy to filter to a specific shard.
- **Workers are stateless.** The discovery ConfigMap is the contract; pods don't talk to each other or to the controller during execution.
- **Resource accounting is centralized.** The Job spec carries a single `template.spec.containers[0].resources` block. Whatever you give it is what each worker gets.

### Negative

- **N is fixed at Job creation time.** If discovery returns 5,000 repos and we set N=20, but the cluster suddenly has more headroom 10 minutes in, we cannot rescale up. We accept this for v0.1.0; mid-run rescaling is a v0.x research item and probably needs the "N independent Jobs" design instead.
- **Shard imbalance is real.** Round-robin distributes randomness in repo size, but a worker that draws the heaviest repos still finishes last. Parallel speedup is bounded by the slowest shard's wall-clock. Acceptable for v0.1.0; v1.x can add bin-packing by historical run time.
- **Worker wrapper code lives somewhere.** We either ship a thin sidecar image (`renovate-worker:<v>`), embed the wrapper in the operator's release artifacts and mount it via emptyDir + initContainer, or shell out from a small inline shell script. We pick the inline-script approach for v0.1.0 (simplest, no new image) and revisit if it gets unwieldy.
- **ConfigMap size limits.** A repo list at 30k entries × ~100 bytes each is ~3 MB; etcd's per-object limit is ~1.5 MB. v0.1.0 ships the single-ConfigMap pattern with gzip+base64 fallback above 900 KiB and a 30k-repo load test as the validation. At enterprise scale this still creaks (cross-Run state, rate budgeting, scheduler smarts all want shared storage); the planned answer is an operator-owned state DB documented in DESIGN-0001 (Future architecture: state DB), not a sharper ConfigMap layout.

### Neutral

- We commit to the Indexed Job programming model for workers. Switching later to "N independent Jobs" would be a rewrite of the Run controller's worker-management code. We accept this lock-in because the alternative is more code we'd be writing now to handle a problem (mid-run rescale) that may never come up.

## Alternatives Considered

### A. JobSet (`jobset.x-k8s.io`)

[JobSet](https://github.com/kubernetes-sigs/jobset) is a SIG-led abstraction over groups of Jobs. Built for ML/HPC workloads where you have heterogeneous job groups (e.g., "1 launcher + N workers + M parameter servers") that need to start together, succeed together, and have networking dependencies. Our workload doesn't have these dependencies — discovery is just a phase, not a parallel coordinated group. JobSet would also add a CRD dependency on the cluster (`jobset.x-k8s.io/v1alpha2`), which is friction for the homelab install. **Rejected** as more sophisticated than we need; revisit if discovery ever becomes a parallel phase itself.

### B. N independent Jobs, controller-managed pool

The controller spawns Jobs up to `maxPods`, watches completions, spawns more as slots free up. Closer to what mogenius does (one Job per repo, capped by parallelism). **Rejected** for v0.1.0 because:

- It re-invents what `parallelism` already gives us in Indexed Job.
- It complicates GC: now we have N Jobs to track per Run, not one.
- The dynamic-resize benefit it offers (mid-run scaling) we don't actually need at v0.1.0.

The one real upside — being able to retry a single failed shard without restarting the whole Job — we get partially via `completionMode: Indexed` already (failed pod indexes can be retried within `backoffLimitPerIndex` once that field stabilizes; at present we set `backoffLimit: 0` per-pod and accept full-Job restart semantics).

### C. One Job per repo (the mogenius approach)

Discovery yields N repos, controller spawns N Jobs throttled by `parallelism`. **Rejected** because:

- Pod startup overhead × N is significant (Renovate's image is ~600 MB; pulling and starting 30k pods is wasteful).
- Each pod runs Renovate's heavy initialization (npm install of managers, plugin loading, etc.) for a single repo, where Indexed Job amortizes init across many repos per pod.
- N Jobs to track per Run is administratively painful (`kubectl get jobs -l ...` floods).

This approach is what makes the mogenius operator fail to scale; we explicitly avoid it.

### D. Custom worker pool (no Job at all)

Controller creates a Deployment (or ReplicaSet) of long-running worker pods, dispatches work via a queue (in-cluster like NATS, or just a CRD-shaped queue). **Rejected** for v0.1.0 — adds significant infrastructure dependencies, is overkill for the schedule-driven nature of Renovate runs (Renovate runs aren't a streaming workload, they're batch), and breaks the "everything is observable as standard k8s objects" property that Indexed Job preserves.

### E. Run inside a single pod with Renovate's own concurrency

Renovate has internal concurrency knobs (per-repo, per-PR). **Rejected** because:

- These are within-process concurrency limits, not horizontal scaling. A single Renovate process is bounded by one CPU core for npm install and one process for managers.
- Doesn't address the wall-clock problem at all — 30k repos in one process is still 30k repos in one process.

This is what mogenius does today.

## Implementation notes

The shard-assignment logic, codified:

```python
def assign_shards(repos: list[str], n: int) -> list[list[str]]:
    sorted_repos = sorted(repos)  # stable across runs
    return [sorted_repos[i::n] for i in range(n)]
```

Worker pods invoke (conceptually):

```bash
#!/bin/sh
INDEX="${JOB_COMPLETION_INDEX}"
TOTAL="${JOB_PARALLELISM}"
REPOS=$(jq -r ".repos | .[range($INDEX; length; $TOTAL)] | @csv" /shard/repos.json)
export RENOVATE_REPOSITORIES="${REPOS}"
exec renovate
```

v0.1.0 ships the inline-shell variant; cutover to a small Go binary or sidecar image is a follow-up only if shell + jq proves fragile across Renovate image variants in practice.

## References

- [Indexed Job (Kubernetes batch/v1)](https://kubernetes.io/docs/concepts/workloads/controllers/job/#completion-mode)
- [`backoffLimitPerIndex` (alpha → beta)](https://kubernetes.io/docs/concepts/workloads/controllers/job/#backoff-limit-per-index)
- [JobSet API (kubernetes-sigs/jobset)](https://github.com/kubernetes-sigs/jobset)
- [RFC-0001](../rfc/0001-build-kubebuilder-renovate-operator.md)
- [ADR-0003: Multi-CRD architecture](0003-multi-crd-architecture.md)
- [ADR-0004: Use `metav1.Condition` and Run child resources for status](0004-use-conditions-and-run-children-for-status.md)
