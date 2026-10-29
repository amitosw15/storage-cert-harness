package grader

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	"gitlab.cee.redhat.com/eco-special-projects/storage-cert-harness/internal/core"
	"gitlab.cee.redhat.com/eco-special-projects/storage-cert-harness/internal/report"
)

func TestNativeOrGradedTruthTable(t *testing.T) {
	for _, tc := range []struct {
		name     string
		criteria bool
		native   core.Outcome
		want     core.Outcome
	}{
		{"no criteria/pass", false, core.OutcomePass, core.OutcomePass},
		{"no criteria/fail", false, core.OutcomeFail, core.OutcomeFail},
		{"no criteria/error", false, core.OutcomeError, core.OutcomeError},
		{"no criteria/skip", false, core.OutcomeSkip, core.OutcomeSkip},
		{"no criteria/empty", false, "", core.OutcomeSkip},
		{"criteria/pass", true, core.OutcomePass, core.OutcomePass},
		{"criteria/fail", true, core.OutcomeFail, core.OutcomeFail},
		{"criteria/error", true, core.OutcomeError, core.OutcomeError},
		{"criteria/skip", true, core.OutcomeSkip, core.OutcomeSkip},
		{"criteria/empty", true, "", core.OutcomeSkip},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tr := core.TestRequirement{ID: "TR-1"}
			res := core.TestResult{Native: tc.native}
			want := map[string]core.Outcome{"native:jobs_passed": tc.want}
			if tc.criteria {
				tr.SLAs = []core.SLA{{Metric: "duration", Unit: "s"}}
				tr.Bars = []core.SLA{{Metric: "duration", Operator: "<=", Value: f64(30), Unit: "s"}}
				tr.Checks = []core.Check{{ID: "lifecycle", Kind: core.CheckScenario}}
				res.Metrics = []core.Metric{{Name: "duration", Value: 1, Unit: "s"}}
				res.Checks = map[string]core.Outcome{"lifecycle": core.OutcomePass}
				want["sla:duration"] = core.OutcomePass
				want["check:lifecycle"] = core.OutcomePass
			}
			vs, err := NativeOrGraded("native:jobs_passed", "execution outcome").Evaluate(context.Background(), tr, res)
			if err != nil {
				t.Fatal(err)
			}
			if len(vs) != len(want) || !reflect.DeepEqual(outcomes(vs), want) {
				t.Fatalf("verdicts = %+v, want outcomes %+v", vs, want)
			}
			if tc.native == "" {
				v := verdictByItem(vs, "native:jobs_passed")
				if v.Reason != "no native execution outcome provided" {
					t.Fatalf("empty native skip reason = %q", v.Reason)
				}
			}
		})
	}
}

func TestNativeOrGradedPreservesCatalogOutcomes(t *testing.T) {
	for _, native := range []core.Outcome{core.OutcomePass, core.OutcomeFail} {
		for _, tc := range []struct {
			name string
			sla  bool
			want core.Outcome
		}{
			{"SLA pass", true, core.OutcomePass},
			{"SLA fail", true, core.OutcomeFail},
			{"SLA error", true, core.OutcomeError},
			{"SLA skip", true, core.OutcomeSkip},
			{"check pass", false, core.OutcomePass},
			{"check fail", false, core.OutcomeFail},
			{"check error", false, core.OutcomeError},
			{"check skip", false, core.OutcomeSkip},
		} {
			t.Run(string(native)+"/"+tc.name, func(t *testing.T) {
				tr := core.TestRequirement{ID: "TR-1"}
				res := core.TestResult{Native: native}
				item := "check:lifecycle"
				if tc.sla {
					item = "sla:duration"
					tr.SLAs = []core.SLA{{Metric: "duration", Unit: "s"}}
					tr.Bars = []core.SLA{{Metric: "duration", Operator: "<=", Value: f64(30), Unit: "s"}}
					res.Metrics = []core.Metric{{Name: "duration", Value: 1, Unit: "s"}}
					switch tc.want {
					case core.OutcomeFail:
						res.Metrics[0].Value = 31
					case core.OutcomeError:
						res.Metrics = nil
					case core.OutcomeSkip:
						tr.Bars = nil
					}
				} else {
					tr.Checks = []core.Check{{ID: "lifecycle", Kind: core.CheckScenario}}
					res.Checks = map[string]core.Outcome{"lifecycle": tc.want}
				}
				vs, err := NativeOrGraded("native:jobs_passed", "execution outcome").Evaluate(context.Background(), tr, res)
				if err != nil {
					t.Fatal(err)
				}
				want := map[string]core.Outcome{item: tc.want, "native:jobs_passed": native}
				if len(vs) != len(want) || !reflect.DeepEqual(outcomes(vs), want) {
					t.Fatalf("verdicts = %+v, want outcomes %+v", vs, want)
				}
			})
		}
	}
}

func TestNativePassCannotOverrideCatalogFailure(t *testing.T) {
	for _, tc := range []struct {
		name     string
		duration float64
		check    core.Outcome
		wantSLA  core.Outcome
	}{
		{"SLA failure", 31, core.OutcomePass, core.OutcomeFail},
		{"check failure", 1, core.OutcomeFail, core.OutcomePass},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tr := core.TestRequirement{
				ID:     "TR-1",
				SLAs:   []core.SLA{{Metric: "duration", Unit: "s"}},
				Bars:   []core.SLA{{Metric: "duration", Operator: "<=", Value: f64(30), Unit: "s"}},
				Checks: []core.Check{{ID: "lifecycle", Kind: core.CheckScenario}},
			}
			res := core.TestResult{
				Native:  core.OutcomePass,
				Metrics: []core.Metric{{Name: "duration", Value: tc.duration, Unit: "s"}},
				Checks:  map[string]core.Outcome{"lifecycle": tc.check},
			}
			vs, err := NativeOrGraded("native:jobs_passed", "execution outcome").Evaluate(context.Background(), tr, res)
			if err != nil {
				t.Fatal(err)
			}
			want := map[string]core.Outcome{
				"native:jobs_passed": core.OutcomePass,
				"sla:duration":       tc.wantSLA,
				"check:lifecycle":    tc.check,
			}
			if len(vs) != len(want) || !reflect.DeepEqual(outcomes(vs), want) {
				t.Fatalf("verdicts = %+v, want outcomes %+v", vs, want)
			}
			for i := range vs {
				vs[i].Level = 3
			}
			rep := report.Build(tc.name, "2.0", core.Provenance{}, vs)
			if rep.Certifiable || rep.Summary.Fail != 1 || rep.Summary.CertifiedLevel != 0 {
				t.Fatalf("native pass overrode catalog failure: %+v", rep)
			}
		})
	}
}

func TestNativeOrGradedRetryReplacesFailure(t *testing.T) {
	for _, skipped := range []bool{false, true} {
		t.Run(fmt.Sprintf("skipped=%t", skipped), func(t *testing.T) {
			tr := core.TestRequirement{ID: "TR-1", SLAs: []core.SLA{{Metric: "duration", Unit: "s"}}}
			if !skipped {
				tr.Bars = []core.SLA{{Metric: "duration", Operator: "<=", Value: f64(30), Unit: "s"}}
			}
			evaluator := NativeOrGraded("native:jobs_passed", "execution outcome")
			reports := make([]core.Report, 0, 2)
			for _, native := range []core.Outcome{core.OutcomeFail, core.OutcomePass} {
				res := core.TestResult{Native: native, Metrics: []core.Metric{{Name: "duration", Value: 1, Unit: "s"}}}
				vs, err := evaluator.Evaluate(context.Background(), tr, res)
				if err != nil {
					t.Fatal(err)
				}
				for i := range vs {
					vs[i].Level = 3
				}
				reports = append(reports, report.Build(string(native), "2.0", core.Provenance{}, vs))
			}
			if reports[0].Certifiable {
				t.Fatal("native failure was certifiable")
			}
			merged, err := report.Merge(reports[0], reports[1])
			if err != nil {
				t.Fatal(err)
			}
			wantLevel := 3
			if !merged.Certifiable || merged.Summary.Fail != 0 || merged.Summary.CertifiedLevel != wantLevel {
				t.Fatalf("retry did not preserve native pass: %+v", merged)
			}
		})
	}
}
