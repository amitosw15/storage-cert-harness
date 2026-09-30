package virtbench

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gitlab.cee.redhat.com/eco-special-projects/storage-cert-harness/internal/core"
)

func TestPreflightMissingBinaryStillChecksParameters(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	findings, err := (preflight{sc: Scenario{CommandCheck: []string{"fio", "--help"}, Validate: fioValidate}}).Check(
		context.Background(), &core.RunCtx{}, nil, []core.TestRequirement{{ID: "TR-STOR-002"}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 4 {
		t.Fatalf("findings = %+v, want binary, command, storage class, and parameter checks", findings)
	}
	for i, level := range []string{"error", "skip", "error", "error"} {
		if findings[i].Level != level {
			t.Errorf("check %d = %+v, want %s", i, findings[i], level)
		}
	}
	if !strings.Contains(findings[1].Message, "CLI not found") {
		t.Fatal("command skip must explain missing CLI")
	}
}

func TestPreflightReplayReportsLiveChecksAsSkipped(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	for _, missing := range []bool{false, true} {
		dir := t.TempDir()
		wantLevel := "info"
		if missing {
			dir = filepath.Join(dir, "missing")
			wantLevel = "error"
		}
		sc := Scenario{CommandCheck: []string{"fio", "--help"}, Preflight: drainPreflight, Validate: func(core.TestRequirement) error {
			t.Fatal("live parameter validation called in replay")
			return nil
		}}
		findings, err := (preflight{sc: sc}).Check(context.Background(), &core.RunCtx{}, nil,
			[]core.TestRequirement{{ID: "TR-VIRT-008", Params: map[string]any{"results_dir": dir}}})
		if err != nil {
			t.Fatal(err)
		}
		if len(findings) != 8 || findings[0].Level != wantLevel {
			t.Fatalf("findings = %+v, want replay directory result and all 7 live checks", findings)
		}
		for _, f := range findings[1:] {
			if f.Level != "skip" || !strings.Contains(f.Message, "replay mode") {
				t.Errorf("finding = %+v, want skip with replay reason", f)
			}
		}
	}
}

func TestScenarioPreflightReportsSuccessfulNodeChecks(t *testing.T) {
	dir := t.TempDir()
	script := `#!/bin/sh
echo '{"items":[{"metadata":{"name":"worker-a","labels":{"node-role.kubernetes.io/worker":""}}},{"metadata":{"name":"worker-b","labels":{"node-role.kubernetes.io/worker":""}}}]}'
`
	if err := os.WriteFile(filepath.Join(dir, "kubectl"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	trs := []core.TestRequirement{{ID: "TR-VIRT-008", Params: map[string]any{"target_node": "worker-a", "node_name": "worker-b"}}}
	findings := drainPreflight(context.Background(), &core.RunCtx{}, trs)
	findings = append(findings, singleNodeCeilingPreflight(context.Background(), &core.RunCtx{}, trs)...)
	if len(findings) != 4 {
		t.Fatalf("findings = %+v, want kubectl, target_node, worker count, and pinned node checks", findings)
	}
	for _, f := range findings {
		if f.Level != "info" {
			t.Errorf("finding = %+v, want pass", f)
		}
	}
}

func TestDrainPreflightMissingKubectlSkipsWorkerLookup(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	findings := drainPreflight(context.Background(), &core.RunCtx{}, []core.TestRequirement{{ID: "TR-VIRT-008"}})
	if len(findings) != 3 || findings[0].Level != "error" || findings[1].Level != "error" || findings[2].Level != "skip" || !strings.Contains(findings[2].Message, "kubectl not found") {
		t.Fatalf("findings = %+v, want missing binary and target failures, skipped worker check with reason", findings)
	}
}

func TestSingleNodePreflightUnpinnedNodeIsSkipped(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	findings := singleNodeCeilingPreflight(context.Background(), &core.RunCtx{}, []core.TestRequirement{{ID: "TR-VIRT-016"}})
	if len(findings) != 1 || findings[0].Level != "skip" || !strings.Contains(findings[0].Message, "no node_name pinned") {
		t.Fatalf("findings = %+v, want skip with unpinned node reason", findings)
	}
}
