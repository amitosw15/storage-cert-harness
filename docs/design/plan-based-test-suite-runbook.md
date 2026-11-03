# Run a Test Suite from a Plan

This is a development smoke run for a lab OpenShift cluster. It is not a
certification-scale execution. Use **private** runbooks for site-specific hostnames,
kubeconfig paths, and credentials.

Design and CI behavior: [storage-cert-harness-ci-smoke-test.md](storage-cert-harness-ci-smoke-test.md).
**How to run smoke locally:** [smoke-testing.md](../smoke-testing.md).

## Test selection

The smoke plan is `plans/harness-smoke-plan.yaml`. It targets backend
`oc-edge-112` and runs **eight** catalog TRs at minimal scale (excludes
**TR-VIRT-007** HA node failure and **TR-VIRT-008** node drain). **TR-STOR-002**
(FIO / virtbench) is **commented out** until gather reliability is fixed
([ECOPROJECT-5566](https://redhat.atlassian.net/browse/ECOPROJECT-5566)); overrides
remain in the plan for when it is re-enabled.

| Test | Workload | Tool |
| --- | --- | --- |
| `TR-VIRT-001` | Boot storm | `virtbench` |
| `TR-VIRT-004` | Live migration (minimal) | `kube-burner-ocp` |
| `TR-STOR-006` | PVC provisioning density | `kube-burner-ocp` |
| `TR-VIRT-010` | VM snapshot creation | `kube-burner` |
| `TR-VIRT-013` | Per-node scale ceiling | `virtbench` |
| `TR-VIRT-018` | Clone at scale | `virtbench` |
| `TR-VIRT-019` | Parallel VM lifecycle | `kube-burner-ocp` |
| `TR-VIRT-027` | Snapshot depth per volume | `kube-burner` |

Keep the plan's `backend` and node overrides aligned with your cluster. The plan
sets `backend: oc-edge-112`; staged `work/backends.local.yaml` (from
`ci/config/smoke/`) must define a backend with that **exact** name (or pass
`BACKEND=<name>` when running).

For node-scoped TRs, set `node_name` / `target_node` in plan overrides to worker
nodes that exist in **your** cluster (for example `<worker-0>.<lab-domain>`).

## Stage config and run the plan

From `storage-cert-harness`, stage the tracked smoke bundle, build the harness
binary and local image, then run smoke against your kubeconfig:

```sh
export KUBECONFIG=/path/to/your/kubeconfig
export KUBECTL_INSECURE_SKIP_TLS_VERIFY=true   # only if your lab requires it

cp -f "$KUBECONFIG" work/kubeconfig   # or symlink; never commit

make build
make image-build-local
make prepare-smoke-env                  # ci/config/smoke -> work/
BACKEND=oc-edge-112 make run-harness-smoke
```

One-shot local CI parity:

```sh
make smoke-ci KUBECONFIG=/path/to/your/kubeconfig
```

(`smoke-ci` = `clean-smoke-env` + `prepare-smoke-env` + `run-image-tests.sh`.)

Equivalent manual `podman run` (same plan and backend as CI after prepare):

```sh
podman run --rm --network host --userns=keep-id \
  --user "$(id -u):$(id -g)" \
  -v "$(pwd)/work/kubeconfig:/kubeconfig:ro,Z" \
  -e KUBECONFIG=/kubeconfig \
  -e KUBECTL_INSECURE_SKIP_TLS_VERIFY="${KUBECTL_INSECURE_SKIP_TLS_VERIFY:-false}" \
  -v "$(pwd)/work:/work:Z" \
  -v "$(pwd)/plans:/plans:ro,Z" \
  -v "$(pwd)/work/backends.local.yaml:/backends.yaml:ro,Z" \
  -w /work \
  "$(tr -d '[:space:]' < work/local-image-ref)" run \
  --plan /plans/harness-smoke-plan.yaml \
  --catalog /work/catalog.json \
  --thresholds /work/harness-run-thresholds.json \
  --backends /backends.yaml \
  --backend oc-edge-112 \
  --workdir /work/harness-smoke-work \
  --output /work/harness-smoke-report \
  --no-attestations \
  --verbose
```

Reports and tool artifacts are written to `work/harness-smoke-report/`. Scripts
live under `ci/scripts/run-harness/` (`prepare-smoke-env.sh`, `run-image-tests.sh`,
`live-cluster-smoke.sh` for CI).

## Validate the cluster first

```sh
export KUBECONFIG=/path/to/your/kubeconfig
export KUBECTL_INSECURE_SKIP_TLS_VERIFY=true   # if required by your lab
oc whoami --show-server
oc whoami
oc get nodes -l node-role.kubernetes.io/worker
oc get clusterversion
oc get storageclass <your-storage-class>
oc get volumesnapshotclass <your-snapshot-class>
```

Use `oc config view --minify` to inspect the active context and cluster
endpoint. Do not commit kubeconfig or backend credentials.

---

## Action items — finish local smoke and CI/CD

Track completion against [storage-cert-harness-ci-smoke-test.md §11–§13](storage-cert-harness-ci-smoke-test.md)
(same IDs **A1–D3**). Jira umbrella: [ECOPROJECT-5406](https://redhat.atlassian.net/browse/ECOPROJECT-5406).

### Lab sign-off (local / edge-112)

| ID | Jira (sub-task of [5406](https://redhat.atlassian.net/browse/ECOPROJECT-5406)) | Item | Notes | Status |
| --- | --- | --- | --- | --- |
| A1 | [ECOPROJECT-5582](https://redhat.atlassian.net/browse/ECOPROJECT-5582) | **Validate edge-112 with current kubeconfig** | Run `make smoke-ci` (or prepare + `run-harness-smoke`) after rotating lab kubeconfig; confirm all **eight** active TRs pass; record per-TR durations for design §6.3/§6.4. | Open |
| A2 | [ECOPROJECT-5583](https://redhat.atlassian.net/browse/ECOPROJECT-5583) | **Re-enable TR-STOR-002 in the smoke plan** | Uncomment in `harness-smoke-plan.yaml` after virtbench gather fix; re-run full sign-off on edge-112. | Blocked — [ECOPROJECT-5566](https://redhat.atlassian.net/browse/ECOPROJECT-5566) |
| A3 | [ECOPROJECT-5580](https://redhat.atlassian.net/browse/ECOPROJECT-5580) | **Review and update smoke contributor docs** | Primary guide: [smoke-testing.md](../smoke-testing.md); plan runbook in `design/` | Mostly done |

### CI enablement and secrets

| ID | Jira | Item | Notes | Status |
| --- | --- | --- | --- | --- |
| B1 | [ECOPROJECT-5584](https://redhat.atlassian.net/browse/ECOPROJECT-5584) | **GitHub secrets and repository variable** | Set `vars.CI_LIVE_SMOKE_ENABLED=true`; configure **`live-smoke`** environment (masked `CI_CLUSTER_KUBECONFIG`, backend materialization, self-hosted runner labels). No kubeconfig in git. | Open |
| B2 | [ECOPROJECT-5585](https://redhat.atlassian.net/browse/ECOPROJECT-5585) | **Implement `live-cluster-smoke.sh` execution** | Today: lease acquire + exit **5** (track under [ECOPROJECT-5406](https://redhat.atlassian.net/browse/ECOPROJECT-5406)). Still needed: secrets → `work/`, load PR image or build, `podman run` from PR `plans/`, janitor, `smoke-timing.json`, release lease on error/cancel. | Open |
| B3 | [ECOPROJECT-5535](https://redhat.atlassian.net/browse/ECOPROJECT-5535) | **Job timeouts** | `ci-live-smoke.yml` self-hosted jobs use `timeout-minutes: 240`. Reopen Jira only if other workflows need the same budget. | Done |
| B4 | [ECOPROJECT-5586](https://redhat.atlassian.net/browse/ECOPROJECT-5586) | **Branch protection** | Require **`live-smoke`** (or agreed check name) on PRs to `main` once B1–B2 pass. | Open |

### CI triggers and policy (workflow mostly scaffolded)

| ID | Jira | Item | Notes | Status |
| --- | --- | --- | --- | --- |
| C1 | [ECOPROJECT-5536](https://redhat.atlassian.net/browse/ECOPROJECT-5536) | **`[skip live-smoke]` opt-out** | Documented in `docs/ci.md` / contributing; workflow skips scheduling when token is in PR title/body. **Verify:** skipped runs report auditable conclusion (e.g. `live_smoke_skipped` in summary) and do not block merge when lab is down. | Open |
| C2 | [ECOPROJECT-5587](https://redhat.atlassian.net/browse/ECOPROJECT-5587) | **Scheduled smoke** | `ci-live-smoke.yml` cron **05:00 UTC** + 24h open-PR gate — **done in repo**; first green run after **B1** + **B2**. | Enablement open |
| C3 | [ECOPROJECT-5588](https://redhat.atlassian.net/browse/ECOPROJECT-5588) | **On-demand smoke** | **`/run-live-smoke`** and **`workflow_dispatch`** — **done in repo**; first green run after **B1** + **B2**. | Enablement open |
| C4 | [ECOPROJECT-5589](https://redhat.atlassian.net/browse/ECOPROJECT-5589) | **Fork / untrusted PRs** | Workflow rejects fork heads on comment/dispatch paths; schedule skips non-canonical PRs. Re-verify when **B1** enables jobs. | Verify at enablement |

### Scripts and repo hygiene

| ID | Jira | Item | Notes | Status |
| --- | --- | --- | --- | --- |
| D1 | [ECOPROJECT-5590](https://redhat.atlassian.net/browse/ECOPROJECT-5590) | **Remove or consolidate duplicate / unused scripts** | Prefer `prepare-smoke-env.sh` over legacy `stage-smoke-config.sh` naming in docs; drop stale copies under `ci/scripts/tests/` (local diag — gitignored) if not needed; align design doc references with `ci/scripts/run-harness/`. | Open |
| D2 | [ECOPROJECT-5591](https://redhat.atlassian.net/browse/ECOPROJECT-5591) ([5569](https://redhat.atlassian.net/browse/ECOPROJECT-5569) closed) | **Cluster Coordination Lease** | Acquire, renewal, fail-fast, and traps in `run-image-tests.sh` and start of `live-cluster-smoke.sh`. Remaining: janitor + CI cancel paths with **B2**. | Partial |
| D3 | [ECOPROJECT-5592](https://redhat.atlassian.net/browse/ECOPROJECT-5592) | **Update ECOPROJECT-5406** | Reflect PR smoke model (on-demand + schedule, no push/open auto-smoke). | Open |

### Already delivered (do not re-implement)

- **`ci/config/smoke/`** bundle + **`make prepare-smoke-env`** / **`make smoke-ci`** staging to `work/`.
- **Eight-TR smoke plan**, **`thresholds-smoke.json`**, FIO runner changes (no harness guest SSH).
- **Local smoke hygiene:** `smoke-run.lock`, log archive, artifact cleanup (`run-image-tests.sh` / `ci-utils.sh`).
- **Cluster Coordination Lease (core):** `smoke-cluster-lease.sh` in local smoke and CI entry (see **D2** for janitor/cancel).
- **`ci-live-smoke.yml`:** schedule (05:00 UTC + 24h PR gate), `/run-live-smoke`, `workflow_dispatch`, fork guards — gated on `CI_LIVE_SMOKE_ENABLED` (see **C2**–**C4** for first live runs).
- **Live-smoke job timeouts:** `timeout-minutes: 240` on self-hosted jobs (**B3**).
- **Skip token docs** for `[skip live-smoke]` (workflow verification remains **C1**).
- **Contributor smoke runbook:** this document + `docs/ci.md` / `README.md` for CI triggers.

### Suggested completion order

1. **A1** — local edge-112 green on eight TRs with rotated kubeconfig.
2. **D1** — script/doc cleanup so operators have one path (`smoke-ci`).
3. **B2** + **D2** — finish live-cluster runner and lease/janitor.
4. **B1** — secrets and enable flag; dry-run comment + schedule.
5. **C1**, **C4** — verify skip and fork behavior when CI is enabled.
6. **B4** — branch protection (timeouts **B3** done for live-smoke workflow).
7. **A2** after **5566** — nine TRs including STOR-002; refresh §6.4 timings.
8. **D3** — Jira / stakeholder update.
