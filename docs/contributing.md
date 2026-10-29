# Contributing

## Workflow

The project follows an ADR-driven, small-task SDLC (modeled on
osac-project/osac-workspace, adapted for a single Go repo):

1. **Decide** — significant design choices get an ADR in `decisions/` (copy
   `template.md`), reviewed and merged as a PR before large implementation.
2. **Slice** — break work into small tasks; a single stage adapter (e.g. one
   tool's `ResultParser`) is a good unit.
3. **Implement** — TDD where practical; parsers get golden-file tests.
4. **Review** — PR with review; branch protection on the default branch.

## Git

- Feature branches; open a PR into the default branch.
- AI-assisted commits carry an `Assisted-by:` trailer.
- Nothing secret in commits — ever (ADR-0002 / ADR-0004).

## Local checks before pushing

See [ci.md](ci.md) for the full pipeline, versioning, and image contents.

```sh
pre-commit install          # yaml + md + go + secret-scan + version-lock
./ci/scripts/install-tools.sh
./ci/scripts/ci.sh
make set-next-version BINARY=1.0.0   # or IMAGE=1.0.0; see docs/ci.md Versioning
```

`make unittest` / `./ci/scripts/unittest.sh` are **unit tests only** (no cluster).
`make lint` and `make test` match the GitLab `lint` and `test` stages.

## Live cluster smoke before you open a PR

**CI automation:** GitHub live smoke runs only when
`vars.CI_LIVE_SMOKE_ENABLED=true` and the `live-smoke` environment is configured
(see [design doc](design/storage-cert-harness-ci-smoke-test.md) and
[ci-live-smoke.yml](../.github/workflows/ci-live-smoke.yml)). Until operators
enable it, use the local commands below.

When enabled, upstream CI runs live smoke **only on demand** (PR comment or
`workflow_dispatch`) or on the **daily schedule** when an open same-repo PR to
`main` was updated in the last 24 hours. A **failed** live-smoke run **blocks
merging the PR** until you investigate, fix your branch, and get a **passing**
re-run. Run smoke on your own lab **before** opening the PR (next section).

### When you must run smoke locally

Run the full CI smoke plan on **your own dedicated OpenShift lab** (your
kubeconfig and `work/backends.local.yaml` — **not** the CI edge cluster) at
least **once** when your branch changes anything that would trigger CI live
smoke, for example:

- `cmd/**`, `internal/**`
- `catalog.json`, `thresholds.json`, `schemas/**`
- `Containerfile`, `ci/config/images.env`, or harness image / builder pins

Changes that touch **only** `go.mod`, `go.sum`, or `vendor/**` do **not** trigger
automatic CI smoke, but run smoke anyway if you changed behavior that depends on
those dependencies.

Design and path rules:
[`design/storage-cert-harness-ci-smoke-test.md`](design/storage-cert-harness-ci-smoke-test.md).

### How to run the smoke plan locally

Prerequisites: [cluster-setup-guide.md](cluster-setup-guide.md),
[cluster-requirements.md](cluster-requirements.md).

```sh
make build
make image-build-local
KUBECONFIG=/path/to/your/kubeconfig \
  BACKEND=oc-edge-112 \
  make run-harness-smoke
```

Optional: `KUBECTL_INSECURE_SKIP_TLS_VERIFY=true` for lab TLS. Reports land under
`work/harness-smoke-report/`.

### After the PR is open — on-demand CI smoke

When `CI_LIVE_SMOKE_ENABLED` is true, request shared-cluster smoke explicitly —
there is **no** automatic run when the PR is created. If a run **fails**,
read the workflow logs and `report` artifacts, fix the issue, then trigger another
run — **do not merge** until live-smoke is green. To run smoke
(same-repo PRs only):

| Method | Action |
| --- | --- |
| PR comment | Post `/run-live-smoke` on the pull request |
| GitHub CLI | `gh pr comment <number> --body '/run-live-smoke'` |
| Actions UI | **CI Live Smoke** → **Run workflow** with the PR **number** |

Do not use `[skip live-smoke]` in the PR title/body unless the lab is down or
maintainers agree — see [ci.md](ci.md) (Skip live smoke). Fork PRs
do not receive upstream cluster credentials; use local smoke on your cluster instead.

## Partner cluster setup

- [cluster-requirements.md](cluster-requirements.md) — hardware / OCP version / storage spec
- [cluster-setup-guide.md](cluster-setup-guide.md) — step-by-step setup guide and prerequisites checklist

## Adding a tool

See [adapter-authoring.md](adapter-authoring.md).

## Decision records

`decisions/NNNN-title.md` from `decisions/template.md`. Status lifecycle:
Proposed → Accepted → (Deprecated | Superseded by NNNN).
