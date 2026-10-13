// Package kubeburnerocp integrates kube-burner-ocp (many workloads, one adapter; the workload is selected per TR by the plan).
package kubeburnerocp

import (
	"gitlab.cee.redhat.com/eco-special-projects/storage-cert-harness/internal/grader"
	"gitlab.cee.redhat.com/eco-special-projects/storage-cert-harness/internal/registry"
	"gitlab.cee.redhat.com/eco-special-projects/storage-cert-harness/internal/stages"
)

func init() {
	provides := []string{"TR-STOR-006", "TR-VIRT-004", "TR-VIRT-019"}

	// TRs with no KB SLA bars grade on native pass/fail alone. TRs with real SLAs
	// (e.g. TR-VIRT-004's total_migration_duration) fall through to the built-in
	// grader by having no custom evaluator here.
	nativeOnly := map[string]bool{"TR-STOR-006": true}
	evals := map[string]stages.Evaluator{}
	for _, id := range provides {
		if nativeOnly[id] {
			evals[id] = grader.NativeOrGraded("native:jobs_passed",
				"SLA bars to-be-validated (KB sla: []); native = all kube-burner-ocp jobs passed")
		}
	}
	evals["TR-VIRT-019"] = virtParallelEvaluator{}
	registry.Register(stages.ToolIntegration{
		Name:              "kube-burner-ocp",
		Provides:          provides,
		Preflight:         preflight{},
		Provisioner:       provisioner{},
		Runner:            runner{},
		LogCollector:      collector{},
		ResultParser:      parser{},
		Teardown:          teardown{},
		Evaluators:        evals,
		ParallelSafe:      false,
		ExclusivityGroups: []string{"kube-burner", "cdi", "storage"},
	})
}
