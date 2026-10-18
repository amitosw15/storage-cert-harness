package report

import (
	"bytes"
	"reflect"
	"strings"
	"testing"

	"github.com/openshift-storage-cert/storage-cert-harness/internal/core"
)

func TestSelectMeasurements(t *testing.T) {
	trs := []core.TestRequirement{
		{ID: "A", SLAs: []core.SLA{{Metric: "latency", Percentile: "p99"}, {Metric: "duration"}}},
		{ID: "B"},
	}
	latency := core.Measurement{TR: "A", Name: "latency", Percentile: "p99", Value: 1}
	otherPercentile := core.Measurement{TR: "A", Name: "latency", Percentile: "p50"}
	duration := core.Measurement{TR: "A", Name: "duration"}
	extra := core.Measurement{TR: "A", Name: "extra"}
	unrelated := core.Measurement{TR: "B", Name: "latency", Percentile: "p99"}
	input := []core.Measurement{latency, otherPercentile, duration, extra, unrelated}
	got, warnings := SelectMeasurements(trs, input, nil, nil)
	if !reflect.DeepEqual(got, []core.Measurement{latency, duration}) || len(warnings) != 0 {
		t.Fatalf("defaults: measurements=%+v warnings=%v", got, warnings)
	}
	got, warnings = SelectMeasurements(trs, input, []string{"latency"}, map[string][]string{
		"A": {"extra", "latency@p99", "missing@p99", "missing@p99"},
		"B": {"extra"},
	})
	if !reflect.DeepEqual(got, input) {
		t.Fatalf("extras: got %+v, want %+v", got, input)
	}
	wantWarnings := []string{
		"TR A: requested report metric \"missing@p99\" was not produced",
		"TR B: requested report metric \"extra\" was not produced",
	}
	if !reflect.DeepEqual(warnings, wantWarnings) {
		t.Fatalf("warnings=%v, want %v", warnings, wantWarnings)
	}
	_, warnings = SelectMeasurements(trs, nil, []string{"extra"}, nil)
	if len(warnings) != len(trs) {
		t.Fatalf("absent results must warn for each requested TR: %v", warnings)
	}
}

func TestReportWarningsAreInformational(t *testing.T) {
	r := Build("run", "2.0", core.Provenance{}, []core.Verdict{{TR: "A", Outcome: core.OutcomePass}})
	before := r.Summary
	r.Warnings = []string{"TR A: requested report metric \"missing\" was not produced"}
	var jsonOut, markdown, junit bytes.Buffer
	if err := WriteJSON(&jsonOut, r); err != nil {
		t.Fatal(err)
	}
	if err := WriteMarkdown(&markdown, r); err != nil {
		t.Fatal(err)
	}
	if err := (JUnitExporter{}).Export(&junit, r); err != nil {
		t.Fatal(err)
	}
	for _, out := range []string{jsonOut.String(), markdown.String(), junit.String()} {
		if !strings.Contains(out, "was not produced") {
			t.Fatalf("warning absent: %s", out)
		}
	}
	if r.Summary != before || strings.Contains(junit.String(), "<failure") || strings.Contains(junit.String(), "<error") {
		t.Fatal("warning changed grading")
	}
}
