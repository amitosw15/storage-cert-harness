// This file holds the output-ingest adapters: turning virtbench's result JSON
// into normalized core.Metrics whose names match the KB SLA metrics for the TR
// under test. Each parser is pure so it is golden-testable with no cluster or
// binary (the pattern every ResultParser follows — decisions/0003, 0006).
//
// Three output shapes exist (decisions/0008):
//   - the shared "summary" shape (datasource-clone, its --boot-storm variant,
//     migration): parseSummary, keyed by a per-scenario metric map.
//   - the disk-ops nested shape: ParseDiskOps.
//   - raw FIO JSON with read/write percentile maps: ParseFIO.
package virtbench

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/openshift-storage-cert/storage-cert-harness/internal/core"
)

// checkAllEvacuated is the functional check id TR-VIRT-008 grades: every VM
// live-migrated off the drained node. It must match the check id in the KB.
const checkAllEvacuated = "all-vms-evacuated"

// drain-nodes emits no results JSON — only a human log (vm-ops/drain-nodes.py).
// That log carries everything TR-VIRT-008 grades: the target node, the per-node
// drain duration, and a per-node VMI distribution printed BEFORE and AFTER the
// drain. ParseDrain reads the evacuation time and the target's VMI count before
// vs after, so the harness needs no cluster snapshot of its own (decisions/0013).
//
// The log is opened in append mode, so a reused path can hold several runs; every
// field is taken from its LAST occurrence.
var (
	drainNodesRe     = regexp.MustCompile(`^Nodes:\s*(.+)$`)
	drainNodeTimeRe  = regexp.MustCompile(`Drain (?:completed in|timed out after)\s+([0-9.]+)s`)
	drainTotalTimeRe = regexp.MustCompile(`Total time:\s+([0-9.]+)s`)
)

// summaryFile is the on-disk shape virtbench writes for the datasource-clone
// family (utils/common.py save_results). Averages may be null when a phase
// produced no samples, so aggregate values are pointers.
type summaryFile struct {
	TotalVMs          int             `json:"total_vms"`
	Successful        int             `json:"successful"`
	Failed            int             `json:"failed"`
	TotalTestDuration *float64        `json:"total_test_duration_sec"`
	Metrics           []summaryMetric `json:"metrics"`
}

// fioRawFile is one FIO JSON result collected from one VM. The FIO profile uses
// group_reporting, so one aggregate job contains both read and write samples.
type fioRawFile struct {
	Jobs []fioJob `json:"jobs"`
}

// fioCollectedResults is the live-run bundle: one raw FIO result for every VM
// plus Virtbench's run-level counts. Replay keeps accepting one raw result.
type fioCollectedResults struct {
	Summary json.RawMessage   `json:"summary"`
	Results []json.RawMessage `json:"results"`
}

type fioSummaryFile struct {
	TotalVMs   int `json:"total_vms"`
	Successful int `json:"successful"`
	Failed     int `json:"failed"`
}

type fioJob struct {
	Error int          `json:"error"`
	Read  fioDirection `json:"read"`
	Write fioDirection `json:"write"`
}

type fioDirection struct {
	IOPS  float64    `json:"iops"`
	LatNS fioLatency `json:"lat_ns"`
}

type fioLatency struct {
	Percentile map[string]float64 `json:"percentile"`
}

// ParseFIO converts one raw FIO result or a collected VM range into p99
// read/write latency metrics. A VM range is graded by its worst per-VM p99.
func ParseFIO(data []byte, trID string) ([]core.TestResult, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("virtbench: empty fio payload")
	}
	var collected fioCollectedResults
	if err := json.Unmarshal(data, &collected); err != nil {
		return nil, fmt.Errorf("virtbench: parse fio: %w", err)
	}
	if len(collected.Results) > 0 {
		return parseCollectedFIO(collected, data, trID)
	}
	return parseFIORaw(data, trID)
}

func parseCollectedFIO(collected fioCollectedResults, raw []byte, trID string) ([]core.TestResult, error) {
	var summary fioSummaryFile
	if err := json.Unmarshal(collected.Summary, &summary); err != nil {
		return nil, fmt.Errorf("virtbench: parse fio summary: %w", err)
	}
	if summary.TotalVMs == 0 {
		return nil, fmt.Errorf("virtbench: fio summary reports 0 VMs")
	}
	if summary.Successful+summary.Failed != summary.TotalVMs {
		return nil, fmt.Errorf("virtbench: fio summary counts are inconsistent")
	}
	metrics := fioRunMetrics(summary)
	if summary.Failed > 0 {
		return []core.TestResult{{TRID: trID, Metrics: metrics, Native: core.OutcomeFail, Raw: json.RawMessage(raw)}}, nil
	}
	if len(collected.Results) != summary.TotalVMs {
		return nil, fmt.Errorf("virtbench: fio collected %d VM results, want %d", len(collected.Results), summary.TotalVMs)
	}

	var readP99, writeP99, readIOPS, writeIOPS float64
	for _, result := range collected.Results {
		job, err := fioJobFromRaw(result)
		if err != nil {
			return nil, err
		}
		read, err := fioP99(job.Read.LatNS.Percentile)
		if err != nil {
			return nil, fmt.Errorf("virtbench: read latency: %w", err)
		}
		write, err := fioP99(job.Write.LatNS.Percentile)
		if err != nil {
			return nil, fmt.Errorf("virtbench: write latency: %w", err)
		}
		if read > readP99 {
			readP99 = read
		}
		if write > writeP99 {
			writeP99 = write
		}
		readIOPS += job.Read.IOPS
		writeIOPS += job.Write.IOPS
	}
	metrics = append(metrics,
		core.Metric{Name: "read_latency", Value: readP99 / 1e6, Unit: "ms", Percentile: "p99"},
		core.Metric{Name: "write_latency", Value: writeP99 / 1e6, Unit: "ms", Percentile: "p99"},
		core.Metric{Name: "read_iops", Value: readIOPS, Unit: "IOPS"},
		core.Metric{Name: "write_iops", Value: writeIOPS, Unit: "IOPS"},
	)
	return []core.TestResult{{TRID: trID, Metrics: metrics, Native: core.OutcomePass, Raw: json.RawMessage(raw)}}, nil
}

func parseFIORaw(data []byte, trID string) ([]core.TestResult, error) {
	job, err := fioJobFromRaw(data)
	if err != nil {
		return nil, err
	}
	readP99, err := fioP99(job.Read.LatNS.Percentile)
	if err != nil {
		return nil, fmt.Errorf("virtbench: read latency: %w", err)
	}
	writeP99, err := fioP99(job.Write.LatNS.Percentile)
	if err != nil {
		return nil, fmt.Errorf("virtbench: write latency: %w", err)
	}

	return []core.TestResult{{
		TRID: trID,
		Metrics: []core.Metric{
			{Name: "read_latency", Value: readP99 / 1e6, Unit: "ms", Percentile: "p99"},
			{Name: "write_latency", Value: writeP99 / 1e6, Unit: "ms", Percentile: "p99"},
			{Name: "read_iops", Value: job.Read.IOPS, Unit: "IOPS"},
			{Name: "write_iops", Value: job.Write.IOPS, Unit: "IOPS"},
		},
		Native: core.OutcomePass,
		Raw:    json.RawMessage(data),
	}}, nil
}

func fioJobFromRaw(data []byte) (fioJob, error) {
	var f fioRawFile
	if err := json.Unmarshal(data, &f); err != nil {
		return fioJob{}, fmt.Errorf("virtbench: parse fio: %w", err)
	}
	if len(f.Jobs) != 1 {
		return fioJob{}, fmt.Errorf("virtbench: fio result has %d jobs, want one group-reported job", len(f.Jobs))
	}
	job := f.Jobs[0]
	if job.Error != 0 {
		return fioJob{}, fmt.Errorf("virtbench: fio job failed with error %d", job.Error)
	}
	return job, nil
}

func fioRunMetrics(summary fioSummaryFile) []core.Metric {
	return []core.Metric{
		{Name: "vms_total", Value: float64(summary.TotalVMs)},
		{Name: "vms_successful", Value: float64(summary.Successful)},
		{Name: "vms_failed", Value: float64(summary.Failed)},
		{Name: "vm_success_rate", Value: float64(summary.Successful) / float64(summary.TotalVMs) * 100, Unit: "%"},
	}
}

func fioP99(percentiles map[string]float64) (float64, error) {
	for key, value := range percentiles {
		percentile, err := strconv.ParseFloat(key, 64)
		if err == nil && percentile == 99 {
			return value, nil
		}
	}
	return 0, fmt.Errorf("p99 percentile missing")
}

type summaryMetric struct {
	Metric string   `json:"metric"`
	Avg    *float64 `json:"avg"`
	Max    *float64 `json:"max"`
	Min    *float64 `json:"min"`
	Count  int      `json:"count"`
}

// detailEntry is one per-VM row from the datasource-clone detailed results file
// (utils/common.py save_results detailed JSON). Only clone_duration_sec is read;
// the summary file carries avg/max/min, this file carries the raw samples a
// percentile needs.
type detailEntry struct {
	CloneDurationSec *float64 `json:"clone_duration_sec"`
}

// metric-map + emit-order for datasource-clone. These names MUST match
// TR-VIRT-002's sla[].metric exactly or the grader can't find the measured value.
var (
	cloneMetricBase = map[string]string{
		"clone_duration_sec": "clone_duration",
		"running_time_sec":   "time_to_running",
		"ping_time_sec":      "time_to_ping",
	}
	cloneMetricOrder = []string{"clone_duration_sec", "running_time_sec", "ping_time_sec"}
)

// metric-map + emit-order for the boot-storm variant. It reports no clone phase;
// its gate (vm_boot_time) is derived from the whole-storm duration in
// ParseBootStorm below.
var (
	bootStormBase = map[string]string{
		"running_time_sec": "time_to_running",
		"ping_time_sec":    "time_to_ping",
	}
	bootStormOrder = []string{"running_time_sec", "ping_time_sec"}
)

// ParseSummary converts a virtbench datasource-clone summary into one normalized
// TestResult for trID.
func ParseSummary(data []byte, trID string) ([]core.TestResult, error) {
	return parseSummary(cloneMetricBase, cloneMetricOrder, data, trID)
}

// ParseBootStorm parses the boot-storm summary and, on top of the shared summary
// metrics, exposes vm_boot_time ← total_test_duration_sec: the KB gate is "100
// VMs booted and SSH-ready in < N min", i.e. the whole storm's wall-clock. The
// grader converts s→min/h (see internal/grader/units.go), so vm_boot_time keeps
// unit "s" here (decisions/0008).
func ParseBootStorm(data []byte, trID string) ([]core.TestResult, error) {
	res, err := parseSummary(bootStormBase, bootStormOrder, data, trID)
	if err != nil {
		return nil, err
	}
	for _, m := range res[0].Metrics {
		if m.Name == "total_test_duration" {
			res[0].Metrics = append(res[0].Metrics, core.Metric{Name: "vm_boot_time", Value: m.Value, Unit: "s"})
			break
		}
	}
	return res, nil
}

// ParseSingleNodeCeiling parses the single-node summary and adds
// max_vms_per_node (TR-VIRT-013's KB gate name) from the achieved successful
// count. Native is always pass: this scenario deliberately pushes VMs onto one
// node until scheduling or volume attach fails, so partial failure is the
// expected result, not a broken run — whether the ceiling is high enough is a
// question for the max_vms_per_node >= 50 SLA bar, an ordinary graded metric,
// not a native outcome.
func ParseSingleNodeCeiling(data []byte, trID string) ([]core.TestResult, error) {
	res, err := parseSummary(cloneMetricBase, cloneMetricOrder, data, trID)
	if err != nil {
		return nil, err
	}
	successful := metricValue(res[0].Metrics, "vms_successful")
	res[0].Metrics = append(res[0].Metrics, core.Metric{Name: "max_vms_per_node", Value: successful, Unit: "count"})
	res[0].Native = core.OutcomePass
	return res, nil
}

// metricValue returns the value of the first metric named name, or 0 if absent.
func metricValue(metrics []core.Metric, name string) float64 {
	for _, m := range metrics {
		if m.Name == name {
			return m.Value
		}
	}
	return 0
}

// parseSummary is the shared summary parser. Averages become SLA-scored metrics
// (unit "s"); max/min and run-level counts are attached as informative metrics.
// Native is fail if any VM failed.
func parseSummary(metricBase map[string]string, metricOrder []string, data []byte, trID string) ([]core.TestResult, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("virtbench: empty summary payload")
	}
	var s summaryFile
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("virtbench: parse summary: %w", err)
	}
	if s.TotalVMs == 0 {
		return nil, fmt.Errorf("virtbench: summary reports 0 VMs (no data to grade)")
	}

	byName := make(map[string]summaryMetric, len(s.Metrics))
	for _, m := range s.Metrics {
		byName[m.Metric] = m
	}

	var metrics []core.Metric
	for _, key := range metricOrder {
		m, ok := byName[key]
		if !ok {
			continue
		}
		base := metricBase[key]
		if m.Avg != nil {
			metrics = append(metrics, core.Metric{Name: base, Value: *m.Avg, Unit: "s"})
		}
		if m.Max != nil {
			metrics = append(metrics, core.Metric{Name: base + "_max", Value: *m.Max, Unit: "s"})
		}
		if m.Min != nil {
			metrics = append(metrics, core.Metric{Name: base + "_min", Value: *m.Min, Unit: "s"})
		}
	}

	// Run-level metrics (informative; available for a TR that wants a success-rate
	// gate).
	metrics = append(metrics,
		core.Metric{Name: "vms_total", Value: float64(s.TotalVMs)},
		core.Metric{Name: "vms_successful", Value: float64(s.Successful)},
		core.Metric{Name: "vms_failed", Value: float64(s.Failed)},
		core.Metric{Name: "vm_success_rate", Value: float64(s.Successful) / float64(s.TotalVMs) * 100, Unit: "%"},
	)
	if s.TotalTestDuration != nil {
		metrics = append(metrics, core.Metric{Name: "total_test_duration", Value: *s.TotalTestDuration, Unit: "s"})
	}

	native := core.OutcomePass
	if s.Failed > 0 {
		native = core.OutcomeFail
	}

	return []core.TestResult{{
		TRID:    trID,
		Metrics: metrics,
		Native:  native,
	}}, nil
}

// ClonePercentiles computes percentile metrics for clone_duration from the per-VM
// detailed results array. Each label in pcts (e.g. "p99") becomes one
// clone_duration@<pct> metric (unit s). Returns nil when the file has no samples.
func ClonePercentiles(data []byte, pcts []string) ([]core.Metric, error) {
	if len(data) == 0 {
		return nil, nil
	}
	var rows []detailEntry
	if err := json.Unmarshal(data, &rows); err != nil {
		return nil, fmt.Errorf("virtbench: parse detailed results: %w", err)
	}
	var samples []float64
	for _, r := range rows {
		if r.CloneDurationSec != nil {
			samples = append(samples, *r.CloneDurationSec)
		}
	}
	if len(samples) == 0 {
		return nil, nil
	}
	sort.Float64s(samples)
	var out []core.Metric
	for _, p := range pcts {
		frac, ok := parsePercentile(p)
		if !ok {
			continue
		}
		out = append(out, core.Metric{Name: "clone_duration", Percentile: p, Value: percentile(samples, frac), Unit: "s"})
	}
	return out, nil
}

// parsePercentile turns a "pNN" label into a 0..1 fraction ("p99" -> 0.99).
func parsePercentile(label string) (float64, bool) {
	n, err := strconv.ParseFloat(strings.TrimPrefix(label, "p"), 64)
	if err != nil || n <= 0 || n > 100 {
		return 0, false
	}
	return n / 100, true
}

// percentile returns the nearest-rank percentile of an ascending-sorted slice.
func percentile(sorted []float64, frac float64) float64 {
	n := len(sorted)
	rank := min(max(int(math.Ceil(frac*float64(n)))-1, 0), n-1)
	return sorted[rank]
}

// --- drain-nodes (Parser C) --------------------------------------------------

// ParseDrain turns a drain-nodes log into one normalized TestResult for trID,
// emitting the same metric/check names measureDrain did so the KB gate and grader
// need no change: evacuation_time (the KB gate), vms_total/_evacuated/
// _remaining, evacuation_success_rate, and the all-vms-evacuated check. A VM is
// evacuated iff it left the target: vms_remaining is the target's VMI count in
// the AFTER distribution (0 when the node no longer appears).
func ParseDrain(data []byte, trID string) ([]core.TestResult, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("virtbench: empty drain log")
	}

	var target string
	var elapsed, totalElapsed float64
	var haveElapsed, haveTotal bool
	before := map[string]int{}
	after := map[string]int{}

	section := "" // "before" | "after" | ""
	for _, raw := range strings.Split(string(data), "\n") {
		msg := drainLogMessage(raw)
		if msg == "" {
			continue
		}
		switch {
		case strings.HasPrefix(msg, "==="):
			section = ""
			continue
		case strings.Contains(msg, "VMI DISTRIBUTION BEFORE DRAIN"):
			section, before = "before", map[string]int{}
			continue
		case strings.Contains(msg, "VMI DISTRIBUTION AFTER DRAIN"):
			section, after = "after", map[string]int{}
			continue
		}
		if m := drainNodesRe.FindStringSubmatch(msg); m != nil {
			target = strings.TrimSpace(strings.SplitN(m[1], ",", 2)[0])
			continue
		}
		if m := drainNodeTimeRe.FindStringSubmatch(msg); m != nil {
			if v, err := strconv.ParseFloat(m[1], 64); err == nil {
				elapsed, haveElapsed = v, true
			}
			continue
		}
		if m := drainTotalTimeRe.FindStringSubmatch(msg); m != nil {
			if v, err := strconv.ParseFloat(m[1], 64); err == nil {
				totalElapsed, haveTotal = v, true
			}
			continue
		}
		if node, count, ok := drainDistRow(msg); ok {
			switch section {
			case "before":
				before[node] = count
			case "after":
				after[node] = count
			}
		}
	}

	if target == "" {
		return nil, fmt.Errorf("virtbench: drain log has no target node (no 'Nodes:' line)")
	}
	if !haveElapsed && haveTotal {
		elapsed, haveElapsed = totalElapsed, true
	}
	if !haveElapsed {
		return nil, fmt.Errorf("virtbench: drain log has no drain duration for %q", target)
	}
	total, ok := before[target]
	if !ok || total == 0 {
		return nil, fmt.Errorf("virtbench: no VMs on target node %q in the BEFORE distribution (nothing to grade)", target)
	}
	remaining := after[target]
	evacuated := total - remaining

	metrics := []core.Metric{
		{Name: "evacuation_time", Value: elapsed, Unit: "s"},
		{Name: "vms_total", Value: float64(total)},
		{Name: "vms_evacuated", Value: float64(evacuated)},
		{Name: "vms_remaining", Value: float64(remaining)},
		{Name: "evacuation_success_rate", Value: float64(evacuated) / float64(total) * 100, Unit: "%"},
	}

	native, checkOutcome := core.OutcomePass, core.OutcomePass
	if remaining > 0 {
		native, checkOutcome = core.OutcomeFail, core.OutcomeFail
	}

	return []core.TestResult{{
		TRID:    trID,
		Metrics: metrics,
		Checks:  map[string]core.Outcome{checkAllEvacuated: checkOutcome},
		Native:  native,
	}}, nil
}

// drainLogMessage strips the "TIMESTAMP - LEVEL - " prefix drain-nodes writes
// (formatter '%(asctime)s - %(levelname)s - %(message)s'), returning the message.
func drainLogMessage(line string) string {
	parts := strings.SplitN(line, " - ", 3)
	if len(parts) < 3 {
		return ""
	}
	return strings.TrimSpace(parts[2])
}

// drainDistRow parses one VMI-distribution table row ("<node> <count>", node
// left-justified). The header ("Node VMI Count") and separator lines have no
// trailing integer and are ignored.
func drainDistRow(msg string) (node string, count int, ok bool) {
	fields := strings.Fields(msg)
	if len(fields) < 2 || fields[0] == "Node" {
		return "", 0, false
	}
	n, err := strconv.Atoi(fields[len(fields)-1])
	if err != nil {
		return "", 0, false
	}
	return fields[0], n, true
}

// --- failure-recovery (Parser D) ---------------------------------------------

// metric-map + emit-order for failure-recovery. KB gate: recovery_time
// (running_time_sec) and ping_time_sec (required — absent means no VMs
// responded to ping, which is a test failure).
const (
	failureRecoveryRunningMetric = "running_time_sec"
	failureRecoveryPingMetric    = "ping_time_sec"
)

var (
	failureRecoveryMetricBase = map[string]string{
		failureRecoveryRunningMetric: "vm_ha_restart_time",
		failureRecoveryPingMetric:    "time_to_ping",
	}
	failureRecoveryMetricOrder = []string{failureRecoveryRunningMetric, failureRecoveryPingMetric}
)

// ParseFailureRecovery converts a virtbench failure-recovery summary into one
// normalized TestResult for trID. It populates Checks so the grader can score
// node-failure-vm-recovery and reattach-failure-modes.
//
// node-failure-vm-recovery passes only when all VMs are successful and the
// ping_time_sec aggregate covers every VM.
//
// A missing ping_time_sec means virtbench never confirmed network reachability
// after recovery — that is a test failure regardless of Running state.
func ParseFailureRecovery(data []byte, trID string) ([]core.TestResult, error) {
	var summary struct {
		TotalVMs   int `json:"total_vms"`
		Successful int `json:"successful"`
		Failed     int `json:"failed"`
		Metrics    []struct {
			Name  string `json:"metric"`
			Count int    `json:"count"`
		} `json:"metrics"`
	}
	if err := json.Unmarshal(data, &summary); err != nil {
		return nil, fmt.Errorf("virtbench: parse failure-recovery summary: %w", err)
	}
	res, err := parseSummary(failureRecoveryMetricBase, failureRecoveryMetricOrder, data, trID)
	if err != nil {
		return nil, err
	}
	complete := summary.TotalVMs > 0 && summary.Successful == summary.TotalVMs && summary.Failed == 0
	pingComplete := false
	for _, metric := range summary.Metrics {
		if metric.Name == failureRecoveryPingMetric && metric.Count == summary.TotalVMs {
			pingComplete = true
		}
	}
	nativeOK := res[0].Native == core.OutcomePass && complete
	var nodeRecovery core.Outcome
	switch {
	case nativeOK && pingComplete:
		nodeRecovery = core.OutcomePass
	default:
		nodeRecovery = core.OutcomeFail
	}
	res[0].Checks = map[string]core.Outcome{
		"node-failure-vm-recovery": nodeRecovery,
		"reattach-failure-modes":   res[0].Native,
	}
	return res, nil
}

// --- disk-ops (Parser B) -----------------------------------------------------

// diskOpsFile is the disk-ops nested shape (disk-ops-benchmark/measure-disk-ops.py
// save_results) — a DIFFERENT shape from the summary family: per-operation
// aggregates under named keys. We grade the hotplug attach path.
type diskOpsFile struct {
	Summary struct {
		TotalVMs        int `json:"total_vms"`
		TotalOperations int `json:"total_operations"`
	} `json:"summary"`
	Hotplug *diskOpAgg `json:"hotplug"`
}

type diskOpAgg struct {
	Successful       int      `json:"successful"`
	Failed           int      `json:"failed"`
	AvgAPIAttachTime *float64 `json:"avg_api_attach_time"`
	MaxAPIAttachTime *float64 `json:"max_api_attach_time"`
	MinAPIAttachTime *float64 `json:"min_api_attach_time"`
	AvgVolumeReady   *float64 `json:"avg_volume_ready_time"`
	AvgValidation    *float64 `json:"avg_validation_time"`
}

// ParseDiskOps converts a virtbench disk-ops result into one normalized
// TestResult. It maps hotplug's average API attach time to pvc_attach_time (the
// TR-STOR-001 gate) and exposes volume-ready/validation timings plus counts as
// informative metrics. Native is fail if any hotplug operation failed.
func ParseDiskOps(data []byte, trID string) ([]core.TestResult, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("virtbench: empty disk-ops payload")
	}
	var d diskOpsFile
	if err := json.Unmarshal(data, &d); err != nil {
		return nil, fmt.Errorf("virtbench: parse disk-ops: %w", err)
	}
	if d.Hotplug == nil {
		return nil, fmt.Errorf("virtbench: disk-ops results have no hotplug section (no attach data to grade)")
	}
	h := d.Hotplug

	var metrics []core.Metric
	if h.AvgAPIAttachTime != nil {
		metrics = append(metrics, core.Metric{Name: "pvc_attach_time", Value: *h.AvgAPIAttachTime, Unit: "s"})
	}
	if h.MaxAPIAttachTime != nil {
		metrics = append(metrics, core.Metric{Name: "pvc_attach_time_max", Value: *h.MaxAPIAttachTime, Unit: "s"})
	}
	if h.MinAPIAttachTime != nil {
		metrics = append(metrics, core.Metric{Name: "pvc_attach_time_min", Value: *h.MinAPIAttachTime, Unit: "s"})
	}
	if h.AvgVolumeReady != nil {
		metrics = append(metrics, core.Metric{Name: "volume_ready_time", Value: *h.AvgVolumeReady, Unit: "s"})
	}
	if h.AvgValidation != nil {
		metrics = append(metrics, core.Metric{Name: "validation_time", Value: *h.AvgValidation, Unit: "s"})
	}

	total := h.Successful + h.Failed
	metrics = append(metrics,
		core.Metric{Name: "ops_total", Value: float64(total)},
		core.Metric{Name: "ops_successful", Value: float64(h.Successful)},
		core.Metric{Name: "ops_failed", Value: float64(h.Failed)},
	)
	if total > 0 {
		metrics = append(metrics, core.Metric{Name: "attach_success_rate", Value: float64(h.Successful) / float64(total) * 100, Unit: "%"})
	}

	if len(metrics) == 0 {
		return nil, fmt.Errorf("virtbench: disk-ops hotplug section has no timing data")
	}

	native := core.OutcomePass
	if h.Failed > 0 {
		native = core.OutcomeFail
	}

	return []core.TestResult{{
		TRID:    trID,
		Metrics: metrics,
		Native:  native,
	}}, nil
}
