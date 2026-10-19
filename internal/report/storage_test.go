package report

import (
	"bytes"
	"strings"
	"testing"

	"github.com/openshift-storage-cert/storage-cert-harness/internal/core"
)

func TestStorageSelectionExportAndMerge(t *testing.T) {
	base := Build("base", "2.0", core.Provenance{}, nil)
	base.StorageSelections = []core.StorageSelection{{TR: "TR-VIRT-010", Variant: "small", StorageClass: "backend-sc", SnapshotClass: "old-snapshot"}}
	next := Build("next", "2.0", core.Provenance{}, nil)
	next.StorageSelections = []core.StorageSelection{
		{TR: "TR-VIRT-010", Variant: "small", StorageClass: "backend-sc", SnapshotClass: "selected-snapshot", EffectiveStorageClass: "private-sc"},
		{TR: "TR-VIRT-010", Variant: "large", StorageClass: "backend-sc", SnapshotClass: "other-snapshot"},
	}
	merged, err := Merge(base, next)
	if err != nil {
		t.Fatal(err)
	}
	if len(merged.StorageSelections) != 2 || merged.StorageSelections[0].SnapshotClass != "selected-snapshot" {
		t.Fatalf("merged=%+v", merged.StorageSelections)
	}
	if base.StorageSelections[0].SnapshotClass != "old-snapshot" {
		t.Fatal("mutated base")
	}
	for _, exporter := range []Exporter{JSONExporter{}, MarkdownExporter{}, JUnitExporter{}} {
		t.Run(exporter.Format(), func(t *testing.T) {
			var buf bytes.Buffer
			if err := exporter.Export(&buf, merged); err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{"backend-sc", "selected-snapshot", "private-sc", "other-snapshot"} {
				if !strings.Contains(buf.String(), want) {
					t.Fatalf("missing %s: %s", want, buf.String())
				}
			}
		})
	}
}
