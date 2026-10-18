package report

import (
	"bytes"
	"strings"
	"testing"

	"gitlab.cee.redhat.com/eco-special-projects/storage-cert-harness/internal/core"
)

func v(level int, tr string, o core.Outcome) core.Verdict {
	return core.Verdict{TR: tr, Level: level, Item: "item", Outcome: o}
}

func TestBuildCertifiedLevel(t *testing.T) {
	cases := []struct {
		name     string
		verdicts []core.Verdict
		want     int
	}{
		// Today's reality: every TR is Level 2. An all-L2 pass must read as level 2,
		// not 0 (L1 is a separate external suite, not tested here).
		{"l2 only pass", []core.Verdict{v(2, "A", core.OutcomePass)}, 2},
		{"l2 fail", []core.Verdict{v(2, "A", core.OutcomeFail)}, 0},
		{"l2 pass, l3 fail", []core.Verdict{
			v(2, "B", core.OutcomePass), v(3, "C", core.OutcomeFail),
		}, 2},
		{"l2 error blocks", []core.Verdict{v(2, "A", core.OutcomeError)}, 0},
		{"l2 all skip is not earned", []core.Verdict{v(2, "A", core.OutcomeSkip)}, 0},
		{"untested lower level does not block", []core.Verdict{
			v(2, "A", core.OutcomeSkip), v(3, "B", core.OutcomePass),
		}, 3},
		{"l2 pass despite a skip sibling", []core.Verdict{
			v(2, "A", core.OutcomePass), v(2, "A", core.OutcomeSkip),
		}, 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := Build("run", "2.0", core.Provenance{}, c.verdicts)
			if r.Summary.CertifiedLevel != c.want {
				t.Fatalf("certified level = %d, want %d (levels=%+v)", r.Summary.CertifiedLevel, c.want, r.Levels)
			}
		})
	}
}

func TestJUnitExport(t *testing.T) {
	r := Build("run", "2.0", core.Provenance{}, []core.Verdict{
		{TR: "EX-001", Level: 3, Item: "sla:lat", Outcome: core.OutcomePass, DurationS: 12.5, Expected: "<= 2 ms", Actual: "0.8 ms"},
		{TR: "EX-002", Level: 3, Item: "check:x", Outcome: core.OutcomeError, Reason: "boom"},
		{TR: "EX-003", Level: 2, Item: "sla:iops", Outcome: core.OutcomeSkip, Reason: "tbv"},
	})
	r.Environment.Hardware.Nodes = 6
	r.Environment.Platform.OCPVersion = "4.22.11"
	r.Environment.Platform.CSIDriver.Name = "csi.example.com"
	r.Attestations = []core.Attestation{{Claim: "ra_url", Value: "https://x", SignedBy: "human:me"}}
	// A measured value with no SLA must still surface (ADR-0014).
	r.Measurements = []core.Measurement{{TR: "EX-001", Level: 3, Name: "podReady", Value: 4.2, Unit: "ms", Percentile: "p99"}}

	var buf bytes.Buffer
	if err := (JUnitExporter{}).Export(&buf, r); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{
		`name="environment-hardware"`,
		`name="environment-platform"`,
		`value="4.22.11"`,
		`name="level-2"`,
		`name="level-3"`,
		`classname="EX-001"`,
		`time="12.500"`,
		`<error message="boom">`,
		`<skipped message="tbv">`,
		`name="measurements"`,
		`name="podReady@p99"`,
		`value="4.2"`,
		`name="attestations"`,
		`value="https://x"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("junit output missing %q\n---\n%s", want, out)
		}
	}
	var markdown bytes.Buffer
	if err := WriteMarkdown(&markdown, r); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"## Attestations", "ra url", "https://x", "Signed by: human:me"} {
		if !strings.Contains(markdown.String(), want) {
			t.Errorf("markdown output missing %q\n---\n%s", want, markdown.String())
		}
	}
}

// A verdict should be traceable to the files it was graded from in JUnit too,
// not only in the JSON report: the catalog accepts either format. Artifacts are
// recorded per execution, so every item of that execution carries them.
func TestJUnitCarriesExecutionArtifacts(t *testing.T) {
	r := core.Report{
		SchemaVersion: "2.0",
		Executions: []core.RunExecution{{
			TR: "TR-VIRT-018", State: core.ExecutionCompleted, Outcome: core.OutcomeFail,
			Artifacts: []string{
				"virtbench/TR-VIRT-018/20261018-180304/2-disk/run/summary_vm_creation_results.json",
				"virtbench/TR-VIRT-018/20261018-180304/2-disk/run/vm_creation_results.json",
			},
		}},
		Verdicts: []core.Verdict{
			{TR: "TR-VIRT-018", Item: "sla:clone_duration@p99", Outcome: core.OutcomeFail, Level: 3},
		},
	}
	var buf bytes.Buffer
	if err := (JUnitExporter{}).Export(&buf, r); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{
		`name="artifacts"`,
		"virtbench/TR-VIRT-018/20261018-180304/2-disk/run/vm_creation_results.json",
		`name="harness.junit_profile_version" value="1"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("junit output missing %q\n---\n%s", want, out)
		}
	}
}

// An execution with no recorded artifacts must not emit an empty property.
func TestJUnitOmitsArtifactsWhenNoneRecorded(t *testing.T) {
	r := core.Report{
		SchemaVersion: "2.0",
		Executions:    []core.RunExecution{{TR: "TR-VIRT-018", State: core.ExecutionCompleted}},
		Verdicts:      []core.Verdict{{TR: "TR-VIRT-018", Item: "sla:clone_duration@p99", Outcome: core.OutcomePass, Level: 3}},
	}
	var buf bytes.Buffer
	if err := (JUnitExporter{}).Export(&buf, r); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), `name="artifacts"`) {
		t.Errorf("emitted an empty artifacts property\n---\n%s", buf.String())
	}
}
