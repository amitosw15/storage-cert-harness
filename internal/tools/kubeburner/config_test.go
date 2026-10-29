package kubeburner

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openshift-storage-cert/storage-cert-harness/internal/core"
	"github.com/openshift-storage-cert/storage-cert-harness/internal/manifest"
)

func renderConfig(t *testing.T, p Params) string {
	t.Helper()
	asset, err := templates.ReadFile(snapshotTemplatesDir + "/config.yaml")
	if err != nil {
		t.Fatal(err)
	}
	values := map[string]any{}
	for _, entry := range buildEnv(p, "fast") {
		key, value, _ := strings.Cut(entry, "=")
		values[key] = value
	}
	data, err := (manifest.GoTemplate{}).Render("config.yaml", asset, values)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
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

func TestWriteConfigPreservesEveryNativeTemplate(t *testing.T) {
	dir := t.TempDir()
	if _, err := writeConfig(dir); err != nil {
		t.Fatalf("writeConfig: %v", err)
	}
	entries, err := templates.ReadDir(snapshotTemplatesDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		source, err := templates.ReadFile(snapshotTemplatesDir + "/" + entry.Name())
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
		source, err := templates.ReadFile(snapshotTemplatesDir + "/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := (manifest.GoTemplate{}).Render(name, source, values); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
}
