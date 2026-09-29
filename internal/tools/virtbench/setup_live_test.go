//go:build integration

package virtbench

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"gitlab.cee.redhat.com/eco-special-projects/storage-cert-harness/internal/core"
)

// TestSSHHelperAfterNodeDrain runs real virtbench stages in the failure order,
// sharing one setup across drain, clone-at-scale, and boot-storm. The scheduler
// does not guarantee job order, so this test drives the registered stages in a
// fixed order. It drains the worker hosting the helper and removes test VMs.
// Opt in on a disposable test cluster with VIRTBENCH_LIVE_STORAGE_CLASS and
// VIRTBENCH_LIVE_WORKDIR; run with -tags=integration -timeout=45m -count=3.
func TestSSHHelperAfterNodeDrain(t *testing.T) {
	storage := os.Getenv("VIRTBENCH_LIVE_STORAGE_CLASS")
	work := os.Getenv("VIRTBENCH_LIVE_WORKDIR")
	if storage == "" || work == "" {
		t.Skip("requires explicit live storage class and work directory; drains a worker")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Minute)
	defer cancel()
	root, err := os.MkdirTemp(work, "ssh-recovery-")
	if err != nil {
		t.Fatal(err)
	}
	rc := &core.RunCtx{RunID: filepath.Base(root), WorkDir: root, Logger: slog.Default()}
	t.Logf("artifacts: %s", root)
	trs := []core.TestRequirement{
		{ID: "TR-VIRT-008", Params: map[string]any{"storage_class": storage, "start": 1, "end": 1, "namespace_prefix": "ssh-recovery-drain"}},
		{ID: "TR-VIRT-018", Params: map[string]any{"storage_class": storage, "iteration_clones": 1, "namespace_prefix": "ssh-recovery-clone"}},
		{ID: "TR-VIRT-001", Params: map[string]any{"storage_class": storage, "num_vms": 1, "namespace_prefix": "ssh-recovery-boot"}},
	}
	setup := sharedSSHSetup
	if err := setup.Setup(ctx, rc, trs); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), teardownBudget)
		defer cleanupCancel()
		if err := setup.Teardown(cleanupCtx, rc, trs); err != nil {
			t.Errorf("shared teardown: %v", err)
		}
	})
	node, ok, err := runKubectl(ctx, "", "get", "pod", sshPodName, "-n", sshPodNS, "-o", "jsonpath={.spec.nodeName}")
	if err != nil || !ok {
		t.Fatalf("get helper node: %v: %s", err, node)
	}
	workers, all, err := listWorkers(ctx, "")
	if err != nil || len(workers) < 2 || len(workers) != len(all) || !slices.Contains(workers, node) {
		t.Fatalf("requires >=2 uncordoned workers and helper on a worker: node=%s workers=%v all=%v err=%v", node, workers, all, err)
	}
	trs[0].Params["target_node"] = node
	t.Logf("draining helper's worker: %s", node)
	for _, tr := range trs {
		t.Run(tr.ID, func(t *testing.T) {
			var sc Scenario
			for _, candidate := range scenarios {
				if candidate.ProvidesTR == tr.ID {
					sc = candidate
				}
			}
			jobRC := *rc
			jobRC.WorkDir = filepath.Join(root, tr.ID)
			bag := core.NewBag()
			defer func() { _ = (teardown{sc}).Teardown(ctx, &jobRC, bag) }()
			if err := (provisioner{sc}).Provision(ctx, &jobRC, bag, []core.TestRequirement{tr}); err != nil {
				t.Fatal(err)
			}
			handle, err := (runner{sc}).Run(ctx, &jobRC, bag, []core.TestRequirement{tr})
			if err != nil {
				t.Fatal(err)
			}
			bundle, err := (collector{sc}).Collect(ctx, &jobRC, bag, handle)
			if err != nil {
				t.Fatal(err)
			}
			results, err := sc.Parse(bundle.Data[sc.ResultFile], tr.ID)
			if err != nil || len(results) == 0 {
				t.Fatalf("parse results: %v", err)
			}
			for _, result := range results {
				t.Logf("%s native=%s metrics=%+v checks=%+v", tr.ID, result.Native, result.Metrics, result.Checks)
				if result.Native != core.OutcomePass {
					t.Errorf("%s native outcome: %s", tr.ID, result.Native)
				}
			}
		})
		if tr.ID == "TR-VIRT-008" {
			out, ok, err := runKubectl(ctx, "", "get", "pod", sshPodName, "-n", sshPodNS, "--ignore-not-found", "-o", "name")
			if err != nil || !ok || strings.TrimSpace(out) != "" {
				t.Fatalf("drain must remove the helper to exercise recovery: ok=%v err=%v output=%s", ok, err, out)
			}
			t.Log("confirmed: drain removed shared SSH helper before TR-VIRT-018 and TR-VIRT-001")
		}
	}
}
