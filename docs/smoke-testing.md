# Live cluster smoke — prepare, validate, run, troubleshoot

How contributors run the **harness smoke plan** on a real OpenShift cluster from
this repository. This is development-scale smoke (**nine** catalog TRs at minimal
size), not full Level-3 certification.

## Related docs

| Topic | Document |
| --- | --- |
| CI policy, edge cluster, GitHub live smoke | [design/storage-cert-harness-ci-smoke-test.md](design/storage-cert-harness-ci-smoke-test.md) |
| Plan selection and lab overrides | [design/plan-based-test-suite-runbook.md](design/plan-based-test-suite-runbook.md) |
| Cluster prerequisites | [cluster-requirements.md](cluster-requirements.md), [cluster-setup-guide.md](cluster-setup-guide.md) |
| CI pipeline (not live smoke) | [ci.md](ci.md) |
| Script internals (short) | [ci/scripts/run-harness/README.md](../ci/scripts/run-harness/README.md) |

Do **not** commit kubeconfigs, passwords, or site-specific secrets. Use your own
lab cluster for pre-PR validation; the shared CI edge cluster is only for
upstream GitHub live smoke when operators enable it.

---

## What runs

| Item | Location |
| --- | --- |
| Smoke plan | `plans/harness-smoke-plan.yaml` (default backend name in plan: `oc-edge-112`) |
| Thresholds | `ci/config/smoke/thresholds-smoke.json` → `work/harness-run-thresholds.json` (+ mirror `work/thresholds-smoke.json`) |
| Backends | Staged from `ci/config/smoke/backends.local.yaml` → `work/backends.local.yaml` (preserved if you already have one) |
| Catalog | Repo `catalog.json` → `work/catalog.json` |

**Nine** TRs run from `plans/harness-smoke-plan.yaml`, including **TR-STOR-002**
(FIO storage IO via virtbench; smoke overrides use `fio_runtime: 50` and extended
`fio_collect_*` retries). **TR-VIRT-007** (HA node failure) and **TR-VIRT-008**
(node drain) stay excluded by design.

---

## Prerequisites

Install on the machine that runs smoke (your laptop or a jump host with cluster
network access):

| Requirement | Used for |
| --- | --- |
| **Podman** | Local harness container (`make image-build-local`) |
| **Go toolchain** | `make build` (image build embeds the binary) |
| **`oc`** (OpenShift CLI) | Preflight and cluster coordination lease |
| **`yq`**, **`jq`** | Plan/backend validation and lease metadata |
| **Readable kubeconfig** | Cluster API access with RBAC for smoke workloads and lease namespace `storage-cert-ci` |

One-time repo setup:

```sh
./ci/scripts/install-tools.sh   # optional but matches CI tool pins
pre-commit install              # if you contribute regularly
```

Place cluster credentials **only** under gitignored paths, typically:

```sh
mkdir -p work
cp /path/to/your/kubeconfig work/kubeconfig
chmod 600 work/kubeconfig
```

If your lab uses a private CA or self-signed API cert:

```sh
export KUBECTL_INSECURE_SKIP_TLS_VERIFY=true
```

---

## Idempotent vs destructive operations

**Idempotent** here means: safe to run again on the same checkout without a live
smoke run in progress; re-running converges `work/` staging toward the current
repo bundle (or leaves your intentional overrides unchanged).

| Operation | Touches cluster? | Idempotent? | What changes on repeat |
| --- | --- | --- | --- |
| **`make prepare-smoke-env`** | No | **Yes** (staging) | Always refreshes `work/harness-run-thresholds.json`, `work/thresholds-smoke.json`, and `work/catalog.json` from `ci/config/smoke/` + repo `catalog.json`. **Does not** overwrite an existing `work/backends.local.yaml` or `work/container-smoke.yaml` (first copy only). Never touches `work/kubeconfig` or `work/local-image-ref`. |
| **`make run-harness-smoke`** (alone) | **Yes** | **No** | Re-stages thresholds/catalog (same as prepare), then runs the full smoke plan on the cluster, acquires lease, and may delete prior `work/harness-smoke-*` dirs before the run (unless `HARNESS_SMOKE_KEEP_WORK=true`). |
| **`make clean-smoke-env`** | No | **No** (destructive) | Removes harness smoke work/report trees, smoke logs, and staged threshold/catalog copies under `work/`. Keeps kubeconfig, backends, local image ref. |
| **`make smoke-env`** | No | **No** | `clean-smoke-env` then `prepare-smoke-env` — net effect wipes then re-stages. |
| **`make smoke-ci`** | **Yes** | **No** | Same as `smoke-env` plus a cluster smoke run. Use for a clean CI-parity attempt, not for “refresh thresholds only”. |
| **`make build`** / **`make image-build-local`** | No | **Mostly** | Rebuilds binary/image; incremental when inputs unchanged. |
| **`make replay-smoke`** | No | **Yes** (offline) | Example catalog run in a temp dir; no `work/` smoke staging. |

**Practical rule:** After you edit **`ci/config/smoke/thresholds-smoke.json`** (or pull
changes to the smoke bundle), run **`make prepare-smoke-env`** only — it is the
lightweight, idempotent way to sync `work/` without cleaning reports or re-running
tests. You do **not** need `smoke-ci` just to pick up new thresholds.

**Concurrency:** `prepare-smoke-env`, `clean-smoke-env`, and live smoke all take
`work/smoke-run.lock`. If another smoke or prepare is running, the second command
exits with an error instead of interleaving — wait or finish the other process first.

---

## Recommended path: `make smoke-ci`

From the **repository root**, this is the same sequence CI contributors should
use locally: clean smoke artifacts, stage config, validate cluster, acquire locks,
run Podman smoke.

```sh
make build
make image-build-local
make smoke-ci KUBECONFIG=work/kubeconfig
```

Optional backend override (must match a `backends[].name` in staged YAML):

```sh
BACKEND=my-lab-backend make smoke-ci KUBECONFIG=/path/to/kubeconfig
```

**What `smoke-ci` does**

1. `clean-smoke-env.sh` — removes prior smoke work/report dirs, smoke logs, staged
   threshold/catalog copies; keeps `work/kubeconfig`, existing `work/backends.local.yaml`, and `work/local-image-ref`.
2. `prepare-smoke-env.sh` — copies `ci/config/smoke/*` and `catalog.json` into `work/`.
3. `run-image-tests.sh` — full preflight, cluster lease, harness `run` in Podman.

---

## Step-by-step (when you should not clean)

Use this when you only refreshed the image or kubeconfig, or you used
**`make prepare-smoke-env`** to idempotently refresh staged config, and you do
not want `clean-smoke-env` to delete prior reports:

```sh
make build
make image-build-local
make prepare-smoke-env          # safe to repeat; refreshes thresholds + catalog
make run-harness-smoke KUBECONFIG=work/kubeconfig
```

If you changed tracked files under `ci/config/smoke/`, **`make prepare-smoke-env`**
alone is enough. Use **`make smoke-env`** only when you also want clean to remove
old staged files and smoke logs before re-staging.

---

## Validate before smoke

The harness script runs these checks automatically; you can run them manually
first to fail fast:

```sh
export KUBECONFIG=work/kubeconfig
oc whoami --show-server
oc whoami
oc get nodes
oc get storageclass <storage-class-from-backend>
oc get volumesnapshotclass <snapshot-class-from-backend>
```

Backend storage/snapshot class names come from staged `work/backends.local.yaml`
for your `BACKEND`. Plan overrides may reference worker **node names** — those
nodes must exist (`oc get node <name>`).

Align plan overrides in `plans/harness-smoke-plan.yaml` (or a private plan copy)
with **your** workers if you are not on the shared lab topology. Do not commit
lab hostnames if policy forbids it; keep overrides in gitignored `work/` or local
branches.

---

## Other entry points

### Published Quay image

Smoke the **published** harness image instead of `work/local-image-ref`:

```sh
make prepare-smoke-env
cp /path/to/kubeconfig work/kubeconfig
make run-quay-image-tests VERSION=latest KUBECONFIG=work/kubeconfig
```

Uses `run-quay-image-tests.sh` (pull + run). Staging and backend rules are the
same as local smoke.

### Offline replay (no cluster)

Exercises the **example** catalog/plan only — not the live smoke plan:

```sh
make -f ci/scripts/run-harness/Makefile replay-smoke
# or: ./ci/scripts/run-harness/replay-smoke.sh
```

Root `make replay-smoke` is intentionally a no-op pointer; use the command above
or `make ci` for offline validate+run in the default pipeline.

### GitHub live cluster smoke (operators / on-demand)

Workflow [`.github/workflows/ci-live-smoke.yml`](../.github/workflows/ci-live-smoke.yml)
runs on self-hosted runners when `CI_LIVE_SMOKE_ENABLED=true`. It invokes
**trusted** `ci/scripts/run-harness/live-cluster-smoke.sh` from the default branch
with PR source in an artifact.

**Important:** `live-cluster-smoke.sh` currently acquires the cluster lease then
exits with code **5** — full harness execution from CI is not implemented yet
([ECOPROJECT-5406](https://redhat.atlassian.net/browse/ECOPROJECT-5406)). For
contributors, use **local** `make smoke-ci` until that work lands. On-demand CI
triggers are documented in [contributing.md](contributing.md).

---

## Environment variables

| Variable | Default | Purpose |
| --- | --- | --- |
| `KUBECONFIG` | `work/kubeconfig` (Make) | Cluster credentials path passed to `run-image-tests.sh` |
| `BACKEND` | Plan `backend:` field | Selects `backends[].name` in `work/backends.local.yaml` |
| `KUBECTL_INSECURE_SKIP_TLS_VERIFY` | `false` | Skip TLS verify for `oc` / harness when lab requires it |
| `HARNESS_SMOKE_KEEP_WORK` | `false` | If `true`, skip pre-run cleanup of `work/harness-smoke-*` |
| `HARNESS_SMOKE_LEASE_RUN_ID` | auto | Override lease holder id for local debugging |
| `VERSION` | `latest` | Quay tag for `run-quay-image-tests` |

---

## Scripts and Make targets

All live smoke scripts live under `ci/scripts/run-harness/`.

| Make target | Script | Cluster? | Idempotent (local `work/` staging) |
| --- | --- | --- | --- |
| `prepare-smoke-env` | `prepare-smoke-env.sh` | No | **Yes** — refreshes thresholds + catalog; preserves existing backends/kubeconfig/image ref |
| `clean-smoke-env` | `clean-smoke-env.sh` | No | **No** — deletes staged thresholds/catalog and smoke artifacts |
| `smoke-env` | clean + prepare | No | **No** — clean then stage |
| `smoke-ci` | smoke-env + `run-image-tests.sh` | **Yes** | **No** — full clean, stage, and cluster run |
| `run-harness-smoke` | `run-image-tests.sh` | **Yes** | **No** — re-stages then runs smoke on cluster |
| `run-quay-image-tests` | `run-quay-image-tests.sh` | **Yes** | **No** — re-stages then runs published image |
| `replay-smoke` | `replay-smoke.sh` | No | **Yes** (offline example run) |

Supporting pieces:

| Script | Role |
| --- | --- |
| `smoke-cluster-lease.sh` | Coordination Lease in `storage-cert-ci` (shared lab) |
| `live-cluster-smoke.sh` | CI wrapper (incomplete harness run) |
| `test-smoke-cluster-lease.sh` | Unit-style checks for lease helpers |

---

## Outputs and logs

| Path | Content |
| --- | --- |
| `work/harness-smoke-report/` | Harness report JSON/Markdown and tool artifacts |
| `work/harness-smoke-work/` | Run workdir (tool scratch space) |
| `logs/run-image-tests.log` | Current smoke transcript (after lock + lease acquired) |
| `logs/run-image-tests-temp.log` | Early preflight before log commit |

Inspect `work/harness-smoke-report/report.json` (and `report.md`) for per-TR
verdicts when smoke fails.

---

## Troubleshooting

### Exit code **3** — local smoke already running

Another process holds `work/smoke-run.lock` on this checkout.

- Find the other terminal or CI job using the same repo path.
- If a stale lock remains after a crash, remove `work/smoke-run.lock` only when
  no harness Podman container is still running.

### Exit code **2** — cluster smoke lease busy

Another job holds `storage-cert-ci/storage-cert-harness-smoke`. The log prints
holder annotations (run id, plan, etc.).

- Wait for the other smoke to finish, or coordinate with the team on a shared lab.
- **Cluster admins** may delete a stale lease after confirming the holder crashed:  
  `oc delete lease storage-cert-harness-smoke -n storage-cert-ci`

### Preflight: backend / storage class / snapshot class

Errors like `backend 'X' is not defined` or `storage class does not exist`:

- Run `make prepare-smoke-env` and confirm `work/backends.local.yaml`.
- Set `BACKEND=` to a backend name that exists in that file.
- Ensure `storage_class` and `snapshot_class` in the backend match objects on the cluster.

### Preflight: plan node does not exist

Edit node overrides in the smoke plan (or use workers that exist on your cluster).
The default plan may name workers for a specific lab.

### `work/local-image-ref is missing`

Run `make image-build-local` (after `make build`).

### `run: make prepare-smoke-env`

Staged thresholds or catalog missing under `work/`. Run **`make prepare-smoke-env`**
(idempotent) or start smoke — `run-harness-smoke` also re-stages thresholds before the run.

### `cluster is not reachable`

Check `KUBECONFIG`, VPN, firewall, and `oc whoami --show-server`.

### Harness test failures (virtbench / kube-burner)

Use report artifacts under `work/harness-smoke-report/`. **TR-STOR-002** is the
longest smoke TR (FIO + virtbench gather); if it fails, check virtbench/FIO logs
in the report and design doc §6. Historical gather fixes: [ECOPROJECT-5566](https://redhat.atlassian.net/browse/ECOPROJECT-5566).

### `live-cluster-smoke.sh` exits **5** in GitHub Actions

Expected until CI harness execution is implemented; use local `make smoke-ci` for
validation. Track progress under [ECOPROJECT-5406](https://redhat.atlassian.net/browse/ECOPROJECT-5406).

### Personal lab vs shared edge-112

- **Your lab:** custom kubeconfig and optionally gitignored `work/backends.local.yaml`
  (prepare preserves an existing file).
- **Shared CI lab:** only operators configure GitHub secrets; contributors must not
  paste CI kubeconfig into the repo.

---

## Editing smoke configuration

Tracked bundle: `ci/config/smoke/` — see
[ci/config/smoke/README.md](../ci/config/smoke/README.md). Changes need review;
they affect everyone’s staged smoke config.

Plan changes: `plans/harness-smoke-plan.yaml` (repository root).
