// Package virtbench
// The node-drain / VM-evacuation scenario (TR-VIRT-008) rides the generic engine
// (engine.go) like every other virtbench scenario, but needs three of the
// engine's optional Scenario hooks because a drain is not a single self-cleaning
// CLI call: a Provision hook clones the VMs onto the target first, the Run
// tolerates drain-nodes' non-zero exit on a timeout, and a Teardown hook uncordons
// the workers and removes the VMs (the clone omits --cleanup so they survive the
// drain). Grading reads virtbench's own drain log (ParseDrain, parse.go): the
// per-node duration and the VMI distribution printed before/after the drain — no
// harness-side cluster snapshot. See decisions/0007, 0008, 0013.
package virtbench

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/openshift-storage-cert/storage-cert-harness/internal/core"
	"github.com/openshift-storage-cert/storage-cert-harness/internal/safefs"
)

//go:embed templates/rhel9-vm-drain.yaml
var drainVMTemplate string

// renderDrainVMTemplate injects the drain target node into the embedded
// template's soft nodeAffinity. virtbench substitutes {{STORAGE_CLASS_NAME}}
// itself, but never sees the target node, so the harness fills it in here.
func renderDrainVMTemplate(target string) string {
	return strings.ReplaceAll(drainVMTemplate, "{{TARGET_NODE}}", target)
}

const (
	drainNSPrefix       = "virtbench-drain"
	drainTemplateFile   = "rhel9-vm-drain.yaml"
	drainDefaultCount   = 10  // default VM count (a plan input)
	drainDefaultTimeout = 600 // drain-nodes --timeout default (seconds)
)

// --- Provision hook ----------------------------------------------------------

// drainProvision clones the VMs to be drained onto the target node. It renders
// the LiveMigrate + ReadWriteMany template with the target's soft nodeAffinity and
// runs datasource-clone WITHOUT --cleanup, so the VMs survive the drain that
// follows (Teardown removes them). Runs before Run, never in replay.
func drainProvision(ctx context.Context, rc *core.RunCtx, _ *session, tr core.TestRequirement, resultsRoot string) error {
	target := strParamOr(tr, "target_node", "")
	if target == "" {
		return fmt.Errorf("virtbench: drain: target_node param is required (the worker to drain)")
	}
	nsPrefix := strParamOr(tr, "namespace_prefix", drainNSPrefix)

	tmplPath := filepath.Join(resultsRoot, drainTemplateFile)
	if custom := strParamOr(tr, "vm_template", ""); custom != "" {
		tmplPath = custom
	} else if err := safefs.WriteFile(tmplPath, []byte(renderDrainVMTemplate(target)), 0o600); err != nil {
		return fmt.Errorf("virtbench: drain: write vm template: %w", err)
	}

	args, err := drainCloneArgs(rc, tr, resultsRoot, tmplPath, nsPrefix)
	if err != nil {
		return err
	}
	rc.Logger.Info("virtbench: drain: cloning VMs onto target", "node", target, "args", args)
	if err := execVirtbench(ctx, rc, resultsRoot, args); err != nil {
		return fmt.Errorf("virtbench: drain: clone VMs: %w", err)
	}
	return nil
}

// --- Teardown hook -----------------------------------------------------------

// drainTeardown uncordons every worker (idempotent; drain-nodes only self-
// uncordons on success, so a timeout would otherwise leave the target cordoned).
// The generic engine teardown runs this on a fresh bounded context and then
// sweeps the namespaces the clone created (no --cleanup was passed), so cleanup
// still happens when the run was cancelled. Best-effort: it never fails the run.
func drainTeardown(ctx context.Context, rc *core.RunCtx, s *session) error {
	kc := s.kubeconfig
	_, all, err := listWorkers(ctx, kc)
	if err != nil {
		rc.Logger.Warn("virtbench: drain teardown could not list workers to uncordon", "err", err)
		return nil
	}
	for _, w := range all {
		if err := uncordonNode(ctx, kc, w); err != nil {
			rc.Logger.Warn("virtbench: drain teardown uncordon failed", "node", w, "err", err)
		}
	}
	return nil
}

// --- arg builders ------------------------------------------------------------

// drainArgs is the Scenario.BuildArgs for the drain: it runs `vm-ops drain-nodes`
// on the required target_node. --ignore-daemonsets is mandatory (every OpenShift
// node runs DaemonSet pods, and without it `kubectl drain` aborts before evicting
// the virt-launcher pods — no eviction, no live-migration); --force keeps a stray
// unmanaged pod from aborting the drain; --log-file pins the log where the
// collector looks. Reads only params, never a threshold.
func drainArgs(_ *core.RunCtx, _ *session, tr core.TestRequirement, resultsRoot string) ([]string, error) {
	target := strParamOr(tr, "target_node", "")
	if target == "" {
		return nil, fmt.Errorf("virtbench: %s: target_node param is required (the worker to drain)", tr.ID)
	}
	args := clusterArgs(tr)
	args = append(args, "vm-ops", "drain-nodes",
		"--nodes", target,
		"--timeout", strconv.Itoa(intParam(tr, "timeout", drainDefaultTimeout)),
		"--ignore-daemonsets",
		"--force",
		"--log-file", filepath.Join(resultsRoot, DrainFileName),
	)
	return args, nil
}

// drainCloneArgs builds the datasource-clone CLI that seeds the VMs to be
// drained. It deliberately omits --cleanup (the VMs must survive the drain;
// Teardown removes them) and always passes the LiveMigrate+RWX template. Like the
// other builders it reads only params/backend, never a threshold.
func drainCloneArgs(rc *core.RunCtx, tr core.TestRequirement, resultsRoot, template, nsPrefix string) ([]string, error) {
	sc := storageClass(rc, tr)
	if sc == "" {
		return nil, fmt.Errorf("virtbench: %s: storage_class required (backend or 'storage_class' param)", tr.ID)
	}
	start := intParam(tr, "start", 1)
	end := intParam(tr, "end", intParam(tr, "count", drainDefaultCount))

	args := clusterArgs(tr)
	args = append(args, "datasource-clone",
		"--start", strconv.Itoa(start),
		"--end", strconv.Itoa(end),
		"--vm-name", defaultVMName,
		"--namespace-prefix", nsPrefix,
		"--storage-class", sc,
		"--vm-template", template,
		"--save-results",
		"--results-folder", resultsRoot,
	)
	if driver := strParamOr(tr, "storage_driver", ""); driver != "" {
		args = append(args, "--storage-driver", driver)
	}
	return args, nil
}

// --- kubectl helpers ---------------------------------------------------------

type nodeList struct {
	Items []struct {
		Metadata struct {
			Name   string            `json:"name"`
			Labels map[string]string `json:"labels"`
		} `json:"metadata"`
		Spec struct {
			Unschedulable bool `json:"unschedulable"`
		} `json:"spec"`
	} `json:"items"`
}

// listWorkers returns the worker nodes' names: schedulable (not cordoned) first,
// then all workers. A worker carries the worker role label and neither
// control-plane nor master role. Both slices are sorted for deterministic
// target selection.
func listWorkers(ctx context.Context, kubeconfig string) (schedulable, all []string, err error) {
	out, ok, err := runKubectl(ctx, kubeconfig, "get", "nodes", "-o", "json")
	if err != nil {
		return nil, nil, err
	}
	if !ok {
		return nil, nil, fmt.Errorf("kubectl get nodes failed: %s", strings.TrimSpace(out))
	}
	var nl nodeList
	if err := json.Unmarshal([]byte(out), &nl); err != nil {
		return nil, nil, fmt.Errorf("parse nodes: %w", err)
	}
	for _, n := range nl.Items {
		_, isWorker := n.Metadata.Labels["node-role.kubernetes.io/worker"]
		_, isCP := n.Metadata.Labels["node-role.kubernetes.io/control-plane"]
		_, isMaster := n.Metadata.Labels["node-role.kubernetes.io/master"]
		if !isWorker || isCP || isMaster {
			continue
		}
		all = append(all, n.Metadata.Name)
		if !n.Spec.Unschedulable {
			schedulable = append(schedulable, n.Metadata.Name)
		}
	}
	sort.Strings(schedulable)
	sort.Strings(all)
	return schedulable, all, nil
}

func uncordonNode(ctx context.Context, kubeconfig, node string) error {
	out, ok, err := runKubectl(ctx, kubeconfig, "uncordon", node)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("kubectl uncordon %s: %s", node, strings.TrimSpace(out))
	}
	return nil
}
