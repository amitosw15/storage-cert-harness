package main

import (
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"

	"gitlab.cee.redhat.com/eco-special-projects/storage-cert-harness/internal/core"
	"gitlab.cee.redhat.com/eco-special-projects/storage-cert-harness/internal/report"
)

// newSimulateReportCmd creates local report bundles without contacting a
// cluster. It is intentionally deterministic so the lifecycle and JUnit output
// can be inspected in CI and during report-consumer development.
func newSimulateReportCmd() *cobra.Command {
	var outputDir string
	cmd := &cobra.Command{
		Use:   "simulate-report",
		Short: "Generate clean, incident, partial, and abandoned report examples",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if outputDir == "" {
				return fmt.Errorf("--output is required")
			}
			logger := newLoggerTo(false, cmd.OutOrStdout(), nil)
			scenarios := map[string]core.Report{
				"clean-composed":         simulatedCleanReport(),
				"complete-with-incident": simulatedIncidentReport(),
				"running-checkpoint":     simulatedRunningReport(),
				"stalled-partial":        simulatedPartialReport(),
				"abandoned-recovered":    simulatedAbandonedReport(),
			}
			for name, rep := range scenarios {
				dir := filepath.Join(outputDir, name)
				if err := writeReportFiles(dir, rep, nil); err != nil {
					return fmt.Errorf("write %s: %w", name, err)
				}
				logger.Info("report generated", "scenario", name, "directory", dir)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&outputDir, "output", "", "directory to receive simulated report bundles")
	return cmd
}

func simulatedCleanReport() core.Report {
	r := report.Build("sim-clean", "2.0", core.Provenance{}, []core.Verdict{
		{TR: "TR-STOR-006", Level: 3, Scenario: "load80", Item: "check:restore", Outcome: core.OutcomePass},
		{TR: "TR-STOR-006", Level: 3, Scenario: "non-load80", Item: "check:restore", Outcome: core.OutcomePass},
	})
	r.Composition = core.RunComposition{PartsCount: 2, Parts: []core.RunPart{
		{PartID: "sim-clean-load80", Kind: "initial", StartedAt: "2026-09-12T10:00:00Z", EndedAt: "2026-09-12T10:30:00Z", Status: core.RunStatusComplete},
		{PartID: "sim-clean-non-load80", Kind: "resume", StartedAt: "2026-09-12T11:00:00Z", EndedAt: "2026-09-12T11:30:00Z", Status: core.RunStatusComplete},
	}}
	r.Executions = []core.RunExecution{{TR: "TR-STOR-006", State: core.ExecutionCompleted, Outcome: core.OutcomePass}}
	report.RefreshLifecycle(&r)
	return r
}

func simulatedIncidentReport() core.Report {
	r := simulatedCleanReport()
	r.RunID = "sim-incident"
	r.Composition.Parts[0].PartID = "sim-incident-load80"
	r.Composition.Parts[1].PartID = "sim-incident-non-load80"
	r.Incidents = []core.Incident{{
		ID: "stall-load80", Type: "stall", Severity: "error", TR: "TR-STOR-006", Stage: "collect",
		Message: "collector heartbeat exceeded 15m timeout", Resolved: true, BlocksCertification: true,
		Impact: "load80 was retried in the second run part", Evidence: "run.log:event-42",
	}}
	report.RefreshLifecycle(&r)
	return r
}

func simulatedPartialReport() core.Report {
	r := simulatedRunningReport()
	r.RunID = "sim-partial"
	r.Composition.Parts[0].PartID = "sim-partial"
	r.Incidents = []core.Incident{{
		ID: "stall-provision", Type: "stall", Severity: "error", TR: "TR-VIRT-004", Stage: "provision",
		Message: "no progress heartbeat", BlocksCertification: true, Impact: "one execution running and one queued",
	}}
	r.Verdicts = []core.Verdict{{TR: "TR-VIRT-001", Level: 3, Item: "check:boot", Outcome: core.OutcomePass}}
	r.Summary.Total = 1
	r.Summary.Pass = 1
	r.Levels = []core.LevelRollup{{Level: 3, Outcome: core.OutcomePass}}
	report.Finalize(&r, core.RunStatusPartial, "provision stalled", "2026-09-12T12:30:00Z")
	return r
}

func simulatedRunningReport() core.Report {
	r := report.NewRunning("sim-running", "2.0", core.Provenance{}, []core.TestRequirement{
		{ID: "TR-VIRT-001"}, {ID: "TR-VIRT-004"}, {ID: "TR-STOR-006"},
	}, "initial", "2026-09-12T12:00:00Z")
	r.Executions[0].State = core.ExecutionCompleted
	r.Executions[0].Outcome = core.OutcomePass
	r.Executions[0].EndedAt = "2026-09-12T12:10:00Z"
	r.Executions[1].State = core.ExecutionRunning
	r.Executions[1].Stage = "provision"
	r.Executions[1].StartedAt = "2026-09-12T12:10:00Z"
	r.Verdicts = []core.Verdict{{TR: "TR-VIRT-001", Level: 3, Item: "check:boot", Outcome: core.OutcomePass}}
	r.Summary.Total = 1
	r.Summary.Pass = 1
	r.Levels = []core.LevelRollup{{Level: 3, Outcome: core.OutcomePass}}
	return r
}

func simulatedAbandonedReport() core.Report {
	r := report.NewRunning("sim-abandoned", "2.0", core.Provenance{}, []core.TestRequirement{{ID: "TR-VIRT-001"}}, "initial", "2026-09-12T13:00:00Z")
	r.Executions[0].State = core.ExecutionRunning
	r.Executions[0].Stage = "run"
	r.Incidents = []core.Incident{{
		ID: "run-abandoned", Type: "abandoned", Severity: "error", Message: "process disappeared before finalization",
		BlocksCertification: true, Impact: "execution state recovered from the last checkpoint",
	}}
	report.Finalize(&r, core.RunStatusAbandoned, "process ended without finalizing the run", "2026-09-12T13:15:00Z")
	return r
}
