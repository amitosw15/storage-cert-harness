package kubeburner

import (
	"fmt"
	"strings"

	"github.com/openshift-storage-cert/storage-cert-harness/internal/safefs"
)

const snapshotJobsMarker = "# SNAPSHOT_JOBS"

// prepareSnapshotConfig fills the snapshot workload's jobs after generic staging.
func prepareSnapshotConfig(path string, params Params) error {
	source, err := safefs.ReadFile(path)
	if err != nil {
		return fmt.Errorf("kube-burner: read snapshot config: %w", err)
	}
	data := []byte(strings.Replace(string(source), snapshotJobsMarker, snapshotJobs(params), 1))
	if err := safefs.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("kube-burner: prepare snapshot config: %w", err)
	}
	return nil
}

// snapshotJobs renders one job or sequential batches.
func snapshotJobs(p Params) string {
	if !p.snapshotCountSet && p.SnapshotCount == p.Replicas {
		return legacySnapshotJob()
	}

	var jobs strings.Builder
	for start, batch := 0, 1; start < p.SnapshotCount; start, batch = start+p.Replicas, batch+1 {
		replicas := min(p.Replicas, p.SnapshotCount-start)
		fmt.Fprintf(&jobs, `  - name: %s%d
    jobType: create
    jobIterations: 1
    qps: {{.QPS}}
    burst: {{.BURST}}
    namespace: {{.NAMESPACE}}
    namespacedIterations: false
    waitWhenFinished: true
    maxWaitTimeout: {{.MAX_WAIT}}
    objects:
      - objectTemplate: templates/vmsnapshot-batch.yaml
        replicas: %d
        inputVars:
          snapshotBatch: %d
        wait: true
        waitOptions:
          customStatusPaths:
            - key: '.readyToUse | tostring'
              value: "true"
`, snapshotJobPrefix, batch, replicas, batch)
	}
	return strings.TrimSuffix(jobs.String(), "\n")
}

func legacySnapshotJob() string {
	return `  - name: vmsnapshot-snapshot
    jobType: create
    jobIterations: 1
    qps: {{.QPS}}
    burst: {{.BURST}}
    namespace: {{.NAMESPACE}}
    namespacedIterations: false
    waitWhenFinished: true
    maxWaitTimeout: {{.MAX_WAIT}}
    objects:
      - objectTemplate: templates/vmsnapshot.yaml
        replicas: {{.REPLICAS}}
        wait: true
        waitOptions:
          customStatusPaths:
            - key: '.readyToUse | tostring'
              value: "true"`
}
