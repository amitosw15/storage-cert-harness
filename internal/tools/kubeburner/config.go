package kubeburner

import (
	"fmt"
	"path/filepath"

	"github.com/openshift-storage-cert/storage-cert-harness/internal/manifest"
	"github.com/openshift-storage-cert/storage-cert-harness/internal/safefs"
)

var renderer manifest.ManifestRenderer = manifest.Native{}

// stageConfig copies a selected workload into kube-burner's runtime layout.
func stageConfig(dir, sourceDir string) (string, error) {
	if err := safefs.MkdirAll(filepath.Join(dir, "templates"), 0o755); err != nil {
		return "", fmt.Errorf("kube-burner: create template directory: %w", err)
	}
	entries, err := templates.ReadDir(sourceDir)
	if err != nil {
		return "", fmt.Errorf("kube-burner: read templates: %w", err)
	}
	hasConfig := false
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		b, err := templates.ReadFile(sourceDir + "/" + name)
		if err != nil {
			return "", fmt.Errorf("kube-burner: read template %s: %w", name, err)
		}
		b, err = manifest.Render(renderer, name, b, nil)
		if err != nil {
			return "", err
		}
		dst := filepath.Join(dir, "templates", name)
		if name == "config.yaml" {
			hasConfig = true
			dst = filepath.Join(dir, name)
		}
		if err := safefs.WriteFile(dst, b, 0o600); err != nil {
			return "", fmt.Errorf("kube-burner: write template %s: %w", name, err)
		}
	}
	if !hasConfig {
		return "", fmt.Errorf("kube-burner: template directory %s has no config.yaml", sourceDir)
	}
	return "config.yaml", nil
}
