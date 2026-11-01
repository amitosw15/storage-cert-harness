package clustercheck

import (
	"errors"
	"testing"

	"github.com/openshift-storage-cert/storage-cert-harness/internal/core"
)

func storageSnapshotCLI(t *testing.T) {
	t.Helper()
	storageCLI(t)
	t.Setenv("STORAGE_SNAPSHOT", `{"metadata":{"name":"image","namespace":"images"},"spec":{"source":{"persistentVolumeClaimName":"original"}},"status":{"readyToUse":true,"boundVolumeSnapshotContentName":"content"}}`)
	t.Setenv("STORAGE_CONTENT", `{"spec":{"driver":"driver","sourceVolumeMode":"Filesystem","source":{"volumeHandle":"original-handle"},"volumeSnapshotRef":{"name":"image","namespace":"images"}}}`)
}

func TestSnapshotSurvivesDeletedSourcePVC(t *testing.T) {
	storageSnapshotCLI(t)
	t.Setenv("STORAGE_PVC", "")
	source, err := inspectStorageSource(t.Context(), "", "snapshot", NamespacedName{Name: "image", Namespace: "images"})
	if err != nil || source.Class != "" || source.Driver != "driver" || source.VolumeMode != "Filesystem" {
		t.Fatalf("got %+v %v", source, err)
	}
}

func TestSnapshotProvenanceUsesVolumeIdentity(t *testing.T) {
	for _, handle := range []string{"original-handle", "recreated-pvc-handle"} {
		t.Run(handle, func(t *testing.T) {
			storageSnapshotCLI(t)
			t.Setenv("STORAGE_PVC", `{"spec":{"storageClassName":"nas","volumeName":"original-pv"},"status":{"phase":"Bound"}}`)
			t.Setenv("STORAGE_PV", `{"spec":{"storageClassName":"nas","csi":{"driver":"driver","volumeHandle":"`+handle+`"}}}`)
			source, err := inspectStorageSource(t.Context(), "", "snapshot", NamespacedName{Name: "image", Namespace: "images"})
			if err != nil {
				t.Fatal(err)
			}
			want := ""
			if handle == "original-handle" {
				want = "nas"
			}
			if source.Class != want {
				t.Fatalf("got %+v; want class %q", source, want)
			}
		})
	}
}

func TestMissingAndUnreadyBootSources(t *testing.T) {
	for _, tc := range []struct {
		kind, key, value string
		want             error
	}{
		{"DataSource", "STORAGE_SOURCE", "", ErrNotFound},
		{"pvc", "STORAGE_PVC", "", ErrNotFound},
		{"pvc", "STORAGE_PVC", `{"status":{"phase":"Pending"}}`, errStorageSourceNotReady},
		{"snapshot", "STORAGE_SNAPSHOT", "", ErrNotFound},
		{"snapshot", "STORAGE_SNAPSHOT", `{"status":{"readyToUse":false}}`, errStorageSourceNotReady},
	} {
		t.Run(tc.kind+tc.value, func(t *testing.T) {
			storageCLI(t)
			t.Setenv(tc.key, tc.value)
			_, err := inspectStorageSource(t.Context(), "", tc.kind, NamespacedName{Name: "image", Namespace: "images"})
			if !errors.Is(err, tc.want) {
				t.Fatalf("got %v; want %v", err, tc.want)
			}
		})
	}
}

func TestSourceClassPolicy(t *testing.T) {
	for _, tc := range []struct {
		name        string
		value       any
		present     bool
		sourceKind  string
		wantError   bool
		wantWarning bool
	}{
		{name: "default strict mismatch", sourceKind: "pvc", wantError: true},
		{name: "explicit strict mismatch", value: true, present: true, sourceKind: "pvc", wantError: true},
		{name: "explicit optout mismatch", value: false, present: true, sourceKind: "pvc", wantWarning: true},
		{name: "default strict unknown snapshot class", sourceKind: "snapshot", wantError: true},
		{name: "optout unknown snapshot class", value: false, present: true, sourceKind: "snapshot"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			storageCLI(t)
			request := StorageRequest{Identity: "root", CDI: true, Namespace: "workloads", ExpectedClass: "economy", Claim: StorageClaim{StorageClassName: stringPointer("economy"), AccessModes: []string{"ReadWriteMany"}, VolumeMode: "Filesystem"}, SourceKind: tc.sourceKind, Source: NamespacedName{Name: "image", Namespace: "images"}}
			requests := []StorageRequest{request}
			tr := core.TestRequirement{ID: "test", Params: map[string]any{}}
			if tc.present {
				tr.Params["require_same_source_storage_class"] = tc.value
			}
			if err := ApplyStoragePolicy(requests, tr); err != nil {
				t.Fatal(err)
			}
			got := CheckStorage(t.Context(), "", requests)
			hasError, hasWarning := false, false
			for _, f := range got {
				hasError = hasError || f.Level == "error"
				hasWarning = hasWarning || f.Level == "warn"
			}
			if hasError != tc.wantError || hasWarning != tc.wantWarning {
				t.Fatalf("unexpected findings: %+v", got)
			}
		})
	}
	if err := ApplyStoragePolicy(nil, core.TestRequirement{ID: "bad", Params: map[string]any{"require_same_source_storage_class": "false"}}); err == nil {
		t.Fatal("non-boolean policy accepted")
	}
}
