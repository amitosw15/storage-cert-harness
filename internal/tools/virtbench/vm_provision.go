package virtbench

import (
	"context"
	crand "crypto/rand"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"time"

	"github.com/openshift-storage-cert/storage-cert-harness/internal/core"
)

// provisionFailureRecoveryVMs creates vm_count VMs (one per namespace) on the
// target node using a required nodeSelector. The custom virtbench FAR mode
// removes that selector before fencing so recreated VMIs can land elsewhere.
// Namespaces follow the pattern {nsPrefix}-{i}, which virtbench picks up via
// --namespace-prefix and Teardown sweeps via listNamespacesByPrefix.
func provisionFailureRecoveryVMs(ctx context.Context, rc *core.RunCtx, bag *core.Bag, trs []core.TestRequirement) error {
	if len(trs) == 0 {
		return nil
	}
	tr := trs[0]
	kubeconfig, _ := core.GetAs[string](bag, "kubeconfig")

	node := strParamOr(tr, "node", "")
	hostnameLabel := strParamOr(tr, "node_hostname_label", "")
	if node == "" {
		picked, label, err := pickWorkerNode(ctx, kubeconfig)
		if err != nil {
			return err
		}
		node, hostnameLabel = picked, label
		rc.Logger.Info("virtbench: auto-selected worker node", "node", node, "hostname_label", hostnameLabel)
	} else if hostnameLabel == "" {
		if label, ok, _ := runKubectl(ctx, kubeconfig, "get", "node", node,
			`-o`, `jsonpath={.metadata.labels.kubernetes\.io/hostname}`); ok && strings.TrimSpace(label) != "" {
			hostnameLabel = strings.TrimSpace(label)
		} else {
			hostnameLabel = node
		}
	}
	bag.Set("picked_node", node)
	sc := storageClass(rc, tr)
	if sc == "" {
		return fmt.Errorf("virtbench: TR-VIRT-007: storage_class required (select a --backend or set the 'storage_class' param)")
	}

	nsPrefix := strParamOr(tr, "namespace_prefix", "failure-recovery")
	vmName := strParamOr(tr, "vm_name", defaultVMName)
	count := intParam(tr, "vm_count", 3)
	createTimeout := intParam(tr, "vm_create_timeout", 300)

	rc.Logger.Info("virtbench: creating VMs on target node", "node", node, "count", count, "storage_class", sc)

	for i := 1; i <= count; i++ {
		ns := nsPrefix + "-" + strconv.Itoa(i)
		if err := ensureNamespace(ctx, kubeconfig, ns); err != nil {
			return fmt.Errorf("virtbench: create namespace %s: %w", ns, err)
		}
		manifest := strings.NewReplacer(
			"{{VM_NAME}}", vmName,
			"{{NAMESPACE}}", ns,
			"{{NODE_NAME}}", node,
			"{{HOSTNAME_LABEL}}", hostnameLabel,
			"{{STORAGE_CLASS_NAME}}", sc,
		).Replace(failureRecoveryVMTemplate)
		if err := kubectlApply(ctx, kubeconfig, manifest); err != nil {
			return fmt.Errorf("virtbench: apply VM in %s: %w", ns, err)
		}
		rc.Logger.Info("virtbench: VM applied", "namespace", ns, "vm", vmName)
	}

	rc.Logger.Info("virtbench: waiting for VMs to reach Running", "count", count, "timeout_s", createTimeout)
	for i := 1; i <= count; i++ {
		ns := nsPrefix + "-" + strconv.Itoa(i)
		if err := waitForVMRunning(ctx, kubeconfig, ns, vmName, createTimeout); err != nil {
			return fmt.Errorf("virtbench: VM %s/%s not ready: %w", ns, vmName, err)
		}
		rc.Logger.Info("virtbench: VM Running", "namespace", ns, "vm", vmName)
	}

	return nil
}

func failureRecoveryValidate(tr core.TestRequirement) error {
	if count := intParam(tr, "vm_count", 3); count <= 0 {
		return fmt.Errorf("virtbench: %s: vm_count must be greater than zero", tr.ID)
	}
	// Default to an active failure trigger. A monitor-only run can otherwise
	// report already-Running VMs as recovered without observing a failure.
	mode := strParamOr(tr, "mode", "far-operator")
	if mode != "monitor" && mode != "far-operator" {
		return fmt.Errorf("virtbench: %s: unsupported failure-recovery mode %q", tr.ID, mode)
	}
	if mode == "monitor" && !boolParam(tr, "allow_monitor_mode") {
		return fmt.Errorf("virtbench: %s: monitor mode requires allow_monitor_mode=true and an externally triggered node failure", tr.ID)
	}
	if mode == "far-operator" && strParamOr(tr, "far_config", "") == "" {
		return fmt.Errorf("virtbench: %s: far_config is required for far-operator mode", tr.ID)
	}
	return nil
}

func boolParam(tr core.TestRequirement, key string) bool {
	if tr.Params == nil {
		return false
	}
	v, ok := core.AsBool(tr.Params[key])
	return ok && v
}

// pickWorkerNode returns a randomly chosen Ready worker node name from the
// cluster, plus its kubernetes.io/hostname label value (used for node affinity
// in the VM template — in OpenShift the node name can be an FQDN while the
// label is the short hostname, so they must be fetched separately).
func pickWorkerNode(ctx context.Context, kubeconfig string) (name, hostnameLabel string, err error) {
	out, ok, err := runKubectl(ctx, kubeconfig, "get", "nodes",
		"-l", "node-role.kubernetes.io/worker",
		"-o", "jsonpath={.items[*].metadata.name}")
	if !ok || err != nil {
		return "", "", fmt.Errorf("virtbench: list worker nodes: %w", err)
	}
	allNodes := strings.Fields(strings.TrimSpace(out))
	if len(allNodes) == 0 {
		return "", "", fmt.Errorf("virtbench: no worker nodes found (label node-role.kubernetes.io/worker)")
	}
	var ready []string
	for _, n := range allNodes {
		cond, condOK, _ := runKubectl(ctx, kubeconfig, "get", "node", n,
			"-o", `jsonpath={.status.conditions[?(@.type=="Ready")].status}`)
		if !condOK || strings.TrimSpace(cond) != "True" {
			continue
		}
		sched, _, _ := runKubectl(ctx, kubeconfig, "get", "node", n,
			"-o", `jsonpath={.metadata.labels.kubevirt\.io/schedulable}`)
		if strings.TrimSpace(sched) != "true" {
			continue
		}
		ready = append(ready, n)
	}
	if len(ready) == 0 {
		return "", "", fmt.Errorf("virtbench: no Ready+schedulable worker nodes found (all %d workers are not Ready or kubevirt.io/schedulable != true)", len(allNodes))
	}
	index, err := crand.Int(crand.Reader, big.NewInt(int64(len(ready))))
	if err != nil {
		return "", "", fmt.Errorf("virtbench: choose worker node: %w", err)
	}
	name = ready[index.Int64()]
	label, labelOK, _ := runKubectl(ctx, kubeconfig, "get", "node", name,
		`-o`, `jsonpath={.metadata.labels.kubernetes\.io/hostname}`)
	if labelOK && strings.TrimSpace(label) != "" {
		hostnameLabel = strings.TrimSpace(label)
	} else {
		hostnameLabel = name
	}
	return name, hostnameLabel, nil
}

// waitForVMRunning polls until the VMI reaches phase Running or timeoutSec elapses.
func waitForVMRunning(ctx context.Context, kubeconfig, ns, vmName string, timeoutSec int) error {
	deadline := time.Now().Add(time.Duration(timeoutSec) * time.Second)
	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			return err
		}
		out, ok, _ := runKubectl(ctx, kubeconfig, "get", "vmi", vmName, "-n", ns,
			"-o", "jsonpath={.status.phase}")
		if ok && strings.TrimSpace(out) == "Running" {
			return nil
		}
		time.Sleep(10 * time.Second)
	}
	return fmt.Errorf("VMI %s/%s not Running after %ds", ns, vmName, timeoutSec)
}

// ensureNamespace creates a namespace if it does not already exist.
func ensureNamespace(ctx context.Context, kubeconfig, ns string) error {
	return kubectlApply(ctx, kubeconfig, "apiVersion: v1\nkind: Namespace\nmetadata:\n  name: "+ns+"\n")
}
