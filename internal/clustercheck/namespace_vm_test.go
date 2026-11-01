package clustercheck

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestListAndDeleteNamespaces(t *testing.T) {
	dir := resourceCLI(t, "present")
	items, err := ListNamespaces(t.Context(), "")
	if err != nil || len(items) != 1 || items[0].Name != "chosen" {
		t.Fatalf("ListNamespaces() = %#v, %v", items, err)
	}
	if err := DeleteNamespace(t.Context(), "", "run-ns"); err != nil {
		t.Fatal(err)
	}
	commands := resourceCommands(t, dir)
	if !strings.Contains(commands, "get namespace -o json") || !strings.Contains(commands, "delete namespace run-ns --ignore-not-found --wait=false") {
		t.Fatalf("unexpected namespace commands: %s", commands)
	}
	if err := DeleteNamespace(t.Context(), "", "-all"); err == nil {
		t.Fatal("DeleteNamespace accepted a flag-like name")
	}
}

func TestApplyVirtualMachineValidatesSingleNamespacedObject(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", dir)
	t.Setenv("VM_TEST_DIR", dir)
	script := "#!/bin/sh\n/bin/cat > \"$VM_TEST_DIR/input\"\nprintf '%s\\n' \"$*\" > \"$VM_TEST_DIR/args\"\n"
	if err := os.WriteFile(filepath.Join(dir, "oc"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := []byte("apiVersion: kubevirt.io/v1\nkind: VirtualMachine\nmetadata:\n  name: guest\n  namespace: workloads\n")
	if err := ApplyVirtualMachine(t.Context(), "", manifest); err != nil {
		t.Fatal(err)
	}
	input, err := os.ReadFile(filepath.Join(dir, "input"))
	if err != nil || string(input) != string(manifest) {
		t.Fatalf("apply input = %q, %v", input, err)
	}
	args, err := os.ReadFile(filepath.Join(dir, "args"))
	if err != nil || string(args) != "apply -f -\n" {
		t.Fatalf("apply args = %q, %v", args, err)
	}
	for _, invalid := range []string{
		"kind: Pod\nmetadata:\n  name: guest\n  namespace: workloads\n",
		"kind: VirtualMachine\nmetadata:\n  name: guest\n",
		"kind: VirtualMachine\nmetadata:\n  name: guest\n  namespace: workloads\n---\nkind: Pod\n",
	} {
		if err := ApplyVirtualMachine(t.Context(), "", []byte(invalid)); err == nil {
			t.Fatalf("ApplyVirtualMachine accepted invalid manifest %q", invalid)
		}
	}
}
