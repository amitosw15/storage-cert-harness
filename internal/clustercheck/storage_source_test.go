package clustercheck

import (
	"errors"
	"testing"
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
	source, err := inspectStorageSource(t.Context(), "", "snapshot", storageReference{Name: "image", Namespace: "images"})
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
			source, err := inspectStorageSource(t.Context(), "", "snapshot", storageReference{Name: "image", Namespace: "images"})
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
		{"DataSource", "STORAGE_SOURCE", "", errPreflightNotFound},
		{"pvc", "STORAGE_PVC", "", errPreflightNotFound},
		{"pvc", "STORAGE_PVC", `{"status":{"phase":"Pending"}}`, errStorageSourceNotReady},
		{"snapshot", "STORAGE_SNAPSHOT", "", errPreflightNotFound},
		{"snapshot", "STORAGE_SNAPSHOT", `{"status":{"readyToUse":false}}`, errStorageSourceNotReady},
	} {
		t.Run(tc.kind+tc.value, func(t *testing.T) {
			storageCLI(t)
			t.Setenv(tc.key, tc.value)
			_, err := inspectStorageSource(t.Context(), "", tc.kind, storageReference{Name: "image", Namespace: "images"})
			if !errors.Is(err, tc.want) {
				t.Fatalf("got %v; want %v", err, tc.want)
			}
		})
	}
}

func TestSourceClassInvestigationIsAdvisory(t *testing.T) {
	for _, tc := range []struct{ name, kind, target, level, fragment string }{
		{"matching PVC", "pvc", "nas", "info", "matches target"},
		{"different PVC", "pvc", "economy", "warn", "differs from target"},
		{"unknown snapshot class", "snapshot", "nas", "warn", "original StorageClass is unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			storageCLI(t)
			request := StorageRequest{Identity: "root", CDI: true, Namespace: "workloads", ExpectedClass: tc.target, Claim: StorageClaim{StorageClassName: stringPointer(tc.target), AccessModes: []string{"ReadWriteMany"}, VolumeMode: "Filesystem"}, SourceKind: tc.kind, Source: storageReference{Name: "image", Namespace: "images"}}
			got := CheckStorage(t.Context(), "", []StorageRequest{request})
			for _, finding := range got {
				if finding.Level == "error" {
					t.Fatalf("source-class investigation blocked execution: %+v", got)
				}
			}
			if !findingContains(got, tc.level, tc.fragment) {
				t.Fatalf("missing %s finding %q: %+v", tc.level, tc.fragment, got)
			}
		})
	}
}

func TestSnapshotDataSourceSurvivesDeletedOriginalVolume(t *testing.T) {
	storageSnapshotCLI(t)
	t.Setenv("STORAGE_SOURCE", `{"spec":{"source":{"snapshot":{"name":"image","namespace":"images"}}},"status":{"conditions":[{"type":"Ready","status":"True"}]}}`)
	t.Setenv("STORAGE_PVC", "")
	source, err := inspectStorageSource(t.Context(), "", "DataSource", storageReference{Name: "golden", Namespace: "images"})
	if err != nil || source.Kind != "snapshot" || source.Class != "" || source.Driver != "driver" {
		t.Fatalf("ready snapshot DataSource with deleted original PVC: source=%+v err=%v", source, err)
	}
}

func TestReadySnapshotWithoutContentProvenanceDoesNotBlock(t *testing.T) {
	storageSnapshotCLI(t)
	t.Setenv("STORAGE_CONTENT", "")
	ref := storageReference{Name: "image", Namespace: "images"}
	requests := []StorageRequest{{Identity: "root", Namespace: "images", CDI: true, ExpectedClass: "nas", SourceKind: "snapshot", Source: ref, Claim: StorageClaim{StorageClassName: stringPointer("nas"), AccessModes: []string{"ReadWriteMany"}, VolumeMode: "Filesystem"}}}
	findings := CheckStorage(t.Context(), "", requests)
	if !findingContains(findings, "warn", "original StorageClass is unavailable") {
		t.Fatalf("unknown provenance warning missing: %+v", findings)
	}
	for _, f := range findings {
		if f.Level == "error" {
			t.Fatalf("optional provenance blocked ready snapshot: %+v", findings)
		}
	}
}
