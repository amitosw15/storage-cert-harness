package orchestrator

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/openshift-storage-cert/storage-cert-harness/internal/config"
	"github.com/openshift-storage-cert/storage-cert-harness/internal/core"
	"github.com/openshift-storage-cert/storage-cert-harness/internal/registry"
	"github.com/openshift-storage-cert/storage-cert-harness/internal/stages"
)

type variantProbe struct{ dirs []string }

func (p *variantProbe) Run(_ context.Context, rc *core.RunCtx, bag *core.Bag, trs []core.TestRequirement) (stages.RunHandle, error) {
	if len(trs) != 1 {
		return stages.RunHandle{}, fmt.Errorf("expected isolated execution")
	}
	if _, ok := bag.Get("result"); ok {
		return stages.RunHandle{}, fmt.Errorf("bag reused")
	}
	tr := trs[0]
	p.dirs = append(p.dirs, rc.WorkDir)
	if err := os.WriteFile(filepath.Join(rc.WorkDir, "artifact"), []byte(tr.Variant), 0o600); err != nil {
		return stages.RunHandle{}, err
	}
	bag.Set("result", core.TestResult{TRID: tr.ID, Metrics: []core.Metric{{Name: "latency", Value: tr.Params["load"].(float64), Unit: "s"}}})
	return stages.RunHandle{}, nil
}
func (*variantProbe) Parse(_ context.Context, _ *core.RunCtx, bag *core.Bag, _ core.LogBundle) ([]core.TestResult, error) {
	r, _ := core.GetAs[core.TestResult](bag, "result")
	return []core.TestResult{r}, nil
}

func TestVariantsRunAndGradeIndependently(t *testing.T) {
	probe := &variantProbe{}
	tool := "variant-probe"
	registry.Register(stages.ToolIntegration{Name: tool, Runner: probe, ResultParser: probe})
	// fake workload values and gates exercise conditional grading.
	small, large, gate := 10.0, 100.0, 50.0
	tr := core.TestRequirement{ID: "TR-VARIANT", AutomationTool: tool,
		Params: map[string]any{"load": small, "default": "kept"},
		SLAs:   []core.SLA{{Metric: "latency", Unit: "s"}},
		Bars: []core.SLA{
			{Metric: "latency", Unit: "s", Operator: "<=", Value: &gate, When: map[string]any{"load": small}},
			{Metric: "latency", Unit: "s", Operator: "<=", Value: &gate, When: map[string]any{"load": large}},
		},
	}
	cfg := config.Config{Overrides: map[string]map[string]any{tr.ID: {"load": large, "override": "kept"}}, Variants: map[string][]config.Variant{tr.ID: {
		{Name: "small", Params: map[string]any{"load": small}}, {Name: "large"},
	}}}
	resolved, err := ResolveExecutions([]core.TestRequirement{tr}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if resolved[0].Params["default"] != "kept" || resolved[0].Params["override"] != "kept" || resolved[0].Params["load"] != small || resolved[1].Params["load"] != large {
		t.Fatalf("incorrect precedence: %+v", resolved)
	}
	resolved[0].Params["default"] = "changed"
	if tr.Params["default"] != "kept" || resolved[1].Params["default"] != "kept" {
		t.Fatal("parameter maps shared")
	}
	rc := &core.RunCtx{WorkDir: t.TempDir(), Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	rep, err := Run(context.Background(), cfg, []core.TestRequirement{tr}, core.Provenance{}, "test", rc)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Verdicts) != 2 || len(rep.Measurements) != 2 {
		t.Fatalf("missing executions: %+v", rep)
	}
	outcomes := map[string]core.Outcome{}
	for _, v := range rep.Verdicts {
		if v.TR != tr.ID {
			t.Fatal("catalog ID changed")
		}
		outcomes[v.Variant] = v.Outcome
	}
	if outcomes["small"] != core.OutcomePass || outcomes["large"] != core.OutcomeFail {
		t.Fatalf("results crossed variants: %v", outcomes)
	}
	if probe.dirs[0] == probe.dirs[1] {
		t.Fatal("artifact directories shared")
	}
	for i, name := range []string{"small", "large"} {
		data, err := os.ReadFile(filepath.Join(probe.dirs[i], "artifact"))
		if err != nil || string(data) != name {
			t.Fatalf("artifact overwritten: %s, %v", data, err)
		}
	}
}

func TestVariantSelectionAndErrors(t *testing.T) {
	trs := []core.TestRequirement{{ID: "a"}, {ID: "b"}}
	cfg := config.Config{Filter: config.Filter{IDs: []string{"a", "a"}}, Variants: map[string][]config.Variant{"b": {{Name: "only"}}}}
	got, err := ResolveExecutions(trs, cfg)
	if err != nil || len(got) != 1 || got[0].ID != "a" || got[0].Variant != "" {
		t.Fatalf("selection changed: %+v, %v", got, err)
	}
	cfg.Variants["unknown"] = []config.Variant{{Name: "only"}}
	if _, err := ResolveExecutions(trs, cfg); err == nil {
		t.Fatal("accepted unknown TR")
	}
}

func TestVariantFailureIdentity(t *testing.T) {
	cfg := config.Config{Variants: map[string][]config.Variant{"manual": {{Name: "first"}, {Name: "second"}}}}
	rep, err := Run(context.Background(), cfg, []core.TestRequirement{{ID: "manual"}}, core.Provenance{}, "test", &core.RunCtx{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Verdicts) != 2 {
		t.Fatalf("missing skips: %+v", rep.Verdicts)
	}
	for _, v := range rep.Verdicts {
		if v.Variant == "" || v.Outcome != core.OutcomeSkip {
			t.Fatalf("unidentified skip: %+v", v)
		}
	}
}
