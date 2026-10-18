package orchestrator

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/openshift-storage-cert/storage-cert-harness/internal/core"
)

func TestProgressLogsQueuedAndFinalLists(t *testing.T) {
	var log bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&log, &slog.HandlerOptions{Level: slog.LevelDebug}))
	trs := []core.TestRequirement{
		{ID: "TR-1"},
		{ID: "TR-2", Variant: "small"},
		{ID: "TR-3"},
	}
	p := newProgressTracker(logger, trs)
	p.start(trs[0])
	p.complete(trs[0], core.OutcomePass)
	p.complete(trs[1], core.OutcomeFail)
	p.complete(trs[2], core.OutcomeError)

	out := log.String()
	for _, want := range []string{
		`status=started completed=0 total=3 remaining=3 queued="[TR-1 TR-2 [small] TR-3]"`,
		`status=completed test=TR-1 outcome=pass completed=1 total=3 remaining=2`,
		`status=complete completed=3 total=3 remaining=0`,
		`passed=[TR-1]`,
		`failed="[TR-2 [small]]"`,
		`errors=[TR-3]`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("log missing %q:\n%s", want, out)
		}
	}
}

func TestProgressSummaryOutcome(t *testing.T) {
	tr := core.TestRequirement{ID: "TR-1", Variant: "v1"}
	verdicts := []core.Verdict{
		{TR: "TR-1", Variant: "v1", Outcome: core.OutcomePass},
		{TR: "TR-1", Variant: "v1", Outcome: core.OutcomeSkip},
	}
	if got := summarizeTR(verdicts, tr); got != core.OutcomePass {
		t.Fatalf("summarizeTR = %q, want pass", got)
	}
	verdicts[0].Outcome = core.OutcomeFail
	if got := summarizeTR(verdicts, tr); got != core.OutcomeFail {
		t.Fatalf("summarizeTR = %q, want fail", got)
	}
	verdicts[0].Outcome = core.OutcomeError
	if got := summarizeTR(verdicts, tr); got != core.OutcomeError {
		t.Fatalf("summarizeTR = %q, want error", got)
	}
	if got := summarizeTR(nil, tr); got != core.OutcomeError {
		t.Fatalf("summarizeTR with no verdicts = %q, want error", got)
	}
}
