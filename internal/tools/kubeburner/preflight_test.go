package kubeburner

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/openshift-storage-cert/storage-cert-harness/internal/core"
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
	for _, want := range []struct{ level, text string }{
		{"error", "TR-VIRT-010 parameters:"},
		{"info", "TR-VIRT-027 parameters valid"},
		{"error", "storage_class required"},
		{"error", "kube-burner"},
		{"skip", "CDI VolumeImportSource populator"},
		{"error", "expected exactly 1 TR"},
	} {
		if !slices.ContainsFunc(got, func(f core.Finding) bool { return f.Level == want.level && strings.Contains(f.Message, want.text) }) {
			t.Errorf("findings = %+v, missing %s containing %q", got, want.level, want.text)
		}
	}

}

func TestPreflightSuccessfulSnapshot(t *testing.T) {
	dir := t.TempDir()
	script := `#!/bin/sh
case "$2" in
  volumepopulator) echo '{"items":[]}' ;;
  storageclass) echo '{"provisioner":"test-driver"}' ;;
  storageprofile) echo '{"status":{"snapshotClass":"test-snapshot"}}' ;;
  volumesnapshotclass) echo '{"driver":"test-driver"}' ;;
esac
exit 0
`
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
	if len(got) != 10 {
		t.Fatalf("findings = %+v, want parameters, storage config, binary, 3 CRDs, storage existence, snapshot class, wait --for=create support, populator", got)
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
