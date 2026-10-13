package kubeburnerocp

import (
	"slices"
	"testing"
)

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
	for _, flag := range []string{"skip_migration_job", "skip_snapshot_job", "skip_resize_job", "skip_restart_job"} {
		p := Params{Workload: WorkloadVirtParallel, Raw: map[string]any{"workload": WorkloadVirtParallel, flag: true}}
		if _, err := buildCLIArgs(p, "trident-sc"); err == nil {
			t.Fatalf("%s must not allow a partial certification lifecycle", flag)
		}
	}
}
