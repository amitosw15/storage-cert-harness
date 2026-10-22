package clustercheck

import (
	"context"
	"encoding/json"
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

func TestResolveSnapshotRequiresStorageClass(t *testing.T) {
	dir := snapshotCLI(t)
	selection, err := ResolveSnapshot(context.Background(), "", "nondefault", "", true)
	if err == nil || !strings.Contains(err.Error(), "storage_class is required") {
		t.Fatalf("selection = %+v, error = %v, want missing storage class", selection, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "commands")); !os.IsNotExist(err) {
		t.Fatalf("cluster commands ran without a storage class: %v", err)
	}
}

func TestResolveSnapshotRequiresClusterCLI(t *testing.T) {
	snapshotCLI(t)
	t.Setenv("PATH", t.TempDir())
	_, err := ResolveSnapshot(context.Background(), "backend", "", "", false)
	if err == nil || !strings.Contains(err.Error(), "neither kubectl nor oc found") {
		t.Fatalf("error = %v, want missing cluster CLI", err)
	}
}

func TestResolveSnapshotVMProfileFallback(t *testing.T) {
	dir := snapshotCLI(t)
	t.Setenv("TEST_PROFILE", "")
	selection, err := ResolveSnapshot(context.Background(), "backend", "", "", true)
	if err != nil || selection.SnapshotClass != "default" {
		t.Fatalf("selection = %+v, error = %v, want default class", selection, err)
	}
	commands, err := os.ReadFile(filepath.Join(dir, "commands"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(commands), "get storageprofile backend") || !strings.Contains(string(commands), "get volumesnapshotclass -o json") {
		t.Fatalf("VM fallback did not inspect profile and class list: %s", commands)
	}
}

func TestResolveSnapshotRejectsMalformedVMProfile(t *testing.T) {
	dir := snapshotCLI(t)
	t.Setenv("TEST_PROFILE", "{")
	_, err := ResolveSnapshot(context.Background(), "backend", "", "", true)
	if err == nil || !strings.Contains(err.Error(), "decode storage profile") {
		t.Fatalf("error = %v, want malformed profile error", err)
	}
	commands, err := os.ReadFile(filepath.Join(dir, "commands"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(commands), "get volumesnapshotclass") {
		t.Fatalf("continued after malformed profile: %s", commands)
	}
}

func TestResolveSnapshotDirectIgnoresVMProfile(t *testing.T) {
	dir := snapshotCLI(t)
	t.Setenv("TEST_PROFILE", "{")
	selection, err := ResolveSnapshot(context.Background(), "backend", "", "", false)
	if err != nil || selection.SnapshotClass != "default" {
		t.Fatalf("selection = %+v, error = %v, want default class", selection, err)
	}
	commands, err := os.ReadFile(filepath.Join(dir, "commands"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(commands), "get storageprofile") {
		t.Fatalf("direct snapshot consulted VM profile: %s", commands)
	}
}

func TestResolveSnapshotRejectsMalformedClassList(t *testing.T) {
	snapshotCLI(t)
	t.Setenv("TEST_CLASSES", "{")
	_, err := ResolveSnapshot(context.Background(), "backend", "", "", false)
	if err == nil || !strings.Contains(err.Error(), "decode get volumesnapshotclass") {
		t.Fatalf("error = %v, want malformed class list error", err)
	}
}

func TestPrepareVMStorageIsIsolated(t *testing.T) {
	dir := snapshotCLI(t)
	s := SnapshotSelection{StorageClass: "backend", SnapshotClass: "nondefault", Kubeconfig: "/test/kubeconfig"}
	created, err := s.PrepareVMStorage(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "created.json"))
	if err != nil {
		t.Fatal(err)
	}
	var sc map[string]any
	if err := json.Unmarshal(data, &sc); err != nil {
		t.Fatal(err)
	}
	meta := sc["metadata"].(map[string]any)
	if meta["annotations"] != nil || meta["name"] != nil || meta["generateName"] != "storage-cert-snapshot-" {
		t.Fatalf("metadata = %+v", meta)
	}
	if sc["provisioner"] != "driver" || sc["parameters"].(map[string]any)["pool"] != "pool-a" || sc["allowVolumeExpansion"] != true {
		t.Fatalf("backend configuration lost: %+v", sc)
	}
	data, err = os.ReadFile(filepath.Join(dir, "patch.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"snapshotClass":"nondefault"`) || !strings.Contains(string(data), `"volumeMode":"Block"`) {
		t.Fatalf("profile patch = %s", data)
	}
	if err := s.CleanupVMStorage(context.Background(), created); err != nil {
		t.Fatal(err)
	}
	commands, _ := os.ReadFile(filepath.Join(dir, "commands"))
	if strings.Contains(string(commands), "patch storageprofile backend") || !strings.Contains(string(commands), "delete storageclass storage-cert-snapshot-test") {
		t.Fatalf("unexpected mutations: %s", commands)
	}
}

func TestPrepareVMStorageReturnsCleanupNameOnFailure(t *testing.T) {
	snapshotCLI(t)
	t.Setenv("TEST_WAIT_FAIL", "true")
	s := SnapshotSelection{StorageClass: "backend", SnapshotClass: "nondefault"}
	created, err := s.PrepareVMStorage(context.Background())
	if err == nil || created != "storage-cert-snapshot-test" {
		t.Fatalf("created=%q, error=%v", created, err)
	}
}
