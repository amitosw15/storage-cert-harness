package kubeburnerocp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openshift-storage-cert/storage-cert-harness/internal/core"
)

func TestPreflightContinuesAfterInvalidWorkload(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	trs := []core.TestRequirement{
		{ID: "INVALID", Params: map[string]any{"workload": "unknown"}},
		{ID: "MIGRATION", Params: map[string]any{"workload": WorkloadVirtMigration, "iterations": 1, "iteration_vms": 2}},
		{ID: "PARALLEL", Params: map[string]any{"workload": WorkloadVirtParallel}},
	}
	got, err := (preflight{}).Check(context.Background(), &core.RunCtx{}, core.NewBag(), trs)
	if err != nil {
		t.Fatal(err)
	}
	want := []struct{ level, message string }{
		{"error", "INVALID parameters:"},
		{"skip", "cannot select checks without a valid workload"},
		{"info", "MIGRATION workload=virt-migration"},
		{"info", "PARALLEL workload=virt-parallel"},
		{"error", "kube-burner-ocp"},
		{"error", "virtctl"},
		{"skip", "kubevirts.kubevirt.io: neither kubectl nor oc found"},
		{"skip", "datavolumes.cdi.kubevirt.io: neither kubectl nor oc found"},
	}
	if len(got) != len(want) {
		t.Fatalf("findings = %+v, want %d checks (shared prerequisites once each)", got, len(want))
	}
	for i, w := range want {
		if got[i].Level != w.level || !strings.Contains(got[i].Message, w.message) {
			t.Errorf("finding %d = %+v, want %s containing %q", i, got[i], w.level, w.message)
		}
	}
}

func TestPreflightSuccessfulMigration(t *testing.T) {
	dir := t.TempDir()
	for _, binary := range []string{"kube-burner-ocp", "virtctl", "kubectl"} {
		if err := os.WriteFile(filepath.Join(dir, binary), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)
	got, err := (preflight{}).Check(context.Background(), &core.RunCtx{Backend: &core.ResolvedBackend{StorageClass: "test-sc"}}, core.NewBag(),
		[]core.TestRequirement{{ID: "MIGRATION", Params: map[string]any{
			"workload": WorkloadVirtMigration, "iterations": 1, "iteration_vms": 2,
		}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 6 {
		t.Fatalf("findings = %+v, want workload, storage class, 2 binaries, and 2 CRDs", got)
	}
	for _, f := range got {
		if f.Level != "info" {
			t.Errorf("finding = %+v, want pass", f)
		}
	}
}

// A plan that names a valid workload but omits a flag that workload requires
// used to clear preflight and then fail seconds into the run, after the tool
// had started creating cluster objects. The catalog's declared params are not
// the ones every workload consumes — virt-migration is declared with
// "concurrency" but driven by "iterations" and "iteration_vms" — so only the
// workload spec can catch this.
func TestPreflightRejectsMissingWorkloadParam(t *testing.T) {
	dir := t.TempDir()
	for _, binary := range []string{"kube-burner-ocp", "virtctl", "kubectl"} {
		if err := os.WriteFile(filepath.Join(dir, binary), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)
	got, err := (preflight{}).Check(context.Background(), &core.RunCtx{Backend: &core.ResolvedBackend{StorageClass: "test-sc"}}, core.NewBag(),
		[]core.TestRequirement{{ID: "MIGRATION", Params: map[string]any{
			"workload": WorkloadVirtMigration, "concurrency": 2, // what the catalog declares, not what the tool needs
		}}})
	if err != nil {
		t.Fatal(err)
	}
	var failed bool
	for _, f := range got {
		if f.Level == "error" && strings.Contains(f.Message, `missing required param "iterations"`) {
			failed = true
		}
	}
	if !failed {
		t.Fatalf("preflight passed a plan the runner would reject: %+v", got)
	}
}
