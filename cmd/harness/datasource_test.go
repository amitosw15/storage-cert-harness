package main

import (
	"bytes"
	"context"
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
