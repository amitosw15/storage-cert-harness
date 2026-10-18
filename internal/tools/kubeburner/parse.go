package kubeburner

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/openshift-storage-cert/storage-cert-harness/internal/core"
)

// MetricSnapshotReadyTime is the normalized VolumeSnapshot time-to-Ready metric (kube-burner emits ms; the grader unit-normalizes, ADR-0008).
const (
	MetricSnapshotReadyTime           = "snapshot_ready_time"
	MetricSnapshotBatchCompletionTime = "snapshot_batch_completion_time"
	MetricSnapshotRequestedCount      = "snapshot_requested_count"
	MetricSnapshotReadyCount          = "snapshot_ready_count"
	MetricSnapshotFailedCount         = "snapshot_failed_count"
	MetricSnapshotSuccessRate         = "snapshot_success_rate"
	MetricSnapshotsPerVolume          = "snapshots_per_volume"

	legacySnapshotJobName = "vmsnapshot-snapshot"
	snapshotJobPrefix     = legacySnapshotJobName + "-"
)

// jobSummary is one row of kube-burner's jobSummary.json.
type jobSummary struct {
	MetricName  string   `json:"metricName"`
	ElapsedTime *float64 `json:"elapsedTime"`
	Passed      bool     `json:"passed"`
	JobConfig   struct {
		Name string `json:"name"`
	} `json:"jobConfig"`
}

// snapshotQuantile is one row of volumeSnapshotLatencyQuantilesMeasurement (quantileName "Ready" carries the percentiles).
type snapshotQuantile struct {
	QuantileName string  `json:"quantileName"`
	P99          float64 `json:"P99"`
	P95          float64 `json:"P95"`
	P50          float64 `json:"P50"`
	Max          float64 `json:"max"`
	Min          float64 `json:"min"`
	Avg          float64 `json:"avg"`
	MetricName   string  `json:"metricName"`
	JobName      string  `json:"jobName,omitempty"`
}

// snapshotLatency is one row of the per-snapshot volumeSnapshotLatencyMeasurement timeseries.
type snapshotLatency struct {
	VSReadyLatency float64 `json:"vsReadyLatency"`
	VSName         string  `json:"vsName"`
	Namespace      string  `json:"namespace"`
	JobName        string  `json:"jobName,omitempty"`
	Replica        int     `json:"replica,omitempty"`
}

func parseJobSummaries(data map[string][]byte) ([]jobSummary, core.Outcome, error) {
	blob, ok := data["jobSummary.json"]
	if !ok || len(blob) == 0 {
		return nil, "", fmt.Errorf("kube-burner: missing or empty jobSummary.json")
	}
	var summaries []jobSummary
	if err := json.Unmarshal(blob, &summaries); err != nil {
		return nil, "", fmt.Errorf("kube-burner: parse jobSummary.json: %w", err)
	}
	native := core.OutcomePass
	if len(summaries) == 0 {
		native = core.OutcomeFail
	}
	for _, s := range summaries {
		if !s.Passed {
			native = core.OutcomeFail
			break
		}
	}
	return summaries, native, nil
}

// ParseResults normalizes a kube-burner result set.
func ParseResults(trID string, data map[string][]byte) (core.TestResult, error) {
	return parseResults(trID, data, 0)
}

// parseResults normalizes configured snapshot metrics.
func parseResults(trID string, data map[string][]byte, requestedSnapshots int) (core.TestResult, error) {
	return parseResultsWithScenario(trID, data, requestedSnapshots, scenarioFor(trID))
}

func parseResultsWithScenario(trID string, data map[string][]byte, requestedSnapshots int, scenario snapshotScenario) (core.TestResult, error) {
	summaries, native, err := parseJobSummaries(data)
	if err != nil {
		return core.TestResult{}, err
	}
	jobsPassed := native == core.OutcomePass

	var metrics []core.Metric
	var perSnapshot []snapshotLatency
	var fallbackQuantiles []snapshotQuantile
	for name, blob := range data {
		switch {
		case strings.Contains(name, "QuantilesMeasurement"):
			var quants []snapshotQuantile
			if err := json.Unmarshal(blob, &quants); err != nil {
				return core.TestResult{}, fmt.Errorf("kube-burner: parse %s: %w", name, err)
			}
			for _, q := range quants {
				if q.QuantileName != "Ready" {
					continue
				}
				fallbackQuantiles = append(fallbackQuantiles, q)
			}
		case strings.Contains(name, "volumeSnapshotLatencyMeasurement"):
			var rows []snapshotLatency
			if err := json.Unmarshal(blob, &rows); err != nil {
				return core.TestResult{}, fmt.Errorf("kube-burner: parse %s: %w", name, err)
			}
			perSnapshot = append(perSnapshot, rows...)
		}
	}

	if len(perSnapshot) > 0 {
		metrics = append(metrics, snapshotReadyMetrics(perSnapshot)...)
	} else if len(fallbackQuantiles) == 1 {
		q := fallbackQuantiles[0]
		metrics = append(metrics,
			core.Metric{Name: MetricSnapshotReadyTime, Value: q.P50, Unit: "ms", Percentile: "p50"},
			core.Metric{Name: MetricSnapshotReadyTime, Value: q.P95, Unit: "ms", Percentile: "p95"},
			core.Metric{Name: MetricSnapshotReadyTime, Value: q.P99, Unit: "ms", Percentile: "p99"},
			core.Metric{Name: MetricSnapshotReadyTime, Value: q.Max, Unit: "ms", Percentile: "max"},
		)
	}
	if requestedSnapshots > 0 {
		elapsed, err := snapshotBatchElapsedTime(summaries)
		if err != nil {
			return core.TestResult{}, err
		}
		ready := readySnapshotCount(perSnapshot)
		if len(perSnapshot) == 0 && jobsPassed {
			// A passing kube-burner job summary proves that all objects in
			// the requested snapshot batches reached the configured ready
			// state even when only aggregate quantiles were indexed.
			ready = requestedSnapshots
		}
		if ready > requestedSnapshots {
			return core.TestResult{}, fmt.Errorf("kube-burner: measured %d ready snapshots, more than %d requested", ready, requestedSnapshots)
		}
		failed := requestedSnapshots - ready
		if ready != requestedSnapshots {
			native = core.OutcomeFail
		}
		metrics = append(metrics,
			core.Metric{Name: MetricSnapshotRequestedCount, Value: float64(requestedSnapshots), Unit: "count"},
			core.Metric{Name: MetricSnapshotReadyCount, Value: float64(ready), Unit: "count"},
			core.Metric{Name: MetricSnapshotFailedCount, Value: float64(failed), Unit: "count"},
			core.Metric{Name: MetricSnapshotSuccessRate, Value: float64(ready) / float64(requestedSnapshots) * 100, Unit: "%"},
		)
		metrics = append(metrics, core.Metric{Name: MetricSnapshotBatchCompletionTime, Value: elapsed, Unit: "s"})
		if scenario.SnapshotsPerVolume {
			// The scenario declares one source volume, so ready count is its depth.
			metrics = append(metrics, core.Metric{Name: MetricSnapshotsPerVolume, Value: float64(ready), Unit: "count"})
		}
	}
	if requestedSnapshots == 0 && scenario.AllowZeroSnapshots {
		// Explicit reduced mode: the source volume was provisioned, but no
		// snapshots were requested. Keep the result measurable and make the
		// intentional reduction visible as a failed 250-snapshot requirement.
		native = core.OutcomeFail
		metrics = append(metrics, core.Metric{Name: MetricSnapshotsPerVolume, Value: 0, Unit: "count"})
	}

	checks := map[string]core.Outcome{"jobs_passed": native}
	raw, _ := json.Marshal(map[string]any{
		"jobSummary":  summaries,
		"perSnapshot": perSnapshot,
	})
	return core.TestResult{TRID: trID, Metrics: metrics, Checks: checks, Native: native, Raw: raw}, nil
}

// snapshotReadyMetrics returns kube-burner-compatible latency percentiles.
func snapshotReadyMetrics(rows []snapshotLatency) []core.Metric {
	latencies := make([]float64, 0, len(rows))
	for _, row := range rows {
		latencies = append(latencies, row.VSReadyLatency)
	}
	sort.Float64s(latencies)
	return []core.Metric{
		{Name: MetricSnapshotReadyTime, Value: float64(kubeBurnerPercentile(latencies, 50)), Unit: "ms", Percentile: "p50"},
		{Name: MetricSnapshotReadyTime, Value: float64(kubeBurnerPercentile(latencies, 95)), Unit: "ms", Percentile: "p95"},
		{Name: MetricSnapshotReadyTime, Value: float64(kubeBurnerPercentile(latencies, 99)), Unit: "ms", Percentile: "p99"},
		{Name: MetricSnapshotReadyTime, Value: float64(int(latencies[len(latencies)-1])), Unit: "ms", Percentile: "max"},
	}
}

// kubeBurnerPercentile calculates a kube-burner-compatible percentile.
func kubeBurnerPercentile(sorted []float64, percentile float64) int {
	if len(sorted) == 1 {
		return int(sorted[0])
	}
	index := percentile / 100 * float64(len(sorted))
	if index == float64(int(index)) {
		return int(sorted[int(index)-1])
	}
	i := int(index)
	return int((sorted[i-1] + sorted[i]) / 2)
}

func readySnapshotCount(rows []snapshotLatency) int {
	ready := map[string]bool{}
	for _, row := range rows {
		if row.VSName != "" {
			ready[row.VSName] = true
		}
	}
	return len(ready)
}

func snapshotBatchElapsedTime(summaries []jobSummary) (float64, error) {
	var elapsed float64
	var batches int
	for _, summary := range summaries {
		if isSnapshotJob(summary.JobConfig.Name) {
			if summary.ElapsedTime == nil {
				return 0, fmt.Errorf("kube-burner: jobSummary for %q has no elapsedTime", summary.JobConfig.Name)
			}
			elapsed += *summary.ElapsedTime
			batches++
		}
	}
	if batches == 0 {
		return 0, fmt.Errorf("kube-burner: missing snapshot jobSummary")
	}
	return elapsed, nil
}

func isSnapshotJob(name string) bool {
	return name == legacySnapshotJobName || strings.HasPrefix(name, snapshotJobPrefix)
}

func snapshotBatchActual(res core.TestResult) string {
	values := map[string]float64{}
	for _, metric := range res.Metrics {
		values[metric.Name] = metric.Value
	}
	return "requested=" + batchMetric(values, MetricSnapshotRequestedCount, "") +
		" ready=" + batchMetric(values, MetricSnapshotReadyCount, "") +
		" failed=" + batchMetric(values, MetricSnapshotFailedCount, "") +
		" success_rate=" + batchMetric(values, MetricSnapshotSuccessRate, "%") +
		" batch_completion_time=" + batchMetric(values, MetricSnapshotBatchCompletionTime, "s")
}

func batchMetric(values map[string]float64, name, unit string) string {
	value, ok := values[name]
	if !ok {
		return "unavailable"
	}
	return strconv.FormatFloat(value, 'f', -1, 64) + unit
}
