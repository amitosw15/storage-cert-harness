package virtbench

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openshift-storage-cert/storage-cert-harness/internal/core"
)

// fakeSSHCluster models pod state across separate kubectl invocations, including
// a drain deleting the pod between setup and the next scenario's provisioning.
func fakeSSHCluster(t *testing.T, state string) (setState func(string), commands func() string) {
	t.Helper()
	dir := t.TempDir()
	statePath, logPath, annotPath := filepath.Join(dir, "state"), filepath.Join(dir, "commands"), filepath.Join(dir, "annotations")
	deleteFailuresPath, getCountPath := filepath.Join(dir, "delete-failures"), filepath.Join(dir, "get-count")
	t.Setenv("SSH_TEST_STATE", statePath)
	t.Setenv("SSH_TEST_LOG", logPath)
	t.Setenv("SSH_TEST_ANNOTATIONS", annotPath)
	t.Setenv("SSH_TEST_DELETE_FAILURES_FILE", deleteFailuresPath)
	t.Setenv("SSH_TEST_GET_COUNT_FILE", getCountPath)
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$SSH_TEST_LOG"
if [ "$1" = --kubeconfig ]; then shift 2; fi
read -r state < "$SSH_TEST_STATE"
case "$1" in
get)
	if [ "$state" = delayed ]; then
		if [ ! -f "$SSH_TEST_GET_COUNT_FILE" ]; then printf '%s\n' 0 > "$SSH_TEST_GET_COUNT_FILE"; fi
		read -r gets < "$SSH_TEST_GET_COUNT_FILE"
		gets=$((gets + 1))
		printf '%s\n' "$gets" > "$SSH_TEST_GET_COUNT_FILE"
		if [ "$gets" -ge 3 ]; then
			state="${SSH_TEST_APPLY_STATE:-Running}"
			printf '%s\n' "$state" > "$SSH_TEST_STATE"
		else
			exit 0
		fi
	fi
  case "$state" in
  forbidden) echo 'Error from server (Forbidden): cannot get pods' >&2; exit 1 ;;
  missing) exit 0 ;;
  terminating)
    printf '%s\n' '{"metadata":{"deletionTimestamp":"2026-09-27T00:00:00Z"},"status":{"phase":"Running"}}'
    printf '%s\n' missing > "$SSH_TEST_STATE" ;;
	*)
		if [ -n "${SSH_TEST_POD_RUN_ID:-}" ]; then
			printf '{"metadata":{"uid":"ssh-test-pod-uid","annotations":{"storage-cert-harness.redhat.com/run-id":"%s"}},"status":{"phase":"%s"}}\n' "$SSH_TEST_POD_RUN_ID" "$state"
		else
			printf '{"metadata":{"uid":"ssh-test-pod-uid"},"status":{"phase":"%s"}}\n' "$state"
		fi ;;
  esac ;;
create)
  /bin/cat >/dev/null
  post="${SSH_TEST_APPLY_STATE:-Running}"
	if [ -n "${SSH_TEST_APPLY_DELAY:-}" ]; then
	  printf '%s\n' delayed > "$SSH_TEST_STATE"
	  /bin/sleep "$SSH_TEST_APPLY_DELAY"
	else
	  printf '%s\n' "$post" > "$SSH_TEST_STATE"
	fi
	run_id="${SSH_TEST_POD_RUN_ID:-run-create}"
	if [ -n "${SSH_TEST_CREATE_STDERR:-}" ]; then
		echo "$SSH_TEST_CREATE_STDERR" >&2
	fi
	printf '{"metadata":{"uid":"ssh-test-pod-uid","annotations":{"storage-cert-harness.redhat.com/run-id":"%s"}},"status":{"phase":"%s"}}\n' "$run_id" "$post" ;;
delete)
	if [ "$2" = --raw ]; then
		opts_file=""
		shift 2
		while [ $# -gt 0 ]; do
			case "$1" in
			-f) opts_file="$2"; shift 2 ;;
			*) shift ;;
			esac
		done
		if [ -z "$opts_file" ] || [ ! -f "$opts_file" ]; then
			echo 'Error from server (BadRequest): delete requires DeleteOptions' >&2
			exit 1
		fi
		want_uid=$(/usr/bin/sed -n 's/.*"uid":"\([^"]*\)".*/\1/p' "$opts_file" | /usr/bin/head -1)
		pod_uid="${SSH_TEST_POD_UID:-ssh-test-pod-uid}"
		if [ -z "$want_uid" ] || [ "$want_uid" != "$pod_uid" ]; then
			echo "Error from server (Conflict): the UID in the precondition ($want_uid) does not match the UID in record ($pod_uid)" >&2
			exit 1
		fi
		if [ ! -f "$SSH_TEST_DELETE_FAILURES_FILE" ]; then printf '%s\n' "${SSH_TEST_DELETE_FAILURES:-0}" > "$SSH_TEST_DELETE_FAILURES_FILE"; fi
		read -r failures < "$SSH_TEST_DELETE_FAILURES_FILE"
		if [ "$failures" -gt 0 ]; then
			printf '%s\n' $((failures - 1)) > "$SSH_TEST_DELETE_FAILURES_FILE"
			echo 'Error from server (InternalError): transient delete failure' >&2
			exit 1
		fi
		printf '%s\n' missing > "$SSH_TEST_STATE"
		exit 0
	fi
	if [ ! -f "$SSH_TEST_DELETE_FAILURES_FILE" ]; then printf '%s\n' "${SSH_TEST_DELETE_FAILURES:-0}" > "$SSH_TEST_DELETE_FAILURES_FILE"; fi
	read -r failures < "$SSH_TEST_DELETE_FAILURES_FILE"
	if [ "$failures" -gt 0 ]; then
		printf '%s\n' $((failures - 1)) > "$SSH_TEST_DELETE_FAILURES_FILE"
		echo 'Error from server (InternalError): transient delete failure' >&2
		exit 1
	fi
	printf '%s\n' missing > "$SSH_TEST_STATE" ;;
annotate)
  if [ -z "$SSH_TEST_ANNOTATIONS" ]; then exit 1; fi
  printf '%s\n' "$*" >> "$SSH_TEST_ANNOTATIONS" ;;
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
		s := &session{sc: scenarioForTR(id), trs: trs}
		if err := s.Provision(t.Context(), rc); err != nil {
			t.Fatal(err)
		}
		if !sshpassReady(t.Context(), "/test/config") {
			t.Fatalf("%s started with a missing SSH helper", id)
		}
	}
	if got := strings.Count(commands(), "create -f -"); got != 3 {
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
			if strings.Count(got, "delete pod") != 1 || strings.Count(got, "create -f -") != 1 {
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
	firstCreate := strings.Index(got, "create -f -")
	if firstCreate < 0 || strings.Count(got[:firstCreate], "get pod") < 2 {
		t.Fatalf("created before waiting for terminating pod to disappear: %s", got)
	}
}

func TestSSHHelperLateCreationIsAlwaysCleanedUpOnTeardown(t *testing.T) {
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
	// A subsequent run reuses an externally created helper and still removes it.
	setState("Running")
	if err := s.Setup(t.Context(), rc, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.Teardown(t.Context(), rc, nil); err != nil {
		t.Fatal(err)
	}
	if got := commands(); strings.Count(got, "delete pod") != 2 {
		t.Fatalf("expected teardown to delete the helper on every run: %s", got)
	}
}

func TestSSHHelperTeardownRetriesTransientDeleteFailure(t *testing.T) {
	t.Setenv("SSH_TEST_DELETE_FAILURES", "1")
	_, commands := fakeSSHCluster(t, "Running")
	if err := (&sshPodSetup{}).Teardown(t.Context(), sshTestRunCtx(t), nil); err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(commands(), "delete pod"); got != 2 {
		t.Fatalf("expected one retry after transient delete failure, got %d attempts: %s", got, commands())
	}
}

func TestDeleteSSHPodUIDEnforcesPreconditionInFake(t *testing.T) {
	_, commands := fakeSSHCluster(t, "Pending")
	if err := deleteSSHPodUID(t.Context(), "", "ssh-test-pod-uid"); err != nil {
		t.Fatalf("matching UID precondition: %v\n%s", err, commands())
	}
	fakeSSHCluster(t, "Pending")
	err := deleteSSHPodUID(t.Context(), "", "other-uid")
	if err == nil {
		t.Fatal("want error when UID precondition does not match the pod")
	}
	if !errors.Is(err, errSSHPodUIDPreconditionFailed) {
		t.Fatalf("want UID precondition error, got: %v", err)
	}
}

func TestSSHHelperCreateSucceedsDespiteStderrWarning(t *testing.T) {
	t.Setenv("SSH_TEST_CREATE_STDERR", "Warning: admission webhook warning")
	_, commands := fakeSSHCluster(t, "missing")
	if err := (&sshPodSetup{}).Setup(t.Context(), sshTestRunCtx(t), nil); err != nil {
		t.Fatalf("create should succeed when stderr has warnings: %v", err)
	}
	if strings.Count(commands(), "create -f -") != 1 {
		t.Fatalf("expected one create: %s", commands())
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
	got := commands()
	if strings.Contains(got, "create -f -") {
		t.Fatalf("healthy pre-existing pod was recreated: %s", got)
	}
	if strings.Count(got, "delete pod") != 1 {
		t.Fatalf("healthy pre-existing pod was not cleaned up on teardown: %s", got)
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
	if got := commands(); strings.Contains(got, "create -f -") || strings.Contains(got, "delete") {
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

func TestAllReplayTRsMixedBatch(t *testing.T) {
	mixed := []core.TestRequirement{
		{Params: map[string]any{"results_dir": "/replay"}},
		{ID: "TR-VIRT-001"},
	}
	if allReplayTRs(mixed) {
		t.Fatal("mixed replay/live batch must not be treated as all replay")
	}
	if !allReplayTRs([]core.TestRequirement{{Params: map[string]any{"results_dir": "/replay"}}}) {
		t.Fatal("replay-only batch should be all replay")
	}
}

func TestSSHHelperTeardownWithMixedReplayAndLive(t *testing.T) {
	_, commands := fakeSSHCluster(t, "Running")
	rc := sshTestRunCtx(t)
	trs := []core.TestRequirement{
		{Params: map[string]any{"results_dir": t.TempDir()}},
		{Params: map[string]any{"kubeconfig": "/test/config"}},
	}
	if err := sharedSSHSetup.Teardown(t.Context(), rc, trs); err != nil {
		t.Fatal(err)
	}
	if strings.Count(commands(), "delete pod") != 1 {
		t.Fatalf("teardown must delete helper when batch is mixed replay/live: %s", commands())
	}
}

func TestSSHHelperFailedSetupRollsBackCreatedPod(t *testing.T) {
	t.Setenv("SSH_TEST_APPLY_STATE", "Pending")
	t.Setenv("SSH_TEST_POD_RUN_ID", "run-rollback")
	_, commands := fakeSSHCluster(t, "missing")
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	rc := sshTestRunCtx(t)
	rc.RunID = "run-rollback"
	if err := (&sshPodSetup{}).Setup(ctx, rc, nil); err == nil {
		t.Fatal("want setup failure while pod stays non-ready")
	}
	got := commands()
	if strings.Count(got, "create -f -") != 1 {
		t.Fatalf("expected one pod create, got: %s", got)
	}
	if strings.Count(got, "delete --raw") != 1 {
		t.Fatalf("expected rollback delete after failed setup, got: %s", got)
	}
}

func TestSSHHelperApplyTimeoutRollsBackPartiallyCreatedPod(t *testing.T) {
	t.Setenv("SSH_TEST_APPLY_STATE", "Pending")
	t.Setenv("SSH_TEST_APPLY_DELAY", "0.5")
	t.Setenv("SSH_TEST_POD_RUN_ID", "run-timeout")
	_, commands := fakeSSHCluster(t, "missing")
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	rc := sshTestRunCtx(t)
	rc.RunID = "run-timeout"
	err := (&sshPodSetup{}).Setup(ctx, rc, nil)
	if err == nil {
		t.Fatal("want setup failure when create times out")
	}
	if !errors.Is(err, context.DeadlineExceeded) && !strings.Contains(err.Error(), "context deadline exceeded") {
		t.Fatalf("want context deadline from timed-out create, got: %v", err)
	}
	got := commands()
	if strings.Count(got, "create -f -") != 1 || strings.Count(got, "delete --raw") < 1 {
		t.Fatalf("expected timed-out create to be rolled back: %s", got)
	}
	state, err := os.ReadFile(os.Getenv("SSH_TEST_STATE"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(state)) != "missing" {
		t.Fatalf("delayed apply recreated helper after rollback: %q", state)
	}
}

func TestSSHHelperFailedSetupDoesNotDeleteAnotherRunPod(t *testing.T) {
	t.Setenv("SSH_TEST_APPLY_STATE", "Pending")
	t.Setenv("SSH_TEST_POD_RUN_ID", "run-other")
	_, commands := fakeSSHCluster(t, "missing")
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	rc := sshTestRunCtx(t)
	rc.RunID = "run-this"
	if err := (&sshPodSetup{}).Setup(ctx, rc, nil); err == nil {
		t.Fatal("want setup failure while another run owns the helper pod")
	}
	got := commands()
	if strings.Count(got, "create -f -") != 1 {
		t.Fatalf("expected create before rollback, got: %s", got)
	}
	if strings.Count(got, "get pod") < 2 {
		t.Fatalf("expected rollback to inspect the helper pod, got: %s", got)
	}
	if strings.Count(got, "delete pod")+strings.Count(got, "delete --raw") != 0 {
		t.Fatalf("deleted another run's helper pod: %s", got)
	}
}

func TestSSHHelperAnnotatesReusedPod(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state")
	annotPath := filepath.Join(dir, "annotations")
	logPath := filepath.Join(dir, "commands")
	t.Setenv("SSH_TEST_STATE", statePath)
	t.Setenv("SSH_TEST_LOG", logPath)
	t.Setenv("SSH_TEST_ANNOTATIONS", annotPath)
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$SSH_TEST_LOG"
if [ "$1" = --kubeconfig ]; then shift 2; fi
read -r state < "$SSH_TEST_STATE"
case "$1" in
get) printf '{"status":{"phase":"Running","podIP":"10.0.0.2"}}\n' ;;
annotate)
  if [ -z "$SSH_TEST_ANNOTATIONS" ]; then exit 1; fi
  printf '%s\n' "$*" >> "$SSH_TEST_ANNOTATIONS" ;;
exec) exit 0 ;;
*) exit 1 ;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "kubectl"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statePath, []byte("Running\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	rc := &core.RunCtx{
		RunID:   "run-annotate-test",
		WorkDir: t.TempDir(),
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	trs := []core.TestRequirement{{Params: map[string]any{"kubeconfig": "/test/config"}}}
	if err := (&sshPodSetup{}).Setup(t.Context(), rc, trs); err != nil {
		t.Fatal(err)
	}
	annot, err := os.ReadFile(annotPath)
	if err != nil {
		t.Fatal(err)
	}
	got := string(annot)
	if !strings.Contains(got, "storage-cert-harness.redhat.com/run-id=run-annotate-test") {
		t.Fatalf("annotate missing run-id for reused pod: %s", got)
	}
	if !strings.Contains(got, "--overwrite") {
		t.Fatalf("annotate missing --overwrite: %s", got)
	}
}

func TestSSHHelperReplayDoesNotUseCluster(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	trs := []core.TestRequirement{{ID: "TR-VIRT-002", Params: map[string]any{"results_dir": t.TempDir()}}}
	rc := sshTestRunCtx(t)
	if err := (&sshPodSetup{}).Setup(t.Context(), rc, trs); err != nil {
		t.Fatal(err)
	}
	if err := (&session{sc: scenarioForTR(trs[0].ID), trs: trs}).Provision(t.Context(), rc); err != nil {
		t.Fatal(err)
	}
}

func TestSSHHelperManifestAvoidsTargetNodes(t *testing.T) {
	manifest := sshPodManifestForNodes(map[string]struct{}{
		"worker-05": {},
	}, nil)
	for _, want := range []string{
		"requiredDuringSchedulingIgnoredDuringExecution",
		"key: kubernetes.io/hostname",
		"operator: NotIn",
		`- "worker-05"`,
	} {
		if !strings.Contains(manifest, want) {
			t.Errorf("manifest missing %q:\n%s", want, manifest)
		}
	}
}

func TestSSHHelperManifestWithoutTargetKeepsDefaultScheduling(t *testing.T) {
	manifest := sshPodManifestForNodes(nil, nil)
	if strings.Contains(manifest, "nodeAffinity") {
		t.Fatalf("unexpected node affinity without a target node:\n%s", manifest)
	}
}

func TestSSHHelperManifestIncludesBootstrapAnnotations(t *testing.T) {
	rc := &core.RunCtx{RunID: "run-abc", PartKind: "initial"}
	manifest := sshPodManifestForNodes(nil, sshPodBootstrapAnnotations(rc))
	for _, want := range []string{
		"annotations:",
		"storage-cert-harness.redhat.com/run-id: \"run-abc\"",
		"storage-cert-harness.redhat.com/part-kind: \"initial\"",
		"storage-cert-harness.redhat.com/bootstrap-at:",
	} {
		if !strings.Contains(manifest, want) {
			t.Errorf("manifest missing %q:\n%s", want, manifest)
		}
	}
}

func TestLogExistingSSHPodAtStartupOrphanAndConcurrent(t *testing.T) {
	var log bytes.Buffer
	rc := &core.RunCtx{
		RunID:  "run-new",
		Logger: slog.New(slog.NewTextHandler(&log, &slog.HandlerOptions{Level: slog.LevelDebug})),
	}
	pod := &sshPodState{}
	pod.Metadata.Labels = map[string]string{"managed-by": "storage-cert-harness"}
	pod.Metadata.Annotations = map[string]string{
		"storage-cert-harness.redhat.com/run-id":         "run-old",
		"storage-cert-harness.redhat.com/bootstrap-host": "worker-1",
	}
	pod.Status.Phase = "Running"
	pod.Status.PodIP = "10.0.0.1"
	logExistingSSHPodAtStartup(rc, pod)
	got := log.String()
	if !strings.Contains(got, "orphan ssh helper pod found") {
		t.Fatalf("missing orphan log: %s", got)
	}
	if !strings.Contains(got, "when this harness run completes") {
		t.Fatalf("missing cleanup promise: %s", got)
	}
	if !strings.Contains(got, "another storage-cert-harness run may still be using") {
		t.Fatalf("missing concurrent harness warning: %s", got)
	}
	if !strings.Contains(got, "run-old") || !strings.Contains(got, "run-new") {
		t.Fatalf("missing run ids: %s", got)
	}
}

func TestLogExistingSSHPodAtStartupSameRunNoOrphan(t *testing.T) {
	var log bytes.Buffer
	rc := &core.RunCtx{
		RunID:  "run-same",
		Logger: slog.New(slog.NewTextHandler(&log, nil)),
	}
	pod := &sshPodState{}
	pod.Metadata.Labels = map[string]string{"managed-by": "storage-cert-harness"}
	pod.Metadata.Annotations = map[string]string{
		"storage-cert-harness.redhat.com/run-id": "run-same",
	}
	logExistingSSHPodAtStartup(rc, pod)
	got := log.String()
	if strings.Contains(got, "orphan") {
		t.Fatalf("same run should not be logged as orphan: %s", got)
	}
	if !strings.Contains(got, "already exists for this harness run") {
		t.Fatalf("missing reuse log: %s", got)
	}
}

func TestSSHHelperOnTargetNode(t *testing.T) {
	pod := &sshPodState{}
	pod.Spec.NodeName = "worker-05.example.test"
	if !sshPodOnNode(pod, map[string]struct{}{"worker-05.example.test": {}}) {
		t.Fatal("helper on target node was not detected")
	}
	if sshPodOnNode(pod, map[string]struct{}{"worker-06.example.test": {}}) {
		t.Fatal("helper on another node was incorrectly detected")
	}
}
