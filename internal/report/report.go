// Package report renders a run's verdicts into machine- and human-readable
// forms. Provenance is stamped into every report (5273 / decisions/0004).
package report

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/openshift-storage-cert/storage-cert-harness/internal/core"
	"github.com/openshift-storage-cert/storage-cert-harness/internal/safefs"
)

// Build assembles a Report from verdicts and provenance, computing the summary,
// the per-level rollup, and the certified level. Each verdict's Level (stamped by
// the orchestrator) drives the rollup. See ADR-0014.
func Build(runID, schemaVersion string, prov core.Provenance, verdicts []core.Verdict) core.Report {
	r := core.Report{
		RunID:         runID,
		Status:        core.RunStatusComplete,
		Health:        core.RunHealthHealthy,
		SchemaVersion: schemaVersion,
		Provenance:    prov,
		Composition: core.RunComposition{
			PartsCount: 1,
			Parts:      []core.RunPart{{PartID: runID, Kind: "initial", Status: core.RunStatusComplete}},
		},
		Verdicts: verdicts,
	}
	r.Summary.Total = len(verdicts)
	for _, v := range verdicts {
		switch v.Outcome {
		case core.OutcomePass:
			r.Summary.Pass++
		case core.OutcomeFail:
			r.Summary.Fail++
		case core.OutcomeError:
			r.Summary.Error++
		case core.OutcomeSkip:
			r.Summary.Skip++
		}
	}
	r.Levels, r.Summary.CertifiedLevel = rollupLevels(verdicts)
	RefreshLifecycle(&r)
	return r
}

// NewRunning creates the durable report shell written before tool execution
// begins. It is intentionally useful without any verdicts: if the process is
// killed later, this file still identifies the selected work and run part.
func NewRunning(runID, schemaVersion string, prov core.Provenance, selected []core.TestRequirement, kind, startedAt string) core.Report {
	if startedAt == "" {
		startedAt = time.Now().UTC().Format(time.RFC3339)
	}
	r := core.Report{
		RunID:         runID,
		Status:        core.RunStatusRunning,
		Health:        core.RunHealthHealthy,
		Certifiable:   false,
		SchemaVersion: schemaVersion,
		Provenance:    prov,
		Composition: core.RunComposition{
			PartsCount: 1,
			Parts:      []core.RunPart{{PartID: runID, Kind: kind, StartedAt: startedAt, Status: core.RunStatusRunning}},
		},
	}
	for _, tr := range selected {
		run := core.RunExecution{TR: tr.ID, Variant: tr.Variant, State: core.ExecutionQueued}
		r.Executions = append(r.Executions, run)
	}
	return r
}

// RefreshLifecycle derives the run-health and certification flags from the
// lifecycle status, verdict summary, and incidents. A completed run may be
// degraded yet still certifiable when incidents are explicitly non-blocking.
func RefreshLifecycle(r *core.Report) {
	if r == nil {
		return
	}
	if r.Status == "" {
		r.Status = core.RunStatusComplete
	}
	if len(r.Composition.Parts) == 0 && r.RunID != "" {
		r.Composition.Parts = []core.RunPart{{PartID: r.RunID, Kind: "initial", Status: r.Status}}
	}
	r.Composition.PartsCount = len(r.Composition.Parts)
	if r.Status == core.RunStatusRunning {
		r.Health = core.RunHealthHealthy
		if len(r.Incidents) > 0 {
			r.Health = core.RunHealthDegraded
		}
		r.Certifiable = false
		return
	}
	if r.Status == core.RunStatusComplete {
		r.Health = core.RunHealthHealthy
		for _, incident := range r.Incidents {
			r.Health = core.RunHealthDegraded
			if incident.BlocksCertification {
				r.Certifiable = false
				return
			}
		}
		r.Certifiable = r.Summary.Fail == 0 && r.Summary.Error == 0
		return
	}
	r.Health = core.RunHealthUnhealthy
	r.Certifiable = false
}

// Finalize marks a snapshot terminal and updates its current run part.
func Finalize(r *core.Report, status core.RunStatus, reason, endedAt string) {
	if r == nil {
		return
	}
	if endedAt == "" {
		endedAt = time.Now().UTC().Format(time.RFC3339)
	}
	r.Status = status
	r.CompletionReason = reason
	if len(r.Composition.Parts) == 0 {
		r.Composition.Parts = []core.RunPart{{PartID: r.RunID}}
	}
	part := &r.Composition.Parts[len(r.Composition.Parts)-1]
	part.Status = status
	part.EndedAt = endedAt
	part.Reason = reason
	r.Composition.PartsCount = len(r.Composition.Parts)
	RefreshLifecycle(r)
}

// rollupLevels groups verdicts by partner level and computes the certified level.
//
// A level passes iff it has at least one pass and no fail/error; a skip-only level
// is "not evaluated". CertifiedLevel is the highest *tested* level that passed,
// with no lower tested level failing; levels that were not tested are ignored (not
// treated as failures). Today every harness TR is Level 2, and Level 1 is a
// separate external suite (openshift-origin-csi), so a strict cumulative ladder
// across all three levels is intentionally NOT enforced here — see ADR-0014.
func rollupLevels(verdicts []core.Verdict) ([]core.LevelRollup, int) {
	type acc struct{ pass, failErr bool }
	per := map[int]*acc{}
	for _, v := range verdicts {
		lvl := v.Level
		if lvl == 0 {
			continue
		}
		a := per[lvl]
		if a == nil {
			a = &acc{}
			per[lvl] = a
		}
		switch v.Outcome {
		case core.OutcomePass:
			a.pass = true
		case core.OutcomeFail, core.OutcomeError:
			a.failErr = true
		}
	}
	if len(per) == 0 {
		return nil, 0
	}
	levels := make([]int, 0, len(per))
	for l := range per {
		levels = append(levels, l)
	}
	sort.Ints(levels)

	outcome := func(a *acc) core.Outcome {
		switch {
		case a.failErr:
			return core.OutcomeFail
		case a.pass:
			return core.OutcomePass
		default:
			return core.OutcomeSkip
		}
	}
	var rollups []core.LevelRollup
	for _, l := range levels {
		rollups = append(rollups, core.LevelRollup{Level: l, Outcome: outcome(per[l])})
	}
	// Walk tested levels ascending: a pass raises the certified level; a fail/error
	// at a tested level stops the ladder; a skip-only level is ignored.
	certified := 0
	for _, l := range levels {
		switch outcome(per[l]) {
		case core.OutcomePass:
			certified = l
		case core.OutcomeFail:
			return rollups, certified
		}
	}
	return rollups, certified
}

// ReadJSON loads a previously written report.json. Used by `run --continue-from`
// to reuse a prior run's environment and append to its verdicts. See ADR-0014.
func ReadJSON(path string) (core.Report, error) {
	b, err := safefs.ReadFile(path)
	if err != nil {
		return core.Report{}, fmt.Errorf("read report %s: %w", path, err)
	}
	var r core.Report
	if err := json.Unmarshal(b, &r); err != nil {
		return core.Report{}, fmt.Errorf("read report %s: %w", path, err)
	}
	return r, nil
}

// Merge combines a base report with a later one into a single certification
// snapshot: it unions their verdicts and measurements, re-derives the summary /
// level rollup / certified level, and carries one shared Environment and
// Attestations set. Used by `run --continue-from` so an idle run and an
// under-pressure run of the same cluster produce one report without collecting
// the environment twice. Verdicts (and measurements) are keyed by TR + item +
// scenario + variant, so distinct executions coexist while a re-run of the same execution
// replaces the earlier result. It is an error to merge reports from different KB
// commits (a report must describe one requirements origin). See ADR-0014.
func Merge(base, next core.Report) (core.Report, error) {
	bc, nc := base.Provenance.KBGitCommit, next.Provenance.KBGitCommit
	if bc != "" && nc != "" && bc != nc {
		return core.Report{}, fmt.Errorf("cannot merge reports from different KB commits (%s vs %s)", bc, nc)
	}

	verdicts := dedupeVerdicts(append(append([]core.Verdict{}, base.Verdicts...), next.Verdicts...))

	prov := base.Provenance
	if prov.KBGitCommit == "" {
		prov = next.Provenance
	}
	schema := base.SchemaVersion
	if schema == "" {
		schema = next.SchemaVersion
	}

	merged := Build(base.RunID, schema, prov, verdicts)
	merged.Measurements = dedupeMeasurements(append(append([]core.Measurement{}, base.Measurements...), next.Measurements...))
	merged.Composition = mergeComposition(base, next)
	merged.Executions = mergeExecutions(base.Executions, next.Executions)
	merged.StorageSelections = mergeStorageSelections(base.StorageSelections, next.StorageSelections)
	merged.Incidents = mergeIncidents(base.Incidents, next.Incidents)
	merged.Warnings = append(append([]string{}, base.Warnings...), next.Warnings...)
	merged.Status = next.Status
	if merged.Status == "" {
		merged.Status = core.RunStatusComplete
	}
	merged.CompletionReason = next.CompletionReason

	merged.Environment = base.Environment
	if emptyEnv(base.Environment) {
		merged.Environment = next.Environment
	}
	merged.Attestations = base.Attestations
	if len(merged.Attestations) == 0 {
		merged.Attestations = next.Attestations
	}
	RefreshLifecycle(&merged)
	return merged, nil
}

func mergeComposition(base, next core.Report) core.RunComposition {
	parts := make([]core.RunPart, 0, len(base.Composition.Parts)+len(next.Composition.Parts))
	seen := map[string]bool{}
	add := func(r core.Report) {
		if len(r.Composition.Parts) == 0 && r.RunID != "" {
			status := r.Status
			if status == "" {
				status = core.RunStatusComplete
			}
			r.Composition.Parts = []core.RunPart{{PartID: r.RunID, Status: status}}
		}
		for _, p := range r.Composition.Parts {
			if p.PartID == "" {
				continue
			}
			if seen[p.PartID] {
				for i := range parts {
					if parts[i].PartID == p.PartID {
						parts[i] = p
					}
				}
				continue
			}
			seen[p.PartID] = true
			parts = append(parts, p)
		}
	}
	add(base)
	add(next)
	return core.RunComposition{PartsCount: len(parts), Parts: parts}
}

func mergeExecutions(base, next []core.RunExecution) []core.RunExecution {
	byKey := map[string]core.RunExecution{}
	for _, e := range append(append([]core.RunExecution{}, base...), next...) {
		byKey[e.TR+"\x00"+e.Variant+"\x00"+e.Scenario] = e
	}
	out := make([]core.RunExecution, 0, len(byKey))
	for _, e := range byKey {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].TR != out[j].TR {
			return out[i].TR < out[j].TR
		}
		if out[i].Variant != out[j].Variant {
			return out[i].Variant < out[j].Variant
		}
		return out[i].Scenario < out[j].Scenario
	})
	return out
}

func mergeIncidents(base, next []core.Incident) []core.Incident {
	seen := map[string]bool{}
	var out []core.Incident
	for _, incident := range append(append([]core.Incident{}, base...), next...) {
		key := incident.ID
		if key == "" {
			key = incident.Type + "\x00" + incident.TR + "\x00" + incident.Message
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, incident)
	}
	return out
}

func dedupeVerdicts(vs []core.Verdict) []core.Verdict {
	seen := make(map[string]int, len(vs))
	out := make([]core.Verdict, 0, len(vs))
	for _, v := range vs {
		key := v.TR + "\x00" + v.Variant + "\x00" + v.Item + "\x00" + v.Scenario
		if i, ok := seen[key]; ok {
			out[i] = v // later run wins for the same scenario
			continue
		}
		seen[key] = len(out)
		out = append(out, v)
	}
	sort.Slice(out, func(i, k int) bool {
		if out[i].TR != out[k].TR {
			return out[i].TR < out[k].TR
		}
		if out[i].Variant != out[k].Variant {
			return out[i].Variant < out[k].Variant
		}
		if out[i].Scenario != out[k].Scenario {
			return out[i].Scenario < out[k].Scenario
		}
		return out[i].Item < out[k].Item
	})
	return out
}

func dedupeMeasurements(ms []core.Measurement) []core.Measurement {
	seen := make(map[string]int, len(ms))
	out := make([]core.Measurement, 0, len(ms))
	for _, m := range ms {
		key := m.TR + "\x00" + m.Variant + "\x00" + m.Name + "\x00" + m.Percentile + "\x00" + m.Scenario
		if i, ok := seen[key]; ok {
			out[i] = m
			continue
		}
		seen[key] = len(out)
		out = append(out, m)
	}
	sort.Slice(out, func(i, k int) bool {
		if out[i].TR != out[k].TR {
			return out[i].TR < out[k].TR
		}
		if out[i].Variant != out[k].Variant {
			return out[i].Variant < out[k].Variant
		}
		if out[i].Scenario != out[k].Scenario {
			return out[i].Scenario < out[k].Scenario
		}
		if out[i].Name != out[k].Name {
			return out[i].Name < out[k].Name
		}
		return out[i].Percentile < out[k].Percentile
	})
	return out
}

// emptyEnv reports whether an Environment carries no collected facts, so Merge can
// fall back to the other report's environment.
func emptyEnv(e core.Environment) bool {
	p := e.Platform
	return e.Hardware.Nodes == 0 && len(e.Hardware.Facts) == 0 &&
		p.OCPVersion == "" && p.CSIDriver.Name == "" && p.Backend == "" &&
		len(p.StorageClasses) == 0 && len(p.Facts) == 0
}

// WriteJSON renders the report as indented JSON.
func WriteJSON(w io.Writer, r core.Report) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false) // keep "<=" readable in reports
	return enc.Encode(r)
}

// WriteMarkdown renders a human-readable summary.
func WriteMarkdown(w io.Writer, r core.Report) error {
	var b strings.Builder
	fmt.Fprintf(&b, "# Certification report %s\n\n", r.RunID)
	fmt.Fprintf(&b, "- Status: %s\n", r.Status)
	fmt.Fprintf(&b, "- Health: %s\n", r.Health)
	fmt.Fprintf(&b, "- Certifiable: %t\n", r.Certifiable)
	fmt.Fprintf(&b, "- Provenance: schema %s", r.SchemaVersion)
	if r.Provenance.KBGitCommit != "" {
		fmt.Fprintf(&b, ", KB commit %s", r.Provenance.KBGitCommit)
	}
	if r.Provenance.GeneratedDate != "" {
		fmt.Fprintf(&b, ", generated %s", r.Provenance.GeneratedDate)
	}
	b.WriteString("\n")
	if p := r.Environment.Platform; p.OCPVersion != "" || p.CSIDriver.Name != "" {
		fmt.Fprintf(&b, "- Environment: OCP %s, CNV %s, driver %s (%d nodes)\n",
			p.OCPVersion, p.CNVVersion, p.CSIDriver.Name, r.Environment.Hardware.Nodes)
	}
	fmt.Fprintf(&b, "- Certified level: %d\n", r.Summary.CertifiedLevel)
	fmt.Fprintf(&b, "- Totals: %d pass / %d fail / %d error / %d skip (of %d)\n\n",
		r.Summary.Pass, r.Summary.Fail, r.Summary.Error, r.Summary.Skip, r.Summary.Total)
	if len(r.StorageSelections) > 0 {
		b.WriteString("## Snapshot storage\n\n| TR | Scenario | Storage class | Snapshot class | Effective storage class |\n|----|----------|---------------|----------------|-------------------------|\n")
		for _, s := range r.StorageSelections {
			fmt.Fprintf(&b, "| %s | %s | %s | %s | %s |\n", core.ExecutionLabel(s.TR, s.Variant), s.Scenario, s.StorageClass, s.SnapshotClass, s.EffectiveStorageClass)
		}
		b.WriteString("\n")
	}
	if len(r.Composition.Parts) > 0 {
		b.WriteString("## Run composition\n\n")
		fmt.Fprintf(&b, "Composed from %d run part(s).\n\n", r.Composition.PartsCount)
		b.WriteString("| Part | Kind | Status | Started | Ended | Reason |\n")
		b.WriteString("|------|------|--------|---------|-------|--------|\n")
		for _, p := range r.Composition.Parts {
			fmt.Fprintf(&b, "| %s | %s | %s | %s | %s | %s |\n", p.PartID, p.Kind, p.Status, p.StartedAt, p.EndedAt, p.Reason)
		}
		b.WriteString("\n")
	}
	if len(r.Incidents) > 0 {
		b.WriteString("## Run incidents\n\n")
		b.WriteString("| ID | Severity | Type | TR | Stage | Resolved | Message | Impact |\n")
		b.WriteString("|----|----------|------|----|-------|----------|---------|--------|\n")
		for _, incident := range r.Incidents {
			fmt.Fprintf(&b, "| %s | %s | %s | %s | %s | %t | %s | %s |\n", incident.ID, incident.Severity, incident.Type, incident.TR, incident.Stage, incident.Resolved, incident.Message, incident.Impact)
		}
		b.WriteString("\n")
	}
	scenarios := false
	for _, v := range r.Verdicts {
		if v.Scenario != "" {
			scenarios = true
			break
		}
	}
	if scenarios {
		b.WriteString("| TR | Scenario | Item | Outcome | Duration | Expected | Actual | Reason |\n")
		b.WriteString("|----|----------|------|---------|----------|----------|--------|--------|\n")
		for _, v := range r.Verdicts {
			fmt.Fprintf(&b, "| %s | %s | %s | %s | %.1fs | %s | %s | %s |\n",
				core.ExecutionLabel(v.TR, v.Variant), v.Scenario, v.Item, v.Outcome, v.DurationS, v.Expected, v.Actual, v.Reason)
		}
	} else {
		b.WriteString("| TR | Item | Outcome | Duration | Expected | Actual | Reason |\n")
		b.WriteString("|----|------|---------|----------|----------|--------|--------|\n")
		for _, v := range r.Verdicts {
			fmt.Fprintf(&b, "| %s | %s | %s | %.1fs | %s | %s | %s |\n",
				core.ExecutionLabel(v.TR, v.Variant), v.Item, v.Outcome, v.DurationS, v.Expected, v.Actual, v.Reason)
		}
	}

	// Show selected measurements, including descriptors without an SLA bar.
	if len(r.Measurements) > 0 {
		b.WriteString("\n## Measurements\n\n")
		b.WriteString("| TR | Metric | Percentile | Value | Unit |\n")
		b.WriteString("|----|--------|------------|-------|------|\n")
		for _, m := range r.Measurements {
			fmt.Fprintf(&b, "| %s | %s | %s | %s | %s |\n",
				core.ExecutionLabel(m.TR, m.Variant), m.Name, m.Percentile, strconv.FormatFloat(m.Value, 'f', -1, 64), m.Unit)
		}
	}
	if len(r.Attestations) > 0 {
		b.WriteString("\n## Attestations\n\n")
		for _, a := range r.Attestations {
			if strings.HasPrefix(a.Claim, "storage_array_") {
				b.WriteString("The self-reported details below identify the CSI and storage-array combination covered by this report.\n\n")
				break
			}
		}
		if r.Attestations[0].SignedBy != "" {
			fmt.Fprintf(&b, "Signed by: %s\n\n", r.Attestations[0].SignedBy)
		}
		b.WriteString("| Claim | Value |\n")
		b.WriteString("|-------|-------|\n")
		for _, a := range r.Attestations {
			fmt.Fprintf(&b, "| %s | %s |\n", strings.NewReplacer(`\`, `\\`, "|", `\|`, "\r\n", "<br>", "\r", "<br>", "\n", "<br>").Replace(attestationLabel(a.Claim)), strings.NewReplacer(`\`, `\\`, "|", `\|`, "\r\n", "<br>", "\r", "<br>", "\n", "<br>").Replace(a.Value))
		}
	}

	if len(r.Warnings) > 0 {
		b.WriteString("\n## Warnings\n\n")
		for _, warning := range r.Warnings {
			fmt.Fprintf(&b, "- %s\n", warning)
		}
	}

	_, err := io.WriteString(w, b.String())
	return err
}

func attestationLabel(claim string) string {
	switch claim {
	case "storage_array_vendor":
		return "Storage array vendor"
	case "storage_array_family":
		return "Storage array product family"
	case "storage_array_model":
		return "Storage array model or deployment type"
	case "storage_array_software_version":
		return "Storage array software or firmware version"
	case "storage_array_protocol":
		return "Storage protocol or access mode"
	case "reference_architecture_url":
		return "Reference architecture URL"
	case "published_slas_url":
		return "Published SLAs URL"
	default:
		return strings.ReplaceAll(claim, "_", " ")
	}
}
