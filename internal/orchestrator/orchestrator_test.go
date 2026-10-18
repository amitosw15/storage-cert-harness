package orchestrator

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/openshift-storage-cert/storage-cert-harness/internal/config"
	"github.com/openshift-storage-cert/storage-cert-harness/internal/core"
	"github.com/openshift-storage-cert/storage-cert-harness/internal/registry"
	"github.com/openshift-storage-cert/storage-cert-harness/internal/stages"
)

// --- fakes for the scoped-setup test -----------------------------------------

type recorder struct {
	mu     sync.Mutex
	events []string
}

func (r *recorder) add(e string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
}

func (r *recorder) index(substr string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i, e := range r.events {
		if strings.Contains(e, substr) {
			return i
		}
	}
	return -1
}

func (r *recorder) count(substr string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, e := range r.events {
		if strings.Contains(e, substr) {
			n++
		}
	}
	return n
}

type recRunner struct {
	name string
	rec  *recorder
}

func (r recRunner) Run(_ context.Context, _ *core.RunCtx, _ *core.Bag, _ []core.TestRequirement) (stages.RunHandle, error) {
	r.rec.add("job:" + r.name)
	return stages.RunHandle{ID: r.name}, nil
}

type recSetup struct {
	scope stages.SetupScope
	key   string
	rec   *recorder
	fail  bool
}

func (s recSetup) SetupInfo() stages.SetupInfo { return stages.SetupInfo{Scope: s.scope, Key: s.key} }

func (s recSetup) Setup(_ context.Context, _ *core.RunCtx, trs []core.TestRequirement) error {
	var ids []string
	for _, tr := range trs {
		ids = append(ids, tr.ID)
	}
	slices.Sort(ids)
	s.rec.add(fmt.Sprintf("setup:%s/%s(%s)", s.scope, s.key, strings.Join(ids, ",")))
	if s.fail {
		return fmt.Errorf("boom")
	}
	return nil
}

func (s recSetup) Teardown(_ context.Context, _ *core.RunCtx, _ []core.TestRequirement) error {
	s.rec.add(fmt.Sprintf("teardown:%s/%s", s.scope, s.key))
	return nil
}

// TestScopedSetupOrderingAndFailure exercises run/group setup: each runs once,
// ordered run→group→jobs→group-teardown→run-teardown; a group whose setup fails
// errors its TRs and skips their jobs without tearing that group down.
func TestScopedSetupOrderingAndFailure(t *testing.T) {
	rec := &recorder{}
	// Two tools share group gA (setup succeeds); tool t3 is in group gB (fails).
	registry.Register(stages.ToolIntegration{Name: "t1", Provides: []string{"a"}, Runner: recRunner{"t1", rec}, SetupGroups: []string{"gA"}})
	registry.Register(stages.ToolIntegration{Name: "t2", Provides: []string{"b"}, Runner: recRunner{"t2", rec}, SetupGroups: []string{"gA"}})
	registry.Register(stages.ToolIntegration{Name: "t3", Provides: []string{"c"}, Runner: recRunner{"t3", rec}, SetupGroups: []string{"gB"}})
	registry.RegisterSetup(recSetup{scope: stages.ScopeRun, key: "r", rec: rec})
	registry.RegisterSetup(recSetup{scope: stages.ScopeGroup, key: "gA", rec: rec})
	registry.RegisterSetup(recSetup{scope: stages.ScopeGroup, key: "gB", rec: rec, fail: true})

	trs := []core.TestRequirement{
		{ID: "a", AutomationTool: "t1"},
		{ID: "b", AutomationTool: "t2"},
		{ID: "c", AutomationTool: "t3"},
	}
	rc := &core.RunCtx{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	report, err := Run(context.Background(), config.Config{}, trs, core.Provenance{}, "2.0", rc)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Each setup/teardown fired exactly once (setup once per run, not per job).
	for _, want := range []string{"setup:run/r", "setup:group/gA", "setup:group/gB", "teardown:group/gA", "teardown:run/r"} {
		if n := rec.count(want); n != 1 {
			t.Errorf("%q fired %d times, want 1", want, n)
		}
	}
	// gB setup failed → its group must not be torn down and t3's job must not run.
	if rec.count("teardown:group/gB") != 0 {
		t.Error("failed group gB should not be torn down")
	}
	if rec.count("job:t3") != 0 {
		t.Error("t3 job must be skipped after its group setup failed")
	}
	// gA group setup saw both its tools' TRs.
	if rec.index("setup:group/gA(a,b)") < 0 {
		t.Errorf("gA setup should receive TRs a,b; events=%v", rec.events)
	}
	// Ordering: run setup first; group setup before both jobs; teardown after both
	// jobs; group teardown before run teardown.
	runSetup := rec.index("setup:run/r")
	gaSetup := rec.index("setup:group/gA")
	j1, j2 := rec.index("job:t1"), rec.index("job:t2")
	gaDown := rec.index("teardown:group/gA")
	runDown := rec.index("teardown:run/r")
	if runSetup >= gaSetup || gaSetup >= j1 || gaSetup >= j2 {
		t.Errorf("setup must precede jobs: run=%d gA=%d j1=%d j2=%d", runSetup, gaSetup, j1, j2)
	}
	if gaDown <= j1 || gaDown <= j2 || gaDown >= runDown {
		t.Errorf("teardown must follow jobs and reverse order: gAdown=%d j1=%d j2=%d runDown=%d", gaDown, j1, j2, runDown)
	}
	// t3 (group gB failed) is reported as error, not silently dropped.
	var cOutcome core.Outcome
	for _, v := range report.Verdicts {
		if v.TR == "c" {
			cOutcome = v.Outcome
		}
	}
	if cOutcome != core.OutcomeError {
		t.Errorf("TR c should be error after group setup failure, got %q", cOutcome)
	}
}

func TestSelectByPartnerLevel(t *testing.T) {
	all := []core.TestRequirement{
		{ID: "a", PartnerLevel: 3, AutomationTool: "fio"},
		{ID: "b", PartnerLevel: 2, AutomationTool: "fio"},
		{ID: "c", PartnerLevel: 3, AutomationTool: "vdbench"},
	}
	got := SelectTRs(all, config.Filter{PartnerLevels: []int{3}})
	if len(got) != 2 || got[0].ID != "a" || got[1].ID != "c" {
		t.Fatalf("partner-level filter = %+v, want [a c]", got)
	}
	got = SelectTRs(all, config.Filter{PartnerLevels: []int{3}, Tools: []string{"fio"}})
	if len(got) != 1 || got[0].ID != "a" {
		t.Fatalf("partner-level+tool filter = %+v, want [a]", got)
	}
	got = SelectTRs(all, config.Filter{IDs: []string{"b"}})
	if len(got) != 1 || got[0].ID != "b" {
		t.Fatalf("id filter = %+v, want [b]", got)
	}
}

func TestApplyOverridesDoesNotMutateCatalog(t *testing.T) {
	orig := map[string]any{"iodepth": 1}
	trs := []core.TestRequirement{{ID: "TR-1", Params: orig}}
	out := applyOverrides(trs, map[string]map[string]any{
		"TR-1": {"iodepth": 32, "runtime": 120},
	})
	if out[0].Params["iodepth"] != 32 || out[0].Params["runtime"] != 120 {
		t.Fatalf("override not applied: %+v", out[0].Params)
	}
	if orig["iodepth"] != 1 || len(orig) != 1 {
		t.Fatalf("shared catalog params were mutated: %+v", orig)
	}
}
