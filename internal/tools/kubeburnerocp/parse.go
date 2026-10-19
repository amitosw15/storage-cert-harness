package kubeburnerocp

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/openshift-storage-cert/storage-cert-harness/internal/core"
)

// resultMapper translates generic kube-burner evidence into the catalog
// contract for one workload. Workload-specific names stay at the adapter
// boundary rather than in the shared parser.
type resultMapper func(summaries []jobSummary, data map[string][]byte, result *core.TestResult) error

// jobSummary is one row from jobSummary.json.
type jobSummary struct {
	MetricName      string            `json:"metricName"`
	ElapsedTime     float64           `json:"elapsedTime"`
	UUID            string            `json:"uuid"`
	Passed          bool              `json:"passed"`
	ExecutionErrors string            `json:"executionErrors,omitempty"`
	WorkloadFlags   map[string]string `json:"workloadFlags,omitempty"`
	JobConfig       struct {
		Name             string `json:"name"`
		JobIterations    int    `json:"jobIterations"`
		JobType          string `json:"jobType"`
		WaitWhenFinished bool   `json:"waitWhenFinished"`
		VerifyObjects    bool   `json:"verifyObjects"`
		ErrorOnVerify    bool   `json:"errorOnVerify"`
	} `json:"jobConfig"`
}

// latencyQuantile is one row from a *QuantilesMeasurement file.
type latencyQuantile struct {
	QuantileName string  `json:"quantileName"`
	P99          float64 `json:"P99"`
	P95          float64 `json:"P95"`
	P50          float64 `json:"P50"`
	MetricName   string  `json:"metricName"`
	JobName      string  `json:"jobName,omitempty"`
}

// parseJobSummaries reads jobSummary.json; native success = all jobs passed.
func parseJobSummaries(data map[string][]byte) ([]jobSummary, core.Outcome, error) {
	blob, ok := data["jobSummary.json"]
	if !ok || len(blob) == 0 {
		return nil, "", fmt.Errorf("kubeburner: missing or empty jobSummary.json")
	}
	var summaries []jobSummary
	if err := json.Unmarshal(blob, &summaries); err != nil {
		return nil, "", fmt.Errorf("kubeburner: parse jobSummary.json: %w", err)
	}
	native := core.OutcomePass
	if len(summaries) == 0 {
		native = core.OutcomeFail
	} else {
		for _, s := range summaries {
			if !s.Passed {
				native = core.OutcomeFail
				break
			}
		}
	}
	return summaries, native, nil
}

// quantileMetricName maps a quantile to e.g. "pvcLatency_Bound".
func quantileMetricName(q latencyQuantile) string {
	m := strings.TrimSuffix(q.MetricName, "QuantilesMeasurement")
	if m == "" {
		m = "latency"
	}
	return m + "_" + q.QuantileName
}

// ParseResults normalizes generic kube-burner output and applies the selected
// workload's result mapping to the catalog contract.
func ParseResults(workload, trID string, data map[string][]byte) (core.TestResult, error) {
	if len(data) == 0 {
		return core.TestResult{}, fmt.Errorf("kubeburner: empty data map")
	}
	summaries, native, err := parseJobSummaries(data)
	if err != nil {
		return core.TestResult{}, err
	}

	var metrics []core.Metric
	for name, blob := range data {
		if !strings.Contains(name, "Quantiles") {
			continue
		}
		var quants []latencyQuantile
		if err := json.Unmarshal(blob, &quants); err != nil {
			continue
		}
		for _, q := range quants {
			base := quantileMetricName(q)
			metrics = append(metrics,
				core.Metric{Name: base, Value: q.P50, Unit: "ms", Percentile: "p50"},
				core.Metric{Name: base, Value: q.P95, Unit: "ms", Percentile: "p95"},
				core.Metric{Name: base, Value: q.P99, Unit: "ms", Percentile: "p99"},
			)
		}
	}

	checks := map[string]core.Outcome{"jobs_passed": native}
	result := core.TestResult{TRID: trID, Metrics: metrics, Checks: checks, Native: native}
	if spec, ok := workloadSpecs[workload]; ok && spec.mapResults != nil {
		if err := spec.mapResults(summaries, data, &result); err != nil {
			return core.TestResult{}, err
		}
	}
	raw, _ := json.Marshal(map[string]any{"jobSummary": summaries})
	result.Raw = raw
	return result, nil
}
