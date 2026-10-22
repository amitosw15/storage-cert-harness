// Package stages defines tool sessions, scoring interfaces, and shared setup contracts.
package stages

import (
	"context"

	"github.com/openshift-storage-cert/storage-cert-harness/internal/core"
)

// Evaluator fully scores one TR from its TestResult, returning a Verdict per
// scorable item. The core grader handles standard sla/checks; a ToolIntegration
// supplies a custom Evaluator (keyed by TR id) for TRs whose scoring the tool
// owns (complex suite/scenario adjudication). See decisions/0006.
type Evaluator interface {
	Evaluate(ctx context.Context, tr core.TestRequirement, res core.TestResult) ([]core.Verdict, error)
}

// ToolIntegration declares a tool and creates isolated sessions for its jobs.
type ToolIntegration struct {
	Name     string
	Image    string
	Provides []string // TR ids this integration can run
	// NewSession creates a fresh session for each pipeline invocation.
	NewSession func() Session

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
