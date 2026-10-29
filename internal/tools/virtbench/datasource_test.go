package virtbench

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/openshift-storage-cert/storage-cert-harness/internal/core"
	"github.com/openshift-storage-cert/storage-cert-harness/internal/manifest"
)

func TestDataSourceTemplatePreservesCustomFields(t *testing.T) {
	source := []byte(`apiVersion: kubevirt.io/v1
kind: VirtualMachine
metadata:
  name: rhel-9-vm
  annotations: {custom: retained}
spec:
  runStrategy: Always
  dataVolumeTemplates:
    - metadata: {name: root, labels: {custom: retained}}
      spec:
        sourceRef: {kind: DataSource, name: original, namespace: images}
        pvc: {storageClassName: fast, volumeMode: Block, accessModes: [ReadWriteMany]}
    - metadata: {name: blank}
      spec:
        source: {blank: {}}
        storage: {storageClassName: fixed, volumeMode: Filesystem}
  template:
    spec:
      volumes:
        - name: cloudinit
          cloudInitNoCloud: {userData: "#cloud-config\ncustom: retained"}
`)
	before, err := manifest.Documents(source)
	if err != nil {
		t.Fatal(err)
	}
	for _, defaults := range []bool{false, true} {
		rendered, refs, err := selectVMSource(source, "chosen/new-image", defaults)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(refs, []cloneSource{{Name: "new-image", Namespace: "chosen"}}) {
			t.Fatalf("refs=%+v", refs)
		}
		docs, err := manifest.Documents(rendered)
		if err != nil {
			t.Fatal(err)
		}
		expected, err := manifest.Documents(source)
		if err != nil {
			t.Fatal(err)
		}
		volumes := expected[0]["spec"].(map[string]any)["dataVolumeTemplates"].([]any)
		root := volumes[0].(map[string]any)["spec"].(map[string]any)
		root["sourceRef"] = map[string]any{"kind": "DataSource", "name": "new-image", "namespace": "chosen"}
		if defaults {
			delete(root["pvc"].(map[string]any), "volumeMode")
			delete(volumes[1].(map[string]any)["spec"].(map[string]any)["storage"].(map[string]any), "volumeMode")
		}
		if !reflect.DeepEqual(docs, expected) {
			t.Fatalf("custom VM fields changed: %s", rendered)
		}
	}
	after, _ := manifest.Documents(source)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("source mutated")
	}
	unchanged, _, err := selectVMSource(source, "", false)
	if err != nil || string(unchanged) != string(source) {
		t.Fatal("unselected custom source changed", err)
	}
	if _, _, err := selectVMSource([]byte("kind: VirtualMachine\nspec: {}"), "images/selected", false); err == nil || !strings.Contains(err.Error(), "no DataSource sourceRef") {
		t.Fatalf("missing sourceRef accepted: %v", err)
	}
}

func TestDataSourceAppliedAtEveryCLIProvisioningBoundary(t *testing.T) {
	runtime := t.TempDir()
	t.Setenv("VIRTBENCH_REPO", runtime)
	for _, path := range []string{"examples/vm-templates/rhel9-vm-datasource.yaml", "disk-ops-benchmark/vm-template.yaml"} {
		path = filepath.Join(runtime, path)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(twoDiskVMTemplate), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"TR-VIRT-001", "TR-VIRT-002", "TR-VIRT-013", "TR-VIRT-018", "TR-STOR-001", "TR-STOR-002", "TR-VIRT-008"} {
		t.Run(id, func(t *testing.T) {
			tr := core.TestRequirement{ID: id, Params: map[string]any{"datasource": "images/selected", "storage_class": "fast", "num_vms": 1, "fio_bs": "4k", "target_node": "worker-a"}}
			root := t.TempDir()
			sc, ok := scenarioFor(tr)
			if !ok {
				t.Fatal("missing scenario")
			}
			var args []string
			var err error
			if id == "TR-VIRT-008" {
				args, err = drainCloneArgs(&core.RunCtx{}, tr, root, "", sc.NSPrefix)
			} else {
				args, err = sc.BuildArgs(&core.RunCtx{}, core.NewBag(), tr, root)
			}
			if err != nil {
				t.Fatal(err)
			}
			entries, err := os.ReadDir(root)
			if err != nil || len(entries) != 0 {
				t.Fatalf("redundant staging: %v %v", entries, err)
			}
			args, err = prepareManifest(t.Context(), manifestRequest{Directory: root, Args: args, TR: tr})
			if err != nil {
				t.Fatal(err)
			}
			path, _ := argument(args, "--vm-template")
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			_, refs, err := selectVMSource(data, "", false)
			if err != nil || len(refs) != 1 || refs[0].Name != "selected" || refs[0].Namespace != "images" {
				t.Fatalf("datasource not selected: %+v %v", refs, err)
			}
			if strings.Contains(string(data), "{{") || strings.Contains(string(data), "volumeMode:") {
				t.Fatalf("unresolved inputs or forced volume mode: %s", data)
			}
			files, err := filepath.Glob(filepath.Join(root, "rendered", "*.yaml"))
			if err != nil || len(files) != 1 {
				t.Fatalf("expected one runtime manifest: %v %v", files, err)
			}
		})
	}
}

func TestPreparedSnapshotIsValidatedWithoutSelectingDataSourceAgain(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "snapshot-vm.yaml")
	data := []byte("apiVersion: kubevirt.io/v1\nkind: VirtualMachine\nmetadata: {name: rhel-9-vm}\nspec:\n  dataVolumeTemplates:\n  - metadata: {name: root}\n    spec:\n      source: {snapshot: {name: selected-snapshot, namespace: images}}\n")
	if err := os.WriteFile(source, data, 0o600); err != nil {
		t.Fatal(err)
	}
	request := manifestRequest{Directory: root, Prepared: true, TR: core.TestRequirement{ID: "TR-VIRT-002", Params: map[string]any{"datasource": "images/selected"}}, Args: []string{"datasource-clone", "--storage-class", "fast", "--vm-template", source}}
	args, err := prepareManifest(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if path, _ := argument(args, "--vm-template"); path != source {
		t.Fatal("prepared snapshot staged again", path)
	}
	if _, err := os.Stat(filepath.Join(root, "rendered")); !os.IsNotExist(err) {
		t.Fatal("prepared snapshot rendered again", err)
	}
	if err := os.WriteFile(source, []byte("kind: Pod"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := prepareManifest(t.Context(), request); err == nil {
		t.Fatal("prepared file skipped VM validation")
	}
}

func TestManifestRejectsOverwritingCustomSource(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "rendered")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(dir, "datasource-clone-vm.yaml")
	if err := os.WriteFile(source, []byte(twoDiskVMTemplate), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := prepareManifest(t.Context(), manifestRequest{Directory: root, Args: []string{"datasource-clone", "--storage-class", "fast", "--vm-template", source}})
	if err == nil || !strings.Contains(err.Error(), "rendering destination") {
		t.Fatal("source overwrite accepted", err)
	}
	original, err := os.ReadFile(source)
	if err != nil || string(original) != twoDiskVMTemplate {
		t.Fatal("source changed", err)
	}
}

func TestFailureRecoveryAppliesSelectedDataSourceInEachNamespace(t *testing.T) {
	root := t.TempDir()
	t.Setenv("PATH", root)
	t.Setenv("APPLIED", filepath.Join(root, "applied.yaml"))
	script := `#!/bin/sh
case "$1 $2" in
 'apply -f') printf '\n---\n' >> "$APPLIED"; /bin/cat >> "$APPLIED" ;;
 'get vmi') printf 'Running' ;;
 *) exit 1 ;;
esac
`
	if err := os.WriteFile(filepath.Join(root, "kubectl"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "custom.yaml")
	original := failureRecoveryVMTemplate
	if err := os.WriteFile(source, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	tr := core.TestRequirement{ID: "TR-VIRT-007", Params: map[string]any{
		"datasource": "chosen/recovery-image", "storage_class": "fast", "vm_template": source,
		"node": "worker-a", "node_hostname_label": "worker-a", "namespace_prefix": "recovery", "vm_count": 2,
	}}
	rc := &core.RunCtx{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if err := provisionFailureRecoveryVMs(t.Context(), rc, core.NewBag(), []core.TestRequirement{tr}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, "applied.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	docs, err := manifest.Documents(data)
	if err != nil {
		t.Fatal(err)
	}
	var namespaces []string
	for _, doc := range docs {
		if doc["kind"] != "VirtualMachine" {
			continue
		}
		namespaces = append(namespaces, doc["metadata"].(map[string]any)["namespace"].(string))
		volumes := doc["spec"].(map[string]any)["dataVolumeTemplates"].([]any)
		ref := volumes[0].(map[string]any)["spec"].(map[string]any)["sourceRef"].(map[string]any)
		if ref["name"] != "recovery-image" || ref["namespace"] != "chosen" {
			t.Fatal("source not selected", ref)
		}
	}
	if !reflect.DeepEqual(namespaces, []string{"recovery-1", "recovery-2"}) {
		t.Fatalf("namespaces=%v", namespaces)
	}
	after, err := os.ReadFile(source)
	if err != nil || string(after) != original {
		t.Fatal("custom recovery source changed", err)
	}
}
