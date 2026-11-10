package orchestrator_test

import (
	"path/filepath"
	"testing"

	"github.com/openshift-storage-cert/storage-cert-harness/internal/catalog"
	"github.com/openshift-storage-cert/storage-cert-harness/internal/config"
	"github.com/openshift-storage-cert/storage-cert-harness/internal/kb"
	"github.com/openshift-storage-cert/storage-cert-harness/internal/orchestrator"
	"github.com/openshift-storage-cert/storage-cert-harness/internal/plan"
	"github.com/openshift-storage-cert/storage-cert-harness/internal/thresholds"
	_ "github.com/openshift-storage-cert/storage-cert-harness/internal/tools/kubeburner"
	_ "github.com/openshift-storage-cert/storage-cert-harness/internal/tools/kubeburnerocp"
	_ "github.com/openshift-storage-cert/storage-cert-harness/internal/tools/virtbench"
)

func TestSmokePlanResolvesTools(t *testing.T) {
	root := filepath.Join("..", "..")
	cat, err := catalog.Load(filepath.Join(root, "catalog.json"))
	if err != nil {
		t.Fatal(err)
	}
	th, err := thresholds.Load(filepath.Join(root, "thresholds.json"))
	if err != nil {
		t.Fatal(err)
	}
	trs, err := kb.Join(cat, th)
	if err != nil {
		t.Fatal(err)
	}
	p, err := plan.Load(filepath.Join(root, "plans", "harness-smoke-plan.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	selected, err := orchestrator.ResolveExecutions(trs, config.Config{Filter: p.Filter(), Overrides: p.Overrides, Variants: p.Variants})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"TR-STOR-002": "virtbench fio", "TR-STOR-006": "kube-burner-ocp",
		"TR-VIRT-001": "virtbench datasource-clone --boot-storm", "TR-VIRT-004": "kube-burner-ocp",
		"TR-VIRT-010": "kube-burner", "TR-VIRT-013": "virtbench datasource-clone --single-node",
		"TR-VIRT-018": "virtbench datasource-clone --num-disks 2", "TR-VIRT-019": "kube-burner-ocp", "TR-VIRT-027": "kube-burner",
	}
	if len(selected) != len(want) {
		t.Fatalf("selected %d TRs, want %d: %+v", len(selected), len(want), selected)
	}
	for _, tr := range selected {
		tool, ok := orchestrator.ResolveTool(tr)
		expected, known := want[tr.ID]
		if !known || !ok || tool.Name != expected {
			t.Errorf("%s resolved to %q (found %v), want %q", tr.ID, tool.Name, ok, expected)
		}
		delete(want, tr.ID)
	}
	if len(want) != 0 {
		t.Fatalf("missing TRs: %v", want)
	}
}
