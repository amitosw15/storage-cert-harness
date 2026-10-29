package virtbench

import (
	"bytes"
	"context"
	"errors"
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

func TestManifestAdapterRendersFIOWithMatchingCLIValues(t *testing.T) {
	for _, custom := range []bool{false, true} {
		t.Run(map[bool]string{false: "defaults", true: "overrides"}[custom], func(t *testing.T) {
			root := t.TempDir()
			source := filepath.Join(root, "source.yaml")
			if err := os.WriteFile(source, []byte(fioVMTemplate), 0o600); err != nil {
				t.Fatal(err)
			}
			args := []string{"--kubeconfig", "/chosen", "fio", "--action", "deploy", "--storage-class=fast", "--vm-template", source}
			if custom {
				args = append(args, "--vm-name=chosen", "--fio-runtime=75", "--fio-iodepth", "8", "--fio-numjobs", "2", "--fio-size", "2G")
			}
			before := append([]string(nil), args...)
			got, err := manifests.Prepare(t.Context(), manifest.Request{Directory: root, Args: args})
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(args, before) {
				t.Fatal("adapter mutated input arguments")
			}
			path, _ := argument(got, "--vm-template")
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if path == source || bytes.Contains(data, []byte("{{")) {
				t.Fatal("expected separate, completely rendered manifest")
			}
			for flag, field := range map[string]string{"--fio-runtime": "runtime", "--fio-bs": "bs", "--fio-rw": "rw", "--fio-iodepth": "iodepth", "--fio-numjobs": "numjobs", "--fio-size": "size"} {
				value, present := argument(got, flag)
				if !present || !strings.Contains(string(data), field+"="+value+"\n") {
					t.Fatalf("manifest does not match %s=%s", flag, value)
				}
			}
			docs, err := manifest.Documents(data)
			if err != nil {
				t.Fatal(err)
			}
			name, _ := argument(got, "--vm-name")
			if docs[0]["metadata"].(map[string]any)["name"] != name || !strings.Contains(string(data), `storageClassName: "fast"`) {
				t.Fatalf("VM identity/storage disagree: %s", data)
			}
			original, err := os.ReadFile(source)
			if err != nil || string(original) != fioVMTemplate {
				t.Fatalf("source template changed: %v", err)
			}
			info, err := os.Stat(path)
			if err != nil || info.Mode().Perm() != 0o600 {
				t.Fatalf("rendered file permissions: %v %v", info, err)
			}
		})
	}
}

func TestManifestAdapterLoadsReleasedLegacyTemplates(t *testing.T) {
	for command, relative := range map[string]string{
		"datasource-clone": "examples/vm-templates/rhel9-vm-datasource.yaml",
		"disk-ops":         "disk-ops-benchmark/vm-template.yaml",
	} {
		t.Run(command, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("VIRTBENCH_REPO", root)
			path := filepath.Join(root, relative)
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			source := []byte("apiVersion: kubevirt.io/v1\nkind: VirtualMachine\nmetadata:\n  name: {{VM_NAME}}\nspec:\n  storageClass: {{STORAGE_CLASS}}\n  custom: retained\n")
			if err := os.WriteFile(path, source, 0o600); err != nil {
				t.Fatal(err)
			}
			args, err := manifests.Prepare(t.Context(), manifest.Request{Directory: t.TempDir(), Args: []string{command, "--storage-class", "fast", "--vm-name", "chosen"}})
			if err != nil {
				t.Fatal(err)
			}
			rendered, _ := argument(args, "--vm-template")
			data, err := os.ReadFile(rendered)
			if err != nil || !strings.Contains(string(data), "name: chosen") || !strings.Contains(string(data), "storageClass: fast") || !strings.Contains(string(data), "custom: retained") {
				t.Fatalf("rendered=%s error=%v", data, err)
			}
			original, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(original, source) {
				t.Fatalf("released source modified: %v", err)
			}
		})
	}
}

func TestManifestAdapterSkipsNonProvisioningActions(t *testing.T) {
	for _, args := range [][]string{{"failure-recovery"}, {"drain-nodes"}, {"fio", "--action", "cleanup"}, {"fio", "--action", "gather-results"}} {
		if _, err := manifests.Prepare(t.Context(), manifest.Request{Directory: t.TempDir(), Args: args}); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := manifests.Prepare(ctx, manifest.Request{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v", err)
	}
}

func TestExecVirtbenchRendersBeforeStartingProcess(t *testing.T) {
	root := t.TempDir()
	script := `#!/bin/sh
while [ "$#" -gt 0 ]; do
  if [ "$1" = --vm-template ]; then /bin/cat "$2" > received.yaml; exit; fi
  shift
done
exit 1
`
	if err := os.WriteFile(filepath.Join(root, "virtbench"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", root)
	rc := &core.RunCtx{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if err := execVirtbench(t.Context(), rc, root, []string{"fio", "--storage-class", "fast"}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, "received.yaml"))
	if err != nil || bytes.Contains(data, []byte("{{")) || !bytes.Contains(data, []byte("runtime=600")) {
		t.Fatalf("process received unrendered input: %s error=%v", data, err)
	}
}

func TestInvalidManifestNeverStartsVirtbench(t *testing.T) {
	for _, tt := range []struct{ name, source, want string }{
		{"missing", "name: {{ .UNKNOWN }}", "map has no entry"},
		{"wrong name", "apiVersion: kubevirt.io/v1\nkind: VirtualMachine\nmetadata: {name: wrong}\nspec: {}", "metadata.name"},
		{"multiple", "kind: VirtualMachine\n---\nkind: ConfigMap", "one YAML document"},
		{"malformed", "kind: [", "invalid YAML"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "virtbench"), []byte("#!/bin/sh\necho started > invoked\n"), 0o700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", root)
			path := filepath.Join(root, "source.yaml")
			if err := os.WriteFile(path, []byte(tt.source), 0o600); err != nil {
				t.Fatal(err)
			}
			err := execVirtbench(t.Context(), &core.RunCtx{}, root, []string{"fio", "--storage-class", "fast", "--vm-template", path})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error=%v, want %s", err, tt.want)
			}
			if _, err := os.Stat(filepath.Join(root, "invoked")); !os.IsNotExist(err) {
				t.Fatalf("started virtbench on invalid manifest: %v", err)
			}
		})
	}
}

func TestAllOwnedTemplatesRender(t *testing.T) {
	values, err := resolveTemplateValues("fio", []string{"--storage-class", "fast", "--vm-name", defaultVMName})
	if err != nil {
		t.Fatal(err)
	}
	for key, value := range map[string]any{
		"NAME": "helper", "NAMESPACE": "test-ns", "TARGET_NODE": "worker-1",
		"NODE_NAME": "worker-1", "HOSTNAME_LABEL": "worker-1",
		"ANNOTATIONS": map[string]string{"run-id": "test"}, "AVOID_NODES": []string{"worker-2"},
		"PVC_NAME": "source", "SNAPSHOT_CLASS": "snapshots",
	} {
		values[key] = value
	}
	vm, err := renderVMTemplate("two-disk", []byte(twoDiskVMTemplate), values)
	if err != nil {
		t.Fatal(err)
	}
	docs, err := manifest.Documents(vm)
	if err != nil {
		t.Fatal(err)
	}
	values["VM"] = docs[0]
	values["SNAPSHOTS"] = map[string]cloneSource{"0": {Name: "existing", Namespace: "images"}}
	paths, err := filepath.Glob("templates/*/*.yaml")
	if err != nil || len(paths) == 0 {
		t.Fatalf("templates: %v %v", paths, err)
	}
	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			source, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := vmRenderer.Render(path, source, values); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestTemplateValuesRejectInvalidNumbersAndMultilineSettings(t *testing.T) {
	for _, args := range [][]string{
		{"--fio-iodepth", "0"}, {"--fio-numjobs", "-1"}, {"--fio-runtime", "bad"},
		{"--fio-size", "1G\nruntime=1"}, {"--vm-name", ""},
	} {
		if _, err := resolveTemplateValues("fio", args); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}

func TestConditionalDiskCountUsesSelectedBackend(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "conditional.yaml")
	source := `apiVersion: kubevirt.io/v1
kind: VirtualMachine
metadata:
  name: {{ .VM_NAME | quote }}
spec:
  template:
    spec:
      volumes:
        - name: root
          dataVolume: {name: root}
        {{ if eq .STORAGE_CLASS_NAME "backend" }}
        - name: extra
          dataVolume: {name: extra}
        {{ end }}
        - name: cloudinit
          cloudInitNoCloud: {userData: "#cloud-config"}
`
	if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	tr := core.TestRequirement{ID: "TR-VIRT-018", Params: map[string]any{"storage_class": "plan", "num_disks": 2, "vm_template": path}}
	rc := &core.RunCtx{Backend: &core.ResolvedBackend{StorageClass: "backend"}}
	t.Setenv("PATH", root)
	findings, err := (preflight{sc: Scenario{Validate: cloneAtScaleValidate}}).Check(t.Context(), rc, core.NewBag(), []core.TestRequirement{tr})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, finding := range findings {
		if strings.Contains(finding.Message, "scenario parameters") {
			found = true
			if finding.Level != "info" {
				t.Fatalf("preflight rendered with wrong values: %+v", finding)
			}
		}
	}
	if !found || tr.Params["storage_class"] != "plan" {
		t.Fatal("preflight did not validate or mutated plan inputs")
	}
	for _, sc := range []string{"backend", "plan"} {
		_, err := manifests.Prepare(t.Context(), manifest.Request{Directory: root, Args: []string{
			"datasource-clone", "--storage-class", sc, "--num-disks", "2", "--vm-template", path,
		}})
		if sc == "backend" && err != nil {
			t.Fatal(err)
		}
		if sc == "plan" && (err == nil || !strings.Contains(err.Error(), "defines 1 disks")) {
			t.Fatalf("expected rendered disk-count mismatch, got %v", err)
		}
	}
}
