package kubeburner

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strconv"

	"github.com/openshift-storage-cert/storage-cert-harness/internal/manifest"
	"github.com/openshift-storage-cert/storage-cert-harness/internal/safefs"
)

var renderer manifest.ManifestRenderer = manifest.Native{}

// writeConfig stages unchanged sources for kube-burner's native renderer.
func writeConfig(dir string) (string, error) {
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
		b, err = manifest.Render(renderer, name, b, nil)
		if err != nil {
			return "", err
		}
		dst := filepath.Join(dir, "templates", name)
		if name == "config.yaml" {
			dst = filepath.Join(dir, name)
		}
		if err := safefs.WriteFile(dst, b, 0o600); err != nil {
			return "", fmt.Errorf("kube-burner: write template %s: %w", name, err)
		}
	}
	return "config.yaml", nil
}

// buildEnv renders the config's template vars from plan params + backend (never a threshold — secret hygiene) as KEY=VALUE strings.
func buildEnv(p Params, storageClass string) []string {
	jobs, _ := json.Marshal(snapshotJobInputs(p))
	return []string{
		"SNAPSHOT_JOBS=" + string(jobs),
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

type snapshotJobInput struct {
	Name     string `json:"name"`
	Template string `json:"template"`
	Replicas int    `json:"replicas"`
	Batch    int    `json:"batch"`
}

func snapshotJobInputs(p Params) []snapshotJobInput {
	if !p.snapshotCountSet && p.SnapshotCount == p.Replicas {
		return []snapshotJobInput{{Name: legacySnapshotJobName, Template: "templates/vmsnapshot.yaml", Replicas: p.Replicas}}
	}
	var jobs []snapshotJobInput
	for start, batch := 0, 1; start < p.SnapshotCount; start, batch = start+p.Replicas, batch+1 {
		jobs = append(jobs, snapshotJobInput{
			Name: snapshotJobPrefix + strconv.Itoa(batch), Template: "templates/vmsnapshot-batch.yaml",
			Replicas: min(p.Replicas, p.SnapshotCount-start), Batch: batch,
		})
	}
	return jobs
}
