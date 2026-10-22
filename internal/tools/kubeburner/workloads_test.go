package kubeburner

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openshift-storage-cert/storage-cert-harness/internal/core"
)

func TestWorkloadPreflightChecksRunnerBinaryAndToolIdentity(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "virtbench"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	findings, err := (&session{}).Preflight(context.Background(), nil, []core.TestRequirement{{
		ID: "TR-VIRT-010", AutomationTool: string(core.ToolVirtbench),
		Params: map[string]any{"replicas": 1},
	}})
	if err != nil {
		t.Fatal(err)
	}
	var identityError, binaryError bool
	for _, finding := range findings {
		if finding.Level != "error" {
			continue
		}
		identityError = identityError || strings.Contains(finding.Message, `automation_tool "virtbench" must be "kube-burner"`)
		binaryError = binaryError || strings.Contains(finding.Message, `needs "kube-burner" on PATH`)
	}
	if !identityError || !binaryError {
		t.Fatalf("findings = %+v, want tool identity and missing runner binary errors", findings)
	}
}

func TestWorkloadPreflightRetainsPerRequirementState(t *testing.T) {
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
	job := &session{}
	rc := &core.RunCtx{Backend: &core.ResolvedBackend{StorageClass: "test-sc"}}
	findings, err := job.Preflight(context.Background(), rc, []core.TestRequirement{
		{ID: "TR-VIRT-010", Params: map[string]any{"replicas": 1}},
		{ID: "TR-VIRT-027", Params: map[string]any{"replicas": 1, "snapshot_count": 0}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var storageConfig, binaryChecks, populatorChecks, reducedSkips int
	for _, finding := range findings {
		if finding.Level == "error" {
			t.Fatalf("preflight error: %s", finding.Message)
		}
		switch {
		case finding.Message == "storage_class=test-sc":
			storageConfig++
		case finding.Message == "host binary found: kube-burner":
			binaryChecks++
		case strings.Contains(finding.Message, "using legacy CDI imports"):
			populatorChecks++
		case finding.Level == "skip" && strings.Contains(finding.Message, "reduced mode"):
			reducedSkips++
		}
	}
	if storageConfig != 1 || binaryChecks != 1 || populatorChecks != 2 || reducedSkips != 1 {
		t.Fatalf("findings = %+v, want shared config/binary once, both populator callbacks, and reduced-mode skip", findings)
	}
	runs := job.executions
	if len(runs) != 2 {
		t.Fatalf("executions = %+v, want both requirements", runs)
	}
	for i := range runs {
		if runs[i].Requirement.Params.UsePopulator {
			t.Errorf("execution %d must use legacy imports", i)
		}
	}
	if runs[0].Requirement.Params.NS == runs[1].Requirement.Params.NS {
		t.Error("requirements must have separate namespaces")
	}
	if runs[0].Selection.SnapshotClass != "test-snapshot" || runs[1].Selection.SnapshotClass != "" {
		t.Fatalf("selections = %+v / %+v, want snapshot selection only on the first run", runs[0].Selection, runs[1].Selection)
	}
}
