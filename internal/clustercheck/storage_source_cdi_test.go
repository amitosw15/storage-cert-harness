package clustercheck

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/openshift-storage-cert/storage-cert-harness/internal/core"
)

func cdiSnapshotCLI(t *testing.T) {
	t.Helper()
	storageSnapshotCLI(t)
	t.Setenv("STORAGE_PVC", "")
	t.Setenv("STORAGE_SNAPSHOT", `{"metadata":{"uid":"snapshot-uid","labels":{"cdi.kubevirt.io/dataImportCron":"image-cron"}},"spec":{"source":{"persistentVolumeClaimName":"original"}},"status":{"readyToUse":true,"boundVolumeSnapshotContentName":"content"}}`)
	t.Setenv("STORAGE_CRON", `{"metadata":{"annotations":{"cdi.kubevirt.io/storage.import.storageClass":"nas"}},"spec":{"managedDataSource":"golden"},"status":{"sourceFormat":"snapshot","currentImports":[{"DataVolumeName":"image"}],"lastImportedPVC":{"name":"original","namespace":"images"}}}`)
	t.Setenv("STORAGE_SOURCE", `{"spec":{"source":{"snapshot":{"name":"image"}}},"status":{"conditions":[{"type":"Ready","status":"True"}]}}`)
}

func TestCDISnapshotClassInvestigation(t *testing.T) {
	for _, tc := range []struct {
		name, target, class, level, fragment string
		setup                                func(*testing.T)
	}{
		{name: "missing optional snapshot content", target: "nas", class: "nas", level: "info", fragment: "matches target", setup: func(t *testing.T) { t.Setenv("STORAGE_CONTENT", "") }},
		{name: "matching recorded import", target: "nas", class: "nas", level: "info", fragment: "matches target"},
		{name: "different recorded import", target: "economy", class: "nas", level: "warn", fragment: "differs from target"},
		{name: "stale current import", target: "nas", level: "warn", fragment: "original StorageClass is unavailable", setup: func(t *testing.T) {
			t.Setenv("STORAGE_CRON", strings.ReplaceAll(os.Getenv("STORAGE_CRON"), `"DataVolumeName":"image"`, `"DataVolumeName":"older"`))
		}},
		{name: "wrong original PVC", target: "nas", level: "warn", fragment: "original StorageClass is unavailable", setup: func(t *testing.T) {
			t.Setenv("STORAGE_CRON", strings.ReplaceAll(os.Getenv("STORAGE_CRON"), `"name":"original"`, `"name":"another"`))
		}},
		{name: "wrong source namespace", target: "nas", level: "warn", fragment: "original StorageClass is unavailable", setup: func(t *testing.T) {
			t.Setenv("STORAGE_CRON", strings.ReplaceAll(os.Getenv("STORAGE_CRON"), `"namespace":"images"`, `"namespace":"other"`))
		}},
		{name: "missing class annotation", target: "nas", level: "warn", fragment: "original StorageClass is unavailable", setup: func(t *testing.T) {
			t.Setenv("STORAGE_CRON", strings.ReplaceAll(os.Getenv("STORAGE_CRON"), `"nas"`, `""`))
		}},
		{name: "managed DataSource moved", target: "nas", level: "warn", fragment: "original StorageClass is unavailable", setup: func(t *testing.T) {
			t.Setenv("STORAGE_SOURCE", strings.ReplaceAll(os.Getenv("STORAGE_SOURCE"), `"name":"image"`, `"name":"new-image"`))
		}},
		{name: "missing optional cron", target: "nas", level: "warn", fragment: "original StorageClass is unavailable", setup: func(t *testing.T) { t.Setenv("STORAGE_CRON", "") }},
		{name: "optional cron forbidden", target: "nas", level: "warn", fragment: "original StorageClass is unavailable", setup: func(t *testing.T) { t.Setenv("CRON_DENIED", "true") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cdiSnapshotCLI(t)
			if tc.setup != nil {
				tc.setup(t)
			}
			ref := NamespacedName{Name: "image", Namespace: "images"}
			source, err := inspectStorageSource(t.Context(), "", "snapshot", ref)
			if err != nil || source.Class != tc.class {
				t.Fatalf("source=%+v err=%v; want class %q", source, err, tc.class)
			}
			request := StorageRequest{Identity: "root", CDI: true, Namespace: "images", ExpectedClass: tc.target, Claim: StorageClaim{StorageClassName: stringPointer(tc.target), AccessModes: []string{"ReadWriteMany"}, VolumeMode: "Filesystem"}, SourceKind: "snapshot", Source: ref}
			findings := CheckStorage(t.Context(), "", []StorageRequest{request})
			for _, f := range findings {
				if f.Level == "error" {
					t.Fatalf("advisory investigation blocked execution: %+v", findings)
				}
			}
			if !findingContains(findings, tc.level, tc.fragment) {
				t.Fatalf("missing %s/%s: %+v", tc.level, tc.fragment, findings)
			}
			if tc.class != "" && !findingContains(findings, "info", "inferred from CDI DataImportCron images/image-cron") {
				t.Fatalf("inference provenance missing: %+v", findings)
			}
		})
	}
}

func TestCDISnapshotInferenceRejectsRecreatedSnapshot(t *testing.T) {
	cdiSnapshotCLI(t)
	var snapshot VolumeSnapshot
	if err := json.Unmarshal([]byte(os.Getenv("STORAGE_SNAPSHOT")), &snapshot); err != nil {
		t.Fatal(err)
	}
	t.Setenv("STORAGE_SNAPSHOT", strings.ReplaceAll(os.Getenv("STORAGE_SNAPSHOT"), "snapshot-uid", "replacement-uid"))
	class, note := inferCDISnapshotClass(t.Context(), "", NamespacedName{Name: "image", Namespace: "images"}, snapshot)
	if class != "" || !strings.Contains(note, "identity or readiness changed") {
		t.Fatalf("class=%q note=%q", class, note)
	}
}

// TestCDISnapshotClassLive investigates an existing image source without provisioning resources.
func TestCDISnapshotClassLive(t *testing.T) {
	kubeconfig := os.Getenv("STORAGE_PREFLIGHT_LIVE_KUBECONFIG")
	value := os.Getenv("STORAGE_PREFLIGHT_LIVE_SNAPSHOT_DATASOURCE")
	target := os.Getenv("STORAGE_PREFLIGHT_LIVE_SNAPSHOT_CLASS")
	if kubeconfig == "" || value == "" || target == "" {
		t.Skip("set live kubeconfig, snapshot DataSource and expected class")
	}
	parts := strings.SplitN(value, "/", 2)
	if len(parts) != 2 {
		t.Fatal("snapshot DataSource must be namespace/name")
	}
	ref := NamespacedName{Name: parts[1], Namespace: parts[0]}
	source, err := inspectStorageSource(t.Context(), kubeconfig, "DataSource", ref)
	if err != nil || source.Class != target {
		t.Fatalf("source=%+v err=%v; want class %q", source, err, target)
	}
	if !strings.Contains(strings.Join(source.Notes, "; "), "inferred from CDI DataImportCron") {
		t.Fatalf("expected CDI inference: %+v", source)
	}
	t.Logf("source=%s/%s StorageClass=%s provenance=%v", source.Reference.Namespace, source.Reference.Name, source.Class, source.Notes)
	for _, id := range []string{"TR-VIRT-001", "TR-VIRT-013", "TR-VIRT-018"} {
		requests := []StorageRequest{{Identity: "root", Namespace: parts[0], ExpectedClass: target, CDI: true, SourceKind: "DataSource", Source: ref, Claim: StorageClaim{StorageClassName: &target, AccessModes: []string{"ReadWriteOnce"}, VolumeMode: "Block"}}}
		LabelStorageRequests(requests, core.TestRequirement{ID: id})
		findings := CheckStorage(t.Context(), kubeconfig, requests)
		for _, f := range findings {
			if f.Level == "error" {
				t.Fatalf("%s blocked: %+v", id, findings)
			}
		}
		if !findingContains(findings, "info", "matches target") {
			t.Fatalf("%s class match missing: %+v", id, findings)
		}
		t.Logf("%s: source matches requested StorageClass; no blocking findings", id)
	}
}
