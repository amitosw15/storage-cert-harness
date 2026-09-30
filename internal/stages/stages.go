// Package stages defines the harness "ports" — the small, single-purpose
// adapter interfaces a test tool implements — and the ToolIntegration that
// binds a set of them together. The core drives these interfaces and never
// imports a concrete tool. See decisions/0003 and 0006.
//
// Every stage method takes context.Context first (cancellation, deadlines,
// abort-on-failure) and a *core.Bag for stage-to-stage state. Stages operate on
// the TestRequirements (TRs) selected for the tool.
package stages

import (
	"context"

	"gitlab.cee.redhat.com/eco-special-projects/storage-cert-harness/internal/core"
)

// RunHandle identifies an in-flight or completed run of a tool, so a
// LogCollector knows what to collect.
type RunHandle struct {
	ID string
}

// Preflight checks prerequisites before anything is provisioned.
// Return one finding for every check, including successes and skips (with a
// reason). Continue independent checks after failures. An error may accompany
// partial findings when the remaining checks cannot be completed.
type Preflight interface {
	Check(ctx context.Context, rc *core.RunCtx, bag *core.Bag, trs []core.TestRequirement) ([]core.Finding, error)
}

// Provisioner is the input/setup adapter: create namespaces, apply manifests,
// deploy the tool's released image, etc.
type Provisioner interface {
	Provision(ctx context.Context, rc *core.RunCtx, bag *core.Bag, trs []core.TestRequirement) error
}

// Runner drives the tool to execute the TRs (typically by launching the tool's
// released container image as a Job/Pod).
type Runner interface {
	Run(ctx context.Context, rc *core.RunCtx, bag *core.Bag, trs []core.TestRequirement) (RunHandle, error)
}

// LogCollector is the log-ingest adapter.
type LogCollector interface {
	Collect(ctx context.Context, rc *core.RunCtx, bag *core.Bag, h RunHandle) (core.LogBundle, error)
}

// ResultParser is the output-ingest adapter: turn raw logs/artifacts into
// normalized TestResults.
type ResultParser interface {
	Parse(ctx context.Context, rc *core.RunCtx, bag *core.Bag, logs core.LogBundle) ([]core.TestResult, error)
}

// Teardown cleans up everything Provision created.
type Teardown interface {
	Teardown(ctx context.Context, rc *core.RunCtx, bag *core.Bag) error
}

// Evaluator fully scores one TR from its TestResult, returning a Verdict per
// scorable item. The core grader handles standard sla/checks; a ToolIntegration
// supplies a custom Evaluator (keyed by TR id) for TRs whose scoring the tool
// owns (complex suite/scenario adjudication). See decisions/0006.
type Evaluator interface {
	Evaluate(ctx context.Context, tr core.TestRequirement, res core.TestResult) ([]core.Verdict, error)
}

// ToolIntegration binds a named set of stage adapters into one pluggable tool.
// Any field may reuse a shared/generic adapter. Image is the tool's released
// container image (overridable); we run that image rather than compiling the
// tool into the harness. See decisions/0003.
type ToolIntegration struct {
	Name         string
	Image        string
	Provides     []string // TR ids this integration can run
	Preflight    Preflight
	Provisioner  Provisioner
	Runner       Runner
	LogCollector LogCollector
	ResultParser ResultParser
	Teardown     Teardown

	// Scheduling defaults (a plan may override per run — see config.ToolSchedule).
	// ParallelSafe true lets this tool's job overlap other parallel-safe jobs.
	// ExclusivityGroups name resources; jobs sharing a group never overlap.
	ParallelSafe      bool
	ExclusivityGroups []string

	// Evaluators supplies custom per-TR evaluators keyed by TR id. Optional.
	Evaluators map[string]Evaluator

	// SetupGroups names the group-scoped SetupProviders (by Key) whose setup this
	// tool's tests depend on. The orchestrator runs each referenced group's setup
	// once before any job and tears it down once after all jobs, so several tools
	// share expensive setup (e.g. a helper pod, a golden base image) instead of
	// repeating it per job. Optional. See SetupProvider and decisions/0008.
	SetupGroups []string
}

// SetupScope identifies when a SetupProvider runs relative to the per-tool job
// loop. Setup has three tiers: run (once per harness run), group (once per named
// group per run — the batching tier), and individual (the per-tool Provisioner
// stage, which already runs once per job with that job's TRs). Only run and group
// are SetupProviders; individual setup stays in Provisioner.
type SetupScope string

const (
	ScopeRun   SetupScope = "run"   // once per harness run, before any job
	ScopeGroup SetupScope = "group" // once per named group per run
)

// SetupInfo identifies a SetupProvider: its scope and (for group scope) the group
// Key that ToolIntegration.SetupGroups references. Run-scope Keys are just unique
// ids used to dedup providers.
type SetupInfo struct {
	Scope SetupScope
	Key   string
}

// SetupProvider performs setup/teardown shared across tests, above the per-tool
// Provisioner. The orchestrator runs Setup once (run scope: before all jobs;
// group scope: before the first job whose tool lists the group) and Teardown once
// in reverse, passing the in-scope TRs (run: all runnable; group: the group's
// TRs) so the provider can read kubeconfig/backend. A tool registers itself as a
// group member via ToolIntegration.SetupGroups; a provider registers via
// registry.RegisterSetup. See decisions/0008.
type SetupProvider interface {
	SetupInfo() SetupInfo
	Setup(ctx context.Context, rc *core.RunCtx, trs []core.TestRequirement) error
	Teardown(ctx context.Context, rc *core.RunCtx, trs []core.TestRequirement) error
}
