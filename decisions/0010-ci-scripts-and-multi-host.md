# 0010. CI logic lives in portable ci/ scripts

- **Status:** Accepted
- **Date:** 2026-08-25

## Context

Local development, GitHub Actions, and pre-commit need the same checks,
including diff-based secret scanning. Makefile-only logic is awkward for hooks
and specialized job images.

## Decision

- **`ci/scripts/` owns CI behavior:** build, unit tests, lint, secret scanning,
  supply-chain checks, replay smoke, and image build/scan/push. Makefile, GitHub,
  and pre-commit are thin wrappers. This supersedes ADR-0002's
  Makefile-owned logic.
- **Offline Go:** commit `vendor/`; use `GOPROXY=off` and `GOFLAGS=-mod=vendor`.
- **Image release gates:** run Trivy and Dive as parallel CI jobs; push to Quay
  only after both pass. [ADR-0011](0011-harness-ships-as-container-image.md)
  defines the release artifact.
- **Internal instruction files:** keep `AGENTS.md`, `CLAUDE.md`, and `skills/`
  out of public release archives with `export-ignore` when those paths exist in
  the tree. This attribute applies to archives; a normal Git push does not filter history.

## Decision notes

### Options considered

- **Portable scripts (chosen):** one implementation across hosts; more files.
- **Makefile-owned logic:** fewer files, but awkward for hooks and job images.
- **Duplicated CI YAML logic:** creates competing sources of truth.

### Consequences

Scripts check required commands and run in different job images. Local image
work uses Podman (`CONTAINER_ENGINE`); GitHub Actions image jobs use Podman.

Contributors run `./ci/scripts/install-tools.sh`, then `make ci` or
`./ci/scripts/ci.sh`. See [CI documentation](../docs/ci.md) for commands/config.

Live cluster smoke requires separate runner/protected-kubeconfig work
(ECOPROJECT-5331). Encryption tooling, Cosign, and signed SBOM publication are
outside this decision.

Related: [ECOPROJECT-5274](https://redhat.atlassian.net/browse/ECOPROJECT-5274),
[ADR-0002](0002-repository-host-and-sdlc-workflow.md).
