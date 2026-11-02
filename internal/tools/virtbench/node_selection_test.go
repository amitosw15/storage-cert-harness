package virtbench

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/openshift-storage-cert/storage-cert-harness/internal/core"
)

func TestWorkerSelectionAndHelperAvoidanceUseNodeResponses(t *testing.T) {
	dir := t.TempDir()
	script := `#!/bin/sh
case "$1 $2" in
 'get nodes') echo '{"items":[{"metadata":{"name":"not-ready","labels":{"node-role.kubernetes.io/worker":"","kubevirt.io/schedulable":"true"}},"status":{"conditions":[{"type":"Ready","status":"False"}]}},{"metadata":{"name":"worker.example.test","labels":{"node-role.kubernetes.io/worker":"","kubevirt.io/schedulable":"true","kubernetes.io/hostname":"worker"}},"status":{"conditions":[{"type":"Ready","status":"True"}]}}]}' ;;
 'get node') echo '{"metadata":{"name":"worker.example.test","labels":{"kubernetes.io/hostname":"worker"}}}' ;;
 *) exit 1 ;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "kubectl"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	name, hostname, err := pickWorkerNode(t.Context(), "")
	if err != nil || name != "worker.example.test" || hostname != "worker" {
		t.Fatalf("selection = %q/%q, %v", name, hostname, err)
	}
	names, labels, err := sshHelperAvoidNodes(t.Context(), "", []core.TestRequirement{{Params: map[string]any{"node": name}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := names[name]; !ok {
		t.Fatalf("missing avoided node: %v", names)
	}
	if _, ok := labels[hostname]; !ok {
		t.Fatalf("missing avoided hostname: %v", labels)
	}
}
