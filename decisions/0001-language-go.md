# 0001. Harness implementation language: Go

- **Status:** Accepted
- **Date:** 2026-08-23

## Context

The harness orchestrates mostly Go storage/virtualization tools, uses the
Kubernetes API, and must be easy to distribute to partners.

## Decision

Use **Go**, producing a single static binary. Use `client-go` for in-process
Kubernetes access and released images for upstream tools; do not vendor their
source into the harness ([ADR-0003](0003-harness-architecture-ports-and-adapters.md)).

The module starts as
`gitlab.cee.redhat.com/eco-special-projects/storage-cert-harness`; rename it at
the public GitHub migration ([ADR-0002](0002-repository-host-and-sdlc-workflow.md)).

## Decision notes

### Options considered

- **Go (chosen):** matches upstream tools and produces a static binary; adds
  boilerplate and team ramp-up.
- **Python:** faster initial development, but needs an interpreter and Go tools.
- **Hybrid:** adds a second toolchain without enough benefit.

### Consequences

Go enables a static harness binary without a Python interpreter.

Use Go tests, golden parser fixtures, and Go linting. CLI framework, client
versions, and build tooling are implementation choices. Container delivery is
covered by [ADR-0011](0011-harness-ships-as-container-image.md).

Related: [ECOPROJECT-5328](https://redhat.atlassian.net/browse/ECOPROJECT-5328).
