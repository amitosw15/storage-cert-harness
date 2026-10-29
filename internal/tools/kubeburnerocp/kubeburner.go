// Package kubeburnerocp integrates kube-burner-ocp (many workloads, one adapter; the workload is selected per TR by the plan).
package kubeburnerocp

import (
	"gitlab.cee.redhat.com/eco-special-projects/storage-cert-harness/internal/grader"
	"gitlab.cee.redhat.com/eco-special-projects/storage-cert-harness/internal/registry"
	"gitlab.cee.redhat.com/eco-special-projects/storage-cert-harness/internal/stages"
)

func init() {
	provides := []string{"TR-STOR-006", "TR-VIRT-004", "TR-VIRT-019"}

	evals := map[string]stages.Evaluator{}
	for _, id := range provides {
		evals[id] = grader.NativeOrGraded("native:jobs_passed",
			"native execution requires successful process exit and passing job summaries without execution errors")
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
