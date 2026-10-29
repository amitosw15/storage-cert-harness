package kubeburner

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/openshift-storage-cert/storage-cert-harness/internal/clustercheck"
	"github.com/openshift-storage-cert/storage-cert-harness/internal/manifest"
)

func storageCLI(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("SNAPSHOT_TEST_DIR", dir)
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$SNAPSHOT_TEST_DIR/commands"
if [ "$1" = --kubeconfig ]; then shift 2; fi
case "$1 $2" in
 'get storageclass') printf '%s' "$TEST_STORAGE_CLASS" ;;
 'get storageprofile') printf '%s' "$TEST_PROFILE" ;;
 'create -f') /bin/cat > "$SNAPSHOT_TEST_DIR/created.json"; echo '{"metadata":{"name":"storage-cert-snapshot-test"}}' ;;
 'patch storageprofile') printf '%s' "$6" > "$SNAPSHOT_TEST_DIR/patch.json" ;;
 'wait --for=create') if [ "$TEST_WAIT_FAIL" = true ]; then exit 1; fi ;;
 'wait storageprofile/storage-cert-snapshot-test'|'delete storageclass') ;;
 *) exit 1 ;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "oc"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("TEST_STORAGE_CLASS", `{"apiVersion":"storage.k8s.io/v1","kind":"StorageClass","metadata":{"name":"backend","annotations":{"storageclass.kubernetes.io/is-default-class":"true","storageclass.kubevirt.io/is-default-virt-class":"true"}},"provisioner":"driver","parameters":{"pool":"pool-a"},"allowVolumeExpansion":true,"volumeBindingMode":"Immediate"}`)
	t.Setenv("TEST_PROFILE", `{"status":{"claimPropertySets":[{"volumeMode":"Block","accessModes":["ReadWriteMany"]}]}}`)
	return dir
}

func TestPrepareVMStorageIsIsolated(t *testing.T) {
	dir := storageCLI(t)
	s := clustercheck.SnapshotSelection{StorageClass: "backend", SnapshotClass: "nondefault", Kubeconfig: "/test/kubeconfig"}
	created, err := prepareVMStorage(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "created.json"))
	if err != nil {
		t.Fatal(err)
	}
	docs, err := manifest.Documents(data)
	if err != nil {
		t.Fatal(err)
	}
	sc := docs[0]
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
	var patch struct {
		Spec struct {
			SnapshotClass     string `json:"snapshotClass"`
			ClaimPropertySets []struct {
				VolumeMode string `json:"volumeMode"`
			} `json:"claimPropertySets"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(data, &patch); err != nil {
		t.Fatal(err)
	}
	if patch.Spec.SnapshotClass != "nondefault" || len(patch.Spec.ClaimPropertySets) != 1 || patch.Spec.ClaimPropertySets[0].VolumeMode != "Block" {
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
	storageCLI(t)
	t.Setenv("TEST_WAIT_FAIL", "true")
	s := clustercheck.SnapshotSelection{StorageClass: "backend", SnapshotClass: "nondefault"}
	created, err := prepareVMStorage(context.Background(), s)
	if err == nil || created != "storage-cert-snapshot-test" {
		t.Fatalf("created=%q, error=%v", created, err)
	}
}

func TestPrepareVMStoragePreservesProvisioningFields(t *testing.T) {
	dir := storageCLI(t)
	source := `{"apiVersion":"storage.k8s.io/v1","kind":"StorageClass","metadata":{"name":"backend","uid":"original","resourceVersion":"7","annotations":{"storageclass.kubernetes.io/is-default-class":"true"}},"status":{"ignored":true},"provisioner":"driver","parameters":{"pool":"pool: a","option":"true"},"reclaimPolicy":"Retain","mountOptions":["nfsvers=4.1"],"allowVolumeExpansion":false,"volumeBindingMode":"WaitForFirstConsumer","allowedTopologies":[{"matchLabelExpressions":[{"key":"topology.example/zone","values":["a","b"]}]}],"futureField":{"enabled":true,"limit":3}}`
	t.Setenv("TEST_STORAGE_CLASS", source)
	s := clustercheck.SnapshotSelection{StorageClass: "backend", SnapshotClass: "chosen"}
	if _, err := prepareVMStorage(t.Context(), s); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "created.json"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := manifest.Documents(data)
	if err != nil {
		t.Fatal(err)
	}
	expected, err := manifest.Documents([]byte(source))
	if err != nil {
		t.Fatal(err)
	}
	delete(expected[0], "status")
	expected[0]["metadata"] = map[string]any{"generateName": "storage-cert-snapshot-", "labels": map[string]any{"app.kubernetes.io/managed-by": "storage-cert-harness"}}
	if !reflect.DeepEqual(got, expected) {
		t.Fatalf("provisioning fields changed: %s", data)
	}
	var input map[string]any
	if err := json.Unmarshal([]byte(source), &input); err != nil {
		t.Fatal(err)
	}
	if _, err := manifest.Render(manifest.GoTemplate{}, "storageclass.yaml", []byte(storageClassTemplate), map[string]any{"STORAGE_CLASS": input}); err != nil {
		t.Fatal(err)
	}
	if input["metadata"].(map[string]any)["name"] != "backend" || input["status"] == nil {
		t.Fatal("rendering mutated source object")
	}
}

func TestPrepareVMStorageOmitsUnavailableClaimDefaults(t *testing.T) {
	for _, source := range []string{`{}`, `{"status":{"claimPropertySets":null}}`, `{"status":{"claimPropertySets":[]}}`} {
		t.Run(source, func(t *testing.T) {
			dir := storageCLI(t)
			t.Setenv("TEST_PROFILE", source)
			s := clustercheck.SnapshotSelection{StorageClass: "backend", SnapshotClass: "chosen"}
			if _, err := prepareVMStorage(t.Context(), s); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(filepath.Join(dir, "patch.json"))
			if err != nil {
				t.Fatal(err)
			}
			var patch map[string]any
			if err := json.Unmarshal(data, &patch); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(patch, map[string]any{"spec": map[string]any{"snapshotClass": "chosen"}}) {
				t.Fatalf("unexpected profile patch: %s", data)
			}
		})
	}
}
