// Package kubeburner drives upstream kube-burner (generic init -c) for the
// VM-snapshot scenarios TR-VIRT-010 and TR-VIRT-027. See decisions/0012.
package kubeburner

import (
	"github.com/openshift-storage-cert/storage-cert-harness/internal/registry"
	"github.com/openshift-storage-cert/storage-cert-harness/internal/stages"
)

func init() {
	registry.Register(stages.ToolIntegration{
		Name:         "kube-burner",
		Provides:     []string{"TR-VIRT-010", "TR-VIRT-027"},
		Preflight:    preflight{},
		Provisioner:  provisioner{},
		Runner:       runner{},
		LogCollector: collector{},
		ResultParser: parser{},
		Teardown:     teardown{},
		Evaluators: map[string]stages.Evaluator{
			"TR-VIRT-010": snapshotEvaluator{},
			"TR-VIRT-027": snapshotEvaluator{},
		},
		ParallelSafe:      false,
		ExclusivityGroups: []string{"kube-burner", "cdi", "storage"},
	})
}
