package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openshift-storage-cert/storage-cert-harness/internal/catalog"
	"github.com/openshift-storage-cert/storage-cert-harness/internal/core"
	"github.com/openshift-storage-cert/storage-cert-harness/internal/registry"
	"github.com/openshift-storage-cert/storage-cert-harness/internal/stages"
)

type preflightStub struct {
	findings []core.Finding
	err      error
}

func (p preflightStub) Check(context.Context, *core.RunCtx, *core.Bag, []core.TestRequirement) ([]core.Finding, error) {
	return p.findings, p.err
}

func init() {
	registry.Register(stages.ToolIntegration{Name: "test-preflight-clean", Preflight: preflightStub{findings: []core.Finding{
		{Level: "info", Message: "binary available"},
		{Level: "info", Message: "cluster reachable"},
	}}})
	registry.Register(stages.ToolIntegration{Name: "test-preflight-mixed", Preflight: preflightStub{findings: []core.Finding{
		{Level: "error", Message: "first check failed\ncommand details"},
		{Level: "info", Message: "later check passed"},
		{Level: "skip", Message: "optional check: not configured"},
	}}})
	registry.Register(stages.ToolIntegration{Name: "test-preflight-partial", Preflight: preflightStub{
		findings: []core.Finding{{Level: "info", Message: "first check passed"}},
		err:      errors.New("remaining check could not complete"),
	}})
	registry.Register(stages.ToolIntegration{Name: "test-preflight-none"})
	registry.Register(stages.ToolIntegration{Name: "test-preflight-empty", Preflight: preflightStub{}})
}

func TestPreflightOutput(t *testing.T) {
	cases := []struct {
		name  string
		tools []string
		want  string
		fail  bool
	}{
		{
			name:  "all checks pass",
			tools: []string{"test-preflight-clean"},
			want:  "[PASS] test-preflight-clean: binary available\n[PASS] test-preflight-clean: cluster reachable\nPreflight summary: 2 PASS, 0 FAIL, 0 SKIP\n",
		},
		{
			name:  "failures do not hide later checks or tools",
			tools: []string{"test-preflight-mixed", "test-preflight-clean"},
			want:  "[PASS] test-preflight-clean: binary available\n[PASS] test-preflight-clean: cluster reachable\n[FAIL] test-preflight-mixed: first check failed command details\n[PASS] test-preflight-mixed: later check passed\n[SKIP] test-preflight-mixed: optional check: not configured\nPreflight summary: 3 PASS, 1 FAIL, 1 SKIP\n",
			fail:  true,
		},
		{
			name:  "findings survive adapter error",
			tools: []string{"test-preflight-partial"},
			want:  "[PASS] test-preflight-partial: first check passed\n[FAIL] test-preflight-partial: remaining check could not complete\nPreflight summary: 1 PASS, 1 FAIL, 0 SKIP\n",
			fail:  true,
		},
		{
			name:  "skips succeed with reasons",
			tools: []string{"test-preflight-none", "manual", "test-preflight-empty", ""},
			want:  "[SKIP] TEST-3: no automation tool; manual check required\n[SKIP] TEST-1: no automation tool; manual check required\n[SKIP] test-preflight-empty: preflight returned no check results\n[SKIP] test-preflight-none: no preflight checks implemented\nPreflight summary: 0 PASS, 0 FAIL, 4 SKIP\n",
		},
		{
			name:  "unregistered tool fails",
			tools: []string{"test-preflight-unknown"},
			want:  "[FAIL] test-preflight-unknown: tool not registered\nPreflight summary: 0 PASS, 1 FAIL, 0 SKIP\n",
			fail:  true,
		},
		{
			name: "empty selection is explicit",
			want: "No test requirements selected.\nPreflight summary: 0 PASS, 0 FAIL, 0 SKIP\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			trs := make([]catalog.TR, 0, len(tc.tools))
			for i, tool := range tc.tools {
				trs = append(trs, catalog.TR{ID: fmt.Sprintf("TEST-%d", i), AutomationTool: tool})
			}
			path := writePreflightCatalog(t, trs)
			var out bytes.Buffer
			cmd := newRootCmd()
			cmd.SetOut(&out)
			cmd.SetArgs([]string{"preflight", "--catalog", path})
			err := cmd.Execute()
			if (err != nil) != tc.fail {
				t.Fatalf("error = %v, want failure=%v", err, tc.fail)
			}
			if out.String() != tc.want {
				if tc.name == "empty selection is explicit" &&
					strings.Contains(out.String(), `msg="No test requirements selected."`) &&
					strings.Contains(out.String(), "Preflight summary: 0 PASS, 0 FAIL, 0 SKIP\n") {
					return
				}
				t.Fatalf("output:\n%s\nwant:\n%s", out.String(), tc.want)
			}
		})
	}
}

func TestPreflightVariants(t *testing.T) {
	path := writePreflightCatalog(t, []catalog.TR{{ID: "TEST-VARIANT", AutomationTool: "example"}})
	plan := filepath.Join(t.TempDir(), "plan.yaml")
	if err := os.WriteFile(plan, []byte("schema_version: \"1\"\nname: preflight-variants\nselect:\n  ids: [TEST-VARIANT]\nvariants:\n  TEST-VARIANT:\n    - name: idle\n    - name: load\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	cmd := newRootCmd()
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"preflight", "--catalog", path, "--plan", plan})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	for _, variant := range []string{"idle", "load"} {
		if !strings.Contains(out.String(), "[PASS] "+core.ExecutionLabel("example:TEST-VARIANT", variant)+": example integration ready") {
			t.Fatalf("missing %s variant: %s", variant, out.String())
		}
	}
	if !strings.Contains(out.String(), "2 PASS, 0 FAIL, 0 SKIP") {
		t.Fatal(out.String())
	}
}

func writePreflightCatalog(t *testing.T, trs []catalog.TR) string {
	t.Helper()
	data, err := json.Marshal(catalog.Doc{SchemaVersion: "2.0", TestRequirements: trs})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "catalog.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

type datasourcePreflightStub struct{}

func (datasourcePreflightStub) Check(_ context.Context, rc *core.RunCtx, _ *core.Bag, trs []core.TestRequirement) ([]core.Finding, error) {
	var findings []core.Finding
	for _, tr := range trs {
		findings = append(findings, core.Finding{Level: "info", Message: fmt.Sprintf("cli=%s plan=%v variant=%s", rc.DataSource, tr.Params["datasource"], tr.Variant)})
	}
	return findings, nil
}

func TestPreflightDataSourceCLIAndVariantParams(t *testing.T) {
	registry.Register(stages.ToolIntegration{Name: "test-datasource", Preflight: datasourcePreflightStub{}})
	catalogPath := writePreflightCatalog(t, []catalog.TR{{ID: "TEST-DS", AutomationTool: "test-datasource"}})
	planPath := filepath.Join(t.TempDir(), "plan.yaml")
	data := `schema_version: "1"
name: datasource-variants
select:
  ids: [TEST-DS]
overrides:
  TEST-DS:
    datasource: images/per-test
variants:
  TEST-DS:
    - name: inherited
    - name: overridden
      params:
        datasource: images/per-variant
`
	if err := os.WriteFile(planPath, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	cmd := newRootCmd()
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"preflight", "--catalog", catalogPath, "--plan", planPath, "--datasource", "images/cli"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"cli=images/cli plan=images/per-test variant=inherited", "cli=images/cli plan=images/per-variant variant=overridden", "--datasource affects Virtbench only"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %q in %s", want, out.String())
		}
	}
	if flag := newRunCmd().Flags().Lookup("datasource"); flag == nil || flag.DefValue != "" {
		t.Fatal("run command datasource flag missing or changes default")
	}
}

func TestDataSourceBoundaryUsesResolvedTool(t *testing.T) {
	for _, tt := range []struct {
		tr   core.TestRequirement
		want bool
	}{
		{core.TestRequirement{ID: "TR-STOR-002", AutomationTool: "catalog alias"}, true},
		{core.TestRequirement{ID: "TR-STOR-006", AutomationTool: "kube-burner-ocp pvc-density"}, false},
		{core.TestRequirement{ID: "unknown", AutomationTool: "virtbench nonexistent"}, false},
	} {
		if got := usesVirtbench(tt.tr); got != tt.want {
			t.Errorf("%+v: got %v, want %v", tt.tr, got, tt.want)
		}
	}
}
