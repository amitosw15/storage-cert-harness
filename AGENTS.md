# AGENTS.md — storage-cert-harness

**Internal only.** This file, `CLAUDE.md`, and `skills/` must not be published
on public GitHub (tampering / jailbreak risk). See [docs/ci.md](docs/ci.md).
GitLab keeps them for Red Hat developers.

Tool-agnostic project instructions (Claude, Cursor, Gemini, Copilot). `CLAUDE.md`
is a thin wrapper that loads this file.

## Project overview

The **storage certification test harness** orchestrates upstream storage/virt
test tools against an OpenShift cluster, grades results against per-Test-
Requirement SLA gates and functional checks, and emits a certification report.
Written in **Go** (single static binary, shipped as a container). It consumes
the KB export **v2.0 two-file contract** — `catalog.json` (publishable) +
`thresholds.json` (sensitive), joined on TR id (ADR-0006). See `decisions/` for
the "why".

- Module: `github.com/openshift-storage-cert/storage-cert-harness`
  (renamed on the GitHub migration — ADR-0002).
- Jira: ECOPROJECT (5273 KB integration, 5328 language, 5329 repo, 5331 first e2e).

## Critical rules

1. **SLA thresholds are public (ADR-0017).** Real `thresholds.json` may be
   committed alongside `catalog.json`; gate numbers in code, plans, fixtures,
   and tests are no longer secret and are no longer scanned. (Historical:
   ADR-0004 treated them as encrypted-in-repo secrets; ADR-0017 supersedes it.)
   Backend credentials remain **references** (file / k8s Secret /
   inline-dev-only), never committed plaintext; only `backends.example.yaml`
   with fake values is tracked (ADR-0005). Resolved secret values live only in
   memory and are never logged or written to the report.
   CI **secret-scan** now covers only run reports and token-shaped credentials:
   it refuses tracked `report.json` / `report.md` (graded numbers) and flags
   `glpat-`/`ghp_`/`sk-`/`AKIA`/JWT-shaped values in editor/workspace files.
2. **The core never imports a concrete tool.** `internal/{orchestrator,grader,
   report,registry}` depend only on the stage interfaces in `internal/stages`.
   Tools live under `internal/tools/<tool>/` and register themselves.
3. **Run tools from their released images**, don't recompile them (ADR-0003). A
   `Containerfile` in a tool package is a fallback only when no image exists.
4. AI-assisted commits carry an `Assisted-by:` trailer.

## Dev environment

- Go 1.26.9+. No cluster needed for the skeleton: the `example` tool runs offline.
- `./ci/scripts/install-tools.sh` then `make build` → `bin/harness`; `make unittest`
  (unit tests) or `make test` (GitLab test stage: unittest, secret-scan,
  supply-chain); `make ci` / `./ci/scripts/ci.sh`. Image build/run uses **podman** by
  default.
- Offline Go: `GOPROXY=off`, committed `vendor/`. CI map: [docs/ci.md](docs/ci.md).

## Repository structure

| Path | Purpose |
|------|---------|
| `cmd/harness/` | CLI (`run`, `preflight`, `validate`, `list`, `version`); blank-imports tools |
| `internal/core/` | Domain types (`TestRequirement`, `SLA`, `Check`, `Verdict`) + `Bag` |
| `internal/stages/` | The adapter "ports" + `ToolIntegration` |
| `internal/registry/` | In-tree tool registry |
| `internal/orchestrator/` | TR selection, scheduling, pipeline driver |
| `internal/grader/` | Per-item scoring (sla + checks) + custom evaluators |
| `internal/{catalog,thresholds}/` | Load/validate the v2.0 KB files (JSON) |
| `internal/kb/` | Join catalog + thresholds on TR id (provenance/commit check) |
| `internal/plan/` | Test plans (YAML): select/parameterize catalog tests |
| `internal/backend/` | Storage backends (YAML) + credential-ref resolution |
| `internal/report/` | JSON/Markdown renderers |
| `internal/tools/example/` | **Reference integration — copy to add a tool** |
| `plans/` | Pre-made test plans (YAML) |
| `schemas/` | JSON Schemas (v2.0) for catalog, thresholds, report, plan, backends |
| `decisions/` | ADRs (`NNNN-*.md`, `template.md`) |
| `docs/` | Architecture + adapter-authoring + contributing + CI |
| `secrets/` | Obsolete encrypted SLA folder (ADR-0004, superseded by ADR-0017) |
| `ci/scripts/` | Portable CI job scripts (`ci-utils.sh`, `build.sh`, `lint-*.sh`, …) |
| `ci/config/` | Linter configs, image pins, allowlists (`images.env`, `golangci.yml`, …) |
| `VERSION` | Last binary version released from main |
| `NEXT-VERSION` | Next binary version (bumped on main push) |
| `IMAGE-VERSION` | Last container image version released from main |
| `NEXT-IMAGE-VERSION` | Next image version (bumped on main push) |

## Build and test

- `make build` / `make unittest` / `make vet` / `make cover`. `make lint` /
  `make test` match the GitLab lint and test stages.
- Every `ResultParser` gets a **golden-file test** (see `internal/tools/example/
  example_test.go`) — cluster-free, so parser work is a small parallel task.
- Version: `make set-next-version BINARY=x.y.z` or `IMAGE=x.y.z`;
  see [docs/ci.md Versioning](docs/ci.md#versioning).

## Architecture (see ADR-0003)

Ports-and-adapters. A run: load `catalog.json` + `thresholds.json` → `kb.Join`
(verify shared `kb_git_commit`, 1:1 id parity) → select TRs by
`partner_level`/`tool`/`id` → group by `automation_tool` → per tool drive
`Preflight → Provision → Run → Collect → Parse` → grade each TR into one verdict
per SLA and per check → report. Scoring skips `sla.value == null`,
`status == to-be-validated`, and `kind == manual`; TRs with no registered
automation tool skip as not-scorable. SLA comparison is **unit-normalized**: the
measured value and the gate are converted to a common base unit before comparing
(so a `min`/`h`/`ms` gate grades correctly against a seconds-valued metric), with
original units shown in the report (ADR-0008). A tool that cannot measure a given
SLA scores it as a clean `skip` via a custom `Evaluator`, keeping the core grader
tool-agnostic. Every stage takes `context.Context`
(cancel) and a `*core.Bag` (state). Scheduling honors `--concurrency` plus
per-tool `parallel_safe` / `exclusivity_groups` (a `ToolIntegration` default,
overridable by a plan's `scheduling:` block). See ADR-0006.

## Adding a tool

See `docs/adapter-authoring.md`. Short version: copy `internal/tools/example/`,
implement the stages you need, declare `Image` + `Provides`, `registry.Register`
in `init()`, add a golden test, and blank-import the package in
`cmd/harness/main.go`.

## Decision records

Architectural decisions go in `decisions/NNNN-title.md` using `template.md`.
Propose → review via PR → mark Accepted. Current: 0001 Go, 0002 repo+SDLC,
0003 architecture, 0004 secret handling, 0005 test plans + backend config,
0006 KB export v2.0 two-file contract, 0007 virtbench local-exec + replay mode,
0008 multi-scenario tool table + unit-normalized grading, 0009 scoped setup
(run/group/individual), 0010 CI scripts (GitLab + GitHub + pre-commit),
0011 harness ships as a container image (Quay), 0012 kube-burner generic
config + snapshot tool, 0013 virtbench node-drain / VM-evacuation (TR-VIRT-008),
0014 report model + exporters, 0015 continue-from-merge + attestations,
0016 named plan variants, 0017 thresholds are public (supersedes 0004).
