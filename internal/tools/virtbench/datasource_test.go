package virtbench

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/openshift-storage-cert/storage-cert-harness/internal/core"
)

func TestDataSourceSelection(t *testing.T) {
	tr := core.TestRequirement{Params: map[string]any{"datasource": "images/plan"}}
	if got := selectedDataSource(&core.RunCtx{DataSource: "cli"}, tr); got != "cli" {
		t.Fatal(got)
	}
	if got := selectedDataSource(&core.RunCtx{}, tr); got != "images/plan" {
		t.Fatal(got)
	}
	if got := selectedDataSource(nil, core.TestRequirement{}); got != "" {
		t.Fatal(got)
	}
	for _, tt := range []struct {
		value, ns, name string
		bad             bool
	}{
		{"fedora-nas", imageNamespace, "fedora-nas", false},
		{"images/rhel9-nas", "images", "rhel9-nas", false},
		{"/name", "", "", true}, {"ns/", "", "", true}, {"ns/a/b", "", "", true}, {"Upper", "", "", true}, {"bad_ns/name", "", "", true},
	} {
		t.Run(tt.value, func(t *testing.T) {
			ref, err := parseDataSource(tt.value)
			if (err != nil) != tt.bad {
				t.Fatalf("ref=%+v err=%v", ref, err)
			}
			if !tt.bad && (ref.Namespace != tt.ns || ref.Name != tt.name) {
				t.Fatal(ref)
			}
		})
	}
}

func TestTemplateLocalDataSourcePreflight(t *testing.T) {
	for _, tt := range []struct {
		name, namespace, selection string
		wantNamespaces             []string
	}{
		{name: "omitted", wantNamespaces: []string{"my-tests-2", "my-tests-3"}},
		{name: "empty", namespace: `namespace: ""`, wantNamespaces: []string{"my-tests-2", "my-tests-3"}},
		{name: "qualified template", namespace: "namespace: custom-images", wantNamespaces: []string{"custom-images"}},
		{name: "unqualified override", selection: "fedora-local", wantNamespaces: []string{imageNamespace}},
		{name: "qualified override", selection: "other-images/fedora-local", wantNamespaces: []string{"other-images"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			commands := filepath.Join(dir, "commands")
			t.Setenv("DS_COMMANDS", commands)
			t.Setenv("DS_NAMESPACES", " "+strings.Join(tt.wantNamespaces, " ")+" ")
			script := `#!/bin/sh
printf '%s\n' "$*" >> "$DS_COMMANDS"
case "$DS_NAMESPACES" in
  *" $5 "*) ;;
  *) echo "wrong namespace: $5" >&2; exit 1 ;;
esac
case "$2" in
  datasource) echo '{"spec":{"source":{"pvc":{"name":"local-image"}}},"status":{"conditions":[{"type":"Ready","status":"True"}]}}' ;;
  pvc) echo '{"status":{"phase":"Bound"}}' ;;
  *) exit 1 ;;
esac
`
			if err := os.WriteFile(filepath.Join(dir, "oc"), []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", dir)
			data := "kind: VirtualMachine\nmetadata:\n  namespace: ignored-by-virtbench\nspec:\n  dataVolumeTemplates:\n  - metadata:\n      name: disk\n    spec:\n      sourceRef:\n        kind: DataSource\n        name: fedora-local\n        " + tt.namespace + "\n"
			path := filepath.Join(dir, "vm.yaml")
			if err := os.WriteFile(path, []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			tr := core.TestRequirement{ID: "TR-VIRT-002", Params: map[string]any{"vm_template": path, "namespace_prefix": "my-tests", "start": 2, "iteration_clones": 3}}
			rc := &core.RunCtx{DataSource: tt.selection}
			refs, err := datasourcePreflight(context.Background(), rc, tr)
			if err != nil {
				t.Fatal(err)
			}
			if len(refs) != len(tt.wantNamespaces) {
				t.Fatalf("refs = %+v", refs)
			}
			log, err := os.ReadFile(commands)
			if err != nil {
				t.Fatal(err)
			}
			for _, ns := range tt.wantNamespaces {
				for _, query := range []string{"datasource fedora-local", "pvc local-image"} {
					if !strings.Contains(string(log), "get "+query+" -n "+ns+" -o json") {
						t.Fatalf("missing namespace lookup: %s", log)
					}
				}
			}
			staged, err := stageSelectedTemplate(rc, tr, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			out, err := os.ReadFile(staged)
			if err != nil {
				t.Fatal(err)
			}
			if tt.selection == "" && string(out) != data {
				t.Fatal("custom template changed without an override")
			}
			original, err := os.ReadFile(path)
			if err != nil || string(original) != data {
				t.Fatal("original template changed", err)
			}
		})
	}
}

func TestLocalDataSourceUsesScenarioNamespaceRange(t *testing.T) {
	original := scenarios
	t.Cleanup(func() { scenarios = original })
	scenarios = append(append([]Scenario(nil), scenarios...), Scenario{
		ProvidesTR: "custom-workload",
		NSPrefix:   "custom",
		VMRange: func(core.TestRequirement) (int, int, error) {
			return 7, 8, nil
		},
	})
	refs, err := resolveLocalDataSources(core.TestRequirement{ID: "custom-workload"}, []cloneSource{{Name: "local-image"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 2 || refs[0].Namespace != "custom-7" || refs[1].Namespace != "custom-8" {
		t.Fatalf("ignored scenario namespace range: %+v", refs)
	}
}

func TestStagedDataSourcePreservesSourceAndStorage(t *testing.T) {
	for _, id := range []string{"TR-STOR-002", "TR-VIRT-018", "TR-VIRT-008", "TR-VIRT-007"} {
		t.Run(id, func(t *testing.T) {
			tr := core.TestRequirement{ID: id, Params: map[string]any{"datasource": "images/selected", "target_node": "worker"}}
			data, _, err := vmSourceTemplate(tr)
			if err != nil {
				t.Fatal(err)
			}
			custom := strings.Replace(string(data), "storageClassName:", "volumeMode: Filesystem\n          storageClassName:", 1)
			src := filepath.Join(t.TempDir(), "custom.yaml")
			if err := os.WriteFile(src, []byte(custom), 0600); err != nil {
				t.Fatal(err)
			}
			key := "vm_template"
			if id == "TR-STOR-002" {
				key = "fio_vm_template"
			}
			tr.Params[key] = src
			path, err := stageSelectedTemplate(&core.RunCtx{DataSource: "other/cli"}, tr, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			out, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{"name: cli", "namespace: other", "volumeMode: Filesystem", "{{STORAGE_CLASS_NAME}}"} {
				if !strings.Contains(string(out), want) {
					t.Fatalf("missing %s in %s", want, out)
				}
			}
			if strings.Contains(custom, "blank:") && !strings.Contains(string(out), "blank:") {
				t.Fatal("blank disk lost")
			}
			if before, after := cloudInitData(t, []byte(custom)), cloudInitData(t, out); before != after {
				t.Fatal("cloud-init content changed")
			}
			original, _ := os.ReadFile(src)
			if string(original) != custom {
				t.Fatal("original mutated")
			}
		})
	}
}

func TestDefaultStorageProfileAndNoDataSourceError(t *testing.T) {
	data := strings.Replace(twoDiskVMTemplate, "storageClassName:", "accessModes: [ReadWriteMany]\n          volumeMode: Block\n          storageClassName:", 1)
	out, _, err := rewriteVMSource([]byte(data), "fedora-nas", true)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "volumeMode:") || !strings.Contains(string(out), "ReadWriteMany") {
		t.Fatalf("storage defaults: %s", out)
	}
	if _, _, err := rewriteVMSource([]byte("kind: VirtualMachine\nspec: {}\n"), "fedora-nas", false); err == nil || !strings.Contains(err.Error(), "no DataSource") {
		t.Fatal(err)
	}
}

func TestDataSourceReadiness(t *testing.T) {
	for _, tt := range []struct{ name, ds, pvc, snap, want string }{
		{name: "missing", want: "not found"},
		{name: "unready", ds: `{"spec":{"source":{"pvc":{"name":"disk"}}}}`, want: "not Ready"},
		{name: "pending pvc", ds: `{"spec":{"source":{"pvc":{"name":"disk"}}},"status":{"conditions":[{"type":"Ready","status":"True"}]}}`, pvc: `{"status":{"phase":"Pending"}}`, want: "not Bound"},
		{name: "bound pvc", ds: `{"spec":{"source":{"pvc":{"name":"disk"}}},"status":{"conditions":[{"type":"Ready","status":"True"}]}}`, pvc: `{"status":{"phase":"Bound"}}`},
		{name: "unready snapshot", ds: `{"spec":{"source":{"snapshot":{"name":"disk"}}},"status":{"conditions":[{"type":"Ready","status":"True"}]}}`, snap: `{"status":{"readyToUse":false}}`, want: "not readyToUse"},
		{name: "ready snapshot", ds: `{"spec":{"source":{"snapshot":{"name":"disk"}}},"status":{"conditions":[{"type":"Ready","status":"True"}]}}`, snap: `{"status":{"readyToUse":true}}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			script := `#!/bin/sh
if [ "$1" = --kubeconfig ]; then shift 2; fi
case "$2" in
 datasource) if [ -z "$DS" ]; then echo 'not found' >&2; exit 1; fi; printf '%s' "$DS" ;;
 pvc) printf '%s' "$PVC" ;;
 volumesnapshot) printf '%s' "$SNAP" ;;
esac
`
			if err := os.WriteFile(filepath.Join(dir, "oc"), []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", dir)
			t.Setenv("DS", tt.ds)
			t.Setenv("PVC", tt.pvc)
			t.Setenv("SNAP", tt.snap)
			err := checkDataSource(context.Background(), "chosen", cloneSource{Name: "nas", Namespace: "images"})
			if tt.want == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("want %s got %v", tt.want, err)
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

func TestDataSourceReplayIsClusterFree(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	tr := core.TestRequirement{ID: "TR-STOR-002", Params: map[string]any{"datasource": "missing", "results_dir": t.TempDir()}}
	findings, err := (preflight{sc: Scenario{}}).Check(context.Background(), &core.RunCtx{}, core.NewBag(), []core.TestRequirement{tr})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range findings {
		if f.Level == "error" {
			t.Fatal(f)
		}
	}
}

func TestStagingDefaultUpstreamAndMultipleDocuments(t *testing.T) {
	repo := t.TempDir()
	dir := filepath.Join(repo, "examples/vm-templates")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	original := strings.Replace(twoDiskVMTemplate, "storageClassName:", "volumeMode: Block\n          storageClassName:", 1) + "\n---\napiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: preserved\ndata:\n  text: |\n    keep cloud-init-like content\n"
	source := filepath.Join(dir, "rhel9-vm-datasource.yaml")
	if err := os.WriteFile(source, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VIRTBENCH_REPO", repo)
	for _, selection := range []string{"", "images/selected"} {
		tr := core.TestRequirement{ID: "TR-VIRT-002", Params: map[string]any{"datasource": selection}}
		path, err := stageSelectedTemplate(nil, tr, t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		out, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(out), "volumeMode:") || !strings.Contains(string(out), "kind: ConfigMap") || !strings.Contains(string(out), "keep cloud-init-like content") {
			t.Fatalf("staged content: %s", out)
		}
		if selection == "" && !strings.Contains(string(out), "name: rhel9") {
			t.Fatal("default source changed")
		}
		if selection != "" && !strings.Contains(string(out), "name: selected") {
			t.Fatal("override not applied")
		}
	}
	after, _ := os.ReadFile(source)
	if string(after) != original {
		t.Fatal("upstream source template mutated")
	}
}

func cloudInitData(t *testing.T, data []byte) string {
	t.Helper()
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(templateToken.ReplaceAllString(string(data), "placeholder")), &doc); err != nil {
		t.Fatal(err)
	}
	var result string
	var walk func(*yaml.Node)
	walk = func(n *yaml.Node) {
		if cloud := yamlField(n, "cloudInitNoCloud"); cloud != nil {
			if user := yamlField(cloud, "userData"); user != nil {
				result = user.Value
			}
		}
		for _, child := range n.Content {
			walk(child)
		}
	}
	walk(&doc)
	return result
}
