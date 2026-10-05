package kubeburnerocp

import (
	"encoding/json"
	"strings"

	"gitlab.cee.redhat.com/eco-special-projects/storage-cert-harness/internal/core"
)

const (
	migrateJobName        = "migrate-vms"
	pvcBoundCheck         = "pvc-1000-bound"
	virtParallelCheck     = "parallel-lifecycle-20"
	virtParallelMetric    = "lifecycle_completion_time"
	virtParallelJobPrefix = "virt-parallel-"
)

func mapPVCDensityResults(summaries []jobSummary, data map[string][]byte, result *core.TestResult) error {
	return mapPVCDensityResultsFor(WorkloadPVCDensity, 1, summaries, data, result)
}

func mapFourPVCDensityResults(summaries []jobSummary, data map[string][]byte, result *core.TestResult) error {
	return mapPVCDensityResultsFor(WorkloadPVCDensityFourPVC, 4, summaries, data, result)
}

func mapPVCDensityResultsFor(jobName string, pvcPerIteration int, summaries []jobSummary, data map[string][]byte, result *core.TestResult) error {
	for _, s := range summaries {
		if s.JobConfig.Name != jobName {
			continue
		}
		// A successful, verified job waits for every requested PVC to be ready.
		outcome := core.OutcomeFail
		if s.Passed && s.ExecutionErrors == "" && s.JobConfig.JobIterations > 0 &&
			s.JobConfig.WaitWhenFinished && s.JobConfig.VerifyObjects && s.JobConfig.ErrorOnVerify {
			outcome = core.OutcomePass
		}
		result.Checks[pvcBoundCheck] = outcome
		result.Metrics = append(result.Metrics,
			core.Metric{Name: "pvc_requested_count", Value: float64(s.JobConfig.JobIterations * pvcPerIteration), Unit: "count"})
		break
	}
	for name, blob := range data {
		if !strings.Contains(name, "Quantiles") {
			continue
		}
		var quants []latencyQuantile
		if err := json.Unmarshal(blob, &quants); err != nil {
			continue
		}
		for _, q := range quants {
			if q.MetricName == "pvcLatencyQuantilesMeasurement" && q.QuantileName == "Bound" && q.JobName == jobName {
				result.Metrics = append(result.Metrics,
					core.Metric{Name: "pvc_bind_latency", Value: q.P99, Unit: "ms", Percentile: "p99"})
			}
		}
	}
	return nil
}

func mapVirtMigrationResults(summaries []jobSummary, _ map[string][]byte, result *core.TestResult) error {
	for _, s := range summaries {
		if s.JobConfig.Name == migrateJobName {
			result.Metrics = append(result.Metrics,
				core.Metric{Name: "total_migration_duration", Value: s.ElapsedTime, Unit: "s"})
			break
		}
	}
	return nil
}

func mapVirtParallelResults(summaries []jobSummary, _ map[string][]byte, result *core.TestResult) error {
	result.Checks[virtParallelCheck] = result.Native

	var lifecycleSeconds float64
	for _, s := range summaries {
		if s.JobConfig.Name == "virt-parallel-start-fresh" ||
			!strings.HasPrefix(s.JobConfig.Name, virtParallelJobPrefix) {
			continue
		}
		lifecycleSeconds += s.ElapsedTime
	}
	if lifecycleSeconds > 0 {
		result.Metrics = append(result.Metrics,
			core.Metric{Name: virtParallelMetric, Value: lifecycleSeconds, Unit: "s"})
	}
	return nil
}
