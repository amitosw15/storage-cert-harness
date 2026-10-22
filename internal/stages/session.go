package stages

import (
	"context"

	"github.com/openshift-storage-cert/storage-cert-harness/internal/core"
)

// Session owns one job's state across the complete tool lifecycle.
type Session interface {
	Preflight(context.Context, *core.RunCtx, []core.TestRequirement) ([]core.Finding, error)
	Provision(context.Context, *core.RunCtx) error
	Run(context.Context, *core.RunCtx) error
	Collect(context.Context, *core.RunCtx) (core.LogBundle, error)
	Parse(context.Context, *core.RunCtx, core.LogBundle) ([]core.TestResult, error)
	Teardown(context.Context, *core.RunCtx) error
}
