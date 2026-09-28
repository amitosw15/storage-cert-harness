package virtbench

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gitlab.cee.redhat.com/eco-special-projects/storage-cert-harness/internal/core"
)

// fakeSSHCluster models pod state across separate kubectl invocations, including
// a drain deleting the pod between setup and the next scenario's provisioning.
func fakeSSHCluster(t *testing.T, state string) (setState func(string), commands func() string) {
	t.Helper()
	dir := t.TempDir()
	statePath, logPath := filepath.Join(dir, "state"), filepath.Join(dir, "commands")
	t.Setenv("SSH_TEST_STATE", statePath)
	t.Setenv("SSH_TEST_LOG", logPath)
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$SSH_TEST_LOG"
if [ "$1" = --kubeconfig ]; then shift 2; fi
read -r state < "$SSH_TEST_STATE"
case "$1" in
get)
  case "$state" in
  forbidden) echo 'Error from server (Forbidden): cannot get pods' >&2; exit 1 ;;
  missing) exit 0 ;;
  terminating)
    printf '%s\n' '{"metadata":{"deletionTimestamp":"2026-09-27T00:00:00Z"},"status":{"phase":"Running"}}'
    printf '%s\n' missing > "$SSH_TEST_STATE" ;;
  *) printf '{"status":{"phase":"%s"}}\n' "$state" ;;
  esac ;;
apply)
  /bin/cat >/dev/null
  printf '%s\n' Running > "$SSH_TEST_STATE" ;;
delete) printf '%s\n' missing > "$SSH_TEST_STATE" ;;
exec) [ "$state" = Running ] ;;
*) exit 1 ;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "kubectl"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	setState = func(state string) {
		t.Helper()
		if err := os.WriteFile(statePath, []byte(state+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	setState(state)
	return setState, func() string {
		t.Helper()
		data, err := os.ReadFile(logPath)
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
}

func sshTestRunCtx(t *testing.T) *core.RunCtx {
	t.Helper()
	return &core.RunCtx{WorkDir: t.TempDir(), Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

func TestProvisionRecoversSSHHelperBetweenScenarios(t *testing.T) {
	setState, commands := fakeSSHCluster(t, "Running")
	rc := sshTestRunCtx(t)
	t.Cleanup(func() { _ = sharedSSHSetup.Teardown(context.Background(), rc, nil) })
	for _, id := range []string{"TR-VIRT-018", "TR-VIRT-001", "TR-VIRT-018"} {
		// Each deletion represents a previous test evicting the shared helper.
		setState("missing")
		trs := []core.TestRequirement{{ID: id, Params: map[string]any{"kubeconfig": "/test/config"}}}
		if err := (provisioner{}).Provision(t.Context(), rc, core.NewBag(), trs); err != nil {
			t.Fatal(err)
		}
		if !sshpassReady(t.Context(), "/test/config") {
			t.Fatalf("%s started with a missing SSH helper", id)
		}
	}
	if got := strings.Count(commands(), "apply -f -"); got != 3 {
		t.Fatalf("recreated helper %d times, want 3", got)
	}
}

func TestSSHHelperReplacesTerminalPods(t *testing.T) {
	for _, phase := range []string{"Failed", "Succeeded"} {
		t.Run(phase, func(t *testing.T) {
			_, commands := fakeSSHCluster(t, phase)
			s := &sshPodSetup{}
			if err := s.Setup(t.Context(), sshTestRunCtx(t), nil); err != nil {
				t.Fatal(err)
			}
			got := commands()
			if strings.Count(got, "delete pod") != 1 || strings.Count(got, "apply -f -") != 1 {
				t.Fatalf("terminal pod was not replaced exactly once: %s", got)
			}
		})
	}
}

func TestSSHHelperWaitsForTerminatingPod(t *testing.T) {
	_, commands := fakeSSHCluster(t, "terminating")
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if err := (&sshPodSetup{}).Setup(ctx, sshTestRunCtx(t), nil); err != nil {
		t.Fatal(err)
	}
	got := commands()
	firstApply := strings.Index(got, "apply -f -")
	if firstApply < 0 || strings.Count(got[:firstApply], "get pod") < 2 {
		t.Fatalf("applied before waiting for terminating pod to disappear: %s", got)
	}
}

func TestSSHHelperLateCreationIsCleanedUpAndOwnershipResets(t *testing.T) {
	setState, commands := fakeSSHCluster(t, "Running")
	s := &sshPodSetup{}
	rc := sshTestRunCtx(t)
	if err := s.Setup(t.Context(), rc, nil); err != nil {
		t.Fatal(err)
	}
	setState("missing")
	if err := s.Setup(t.Context(), rc, nil); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := s.Teardown(ctx, rc, nil); err != nil {
		t.Fatal(err)
	}
	// A subsequent run reuses an externally created helper and must leave it.
	setState("Running")
	if err := s.Setup(t.Context(), rc, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.Teardown(t.Context(), rc, nil); err != nil {
		t.Fatal(err)
	}
	if got := commands(); strings.Count(got, "delete pod") != 1 {
		t.Fatalf("expected only the recreated pod to be cleaned up: %s", got)
	}
}

func TestSSHHelperHealthyPodIsReused(t *testing.T) {
	_, commands := fakeSSHCluster(t, "Running")
	s := &sshPodSetup{}
	for range 2 {
		if err := s.Setup(t.Context(), sshTestRunCtx(t), nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Teardown(t.Context(), sshTestRunCtx(t), nil); err != nil {
		t.Fatal(err)
	}
	if got := commands(); strings.Contains(got, "apply") || strings.Contains(got, "delete") {
		t.Fatalf("healthy pre-existing pod was changed: %s", got)
	}
}

func TestSSHHelperLookupErrorIsNotTreatedAsMissing(t *testing.T) {
	_, commands := fakeSSHCluster(t, "forbidden")
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	err := (&sshPodSetup{}).Setup(ctx, sshTestRunCtx(t), nil)
	if err == nil || !strings.Contains(err.Error(), "Forbidden") {
		t.Fatalf("want actionable lookup error, got %v", err)
	}
	if got := commands(); strings.Contains(got, "apply") || strings.Contains(got, "delete") {
		t.Fatalf("API failure triggered mutation: %s", got)
	}
}

func TestSSHHelperCancelledWait(t *testing.T) {
	fakeSSHCluster(t, "Pending")
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := (&sshPodSetup{}).Setup(ctx, sshTestRunCtx(t), nil); err == nil {
		t.Fatal("want cancellation error")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("cancelled setup took %s", elapsed)
	}
}

func TestSSHHelperReplayDoesNotUseCluster(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	trs := []core.TestRequirement{{Params: map[string]any{"results_dir": t.TempDir()}}}
	rc := sshTestRunCtx(t)
	if err := (&sshPodSetup{}).Setup(t.Context(), rc, trs); err != nil {
		t.Fatal(err)
	}
	if err := (provisioner{}).Provision(t.Context(), rc, core.NewBag(), trs); err != nil {
		t.Fatal(err)
	}
}
