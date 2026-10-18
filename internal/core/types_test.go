package core

import (
	"bytes"
	"testing"
)

func TestToolOutputMirrorsSubprocessStreams(t *testing.T) {
	var terminal, runLog bytes.Buffer
	rc := &RunCtx{LogOutput: &runLog}
	if _, err := rc.ToolOutput(&terminal).Write([]byte("tool output\n")); err != nil {
		t.Fatal(err)
	}

	if terminal.String() != runLog.String() {
		t.Fatalf("terminal and run log differ: terminal=%q run-log=%q", terminal.String(), runLog.String())
	}
}

// Each execution needs a results directory no other execution can reach: two
// TRs in one run, two variants of one TR, and a --retry-failed attempt and the
// attempt it replaces must all land somewhere different. The retry case is why
// the run id is in the path — a resumed run reuses --workdir, so without it the
// retry would share a directory with the failed attempt it is meant to supersede.
func TestExecutionDirSeparatesEveryExecution(t *testing.T) {
	first := &RunCtx{WorkDir: "/w", RunID: "20261018-060401"}
	retry := &RunCtx{WorkDir: "/w", RunID: "20261018-143000"}

	seen := map[string]string{}
	for _, tc := range []struct{ what, got string }{
		{"TR-VIRT-013", first.ExecutionDir("virtbench", "TR-VIRT-013", "")},
		{"TR-VIRT-018", first.ExecutionDir("virtbench", "TR-VIRT-018", "")},
		{"TR-VIRT-018 variant small", first.ExecutionDir("virtbench", "TR-VIRT-018", "small")},
		{"TR-VIRT-018 variant large", first.ExecutionDir("virtbench", "TR-VIRT-018", "large")},
		{"TR-VIRT-018 retried", retry.ExecutionDir("virtbench", "TR-VIRT-018", "")},
	} {
		if prev, dup := seen[tc.got]; dup {
			t.Errorf("%s and %s share a results dir %s", prev, tc.what, tc.got)
		}
		seen[tc.got] = tc.what
	}

	if got, want := first.ExecutionDir("virtbench", "TR-VIRT-018", ""), "/w/virtbench/TR-VIRT-018/20261018-060401"; got != want {
		t.Errorf("ExecutionDir = %q, want %q", got, want)
	}
	if got, want := first.ExecutionDir("virtbench", "TR-VIRT-018", "small"), "/w/virtbench/TR-VIRT-018.small/20261018-060401"; got != want {
		t.Errorf("variant ExecutionDir = %q, want %q", got, want)
	}
}
