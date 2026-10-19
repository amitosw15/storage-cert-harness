package kb

import (
	"testing"

	"github.com/openshift-storage-cert/storage-cert-harness/internal/catalog"
	"github.com/openshift-storage-cert/storage-cert-harness/internal/core"
	"github.com/openshift-storage-cert/storage-cert-harness/internal/thresholds"
)

func prov(commit string) core.Provenance {
	return core.Provenance{KBGitCommit: commit, KBCommitDate: "2026-01-01", GeneratedDate: "2026-01-01"}
}

func TestJoinMergesAndSorts(t *testing.T) {
	cat := &catalog.Doc{
		SchemaVersion: "2.0",
		Provenance:    prov("abc123"),
		TestRequirements: []catalog.TR{
			// TR-2 carries its check descriptors in the catalog (ADR-0013).
			{ID: "TR-2", Title: "second", PartnerLevel: 2, AutomationTool: "fio",
				Checks: []core.Check{{ID: "c1", Kind: core.CheckCapability}}},
			// TR-1 carries its metric descriptor (no number) in the catalog.
			{ID: "TR-1", Title: "first", PartnerLevel: 3, AutomationTool: "fio",
				SLA: []core.SLA{{Metric: "latency_ms", Unit: "ms"}}},
		},
	}
	v := 2.0
	th := &thresholds.Doc{
		SchemaVersion: "2.0",
		Provenance:    prov("abc123"),
		TestRequirements: []thresholds.TR{
			{ID: "TR-1", SLA: []core.SLA{{Metric: "latency_ms", Operator: "<=", Value: &v, Unit: "ms"}}},
			{ID: "TR-2"},
		},
	}
	got, err := Join(cat, th)
	if err != nil {
		t.Fatalf("join: %v", err)
	}
	if len(got) != 2 || got[0].ID != "TR-1" || got[1].ID != "TR-2" {
		t.Fatalf("expected sorted [TR-1 TR-2], got %+v", got)
	}
	// TR-1: descriptor from the catalog, bar joined from thresholds.
	if len(got[0].SLAs) != 1 || got[0].SLAs[0].Metric != "latency_ms" {
		t.Fatalf("TR-1 descriptor not carried: %+v", got[0].SLAs)
	}
	if len(got[0].Bars) != 1 || got[0].Bars[0].Value == nil || *got[0].Bars[0].Value != 2.0 {
		t.Fatalf("TR-1 bar not joined from thresholds: %+v", got[0].Bars)
	}
	// TR-2: checks come from the catalog, not thresholds.
	if len(got[1].Checks) != 1 || got[1].Checks[0].ID != "c1" {
		t.Fatalf("TR-2 checks not carried from catalog: %+v", got[1].Checks)
	}
}

func TestJoinRejectsStaleCommit(t *testing.T) {
	cat := &catalog.Doc{SchemaVersion: "2.0", Provenance: prov("abc123"),
		TestRequirements: []catalog.TR{{ID: "TR-1"}}}
	th := &thresholds.Doc{SchemaVersion: "2.0", Provenance: prov("deadbeef"),
		TestRequirements: []thresholds.TR{{ID: "TR-1"}}}
	if _, err := Join(cat, th); err == nil {
		t.Fatal("expected stale-commit error, got nil")
	}
}

func TestJoinRejectsIDMismatch(t *testing.T) {
	cat := &catalog.Doc{SchemaVersion: "2.0", Provenance: prov("abc123"),
		TestRequirements: []catalog.TR{{ID: "TR-1"}, {ID: "TR-2"}}}
	th := &thresholds.Doc{SchemaVersion: "2.0", Provenance: prov("abc123"),
		TestRequirements: []thresholds.TR{{ID: "TR-1"}}}
	if _, err := Join(cat, th); err == nil {
		t.Fatal("expected id-parity error (TR-2 missing from thresholds), got nil")
	}

	th2 := &thresholds.Doc{SchemaVersion: "2.0", Provenance: prov("abc123"),
		TestRequirements: []thresholds.TR{{ID: "TR-1"}, {ID: "TR-2"}, {ID: "TR-3"}}}
	if _, err := Join(cat, th2); err == nil {
		t.Fatal("expected id-parity error (TR-3 missing from catalog), got nil")
	}
}

func TestFromCatalogHasNoDefinitions(t *testing.T) {
	cat := &catalog.Doc{SchemaVersion: "2.0", Provenance: prov("abc123"),
		TestRequirements: []catalog.TR{{ID: "TR-1", SLACount: 1}}}
	got := FromCatalog(cat)
	if len(got) != 1 || got[0].SLACount != 1 || len(got[0].SLAs) != 0 || len(got[0].Checks) != 0 {
		t.Fatalf("catalog-only TRs must keep counts but carry no definitions: %+v", got)
	}
}
