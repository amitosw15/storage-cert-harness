package kubeburner

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gitlab.cee.redhat.com/eco-special-projects/storage-cert-harness/internal/core"
)

func TestSnapshotJobsMarkerIsYAMLComment(t *testing.T) {
	if !strings.HasPrefix(snapshotJobsMarker, "# ") {
		t.Fatalf("snapshot job marker must be a YAML comment, got %q", snapshotJobsMarker)
	}
	asset, err := assets.ReadFile("assets/config.yaml")
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
	if _, err := writeConfig(dir, params); err != nil {
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
