package kubeburner

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/openshift-storage-cert/storage-cert-harness/internal/clustercheck"
	"github.com/openshift-storage-cert/storage-cert-harness/internal/core"
	"github.com/openshift-storage-cert/storage-cert-harness/internal/safefs"
)

const cleanupTimeout = 30 * time.Second

func (s *session) Run(ctx context.Context, rc *core.RunCtx) error {
	if len(s.executions) == 0 {
		return fmt.Errorf("kube-burner: no TRs to run (preflight first)")
	}
	runs := s.executions
	for _, run := range runs {
		if run.Requirement.TR.ID == "" {
			return fmt.Errorf("kube-burner: TR has empty id")
		}
		if run.Requirement.Err != nil {
			return run.Requirement.Err
		}
	}
	resultsDir := s.resultsDir
	if resultsDir == "" {
		return fmt.Errorf("kube-burner: results directory missing (provision first)")
	}
	storageClass := ""
	if rc.Backend != nil {
		storageClass = rc.Backend.StorageClass
	}
	if storageClass == "" {
		return fmt.Errorf("kube-burner: storage_class required (pass --backends/--backend)")
	}
	for i := range runs {
		if err := ctx.Err(); err != nil {
			return err
		}
		run := &runs[i]
		tr := run.Requirement.TR
		params := run.Requirement.Params
		sc := storageClass
		if run.SnapshotStorageClass != "" {
			sc = run.SnapshotStorageClass
		}
		if run.Selection.SnapshotClass != "" && rc.RecordStorageSelection != nil {
			rc.RecordStorageSelection(core.StorageSelection{TR: tr.ID, Variant: tr.Variant, StorageClass: run.Selection.StorageClass, SnapshotClass: run.Selection.SnapshotClass, EffectiveStorageClass: sc})
		}
		run.Subdir = filepath.Join(resultsDir, tr.ID)
		if err := os.RemoveAll(run.Subdir); err != nil {
			return err
		}
		if err := safefs.MkdirAll(run.Subdir, 0o755); err != nil {
			return err
		}
		if _, err := writeConfig(run.Subdir, params); err != nil {
			return err
		}
		cmd, err := buildRunCmd(ctx, rc, buildEnv(params, sc), resultsDir, tr.ID)
		if err != nil {
			return err
		}
		cmd.Stdout = rc.ToolOutput(os.Stdout)
		cmd.Stderr = rc.ToolOutput(os.Stderr)
		rc.Logger.Info("kube-burner: running", "tr", tr.ID, "replicas", params.Replicas, "snapshot_count", params.SnapshotCount, "namespace", params.NS)
		if err := cmd.Run(); err != nil {
			run.RunErr = err.Error()
			rc.Logger.Warn("kube-burner: run failed", "tr", tr.ID, "err", err)
		}
	}
	return nil
}

// buildRunCmd runs kube-burner init -c using the host binary.
func buildRunCmd(ctx context.Context, rc *core.RunCtx, env []string, resultsDir, subdir string) (*exec.Cmd, error) {
	path, err := exec.LookPath("kube-burner")
	if err != nil {
		return nil, fmt.Errorf("kube-burner: kube-burner not on PATH: %w", err)
	}
	absResults, err := filepath.Abs(resultsDir)
	if err != nil {
		return nil, fmt.Errorf("kube-burner: results dir: %w", err)
	}
	cfg := filepath.Join(absResults, subdir, "config.yaml")
	cmd := exec.CommandContext(ctx, path, "init", "-c", cfg) // #nosec G204 -- path comes from LookPath and args are harness-generated.
	cmd.Dir = filepath.Join(absResults, subdir)
	cmd.Env = append(os.Environ(), env...)
	rc.Logger.Info("kube-burner: running host binary", "path", path)
	return cmd, nil
}

func (s *session) Collect(_ context.Context, rc *core.RunCtx) (core.LogBundle, error) {
	runs := s.executions
	if runs == nil {
		return core.LogBundle{}, fmt.Errorf("kube-burner: parameters missing (preflight first)")
	}
	data := make(map[string][]byte)
	refsByTR := make(map[string][]string, len(runs))
	var refs []string
	for _, run := range runs {
		trID := run.Requirement.TR.ID
		metrics, files, err := findCollectedMetrics(run.Subdir)
		if err != nil {
			rc.Logger.Warn("kube-burner: no results collected", "tr", trID, "err", err)
			continue
		}
		rc.Logger.Info("kube-burner: collected results", "tr", trID, "files", len(metrics))
		refs = append(refs, files...)
		refsByTR[trID] = files
		for name, content := range metrics {
			data[trID+"/"+name] = content
		}
	}
	return core.LogBundle{Refs: refs, RefsByTR: refsByTR, Data: data}, nil
}

// findCollectedMetrics returns the metric files under root keyed by base name,
// alongside the path of each one read, so a report can cite the files a verdict
// came from rather than the directory that held them.
func findCollectedMetrics(root string) (map[string][]byte, []string, error) {
	data := map[string][]byte{}
	var paths []string
	rootFS, err := os.OpenRoot(root)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = rootFS.Close() }()
	err = fs.WalkDir(rootFS.FS(), ".", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() {
			return walkErr
		}
		name := entry.Name()
		if name == "jobSummary.json" || strings.Contains(name, "volumeSnapshotLatency") {
			content, err := rootFS.ReadFile(path)
			if err != nil {
				return err
			}
			data[name] = content
			paths = append(paths, filepath.Join(root, path))
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	if len(data) == 0 {
		return nil, nil, fmt.Errorf("kube-burner: no collected-metrics under %s", root)
	}
	sort.Strings(paths)
	return data, paths, nil
}

func (s *session) Parse(_ context.Context, _ *core.RunCtx, logs core.LogBundle) ([]core.TestResult, error) {
	runs := s.executions
	if runs == nil {
		return nil, fmt.Errorf("kube-burner: per-TR parameters missing (preflight first)")
	}
	results := make([]core.TestResult, 0, len(runs))
	for _, run := range runs {
		trID := run.Requirement.TR.ID
		data := make(map[string][]byte)
		prefix := trID + "/"
		for name, content := range logs.Data {
			if strings.HasPrefix(name, prefix) {
				data[strings.TrimPrefix(name, prefix)] = content
			}
		}
		if _, ok := data["jobSummary.json"]; !ok {
			reason := "kube-burner produced no jobSummary"
			if run.RunErr != "" {
				reason = run.RunErr
			}
			raw, _ := json.Marshal(map[string]string{"error": reason})
			results = append(results, core.TestResult{TRID: trID, Native: core.OutcomeFail, Raw: raw})
			continue
		}
		result, err := parseResultsWithScenario(trID, data, run.Requirement.Params.SnapshotCount, snapshotScenarios[trID])
		if err != nil {
			return nil, err
		}
		results = append(results, result)
	}
	return results, nil
}

// Teardown best-effort deletes each run namespace and private snapshot class.
func (s *session) Teardown(ctx context.Context, rc *core.RunCtx) error {
	runs := s.executions
	defer func() {
		for _, run := range runs {
			cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
			if err := run.Selection.CleanupVMStorage(cleanupCtx, run.SnapshotStorageClass); err != nil {
				rc.Logger.Warn("kube-burner: remove snapshot storage class", "err", err)
			}
			cancel()
		}
	}()
	cli := clustercheck.KubeCLI()
	if cli == "" {
		return nil
	}
	seen := make(map[string]bool, len(runs))
	for _, run := range runs {
		namespace := run.Requirement.Params.NS
		if namespace == "" || seen[namespace] {
			continue
		}
		seen[namespace] = true
		rc.Logger.Info("kube-burner: removing run namespace", "ns", namespace)
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
		output, err := exec.CommandContext(cleanupCtx, cli, "delete", "ns", namespace, "--wait=false", "--ignore-not-found").CombinedOutput() // #nosec G204 -- CLI is resolved by clustercheck and arguments are structured.
		cancel()
		if err != nil {
			rc.Logger.Warn("kube-burner: remove run namespace", "ns", namespace, "err", err, "output", strings.TrimSpace(string(output)))
		}
	}
	return nil
}
