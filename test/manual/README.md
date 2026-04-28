# Manual / homelab acceptance runs

The automated e2e suite under `test/e2e/` covers the operator-internal
contract (CRDs install, controller pod runs, manager starts). The full
end-to-end behaviour against real Git platforms — GitHub.com and a
homelab Forgejo — is exercised manually here as Phase 9 acceptance per
[IMPL-0001](../../docs/impl/0001-renovate-operator-v010-implementation.md).

## Prerequisites

- A kind cluster or homelab kubernetes cluster reachable via kubectl.
- The operator image published to a registry the cluster can pull
  (or loaded via `kind load docker-image renovate-operator:dev`).
- Operator helm release installed in `renovate-system` namespace:
  ```bash
  helm upgrade --install renovate-operator dist/chart \
    --namespace renovate-system \
    --create-namespace \
    --set image.repository=ghcr.io/donaldgifford/renovate-operator \
    --set image.tag=dev \
    --set defaultScan.enabled=false \
    --wait
  ```

## Scenario A — GitHub.com against `donaldgifford/server-price-tracker`

This scenario validates that the operator can authenticate as a GitHub
App, discover repositories, and run the Renovate CLI against them.

### A.1. Create the App credential Secret

```bash
kubectl -n renovate-system create secret generic gh-app-creds \
  --from-file=private-key.pem=$HOME/.ssh/renovate-app.pem
```

### A.2. Apply the Platform

```yaml
# /tmp/spt-platform.yaml
apiVersion: renovate.fartlab.dev/v1alpha1
kind: RenovatePlatform
metadata:
  name: github
spec:
  platformType: github
  renovateImage: ghcr.io/renovatebot/renovate:latest
  auth:
    githubApp:
      appId: <APP_ID>
      installationId: <INSTALLATION_ID>
      privateKeyRef:
        name: gh-app-creds
```

```bash
kubectl apply -f /tmp/spt-platform.yaml
kubectl wait --for=condition=Ready renovateplatform/github --timeout=60s
```

### A.3. Apply a Scan filtered to `donaldgifford/server-price-tracker`

```yaml
# /tmp/spt-scan.yaml
apiVersion: renovate.fartlab.dev/v1alpha1
kind: RenovateScan
metadata:
  name: spt
  namespace: default
spec:
  platformRef:
    name: github
  schedule: "*/5 * * * *"  # every 5 minutes for the manual run
  timeZone: UTC
  workers:
    minWorkers: 1
    maxWorkers: 1
    reposPerWorker: 50
  discovery:
    autodiscover: false
    requireConfig: true
    filter:
      - donaldgifford/server-price-tracker
```

```bash
kubectl apply -f /tmp/spt-scan.yaml
```

### A.4. Watch the Run materialize

```bash
kubectl -n default get rscan -w
kubectl -n default get rrun -w
kubectl -n default logs -l job-name -f --tail=200
```

### A.5. Acceptance checks

- [ ] `RenovateScan` `Scheduled` condition becomes `True`, `lastSuccessfulRunTime` populates.
- [ ] A `RenovateRun` reaches `phase: Succeeded`.
- [ ] At least one PR is opened on `github.com/donaldgifford/server-price-tracker`
      (or a `Skipping branch creation` log line confirms there's nothing
      eligible to update — both are valid for acceptance).
- [ ] The Grafana operator dashboard's "Reconcile rate" panel shows
      non-zero traffic on the `renovaterun` and `renovatescan` controllers.
- [ ] `renovate_operator_runs_total{result="succeeded"}` increments in Prometheus.

## Scenario B — Forgejo against the homelab instance

This scenario validates token-auth + the Forgejo SDK path (gitea SDK
under the hood, since they're API-compatible).

### B.1. Create the token Secret

```bash
kubectl -n renovate-system create secret generic forgejo-token \
  --from-literal=token=<FORGEJO_PAT>
```

### B.2. Apply the Platform

```yaml
# /tmp/forgejo-platform.yaml
apiVersion: renovate.fartlab.dev/v1alpha1
kind: RenovatePlatform
metadata:
  name: forgejo
spec:
  platformType: forgejo
  baseURL: https://forgejo.fartlab.dev
  renovateImage: ghcr.io/renovatebot/renovate:latest
  auth:
    token:
      secretRef:
        name: forgejo-token
```

```bash
kubectl apply -f /tmp/forgejo-platform.yaml
kubectl wait --for=condition=Ready renovateplatform/forgejo --timeout=60s
```

### B.3. Apply a Scan

```yaml
# /tmp/forgejo-scan.yaml
apiVersion: renovate.fartlab.dev/v1alpha1
kind: RenovateScan
metadata:
  name: forgejo-nightly
  namespace: default
spec:
  platformRef:
    name: forgejo
  schedule: "*/5 * * * *"
  timeZone: UTC
  workers:
    minWorkers: 1
    maxWorkers: 2
    reposPerWorker: 50
  discovery:
    autodiscover: true
    requireConfig: false
```

```bash
kubectl apply -f /tmp/forgejo-scan.yaml
```

### B.4. Acceptance checks

Mirror Scenario A's A.4–A.5 with the Forgejo Platform in scope:

- [ ] `RenovatePlatform/forgejo` reaches `Ready=True`.
- [ ] A `RenovateRun` reaches `phase: Succeeded` with discovered repos > 0.
- [ ] Worker logs contain `RENOVATE_PLATFORM=gitea` (per IMPL-0001 Q9 —
      Renovate's CLI knows Forgejo as `gitea`).
- [ ] At least one PR is opened on the homelab Forgejo or a "no
      updates" log line confirms the run executed against real repos.

## Cleanup

```bash
kubectl delete rscan -A --all
kubectl delete rrun -A --all
kubectl delete renovateplatform --all
kubectl delete secret -n renovate-system gh-app-creds forgejo-token --ignore-not-found
helm uninstall renovate-operator -n renovate-system
```

## Troubleshooting

| Symptom | Likely cause |
| --- | --- |
| Platform stuck `Ready=False/SecretNotFound` | Secret in wrong namespace — must be `renovate-system` |
| Platform `Ready=False/AuthFailed` | PEM parse failure (use `openssl rsa -in key.pem -check`) |
| Run stuck `Discovering` indefinitely | Check operator logs for rate-limit messages; the App may have hit its 4500/hr budget |
| Run `Failed` with `no repositories matched discovery filter` | `discovery.filter` glob didn't hit any repos — try `kubectl logs -l job-name=...` for the underlying API response |
| Worker Job `BackoffLimitExceeded` | Renovate CLI itself errored — `kubectl logs -l job-name=<name>-workers --all-containers` |
