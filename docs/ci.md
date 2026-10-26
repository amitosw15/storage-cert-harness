# CI/CD

Script-centric checks for **storage-cert-harness** ([ECOPROJECT-5274](https://redhat.atlassian.net/browse/ECOPROJECT-5274),
[ADR-0010](../decisions/0010-ci-scripts-and-multi-host.md),
[ADR-0011](../decisions/0011-harness-ships-as-container-image.md)). The same `ci/scripts/*.sh`
files run locally, in GitHub Actions, and via
pre-commit (yamllint, markdownlint, golangci-lint, secret-scan).

**Support status:** GitHub Actions is the only supported and maintained CI
provider. GitLab CI is no longer supported.

**`unittest.sh`** (`make unittest`) runs unit tests only (`go test ./...`:
package tests and golden-file parser tests). It does not need a cluster.
`make lint` / `make test` provide the corresponding local checks.
Offline CLI smoke is `replay-smoke.sh`. Live 3-VM boot-storm is a follow-up
(see Open items).

The GitHub Actions `supply-chain` job is gating: a failure fails the workflow.

## Layout

```text
ci/
  scripts/    # portable job logic
    ci-utils.sh          # shared helpers (REPO_ROOT, version funcs, container_engine)
    images.sh            # load ci/config/images.env, derive tool refs
    build.sh             # bin/harness (binary_version ldflags)
    image-build.sh       # linux/amd64 container → dist/harness-image.tar
    image-contents-check.sh  # verify harness + kubectl + virtctl + tool binaries
    layout-check.sh      # verify ci/scripts + ci/config + version files
    set-next-version.sh  # human-driven version bumps
    advance-version.sh   # CI auto-advance on main push
    lint-actions.sh      # actionlint wrapper for .github/workflows/*.yml
    ...                  # lint-*.sh, unittest.sh, secret-scan.sh, etc.
  config/     # linters, pins, allowlists
    images.env           # pinned CI images and tool versions (single source of truth)
    images.yml           # generated image configuration
    golangci.yml         # golangci-lint config
    yamllint.yaml        # yamllint config
    markdownlint-cli2.jsonc  # markdownlint-cli2 config (includes MD rule overrides)
    markdownlint.yaml    # markdownlint fallback config
    trivyignore          # accepted CVEs for Trivy
    secret-scan-allowlist.txt  # unused since ADR-0017 (numeric scan removed)
VERSION / NEXT-VERSION               # harness binary semver
IMAGE-VERSION / NEXT-IMAGE-VERSION   # container image tag semver
```

## Wrappers and scripts

```mermaid
flowchart TB
  subgraph wrappers [Thin wrappers]
    Local["make ci / ./ci/scripts/ci.sh"]
    PreCommit["pre-commit: yaml + md + go + secret-scan + version-lock"]
    GHA[".github/workflows/ci.yml"]
  end
  subgraph scripts [ci/scripts/]
    LintYaml["lint-yaml.sh"]
    LintActions["lint-actions.sh"]
    LintMd["lint-md.sh"]
    LintGo["lint-go.sh"]
    Unittest["unittest.sh"]
    Secret["secret-scan.sh"]
    Supply["supply-chain.sh"]
    Build["build.sh"]
    Replay["replay-smoke.sh"]
    Image["image-build.sh"]
    ImageCheck["image-contents-check.sh"]
    ImageLint["image-lint.sh"]
    ImageTrivy["image-scan-trivy.sh"]
    ImageDive["image-scan-dive.sh"]
  end
  Local --> LintYaml
  Local --> LintMd
  Local --> LintGo
  Local --> Unittest
  Local --> Secret
  Local --> Supply
  Local --> Build
  Local --> Replay
  Local --> Image
  Local --> ImageCheck
  Local --> ImageLint
  Local --> ImageTrivy
  Local --> ImageDive
  PreCommit --> LintYaml
  GHA --> LintActions
  PreCommit --> LintMd
  PreCommit --> LintGo
  PreCommit --> Secret
  GHA --> LintYaml
  GHA --> LintMd
  GHA --> LintGo
  GHA --> Unittest
  GHA --> Secret
  GHA --> Supply
  GHA --> Build
  GHA --> Image
  GHA --> ImageCheck
```

Offline Go: `GOPROXY=off` and `GOFLAGS=-mod=vendor`. Commit `vendor/`.
`go install` of govulncheck in the supply-chain job must set `GOFLAGS=`
(empty) so the proxy can be queried; that binary is not in `vendor/`.
gosec is the GitHub **release binary** (`GOSEC_VERSION`); do not `go install`
it in CI — compiling it pulls AI SDKs and OOM-kills CEE pods.

`go mod verify` / `go mod vendor` need a populated module cache. A cold cache
fails with `module lookup disabled by GOPROXY=off` before govulncheck runs.
`supply-chain.sh` logs each step; under `GOPROXY=off` it continues from
committed `vendor/` when verify/vendor cannot run. Integrity on CEE is
`vendor/` + `go list -mod=vendor`. To force verify locally:
`GOPROXY=https://proxy.golang.org,direct ./ci/scripts/supply-chain.sh`.

## Versioning

Two independent semver tracks at the repo root. Each file contains a single
`MAJOR.MINOR.PATCH` line (no `v` prefix):

| File | Purpose |
|------|---------|
| `VERSION` | Last harness **binary** released from main (starts `0.0.0`) |
| `NEXT-VERSION` | Next binary version (starts `0.1.0`) |
| `IMAGE-VERSION` | Last **container image** released from main (starts `0.0.0`) |
| `NEXT-IMAGE-VERSION` | Next image version (starts `0.1.0`) |

The tracks may diverge (binary `1.2.0` inside image tagged `3.0.0`).
`harness version` is always the **binary** track. The Quay tag and OCI label
`org.opencontainers.image.version` are always the **image** track.

### Computed version strings (`ci-utils.sh`)

| Helper | Local / PR | CI push to `main` |
|--------|-----------------|-------------------|
| `binary_version()` | `$(cat NEXT-VERSION)-$(git_sha)` | `$(cat NEXT-VERSION)` |
| `image_version()` | `$(cat NEXT-IMAGE-VERSION)-$(git_sha)` | `$(cat NEXT-IMAGE-VERSION)` |

Process-only overrides (do not write files):
`HARNESS_VERSION` → binary; `HARNESS_IMAGE_VERSION` → image tag.

`is_main_push`: GitHub `$GITHUB_REF == refs/heads/main &&
$GITHUB_EVENT_NAME == push`.
Local checkout of main still gets `-sha`.

### Wiring

- `build.sh`: `-X main.version=$(binary_version)` → `dist/binary-version.txt`
- `build` publishes `bin/harness` as the `harness-binary` CI artifact. The
  image job downloads that artifact before running `image-build.sh`; image
  construction does not compile Go.
- `image-build.sh`: `--build-arg HARNESS_VERSION=$(binary_version)` (binary
  supplied by the build artifact),
  `--build-arg IMAGE_VERSION=$(image_version)` (OCI label),
  `--build-arg BUILD_IMAGE=…` (digest-pinned **harness-builder** tool payload;
  see [Container image build](#container-image-build)),
  `--build-arg RUNTIME_IMAGE=…` (UBI python-minimal runtime base),
  tag `${QUAY_IMAGE}:$(image_version)`, `:main`, and `:latest` on main push.
  Write `dist/image-version.txt`.

### Version flow

```mermaid
flowchart LR
  dev[Developer sets NEXT-VERSION]
  mr[MR: 0.1.0-abc123]
  main[main push: 0.1.0]
  adv[advance-version.sh]
  next[VERSION=0.1.0 NEXT=0.1.1]
  dev --> mr --> main --> adv --> next
```

### Advance (main only, after a successful image push)

`advance-version.sh` — one commit for both tracks so a failed image does not
consume either number:

1. `VERSION :=` current `NEXT-VERSION`; `NEXT-VERSION` patch + 1
2. `IMAGE-VERSION :=` current `NEXT-IMAGE-VERSION`; `NEXT-IMAGE-VERSION` patch + 1
3. Stage all four files and set `ALLOW_VERSION_CHANGE=1`. GitHub passes the
  staged changes to its automated version-bump pull request.

Refuses unless `is_main_push` and each NEXT is valid semver greater than its
released counterpart.

### Bootstrap, lock, and setting one track

Pre-commit hook `version-files-locked` blocks staged changes to the four
version files unless `ALLOW_VERSION_CHANGE=1` or `GITHUB_ACTIONS` is set. On
failure, it prints the commands below. Never use `--no-verify`.

**Initial (both tracks):**

```sh
./ci/scripts/set-next-version.sh --init 0.1.0 0.1.0   # binary then image
ALLOW_VERSION_CHANGE=1 git add VERSION NEXT-VERSION IMAGE-VERSION NEXT-IMAGE-VERSION
ALLOW_VERSION_CHANGE=1 git commit -m "chore: initial binary 0.1.0 image 0.1.0"
```

**Later — one track only:**

```sh
./ci/scripts/set-next-version.sh --binary 1.0.0
./ci/scripts/set-next-version.sh --image 2.0.0
# make set-next-version BINARY=1.0.0
# make set-next-version IMAGE=2.0.0
ALLOW_VERSION_CHANGE=1 git add NEXT-VERSION          # or NEXT-IMAGE-VERSION
ALLOW_VERSION_CHANGE=1 git commit -m "chore: set next binary version to 1.0.0"
```

**One-off local:**

```sh
HARNESS_VERSION=1.0.0-local ./ci/scripts/build.sh
HARNESS_IMAGE_VERSION=2.0.0-local ./ci/scripts/image-build.sh
```

**Not supported:** editing the released `VERSION` / `IMAGE-VERSION` files by
hand (CI-owned after bootstrap); `-rc` in the NEXT files.

GitHub's `version-bump` job has `contents: write` and `pull-requests: write`.
It runs `advance-version.sh` with `VERSION_BUMP_PR=1`, which stages the four
files without committing. `peter-evans/create-pull-request` creates or updates
`chore/version-bump` against `main`, then `gh pr review` approves it using the
`ADMIN_PAT_TOKEN` secret and `gh pr merge --auto --squash` queues the merge using
the workflow's `GITHUB_TOKEN`. The PR is merged only after branch protection
requirements pass.

## GitHub Actions

`.github/workflows/ci.yml` orchestrates six ordered reusable workflows:
`ci-linters.yml`, `ci-tests.yml`, `ci-build.yml`, `ci-smoke.yml`,
`ci-images.yml`, and `ci-publish.yml`. The stages run in order as
linters -> tests -> build -> image build -> image push/version bump.
`ci-smoke.yml` remains **skipped by default** (opt-in via repository variables).
The Trivy/Dive jobs inside `ci-images.yml` run with the image build and are
non-blocking; they are not CI gates. The linter workflow
installs a pinned `actionlint` release and validates all
`.github/workflows/*.yml` files. The build and image workflows pass the
`harness-binary` and `harness-image` artifacts to later stages.

On a successful `main` publish, the `version-bump` job creates the automated
version-bump PR. The shared `advance-version.sh` script configures the
`github-actions[bot]` identity when running in GitHub Actions; local runs
preserve the developer's Git identity. The resulting commit is not
cryptographically signed by the current CI setup.

The top-level workflow dispatch input is:

| Input | Purpose | Default |
|-------|---------|---------|
| `publish` | Enable image push and version advancement after the checks pass | `false` |

**Publish gates (GitHub).** On a push to `main`, `image-push` runs when
`image-build` succeeds. `replay-smoke`, `image-scan-trivy`, and
`image-scan-dive` run with allowed failures and do not gate publish. A manual
`workflow_dispatch` with `publish=true` can also push from `main`.

**Image lint and scans.**

| Where | `image-lint` | `image-scan-trivy` / `image-scan-dive` |
|-------|--------------|----------------------------------------|
| GitHub Actions | Before `image-build`; `continue-on-error: true` | Run after image build; failures are allowed |
| Local | `make image-lint` | `make image-scan` / `make ci-image` |

Optional GitHub repository variable (unset by default): `CI_RUN_REPLAY_SMOKE` set
to `true` to run the smoke job for debugging. The image lint and image scans run
by default and remain non-gating.

Run the Containerfile lint locally with `make image-lint`. Locally it uses the
pinned native Hadolint release binary installed by `install-tools.sh`; CI uses
the pinned Hadolint image from `CI_TOOLS_IMAGE`. The script also reports any
privileged `USER 0` setup lines for context. Run image scans locally with
`make image-build && make image-scan`.
Trivy writes `dist/trivy-report.json`, `dist/trivy-report.txt`,
`dist/trivy-secret-report.json`, `dist/trivy-misconfig-report.txt`, and
`dist/sbom.cdx.json`; Dive writes
`dist/dive-report.txt`. GitHub runs the scan jobs with failures allowed, so
logs and reports are available as job artifacts; it produces no Code scanning
(SARIF) upload. Local `dist/` output remains available via `make image-scan`.

The `secret-scan` job uses `fetch-depth: 0` so its merge-base diff scan can
inspect the complete history.

## Image contents

The release image is `linux/amd64` only. It covers x86 hosts natively, Intel
Macs natively, and Apple Silicon via Docker/Podman Desktop Rosetta 2.

| Binary | Purpose | Source in the runtime image |
|--------|---------|------------------------------|
| `harness` | CLI (this repo) | `bin/harness` from `build.sh` / CI artifact |
| `kube-burner` | TR-VIRT-010 adapter | Pre-built in **harness-builder** |
| `kube-burner-ocp` | TR-STOR-006 adapter | Pre-built in **harness-builder** |
| `oc`, `kubectl` | Cluster clients | Pre-built in **harness-builder** |
| `virtctl` | KubeVirt CLI | Pre-built in **harness-builder** |
| `virtbench` | virtbench adapter (local exec) | Pre-built venv + runtime tree in **harness-builder** |

Certification tools are **not** downloaded or compiled in the harness
`Containerfile`. They are copied from the digest pinned as `BUILD_IMAGE` in
`ci/config/images.env` (Quay `quay.io/virtarraycert/harness-builder@sha256:…`).
The builder's OCI labels and packaged binaries are the source of truth for
tool versions and source revisions. `HARNESS_BUILDER_VERSION` records the
matching builder tag for humans; the digest is the reproducible build input.

The harness image adds harness-owned files only: the `harness` binary,
`container-entrypoint.sh`, and `container-patches/` (for example
`examples/utilities/ssh-pod.yaml`). Virtbench `examples/vm-templates/` comes
from the **harness-builder** payload copied into `/opt/virtbench-runtime`. `virtbench` scenarios such as `datasource-clone`
require `examples/vm-templates/rhel9-vm-datasource.yaml` under
`/opt/virtbench-runtime` after the entrypoint copies the tree to a temp dir.

All listed binaries must be on `PATH` under `/usr/bin/` (virtbench also uses
`/opt/virtbench-venv`). The builder release owns their version pins.
`image-contents-check.sh` verifies each binary and the datasource template after
every image build (locally and in CI).

The **ssh-helper** pod image is separate: digest pins in
`internal/tools/virtbench/setup.go` and
`container-patches/virtbench/examples/utilities/ssh-pod.yaml` (see
[`ssh-helper-image.md`](ssh-helper-image.md)). Bump those when
**storage-cert-harness-images** publishes a new `ssh-helper` release.

The slim harness `Containerfile` does not run tool-compile stages or pass
tool source/version arguments; customized builds belong in the images
repository before a new builder digest is promoted here.

In the supported GitHub Actions flow, `image-push` runs automatically on
`main` pushes. `supply-chain` is gating.

## Container image build

1. **storage-cert-harness-images** builds and publishes **harness-builder**
   (release tarballs today; customized source builds land there first).
2. Record the immutable digest (`skopeo inspect` on the semver tag).
3. In this repo, set `HARNESS_BUILDER_VERSION` and `BUILD_IMAGE@sha256:…` in
   `ci/config/images.env`, then run `./ci/scripts/sync-images-yml.sh --check`.
4. Build the runtime image: `make image-build-local` (rebuilds `bin/harness`
   under the same lock) or `./ci/scripts/image-build.sh` (GitHub: `ci-images.yml`
   after the `harness-binary` artifact).

The `Containerfile` uses `FROM ${BUILD_IMAGE} AS tools` and `COPY --from=tools`
for `/usr/bin/*`, `/opt/virtbench-runtime` (including `examples/vm-templates`),
and `/opt/virtbench-venv`, then `FROM ${RUNTIME_IMAGE}` for the final non-root
runtime. It does not download, compile, or relabel the packaged tools.
Override `BUILD_IMAGE` in the environment only for local experiments;
release CI uses the pinned digest from `images.env`.

Use `IMAGE_BUILD_NO_CACHE=1` (or `make image-build-fresh`) when refreshing layers
after a new builder digest. `make image-build-local` tags a unique local name;
CI writes `dist/harness-image.tar` for scan jobs.

### Local image build lock

`clean.sh`, `image-build.sh`, `image-build-local.sh`, `clean-image-cache.sh`,
`image-contents-check.sh`, `image-scan-trivy.sh`, `image-scan-dive.sh`, and
`image-push.sh` acquire an exclusive `flock` on `work/local-image.lock`. The
lock serializes access to `work/local-image-ref`, image tarballs/tags, and the
shared `dist/*-version.txt` metadata so `make clean`, scan, push, or validation
cannot overlap an image build or cache prune. `make build` (`build.sh`) does
**not** take this lock; only image-related scripts do. `make image-build-local`
rebuilds the binary under the same image lock for the full rebuild.
`make image-build-fresh` prunes cache and rebuilds in the same locked
`image-build.sh` process (`IMAGE_BUILD_FRESH=1`). `image-build.sh` and
`image-build-local.sh` call `invalidate_previous_local_image_build` before
starting a rebuild (remove `work/local-image-ref`, the prior Podman image,
`dist/harness-image.tar`, and `dist/*-version.txt`). Local image validation
always runs the local image with Podman and preserves its native platform;
`image-contents-check.sh` and `run-image-tests.sh` refuse a stale ref when the
named image is not present in Podman.

### Smoke run lock

`run-image-tests.sh` acquires a separate exclusive `flock` on
`work/smoke-run.lock` so two local smoke runs cannot overlap (log archive,
`work/harness-smoke-work`, and `work/harness-smoke-report`). Smoke does not
hold the image lock; `make build` and a long smoke run may proceed in parallel.

Both locks are released automatically when the owning process exits, including
when a command fails. The image lock is not a transaction: a failed build can
leave partial engine layers, so rerun the relevant build before validation after
an error. Lock files may remain on disk and are reused by subsequent runs.

### Partner certification runs (published image)

Partners run Level 3 certification against the **published** Quay image (no local
`make image-build-local`). Workflow and cluster prep:
[PARTNER-CERTIFICATION-GUIDE.md](../PARTNER-CERTIFICATION-GUIDE.md).

| Make target | Script | Role |
| --- | --- | --- |
| `make partner-preflight` | `run-partner-preflight.sh` | Host `oc` checks + `harness preflight` in the container |
| `make partner-cert-load-80` | `run-partner-cert.sh load-80` | `plans/level3-customer-load-80.yaml`, root `thresholds.json` |
| `make partner-cert-no-load-80` | `run-partner-cert.sh no-load-80` | `plans/level3-customer-no-load-80.yaml`, `--continue-from` load-80 report |
| `make partner-package-results` | `run-partner-package-results.sh` | Level 3 → `certification-results-level3.tar.gz` (`load-80` + `no-load-80`) |
| `make partner-smoke-package` | `run-partner-package-results.sh` | Lab smoke → `certification-results-smoke.tar.gz` (branch `test/partner-scripts-smoke`) |

Required environment: `KUBECONFIG`, `BACKEND` (name in `backends.yaml`). Optional:
`HARNESS_IMAGE` (default `quay.io/virtarraycert/storage-cert-harness:main`),
`CERT_RESULTS_ROOT`, `PARTNER_KEEP_WORK`, `PLAN` (preflight only).

Outputs:

- **Host logs:** each partner script archives the previous file, then tees to
  `logs/run-partner-preflight.log`, `logs/run-partner-cert-load-80.log`, or
  `logs/run-partner-cert-no-load-80.log` (see `ci_log_init` in `ci-utils.sh`).
- **Harness artifacts:** `certification-results/<profile>/report/` (reports,
  `run.log`) and `certification-results/<profile>/work/` (tool workdirs).
  Re-running a profile archives the prior `certification-results/<profile>/`
  tree with a UTC timestamp unless `PARTNER_KEEP_WORK=true`.

`partner-harness-common.sh` is sourced only (validation, `podman pull`, log/
result hygiene). Partner runs do **not** take `work/smoke-run.lock`; only one
harness execution per cluster is documented as partner responsibility.

Contributor smoke against a **local** image still uses `make run-harness-smoke`
(`run-image-tests.sh`, `work/local-image-ref`). Pulling the registry image for
lab checks uses `make run-quay-image-tests` (`run-quay-image-tests.sh`), which
also archives `logs/run-quay-image-tests.log` before each run.

## Job images (no Docker Hub)

Pins live in **`ci/config/images.env`**. Shell scripts source
`ci/scripts/images.sh` via `ci/scripts/ci-utils.sh` or `install-tools.sh`.
After editing the env file, run `./ci/scripts/sync-images-yml.sh`; the
generated projection is retained for repository compatibility.

| Job | Pin (`ci/config/images.env`) |
| --- | --- |
| lint-yaml | `YAML_LINT_IMAGE` |
| lint-md | `MD_LINT_IMAGE` |
| lint-go, unittest, secret-scan, supply-chain, replay-smoke, build | Host Go on GitHub Actions (`setup-go`); `GO_IMAGE` is listed for legacy GitLab job images |
| image-build | `BUILD_IMAGE` (digest-pinned **harness-builder**) and `RUNTIME_IMAGE` |
| image-scan-trivy | Trivy release binary (`TRIVY_VERSION`) + `dist/harness-image.tar` |
| image-scan-dive | Dive release binary (`DIVE_VERSION`) + `dist/harness-image.tar` |

`GO_IMAGE` (`docker.io/library/golang:1.26.9`, Go **1.26.9+**) is not used by the harness
`Containerfile`. `BUILD_IMAGE` is **not** the UBI python builder base; it names
the published **harness-builder** image that already contains the cert tools.
`YAML_LINT_IMAGE` and `MD_LINT_IMAGE` use the UBI **minor stream** (`:9.8` /
`:9.7`), not `:latest`. golangci-lint is the GitHub **release tarball**
(`GOLANGCI_LINT_VERSION`) into `$(go env GOPATH)/bin` on CI and `~/.local/bin`
locally — not Docker Hub and not tied to `BUILD_IMAGE`.

### Populating `quay.io/virtarraycert/ci_tools`

That repo is **public and starts empty**. Trivy/Dive jobs cannot run until
someone retags the pinned Hub images from a machine that *can* pull Hub and
push to Quay ([ECOPROJECT-5411](https://redhat.atlassian.net/browse/ECOPROJECT-5411)):

```sh
podman login quay.io
make mirror-ci-tools          # or PUSH=0 ./ci/scripts/mirror-ci-tools.sh to tag only
```

Tags come from `TRIVY_VERSION` / `DIVE_VERSION` in `ci/config/images.env`
(`trivy-<version>`, `dive-<tag>`).

## Local tools

Install once (idempotent). Requires Go 1.26.9+, git, python3, pip, and npm
already on `PATH`. Tool versions are pinned in `ci/config/images.env`.

```sh
./ci/scripts/install-tools.sh          # install anything missing
./ci/scripts/install-tools.sh --check  # print status only
make install-tools
```

Put `$(go env GOPATH)/bin` **before** `/usr/local/bin` on `PATH`. Pins match
`ci/config/images.env`: golangci-lint is a GitHub release binary installed
into `~/.local/bin` (local) or `$(go env GOPATH)/bin` (CI). That build needs
Go 1.26.9+ because `go.mod` is 1.26.9.

| Tool | Scripts | Installed by |
| ------ | --------- | -------------- |
| **podman** | `image-build.sh`, `image-push.sh` | OS package (`dnf`/`apt`). Default engine. |
| `yamllint` | `lint-yaml.sh` | `pip install --user yamllint` |
| `markdownlint-cli2` | `lint-md.sh` | `npm install -g --prefix ~/.local` |
| `golangci-lint` | `lint-go.sh` | GitHub release tarball → `~/.local/bin` or `GOPATH/bin`. Not Docker Hub. |
| `govulncheck` | `supply-chain.sh` | `go install …@latest` |
| `gosec` | `supply-chain.sh` | GitHub release tarball (`GOSEC_VERSION`). Not `go install`. |
| `trivy` | `supply-chain.sh`, `image-scan-trivy.sh` | GitHub release tarball → `~/.local/bin` |
| `dive` | `image-scan-dive.sh` | GitHub release tarball → `~/.local/bin` |
| `hadolint` | `image-lint.sh` | GitHub release binary (`HADOLINT_VERSION`) → `~/.local/bin` |
| `go`, `git`, `gofmt` | `lint-go.sh`, `unittest.sh`, `build.sh`, … | not installed here |

Optional: `gitleaks` (secret-scan), `pre-commit` (yamllint + markdownlint +
golangci-lint + secret-scan + version-lock). Hadolint runs from the pinned
`HADOLINT_VERSION` release binary locally and from the pinned `CI_TOOLS_IMAGE`
container in CI through `image-lint.sh`; it is not part of the Trivy scan.
GitHub Actions image jobs and local runs use Podman via
`CONTAINER_ENGINE=podman`. Podman is the preferred and supported container
engine for local image builds, scans, tests, and GitHub publication.

## Local

```sh
pre-commit install          # yaml + md + go + secret-scan + version-lock → logs/pre-commit-*.log
./ci/scripts/install-tools.sh       # one-time: yamllint, markdownlint, golangci-lint, …
make lint                   # lint-yaml (+ layout-check), lint-md, lint-go
make lint-actions           # actionlint for GitHub Actions workflows
make test                   # unittest, secret-scan, supply-chain
make build                  # bin/harness + dist/binary-version.txt
make unittest               # go test ./... only
./ci/scripts/ci.sh          # lint + test stages, then replay-smoke
./ci/scripts/ci.sh --image  # also build + contents-check + Trivy + Dive with podman (sequential)
make ci-image               # same as ci.sh --image
make image-lint             # Hadolint Containerfile check (non-gating in CI)
make image-scan-trivy       # Trivy only (after make image-build)
make image-scan-dive        # Dive only
make set-next-version BINARY=1.0.0   # set next binary version (see Versioning)
make set-next-version IMAGE=2.0.0    # set next image version
```

Hooks run only when staged files match `.pre-commit-config.yaml` (`files:`).
The GitHub Actions `supply-chain` job is gating.

Logs: [`logs/`](../logs/README.md) (gitignored except `logs/README.md`). GitHub
uploads `logs/*.log` when a job fails (not the README).

## Scripts

| Script | Role |
| -------- | ------ |
| `install-tools.sh` | Local install of lint/scan tools + podman (idempotent; `--check`) |
| `ci-utils.sh` | Shared helpers: `REPO_ROOT`, `CI_SCRIPTS_DIR`, `CI_CONFIG_DIR`, version functions, `container_engine`, logging |
| `images.sh` | Load `ci/config/images.env`, derive tool refs |
| `sync-images-yml.sh` | Project `images.env` → generated `images.yml`; `--check` from lint-yaml |
| `layout-check.sh` | Verify reorder layout: scripts, configs, version files, no leftovers |
| `lint-yaml.sh` | yamllint + images.yml/Containerfile pin check + layout-check |
| `lint-actions.sh` | actionlint validation for `.github/workflows/*.yml` |
| `lint-md.sh` | markdownlint-cli2 (config from `ci/config/markdownlint-cli2.jsonc`) |
| `lint-go.sh` | gofmt, go vet, golangci-lint (config from `ci/config/golangci.yml`) |
| `unittest.sh` | **Unit tests only** (`go test ./...`). |
| `build.sh` | `bin/harness` with `binary_version()` ldflags |
| `secret-scan.sh` | tracked reports (`report.json`/`.md`); editor/workspace tokens; gitleaks |
| `supply-chain.sh` | vendor/`go list`, govulncheck, gosec, `trivy fs`. **Gating on GitHub Actions.** |
| `replay-smoke.sh` | `harness validate` + `run` with example catalog/plan (no cluster). GitHub: skipped (opt-in via `CI_RUN_REPLAY_SMOKE`). |
| `image-build.sh` | `linux/amd64` Containerfile → `dist/harness-image.tar` (copies tools from digest-pinned `BUILD_IMAGE`). Local: **podman**. |
| `image-contents-check.sh` | Verify `harness`, cert tool binaries, virtbench fio, and `examples/vm-templates/rhel9-vm-datasource.yaml` |
| `image-lint.sh` | Native Hadolint check locally; pinned `CI_TOOLS_IMAGE` check in CI; reports privileged `USER 0` setup context. |
| `image-scan-trivy.sh` | Trivy HIGH/CRITICAL `--ignore-unfixed`, secrets, misconfig, CycloneDX SBOM. |
| `image-scan-dive.sh` | `CI=true dive` (wasted layers). |
| `image-push.sh` | Quay push; requires `PUSH=1`. GitHub: **automatic** on `main` push (or `workflow_dispatch` with `publish=true`). |
| `mirror-ci-tools.sh` | Retag Trivy/Dive/Hadolint into `quay.io/virtarraycert/ci_tools`; run locally with Quay push access. |
| `set-next-version.sh` | Set next binary/image version (`--binary`, `--image`, `--init`, `--self-test`) |
| `advance-version.sh` | Auto-advance both tracks on main push (CI only) |
| `partner-harness-common.sh` | Shared partner helpers (sourced): image default, backends/cluster validation, log and result archiving |
| `run-partner-preflight.sh` | Partner preflight via published image (`make partner-preflight`) |
| `run-partner-cert.sh` | Partner Level 3 load-80 or no-load-80 run (`make partner-cert-*`) |
| `run-partner-package-results.sh` | `PARTNER_PACKAGE_MODE=level3\|smoke\|all`; default tarballs `certification-results-level3.tar.gz` or `-smoke.tar.gz` |
| `run-image-tests.sh` | Live smoke with **local** image ref (`make run-harness-smoke`); holds `work/smoke-run.lock` |
| `run-quay-image-tests.sh` | Pull and smoke **published** image against `work/` (`make run-quay-image-tests`) |

The **release artifact** is that image ([ADR-0011](../decisions/0011-harness-ships-as-container-image.md)),
not the `bin/harness` binary. Registry:
`quay.io/virtarraycert/storage-cert-harness:<image_version>` and `:main` on the
default branch. The `build` job produces `bin/harness` as a debug/dev artifact.

On **GitHub**, `image-lint` runs before `image-build` with
`continue-on-error: true`; Trivy and Dive run after the build with
`continue-on-error: true`. Run `make image-lint` or `make image-scan` locally
for the corresponding checks and report paths.

## Secret scan

SLA thresholds are public (ADR-0017), so the scan no longer refuses
`thresholds.json` or hunts for SLA-like numeric literals in code/plans/fixtures.
It now has two gates:

- **Tracked reports:** fails if git would contain a harness `report.json` /
  `report.md` (they can carry graded numbers).
- **Editor/workspace tokens:** fails on tracked VS Code / `*.code-workspace` /
  `.cursor` / `.devcontainer` / `.env` files with API key or token env vars
  holding real-looking values (`glpat-`, `ghp_`, `sk-`, `AKIA`, JWT, …).

If installed, `gitleaks` also runs over the working tree.

## Testing

### Local test execution flow

```sh
./ci/scripts/install-tools.sh
make lint          # layout-check via lint-yaml
make test
make build         # bin/harness + version assert
make ci            # lint + test + build + replay-smoke
make ci-image      # image-build (linux/amd64 + contents-check) + trivy + dive
```

Scratch/logs stay gitignored (`/bin/`, `/dist/`, `/logs/**`, `/ci-local/`).
Partner and smoke wrappers write transcripts under `logs/` (see
[logs/README.md](../logs/README.md)); only `logs/README.md` is tracked.
Self-tests use `mktemp`.

On GitHub, `image-lint`, `image-scan-trivy`, and `image-scan-dive` run with
`continue-on-error: true` and do not block publish. `image-lint` completes
before `image-build`.

### GitHub `main` pipeline (publish)

| Job | When | Asserts |
|-----|------|---------|
| `image-build` | every `main` push | `linux/amd64` tar uploaded as `harness-image` artifact |
| `image-push` | after `image-build` succeeds | `image-push.sh` with `PUSH=1` → Quay (`:<version>-amd64`, `:<version>`, `:main`, `:latest`) |
| `version-bump` | after `image-push` succeeds | `advance-version.sh` → automated PR on `chore/version-bump` |

`image-push` is **automatic** on `main` pushes. A `workflow_dispatch` with
`publish=true` can also trigger push and version bump without a new commit.
Neither MR/PR runs nor non-main pushes publish an image.

## Open items

### Supply-chain job

GitHub Actions fails the workflow when `supply-chain` fails.

### Replay-smoke job

GitHub skips the `smoke` job unless `vars.CI_RUN_REPLAY_SMOKE=true`; image build
and publish do not depend on it. Run `./ci/scripts/replay-smoke.sh` locally
when needed.

### Image lint and scan jobs (non-blocking)

GitHub runs `image-lint` before `image-build`, then both image scans, with
`continue-on-error: true`; `image-build` alone gates `publish`. GitHub lint and
scan logs and reports are uploaded as job artifacts, but no Code scanning
(SARIF) results are produced. Use `make image-lint` or
`make image-build && make image-scan` locally when you need the corresponding
output.

### Dedicated CI cluster (live smoke)

Workflow [ci-live-smoke.yml](../.github/workflows/ci-live-smoke.yml) is **off**
until repository variable `CI_LIVE_SMOKE_ENABLED` is set to `true` and the
`live-smoke` environment has cluster secrets. Until then, use local
`make run-harness-smoke`.

Today's other jobs use hosted GitHub runners (no cluster). The **live-smoke** job
(planned; see design doc below) runs against a real OpenShift cluster using the
harness smoke plan. It will use GitHub environment secrets for
`CI_CLUSTER_KUBECONFIG` and `HARNESS_THRESHOLDS_JSON`. Never echo them (`set +x`);
write kubeconfig to a `0600` temp file and delete it in `trap`. Do not artifact
full graded reports that contain real SLA bars.

Design: [storage-cert-harness-ci-smoke-test.md](design/storage-cert-harness-ci-smoke-test.md)
(in this repository).

**What live smoke affects.** When implemented, the live-smoke workflow runs on PR
open (and on demand). A **failed** smoke run is a **merge gate**: branch
protection should require a passing live-smoke check before merging to `main`.
Investigate failures and re-run smoke after fixes. Lint, unit test, build, image
scan, and publish jobs are unchanged; only merge is blocked on smoke failure.

#### Skip live smoke (`[skip live-smoke]`)

Full-catalog live smoke can add hours to a PR pipeline. To skip **scheduling**
live-cluster smoke (for example lab outage), include this token in the **PR title
or description** (or merge commit message on squash):

```text
[skip live-smoke]
```

| Rule | Detail |
|------|--------|
| Case sensitivity | The token is **case-sensitive**. `[Skip live-smoke]` does **not** skip. |
| Squash merges | GitHub uses the PR title and description for the squash commit message — put `[skip live-smoke]` in the PR title or description when you need a skipped smoke on merge. |
| Without token | Live smoke runs on PR open when path filters match (see design doc). |
| Failed smoke job | **Blocks PR merge** until a passing re-run; skip token does not override a failed run |
| Cluster lease | Skipped runs must not acquire the cluster lease. |
| CI behavior (when implemented) | Log that smoke was skipped; GitHub check conclusion **Skipped** (not Success); workflow summary includes SHA, actor, token, and `live_smoke_skipped=true` for audit and follow-up (nightly or manual smoke). |

Do **not** use `[skip ci]` for this — it is too broad. Skipping smoke should be
exceptional; note the reason in the PR. Optional policy: disallow skip when
image-changing paths are touched unless a maintainer approves (see design doc).

Implementation tracking: [ECOPROJECT-5536](https://redhat.atlassian.net/browse/ECOPROJECT-5536).

#### Forks and cluster secrets

Live-cluster smoke is **upstream-only**. A GitHub **fork** must not trigger live
smoke or receive the upstream kubeconfig.

| Scenario | Live smoke | Cluster secrets (`CI_CLUSTER_KUBECONFIG`, etc.) |
|----------|------------|--------------------------------------------------|
| Pull request **from a fork** into upstream | Not run (live-smoke workflow has no `pull_request` trigger) | Not passed to fork PR workflows ([GitHub fork PR secret rules](https://docs.github.com/en/actions/security-for-github-actions/security-guides/using-secrets-in-github-actions#accessing-your-secrets-in-forked-pull-requests)) |
| Push to **fork** `main` | Not run — job requires `github.repository == 'openshift-storage-cert/storage-cert-harness'` | Upstream secrets are **not** available on the fork; only secrets the fork owner configures locally |
| Push to **upstream** `main` | Runs when path filters match (unless `[skip live-smoke]`) | Only via GitHub **environment** `live-smoke` on the canonical repo |

**Required when implementing live smoke:**

1. Dedicated workflow with `on.push.branches: [main]` and `workflow_dispatch` only
   — never `pull_request` or `pull_request_target` for cluster access.
2. Store kubeconfig, thresholds, and CI backends as **environment secrets** on
   `live-smoke`, deployment branch `main` on upstream — not shared via broad
   `secrets: inherit` on the default CI workflow.
3. Job `if` includes the canonical repository guard (update the org/repo string if
   the GitHub home changes).
4. Self-hosted runners with cluster network access are registered on the **upstream**
   organization only.

Standard `CI` on fork PRs (lint, test, build) continues without cluster credentials.
See the design doc section **Forks and untrusted repositories** in
[storage-cert-harness-ci-smoke-test.md](design/storage-cert-harness-ci-smoke-test.md).

### AI agent files must not go to public GitHub

`AGENTS.md`, `CLAUDE.md`, and `skills/` are AI instruction surfaces. On a public
mirror they can be edited to mis-instruct agents. They stay on **internal
GitLab**. `.gitattributes` marks them `export-ignore`. GitHub migration must
publish a tree **without** those paths and fail CI if they reappear. Public
contributors use this file, [contributing.md](contributing.md), and
[adapter-authoring.md](adapter-authoring.md).
