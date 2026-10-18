// Package orchestrator selects test requirements, groups them by automation
// tool, schedules the work honoring per-tool concurrency constraints, drives
// each tool's stage pipeline, and grades results. It depends only on the stage
// interfaces and the registry — never on a concrete tool. See decisions/0003,
// 0006.
package orchestrator

import (
	"context"
	"encoding/hex"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"gitlab.cee.redhat.com/eco-special-projects/storage-cert-harness/internal/config"
	"gitlab.cee.redhat.com/eco-special-projects/storage-cert-harness/internal/core"
	"gitlab.cee.redhat.com/eco-special-projects/storage-cert-harness/internal/grader"
	"gitlab.cee.redhat.com/eco-special-projects/storage-cert-harness/internal/registry"
	"gitlab.cee.redhat.com/eco-special-projects/storage-cert-harness/internal/report"
	"gitlab.cee.redhat.com/eco-special-projects/storage-cert-harness/internal/stages"
)

// job is one unit of scheduling: all selected TRs for a single automation tool.
type job struct {
	tool         string
	trs          []core.TestRequirement
	parallelSafe bool
	groups       []string
}

// Run selects and scores the given TRs, returning a graded report. TRs whose
// automation tool is empty/manual or is not a registered integration are scored
// as skip (not-scorable), never pass/fail. prov + schemaVersion are stamped into
// the report.
func Run(ctx context.Context, cfg config.Config, trs []core.TestRequirement, prov core.Provenance, schemaVersion string, rc *core.RunCtx) (core.Report, error) {
	selected, err := ResolveExecutions(trs, cfg)
	if err != nil {
		return core.Report{}, err
	}
	progress := newProgressTracker(rc.Logger, selected)

	var scorable []core.TestRequirement
	var verdicts []core.Verdict
	var measurements []core.Measurement
	var storageSelections []core.StorageSelection
	var resultsMu sync.Mutex
	externalStorageRecorder := rc.RecordStorageSelection
	rc.RecordStorageSelection = func(selection core.StorageSelection) {
		selection.Scenario = cfg.Scenario
		resultsMu.Lock()
		storageSelections = append(storageSelections, selection)
		resultsMu.Unlock()
		if externalStorageRecorder != nil {
			externalStorageRecorder(selection)
		}
	}
	var stateMu sync.Mutex
	var incidentMu sync.Mutex
	startedAt := time.Now().UTC().Format(time.RFC3339)
	partKind := rc.PartKind
	if partKind == "" {
		partKind = "initial"
	}
	executionStates := make(map[string]core.RunExecution, len(selected))
	for _, tr := range selected {
		label := core.ExecutionLabel(tr.ID, tr.Variant)
		executionStates[label] = core.RunExecution{TR: tr.ID, Variant: tr.Variant, Scenario: cfg.Scenario, State: core.ExecutionQueued}
	}
	incidents := []core.Incident{}

	levelOf := make(map[string]int, len(selected))
	for _, tr := range selected {
		levelOf[tr.ID] = tr.PartnerLevel
	}

	checkpointSnapshot := func(status core.RunStatus) core.Report {
		resultsMu.Lock()
		vs := append([]core.Verdict(nil), verdicts...)
		ms := append([]core.Measurement(nil), measurements...)
		ss := append([]core.StorageSelection(nil), storageSelections...)
		resultsMu.Unlock()
		for i := range vs {
			vs[i].Level = levelOf[vs[i].TR]
			vs[i].Scenario = cfg.Scenario
		}
		for i := range ms {
			ms[i].Level = levelOf[ms[i].TR]
			ms[i].Scenario = cfg.Scenario
		}
		r := report.Build(rc.RunID, schemaVersion, prov, vs)
		r.StorageSelections = ss
		sort.Slice(r.StorageSelections, func(i, j int) bool {
			return core.ExecutionLabel(ss[i].TR, ss[i].Variant) < core.ExecutionLabel(ss[j].TR, ss[j].Variant)
		})
		r.Status = status
		r.Composition = core.RunComposition{PartsCount: 1, Parts: []core.RunPart{{
			PartID: rc.RunID, Kind: partKind, StartedAt: startedAt, Status: status,
		}}}
		stateMu.Lock()
		for _, execution := range executionStates {
			r.Executions = append(r.Executions, execution)
		}
		stateMu.Unlock()
		sort.Slice(r.Executions, func(i, j int) bool {
			if r.Executions[i].TR != r.Executions[j].TR {
				return r.Executions[i].TR < r.Executions[j].TR
			}
			return r.Executions[i].Variant < r.Executions[j].Variant
		})
		incidentMu.Lock()
		r.Incidents = append([]core.Incident(nil), incidents...)
		incidentMu.Unlock()
		r.Measurements, r.Warnings = report.SelectMeasurements(selected, ms, cfg.ExtraMetrics, cfg.ReportMetrics)
		report.RefreshLifecycle(&r)
		return r
	}
	checkpoint := func(status core.RunStatus) {
		if rc.Checkpoint != nil {
			rc.Checkpoint(checkpointSnapshot(status))
		}
	}
	recordIncident := func(incident core.Incident) {
		incidentMu.Lock()
		if incident.ID == "" {
			incident.ID = fmt.Sprintf("incident-%03d", len(incidents)+1)
		}
		incidents = append(incidents, incident)
		incidentMu.Unlock()
	}
	externalIncidentRecorder := rc.RecordIncident
	rc.RecordIncident = func(incident core.Incident) {
		recordIncident(incident)
		if externalIncidentRecorder != nil {
			externalIncidentRecorder(incident)
		}
	}
	externalStageRecorder := rc.SetStage
	rc.SetStage = func(stage string, trs []core.TestRequirement) {
		stateMu.Lock()
		for _, tr := range trs {
			label := core.ExecutionLabel(tr.ID, tr.Variant)
			execution := executionStates[label]
			execution.Stage = stage
			executionStates[label] = execution
		}
		stateMu.Unlock()
		checkpoint(core.RunStatusRunning)
		if externalStageRecorder != nil {
			externalStageRecorder(stage, trs)
		}
	}
	externalArtifactRecorder := rc.RecordArtifacts
	rc.RecordArtifacts = func(trs []core.TestRequirement, refs []string) {
		rel, dropped := relativeTo(rc.WorkDir, refs)
		if len(dropped) > 0 {
			// A reference that is not a path under the working directory cannot
			// be published (an absolute one would leak the operator's home).
			// Some collectors legitimately return a handle rather than a file,
			// so this is debug rather than a warning, but a collector emitting
			// real paths from the wrong place is visible here instead of just
			// producing an execution with no artifacts.
			rc.Logger.Debug("artifact references not recorded; not under the working directory",
				"refs", dropped, "workdir", rc.WorkDir)
		}
		if len(rel) > 0 {
			stateMu.Lock()
			for _, tr := range trs {
				label := core.ExecutionLabel(tr.ID, tr.Variant)
				execution := executionStates[label]
				execution.Artifacts = append(execution.Artifacts, rel...)
				executionStates[label] = execution
			}
			stateMu.Unlock()
			// Persist immediately: a run that stops between collection and the
			// next stage change would otherwise leave a report that cannot say
			// which results had already been gathered.
			checkpoint(core.RunStatusRunning)
		}
		if externalArtifactRecorder != nil {
			externalArtifactRecorder(trs, refs)
		}
	}
	progress.onStateChange = func(label string, state progressState, outcome core.Outcome) {
		stateMu.Lock()
		execution := executionStates[label]
		switch state {
		case progressRunning:
			execution.State = core.ExecutionRunning
			execution.StartedAt = time.Now().UTC().Format(time.RFC3339)
		case progressComplete:
			execution.State = core.ExecutionCompleted
			execution.Outcome = outcome
			execution.Stage = "complete"
			execution.EndedAt = time.Now().UTC().Format(time.RFC3339)
		case progressAborted:
			execution.State = core.ExecutionAborted
			execution.Outcome = outcome
			execution.Reason = "run ended before execution completed"
			execution.EndedAt = time.Now().UTC().Format(time.RFC3339)
		}
		executionStates[label] = execution
		stateMu.Unlock()
		if state == progressRunning {
			checkpoint(core.RunStatusRunning)
		}
	}
	checkpoint(core.RunStatusRunning)
	for _, tr := range selected {
		if !runnable(tr) {
			verdicts = append(verdicts, notScorable(tr)...)
			progress.complete(tr, core.OutcomeSkip)
			continue
		}
		if missing := missingRequiredParams(tr); len(missing) > 0 {
			verdicts = append(verdicts, errTR(tr, fmt.Sprintf("required param(s) not supplied and have no default: %s (set them in the plan's overrides)", strings.Join(missing, ", ")))...)
			progress.complete(tr, core.OutcomeError)
			continue
		}
		scorable = append(scorable, tr)
	}
	checkpoint(core.RunStatusRunning)

	// Run scoped setup (run → group) once each before the job loop; a failed
	// setup errors its in-scope TRs and skips their jobs. Successful setups are
	// torn down once each in reverse order after all jobs (deferred). See ADR-0008.
	setups := activeSetups(scorable)
	var ranSetups []scopedSetup
	for _, s := range setups {
		rc.SetStage("setup:"+s.label, s.trs)
		if err := s.provider.Setup(ctx, rc, s.trs); err != nil {
			verdicts = append(verdicts, errTRs(s.trs, "setup ["+s.label+"]: "+err.Error())...)
			for _, tr := range s.trs {
				if ctx.Err() != nil {
					progress.abort(tr, core.OutcomeError)
				} else {
					progress.complete(tr, core.OutcomeError)
				}
			}
			scorable = withoutTRs(scorable, s.trs)
			continue
		}
		ranSetups = append(ranSetups, s)
	}
	checkpoint(core.RunStatusRunning)
	defer func() {
		for i := len(ranSetups) - 1; i >= 0; i-- {
			if err := ranSetups[i].provider.Teardown(ctx, rc, ranSetups[i].trs); err != nil {
				rc.Logger.Warn("shared setup teardown failed", "setup", ranSetups[i].label, "err", err)
			}
		}
	}()

	jobs := groupByTool(scorable, cfg.Scheduling)
	g := grader.New()

	schedule(cfg.Concurrency, jobs, func(j job) {
		vs, ms := runToolExecutions(ctx, j, rc, g, progress)
		resultsMu.Lock()
		verdicts = append(verdicts, vs...)
		measurements = append(measurements, ms...)
		resultsMu.Unlock()
		checkpoint(core.RunStatusRunning)
	})

	sort.Slice(verdicts, func(i, k int) bool {
		if verdicts[i].TR != verdicts[k].TR {
			return verdicts[i].TR < verdicts[k].TR
		}
		if verdicts[i].Variant != verdicts[k].Variant {
			return verdicts[i].Variant < verdicts[k].Variant
		}
		return verdicts[i].Item < verdicts[k].Item
	})
	sort.Slice(measurements, func(i, k int) bool {
		if measurements[i].TR != measurements[k].TR {
			return measurements[i].TR < measurements[k].TR
		}
		if measurements[i].Variant != measurements[k].Variant {
			return measurements[i].Variant < measurements[k].Variant
		}
		if measurements[i].Name != measurements[k].Name {
			return measurements[i].Name < measurements[k].Name
		}
		return measurements[i].Percentile < measurements[k].Percentile
	})
	// Stamp each verdict/measurement with its TR's partner level so the report can
	// roll up by level and compute the certified level (ADR-0014).
	for i := range verdicts {
		verdicts[i].Level = levelOf[verdicts[i].TR]
		verdicts[i].Scenario = cfg.Scenario
	}
	for i := range measurements {
		measurements[i].Level = levelOf[measurements[i].TR]
		measurements[i].Scenario = cfg.Scenario
	}
	stateMu.Lock()
	allTerminal := true
	for label, execution := range executionStates {
		if execution.State == core.ExecutionRunning {
			execution.State = core.ExecutionAborted
			execution.Reason = "run ended before execution completed"
			execution.EndedAt = time.Now().UTC().Format(time.RFC3339)
			executionStates[label] = execution
		}
		if executionStates[label].State != core.ExecutionCompleted && executionStates[label].State != core.ExecutionAborted {
			allTerminal = false
		}
	}
	stateMu.Unlock()
	status := core.RunStatusComplete
	reason := ""
	if ctx.Err() != nil {
		status = core.RunStatusPartial
		reason = ctx.Err().Error()
		recordIncident(core.Incident{ID: "run-cancelled", Type: "cancellation", Severity: "error", Message: ctx.Err().Error(), StartedAt: time.Now().UTC().Format(time.RFC3339), BlocksCertification: true, Impact: "run did not complete all selected executions"})
	} else if !allTerminal {
		status = core.RunStatusPartial
		reason = "run ended with queued executions"
		recordIncident(core.Incident{ID: "run-incomplete", Type: "incomplete", Severity: "error", Message: reason, BlocksCertification: true, Impact: "selected executions were not completed"})
	}
	rep := checkpointSnapshot(status)
	if reason != "" {
		report.Finalize(&rep, status, reason, time.Now().UTC().Format(time.RFC3339))
	} else {
		report.Finalize(&rep, status, "", time.Now().UTC().Format(time.RFC3339))
	}
	for _, warning := range rep.Warnings {
		if rc.Logger != nil {
			rc.Logger.Warn(warning)
		}
	}
	return rep, nil
}

// runnable reports whether a TR has a usable, registered automation tool.
func runnable(tr core.TestRequirement) bool {
	if tr.AutomationTool == "" || tr.AutomationTool == core.CheckManual {
		return false
	}
	_, ok := resolveTool(tr)
	return ok
}

// ResolveTool binds a TR to its integration: an exact automation_tool == Name
// match first, then the integration that declares it Provides the TR id. Exported
// so CLI subcommands (preflight) resolve tools identically to a run. See
// registry.ForTRID.
func ResolveTool(tr core.TestRequirement) (stages.ToolIntegration, bool) {
	if t, ok := registry.Get(tr.AutomationTool); ok {
		return t, true
	}
	return registry.ForTRID(tr.ID)
}

func resolveTool(tr core.TestRequirement) (stages.ToolIntegration, bool) { return ResolveTool(tr) }

// SelectTRs applies the filter to a TR list. Exported for CLI subcommands (list,
// preflight) that need the same selection semantics as a run.
func SelectTRs(all []core.TestRequirement, f config.Filter) []core.TestRequirement {
	var out []core.TestRequirement
	for _, tr := range all {
		if len(f.IDs) > 0 && !slices.Contains(f.IDs, tr.ID) {
			continue
		}
		if len(f.Tools) > 0 && !slices.Contains(f.Tools, tr.AutomationTool) {
			continue
		}
		if len(f.PartnerLevels) > 0 && !slices.Contains(f.PartnerLevels, tr.PartnerLevel) {
			continue
		}
		out = append(out, tr)
	}
	return out
}

// ResumeExecutions removes executions that already reached a terminal completed
// state in a prior report. It is the true continuation mode: failed results are
// preserved rather than silently rerun. Use ResumeExecutionsWithOptions with
// retryFailed when a caller explicitly wants to retry fail/error results.
func ResumeExecutions(selected []core.TestRequirement, prior core.Report, scenario string) []core.TestRequirement {
	return ResumeExecutionsWithOptions(selected, prior, scenario, false)
}

// ResumeExecutionsWithOptions is ResumeExecutions with an explicit failed-test
// retry policy. Scenario is part of the identity: a load80 execution must not
// satisfy a non-load80 continuation.
func ResumeExecutionsWithOptions(selected []core.TestRequirement, prior core.Report, scenario string, retryFailed bool) []core.TestRequirement {
	completed := map[string]bool{}
	failed := map[string]bool{}
	knownExecutions := map[string]bool{}
	for _, execution := range prior.Executions {
		if execution.Scenario != scenario {
			continue
		}
		key := executionKey(execution.TR, execution.Variant, execution.Scenario)
		knownExecutions[key] = true
		if execution.State == core.ExecutionCompleted {
			completed[key] = true
			if execution.Outcome == core.OutcomeFail || execution.Outcome == core.OutcomeError {
				failed[key] = true
			}
		}
	}

	// Reports written before persisted execution state existed can still resume
	// safely: any item-level verdict means that execution reached a terminal
	// result in that report.
	for _, verdict := range prior.Verdicts {
		if verdict.Scenario != scenario {
			continue
		}
		key := executionKey(verdict.TR, verdict.Variant, verdict.Scenario)
		// New reports persist execution state for every selected item. An
		// aborted/queued/running execution may already have cancellation
		// verdicts, but it must remain resumable. Use legacy verdict fallback
		// only when no execution record exists.
		if knownExecutions[key] {
			continue
		}
		completed[key] = true
		switch verdict.Outcome {
		case core.OutcomeFail, core.OutcomeError:
			failed[key] = true
		}
	}

	out := make([]core.TestRequirement, 0, len(selected))
	for _, tr := range selected {
		key := executionKey(tr.ID, tr.Variant, scenario)
		skip := completed[key] && (!retryFailed || !failed[key])
		if !skip {
			out = append(out, tr)
		}
	}
	return out
}

func executionKey(tr, variant, scenario string) string {
	return tr + "\x00" + variant + "\x00" + scenario
}

// missingRequiredParams returns the names of a TR's required params that have no
// resolved value (no catalog default and no plan override). Sorted for a stable
// message. See decisions/0013.
func missingRequiredParams(tr core.TestRequirement) []string {
	var missing []string
	for name, spec := range tr.ParamSpec {
		if !spec.Required {
			continue
		}
		if _, ok := tr.Params[name]; !ok {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	return missing
}

// ApplyOverrides applies plan params without mutating the source catalog.
func ApplyOverrides(trs []core.TestRequirement, overrides map[string]map[string]any) []core.TestRequirement {
	return applyOverrides(trs, overrides)
}

func applyOverrides(trs []core.TestRequirement, overrides map[string]map[string]any) []core.TestRequirement {
	if len(overrides) == 0 {
		return trs
	}
	out := make([]core.TestRequirement, len(trs))
	copy(out, trs)
	for i := range out {
		ov, ok := overrides[out[i].ID]
		if !ok || len(ov) == 0 {
			continue
		}
		merged := make(map[string]any, len(out[i].Params)+len(ov))
		maps.Copy(merged, out[i].Params)
		maps.Copy(merged, ov)
		out[i].Params = merged
	}
	return out
}

// scopedSetup is one SetupProvider to run, with the TR subset it applies to and a
// human label for diagnostics.
type scopedSetup struct {
	provider stages.SetupProvider
	label    string
	trs      []core.TestRequirement
}

// activeSetups returns the setup providers to run, ordered run-scope first then
// group-scope (sorted by key). Run-scope providers apply to all runnable TRs;
// each group-scope provider applies to the runnable TRs whose tool lists that
// group in SetupGroups. Only groups that have a registered provider are returned.
func activeSetups(scorable []core.TestRequirement) []scopedSetup {
	if len(scorable) == 0 {
		return nil
	}
	var out []scopedSetup
	for _, p := range registry.RunSetupProviders() {
		out = append(out, scopedSetup{provider: p, label: "run/" + p.SetupInfo().Key, trs: scorable})
	}
	groupTRs := map[string][]core.TestRequirement{}
	var order []string
	for _, tr := range scorable {
		ti, ok := resolveTool(tr)
		if !ok {
			continue
		}
		for _, gkey := range ti.SetupGroups {
			if _, seen := groupTRs[gkey]; !seen {
				order = append(order, gkey)
			}
			groupTRs[gkey] = append(groupTRs[gkey], tr)
		}
	}
	sort.Strings(order)
	for _, gkey := range order {
		p, ok := registry.SetupProvider(stages.ScopeGroup, gkey)
		if !ok {
			continue
		}
		out = append(out, scopedSetup{provider: p, label: "group/" + gkey, trs: groupTRs[gkey]})
	}
	return out
}

// withoutTRs returns trs with any TR whose id appears in remove dropped.
func withoutTRs(trs, remove []core.TestRequirement) []core.TestRequirement {
	drop := make(map[string]bool, len(remove))
	for _, tr := range remove {
		drop[tr.ID] = true
	}
	var out []core.TestRequirement
	for _, tr := range trs {
		if !drop[tr.ID] {
			out = append(out, tr)
		}
	}
	return out
}

func groupByTool(trs []core.TestRequirement, sched map[string]config.ToolSchedule) []job {
	byTool := map[string][]core.TestRequirement{}
	order := []string{}
	for _, tr := range trs {
		if _, ok := byTool[tr.AutomationTool]; !ok {
			order = append(order, tr.AutomationTool)
		}
		byTool[tr.AutomationTool] = append(byTool[tr.AutomationTool], tr)
	}
	var jobs []job
	for _, tool := range order {
		ti, _ := resolveTool(byTool[tool][0]) // guaranteed resolvable (runnable filtered)
		j := job{tool: tool, trs: byTool[tool], parallelSafe: ti.ParallelSafe}
		groups := map[string]bool{}
		for _, gr := range ti.ExclusivityGroups {
			groups[gr] = true
		}
		if ov, ok := sched[tool]; ok {
			if ov.ParallelSafe != nil {
				j.parallelSafe = *ov.ParallelSafe
			}
			if ov.ExclusivityGroups != nil {
				groups = map[string]bool{}
				for _, gr := range ov.ExclusivityGroups {
					groups[gr] = true
				}
			}
		}
		for gr := range groups {
			j.groups = append(j.groups, gr)
		}
		sort.Strings(j.groups)
		jobs = append(jobs, j)
	}
	return jobs
}

// schedule runs jobs with a max concurrency bound, running non-parallel-safe
// jobs alone (exclusive) and never overlapping jobs that share an exclusivity
// group. See decisions/0003 (--concurrency + parallel_safe/exclusivity_groups).
func schedule(concurrency int, jobs []job, run func(job)) {
	if concurrency < 1 {
		concurrency = 1
	}
	sem := make(chan struct{}, concurrency)
	var runLock sync.RWMutex // exclusive jobs take Lock; parallel jobs take RLock

	var gmMu sync.Mutex
	groupMu := map[string]*sync.Mutex{}
	group := func(name string) *sync.Mutex {
		gmMu.Lock()
		defer gmMu.Unlock()
		m, ok := groupMu[name]
		if !ok {
			m = &sync.Mutex{}
			groupMu[name] = m
		}
		return m
	}

	var wg sync.WaitGroup
	for _, j := range jobs {
		wg.Add(1)
		go func(j job) {
			defer wg.Done()
			if !j.parallelSafe {
				runLock.Lock() // runs alone
				defer runLock.Unlock()
				run(j)
				return
			}
			sem <- struct{}{}
			defer func() { <-sem }()
			runLock.RLock()
			defer runLock.RUnlock()
			for _, gr := range j.groups { // sorted → consistent lock order
				m := group(gr)
				m.Lock()
				defer m.Unlock()
			}
			run(j)
		}(j)
	}
	wg.Wait()
}

// runJob drives one tool's stage pipeline over its selected TRs and grades the
// results. Teardown always runs. Context cancellation short-circuits.
// relativeTo rewrites artifact paths against the run's working directory. An
// absolute path would pin the report to one operator's home directory, and the
// report is published evidence, so a path that will not relativize is dropped
// rather than recorded.
func relativeTo(workdir string, paths []string) (kept, dropped []string) {
	if workdir == "" {
		return nil, paths
	}
	for _, p := range paths {
		abs, err := filepath.Abs(p)
		if err != nil {
			dropped = append(dropped, p)
			continue
		}
		rel, err := filepath.Rel(workdir, abs)
		if err != nil || strings.HasPrefix(rel, "..") {
			dropped = append(dropped, p)
			continue
		}
		kept = append(kept, filepath.ToSlash(rel))
	}
	return kept, dropped
}

func runJob(ctx context.Context, j job, rc *core.RunCtx, g *grader.Grader) (verdicts []core.Verdict, measurements []core.Measurement) {
	// Time the whole tool run for this job and stamp it onto every verdict, so the
	// report always shows how long the test took — with or without an SLA bar.
	start := time.Now()
	defer func() {
		d := time.Since(start).Seconds()
		for i := range verdicts {
			verdicts[i].DurationS = d
		}
	}()
	if err := ctx.Err(); err != nil {
		return errTRs(j.trs, "cancelled: "+err.Error()), nil
	}
	ti, ok := resolveTool(j.trs[0])
	if !ok {
		return errTRs(j.trs, fmt.Sprintf("tool %q not registered", j.tool)), nil
	}
	bag := core.NewBag()

	if ti.Teardown != nil {
		defer func() {
			rc.SetStage("teardown", j.trs)
			_ = ti.Teardown.Teardown(ctx, rc, bag)
		}()
	}

	if ti.Preflight != nil {
		rc.SetStage("preflight", j.trs)
		findings, err := ti.Preflight.Check(ctx, rc, bag, j.trs)
		if err != nil {
			return errTRs(j.trs, "preflight: "+err.Error()), nil
		}
		for _, f := range findings {
			if f.Level == "error" {
				return errTRs(j.trs, "preflight failed: "+f.Message), nil
			}
		}
	}
	if ti.Provisioner != nil {
		rc.SetStage("provision", j.trs)
		if err := ti.Provisioner.Provision(ctx, rc, bag, j.trs); err != nil {
			return errTRs(j.trs, "provision: "+err.Error()), nil
		}
	}
	var handle stages.RunHandle
	if ti.Runner != nil {
		rc.SetStage("run", j.trs)
		h, err := ti.Runner.Run(ctx, rc, bag, j.trs)
		if err != nil {
			return errTRs(j.trs, "run: "+err.Error()), nil
		}
		handle = h
	}
	var logs core.LogBundle
	if ti.LogCollector != nil {
		rc.SetStage("collect", j.trs)
		lb, err := ti.LogCollector.Collect(ctx, rc, bag, handle)
		if err != nil {
			return errTRs(j.trs, "collect: "+err.Error()), nil
		}
		if rc.RecordArtifacts != nil {
			rc.RecordArtifacts(j.trs, lb.Refs)
		}
		logs = lb
	}
	var results []core.TestResult
	if ti.ResultParser != nil {
		rc.SetStage("parse", j.trs)
		rs, err := ti.ResultParser.Parse(ctx, rc, bag, logs)
		if err != nil {
			return errTRs(j.trs, "parse: "+err.Error()), nil
		}
		results = rs
	}

	byID := map[string]core.TestResult{}
	for _, r := range results {
		byID[r.TRID] = r
	}
	for _, tr := range j.trs {
		r, ok := byID[tr.ID]
		if !ok {
			verdicts = append(verdicts, errTR(tr, "no result produced for test requirement")...)
			continue
		}
		vs := g.GradeTR(ctx, tr, r, ti.Evaluators)
		for i := range vs {
			vs[i].Variant = tr.Variant
		}
		verdicts = append(verdicts, vs...)
		// Surface every measured metric, whether or not an SLA graded it (ADR-0014).
		for _, m := range r.Metrics {
			measurements = append(measurements, core.Measurement{
				TR: tr.ID, Variant: tr.Variant, Name: m.Name, Value: m.Value, Unit: m.Unit, Percentile: m.Percentile,
			})
		}
	}
	return verdicts, measurements
}

// notScorable yields skip verdicts for a TR with no usable automation tool.
func notScorable(tr core.TestRequirement) []core.Verdict {
	reason := "no automation tool"
	if tr.AutomationTool == core.CheckManual {
		reason = "manual: requires human sign-off"
	} else if tr.AutomationTool != "" {
		reason = fmt.Sprintf("automation tool %q not integrated", tr.AutomationTool)
	}
	return itemVerdicts(tr, core.OutcomeSkip, reason)
}

func errTRs(trs []core.TestRequirement, reason string) []core.Verdict {
	var out []core.Verdict
	for _, tr := range trs {
		out = append(out, errTR(tr, reason)...)
	}
	return out
}

func errTR(tr core.TestRequirement, reason string) []core.Verdict {
	return itemVerdicts(tr, core.OutcomeError, reason)
}

// itemVerdicts emits one verdict per scorable item (each sla + each check) with
// the given outcome/reason; a single "(none)" verdict if the TR has neither.
func itemVerdicts(tr core.TestRequirement, outcome core.Outcome, reason string) []core.Verdict {
	var out []core.Verdict
	for _, s := range tr.SLAs {
		item := "sla:" + s.Metric
		if s.Percentile != "" {
			item += "@" + s.Percentile
		}
		out = append(out, core.Verdict{TR: tr.ID, Variant: tr.Variant, Item: item, Outcome: outcome, Reason: reason})
	}
	for _, c := range tr.Checks {
		out = append(out, core.Verdict{TR: tr.ID, Variant: tr.Variant, Item: "check:" + c.ID, Outcome: outcome, Reason: reason})
	}
	if len(out) == 0 {
		out = append(out, core.Verdict{TR: tr.ID, Variant: tr.Variant, Item: "(none)", Outcome: outcome, Reason: reason})
	}
	return out
}

// ResolveExecutions selects TRs and applies catalog < plan < variant precedence.
// Variant entries replace the default execution; unknown catalog IDs are errors.
func ResolveExecutions(trs []core.TestRequirement, cfg config.Config) ([]core.TestRequirement, error) {
	known := map[string]bool{}
	for _, tr := range trs {
		known[tr.ID] = true
	}
	for id := range cfg.Variants {
		if !known[id] {
			return nil, fmt.Errorf("variants: unknown TR %q", id)
		}
	}
	selected := applyOverrides(SelectTRs(trs, cfg.Filter), cfg.Overrides)
	var out []core.TestRequirement
	for _, tr := range selected {
		variants, ok := cfg.Variants[tr.ID]
		if !ok {
			out = append(out, tr)
			continue
		}
		if len(variants) == 0 {
			return nil, fmt.Errorf("variants[%s] is empty", tr.ID)
		}
		for _, v := range variants {
			copyTR := tr
			copyTR.Variant = v.Name
			copyTR.Params = make(map[string]any, len(tr.Params)+len(v.Params))
			maps.Copy(copyTR.Params, tr.Params)
			maps.Copy(copyTR.Params, v.Params)
			out = append(out, copyTR)
		}
	}
	return out, nil
}

// Keep ordinary multi-TR batching; named variants get independent pipelines.
// All executions for this tool stay inside its scheduling lock.
func runToolExecutions(ctx context.Context, j job, rc *core.RunCtx, g *grader.Grader, progress *progressTracker) ([]core.Verdict, []core.Measurement) {
	var ordinary []core.TestRequirement
	for _, tr := range j.trs {
		if tr.Variant == "" {
			ordinary = append(ordinary, tr)
		}
	}
	var vs []core.Verdict
	var ms []core.Measurement
	if len(ordinary) > 0 {
		batch := j
		batch.trs = ordinary
		for _, tr := range ordinary {
			progress.start(tr)
		}
		v, m := runJob(ctx, batch, rc, g)
		for _, tr := range ordinary {
			if ctx.Err() != nil {
				progress.abort(tr, core.OutcomeError)
			} else {
				progress.complete(tr, summarizeTR(v, tr))
			}
		}
		vs = append(vs, v...)
		ms = append(ms, m...)
	}
	for _, tr := range j.trs {
		if tr.Variant == "" {
			continue
		}
		variantRC := *rc
		// Encode both path components so even programmatically supplied IDs cannot escape.
		variantRC.WorkDir = filepath.Join(rc.WorkDir, "variants", hex.EncodeToString([]byte(tr.ID)), hex.EncodeToString([]byte(tr.Variant)))
		if err := os.MkdirAll(variantRC.WorkDir, 0o750); err != nil {
			vs = append(vs, errTR(tr, "variant workdir: "+err.Error())...)
			if ctx.Err() != nil {
				progress.abort(tr, core.OutcomeError)
			} else {
				progress.complete(tr, core.OutcomeError)
			}
			continue
		}
		batch := j
		batch.trs = []core.TestRequirement{tr}
		progress.start(tr)
		v, m := runJob(ctx, batch, &variantRC, g)
		if ctx.Err() != nil {
			progress.abort(tr, core.OutcomeError)
		} else {
			progress.complete(tr, summarizeTR(v, tr))
		}
		vs = append(vs, v...)
		ms = append(ms, m...)
	}
	return vs, ms
}
