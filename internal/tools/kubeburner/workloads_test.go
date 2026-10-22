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
	findings, err := (preflight{}).Check(context.Background(), nil, core.NewBag(), []core.TestRequirement{{
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
