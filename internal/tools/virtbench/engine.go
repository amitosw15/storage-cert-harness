// Package virtbench integrates portworx/kubevirt-benchmark ("virtbench") as a
// tool. virtbench ships no upstream container image, so phase 1 runs the released
// CLI locally via os/exec; an in-cluster Job model is a documented follow-up
// (decisions/0007).
//
// virtbench exposes several scenarios (datasource-clone, its --boot-storm
// variant, disk-ops, …), each a distinct KB automation_tool and a distinct TR.
// They share the lifecycle plumbing and differ in their Scenario configuration.
//
// Bag contract (see decisions/0003):
//   - provisioner writes "tr_id" (string), "replay" (bool), "results_root" (string)
//   - runner      reads  "replay", "results_root"; execs virtbench unless replay
//   - collector   reads  "results_root"; finds + reads the scenario's result file
//   - parser      reads  "tr_id"
//
// Replay mode: a TR param "results_dir" points at a directory of already-collected
// virtbench results. Preflight skips binary/cluster checks, the runner skips exec,
// and the harness grades the pre-collected file — enabling offline e2e and
// re-grading without a cluster.
package virtbench

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/openshift-storage-cert/storage-cert-harness/internal/core"
	"github.com/openshift-storage-cert/storage-cert-harness/internal/stages"
)

const (
	// binary is the virtbench CLI, expected on PATH (installed per
	// https://portworx.github.io/kubevirt-benchmark/install/).
	binary = "virtbench"

	// defaultVMName is the VM metadata.name in virtbench's bundled datasource-clone
	// template (examples/vm-templates/rhel9-vm-datasource.yaml). datasource-clone does
	// NOT substitute --vm-name into that template; it only polls the VM by that name.
	// So --vm-name MUST equal the template's name or the run hangs forever waiting for
	// a VM that never exists. Override vm_name only together with a matching custom
	// --vm-template. See decisions/0008.
	defaultVMName = "rhel-9-vm"
)

// --- shared arg helpers ------------------------------------------------------

// datasourceCloneArgs builds the CLI for the datasource-clone family. The
// boot-storm scenario is the same subcommand plus the --boot-storm flag; it reads
// ONLY params/backend config — never a threshold/SLA value — so nothing sensitive
// can reach a command line. resultsRoot is where virtbench must write results so
// the collector can find them.
func datasourceCloneArgs(bootStorm bool, nsPrefix string) func(rc *core.RunCtx, _ *core.Bag, tr core.TestRequirement, resultsRoot string) ([]string, error) {
	return func(rc *core.RunCtx, _ *core.Bag, tr core.TestRequirement, resultsRoot string) ([]string, error) {
		sc := storageClass(rc, tr)
		if sc == "" {
			return nil, fmt.Errorf("virtbench: %s: storage_class required (backend or 'storage_class' param)", tr.ID)
		}
		start, end, err := cloneVMRange(tr)
		if err != nil {
			return nil, err
		}

		args := clusterArgs(tr)
		args = append(args, "datasource-clone",
			"--start", strconv.Itoa(start),
			"--end", strconv.Itoa(end),
			"--vm-name", strParamOr(tr, "vm_name", defaultVMName),
			"--namespace-prefix", strParamOr(tr, "namespace_prefix", nsPrefix),
			"--storage-class", sc,
			"--save-results",
			"--results-folder", resultsRoot,
		)
		if tmpl := strParamOr(tr, "vm_template", ""); tmpl != "" {
			args = append(args, "--vm-template", tmpl)
		}
		if driver := strParamOr(tr, "storage_driver", ""); driver != "" {
			args = append(args, "--storage-driver", driver)
		}
		if batch := intParam(tr, "namespace_batch_size", 0); batch > 0 {
			args = append(args, "--namespace-batch-size", strconv.Itoa(batch))
		}
		if disks := intParam(tr, "num_disks", 0); disks > 0 {
			args = append(args, "--num-disks", strconv.Itoa(disks))
		}
		if bootStorm {
			args = append(args, "--boot-storm")
		}
		// Let virtbench remove the namespaces/VMs it created, on success and on its
		// own failures; --yes skips the interactive confirmation. The adapter's
		// Teardown is only a safety net for a killed-mid-run process.
		args = append(args, "--cleanup", "--cleanup-on-failure", "--yes")
		return args, nil
	}
}

// diskOpsArgs builds the disk-ops CLI with explicit manifest inputs.
func diskOpsArgs(nsPrefix string) func(rc *core.RunCtx, _ *core.Bag, tr core.TestRequirement, resultsRoot string) ([]string, error) {
	return func(rc *core.RunCtx, _ *core.Bag, tr core.TestRequirement, resultsRoot string) ([]string, error) {
		sc := storageClass(rc, tr)
		if sc == "" {
			return nil, fmt.Errorf("virtbench: %s: storage_class required (backend or 'storage_class' param)", tr.ID)
		}
		start, end, err := diskOpsVMRange(tr)
		if err != nil {
			return nil, err
		}

		args := clusterArgs(tr)
		args = append(args, "disk-ops",
			"--vm-name", strParamOr(tr, "vm_name", "disk-ops-vm"),
			"--start", strconv.Itoa(start),
			"--end", strconv.Itoa(end),
			"--namespace-prefix", strParamOr(tr, "namespace_prefix", nsPrefix),
			"--storage-class", sc,
			"--operation", strParamOr(tr, "operation", "hotplug"),
			"--save-results",
			"--results-dir", resultsRoot,
		)
		if tmpl := strParamOr(tr, "vm_template", ""); tmpl != "" {
			args = append(args, "--vm-template", tmpl)
		}
		if disks := intParam(tr, "disks", 0); disks > 0 {
			args = append(args, "--disks", strconv.Itoa(disks))
		}
		// disk-ops needs --create-vms to provision the VMs it will hotplug into, and
		// --cleanup to remove them afterwards.
		args = append(args, "--create-vms", "--cleanup")
		return args, nil
	}
}

const defaultFIORuntime = 600

const (
	defaultFIOVMReadyTimeout   = 600
	defaultFIOCollectRetries   = 8
	defaultFIOCollectRetryWait = 20
	defaultFIOVMName           = "fio-vm"
)

const (
	fioActionDeploy        = "deploy"
	fioActionStatus        = "status"
	fioActionGatherResults = "gather-results"
	fioActionCleanup       = "cleanup"
	fioActionRunAll        = "run-all"
)

// fioArgs builds the fixed 4 KiB random read/write profile. The plan's start/end
// range selects one VM or a concurrent fleet; the parser grades the worst p99.
func fioArgs(nsPrefix string) func(rc *core.RunCtx, _ *core.Bag, tr core.TestRequirement, resultsRoot string) ([]string, error) {
	return func(rc *core.RunCtx, _ *core.Bag, tr core.TestRequirement, resultsRoot string) ([]string, error) {
		sc := storageClass(rc, tr)
		if sc == "" {
			return nil, fmt.Errorf("virtbench: %s: storage_class required (backend or 'storage_class' param)", tr.ID)
		}
		start, end, err := fioVMRange(tr)
		if err != nil {
			return nil, fmt.Errorf("virtbench: %s: %w", tr.ID, err)
		}
		if _, err := fioVMReadyTimeout(tr); err != nil {
			return nil, fmt.Errorf("virtbench: %s: %w", tr.ID, err)
		}
		retries, delay, err := fioCollectionSettings(tr)
		if err != nil {
			return nil, fmt.Errorf("virtbench: %s: %w", tr.ID, err)
		}
		if _, err := fioProfile(tr); err != nil {
			return nil, fmt.Errorf("virtbench: %s: %w", tr.ID, err)
		}
		if rw := strParamOr(tr, "fio_rw", "randrw"); rw != "randrw" {
			return nil, fmt.Errorf("virtbench: %s: fio_rw must be randrw for read/write latency", tr.ID)
		}
		bs := strParamOr(tr, "fio_bs", "")
		if bs != "4k" {
			return nil, fmt.Errorf("virtbench: %s: fio_bs must be 4k for read/write latency", tr.ID)
		}
		template, err := fioTemplate(tr)
		if err != nil {
			return nil, err
		}

		args := clusterArgs(tr)
		args = append(args, "fio",
			"--action", fioActionRunAll,
			"--vm-name", strParamOr(tr, "vm_name", defaultFIOVMName),
			"--start", strconv.Itoa(start),
			"--end", strconv.Itoa(end),
			"--namespace-prefix", strParamOr(tr, "namespace_prefix", nsPrefix),
			"--storage-class", sc,
			"--fio-rw", "randrw",
			"--fio-bs", bs,
			"--save-results",
			"--results-dir", resultsRoot,
			"--collect-retries", strconv.Itoa(retries),
			"--collect-retry-delay", strconv.Itoa(delay),
		)
		if template != "" {
			args = append(args, "--vm-template", template)
		}
		runtime := intParam(tr, "fio_runtime", defaultFIORuntime)
		if runtime < 1 {
			return nil, fmt.Errorf("virtbench: %s: fio_runtime must be >= 1", tr.ID)
		}
		args = append(args, "--fio-runtime", strconv.Itoa(runtime))
		for _, key := range []string{"fio_iodepth", "fio_numjobs"} {
			if value, present := tr.Params[key]; present {
				args = append(args, "--"+strings.ReplaceAll(key, "_", "-"), fmt.Sprint(value))
			}
		}
		if size := strParamOr(tr, "fio_size", ""); size != "" {
			args = append(args, "--fio-size", size)
		}
		if n := intParam(tr, "concurrency", 0); n > 0 {
			args = append(args, "--concurrency", strconv.Itoa(n))
		}
		if driver := strParamOr(tr, "storage_driver", ""); driver != "" {
			args = append(args, "--storage-driver", driver)
		}
		if _, err := resolveTemplateValues("fio", args); err != nil {
			return nil, err
		}
		return args, nil
	}
}

// fioVMRange converts KB num_vms to Virtbench's inclusive VM index range.
func fioVMRange(tr core.TestRequirement) (start, end int, err error) {
	count := intParam(tr, "num_vms", 0)
	if count < 1 {
		return 0, 0, fmt.Errorf("num_vms must be >= 1")
	}
	return 1, count, nil
}

func fioProfile(tr core.TestRequirement) (string, error) {
	profile := strParamOr(tr, "profile", "")
	switch profile {
	case "", "load-80":
		return profile, nil
	default:
		return "", fmt.Errorf("profile must be load-80 when set")
	}
}

// runFIO delegates FIO execution and collection to Virtbench while retaining harness cleanup and grading.
func runFIO(ctx context.Context, rc *core.RunCtx, bag *core.Bag, trs []core.TestRequirement) (stages.RunHandle, error) {
	if len(trs) == 0 {
		return stages.RunHandle{}, fmt.Errorf("virtbench: no FIO test requirement")
	}
	root, _ := core.GetAs[string](bag, "results_root")
	if root == "" {
		return stages.RunHandle{}, fmt.Errorf("virtbench: no results_root in bag")
	}
	tr := trs[0]
	runArgs, err := fioArgs("fio-latency")(rc, nil, tr, root)
	if err != nil {
		return stages.RunHandle{}, err
	}
	cleanupArgs, err := fioActionArgs(runArgs, fioActionCleanup)
	if err != nil {
		return stages.RunHandle{}, fmt.Errorf("virtbench: %s: %w", tr.ID, err)
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Minute)
		defer cancel()
		if err := runVirtbench(cleanupCtx, rc, root, cleanupArgs, tr); err != nil {
			rc.Logger.Warn("virtbench fio: cleanup failed; teardown will retry", "err", err)
		}
	}()

	if err := runVirtbench(ctx, rc, root, runArgs, tr); err != nil {
		return stages.RunHandle{}, fmt.Errorf("virtbench fio: run-all: %w", err)
	}
	return stages.RunHandle{ID: "virtbench-" + rc.RunID}, nil
}

func fioActionArgs(base []string, action string) ([]string, error) {
	switch action {
	case fioActionDeploy, fioActionStatus, fioActionGatherResults, fioActionCleanup, fioActionRunAll:
	default:
		return nil, fmt.Errorf("unsupported FIO action %q", action)
	}
	args := make([]string, 0, len(base)+4)
	for i := 0; i < len(base); i++ {
		if base[i] == "--cleanup" {
			continue
		}
		if base[i] == "--action" && i+1 < len(base) {
			args = append(args, "--action", action)
			i++
			continue
		}
		args = append(args, base[i])
	}
	return args, nil
}

func fioVMReadyTimeout(tr core.TestRequirement) (time.Duration, error) {
	seconds := intParam(tr, "vm_ready_timeout", defaultFIOVMReadyTimeout)
	if seconds < 1 {
		return 0, fmt.Errorf("vm_ready_timeout must be >= 1")
	}
	return time.Duration(seconds) * time.Second, nil
}

func fioCollectionSettings(tr core.TestRequirement) (retries, delay int, err error) {
	delay = intParam(tr, "fio_collect_retry_delay", defaultFIOCollectRetryWait)
	if delay < 1 {
		return 0, 0, fmt.Errorf("fio_collect_retry_delay must be >= 1")
	}
	runtime := intParam(tr, "fio_runtime", defaultFIORuntime)
	retries = intParam(tr, "fio_collect_retries", 0)
	if retries == 0 {
		retries = (runtime+delay-1)/delay + 2
		if retries < defaultFIOCollectRetries {
			retries = defaultFIOCollectRetries
		}
	}
	if retries < 1 {
		return 0, 0, fmt.Errorf("fio_collect_retries must be >= 1")
	}
	return retries, delay, nil
}

func fioValidate(tr core.TestRequirement) error {
	if _, _, err := fioVMRange(tr); err != nil {
		return err
	}
	if _, err := fioProfile(tr); err != nil {
		return err
	}
	_, err := fioTemplate(tr)
	if err != nil {
		return err
	}
	return nil
}

func fioTemplate(tr core.TestRequirement) (string, error) {
	template := strParamOr(tr, "fio_vm_template", "")
	if template == "" {
		return "", nil
	}
	path, err := filepath.Abs(template)
	if err != nil {
		return "", fmt.Errorf("virtbench: %s: resolve fio_vm_template: %w", tr.ID, err)
	}
	if info, err := os.Stat(path); err != nil {
		return "", fmt.Errorf("virtbench: %s: fio_vm_template %q not accessible: %w", tr.ID, template, err)
	} else if info.IsDir() {
		return "", fmt.Errorf("virtbench: %s: fio_vm_template %q is a directory", tr.ID, template)
	}
	return path, nil
}

// clusterArgs emits the optional global connection flags shared by every
// scenario, before the subcommand.
func clusterArgs(tr core.TestRequirement) []string {
	var args []string
	if kc := strParamOr(tr, "kubeconfig", ""); kc != "" {
		args = append(args, "--kubeconfig", kc)
	}
	if uuid := strParamOr(tr, "uuid", ""); uuid != "" {
		args = append(args, "--uuid", uuid)
	}
	return args
}

// --- misc helpers ------------------------------------------------------------

// logWriter forwards a subprocess stream to the run logger, one write at a time.
type logWriter struct {
	log    *slog.Logger
	stream string
}

func (w logWriter) Write(p []byte) (int, error) {
	w.log.Info("virtbench", "stream", w.stream, "line", strings.TrimRight(string(p), "\n"))
	return len(p), nil
}

// execVirtbench runs the virtbench CLI in dir, forwarding output to the logger.
func execVirtbench(ctx context.Context, rc *core.RunCtx, request manifestRequest) error {
	args, err := prepareManifest(ctx, request)
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, binary, args...) // #nosec G204 -- arguments are assembled by the harness scenario.
	cmd.WaitDelay = 5 * time.Second
	cmd.Dir = request.Directory
	cmd.Stdout = logWriter{log: rc.Logger, stream: "stdout"}
	cmd.Stderr = logWriter{log: rc.Logger, stream: "stderr"}
	return cmd.Run()
}

// storageClass resolves the storage class: the active backend wins, else the
// TR's 'storage_class' param.
func storageClass(rc *core.RunCtx, tr core.TestRequirement) string {
	if rc != nil && rc.Backend != nil && rc.Backend.StorageClass != "" {
		return rc.Backend.StorageClass
	}
	return strParamOr(tr, "storage_class", "")
}

// replayDir returns the first non-empty results_dir param among trs, if any.
func replayDir(trs []core.TestRequirement) string {
	for _, tr := range trs {
		if d := strParamOr(tr, "results_dir", ""); d != "" {
			return d
		}
	}
	return ""
}

// allReplayTRs reports whether every TR in the batch is replay-only (each has
// results_dir). Mixed replay/live batches still need live cluster setup/teardown.
func allReplayTRs(trs []core.TestRequirement) bool {
	if len(trs) == 0 {
		return false
	}
	for _, tr := range trs {
		if strParamOr(tr, "results_dir", "") == "" {
			return false
		}
	}
	return true
}

func strParamOr(tr core.TestRequirement, key, def string) string {
	if tr.Params == nil {
		return def
	}
	if v, ok := tr.Params[key]; ok {
		if s, ok := v.(string); ok && s != "" {
			return s
		}
	}
	return def
}

func intParam(tr core.TestRequirement, key string, fallback int) int {
	if tr.Params == nil {
		return fallback
	}
	v, ok := tr.Params[key]
	if !ok {
		return fallback
	}
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	case string:
		if i, err := strconv.Atoi(n); err == nil {
			return i
		}
	}
	return fallback
}

// failureRecoveryArgs builds the CLI for the failure-recovery scenario.
// The node is taken from the bag (set by provisionFailureRecoveryVMs which
// auto-picks a worker at Provision time) or from an explicit "node" param.
func failureRecoveryArgs(nsPrefix string) func(rc *core.RunCtx, bag *core.Bag, tr core.TestRequirement, resultsRoot string) ([]string, error) {
	return func(rc *core.RunCtx, bag *core.Bag, tr core.TestRequirement, resultsRoot string) ([]string, error) {
		node, _ := core.GetAs[string](bag, "picked_node")
		if node == "" {
			node = strParamOr(tr, "node", "")
		}
		if node == "" {
			return nil, fmt.Errorf("virtbench: %s: worker node unknown (provisioning may have failed)", tr.ID)
		}
		args := clusterArgs(tr)
		// A live certification run must trigger the failure. Monitor mode is
		// reserved for explicitly authorized external-failure workflows.
		mode := strParamOr(tr, "mode", "far-operator")
		args = append(args, "failure-recovery",
			"--mode", mode,
			"--node", node,
			"--vm-name", strParamOr(tr, "vm_name", defaultVMName),
			"--namespace-prefix", strParamOr(tr, "namespace_prefix", nsPrefix),
			"--yes",
			"--save-results",
			"--results-folder", resultsRoot,
		)
		if t := intParam(tr, "vm_recovery_timeout", 0); t > 0 {
			args = append(args, "--recovery-timeout", strconv.Itoa(t))
		}
		if mode == "far-operator" {
			farConfig := strParamOr(tr, "far_config", "")
			if farConfig == "" {
				return nil, fmt.Errorf("virtbench: %s: far_config is required for far-operator mode", tr.ID)
			}
			args = append(args, "--far-config", farConfig, "--far-name", node,
				"--remove-node-selector", "--cleanup", "--cleanup-vms")
		}
		if sc := storageClass(nil, tr); sc != "" {
			args = append(args, "--storage-class", sc)
		}
		return args, nil
	}
}
