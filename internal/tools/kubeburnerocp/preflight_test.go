package kubeburnerocp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gitlab.cee.redhat.com/eco-special-projects/storage-cert-harness/internal/core"
)

func TestPreflightContinuesAfterInvalidWorkload(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	trs := []core.TestRequirement{
		{ID: "INVALID", Params: map[string]any{"workload": "unknown"}},
		{ID: "MIGRATION", Params: map[string]any{"workload": WorkloadVirtMigration}},
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
		[]core.TestRequirement{{ID: "MIGRATION", Params: map[string]any{"workload": WorkloadVirtMigration}}})
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
