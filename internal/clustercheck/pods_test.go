package clustercheck

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCreatePodIsStrictAndRequiresNamespace(t *testing.T) {
	manifest := []byte("apiVersion: v1\nkind: Pod\nmetadata:\n  name: helper\n  namespace: tools\n")
	dir := resourceCLI(t, "present")
	pod, err := CreatePod(t.Context(), "", manifest)
	if err != nil || pod.Metadata.Name != "existing" {
		t.Fatalf("CreatePod() = %#v, %v", pod, err)
	}
	if got := resourceCommands(t, dir); !strings.Contains(got, "create -f - -o json") {
		t.Fatalf("expected strict create, got %q", got)
	}
	if _, err := CreatePod(t.Context(), "", []byte("kind: Pod\nmetadata:\n  name: helper\n")); err == nil {
		t.Fatal("CreatePod accepted a manifest without an explicit namespace")
	}
	resourceCLI(t, "exists")
	if _, err := CreatePod(t.Context(), "", manifest); !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("CreatePod collision error = %v; want ErrAlreadyExists", err)
	}
}

func TestGetPodDecodesResourceState(t *testing.T) {
	resourceCLI(t, "present")
	got, err := GetPod(t.Context(), "", NamespacedName{Name: "existing", Namespace: "images"})
	if err != nil || got.Metadata.Name != "existing" || got.Status.Phase != "Bound" {
		t.Fatalf("GetPod() = %#v, %v", got, err)
	}
}

func TestPodDeleteOptionsUIDPrecondition(t *testing.T) {
	uid := "uid-\"\\slash\nend"
	path, err := writePodDeleteOptions(uid)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Remove(path) }()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var value struct {
		APIVersion    string `json:"apiVersion"`
		Kind          string `json:"kind"`
		Preconditions struct {
			UID string `json:"uid"`
		} `json:"preconditions"`
	}
	if err := json.Unmarshal(data, &value); err != nil {
		t.Fatal(err)
	}
	if value.APIVersion != "v1" || value.Kind != "DeleteOptions" || value.Preconditions.UID != uid {
		t.Fatalf("unexpected delete options: %s", data)
	}
}

func TestDeletePodWithUIDPreservesPreconditionFailure(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", dir)
	t.Setenv("POD_TEST_DIR", dir)
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" > \"$POD_TEST_DIR/args\"\nprintf '%s' \"$*\" | /usr/bin/grep -q -- '--raw' || exit 2\necho 'the UID in the precondition does not match' >&2\nexit 1\n"
	if err := os.WriteFile(filepath.Join(dir, "oc"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	err := DeletePodWithUID(t.Context(), "", NamespacedName{Name: "helper", Namespace: "tools"}, "uid-old")
	if !errors.Is(err, ErrUIDPreconditionFailed) {
		t.Fatalf("DeletePodWithUID() error = %v; want UID precondition error", err)
	}
	args, err := os.ReadFile(filepath.Join(dir, "args"))
	if err != nil || !strings.Contains(string(args), "/api/v1/namespaces/tools/pods/helper") || !strings.Contains(string(args), "-f ") {
		t.Fatalf("raw UID-guarded delete args = %q, %v", args, err)
	}
}

func TestPodOperationsRequireNamespacedReference(t *testing.T) {
	ref := NamespacedName{Name: "helper"}
	if _, err := GetPod(t.Context(), "", ref); err == nil {
		t.Fatal("GetPod accepted missing namespace")
	}
	if err := DeletePod(t.Context(), "", ref); err == nil {
		t.Fatal("DeletePod accepted missing namespace")
	}
	if _, err := ExecPod(t.Context(), "", ref, "true"); err == nil {
		t.Fatal("ExecPod accepted missing namespace")
	}
}
