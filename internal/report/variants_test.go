package report

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/openshift-storage-cert/storage-cert-harness/internal/core"
)

func TestVariantReportIdentity(t *testing.T) {
	r := Build("run", "test", core.Provenance{}, []core.Verdict{
		{TR: "TR-A", Variant: "small", Item: "sla:latency", Outcome: core.OutcomePass},
		{TR: "TR-A", Variant: "large", Item: "sla:latency", Outcome: core.OutcomeFail},
	})
	r.Measurements = []core.Measurement{{TR: "TR-A", Variant: "small", Name: "latency"}, {TR: "TR-A", Variant: "large", Name: "latency"}}
	for _, render := range []func(io.Writer, core.Report) error{WriteMarkdown, func(w io.Writer, r core.Report) error { return writeJUnit(w, r, false) }} {
		var b bytes.Buffer
		if err := render(&b, r); err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"small", "large"} {
			if !strings.Contains(b.String(), "TR-A ["+name+"]") {
				t.Fatalf("variant identity missing: %s", b.String())
			}
		}
	}
	var b bytes.Buffer
	if err := WriteJSON(&b, r); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), `"variant": "small"`) || !strings.Contains(b.String(), `"variant": "large"`) {
		t.Fatalf("JSON variant missing: %s", b.String())
	}
}

func TestMissingExtraMetricWarnsPerVariant(t *testing.T) {
	trs := []core.TestRequirement{{ID: "TR-A", Variant: "small"}, {ID: "TR-A", Variant: "large"}}
	got, warnings := SelectMeasurements(trs, []core.Measurement{{TR: "TR-A", Variant: "small", Name: "extra"}}, []string{"extra"}, nil)
	if len(got) != 1 || len(warnings) != 1 || !strings.Contains(warnings[0], "TR-A [large]") {
		t.Fatalf("variant warning mixed: %+v, %v", got, warnings)
	}
}

func TestMergePreservesVariantsAndReplacesReruns(t *testing.T) {
	base := Build("run", "test", core.Provenance{}, []core.Verdict{
		{TR: "TR-A", Variant: "small", Scenario: "idle", Item: "sla:latency", Outcome: core.OutcomePass},
		{TR: "TR-A", Variant: "large", Scenario: "idle", Item: "sla:latency", Outcome: core.OutcomeFail},
	})
	base.Measurements = []core.Measurement{{TR: "TR-A", Variant: "small", Scenario: "idle", Name: "latency"}, {TR: "TR-A", Variant: "large", Scenario: "idle", Name: "latency"}}
	next := Build("rerun", "test", core.Provenance{}, []core.Verdict{{TR: "TR-A", Variant: "large", Scenario: "idle", Item: "sla:latency", Outcome: core.OutcomePass}})
	next.Measurements = []core.Measurement{{TR: "TR-A", Variant: "large", Scenario: "idle", Name: "latency", Unit: "s"}}
	merged, err := Merge(base, next)
	if err != nil {
		t.Fatal(err)
	}
	if len(merged.Verdicts) != 2 || len(merged.Measurements) != 2 || merged.Summary.Fail != 0 {
		t.Fatalf("variants lost or rerun duplicated: %+v", merged)
	}
	for _, m := range merged.Measurements {
		if m.Variant == "large" && m.Unit != "s" {
			t.Fatal("rerun did not replace measurement")
		}
	}
}
