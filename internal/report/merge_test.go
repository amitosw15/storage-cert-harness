package report

import (
	"testing"

	"github.com/openshift-storage-cert/storage-cert-harness/internal/core"
)

func TestMergeUnionsAndReusesEnvironment(t *testing.T) {
	base := Build("run-idle", "2.0", core.Provenance{KBGitCommit: "abc"}, []core.Verdict{
		{TR: "TR-1", Level: 2, Scenario: "idle", Item: "sla:lat", Outcome: core.OutcomePass},
	})
	base.Environment.Hardware.Nodes = 6
	base.Environment.Platform.Backend = "netapp"
	base.Measurements = []core.Measurement{{TR: "TR-1", Scenario: "idle", Name: "lat", Value: 1}}
	base.Attestations = []core.Attestation{{Claim: "ra_url", Value: "https://x", SignedBy: "me"}}

	// Second run collected no environment (--skip-env / --continue-from).
	next := Build("run-pressure", "2.0", core.Provenance{KBGitCommit: "abc"}, []core.Verdict{
		{TR: "TR-1", Level: 2, Scenario: "under-pressure", Item: "sla:lat", Outcome: core.OutcomeFail},
	})
	next.Measurements = []core.Measurement{{TR: "TR-1", Scenario: "under-pressure", Name: "lat", Value: 9}}

	m, err := Merge(base, next)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Verdicts) != 2 {
		t.Fatalf("want 2 verdicts (distinct scenarios), got %d", len(m.Verdicts))
	}
	if m.Summary.Pass != 1 || m.Summary.Fail != 1 {
		t.Fatalf("summary not recomputed: %+v", m.Summary)
	}
	if m.Summary.CertifiedLevel != 0 {
		t.Fatalf("a failing scenario must drop the certified level, got %d", m.Summary.CertifiedLevel)
	}
	if m.Environment.Hardware.Nodes != 6 || m.Environment.Platform.Backend != "netapp" {
		t.Fatalf("environment not reused from base: %+v", m.Environment)
	}
	if len(m.Measurements) != 2 {
		t.Fatalf("want 2 measurements, got %d", len(m.Measurements))
	}
	if len(m.Attestations) != 1 || m.Attestations[0].Claim != "ra_url" {
		t.Fatalf("attestations not carried: %+v", m.Attestations)
	}
}

func TestMergeFallsBackToNextEnvironment(t *testing.T) {
	base := Build("a", "2.0", core.Provenance{}, []core.Verdict{{TR: "TR-1", Level: 2, Item: "i", Outcome: core.OutcomePass}})
	next := Build("b", "2.0", core.Provenance{}, []core.Verdict{{TR: "TR-2", Level: 2, Item: "i", Outcome: core.OutcomePass}})
	next.Environment.Platform.Backend = "netapp"

	m, err := Merge(base, next)
	if err != nil {
		t.Fatal(err)
	}
	if m.Environment.Platform.Backend != "netapp" {
		t.Fatalf("empty base env should fall back to next: %+v", m.Environment)
	}
}

func TestMergeRejectsDifferentKBCommit(t *testing.T) {
	base := Build("a", "2.0", core.Provenance{KBGitCommit: "abc"}, nil)
	next := Build("b", "2.0", core.Provenance{KBGitCommit: "def"}, nil)
	if _, err := Merge(base, next); err == nil {
		t.Fatal("expected error merging reports from different KB commits")
	}
}

func TestMergeReRunReplacesSameScenario(t *testing.T) {
	base := Build("a", "2.0", core.Provenance{}, []core.Verdict{
		{TR: "TR-1", Level: 2, Scenario: "idle", Item: "sla:lat", Outcome: core.OutcomeFail},
	})
	next := Build("b", "2.0", core.Provenance{}, []core.Verdict{
		{TR: "TR-1", Level: 2, Scenario: "idle", Item: "sla:lat", Outcome: core.OutcomePass},
	})
	m, err := Merge(base, next)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Verdicts) != 1 {
		t.Fatalf("same TR+item+scenario should dedupe to 1, got %d", len(m.Verdicts))
	}
	if m.Verdicts[0].Outcome != core.OutcomePass {
		t.Fatalf("later run should win, got %s", m.Verdicts[0].Outcome)
	}
}
