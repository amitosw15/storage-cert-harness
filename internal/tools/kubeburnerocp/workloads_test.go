package kubeburnerocp

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/openshift-storage-cert/storage-cert-harness/internal/core"
)

func TestWorkloadPreflightContinuesAfterInvalidParameters(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	job := &session{}
	findings, err := job.Preflight(context.Background(), nil, []core.TestRequirement{
		{ID: "INVALID", Params: map[string]any{"workload": "unknown"}},
		{ID: "MIGRATION", Params: map[string]any{"workload": WorkloadVirtMigration, "iteration_vms": 2}},
		{ID: "PARALLEL", Params: map[string]any{"workload": WorkloadVirtParallel}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{
		`INVALID parameters: kube-burner-ocp: unknown workload "unknown"`,
		`MIGRATION parameters: kube-burner-ocp: missing required param "iterations"`,
		"PARALLEL parameters valid",
		"storage_class required",
		`needs "kube-burner-ocp" on PATH`,
		`needs "virtctl" on PATH`,
		"cluster checks require kubectl or oc",
		"kubevirts.kubevirt.io: neither kubectl nor oc found",
		"datavolumes.cdi.kubevirt.io: neither kubectl nor oc found",
	} {
		count := 0
		for _, finding := range findings {
			if strings.Contains(finding.Message, fragment) {
				count++
			}
		}
		if count != 1 {
			t.Errorf("findings = %+v, want %q exactly once; got %d", findings, fragment, count)
		}
	}
	executions := job.executions
	if len(executions) != 3 || executions[0].Requirement.Err == nil || executions[1].Requirement.Err == nil || executions[2].Requirement.Err != nil {
		t.Fatalf("executions = %+v, want both invalid requirements and the later valid requirement", executions)
	}
}

func TestBuildCLIArgsPVCDensity(t *testing.T) {
	p := Params{
		Workload: WorkloadPVCDensity,
		Raw:      map[string]any{"workload": WorkloadPVCDensity, "iterations": 5, "claim_size": "256Mi"},
	}
	args, err := buildCLIArgs(p, "ocs-sc")
	if err != nil {
		t.Fatalf("buildCLIArgs failed: %v", err)
	}
	if len(args) == 0 || args[0] != "pvc-density" {
		t.Fatalf("expected first arg 'pvc-density', got %q", args[0])
	}
	for _, flag := range []string{"--iterations", "--claim-size", "--storage-class-name"} {
		if !slices.Contains(args, flag) {
			t.Errorf("missing required flag %q in args", flag)
		}
	}
	if slices.Contains(args, "--namespace") {
		t.Errorf("pvc-density must not pass --namespace (no such flag upstream)")
	}
}

func TestBuildCLIArgsMissingRequiredParam(t *testing.T) {
	p := Params{
		Workload: WorkloadPVCDensity,
		Raw:      map[string]any{"workload": WorkloadPVCDensity, "iterations": 5},
	}
	if _, err := buildCLIArgs(p, "ocs-sc"); err == nil {
		t.Errorf("expected error for missing required param 'claim_size', got nil")
	}
}

func TestBuildCLIArgsWrongType(t *testing.T) {
	p := Params{
		Workload: WorkloadPVCDensity,
		Raw:      map[string]any{"workload": WorkloadPVCDensity, "iterations": "not-an-int", "claim_size": "256Mi"},
	}
	if _, err := buildCLIArgs(p, "ocs-sc"); err == nil {
		t.Errorf("expected error for non-integer 'iterations', got nil")
	}
}

func TestBuildCLIArgsMissingStorageClass(t *testing.T) {
	p := Params{
		Workload: WorkloadPVCDensity,
		Raw:      map[string]any{"workload": WorkloadPVCDensity, "iterations": 5, "claim_size": "256Mi"},
	}
	if _, err := buildCLIArgs(p, ""); err == nil {
		t.Errorf("expected error when storage_class is empty, got nil")
	}
}

func TestBuildCLIArgsVirtParallelDefaults(t *testing.T) {
	p := Params{
		Workload: WorkloadVirtParallel,
		Raw:      map[string]any{"workload": WorkloadVirtParallel},
	}
	args, err := buildCLIArgs(p, "trident-sc")
	if err != nil {
		t.Fatalf("buildCLIArgs failed: %v", err)
	}
	if len(args) == 0 || args[0] != "virt-parallel" {
		t.Fatalf("expected first arg 'virt-parallel', got %q", args[0])
	}
	if !slices.Contains(args, "--storage-class") {
		t.Errorf("missing --storage-class flag")
	}
	if slices.Contains(args, "--storage-class-name") {
		t.Errorf("virt-parallel must not use --storage-class-name (wrong flag for this workload)")
	}
}

func TestBuildCLIArgsVirtParallelSkipFlags(t *testing.T) {
	p := Params{
		Workload: WorkloadVirtParallel,
		Raw: map[string]any{
			"workload":           WorkloadVirtParallel,
			"initial_vms":        2,
			"max_iterations":     1,
			"skip_migration_job": true,
			"skip_snapshot_job":  true,
		},
	}
	args, err := buildCLIArgs(p, "trident-sc")
	if err != nil {
		t.Fatalf("buildCLIArgs failed: %v", err)
	}
	for _, flag := range []string{"--initial-vms", "--max-iterations", "--skip-migration-job=true", "--skip-snapshot-job=true"} {
		if !slices.Contains(args, flag) {
			t.Errorf("missing expected flag %q", flag)
		}
	}
}
