package clustercheck

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func snapshotCLI(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("SNAPSHOT_TEST_DIR", dir)
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$SNAPSHOT_TEST_DIR/commands"
if [ "$1" = --kubeconfig ]; then shift 2; fi
case "$1 $2" in
  'get storageclass') echo 'Warning: a harmless API warning' >&2; echo '{"apiVersion":"storage.k8s.io/v1","kind":"StorageClass","metadata":{"name":"backend","annotations":{"storageclass.kubernetes.io/is-default-class":"true","storageclass.kubevirt.io/is-default-virt-class":"true"}},"provisioner":"driver","parameters":{"pool":"pool-a"},"allowVolumeExpansion":true,"volumeBindingMode":"Immediate"}' ;;
  'get storageprofile') printf '%s' "$TEST_PROFILE" ;;
  'get volumesnapshotclass')
    case "$3" in
      missing) echo 'NotFound' >&2; exit 1 ;;
      wrong) echo '{"driver":"other"}' ;;
      -o) printf '%s' "$TEST_CLASSES" ;;
      *) echo '{"driver":"driver"}' ;;
    esac ;;
  'create -f') /bin/cat > "$SNAPSHOT_TEST_DIR/created.json"; echo '{"metadata":{"name":"storage-cert-snapshot-test"}}' ;;
  'patch storageprofile') printf '%s' "$6" > "$SNAPSHOT_TEST_DIR/patch.json" ;;
  'wait --for=create') if [ "$TEST_WAIT_FAIL" = true ]; then exit 1; fi ;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "oc"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("TEST_PROFILE", `{"status":{"snapshotClass":"profile-class","claimPropertySets":[{"volumeMode":"Block","accessModes":["ReadWriteMany"]}]}}`)
	t.Setenv("TEST_CLASSES", `{"items":[{"metadata":{"name":"default","annotations":{"snapshot.storage.kubernetes.io/is-default-class":"true"}},"driver":"driver"},{"metadata":{"name":"nondefault"},"driver":"driver"}]}`)
	return dir
}

func TestResolveSnapshot(t *testing.T) {
	for _, tt := range []struct {
		name, configured string
		vm               bool
		want, wantErr    string
	}{
		{name: "explicit non-default", configured: "nondefault", want: "nondefault"},
		{name: "explicit overrides profile", configured: "nondefault", vm: true, want: "nondefault"},
		{name: "missing", configured: "missing", wantErr: `snapshot class "missing" lookup failed`},
		{name: "driver mismatch", configured: "wrong", wantErr: "does not match"},
		{name: "direct default", want: "default"},
		{name: "VM profile", vm: true, want: "profile-class"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := snapshotCLI(t)
			s, err := ResolveSnapshot(context.Background(), "backend", tt.configured, "/test/kubeconfig", tt.vm)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want %s", err, tt.wantErr)
				}
			} else if err != nil || s.SnapshotClass != tt.want {
				t.Fatalf("selection = %+v, error = %v", s, err)
			}
			commands, err := os.ReadFile(filepath.Join(dir, "commands"))
			if err != nil {
				t.Fatal(err)
			}
			for line := range strings.Lines(string(commands)) {
				if !strings.HasPrefix(line, "--kubeconfig /test/kubeconfig get ") {
					t.Fatalf("preflight mutated cluster or used wrong context: %s", line)
				}
			}
		})
	}
}

func TestResolveSnapshotAmbiguity(t *testing.T) {
	for _, tt := range []struct {
		name, classes string
		vm            bool
		want          string
	}{
		{"no match", `{"items":[]}`, false, ""},
		{"single VM class", `{"items":[{"metadata":{"name":"only"},"driver":"driver"}]}`, true, "only"},
		{"single direct nondefault", `{"items":[{"metadata":{"name":"only"},"driver":"driver"}]}`, false, ""},
		{"two defaults", `{"items":[{"metadata":{"name":"a","annotations":{"snapshot.storage.kubernetes.io/is-default-class":"true"}},"driver":"driver"},{"metadata":{"name":"b","annotations":{"snapshot.storage.kubernetes.io/is-default-class":"true"}},"driver":"driver"}]}`, true, ""},
		{"unrelated default", `{"items":[{"metadata":{"name":"other","annotations":{"snapshot.storage.kubernetes.io/is-default-class":"true"}},"driver":"other"}]}`, false, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			snapshotCLI(t)
			t.Setenv("TEST_PROFILE", "")
			t.Setenv("TEST_CLASSES", tt.classes)
			s, err := ResolveSnapshot(context.Background(), "backend", "", "", tt.vm)
			if tt.want == "" {
				if err == nil {
					t.Fatalf("expected ambiguous/missing class error: %+v", s)
				}
			} else if err != nil || s.SnapshotClass != tt.want {
				t.Fatalf("selection = %+v, error = %v", s, err)
			}
		})
	}
}
