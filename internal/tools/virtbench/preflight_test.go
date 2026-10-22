package virtbench

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openshift-storage-cert/storage-cert-harness/internal/core"
	"github.com/openshift-storage-cert/storage-cert-harness/internal/registry"
)

func scenarioForTR(id string) Scenario {
	for _, scenario := range scenarios {
		if scenario.ProvidesTR == id {
			return scenario
		}
	}
	return Scenario{}
}

func testSession(trs []core.TestRequirement) *session {
	return &session{sc: scenarioForTR(trs[0].ID), trs: trs}
}

func TestPreflightMissingBinaryStopsAtFirstError(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	trs := []core.TestRequirement{{ID: "TR-STOR-002", Params: map[string]any{"storage_class": "sc", "num_vms": 1}}}
	findings, err := testSession(trs).Preflight(context.Background(), &core.RunCtx{}, trs)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 1 || findings[0].Level != "error" || !strings.Contains(findings[0].Message, "virtbench") {
		t.Fatalf("findings = %+v, want only the missing VirtBench binary error", findings)
	}
}

func TestPreflightReplayReportsLiveChecksAsSkipped(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	dir := t.TempDir()
	trs := []core.TestRequirement{{ID: "TR-VIRT-008", Params: map[string]any{"results_dir": dir}}}
	findings, err := testSession(trs).Preflight(context.Background(), &core.RunCtx{}, trs)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) < 2 || findings[0].Level != "info" {
		t.Fatalf("findings = %+v, want accessible replay path then skipped live checks", findings)
	}
	for _, finding := range findings[1:] {
		if finding.Level != "skip" || !strings.Contains(finding.Message, "replay mode") {
			t.Errorf("finding = %+v, want replay skip", finding)
		}
	}
}

func TestPreflightReplayPathErrorStopsBeforeLiveChecks(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	missing := filepath.Join(t.TempDir(), "missing")
	trs := []core.TestRequirement{{ID: "TR-VIRT-008", Params: map[string]any{"results_dir": missing}}}
	findings, err := testSession(trs).Preflight(context.Background(), &core.RunCtx{}, trs)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 1 || findings[0].Level != "error" {
		t.Fatalf("findings = %+v, want only inaccessible replay path error", findings)
	}
}

func TestPreflightStopsBatchBeforeLaterProbes(t *testing.T) {
	dir := t.TempDir()
	probe := filepath.Join(dir, "probe-called")
	virtbench := "#!/bin/sh\nif [ \"$1\" = fio ]; then touch '" + probe + "'; fi\nexit 0\n"
	kubectl := "#!/bin/sh\ntouch '" + probe + "'\necho '{\"items\':[]}'\n"
	for name, body := range map[string]string{"virtbench": virtbench, "kubectl": kubectl} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)
	trs := []core.TestRequirement{
		{ID: "TR-VIRT-008", Params: map[string]any{"storage_class": "sc"}},
		{ID: "TR-STOR-002", Params: map[string]any{"storage_class": "sc", "num_vms": 1}},
	}
	findings, err := testSession(trs).Preflight(context.Background(), &core.RunCtx{}, trs)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) == 0 || findings[len(findings)-1].Level != "error" || !strings.Contains(findings[len(findings)-1].Message, "target_node") {
		t.Fatalf("findings = %+v, want first missing target_node error", findings)
	}
	if _, err := os.Stat(probe); !os.IsNotExist(err) {
		t.Fatalf("later worker/FIO probe ran after first error: %v", err)
	}
}

func TestSnapshotFlagDoesNotProbeForNonCloneWorkload(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, binary), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	trs := []core.TestRequirement{{ID: "TR-STOR-001", Params: map[string]any{"storage_class": "sc", "use_snapshot": true}}}
	findings, err := testSession(trs).Preflight(context.Background(), &core.RunCtx{}, trs)
	if err != nil {
		t.Fatal(err)
	}
	for _, finding := range findings {
		if finding.Level == "error" {
			t.Fatalf("non-clone snapshot flag triggered a cluster probe: %+v", finding)
		}
	}
}

func TestScenarioPreflightReportsSuccessfulWorkerChecks(t *testing.T) {
	dir := t.TempDir()
	virtbench := filepath.Join(dir, binary)
	if err := os.WriteFile(virtbench, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	kubectl := `#!/bin/sh
echo '{"items":[{"metadata":{"name":"worker-a","labels":{"node-role.kubernetes.io/worker":""}}},{"metadata":{"name":"worker-b","labels":{"node-role.kubernetes.io/worker":""}}}]}'
`
	if err := os.WriteFile(filepath.Join(dir, "kubectl"), []byte(kubectl), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	trs := []core.TestRequirement{{ID: "TR-VIRT-008", Params: map[string]any{"storage_class": "sc", "target_node": "worker-a"}}}
	findings, err := testSession(trs).Preflight(context.Background(), &core.RunCtx{}, trs)
	if err != nil {
		t.Fatal(err)
	}
	for _, finding := range findings {
		if finding.Level == "error" {
			t.Fatalf("preflight failed: %+v", findings)
		}
	}
	if !hasFinding(findings, "2 schedulable workers") {
		t.Fatalf("findings = %+v, want worker-count check", findings)
	}
}

func TestSingleNodeUnpinnedWorkerCheckIsSkipped(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, binary), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	trs := []core.TestRequirement{{ID: "TR-VIRT-013", Params: map[string]any{"storage_class": "sc"}}}
	findings, err := testSession(trs).Preflight(context.Background(), &core.RunCtx{}, trs)
	if err != nil {
		t.Fatal(err)
	}
	if !hasFinding(findings, "no node_name pin") {
		t.Fatalf("findings = %+v, want unpinned worker skip", findings)
	}
}

func TestVirtbenchWorkloadsMatchCatalogAndRegistrySessions(t *testing.T) {
	var catalog struct {
		TestRequirements []struct {
			ID             string `json:"id"`
			AutomationTool string `json:"automation_tool"`
			Scenario       string `json:"scenario"`
		} `json:"test_requirements"`
	}
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "catalog.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &catalog); err != nil {
		t.Fatal(err)
	}
	actual := make(map[string]string)
	for _, requirement := range catalog.TestRequirements {
		if requirement.AutomationTool == string(core.ToolVirtbench) {
			actual[requirement.ID] = requirement.Scenario
		}
	}
	declared := make(map[string]bool)
	for _, scenario := range scenarios {
		if scenario.AutomationTool != core.ToolVirtbench {
			t.Errorf("%s tool enum = %q, want %q", scenario.ProvidesTR, scenario.AutomationTool, core.ToolVirtbench)
		}
		if _, ok := workloadSpecs[scenario.ProvidesTR]; !ok {
			t.Errorf("%s has no workload declaration", scenario.ProvidesTR)
		}
		if got, ok := actual[scenario.ProvidesTR]; !ok || got != string(scenario.ScenarioName) {
			t.Errorf("%s scenario = %q, catalog has %q (present=%t)", scenario.ProvidesTR, scenario.ScenarioName, got, ok)
		}
		declared[scenario.ProvidesTR] = true
		integration, ok := registry.Get(scenario.IntegrationName())
		if !ok {
			t.Errorf("registry lacks %q", scenario.IntegrationName())
			continue
		}
		first, second := integration.NewSession().(*session), integration.NewSession().(*session)
		if first == second || first.sc.ProvidesTR != scenario.ProvidesTR || second.sc.ProvidesTR != scenario.ProvidesTR {
			t.Errorf("%s registry did not create isolated scenario sessions", scenario.ProvidesTR)
		}
		first.resultsRoot = "first"
		if second.resultsRoot != "" {
			t.Errorf("%s session state leaked between factory calls", scenario.ProvidesTR)
		}
	}
	for id := range actual {
		if !declared[id] {
			t.Errorf("catalog VirtBench TR %s has no scenario declaration", id)
		}
	}
}

func hasFinding(findings []core.Finding, fragment string) bool {
	for _, finding := range findings {
		if strings.Contains(finding.Message, fragment) {
			return true
		}
	}
	return false
}
