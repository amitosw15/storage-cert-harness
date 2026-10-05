package kubeburnerocp

import (
	"context"
	"embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"gitlab.cee.redhat.com/eco-special-projects/storage-cert-harness/internal/core"
)

//go:embed assets/four-pvc-density.yml assets/pod.yml assets/pvc.yml
var fourPVCAssets embed.FS

var (
	dnsLabelPattern  = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?$`)
	claimSizePattern = regexp.MustCompile(`^[1-9][0-9]*(?:Mi|Gi|Ti|M|G|T)$`)
)

func prepareFourPVCConfig(ctx context.Context, rc *core.RunCtx, dir, storageClass string, p Params) ([]string, error) {
	iterations, claimSize, workers, err := fourPVCInputs(p.Raw)
	if err != nil {
		return nil, err
	}
	if !validDNSName(storageClass) {
		return nil, fmt.Errorf("kube-burner-ocp: backend storage_class must be a DNS name")
	}
	if err := renderFourPVCFiles(dir, storageClass, iterations, claimSize, workers); err != nil {
		return nil, err
	}

	path, err := exec.LookPath("kube-burner-ocp")
	if err != nil {
		return nil, fmt.Errorf("kube-burner-ocp: kube-burner-ocp not on PATH: %w", err)
	}
	extractDir := filepath.Join(dir, "upstream-extracted")
	if err := os.MkdirAll(extractDir, 0o700); err != nil {
		return nil, fmt.Errorf("kube-burner-ocp: create extraction directory: %w", err)
	}
	extract := exec.CommandContext(ctx, path, "pvc-density", "--extract") // #nosec G204 -- path comes from LookPath and args are constants.
	extract.Dir = extractDir
	extract.Stdout = rc.ToolOutput(os.Stdout)
	extract.Stderr = rc.ToolOutput(os.Stderr)
	if err := extract.Run(); err != nil {
		return nil, fmt.Errorf("kube-burner-ocp: extract released pvc-density metrics: %w", err)
	}
	for _, name := range []string{"metrics.yml", "alerts.yml"} {
		contents, err := os.ReadFile(filepath.Join(extractDir, name))
		if err != nil {
			return nil, fmt.Errorf("kube-burner-ocp: read extracted %s: %w", name, err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), contents, 0o600); err != nil {
			return nil, fmt.Errorf("kube-burner-ocp: write extracted %s: %w", name, err)
		}
	}

	args := []string{"init", "--config", "four-pvc-density.yml", "--gc=false"}
	for _, a := range []argSpec{
		{flag: "--qps", param: "qps", typ: argInt, optional: true},
		{flag: "--burst", param: "burst", typ: argInt, optional: true},
		{flag: "--timeout", param: "timeout", typ: argString, optional: true},
	} {
		seg, err := renderArg(p.Raw, a)
		if err != nil {
			return nil, err
		}
		args = append(args, seg...)
	}
	return args, nil
}

func fourPVCInputs(raw map[string]any) (int, string, []string, error) {
	iterations, ok := asInt(raw["iterations"])
	if !ok || iterations < 1 {
		return 0, "", nil, fmt.Errorf("kube-burner-ocp: param %q must be a positive integer", "iterations")
	}
	claimSize, ok := asString(raw["claim_size"])
	if !ok || !claimSizePattern.MatchString(claimSize) {
		return 0, "", nil, fmt.Errorf("kube-burner-ocp: param %q must be a storage quantity", "claim_size")
	}
	workers, err := workerNodeNames(raw["worker_nodes"])
	if err != nil {
		return 0, "", nil, err
	}
	return iterations, claimSize, workers, nil
}

func renderFourPVCFiles(dir, storageClass string, iterations int, claimSize string, workers []string) error {
	config := map[string]string{
		"four-pvc-density.yml": "assets/four-pvc-density.yml",
		"pod.yml":              "assets/pod.yml",
		"pvc.yml":              "assets/pvc.yml",
	}
	replacements := strings.NewReplacer(
		"__ITERATIONS__", fmt.Sprint(iterations),
		"__CLAIM_SIZE__", claimSize,
		"__STORAGE_CLASS__", storageClass,
		"__WORKER_1__", workers[0],
		"__WORKER_2__", workers[1],
		"__WORKER_3__", workers[2],
	)
	for target, source := range config {
		contents, err := fourPVCAssets.ReadFile(source)
		if err != nil {
			return fmt.Errorf("kube-burner-ocp: read embedded profile %s: %w", source, err)
		}
		if err := os.WriteFile(filepath.Join(dir, target), []byte(replacements.Replace(string(contents))), 0o600); err != nil {
			return fmt.Errorf("kube-burner-ocp: write profile %s: %w", target, err)
		}
	}
	return nil
}
func workerNodeNames(value any) ([]string, error) {
	raw, ok := asString(value)
	if !ok {
		return nil, fmt.Errorf("kube-burner-ocp: param %q must list three comma-separated worker names", "worker_nodes")
	}
	parts := strings.Split(raw, ",")
	if len(parts) != 3 {
		return nil, fmt.Errorf("kube-burner-ocp: param %q must list exactly three worker names", "worker_nodes")
	}
	seen := map[string]bool{}
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
		if !validDNSName(parts[i]) || seen[parts[i]] {
			return nil, fmt.Errorf("kube-burner-ocp: param %q must contain three distinct DNS names", "worker_nodes")
		}
		seen[parts[i]] = true
	}
	return parts, nil
}

func validDNSName(name string) bool {
	if len(name) == 0 || len(name) > 253 {
		return false
	}
	for _, label := range strings.Split(name, ".") {
		if len(label) > 63 || !dnsLabelPattern.MatchString(label) {
			return false
		}
	}
	return true
}
