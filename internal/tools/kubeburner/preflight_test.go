package kubeburner

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gitlab.cee.redhat.com/eco-special-projects/storage-cert-harness/internal/core"
)

func TestPreflightContinuesAfterInvalidParameters(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	got, err := (preflight{}).Check(context.Background(), &core.RunCtx{}, core.NewBag(), []core.TestRequirement{
		{ID: "TR-VIRT-010"},
		{ID: "TR-VIRT-027", Params: map[string]any{"replicas": 1, "snapshot_count": 1}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 9 {
		t.Fatalf("findings = %+v, want all 9 checks", got)
	}
	for i, level := range []string{"error", "info", "error", "error", "skip", "skip", "skip", "skip", "skip"} {
		if got[i].Level != level {
			t.Errorf("check %d = %+v, want %s", i, got[i], level)
		}
	}
	if !strings.Contains(got[1].Message, "TR-VIRT-027") {
		t.Fatal("missing later TR's parameter check")
	}
}

func TestPreflightSuccessfulSnapshot(t *testing.T) {
	dir := t.TempDir()
	script := "#!/bin/sh\nif [ \"$2\" = volumepopulator ]; then echo '{\"items\":[]}'; fi\nexit 0\n"
	for _, binary := range []string{"kube-burner", "kubectl"} {
		if err := os.WriteFile(filepath.Join(dir, binary), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)
	bag := core.NewBag()
	got, err := (preflight{}).Check(context.Background(), &core.RunCtx{Backend: &core.ResolvedBackend{StorageClass: "test-sc"}}, bag,
		[]core.TestRequirement{{ID: "TR-VIRT-010", Params: map[string]any{"replicas": 1, "snapshot_count": 1}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 8 {
		t.Fatalf("findings = %+v, want parameters, storage config, binary, 3 CRDs, storage existence, populator", got)
	}
	for _, f := range got {
		if f.Level != "info" {
			t.Errorf("finding = %+v, want pass", f)
		}
	}
	p, ok := core.GetAs[Params](bag, "params")
	if !ok || p.UsePopulator {
		t.Fatalf("params = %+v, want legacy CDI import fallback", p)
	}
}
