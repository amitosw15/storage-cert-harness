package kubeburner

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/openshift-storage-cert/storage-cert-harness/internal/safefs"
)

const snapshotJobsMarker = "# SNAPSHOT_JOBS"

// writeConfig stages templates in kube-burner's runtime layout and renders snapshot jobs.
func writeConfig(dir string, params Params) (string, error) {
	if err := safefs.MkdirAll(filepath.Join(dir, "templates"), 0o755); err != nil {
		return "", fmt.Errorf("kube-burner: create template directory: %w", err)
	}
	entries, err := templates.ReadDir(snapshotTemplatesDir)
	if err != nil {
		return "", fmt.Errorf("kube-burner: read templates: %w", err)
	}
	for _, entry := range entries {
		name := entry.Name()
		b, err := templates.ReadFile(snapshotTemplatesDir + "/" + name)
		if err != nil {
			return "", fmt.Errorf("kube-burner: read template %s: %w", name, err)
		}
		dst := filepath.Join(dir, "templates", name)
		if name == "config.yaml" {
			dst = filepath.Join(dir, name)
			b = []byte(strings.Replace(string(b), snapshotJobsMarker, snapshotJobs(params), 1))
		}
		if err := safefs.WriteFile(dst, b, 0o600); err != nil {
			return "", fmt.Errorf("kube-burner: write template %s: %w", name, err)
		}
	}
	return "config.yaml", nil
}

// buildEnv renders the config's template vars from plan params + backend (never a threshold — secret hygiene) as KEY=VALUE strings.
func buildEnv(p Params, storageClass string) []string {
	return []string{
		"REPLICAS=" + strconv.Itoa(p.Replicas),
		"VM_IMAGE=" + p.VMImage,
		"DISK_SIZE=" + p.DiskSize,
		"STORAGE_CLASS=" + storageClass,
		"NAMESPACE=" + p.NS,
		"USE_POPULATOR=" + strconv.FormatBool(p.UsePopulator),
		"MAX_WAIT=" + p.MaxWait,
		"QPS=" + strconv.Itoa(p.QPS),
		"BURST=" + strconv.Itoa(p.Burst),
	}
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
