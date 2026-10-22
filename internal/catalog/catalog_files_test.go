package catalog_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/openshift-storage-cert/storage-cert-harness/internal/catalog"
	"github.com/openshift-storage-cert/storage-cert-harness/internal/core"
	"github.com/openshift-storage-cert/storage-cert-harness/internal/orchestrator"
	"github.com/openshift-storage-cert/storage-cert-harness/internal/registry"
	_ "github.com/openshift-storage-cert/storage-cert-harness/internal/tools/example"
	_ "github.com/openshift-storage-cert/storage-cert-harness/internal/tools/kubeburner"
	_ "github.com/openshift-storage-cert/storage-cert-harness/internal/tools/kubeburnerocp"
	_ "github.com/openshift-storage-cert/storage-cert-harness/internal/tools/virtbench"
)

func TestCatalogSchemaToolEnum(t *testing.T) {
	data, err := os.ReadFile("../../schemas/catalog.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Defs struct {
			TR struct {
				Properties struct {
					AutomationTool struct {
						Enum []string `json:"enum"`
					} `json:"automation_tool"`
				} `json:"properties"`
			} `json:"tr"`
		} `json:"$defs"`
	}
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatal(err)
	}
	want := []string{string(core.ToolExample), string(core.ToolManual), string(core.ToolVirtbench), string(core.ToolKubeBurner), string(core.ToolKubeBurnerOCP)}
	got := schema.Defs.TR.Properties.AutomationTool.Enum
	if !slices.Equal(got, want) {
		t.Fatalf("catalog schema automation_tool enum = %v, want %v", got, want)
	}
}

func TestShippedCatalogToolNames(t *testing.T) {
	paths, err := filepath.Glob("../../examples/catalog*.json")
	if err != nil {
		t.Fatal(err)
	}
	paths = append(paths, "../../catalog.json")
	for _, path := range paths {
		t.Run(filepath.Base(path), func(t *testing.T) {
			doc, err := catalog.Load(path)
			if err != nil {
				t.Fatal(err)
			}
			for _, tr := range doc.TestRequirements {
				if tr.AutomationTool == "" {
					t.Errorf("%s: missing automation_tool", tr.ID)
					continue
				}
				if !core.ToolName(tr.AutomationTool).Valid() {
					t.Errorf("%s: invalid automation_tool %q", tr.ID, tr.AutomationTool)
					continue
				}
				if tr.AutomationTool == string(core.ToolManual) {
					continue
				}
				name := tr.AutomationTool
				if tr.AutomationTool == string(core.ToolVirtbench) {
					if tr.Scenario == "" {
						t.Errorf("%s: virtbench scenario required", tr.ID)
						continue
					}
					name += " " + tr.Scenario
				} else if tr.Scenario != "" {
					t.Errorf("%s: unexpected scenario %q", tr.ID, tr.Scenario)
					continue
				}
				integration, ok := registry.Get(name)
				if !ok {
					t.Errorf("%s: integration %q not registered", tr.ID, name)
					continue
				}
				found := false
				for _, id := range integration.Provides {
					if id == tr.ID {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("%s: integration %q does not provide this TR", tr.ID, name)
				}
				resolved, ok := orchestrator.ResolveTool(core.TestRequirement{ID: tr.ID, AutomationTool: tr.AutomationTool, Scenario: tr.Scenario})
				if !ok || resolved.Name != name {
					t.Errorf("%s: resolver selected %q, want %q", tr.ID, resolved.Name, name)
				}
			}
		})
	}
}
