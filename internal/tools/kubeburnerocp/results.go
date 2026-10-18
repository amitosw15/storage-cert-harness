package kubeburnerocp

import (
	"encoding/json"
	"strconv"
	"strings"

	"gitlab.cee.redhat.com/eco-special-projects/storage-cert-harness/internal/core"
)

const (
	migrateJobName        = "migrate-vms"
	pvcBoundCheck         = "pvc-1000-bound"
	virtParallelCheck     = "parallel-lifecycle-20"
	virtParallelMetric    = "lifecycle_completion_time"
	virtParallelJobPrefix = "virt-parallel-"
	virtParallelCheckVMs  = 20
)

func mapPVCDensityResults(summaries []jobSummary, data map[string][]byte, result *core.TestResult) error {
	for _, s := range summaries {
		if s.JobConfig.Name != WorkloadPVCDensity {
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
			core.Metric{Name: "pvc_requested_count", Value: float64(s.JobConfig.JobIterations), Unit: "count"})
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
			if q.MetricName == "pvcLatencyQuantilesMeasurement" && q.QuantileName == "Bound" && q.JobName == WorkloadPVCDensity {
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

// mapVirtParallelResults maps the results of the virt-parallel workload to the test result.
// It checks if the workload reached the required scale and calculates the total lifecycle duration.
func mapVirtParallelResults(summaries []jobSummary, _ map[string][]byte, result *core.TestResult) error {
	if virtParallelReachesCheckScale(summaries) {
		result.Checks[virtParallelCheck] = result.Native
	} else {
		result.Checks[virtParallelCheck] = core.OutcomeSkip
	}

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

// virtParallelReachesCheckScale determines if the virt-parallel workload reached the scale required for the check.
func virtParallelReachesCheckScale(summaries []jobSummary) bool {
	for _, summary := range summaries {
		initialVMS, err := strconv.Atoi(summary.WorkloadFlags["initialVms"])
		if err != nil {
			continue
		}
		increment, _ := strconv.Atoi(summary.WorkloadFlags["increment"])
		iterations, _ := strconv.Atoi(summary.WorkloadFlags["maxIterations"])
		if iterations < 1 {
			iterations = 1
		}
		if initialVMS+increment*(iterations-1) >= virtParallelCheckVMs {
			return true
		}
	}
	return false
}
