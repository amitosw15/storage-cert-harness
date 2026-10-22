package virtbench

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/openshift-storage-cert/storage-cert-harness/internal/core"
)

func TestParseSingleNodeCeiling(t *testing.T) {
	goldenParse(t, ParseSingleNodeCeiling, "summary_single_node_results.json", "parse_single_node.golden.json", "TR-VIRT-013")
}

// TestParseSingleNodeCeilingNativePassDespiteFailures guards the behavior that
// sets this scenario apart from every other summary-based one: the fixture has
// 38 failed VMs (expected — the run pushed past the node's ceiling), yet
// Native must stay pass so a real capacity shortfall is only ever reported via
// the max_vms_per_node SLA bar, never as a native run failure.
func TestParseSingleNodeCeilingNativePassDespiteFailures(t *testing.T) {
	in, err := os.ReadFile(filepath.Join("testdata", "summary_single_node_results.json"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	got, err := ParseSingleNodeCeiling(in, "TR-VIRT-013")
	if err != nil {
		t.Fatalf("ParseSingleNodeCeiling: %v", err)
	}
	if got[0].Native != core.OutcomePass {
		t.Errorf("native = %q, want pass (partial failure is the expected outcome)", got[0].Native)
	}
	if v := metricValue(got[0].Metrics, "max_vms_per_node"); v != 62 {
		t.Errorf("max_vms_per_node = %v, want 62 (the fixture's successful count)", v)
	}
}

// tr013SLAs and tr013Bars mirror the real catalog/thresholds entries for
// TR-VIRT-013: a real >= 50 bar for max_vms_per_node, and no bar at all for
// max_luns_per_node (it is published, never gated).
func tr013SLAs() []core.SLA {
	measuredBy := []string{string(core.ToolVirtbench)}
	return []core.SLA{
		{Metric: "max_vms_per_node", Type: "count", Unit: "count", MeasuredBy: measuredBy},
		{Metric: "max_luns_per_node", Type: "count", Unit: "count", MeasuredBy: measuredBy},
	}
}

func tr013Bars() []core.SLA {
	v := 50.0
	return []core.SLA{
		{Metric: "max_vms_per_node", Operator: ">=", Value: &v, Unit: "count"},
	}
}

// singleNodeEvaluator returns the exact evaluator the single-node scenario
// registers: the default scenarioEvaluator with the max_luns_per_node
// DeriveMetrics hook wired in.
func singleNodeEvaluator() scenarioEvaluator {
	return scenarioEvaluator{sc: Scenario{
		AutomationTool: core.ToolVirtbench,
		ScenarioName:   WorkloadSingleNodeCeiling,
		DeriveMetrics:  singleNodeLunMetric,
	}}
}

// TestSingleNodeCeilingComputesLuns checks the one piece of custom logic the
// scenario adds on top of the standard grader: the DeriveMetrics hook derives
// max_luns_per_node from num_disks × the achieved VM count, then the default
// evaluator grades both metrics with the ordinary rules (no custom pass/fail).
func TestSingleNodeCeilingComputesLuns(t *testing.T) {
	tr := core.TestRequirement{
		ID:             "TR-VIRT-013",
		AutomationTool: string(core.ToolVirtbench),
		Scenario:       string(WorkloadSingleNodeCeiling),
		SLAs:           tr013SLAs(),
		Bars:           tr013Bars(),
		Params:         map[string]any{"num_disks": 2},
	}
	res := core.TestResult{TRID: tr.ID, Native: core.OutcomePass, Metrics: []core.Metric{
		{Name: "max_vms_per_node", Value: 62, Unit: "count"},
	}}

	verdicts, err := singleNodeEvaluator().Evaluate(context.Background(), tr, res)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}

	byItem := map[string]core.Verdict{}
	for _, v := range verdicts {
		byItem[v.Item] = v
	}

	if v, ok := byItem["sla:max_vms_per_node"]; !ok || v.Outcome != core.OutcomePass {
		t.Errorf("sla:max_vms_per_node = %+v, want pass (62 >= 50)", v)
	}
	v, ok := byItem["sla:max_luns_per_node"]
	if !ok || v.Outcome != core.OutcomeSkip {
		t.Fatalf("sla:max_luns_per_node = %+v, want a report-only skip (no threshold bar)", v)
	}
	if v.Actual != "124 count" {
		t.Errorf("sla:max_luns_per_node Actual = %q, want %q (62 VMs x 2 disks)", v.Actual, "124 count")
	}
}

// TestSingleNodeCeilingBelowThresholdFails proves there's no special-casing
// hiding a real shortfall: a low VM count fails max_vms_per_node exactly like
// any other SLA compared against its bar.
func TestSingleNodeCeilingBelowThresholdFails(t *testing.T) {
	tr := core.TestRequirement{
		ID:             "TR-VIRT-013",
		AutomationTool: string(core.ToolVirtbench),
		Scenario:       string(WorkloadSingleNodeCeiling),
		SLAs:           tr013SLAs(),
		Bars:           tr013Bars(),
	}
	res := core.TestResult{TRID: tr.ID, Native: core.OutcomePass, Metrics: []core.Metric{
		{Name: "max_vms_per_node", Value: 3, Unit: "count"},
	}}

	verdicts, err := singleNodeEvaluator().Evaluate(context.Background(), tr, res)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	for _, v := range verdicts {
		if v.Item == "sla:max_vms_per_node" {
			if v.Outcome != core.OutcomeFail {
				t.Errorf("sla:max_vms_per_node = %+v, want fail (3 < 50)", v)
			}
			return
		}
	}
	t.Fatal("no sla:max_vms_per_node verdict produced")
}

// vmTemplate returns a datasource-clone VM template with n non-cloud-init
// disks (plus a cloud-init volume that must not be counted), using the
// {{STORAGE_CLASS_NAME}} placeholder virtbench substitutes at run time.
func vmTemplate(n int) string {
	var dvTemplates, disks, volumes string
	for i := 1; i <= n; i++ {
		dvTemplates += fmt.Sprintf(`    - metadata:
        name: disk-%d
      spec:
        sourceRef:
          kind: DataSource
          name: rhel9
          namespace: openshift-virtualization-os-images
        storage:
          resources:
            requests:
              storage: 30Gi
          storageClassName: {{STORAGE_CLASS_NAME}}
          volumeMode: Block
`, i)
		disks += fmt.Sprintf(`            - name: disk-%d
              disk:
                bus: virtio
`, i)
		volumes += fmt.Sprintf(`        - name: disk-%d
          dataVolume:
            name: disk-%d
`, i, i)
	}
	return fmt.Sprintf(`apiVersion: kubevirt.io/v1
kind: VirtualMachine
metadata:
  name: rhel-9-vm
spec:
  dataVolumeTemplates:
%s  runStrategy: Always
  template:
    spec:
      domain:
        devices:
          disks:
%s            - name: cloudinitdisk
              disk:
                bus: virtio
      volumes:
%s        - name: cloudinitdisk
          cloudInitNoCloud:
            userData: |
              #cloud-config
              user: cloud-user
`, dvTemplates, disks, volumes)
}

func writeTemplate(t *testing.T, dir string, n int) string {
	t.Helper()
	path := filepath.Join(dir, fmt.Sprintf("vm-%d-disk.yaml", n))
	if err := os.WriteFile(path, []byte(vmTemplate(n)), 0o600); err != nil {
		t.Fatalf("write template: %v", err)
	}
	return path
}

// TestTemplateDiskCount confirms the Go-side counter matches virtbench's
// detect_disk_count_from_template: non-cloud-init volumes only, placeholder
// tolerated.
func TestTemplateDiskCount(t *testing.T) {
	dir := t.TempDir()
	for _, want := range []int{1, 2, 3} {
		got, err := templateDiskCount(writeTemplate(t, dir, want))
		if err != nil {
			t.Fatalf("templateDiskCount(%d disks): %v", want, err)
		}
		if got != want {
			t.Errorf("templateDiskCount = %d, want %d (cloud-init volume must be excluded)", got, want)
		}
	}
}

// TestSingleNodeCeilingArgsIncludesTemplate checks that a plan-supplied
// vm_template is staged and passed to virtbench (the wiring that lets
// TR-VIRT-013 run genuinely multi-disk VMs).
func TestSingleNodeCeilingArgsIncludesTemplate(t *testing.T) {
	root := t.TempDir()
	tmpl := writeTemplate(t, t.TempDir(), 2)
	tr := core.TestRequirement{
		ID: "TR-VIRT-013",
		Params: map[string]any{
			"storage_class": "sc",
			"vm_template":   tmpl,
			"num_disks":     2,
		},
	}
	args, err := singleNodeCeilingArgs(singleNodeNSPrefix)(&core.RunCtx{}, tr, root)
	if err != nil {
		t.Fatalf("singleNodeCeilingArgs: %v", err)
	}
	var staged string
	for i, a := range args {
		if a == "--vm-template" && i+1 < len(args) {
			staged = args[i+1]
		}
	}
	if staged == "" {
		t.Fatalf("--vm-template not passed; args = %v", args)
	}
	if filepath.Dir(staged) != root {
		t.Errorf("template staged at %q, want inside results root %q", staged, root)
	}
	if _, err := os.Stat(staged); err != nil {
		t.Errorf("staged template not written: %v", err)
	}
}

// TestSingleNodeCeilingValidate covers the num_disks / vm_template consistency
// rules that keep max_luns_per_node honest.
func TestSingleNodeCeilingValidate(t *testing.T) {
	dir := t.TempDir()
	tmpl2 := writeTemplate(t, dir, 2)

	tests := []struct {
		name    string
		params  map[string]any
		wantErr bool
	}{
		{"default single disk, no template", map[string]any{}, false},
		{"multi-disk without template", map[string]any{"num_disks": 2}, true},
		{"template matches num_disks", map[string]any{"num_disks": 2, "vm_template": tmpl2}, false},
		{"template disagrees with num_disks", map[string]any{"num_disks": 3, "vm_template": tmpl2}, true},
		{"num_disks below one", map[string]any{"num_disks": 0}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := singleNodeCeilingValidate(core.TestRequirement{ID: "TR-VIRT-013", Params: tc.params})
			if tc.wantErr && err == nil {
				t.Errorf("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}
