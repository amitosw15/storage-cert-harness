package kubeburner

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"text/template"

	"github.com/Masterminds/sprig/v3"
	"github.com/openshift-storage-cert/storage-cert-harness/internal/core"
	"github.com/openshift-storage-cert/storage-cert-harness/internal/manifest"
)

func renderConfig(t *testing.T, p Params) string {
	t.Helper()
	asset, err := templates.ReadFile(workloadTemplatesDir + "/config.yaml")
	if err != nil {
		t.Fatal(err)
	}
	values := map[string]any{}
	for _, entry := range buildEnv(p, "fast") {
		key, value, _ := strings.Cut(entry, "=")
		values[key] = value
	}
	parsed, err := template.New("config.yaml").Option("missingkey=error").Funcs(sprig.GenericFuncMap()).Parse(string(asset))
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := parsed.Execute(&output, values); err != nil {
		t.Fatal(err)
	}
	return output.String()
}

func TestResolveParamsUsesKBCanonicalDiskSize(t *testing.T) {
	p := resolveParams([]core.TestRequirement{{Params: map[string]any{"disk_size": "2Gi"}}})
	if p.DiskSize != "2Gi" {
		t.Fatalf("disk size = %q, want KB value", p.DiskSize)
	}
	if !p.UsePopulator {
		t.Fatal("UsePopulator default = false, want true")
	}
}

func TestBuildEnvSetsUsePopulator(t *testing.T) {
	if got := strings.Join(buildEnv(Params{UsePopulator: false}, "example-storage"), "\n"); !strings.Contains(got, "USE_POPULATOR=false") {
		t.Fatalf("environment does not select legacy CDI imports: %s", got)
	}
}

func TestStageConfigPreservesEveryNativeTemplate(t *testing.T) {
	dir := t.TempDir()
	if _, err := stageConfig(dir, workloadTemplatesDir); err != nil {
		t.Fatalf("stageConfig: %v", err)
	}
	entries, err := templates.ReadDir(workloadTemplatesDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		source, err := templates.ReadFile(workloadTemplatesDir + "/" + entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, "templates", entry.Name())
		if entry.Name() == "config.yaml" {
			path = filepath.Join(dir, entry.Name())
		}
		staged, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(staged, source) {
			t.Fatalf("native source %s changed: %v", entry.Name(), err)
		}
	}
}

func TestNativeObjectTemplatesRender(t *testing.T) {
	values := map[string]any{
		"Replica": 2, "snapshotBatch": 3, "usePopulator": "true",
		"storageClass": "fast", "diskSize": "2Gi", "vmImage": "quay.io/example/disk:latest",
	}
	for _, name := range []string{"vm.yaml", "vmsnapshot.yaml", "vmsnapshot-batch.yaml"} {
		source, err := templates.ReadFile(workloadTemplatesDir + "/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := manifest.Render(manifest.GoTemplate{}, name, source, values); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
}

func TestStageConfigRequiresWorkloadConfig(t *testing.T) {
	for _, sourceDir := range []string{"templates", "templates/missing"} {
		if _, err := stageConfig(t.TempDir(), sourceDir); err == nil {
			t.Fatalf("stageConfig accepted %q without config.yaml", sourceDir)
		}
	}
}

func TestWorkloadTemplateSelection(t *testing.T) {
	for _, trID := range []string{"TR-VIRT-010", "TR-VIRT-027"} {
		dir := t.TempDir()
		name, err := stageConfig(dir, scenarioFor(trID).TemplateDir)
		if err != nil {
			t.Fatalf("%s: %v", trID, err)
		}
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("%s: staged config is missing: %v", trID, err)
		}
	}
}

func TestLargeSnapshotEnvironmentStartsProcess(t *testing.T) {
	const childFlag = "KB_CONFIG_TEST_CHILD"
	if os.Getenv(childFlag) == "1" {
		if os.Getenv("SNAPSHOT_COUNT") != "1250" || os.Getenv("SNAPSHOT_JOBS") != "" {
			t.Fatal("child process did not receive scalar snapshot inputs")
		}
		return
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	p := resolveParams([]core.TestRequirement{{Params: map[string]any{"replicas": 1, "snapshot_count": 1250}}})
	cmd := exec.CommandContext(t.Context(), executable, "-test.run=^TestLargeSnapshotEnvironmentStartsProcess$")
	cmd.Env = append(os.Environ(), buildEnv(p, "fast")...)
	cmd.Env = append(cmd.Env, childFlag+"=1")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("large snapshot workload could not start: %v\n%s", err, output)
	}
}
