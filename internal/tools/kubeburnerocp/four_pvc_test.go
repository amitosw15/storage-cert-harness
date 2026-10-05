package kubeburnerocp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gitlab.cee.redhat.com/eco-special-projects/storage-cert-harness/internal/core"
)

func TestFourPVCProfileRendersFourClaimsAndHardSpread(t *testing.T) {
	dir := t.TempDir()
	workers := []string{"worker-a", "worker-b", "worker-c"}
	if err := renderFourPVCFiles(dir, "rhos-san-economy-iscsi-fs", 10, "256Mi", workers); err != nil {
		t.Fatal(err)
	}
	config := readRenderedFile(t, dir, "four-pvc-density.yml")
	pod := readRenderedFile(t, dir, "pod.yml")
	pvc := readRenderedFile(t, dir, "pvc.yml")

	if strings.Count(config, "objectTemplate: pvc.yml") != 4 || strings.Count(config, "suffix:") != 4 {
		t.Fatalf("expected four PVC template entries, got config:\n%s", config)
	}
	if !strings.Contains(config, "jobIterations: 10") || !strings.Contains(config, "storageClassName: rhos-san-economy-iscsi-fs") {
		t.Fatalf("rendered config omitted target iterations or storage class:\n%s", config)
	}
	if !strings.Contains(config, "namespace: pvc-density-four-pvc-per-pod") {
		t.Fatalf("rendered config omitted the workload namespace:\n%s", config)
	}
	for _, suffix := range []string{"a", "b", "c", "d"} {
		if !strings.Contains(pod, "claimName: pvc-{{.Iteration}}-"+suffix) || !strings.Contains(pod, "mountPath: /data/"+suffix) || !strings.Contains(config, "suffix: "+suffix) {
			t.Errorf("missing distinct claim or mount %q", suffix)
		}
	}
	for _, worker := range workers {
		if !strings.Contains(pod, "- "+worker) {
			t.Errorf("rendered Pod is not constrained to %s", worker)
		}
	}
	for _, expected := range []string{"matchExpressions:", "operator: In", "maxSkew: 1", "minDomains: 3", "whenUnsatisfiable: DoNotSchedule", `cpu: "10m"`, `memory: "10Mi"`} {
		if !strings.Contains(pod, expected) {
			t.Errorf("rendered Pod omitted %q", expected)
		}
	}
	if !strings.Contains(pvc, "storage: {{.claimSize}}") {
		t.Errorf("PVC template does not use the per-claim size: %s", pvc)
	}
}

func TestFourPVCInputsRequiresThreeDistinctWorkers(t *testing.T) {
	for _, workers := range []string{"worker-a,worker-b", "worker-a,worker-b,worker-a", "worker-a,worker-b,invalid/name"} {
		_, _, _, err := fourPVCInputs(map[string]any{
			"iterations":   10,
			"claim_size":   "256Mi",
			"worker_nodes": workers,
		})
		if err == nil {
			t.Errorf("accepted invalid worker_nodes %q", workers)
		}
	}
}

func TestFourPVCResultsCountAllClaims(t *testing.T) {
	var summary jobSummary
	summary.JobConfig.Name = WorkloadPVCDensityFourPVC
	summary.JobConfig.JobIterations = 10
	summary.JobConfig.WaitWhenFinished = true
	summary.JobConfig.VerifyObjects = true
	summary.JobConfig.ErrorOnVerify = true
	summary.Passed = true
	result := core.TestResult{Checks: map[string]core.Outcome{}}
	if err := mapFourPVCDensityResults([]jobSummary{summary}, nil, &result); err != nil {
		t.Fatal(err)
	}
	if result.Checks[pvcBoundCheck] != core.OutcomePass {
		t.Fatalf("PVC binding check = %q, want pass", result.Checks[pvcBoundCheck])
	}
	if len(result.Metrics) != 1 || result.Metrics[0].Name != "pvc_requested_count" || result.Metrics[0].Value != 40 {
		t.Fatalf("requested PVC count = %+v, want 40", result.Metrics)
	}
}

func readRenderedFile(t *testing.T, dir, name string) string {
	t.Helper()
	contents, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(contents)
}
