package virtbench

import (
	"context"
	crand "crypto/rand"
	"fmt"
	"math/big"
	"strconv"
	"time"

	"github.com/openshift-storage-cert/storage-cert-harness/internal/clustercheck"
	"github.com/openshift-storage-cert/storage-cert-harness/internal/core"
	"github.com/openshift-storage-cert/storage-cert-harness/internal/manifest"
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
		if obj, err := clustercheck.GetNode(ctx, kubeconfig, node); err == nil && obj.Metadata.Labels["kubernetes.io/hostname"] != "" {
			hostnameLabel = obj.Metadata.Labels["kubernetes.io/hostname"]
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
	start, count, err := recoveryVMRange(tr)
	if err != nil {
		return err
	}
	createTimeout := intParam(tr, "vm_create_timeout", 300)

	rc.Logger.Info("virtbench: creating VMs on target node", "node", node, "count", count, "storage_class", sc)

	source, _, err := vmSourceTemplate(ctx, tr)
	if err != nil {
		return err
	}

	values, err := resolvePlanTemplateValues("datasource-clone", rc, tr)
	if err != nil {
		return err
	}
	values["NODE_NAME"] = node
	values["HOSTNAME_LABEL"] = hostnameLabel

	for i := start; i <= count; i++ {
		ns := nsPrefix + "-" + strconv.Itoa(i)
		values["NAMESPACE"] = ns
		rendered, err := renderVMTemplate("failure-recovery.yaml", source, values)
		if err != nil {
			return err
		}

		if err := ensureNamespace(ctx, kubeconfig, ns); err != nil {
			return fmt.Errorf("virtbench: create namespace %s: %w", ns, err)
		}

		if err := clustercheck.ApplyVirtualMachine(ctx, kubeconfig, rendered); err != nil {
			return fmt.Errorf("virtbench: apply VM in %s: %w", ns, err)
		}
		rc.Logger.Info("virtbench: VM applied", "namespace", ns, "vm", vmName)
	}

	rc.Logger.Info("virtbench: waiting for VMs to reach Running", "count", count, "timeout_s", createTimeout)
	for i := start; i <= count; i++ {
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
	nodes, err := clustercheck.ListNodes(ctx, kubeconfig)
	if err != nil {
		return "", "", fmt.Errorf("virtbench: list worker nodes: %w", err)
	}
	var allNodes []string
	for _, node := range nodes {
		if _, isWorker := node.Metadata.Labels["node-role.kubernetes.io/worker"]; isWorker {
			allNodes = append(allNodes, node.Metadata.Name)
		}
	}
	if len(allNodes) == 0 {
		return "", "", fmt.Errorf("virtbench: no worker nodes found (label node-role.kubernetes.io/worker)")
	}
	var ready []string
	for _, n := range nodes {
		if _, isWorker := n.Metadata.Labels["node-role.kubernetes.io/worker"]; !isWorker {
			continue
		}
		isReady := false
		for _, condition := range n.Status.Conditions {
			if condition.Type == "Ready" && condition.Status == "True" {
				isReady = true
				break
			}
		}
		if !isReady || n.Metadata.Labels["kubevirt.io/schedulable"] != "true" {
			continue
		}
		ready = append(ready, n.Metadata.Name)
	}
	if len(ready) == 0 {
		return "", "", fmt.Errorf("virtbench: no Ready+schedulable worker nodes found (all %d workers are not Ready or kubevirt.io/schedulable != true)", len(allNodes))
	}
	index, err := crand.Int(crand.Reader, big.NewInt(int64(len(ready))))
	if err != nil {
		return "", "", fmt.Errorf("virtbench: choose worker node: %w", err)
	}
	name = ready[index.Int64()]
	for _, node := range nodes {
		if node.Metadata.Name == name && node.Metadata.Labels["kubernetes.io/hostname"] != "" {
			hostnameLabel = node.Metadata.Labels["kubernetes.io/hostname"]
			break
		}
	}
	if hostnameLabel == "" {
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
		vmi, err := clustercheck.GetVMI(ctx, kubeconfig, clustercheck.NamespacedName{Name: vmName, Namespace: ns})
		if err == nil && vmi.Status.Phase == "Running" {
			return nil
		}
		time.Sleep(10 * time.Second)
	}
	return fmt.Errorf("VMI %s/%s not Running after %ds", ns, vmName, timeoutSec)
}

// ensureNamespace creates a namespace if it does not already exist.
func ensureNamespace(ctx context.Context, kubeconfig, ns string) error {
	templateSource, err := templates.ReadFile(namespaceTemplatePath)
	if err != nil {
		return err
	}
	rendered, err := manifest.Render(vmRenderer, "namespace.yaml", templateSource, map[string]any{"NAMESPACE": ns})
	if err != nil {
		return err
	}
	_, err = clustercheck.EnsureNamespace(ctx, kubeconfig, ns, rendered)
	return err
}
