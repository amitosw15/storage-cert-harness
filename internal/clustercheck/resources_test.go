package clustercheck

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func resourceCLI(t *testing.T, scenario string) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("PATH", dir)
	t.Setenv("RESOURCE_TEST_DIR", dir)
	t.Setenv("RESOURCE_TEST_CASE", scenario)
	t.Setenv("RESOURCE_TEST_JSON", `{"metadata":{"name":"existing","namespace":"images"},"provisioner":"driver","parameters":{"custom":"retained"},"driver":"driver","spec":{"storageClassName":"fast","volumeSnapshotClassName":"chosen","source":{"pvc":{"name":"disk","namespace":"images"}}},"status":{"phase":"Bound","readyToUse":true,"snapshotClass":"chosen","claimPropertySets":[{"volumeMode":"Filesystem","accessModes":["ReadWriteOnce"],"future":{"enabled":true}}],"conditions":[{"type":"Ready","status":"True"}]},"items":[{"metadata":{"name":"chosen"},"driver":"driver"}]}`)
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$RESOURCE_TEST_DIR/commands"
if [ "$1" = --kubeconfig ]; then shift 2; fi
case "$1" in
 get)
  case "$RESOURCE_TEST_CASE" in
   forbidden) echo 'Error from server (Forbidden): denied' >&2; exit 1 ;;
   malformed) echo '['; exit 0 ;;
   missing) exit 0 ;;
   ensure-new|ensure-race|ensure-create-failure|ensure-malformed)
    if [ ! -f "$RESOURCE_TEST_DIR/created" ]; then exit 0; fi ;;
  esac
  printf '%s' "$RESOURCE_TEST_JSON" ;;
 create)
  /bin/cat > "$RESOURCE_TEST_DIR/input"
  case "$RESOURCE_TEST_CASE" in
   exists|ensure-race)
    : > "$RESOURCE_TEST_DIR/created"
    echo 'Error from server (AlreadyExists): namespace already exists' >&2; exit 1 ;;
   ensure-create-failure) echo 'Error from server (Forbidden): denied' >&2; exit 1 ;;
  esac
  : > "$RESOURCE_TEST_DIR/created"
  if [ "$RESOURCE_TEST_CASE" = ensure-malformed ]; then echo '['; else printf '%s' "$RESOURCE_TEST_JSON"; fi ;;
 patch|wait|delete)
  if [ "$RESOURCE_TEST_CASE" = forbidden ]; then echo 'Error from server (Forbidden): denied' >&2; exit 1; fi ;;
 *) exit 1 ;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "oc"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func resourceCommands(t *testing.T, dir string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "commands"))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestResourceGetters(t *testing.T) {
	ctx := t.Context()
	ref := NamespacedName{Name: "disk", Namespace: "images"}
	for _, tc := range []struct {
		name, args, want string
		get              func() (string, error)
	}{
		{"class", "storageclass fast", "driver", func() (string, error) {
			v, e := GetStorageClass(ctx, "/cluster", "fast")
			value, _ := v["provisioner"].(string)
			return value, e
		}},
		{"profile", "storageprofile fast", "chosen", func() (string, error) {
			v, e := GetStorageProfile(ctx, "/cluster", "fast")
			return v.Status.SnapshotClass, e
		}},
		{"snapshot class", "volumesnapshotclass chosen", "driver", func() (string, error) { v, e := GetVolumeSnapshotClass(ctx, "/cluster", "chosen"); return v.Driver, e }},
		{"pvc", "pvc disk -n images", "Bound", func() (string, error) { v, e := GetPVC(ctx, "/cluster", ref); return v.Status.Phase, e }},
		{"snapshot", "volumesnapshot disk -n images", "chosen", func() (string, error) {
			v, e := GetVolumeSnapshot(ctx, "/cluster", ref)
			return v.Spec.VolumeSnapshotClassName, e
		}},
		{"datasource", "datasource disk -n images", "disk", func() (string, error) {
			v, e := GetDataSource(ctx, "/cluster", ref)
			return v.Spec.Source["pvc"].Name, e
		}},
		{"namespace", "namespace images", "existing", func() (string, error) { v, e := GetNamespace(ctx, "/cluster", "images"); return v.Name, e }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := resourceCLI(t, "present")
			got, err := tc.get()
			if err != nil || got != tc.want {
				t.Fatalf("got %q, error %v; want %q", got, err, tc.want)
			}
			if got := resourceCommands(t, dir); got != "--kubeconfig /cluster get "+tc.args+" -o json --ignore-not-found\n" {
				t.Fatalf("unexpected command: %s", got)
			}
		})
	}
}

func TestGetFailureDoesNotCreate(t *testing.T) {
	for _, scenario := range []string{"missing", "forbidden", "malformed"} {
		t.Run(scenario, func(t *testing.T) {
			dir := resourceCLI(t, scenario)
			_, err := GetStorageProfile(t.Context(), "", "profile")
			if err == nil || errors.Is(err, ErrNotFound) != (scenario == "missing") {
				t.Fatalf("incorrect missing/failed-read classification: %v", err)
			}
			commands := resourceCommands(t, dir)
			if strings.Contains(commands, "create") || strings.Contains(commands, "patch") {
				t.Fatalf("get mutated the cluster: %s", commands)
			}
		})
	}
}

func TestStrictCreatesDoNotAdoptExistingResources(t *testing.T) {
	for _, kind := range []string{"Namespace", "StorageClass", "VolumeSnapshot"} {
		t.Run(kind, func(t *testing.T) {
			dir := resourceCLI(t, "exists")
			data := []byte(`{"kind":"` + kind + `","metadata":{"name":"existing","namespace":"images"}}`)
			var err error
			switch kind {
			case "Namespace":
				_, err = CreateNamespace(t.Context(), "", data)
			case "StorageClass":
				_, err = CreateStorageClass(t.Context(), "", data)
			case "VolumeSnapshot":
				_, err = CreateVolumeSnapshot(t.Context(), "", data)
			}
			if !errors.Is(err, ErrAlreadyExists) {
				t.Fatalf("strict create did not report AlreadyExists: %v", err)
			}
			if got := resourceCommands(t, dir); got != "create -f - -o json\n" {
				t.Fatalf("create performed a lookup or overwrite: %s", got)
			}
			input, readErr := os.ReadFile(filepath.Join(dir, "input"))
			if readErr != nil || string(input) != string(data) {
				t.Fatal("create changed the rendered source")
			}
		})
	}
}

func TestEnsureNamespaceLifecycle(t *testing.T) {
	for _, tc := range []struct {
		scenario string
		created  bool
		creates  int
		wantErr  bool
	}{
		{"present", false, 0, false},
		{"ensure-new", true, 1, false},
		{"ensure-race", false, 1, false},
		{"forbidden", false, 0, true},
		{"ensure-create-failure", false, 1, true},
		{"ensure-malformed", true, 1, true},
	} {
		t.Run(tc.scenario, func(t *testing.T) {
			dir := resourceCLI(t, tc.scenario)
			created, err := EnsureNamespace(t.Context(), "/cluster", "existing", []byte("kind: Namespace\nmetadata:\n  name: existing\n"))
			if created != tc.created || (err != nil) != tc.wantErr {
				t.Fatalf("created=%v, err=%v", created, err)
			}
			commands := resourceCommands(t, dir)
			if strings.Count(commands, " create ") != tc.creates || strings.Contains(commands, "apply") || strings.Contains(commands, "patch") {
				t.Fatalf("unexpected ensure operations: %s", commands)
			}
			if tc.scenario == "ensure-race" && strings.Count(commands, " get namespace ") != 2 {
				t.Fatalf("concurrent creation was not re-read: %s", commands)
			}
		})
	}
}

func TestCleanupDeletesAndExplicitWaits(t *testing.T) {
	dir := resourceCLI(t, "missing")
	ref := NamespacedName{Name: "snapshot", Namespace: "images"}
	if err := DeleteStorageClass(t.Context(), "/cluster", "private"); err != nil {
		t.Fatal(err)
	}
	if err := DeleteVolumeSnapshot(t.Context(), "/cluster", ref); err != nil {
		t.Fatal(err)
	}
	if err := WaitForStorageProfileCreated(t.Context(), "/cluster", "private", 90*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := WaitForVolumeSnapshotReady(t.Context(), "/cluster", ref, 5*time.Minute); err != nil {
		t.Fatal(err)
	}
	commands := resourceCommands(t, dir)
	for _, want := range []string{
		"delete storageclass private --ignore-not-found --wait=false",
		"delete volumesnapshot snapshot -n images --ignore-not-found --wait=false",
		"wait --for=create storageprofile/private --timeout=1m30s",
		"wait volumesnapshot/snapshot -n images --for=jsonpath={.status.readyToUse}=true --timeout=5m0s",
	} {
		if !strings.Contains(commands, "--kubeconfig /cluster "+want+"\n") {
			t.Fatalf("missing command %q: %s", want, commands)
		}
	}
	t.Setenv("RESOURCE_TEST_CASE", "forbidden")
	if err := DeleteStorageClass(t.Context(), "", "private"); err == nil {
		t.Fatal("delete swallowed a permission error")
	}
}

func TestProfileCopyPreservesUnknownProperties(t *testing.T) {
	resourceCLI(t, "present")
	profile, err := GetStorageProfile(t.Context(), "", "profile")
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(profile.Status.ClaimPropertySets)
	if err != nil || !strings.Contains(string(data), `"future":{"enabled":true}`) {
		t.Fatalf("unknown profile properties were lost: %s, %v", data, err)
	}
}

func TestInvalidResourceArgumentsDoNotInvokeCLI(t *testing.T) {
	dir := resourceCLI(t, "present")
	_, getErr := GetPVC(t.Context(), "", NamespacedName{Name: "disk"})
	deleteErr := DeleteStorageClass(t.Context(), "", "")
	flagErr := DeleteStorageClass(t.Context(), "", "--all")
	_, scopeErr := CreateVolumeSnapshot(t.Context(), "", []byte("kind: VolumeSnapshot\nmetadata:\n  name: snap\n"))
	_, createErr := CreateStorageClass(t.Context(), "", []byte("kind: Pod\n"))
	_, multiErr := CreateStorageClass(t.Context(), "", []byte("kind: StorageClass\n---\nkind: Pod\n"))
	if getErr == nil || deleteErr == nil || createErr == nil || flagErr == nil || scopeErr == nil || multiErr == nil {
		t.Fatal("invalid resource requests were accepted")
	}
	if _, err := os.Stat(filepath.Join(dir, "commands")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("invalid resource request invoked the CLI")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := GetNamespace(ctx, "", "ns"); !errors.Is(err, context.Canceled) {
		t.Fatalf("lost cancellation: %v", err)
	}
}
