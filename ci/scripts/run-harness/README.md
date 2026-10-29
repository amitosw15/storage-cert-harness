# Harness smoke (local cluster)

Live smoke runs the harness image against a real cluster using
`plans/harness-smoke-plan.yaml`. Configuration is **tracked** under
[`ci/config/smoke/`](../../config/smoke/README.md) and staged into gitignored
`work/` before a run.

## Prerequisites (your responsibility)

| Requirement | Notes |
| --- | --- |
| **Kubeconfig** | Place a readable file at `work/kubeconfig` (or pass another path via `KUBECONFIG=`). The repo does **not** ship cluster credentials. Your user must have RBAC to run the smoke plan and to create/update the coordination lease in `storage-cert-ci`. |
| **Podman** | Local image smoke uses Podman (`make image-build-local` writes `work/local-image-ref`). |
| **Cluster access** | Backend arrays in staged `work/backends.local.yaml` must match storage classes / snapshot classes available on the cluster. |
| **Tools** | `yq`, `kubectl` or `oc` (lease script), and a built harness image for local smoke. |

Tracked smoke config must not be edited casually — see
[ci/config/smoke/README.md](../../config/smoke/README.md).

## Quick start (first time or CI-style)

From the repo root:

```bash
make image-build-local
cp /path/to/your/kubeconfig work/kubeconfig
make smoke-ci
```

`smoke-ci` runs **clean → prepare → smoke**:

1. **`clean-smoke-env`** — harness work/report dirs, smoke run logs, staged config copies, stale `work/smoke-run.lock` (not `kubeconfig` or `local-image-ref`).
2. **`prepare-smoke-env`** — copies `ci/config/smoke/*` and repo `catalog.json` into `work/`.
3. **`run-harness-smoke`** — validates cluster/backend, acquires locks, runs the plan in Podman.

To prepare without cleaning (e.g. refresh config only):

```bash
make prepare-smoke-env
make run-harness-smoke
```

Override kubeconfig:

```bash
make run-harness-smoke KUBECONFIG=/path/to/kubeconfig
```

Published image smoke (Quay) uses `make run-quay-image-tests`; it still expects
`work/kubeconfig` and appropriate backends/thresholds staging for your pipeline.

## Concurrency: three layers

Smoke uses separate mechanisms so local runs do not stomp each other or shared lab clusters.

### 1. Local smoke-run lock (`work/smoke-run.lock`)

- Acquired at the start of `run-image-tests.sh` via non-blocking `flock`.
- Only one harness smoke **script** run per repo checkout at a time.
- Exit code **3** if another run holds the lock (no long wait).

### 2. Local image lock (`work/local-image.lock`)

- Held only from local image resolution through **Podman container start** (short window).
- Prevents two runs from racing on the same local image tag/tarball without blocking the full harness execution.

### 3. Cluster coordination lease

- Resource: `storage-cert-ci/storage-cert-harness-smoke` (template in `ci/config/smoke/`).
- Acquired in `smoke-cluster-lease.sh` before the main harness work; released on exit.
- Holder ID: `GITHUB_RUN_ID`, `CI_PIPELINE_ID`, `HARNESS_SMOKE_LEASE_RUN_ID`, or `local-<user>-<timestamp>`.
- Exit code **2** if another CI job or operator holds the lease.
- Stale lease (crashed runner): delete with cluster admin tools, e.g.  
  `oc delete lease storage-cert-harness-smoke -n storage-cert-ci`

Logging: preflight output goes to `logs/run-image-tests-temp.log` until both the smoke-run lock and cluster lease succeed; then the log is archived and tee’d to `logs/run-image-tests.log`.

## Make targets

| Target | Description |
| --- | --- |
| `prepare-smoke-env` | Stage smoke bundle + `catalog.json` into `work/` |
| `clean-smoke-env` | Remove smoke artifacts, logs, staged configs |
| `smoke-env` | `clean-smoke-env` + `prepare-smoke-env` |
| `smoke-ci` | `smoke-env` + `run-harness-smoke` |
| `run-harness-smoke` | Live smoke with local Podman image |
| `run-quay-image-tests` | Smoke using published image (`VERSION=`) |
| `replay-smoke` | Offline example (no cluster) |

Root `Makefile` delegates to this directory’s `Makefile`.

## Scripts

| Script | Role |
| --- | --- |
| `prepare-smoke-env.sh` | Copy tracked smoke config into `work/` |
| `clean-smoke-env.sh` | Hygiene for repeat runs / CI |
| `run-image-tests.sh` | Local Podman smoke entry |
| `smoke-cluster-lease.sh` | Acquire/renew/release cluster lease |
| `live-cluster-smoke.sh` | CI wrapper around image smoke + lease |
| `run-quay-image-tests.sh` | Quay image path |
| `replay-smoke.sh` | Offline replay |

Self-test for lease logic: `ci/scripts/run-harness/test-smoke-cluster-lease.sh` (if present in your branch).
