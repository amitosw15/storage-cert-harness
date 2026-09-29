package kubeburner

import (
	"embed"
	"fmt"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"

	"gitlab.cee.redhat.com/eco-special-projects/storage-cert-harness/internal/safefs"
)

//go:embed assets
var assets embed.FS

const snapshotJobsMarker = "# SNAPSHOT_JOBS"

// writeConfig writes assets and renders snapshot jobs.
func writeConfig(dir string, params Params) (string, error) {
	root := "assets"
	err := fs.WalkDir(assets, root, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		dst := filepath.Join(dir, rel)
		if d.IsDir() {
			return safefs.MkdirAll(dst, 0o755)
		}
		b, err := assets.ReadFile(p)
		if err != nil {
			return err
		}
		if rel == "config.yaml" {
			b = []byte(strings.Replace(string(b), snapshotJobsMarker, snapshotJobs(params), 1))
		}
		return safefs.WriteFile(dst, b, 0o600)
	})
	if err != nil {
		return "", fmt.Errorf("kube-burner: write config assets: %w", err)
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
