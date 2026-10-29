package virtbench

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openshift-storage-cert/storage-cert-harness/internal/core"
)

func TestPreflightMissingBinaryStillChecksParameters(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	findings, err := (preflight{sc: Scenario{CommandCheck: []string{"fio", "--help"}, Validate: fioValidate}}).Check(
		context.Background(), &core.RunCtx{}, nil, []core.TestRequirement{{ID: "TR-STOR-002"}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 5 {
		t.Fatalf("findings = %+v, want binary, command, source, storage class, and parameter checks", findings)
	}
	for i, level := range []string{"error", "skip", "error", "error", "error"} {
		if findings[i].Level != level {
			t.Errorf("check %d = %+v, want %s", i, findings[i], level)
		}
	}
	if !strings.Contains(findings[1].Message, "CLI not found") {
		t.Fatal("command skip must explain missing CLI")
	}
}

func TestPreflightReplayReportsLiveChecksAsSkipped(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	for _, missing := range []bool{false, true} {
		dir := t.TempDir()
		wantLevel := "info"
		if missing {
			dir = filepath.Join(dir, "missing")
			wantLevel = "error"
		}
		sc := Scenario{CommandCheck: []string{"fio", "--help"}, Preflight: drainPreflight, Validate: func(core.TestRequirement) error {
			t.Fatal("live parameter validation called in replay")
			return nil
		}}
		findings, err := (preflight{sc: sc}).Check(context.Background(), &core.RunCtx{}, nil,
			[]core.TestRequirement{{ID: "TR-VIRT-008", Params: map[string]any{"results_dir": dir}}})
		if err != nil {
			t.Fatal(err)
		}
		if len(findings) != 8 || findings[0].Level != wantLevel {
			t.Fatalf("findings = %+v, want replay directory result and all 7 live checks", findings)
		}
		for _, f := range findings[1:] {
			if f.Level != "skip" || !strings.Contains(f.Message, "replay mode") {
				t.Errorf("finding = %+v, want skip with replay reason", f)
			}
		}
	}
}

func TestScenarioPreflightReportsSuccessfulNodeChecks(t *testing.T) {
	dir := t.TempDir()
	script := `#!/bin/sh
echo '{"items":[{"metadata":{"name":"worker-a","labels":{"node-role.kubernetes.io/worker":""}}},{"metadata":{"name":"worker-b","labels":{"node-role.kubernetes.io/worker":""}}}]}'
`
	if err := os.WriteFile(filepath.Join(dir, "kubectl"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	trs := []core.TestRequirement{{ID: "TR-VIRT-008", Params: map[string]any{"target_node": "worker-a", "node_name": "worker-b"}}}
	findings := drainPreflight(context.Background(), &core.RunCtx{}, trs)
	findings = append(findings, singleNodeCeilingPreflight(context.Background(), &core.RunCtx{}, trs)...)
	if len(findings) != 4 {
		t.Fatalf("findings = %+v, want kubectl, target_node, worker count, and pinned node checks", findings)
	}
	for _, f := range findings {
		if f.Level != "info" {
			t.Errorf("finding = %+v, want pass", f)
		}
	}
}

func TestDrainPreflightMissingKubectlSkipsWorkerLookup(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	findings := drainPreflight(context.Background(), &core.RunCtx{}, []core.TestRequirement{{ID: "TR-VIRT-008"}})
	if len(findings) != 3 || findings[0].Level != "error" || findings[1].Level != "error" || findings[2].Level != "skip" || !strings.Contains(findings[2].Message, "kubectl not found") {
		t.Fatalf("findings = %+v, want missing binary and target failures, skipped worker check with reason", findings)
	}
}

func TestSingleNodePreflightUnpinnedNodeIsSkipped(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	findings := singleNodeCeilingPreflight(context.Background(), &core.RunCtx{}, []core.TestRequirement{{ID: "TR-VIRT-016"}})
	if len(findings) != 1 || findings[0].Level != "skip" || !strings.Contains(findings[0].Message, "no node_name pinned") {
		t.Fatalf("findings = %+v, want skip with unpinned node reason", findings)
	}
}

func TestDataSourceSelection(t *testing.T) {
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
			tr.Params["datasource"] = tt.selection
			refs, err := datasourcePreflight(context.Background(), tr)
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
			staged, err := stageSelectedTemplate(tr, t.TempDir())
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
		Template:   vmTemplateConfig{Param: "custom_template", File: "custom-staged.yaml", Render: func(core.TestRequirement) string { return twoDiskVMTemplate }},
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
	staged, err := stageSelectedTemplate(core.TestRequirement{ID: "custom-workload", Params: map[string]any{"datasource": "images/custom"}}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(staged)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(staged) != "custom-staged.yaml" || !strings.Contains(string(data), "name: custom") {
		t.Fatalf("scenario template config ignored: %s: %s", staged, data)
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
