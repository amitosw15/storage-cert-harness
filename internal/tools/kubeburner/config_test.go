package kubeburner

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openshift-storage-cert/storage-cert-harness/internal/core"
)

func TestSnapshotJobsMarkerIsYAMLComment(t *testing.T) {
	if !strings.HasPrefix(snapshotJobsMarker, "# ") {
		t.Fatalf("snapshot job marker must be a YAML comment, got %q", snapshotJobsMarker)
	}
	asset, err := templates.ReadFile(workloadTemplatesDir + "/config.yaml")
	if err != nil {
		t.Fatalf("read config asset: %v", err)
	}
	if !strings.Contains(string(asset), "\n"+snapshotJobsMarker+"\n") {
		t.Fatalf("config asset does not contain snapshot job marker %q", snapshotJobsMarker)
	}
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

func TestWriteConfigReplacesYAMLCommentMarker(t *testing.T) {
	dir := t.TempDir()
	params := Params{Replicas: 2, SnapshotCount: 3, snapshotCountSet: true}
	if _, err := stageSnapshotConfigForTest(dir, params); err != nil {
		t.Fatalf("writeConfig: %v", err)
	}

	config, err := os.ReadFile(filepath.Join(dir, "config.yaml"))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if strings.Contains(string(config), snapshotJobsMarker) {
		t.Fatalf("config still contains snapshot job marker %q", snapshotJobsMarker)
	}
	if !strings.Contains(string(config), "name: vmsnapshot-snapshot-1") {
		t.Fatalf("config does not contain generated snapshot batch: %s", config)
	}
}

func stageSnapshotConfigForTest(dir string, params Params) (string, error) {
	config, err := stageConfig(dir, workloadTemplatesDir)
	if err != nil {
		return "", err
	}
	if err := prepareSnapshotConfig(filepath.Join(dir, config), params); err != nil {
		return "", err
	}
	return config, nil
}

func TestStageConfigPreservesSources(t *testing.T) {
	for _, trID := range []string{"TR-VIRT-010", "TR-VIRT-027"} {
		dir := t.TempDir()
		sourceDir := scenarioFor(trID).TemplateDir
		config, err := stageConfig(dir, sourceDir)
		if err != nil {
			t.Fatal(err)
		}
		if config != "config.yaml" {
			t.Fatalf("config path = %q", config)
		}
		entries, err := templates.ReadDir(sourceDir)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			source, err := templates.ReadFile(sourceDir + "/" + entry.Name())
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "templates", entry.Name())
			if entry.Name() == config {
				path = filepath.Join(dir, config)
			}
			staged, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(source, staged) {
				t.Fatalf("%s: source %s changed: %v", trID, entry.Name(), err)
			}
		}
	}
}

func TestStageConfigRequiresConfig(t *testing.T) {
	for _, sourceDir := range []string{"templates", "templates/missing"} {
		if _, err := stageConfig(t.TempDir(), sourceDir); err == nil {
			t.Fatalf("accepted source directory %q without config.yaml", sourceDir)
		}
	}
}
