package virtbench

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/openshift-storage-cert/storage-cert-harness/internal/core"
)

// TestCloneAtScaleArgsDefaultsToEmbeddedTemplate confirms the fix's default
// path: no vm_template/num_disks set, and the embedded 2-disk template is
// staged with --num-disks 2.
func TestCloneAtScaleArgsDefaultsToEmbeddedTemplate(t *testing.T) {
	root := t.TempDir()
	tr := core.TestRequirement{ID: "TR-VIRT-018", Params: map[string]any{
		"storage_class":    "ocs-storagecluster-ceph-rbd",
		"iteration_clones": 5,
	}}
	args, err := cloneAtScaleArgs("virtbench")(&core.RunCtx{}, tr, root)
	if err != nil {
		t.Fatalf("cloneAtScaleArgs: %v", err)
	}

	staged, disks := findTemplateAndDisks(t, args)
	if disks != "2" {
		t.Errorf("--num-disks = %q, want 2", disks)
	}
	if filepath.Dir(staged) != root || filepath.Base(staged) != twoDiskVMTemplateFile {
		t.Errorf("staged template = %q, want the embedded default inside %q", staged, root)
	}
}

// TestCloneAtScaleArgsUsesPlanTemplate confirms a plan can swap in its own
// vm_template (e.g. more than 2 disks), matched with num_disks.
func TestCloneAtScaleArgsUsesPlanTemplate(t *testing.T) {
	root := t.TempDir()
	tmpl := writeTemplate(t, t.TempDir(), 3)
	tr := core.TestRequirement{ID: "TR-VIRT-018", Params: map[string]any{
		"storage_class": "sc",
		"vm_template":   tmpl,
		"num_disks":     3,
	}}
	args, err := cloneAtScaleArgs("virtbench")(&core.RunCtx{}, tr, root)
	if err != nil {
		t.Fatalf("cloneAtScaleArgs: %v", err)
	}
	staged, disks := findTemplateAndDisks(t, args)
	if disks != "3" {
		t.Errorf("--num-disks = %q, want 3", disks)
	}
	if filepath.Dir(staged) != root {
		t.Errorf("template staged at %q, want inside results root %q", staged, root)
	}
	if _, err := os.Stat(staged); err != nil {
		t.Errorf("staged template not written: %v", err)
	}
}

func findTemplateAndDisks(t *testing.T, args []string) (template, disks string) {
	t.Helper()
	for i, a := range args {
		if a == "--vm-template" && i+1 < len(args) {
			template = args[i+1]
		}
		if a == "--num-disks" && i+1 < len(args) {
			disks = args[i+1]
		}
	}
	if template == "" {
		t.Fatalf("--vm-template not passed; args = %v", args)
	}
	return template, disks
}

// TestCloneAtScaleArgsStagedTemplateHasTwoDataVolumes pins down the actual bug
// fix: unlike virtbench's --num-disks flag (which only labels output), the
// built-in default template really does define two DataVolumes.
func TestCloneAtScaleArgsStagedTemplateHasTwoDataVolumes(t *testing.T) {
	root := t.TempDir()
	tr := core.TestRequirement{ID: "TR-VIRT-018", Params: map[string]any{"storage_class": "sc"}}
	args, err := cloneAtScaleArgs("virtbench")(&core.RunCtx{}, tr, root)
	if err != nil {
		t.Fatalf("cloneAtScaleArgs: %v", err)
	}
	args, err = prepareManifest(t.Context(), manifestRequest{Directory: root, Args: args})
	if err != nil {
		t.Fatal(err)
	}
	staged, _ := findTemplateAndDisks(t, args)
	data, err := os.ReadFile(staged)
	if err != nil {
		t.Fatalf("read staged template: %v", err)
	}
	if strings.Contains(string(data), "{{") {
		t.Fatal("unresolved template")
	}

	var doc struct {
		Spec struct {
			DataVolumeTemplates []map[string]any `yaml:"dataVolumeTemplates"`
		} `yaml:"spec"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatalf("parse staged template: %v", err)
	}
	if got := len(doc.Spec.DataVolumeTemplates); got != 2 {
		t.Errorf("staged template has %d dataVolumeTemplates, want 2", got)
	}
}

// TestCloneAtScaleValidate covers the num_disks / vm_template consistency
// rules, reusing TR-VIRT-013's templateDiskCount (singlenode.go) the same way
// its own singleNodeCeilingValidate does.
func TestCloneAtScaleValidate(t *testing.T) {
	dir := t.TempDir()
	tmpl3 := writeTemplate(t, dir, 3)

	tests := []struct {
		name    string
		params  map[string]any
		wantErr bool
	}{
		{"default (embedded 2-disk template, no override)", map[string]any{}, false},
		{"num_disks matches default template", map[string]any{"num_disks": 2}, false},
		{"num_disks without a template that supports it", map[string]any{"num_disks": 3}, true},
		{"custom template matches num_disks", map[string]any{"num_disks": 3, "vm_template": tmpl3}, false},
		{"custom template disagrees with num_disks", map[string]any{"num_disks": 2, "vm_template": tmpl3}, true},
		{"num_disks below one", map[string]any{"num_disks": 0}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := cloneAtScaleValidate(core.TestRequirement{ID: "TR-VIRT-018", Params: tc.params})
			if tc.wantErr && err == nil {
				t.Error("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}
