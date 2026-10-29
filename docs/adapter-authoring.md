# Authoring a tool integration

A tool integration is a Go package under `internal/tools/<tool>/` that registers
a `ToolIntegration`. This is designed so a tool decomposes into several small,
independently-ownable tasks — great for parallel work.

## Steps

1. **Copy the reference.** `cp -r internal/tools/example internal/tools/<tool>`
   and rename the package.

2. **Declare the integration** in `init()`:

   ```go
   registry.Register(stages.ToolIntegration{
       Name:     "<tool>",
       Image:    "quay.io/<vendor>/<tool>:<tag>", // the tool's RELEASED image
       Provides: []string{"<TR-id>", ...},        // TR ids from catalog.json
       Provisioner:  provisioner{},
       Runner:       runner{},
       LogCollector: collector{},
       ResultParser: parser{},
       Teardown:     teardown{},
       // Optional: Preflight, Evaluators (custom per-TR scoring),
       // ParallelSafe + ExclusivityGroups (scheduling defaults).
   })
   ```

3. **Implement only the stages you need.** Each is a small interface (see
   `internal/stages`). Reuse shared adapters where they exist. Every method gets
   `context.Context` (honor cancellation) and a `*core.Bag`.

4. **Use the Bag for cross-stage state** and document its keys at the top of the
   package (see the `example` package header):
   - Provision writes what Run/Collect/Teardown need (namespace, release name…).
   - Read with `core.GetAs[string](bag, "namespace")`.

   For the active storage array, read `rc.Backend` (a `*core.ResolvedBackend`,
   may be nil): `rc.Backend.StorageClass`, `rc.Backend.Param("svm")`, and
   `rc.Backend.Secret("password")`. **Never log or serialize secret values.** If
   your tool requires a backend, assert it in `Preflight` and emit an `error`
   finding when it is missing.

   Return one `core.Finding` for every preflight check: `Level: "info"` for
   success, `"error"` for failure, or `"skip"` with a reason when the check did
   not run. Continue independent checks after a failure; report dependent
   checks as skipped when a prerequisite is missing. The CLI renders these as
   PASS / FAIL / SKIP and includes all results in its closing tally. Legacy
   `"warn"` findings render as non-fatal SKIP results.

5. **Write a golden-file test for the parser.** Put a captured sample of the
   tool's output under `testdata/`, parse it, and compare to a golden file
   (`example_test.go` is the template; `go test ./... -update` regenerates). This
   needs no cluster.

6. **Emit results the grader can score.** Your `ResultParser` returns one
   `TestResult` per TR (keyed by `TRID`):
   - For **SLAs**: append `core.Metric{Name, Value, Unit, Percentile}` — the
     grader matches each `sla` by metric name (and percentile when set) and
     compares against the thresholds value. You emit measurements only; the bar
     lives in `thresholds.json`.
   - For **checks**: set `TestResult.Checks[<checkID>]` to the tool's native
     `Outcome` for each `capability`/`suite`/`scenario` check. `manual` checks
     are never yours to score.
   - When a TR's scoring doesn't reduce to these rules, add an `Evaluator` to
     the integration's `Evaluators` map keyed by that TR id — it fully owns the
     TR's verdicts.

7. **Compile it in.** Add one blank import to `cmd/harness/main.go`:

   ```go
   _ "github.com/openshift-storage-cert/storage-cert-harness/internal/tools/<tool>"
   ```

8. **Verify:** `make unittest && ./bin/harness list` should show your tool and
   `run --tool <tool>` should exercise it.

## Runtime template layout

Keep harness-owned source YAML under `internal/tools/<tool>/templates/<TR-ID>/`.
Use `templates/shared/` for resources used by several TRs, such as Virtbench's
SSH helper pod. Keep the embed declarations together in the adapter's
`templates.go`; test fixtures stay under `testdata/`.

```text
internal/tools/<tool>/
├── templates.go
├── templates/
│   ├── <TR-ID>/
│   │   └── <template>.yaml
│   └── shared/
│       └── <template>.yaml
└── testdata/
```

Source templates are embedded in the binary and remain unchanged during runs.
Stage generated files in the run's working directory using the filenames and
relative paths required by the external tool. For example, kube-burner's
`templates/TR-VIRT-010/config.yaml` becomes `config.yaml`, while its object
templates become `templates/*.yaml` in the run directory.

Adapters without harness-owned templates document that in `templates/README.md`
and need no `templates.go`. Keep upstream-owned templates in their released
runtime; do not copy them into the harness merely to populate a TR directory.
User-supplied template paths remain external inputs.

## Kube-burner workload templates

Kube-burner keeps native rendering. `manifest.Native` passes source bytes through
unchanged, while `snapshotJobInputs` supplies job values as JSON in `SNAPSHOT_JOBS`.
The config template uses `fromJson` and `range` to construct legacy or batched
snapshot jobs, including a final partial batch. Raw template YAML is excluded
from yamllint; tests validate rendered configuration and preserve native sources.

## Kube-burner storage setup

Private StorageClass and StorageProfile setup is owned by the harness and uses
`manifest.GoTemplate`, independently of kube-burner's native workload rendering.
Templates live under `templates/TR-VIRT-010/setup/`. They preserve provisioning
fields and claim defaults while replacing metadata and omitting copied status.
The created StorageClass name is retained on later failure so cleanup can remove
it without modifying the backend's original resources. Tests cover preservation,
omitted defaults, and cleanup after partial failure.

## Virtbench command manifests

Clone, disk-ops, and FIO resolve explicit template values before launching
Virtbench. The shared Go/Sprig renderer validates YAML and the tool validates
VM identity and disk counts. Argument builders select sources without copying
them; preparation writes a private rendered file once. Legacy upstream tokens
use aliases in the same engine. Templates are discovered from the runtime
checkout or the installed CLI's Python environment.

The final clone command omits `--storage-class` after rendering so upstream
cannot overwrite fixed classes or reject PVC-form storage definitions. FIO
retains upstream's run-all lifecycle and harness cleanup. Template tests cover
source preservation, invalid inputs, native defaults, and the process boundary.

## Multi-scenario tools = a scenario table

Some tools expose several subcommands, each mapping to a different TR and (often)
a different output shape — virtbench is the reference (`internal/tools/virtbench`,
ADR-0008). Don't copy the package per subcommand. Instead:

- Keep one generic `engine.go` with the stage plumbing (preflight, provision,
  exec, collect, teardown, replay), parameterized over a `Scenario`.
- Put a `Scenario` table in `scenario.go`: each row carries the
  `automation_tool` string (the registry key), the TR it provides, the result
  file, a `BuildArgs` closure, and a **pluggable `Parse` closure**. `init()`
  loops the table and registers one `ToolIntegration` per row.
- A metric-map scenario is a new row + fixture; a differently-shaped output is a
  new row + a new `Parse` closure. If either forces an `engine.go` edit, that's
  the signal the engine isn't generic enough yet — generalize it and note what
  leaked.

**Units:** emit each metric in whatever unit the tool reports (e.g. seconds); the
grader normalizes units before comparing to the gate (ADR-0008), so you never
convert to the SLA's unit yourself.

**SLAs your run can't measure** (a percentile the tool doesn't emit, a metric
from another subsystem, a scale you didn't run): declare them via a per-scenario
`Applicable(sla)` predicate consulted in the tool's `Evaluator`, which skips them
with a reason and delegates the rest to `grader.GradeSLAs`/`grader.GradeChecks`.
This yields an explained `skip` instead of a spurious `error`, and keeps the core
grader tool-agnostic.

## Setup that's shared across tests (run / group scope)

The `Provisioner` stage runs once per tool-job (individual scope). For setup that
must happen **once per run** or **once for a batch of tests that share it**, use a
`stages.SetupProvider` instead of repeating work per job (ADR-0009):

- Implement `SetupInfo() {Scope, Key}`, `Setup(ctx, rc, trs)`,
  `Teardown(ctx, rc, trs)`. Scope is `stages.ScopeRun` (once per run) or
  `stages.ScopeGroup` (once per named group).
- `registry.RegisterSetup(&yourSetup{})` in `init()` (dedup is by `(scope,key)`,
  so registering the same group from several tool packages is safe).
- Each tool that depends on a group lists it in
  `ToolIntegration.SetupGroups: []string{"<key>"}`.
- The orchestrator runs `Setup` once (run → group) before the job loop and
  `Teardown` once in reverse after all jobs; a failed `Setup` errors its in-scope
  TRs instead of running them. Make `Setup` idempotent (create-if-absent). Roll
  back resources your `Setup` created when `Setup` returns an error (teardown is
  not called for failed providers). In `Teardown`, delete only what your provider
  owns unless the resource is explicitly shared and documented (see virtbench).

virtbench is the reference: all its scenarios share one `ssh-test-pod` helper via
a group provider (`internal/tools/virtbench/setup.go`). A batch reuses the helper
when present, stamps harness run metadata on it, and **always deletes**
`default/ssh-test-pod` at successful group teardown—even when this run did not
create it—so orphans from crashed runs are cleared. That policy can delete a pod
another concurrent harness run still expects; the provider logs warnings when it
detects a different `run-id` annotation. Failed setup deletes a pod this setup
just created. Each live scenario's provisioner rechecks that the helper is
Running with `sshpass` available, recreating it if a preceding node drain removed
it. Terminating pods are allowed to disappear before recreation; completed or
failed pods are replaced. Skip shared setup and teardown only when **every** TR
in the provider's batch is replay (`results_dir`); mixed replay/live batches
still touch the cluster.

To bump the Quay `ssh-helper` image pin, see
[`docs/ssh-helper-image.md`](ssh-helper-image.md).

The opt-in `TestSSHHelperAfterNodeDrain` regression (`-tags=integration`) runs
the real drain → clone-at-scale → boot-storm sequence. Set
`VIRTBENCH_LIVE_STORAGE_CLASS` and `VIRTBENCH_LIVE_WORKDIR`, with `KUBECONFIG`
pointing to a test cluster and the released Virtbench runtime available. It
drains the worker hosting the helper, so use a cluster reserved for testing.
Run with `-run '^TestSSHHelperAfterNodeDrain$' -count=3 -timeout=90m` to check
repeated recovery; ordinary unit tests do not contact a cluster.

## When there's no released image

Add a `Containerfile` to the tool package and set `Image` to where you publish
it. This is the exception, not the rule (ADR-0003).
