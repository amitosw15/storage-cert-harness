package virtbench

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/openshift-storage-cert/storage-cert-harness/internal/core"
	"github.com/openshift-storage-cert/storage-cert-harness/internal/manifest"
)

func cloneSnapshotCLI(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("SNAPSHOT_TEST_DIR", dir)
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$SNAPSHOT_TEST_DIR/commands"
if [ "$1" = --kubeconfig ]; then shift 2; fi
case "$1 $2" in
  'get storageclass') echo '{"provisioner":"driver"}' ;;
  'get volumesnapshotclass')
    if [ "$3" = missing ]; then echo NotFound >&2; exit 1; fi
    echo '{"driver":"driver"}' ;;
  'get datasource') printf '%s' "$TEST_DATASOURCE" ;;
  'get pvc') echo '{"spec":{"storageClassName":"source-sc"}}' ;;
  'get volumesnapshot') echo '{"spec":{"volumeSnapshotClassName":"other-class"}}' ;;
  'create -f') /bin/cat > "$SNAPSHOT_TEST_DIR/created.json"; echo '{"metadata":{"name":"storage-cert-clone-test","namespace":"images"}}' ;;
  'wait volumesnapshot/storage-cert-clone-test') if [ "$TEST_WAIT_FAIL" = true ]; then exit 1; fi ;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "oc"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("TEST_DATASOURCE", `{"spec":{"source":{"pvc":{"name":"source-pvc","namespace":"images"}}}}`)
	return dir
}

func TestSnapshotTemplatePreservesCustomFieldsAndOriginal(t *testing.T) {
	source := []byte(`apiVersion: kubevirt.io/v1
kind: VirtualMachine
metadata:
  name: custom
  annotations: {custom: retained}
spec:
  runStrategy: Always
  template:
    spec:
      nodeSelector: {custom: retained}
  dataVolumeTemplates:
    - metadata: {name: disk, labels: {custom: retained}}
      spec:
        sourceRef: {kind: DataSource, name: original, namespace: images}
        storage: {storageClassName: fast, resources: {requests: {storage: 30Gi}}}
    - metadata: {name: blank}
      spec:
        source: {blank: {}}
        storage: {storageClassName: fast}
`)
	docs, err := manifest.Documents(source)
	if err != nil {
		t.Fatal(err)
	}
	c := &snapshotClone{Template: docs[0], Snapshots: map[string]cloneSource{"0": {Name: "existing", Namespace: "images"}}}
	path, err := c.prepare(t.Context(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := manifest.Documents(data)
	if err != nil {
		t.Fatal(err)
	}
	expected, err := manifest.Documents(source)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(c.Template, expected[0]) {
		t.Fatal("snapshot conversion mutated original VM")
	}
	volume := expected[0]["spec"].(map[string]any)["dataVolumeTemplates"].([]any)[0].(map[string]any)["spec"].(map[string]any)
	delete(volume, "sourceRef")
	volume["source"] = map[string]any{"snapshot": map[string]any{"name": "existing", "namespace": "images"}}
	if !reflect.DeepEqual(got[0], expected[0]) {
		t.Fatalf("conversion changed unrelated fields: %s", data)
	}
}

func TestSnapshotCloneUsesExplicitClassAndPreservesBlankDisk(t *testing.T) {
	dir := cloneSnapshotCLI(t)
	rc := &core.RunCtx{Backend: &core.ResolvedBackend{StorageClass: "backend", SnapshotClass: "nondefault"}, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	tr := core.TestRequirement{ID: "TR-VIRT-018", Params: map[string]any{"use_snapshot": true, "kubeconfig": "/chosen/kubeconfig"}}
	c, err := snapshotClonePreflight(context.Background(), rc, tr)
	if err != nil {
		t.Fatal(err)
	}
	commands, err := os.ReadFile(filepath.Join(dir, "commands"))
	if err != nil {
		t.Fatal(err)
	}
	for line := range strings.Lines(string(commands)) {
		if !strings.HasPrefix(line, "--kubeconfig /chosen/kubeconfig get ") {
			t.Fatalf("preflight mutation/wrong context: %s", line)
		}
	}
	path, err := c.prepare(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "created.json"))
	if err != nil {
		t.Fatal(err)
	}
	var snapshot struct {
		Spec struct {
			Class  string `yaml:"volumeSnapshotClassName"`
			Source struct {
				PVC string `yaml:"persistentVolumeClaimName"`
			} `json:"source"`
		} `json:"spec"`
	}
	if err := yaml.Unmarshal(data, &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.Spec.Class != "nondefault" || snapshot.Spec.Source.PVC != "source-pvc" {
		t.Fatalf("snapshot=%s", data)
	}
	data, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var vm struct {
		Spec struct {
			Volumes []struct {
				Spec struct {
					SourceRef any            `yaml:"sourceRef"`
					Source    map[string]any `yaml:"source"`
				} `yaml:"spec"`
			} `yaml:"dataVolumeTemplates"`
		} `yaml:"spec"`
	}
	if err := yaml.Unmarshal(data, &vm); err != nil {
		t.Fatal(err)
	}
	if len(vm.Spec.Volumes) != 2 || vm.Spec.Volumes[0].Spec.SourceRef != nil || vm.Spec.Volumes[0].Spec.Source["snapshot"] == nil || vm.Spec.Volumes[1].Spec.Source["blank"] == nil {
		t.Fatalf("incorrect staged VM: %s", data)
	}
	c.cleanup(context.Background(), rc)
	commands, err = os.ReadFile(filepath.Join(dir, "commands"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(commands), "delete volumesnapshot storage-cert-clone-test -n images") || strings.Contains(string(commands), "patch ") {
		t.Fatalf("unexpected commands: %s", commands)
	}
}

func TestSnapshotClonePreflightRejectsMissingClassAndWrongSourceClass(t *testing.T) {
	for _, tt := range []struct{ name, configured, datasource, want string }{
		{name: "missing class", configured: "missing", want: `snapshot class "missing" lookup failed`},
		{name: "existing snapshot mismatch", configured: "nondefault", datasource: `{"spec":{"source":{"snapshot":{"name":"existing","namespace":"images"}}}}`, want: "uses snapshot class"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cloneSnapshotCLI(t)
			if tt.datasource != "" {
				t.Setenv("TEST_DATASOURCE", tt.datasource)
			}
			rc := &core.RunCtx{Backend: &core.ResolvedBackend{StorageClass: "backend", SnapshotClass: tt.configured}}
			_, err := snapshotClonePreflight(context.Background(), rc, core.TestRequirement{ID: "TR-VIRT-018"})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error=%v, want %s", err, tt.want)
			}
		})
	}
}

func TestSnapshotCloneCleanupAfterWaitFailure(t *testing.T) {
	dir := cloneSnapshotCLI(t)
	t.Setenv("TEST_WAIT_FAIL", "true")
	rc := &core.RunCtx{Backend: &core.ResolvedBackend{StorageClass: "backend", SnapshotClass: "nondefault"}, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	c, err := snapshotClonePreflight(context.Background(), rc, core.TestRequirement{ID: "TR-VIRT-018"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.prepare(context.Background(), t.TempDir()); err == nil {
		t.Fatal("expected wait failure")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c.cleanup(ctx, rc)
	commands, err := os.ReadFile(filepath.Join(dir, "commands"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(commands), "delete volumesnapshot storage-cert-clone-test -n images") {
		t.Fatalf("partial failure not cleaned up: %s", commands)
	}
}

func TestSnapshotCloneReplayDoesNotReadCluster(t *testing.T) {
	dir := cloneSnapshotCLI(t)
	tr := core.TestRequirement{ID: "TR-VIRT-018", Params: map[string]any{"use_snapshot": true, "results_dir": t.TempDir()}}
	findings, err := (preflight{sc: Scenario{AutomationTool: "virtbench datasource-clone"}}).Check(context.Background(), &core.RunCtx{}, core.NewBag(), []core.TestRequirement{tr})
	if err != nil {
		t.Fatal(err)
	}
	for _, finding := range findings {
		if finding.Level == "error" {
			t.Fatalf("replay failed: %+v", finding)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "commands")); !os.IsNotExist(err) {
		t.Fatalf("replay accessed cluster: %v", err)
	}
}

func TestSnapshotCloneLocalDataSourceNamespaces(t *testing.T) {
	for _, tt := range []struct {
		name, selection, backing string
		count                    int
		wantNamespaces           []string
		wantSourceNamespace      string
		wantError                string
	}{
		{name: "one local VM", count: 1, backing: `{"pvc":{"name":"source-pvc"}}`, wantNamespaces: []string{"local-1"}, wantSourceNamespace: "local-1"},
		{name: "multiple locals share source", count: 2, backing: `{"pvc":{"name":"source-pvc","namespace":"shared"}}`, wantNamespaces: []string{"local-1", "local-2"}, wantSourceNamespace: "shared"},
		{name: "different local sources", count: 2, backing: `{"pvc":{"name":"source-pvc"}}`, wantNamespaces: []string{"local-1", "local-2"}, wantError: "resolves to different sources"},
		{name: "explicit override", count: 2, selection: "fedora-local", backing: `{"pvc":{"name":"source-pvc"}}`, wantNamespaces: []string{imageNamespace}, wantSourceNamespace: imageNamespace},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := cloneSnapshotCLI(t)
			t.Setenv("TEST_DATASOURCE", `{"spec":{"source":`+tt.backing+`}}`)
			template := strings.Replace(twoDiskVMTemplate, "namespace: openshift-virtualization-os-images", "", 1)
			path := filepath.Join(dir, "local.yaml")
			if err := os.WriteFile(path, []byte(template), 0600); err != nil {
				t.Fatal(err)
			}
			tr := core.TestRequirement{ID: "TR-VIRT-018", Params: map[string]any{"vm_template": path, "namespace_prefix": "local", "iteration_clones": tt.count}}
			tr.Params["datasource"] = tt.selection
			rc := &core.RunCtx{Backend: &core.ResolvedBackend{StorageClass: "backend", SnapshotClass: "nondefault"}}
			clone, err := snapshotClonePreflight(context.Background(), rc, tr)
			if tt.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantError) || !strings.Contains(err.Error(), "datasource: namespace/name") {
					t.Fatalf("want actionable error %q, got %v", tt.wantError, err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if source := clone.Sources[0]; source.Namespace != tt.wantSourceNamespace || source.Name != "source-pvc" {
					t.Fatalf("resolved wrong backing source: %+v", source)
				}
			}
			commands, err := os.ReadFile(filepath.Join(dir, "commands"))
			if err != nil {
				t.Fatal(err)
			}
			for _, ns := range tt.wantNamespaces {
				if !strings.Contains(string(commands), "get datasource ") || !strings.Contains(string(commands), " -n "+ns+" -o json") {
					t.Fatalf("missing local lookup in %s: %s", ns, commands)
				}
			}
			if tt.selection == "" && strings.Contains(string(commands), " -n "+imageNamespace+" ") {
				t.Fatalf("looked up unrelated image namespace: %s", commands)
			}
			if strings.Contains(string(commands), "create ") {
				t.Fatalf("preflight created resources: %s", commands)
			}
		})
	}
}

func TestSnapshotCloneDataSourceOverrideBeforeResolution(t *testing.T) {
	dir := cloneSnapshotCLI(t)
	tr := core.TestRequirement{ID: "TR-VIRT-018", Params: map[string]any{"datasource": "images/selected", "use_snapshot": true}}
	rc := &core.RunCtx{Backend: &core.ResolvedBackend{StorageClass: "target", SnapshotClass: "nondefault"}}
	if _, err := snapshotClonePreflight(context.Background(), rc, tr); err != nil {
		t.Fatal(err)
	}
	commands, _ := os.ReadFile(filepath.Join(dir, "commands"))
	if !strings.Contains(string(commands), "get datasource selected -n images") {
		t.Fatalf("override missing: %s", commands)
	}
}
