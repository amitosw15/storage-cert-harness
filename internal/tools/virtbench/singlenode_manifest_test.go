package virtbench

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/openshift-storage-cert/storage-cert-harness/internal/core"
	"github.com/openshift-storage-cert/storage-cert-harness/internal/manifest"
)

func TestSingleNodeRendererSupportsVirtbenchSelector(t *testing.T) {
	source := `apiVersion: kubevirt.io/v1
kind: VirtualMachine
metadata: {name: custom, annotations: {keep: value}}
spec:
  runStrategy: Always
  template:
    metadata: {labels: {custom: retained}}
    spec:
      domain: {resources: {requests: {memory: 2Gi}}}
      networks: [{name: default, pod: {}}]
      volumes: [{name: root, dataVolume: {name: root}}]
  dataVolumeTemplates:
    - metadata: {name: root}
      spec:
        sourceRef: {kind: DataSource, name: original, namespace: images}
        storage: {storageClassName: fast, volumeMode: Filesystem, accessModes: [ReadWriteMany]}
`
	for _, selection := range []string{"", "images/chosen"} {
		t.Run(selection, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "custom.yaml")
			if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
				t.Fatal(err)
			}
			tr := core.TestRequirement{ID: "TR-VIRT-013", Params: map[string]any{"datasource": selection}}
			args, err := prepareManifest(t.Context(), manifestRequest{Directory: root, TR: tr, Args: []string{"datasource-clone", "--storage-class", "fast", "--vm-name", "custom", "--vm-template", path}})
			if err != nil {
				t.Fatal(err)
			}
			path, _ = argument(args, "--vm-template")
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			before, err := manifest.Documents(data)
			if err != nil {
				t.Fatal(err)
			}
			expected, err := manifest.Documents([]byte(source))
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, expected) {
				t.Fatalf("rendering changed custom VM fields: %s", data)
			}
			lines := strings.Split(string(data), "\n")
			inTemplate := false
			inserted := false
			for i, line := range lines {
				if strings.TrimSpace(line) == "template:" {
					inTemplate = true
				} else if inTemplate && strings.TrimSpace(line) == "spec:" {
					indent := len(line) - len(strings.TrimLeft(line, " "))
					lines[i] += "\n" + strings.Repeat(" ", indent+2) + "nodeSelector:\n" + strings.Repeat(" ", indent+4) + "kubernetes.io/hostname: worker-a"
					inserted = true
					break
				}
			}
			if !inserted {
				t.Fatal("template.spec not found")
			}
			after, err := manifest.Documents([]byte(strings.Join(lines, "\n")))
			if err != nil {
				t.Fatal(err)
			}
			spec := after[0]["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)
			if !reflect.DeepEqual(spec["nodeSelector"], map[string]any{"kubernetes.io/hostname": "worker-a"}) {
				t.Fatalf("selector includes VM fields: %#v", spec["nodeSelector"])
			}
			delete(spec, "nodeSelector")
			if !reflect.DeepEqual(before, after) {
				t.Fatalf("VM fields changed after selector insertion: %s", strings.Join(lines, "\n"))
			}
		})
	}
}

func TestPlanTemplateValuesIncludeSeparateDataSourceFields(t *testing.T) {
	tr := core.TestRequirement{Params: map[string]any{"datasource_name": "custom-image", "datasource_namespace": "my-images"}}
	values, err := resolveTemplateValues("datasource-clone", planTemplateArgs("datasource-clone", tr))
	if err != nil {
		t.Fatal(err)
	}
	if values["DATASOURCE_NAME"] != "custom-image" || values["DATASOURCE_NAMESPACE"] != "my-images" {
		t.Fatalf("unexpected values: %#v", values)
	}
}
