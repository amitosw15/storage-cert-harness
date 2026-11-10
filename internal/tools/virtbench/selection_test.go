package virtbench

import (
	"testing"

	"github.com/openshift-storage-cert/storage-cert-harness/internal/core"
	"github.com/openshift-storage-cert/storage-cert-harness/internal/registry"
)

func TestScenarioSelection(t *testing.T) {
	cases := map[string]string{
		"TR-VIRT-002": "virtbench datasource-clone",
		"TR-VIRT-001": "virtbench datasource-clone --boot-storm",
		"TR-STOR-001": "virtbench disk-ops",
		"TR-VIRT-018": "virtbench datasource-clone --num-disks 2",
		"TR-VIRT-008": "virtbench vm-ops drain-nodes",
		"TR-STOR-002": "virtbench fio",
		"TR-VIRT-013": "virtbench datasource-clone --single-node",
		"TR-VIRT-007": "virtbench failure-recovery",
	}
	if len(scenarios) != len(cases) || len(scenarioSpecs) != len(cases) {
		t.Fatal("scenario count differs from supported TRs")
	}
	for id, want := range cases {
		t.Run(id, func(t *testing.T) {
			sc, ok := scenarioFor(core.TestRequirement{ID: id})
			if !ok || sc.ProvidesTR != id || sc.AutomationTool != want {
				t.Fatalf("scenario = %+v, found %v", sc, ok)
			}
			tool, ok := registry.Get(want)
			if !ok || len(tool.Provides) != 1 || tool.Provides[0] != id {
				t.Fatalf("registered tool = %+v, found %v", tool, ok)
			}
		})
	}
	if _, ok := scenarioFor(core.TestRequirement{ID: "TR-UNKNOWN"}); ok {
		t.Fatal("unknown TR resolved")
	}
}
