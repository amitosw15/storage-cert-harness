package report

import (
	"bytes"
	"strings"
	"testing"

	"github.com/openshift-storage-cert/storage-cert-harness/internal/core"
)

func TestLifecycleAndJUnitCompositionScenarios(t *testing.T) {
	tests := []struct {
		name             string
		report           core.Report
		wantStatus       core.RunStatus
		wantHealth       core.RunHealth
		wantCertifiable  bool
		wantIncidentFlag string
		wantPartsCount   string
	}{
		{
			name: "clean composed load80 and non-load80 parts",
			report: func() core.Report {
				r := Build("cert-clean", "2.0", core.Provenance{}, []core.Verdict{{TR: "TR-STOR-006", Level: 3, Scenario: "load80", Item: "check:restore", Outcome: core.OutcomePass}, {TR: "TR-STOR-006", Level: 3, Scenario: "non-load80", Item: "check:restore", Outcome: core.OutcomePass}})
				r.Composition = core.RunComposition{PartsCount: 2, Parts: []core.RunPart{{PartID: "part-load80", Kind: "initial", Status: core.RunStatusComplete}, {PartID: "part-non-load80", Kind: "resume", Status: core.RunStatusComplete}}}
				r.Executions = []core.RunExecution{{TR: "TR-STOR-006", State: core.ExecutionCompleted, Outcome: core.OutcomePass}}
				RefreshLifecycle(&r)
				return r
			}(),
			wantStatus: core.RunStatusComplete, wantHealth: core.RunHealthHealthy, wantCertifiable: true, wantIncidentFlag: `value="false"`, wantPartsCount: `value="2"`,
		},
		{
			name: "complete but degraded after a stall",
			report: func() core.Report {
				r := Build("cert-degraded", "2.0", core.Provenance{}, []core.Verdict{{TR: "TR-VIRT-001", Level: 3, Item: "check:boot", Outcome: core.OutcomePass}})
				r.Incidents = []core.Incident{{ID: "stall-001", Type: "stall", Severity: "error", TR: "TR-VIRT-001", Stage: "provision", Message: "no progress before timeout", BlocksCertification: true}}
				r.Composition = core.RunComposition{PartsCount: 2, Parts: []core.RunPart{{PartID: "part-001", Kind: "initial", Status: core.RunStatusPartial}, {PartID: "part-002", Kind: "resume", Status: core.RunStatusComplete}}}
				RefreshLifecycle(&r)
				return r
			}(),
			wantStatus: core.RunStatusComplete, wantHealth: core.RunHealthDegraded, wantCertifiable: false, wantIncidentFlag: `value="true"`, wantPartsCount: `value="2"`,
		},
		{
			name: "stalled partial checkpoint",
			report: func() core.Report {
				r := NewRunning("cert-partial", "2.0", core.Provenance{}, []core.TestRequirement{{ID: "TR-VIRT-001"}, {ID: "TR-VIRT-004"}}, "initial", "2026-09-12T10:00:00Z")
				r.Executions[0].State = core.ExecutionRunning
				r.Executions[0].Stage = "provision"
				r.Incidents = []core.Incident{{ID: "stall-002", Type: "stall", Severity: "error", TR: "TR-VIRT-001", Stage: "provision", Message: "heartbeat timed out", BlocksCertification: true}}
				Finalize(&r, core.RunStatusPartial, "provision stalled", "2026-09-12T10:30:00Z")
				return r
			}(),
			wantStatus: core.RunStatusPartial, wantHealth: core.RunHealthUnhealthy, wantCertifiable: false, wantIncidentFlag: `value="true"`, wantPartsCount: `value="1"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.report.Status != tt.wantStatus || tt.report.Health != tt.wantHealth || tt.report.Certifiable != tt.wantCertifiable {
				t.Fatalf("lifecycle = status=%s health=%s certifiable=%t, want %s/%s/%t", tt.report.Status, tt.report.Health, tt.report.Certifiable, tt.wantStatus, tt.wantHealth, tt.wantCertifiable)
			}
			var junit bytes.Buffer
			if err := (JUnitExporter{}).Export(&junit, tt.report); err != nil {
				t.Fatal(err)
			}
			out := junit.String()
			for _, want := range []string{`name="harness.status"`, `name="harness.health"`, `name="harness.certifiable"`, `name="harness.report_json"`, tt.wantIncidentFlag, tt.wantPartsCount} {
				if !strings.Contains(out, want) {
					t.Errorf("JUnit output missing %q\n---\n%s", want, out)
				}
			}
			var markdown bytes.Buffer
			if err := WriteMarkdown(&markdown, tt.report); err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{"Status:", "Health:", "Certifiable:", "Run composition"} {
				if !strings.Contains(markdown.String(), want) {
					t.Errorf("Markdown output missing %q", want)
				}
			}
		})
	}
}

func TestMergePreservesRunPartsAndIncidents(t *testing.T) {
	base := Build("logical-run", "2.0", core.Provenance{KBGitCommit: "abc"}, []core.Verdict{{TR: "TR-A", Level: 3, Scenario: "load80", Item: "check:x", Outcome: core.OutcomePass}})
	base.Composition = core.RunComposition{PartsCount: 1, Parts: []core.RunPart{{PartID: "part-load80", Kind: "initial", Status: core.RunStatusComplete}}}
	next := Build("resume-run", "2.0", core.Provenance{KBGitCommit: "abc"}, []core.Verdict{{TR: "TR-B", Level: 3, Scenario: "non-load80", Item: "check:x", Outcome: core.OutcomePass}})
	next.Composition = core.RunComposition{PartsCount: 1, Parts: []core.RunPart{{PartID: "part-non-load80", Kind: "resume", Status: core.RunStatusComplete}}}
	next.Incidents = []core.Incident{{ID: "warn-001", Type: "stall", Message: "recovered heartbeat gap"}}
	merged, err := Merge(base, next)
	if err != nil {
		t.Fatal(err)
	}
	if merged.Composition.PartsCount != 2 || len(merged.Composition.Parts) != 2 {
		t.Fatalf("composition = %+v, want two parts", merged.Composition)
	}
	if len(merged.Incidents) != 1 || merged.Incidents[0].ID != "warn-001" {
		t.Fatalf("incidents = %+v, want preserved incident", merged.Incidents)
	}
}
