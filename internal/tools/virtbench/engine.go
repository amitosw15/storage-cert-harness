// Package virtbench integrates portworx/kubevirt-benchmark ("virtbench") as a
// tool. virtbench ships no upstream container image, so phase 1 runs the released
// CLI locally via os/exec; an in-cluster Job model is a documented follow-up
// (decisions/0007).
//
// virtbench exposes several scenarios (datasource-clone, its --boot-storm
// variant, disk-ops, …), each a distinct KB automation_tool and a distinct TR.
// They share ALL the plumbing here — preflight, provision, exec, collect,
// teardown, and replay — and differ only in a small Scenario config
// (scenario.go): the automation_tool string, the TR it provides, the result file
// name, how to build the CLI args, and how to parse the output. Adding a scenario
// should be a new table row, never an engine edit (decisions/0008).
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
	_ "embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"gitlab.cee.redhat.com/eco-special-projects/storage-cert-harness/internal/core"
	"gitlab.cee.redhat.com/eco-special-projects/storage-cert-harness/internal/safefs"
	"gitlab.cee.redhat.com/eco-special-projects/storage-cert-harness/internal/stages"
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

const fioVMTemplateFile = "fio-vm-template.yaml"

// fioVMTemplate is compiled into the harness so live FIO runs do not depend on
// the source repository being mounted or on a plan knowing an internal path.
// A plan may still provide fio_vm_template to use a custom VM definition.
//
//go:embed assets/fio-vm-template.yaml
var fioVMTemplate string

// teardownBudget bounds the best-effort cleanup, which runs on a fresh context so
// it still happens when the run ctx was cancelled.
const teardownBudget = 120 * time.Second

// --- Preflight ---------------------------------------------------------------

type preflight struct{ sc Scenario }

func (p preflight) Check(ctx context.Context, rc *core.RunCtx, _ *core.Bag, trs []core.TestRequirement) ([]core.Finding, error) {
	var findings []core.Finding
	// Replay grades pre-collected results — no binary or cluster needed.
	dir := replayDir(trs)
	if dir != "" {
		if _, err := os.Stat(dir); err != nil {
			findings = append(findings, core.Finding{Level: "error", Message: fmt.Sprintf("virtbench: replay results_dir %q not accessible: %v", dir, err)})
		} else {
			findings = append(findings, core.Finding{Level: "info", Message: "virtbench: replay results_dir accessible: " + dir})
		}
	}

	if dir != "" {
		findings = append(findings, core.Finding{Level: "skip", Message: "virtbench CLI on PATH: replay mode"})
		if len(p.sc.CommandCheck) > 0 {
			findings = append(findings, core.Finding{Level: "skip", Message: "virtbench command " + p.sc.CommandCheck[0] + ": replay mode"})
		}
	} else if _, err := exec.LookPath(binary); err != nil {
		findings = append(findings, core.Finding{Level: "error", Message: "virtbench CLI not found on PATH (install per https://portworx.github.io/kubevirt-benchmark/install/, or use replay mode via the results_dir param)"})
		if len(p.sc.CommandCheck) > 0 {
			findings = append(findings, core.Finding{Level: "skip", Message: "virtbench command " + p.sc.CommandCheck[0] + ": virtbench CLI not found on PATH"})
		}
	} else {
		findings = append(findings, core.Finding{Level: "info", Message: "virtbench CLI found on PATH"})
		if len(p.sc.CommandCheck) > 0 {
			cmd := exec.CommandContext(ctx, binary, p.sc.CommandCheck...) // #nosec G204 -- command checks are declared by built-in scenarios.
			if output, err := cmd.CombinedOutput(); err != nil {
				findings = append(findings, core.Finding{Level: "error", Message: fmt.Sprintf("virtbench: required command %q unavailable: %v: %s", p.sc.CommandCheck[0], err, strings.TrimSpace(string(output)))})
			} else {
				findings = append(findings, core.Finding{Level: "info", Message: "virtbench command available: " + p.sc.CommandCheck[0]})
			}
		}
	}
	for _, tr := range trs {
		if dir != "" {
			findings = append(findings, core.Finding{Level: "skip", Message: tr.ID + " storage_class: replay mode"})
		} else if sc := storageClass(rc, tr); sc == "" {
			findings = append(findings, core.Finding{Level: "error", Message: fmt.Sprintf("virtbench: %s has no storage_class (select a --backend or set the TR 'storage_class' param)", tr.ID)})
		} else {
			findings = append(findings, core.Finding{Level: "info", Message: tr.ID + " storage_class=" + sc})
		}
		if p.sc.Validate != nil {
			if dir != "" {
				findings = append(findings, core.Finding{Level: "skip", Message: tr.ID + " scenario parameters: replay mode"})
			} else if err := p.sc.Validate(tr); err != nil {
				findings = append(findings, core.Finding{Level: "error", Message: tr.ID + " scenario parameters: " + err.Error()})
			} else {
				findings = append(findings, core.Finding{Level: "info", Message: tr.ID + " scenario parameters valid"})
			}
		}
	}
	if p.sc.Preflight != nil {
		findings = append(findings, p.sc.Preflight(ctx, rc, trs)...)
	}
	return findings, nil
}

// --- Provision ---------------------------------------------------------------

type provisioner struct{ sc Scenario }

func (p provisioner) Provision(ctx context.Context, rc *core.RunCtx, bag *core.Bag, trs []core.TestRequirement) error {
	if len(trs) > 0 {
		bag.Set("tr_id", trs[0].ID)
		// Stash what Teardown's safety-net namespace sweep needs (Teardown gets no
		// TRs). namespace_prefix mirrors the default in the scenario's BuildArgs.
		bag.Set("ns_prefix", strParamOr(trs[0], "namespace_prefix", p.sc.NSPrefix))
		bag.Set("kubeconfig", strParamOr(trs[0], "kubeconfig", ""))
	}
	if dir := replayDir(trs); dir != "" {
		bag.Set("replay", true)
		bag.Set("results_root", dir)
		rc.Logger.Info("virtbench: replay mode", "results_dir", dir)
		return nil
	}
	// A preceding test (notably node drain) may have evicted the shared helper.
	// Recheck before scenario provisioning, which can itself invoke virtbench.
	if err := sharedSSHSetup.Setup(ctx, rc, trs); err != nil {
		return fmt.Errorf("virtbench: ensure ssh helper pod: %w", err)
	}
	root := rc.WorkDir
	if root == "" {
		root = "."
	}
	root = filepath.Join(root, "virtbench")
	if err := safefs.MkdirAll(root, 0o755); err != nil {
		return fmt.Errorf("virtbench: create results dir: %w", err)
	}
	bag.Set("replay", false)
	bag.Set("results_root", root)
	rc.Logger.Debug("virtbench: provisioned results dir", "results_root", root)
	// A scenario that must stage the cluster before Run does it here (drain clones
	// the VMs onto the target); the generic scenarios have no extra provisioning.
	if p.sc.Provision != nil && len(trs) > 0 {
		if err := p.sc.Provision(ctx, rc, bag, trs[0], root); err != nil {
			return err
		}
	}
	return nil
}

// --- Run ---------------------------------------------------------------------

type runner struct{ sc Scenario }

func (r runner) Run(ctx context.Context, rc *core.RunCtx, bag *core.Bag, trs []core.TestRequirement) (stages.RunHandle, error) {
	replay, _ := core.GetAs[bool](bag, "replay")
	root, _ := core.GetAs[string](bag, "results_root")
	if replay {
		rc.Logger.Info("virtbench: replay — skipping exec", "results_root", root)
		return stages.RunHandle{ID: "virtbench-replay"}, nil
	}
	if len(trs) == 0 {
		return stages.RunHandle{}, fmt.Errorf("virtbench: no TRs to run")
	}
	if r.sc.Run != nil {
		return r.sc.Run(ctx, rc, bag, trs)
	}
	args, err := r.sc.BuildArgs(rc, trs[0], root)
	if err != nil {
		return stages.RunHandle{}, err
	}
	rc.Logger.Info("virtbench: exec", "cmd", binary, "args", args, "dir", root)
	if err := execVirtbench(ctx, rc, root, args); err != nil {
		// drain-nodes exits 1 on a timeout — a gradeable FAIL, not a crash. Keep
		// going to Collect/Parse so the log is still scored (decisions/0013).
		if !r.sc.TolerateRunError {
			return stages.RunHandle{}, fmt.Errorf("virtbench: run failed: %w", err)
		}
		rc.Logger.Warn("virtbench: CLI exited non-zero; grading from its output anyway", "err", err)
	}
	return stages.RunHandle{ID: "virtbench-" + rc.RunID}, nil
}

func runVirtbench(ctx context.Context, rc *core.RunCtx, root string, args []string) error {
	rc.Logger.Info("virtbench: exec", "cmd", binary, "args", args, "dir", root)
	if err := execVirtbench(ctx, rc, root, args); err != nil {
		return fmt.Errorf("virtbench: run failed: %w", err)
	}
	return nil
}

// --- Collect -----------------------------------------------------------------

type collector struct{ sc Scenario }

func (c collector) Collect(_ context.Context, rc *core.RunCtx, bag *core.Bag, h stages.RunHandle) (core.LogBundle, error) {
	root, _ := core.GetAs[string](bag, "results_root")
	if root == "" {
		return core.LogBundle{}, fmt.Errorf("virtbench: no results_root in bag")
	}
	if c.sc.Collect != nil {
		replay, _ := core.GetAs[bool](bag, "replay")
		refs, data, err := c.sc.Collect(root, replay)
		if err != nil {
			return core.LogBundle{}, err
		}
		rc.Logger.Info("virtbench: collected results", "paths", refs)
		return core.LogBundle{
			Refs: refs,
			Data: map[string][]byte{c.sc.ResultFile: data},
		}, nil
	}
	path, err := findResult(root, c.sc.ResultFile)
	if err != nil {
		return core.LogBundle{}, err
	}
	data, err := safefs.ReadUnder(root, path)
	if err != nil {
		return core.LogBundle{}, fmt.Errorf("virtbench: read results: %w", err)
	}
	rc.Logger.Info("virtbench: collected results", "path", path)
	refs := []string{path}
	bundle := map[string][]byte{c.sc.ResultFile: data}
	if c.sc.DetailFile != "" {
		if dp, err := findResult(root, c.sc.DetailFile); err == nil {
			if dd, err := safefs.ReadUnder(root, dp); err == nil {
				bundle[c.sc.DetailFile] = dd
				refs = append(refs, dp)
			}
		} else {
			rc.Logger.Warn("virtbench: detail results not found", "file", c.sc.DetailFile)
		}
	}
	return core.LogBundle{Refs: refs, Data: bundle}, nil
}

// collectFIOResults keeps every per-VM raw FIO result together with Virtbench's
// run summary, so the parser can reject an incomplete VM range.
func collectFIOResults(root string, replay bool) ([]string, []byte, error) {
	paths, err := findResults(root, FIOFileName)
	if err != nil {
		return nil, nil, err
	}
	results := make([]json.RawMessage, 0, len(paths))
	for _, path := range paths {
		data, err := safefs.ReadUnder(root, path)
		if err != nil {
			return nil, nil, fmt.Errorf("virtbench: read fio result %q: %w", path, err)
		}
		results = append(results, data)
	}
	if replay {
		if len(results) != 1 {
			return nil, nil, fmt.Errorf("virtbench: replay FIO results has %d raw files, want one", len(results))
		}
		return paths, results[0], nil
	}

	summaryPath, err := findResult(root, FIOSummaryFileName)
	if err != nil {
		return nil, nil, err
	}
	summary, err := safefs.ReadUnder(root, summaryPath)
	if err != nil {
		return nil, nil, fmt.Errorf("virtbench: read fio summary: %w", err)
	}
	bundle, err := json.Marshal(fioCollectedResults{Summary: summary, Results: results})
	if err != nil {
		return nil, nil, fmt.Errorf("virtbench: package fio results: %w", err)
	}
	return append(paths, summaryPath), bundle, nil
}

// findResult returns the path to the scenario's result file: root/<name> if
// present, else the first match found walking root (virtbench may nest results in
// a subdir).
func findResult(root, name string) (string, error) {
	direct := filepath.Join(root, name)
	if _, err := os.Stat(direct); err == nil {
		return direct, nil
	}
	var found string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && d.Name() == name {
			found = path
			return filepath.SkipAll
		}
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("virtbench: search %s: %w", root, err)
	}
	if found == "" {
		return "", fmt.Errorf("virtbench: %s not found under %s", name, root)
	}
	return found, nil
}

func findResults(root, name string) ([]string, error) {
	var found []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && d.Name() == name {
			found = append(found, path)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("virtbench: search %s: %w", root, err)
	}
	if len(found) == 0 {
		return nil, fmt.Errorf("virtbench: %s not found under %s", name, root)
	}
	sort.Strings(found)
	return found, nil
}

// --- Parse -------------------------------------------------------------------

type parser struct{ sc Scenario }

func (p parser) Parse(_ context.Context, _ *core.RunCtx, bag *core.Bag, logs core.LogBundle) ([]core.TestResult, error) {
	trID, _ := core.GetAs[string](bag, "tr_id")
	if trID == "" {
		trID = p.sc.ProvidesTR
	}
	res, err := p.sc.Parse(logs.Data[p.sc.ResultFile], trID)
	if err != nil {
		return nil, err
	}
	if p.sc.DetailParse != nil && len(res) > 0 {
		if dd, ok := logs.Data[p.sc.DetailFile]; ok {
			extra, err := p.sc.DetailParse(dd)
			if err != nil {
				return nil, err
			}
			res[0].Metrics = append(res[0].Metrics, extra...)
		}
	}
	return res, nil
}

// --- Teardown ----------------------------------------------------------------

type teardown struct{ sc Scenario }

// Teardown is a best-effort safety net. Normal runs pass --cleanup(-on-failure)
// so virtbench removes its own namespaces/VMs; the generic sweep only catches the
// case where the process was killed mid-run (drain-nodes deliberately omits
// --cleanup, so its VMs always land here). It runs on a fresh bounded context so
// cleanup still happens when the run ctx was cancelled, and never fails the run.
func (t teardown) Teardown(_ context.Context, rc *core.RunCtx, bag *core.Bag) error {
	if replay, _ := core.GetAs[bool](bag, "replay"); replay {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), teardownBudget)
	defer cancel()

	// A scenario adds its own cleanup on top of the generic namespace sweep (drain
	// uncordons the workers it cordoned).
	if t.sc.Teardown != nil {
		if err := t.sc.Teardown(ctx, rc, bag); err != nil {
			rc.Logger.Warn("virtbench: scenario teardown failed", "tool", t.sc.AutomationTool, "err", err)
		}
	}

	prefix, _ := core.GetAs[string](bag, "ns_prefix")
	if prefix == "" {
		prefix = t.sc.NSPrefix
	}
	kubeconfig, _ := core.GetAs[string](bag, "kubeconfig")

	leftovers, err := listNamespacesByPrefix(ctx, kubeconfig, prefix+"-")
	if err != nil {
		rc.Logger.Warn("virtbench: teardown could not list namespaces; verify none leftover", "prefix", prefix+"-", "err", err)
		return nil
	}
	if len(leftovers) == 0 {
		return nil
	}
	rc.Logger.Warn("virtbench: cleaning up leftover namespaces", "namespaces", leftovers)
	for _, ns := range leftovers {
		if err := deleteNamespace(ctx, kubeconfig, ns); err != nil {
			rc.Logger.Warn("virtbench: failed to delete leftover namespace", "namespace", ns, "err", err)
		}
	}
	return nil
}

// --- shared arg helpers ------------------------------------------------------

// datasourceCloneArgs builds the CLI for the datasource-clone family. The
// boot-storm scenario is the same subcommand plus the --boot-storm flag; it reads
// ONLY params/backend config — never a threshold/SLA value — so nothing sensitive
// can reach a command line. resultsRoot is where virtbench must write results so
// the collector can find them.
func datasourceCloneArgs(bootStorm bool, nsPrefix string) func(rc *core.RunCtx, tr core.TestRequirement, resultsRoot string) ([]string, error) {
	return func(rc *core.RunCtx, tr core.TestRequirement, resultsRoot string) ([]string, error) {
		sc := storageClass(rc, tr)
		if sc == "" {
			return nil, fmt.Errorf("virtbench: %s: storage_class required (backend or 'storage_class' param)", tr.ID)
		}
		start := intParam(tr, "start", 1)
		end := intParam(tr, "iteration_clones", 100) // TR-VIRT-018
		end = intParam(tr, "num_vms", end)           // TR-VIRT-001

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
			staged, err := stageTemplate(resultsRoot, tmpl)
			if err != nil {
				return nil, fmt.Errorf("virtbench: %s: %w", tr.ID, err)
			}
			args = append(args, "--vm-template", staged)
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

// diskOpsArgs builds the CLI for the disk-ops scenario (a different subcommand
// with its own flags — note --results-dir vs the clone family's --results-folder,
// and disk-ops templates its own VM name so no --vm-name fix is needed). Same
// secret-hygiene rule as datasourceCloneArgs.
func diskOpsArgs(nsPrefix string) func(rc *core.RunCtx, tr core.TestRequirement, resultsRoot string) ([]string, error) {
	return func(rc *core.RunCtx, tr core.TestRequirement, resultsRoot string) ([]string, error) {
		sc := storageClass(rc, tr)
		if sc == "" {
			return nil, fmt.Errorf("virtbench: %s: storage_class required (backend or 'storage_class' param)", tr.ID)
		}
		start := intParam(tr, "start", 1)
		end := intParam(tr, "end", intParam(tr, "count", 100))

		args := clusterArgs(tr)
		args = append(args, "disk-ops",
			"--start", strconv.Itoa(start),
			"--end", strconv.Itoa(end),
			"--namespace-prefix", strParamOr(tr, "namespace_prefix", nsPrefix),
			"--storage-class", sc,
			"--operation", strParamOr(tr, "operation", "hotplug"),
			"--save-results",
			"--results-dir", resultsRoot,
		)
		if tmpl := strParamOr(tr, "vm_template", ""); tmpl != "" {
			staged, err := stageTemplate(resultsRoot, tmpl)
			if err != nil {
				return nil, fmt.Errorf("virtbench: %s: %w", tr.ID, err)
			}
			args = append(args, "--vm-template", staged)
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

// stageTemplate copies the VM template into resultsRoot and returns the copy's
// absolute path. virtbench substitutes {{STORAGE_CLASS_NAME}} into the template
// file in place, so it must run against a throwaway copy in the workdir — never
// the repo template, which would get mutated (and its placeholder clobbered).
func stageTemplate(resultsRoot, tmpl string) (string, error) {
	data, err := safefs.ReadFile(tmpl)
	if err != nil {
		return "", fmt.Errorf("read vm_template %q: %w", tmpl, err)
	}
	dst := filepath.Join(resultsRoot, filepath.Base(tmpl))
	if err := safefs.WriteFile(dst, data, 0o600); err != nil {
		return "", fmt.Errorf("stage vm_template: %w", err)
	}
	return filepath.Abs(dst)
}

const defaultFIORuntime = 600

const (
	defaultFIOVMReadyTimeout   = 600
	defaultFIOCollectRetries   = 8
	defaultFIOCollectRetryWait = 20
	defaultFIOVMName           = "fio-vm"
)

// fioArgs builds the fixed 4 KiB random read/write profile. The plan's start/end
// range selects one VM or a concurrent fleet; the parser grades the worst p99.
func fioArgs(nsPrefix string) func(rc *core.RunCtx, tr core.TestRequirement, resultsRoot string) ([]string, error) {
	return func(rc *core.RunCtx, tr core.TestRequirement, resultsRoot string) ([]string, error) {
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
		if _, _, err := fioCollectionSettings(tr); err != nil {
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
		staged, err := stageFIOTemplate(resultsRoot, tr)
		if err != nil {
			return nil, fmt.Errorf("virtbench: %s: %w", tr.ID, err)
		}

		args := clusterArgs(tr)
		args = append(args, "fio",
			"--action", "run-all",
			"--start", strconv.Itoa(start),
			"--end", strconv.Itoa(end),
			"--namespace-prefix", strParamOr(tr, "namespace_prefix", nsPrefix),
			"--storage-class", sc,
			"--vm-template", staged,
			"--fio-rw", "randrw",
			"--fio-bs", bs,
			"--save-results",
			"--results-dir", resultsRoot,
			"--cleanup",
		)
		runtime := intParam(tr, "fio_runtime", defaultFIORuntime)
		if runtime < 1 {
			return nil, fmt.Errorf("virtbench: %s: fio_runtime must be >= 1", tr.ID)
		}
		args = append(args, "--fio-runtime", strconv.Itoa(runtime))
		if n := intParam(tr, "fio_iodepth", 0); n > 0 {
			args = append(args, "--fio-iodepth", strconv.Itoa(n))
		}
		if n := intParam(tr, "fio_numjobs", 0); n > 0 {
			args = append(args, "--fio-numjobs", strconv.Itoa(n))
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

// runFIO avoids Virtbench's fixed five-minute VM boot wait. FIO starts during
// guest boot; this runner waits for every VM to be Running, then gathers results.
func runFIO(ctx context.Context, rc *core.RunCtx, bag *core.Bag, trs []core.TestRequirement) (stages.RunHandle, error) {
	if len(trs) == 0 {
		return stages.RunHandle{}, fmt.Errorf("virtbench: no FIO test requirement")
	}
	root, _ := core.GetAs[string](bag, "results_root")
	if root == "" {
		return stages.RunHandle{}, fmt.Errorf("virtbench: no results_root in bag")
	}
	tr := trs[0]
	base, err := fioArgs("fio-latency")(rc, tr, root)
	if err != nil {
		return stages.RunHandle{}, err
	}
	deploy := fioActionArgs(base, "deploy")
	gather := fioActionArgs(base, "gather-results")
	cleanup := fioActionArgs(base, "cleanup")
	retries, delay, err := fioCollectionSettings(tr)
	if err != nil {
		return stages.RunHandle{}, fmt.Errorf("virtbench: %s: %w", tr.ID, err)
	}
	gather = append(gather,
		"--collect-retries", strconv.Itoa(retries),
		"--collect-retry-delay", strconv.Itoa(delay),
	)

	deployed := false
	defer func() {
		if !deployed {
			return
		}
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Minute)
		defer cancel()
		if err := runVirtbench(cleanupCtx, rc, root, cleanup); err != nil {
			rc.Logger.Warn("virtbench fio: cleanup failed; teardown will retry", "err", err)
		}
	}()

	deployed = true
	if err := runVirtbench(ctx, rc, root, deploy); err != nil {
		return stages.RunHandle{}, fmt.Errorf("virtbench fio: deploy: %w", err)
	}
	if err := waitForFIOVMs(ctx, rc, tr); err != nil {
		return stages.RunHandle{}, err
	}
	if err := runVirtbench(ctx, rc, root, gather); err != nil {
		return stages.RunHandle{}, fmt.Errorf("virtbench fio: gather results: %w", err)
	}
	return stages.RunHandle{ID: "virtbench-" + rc.RunID}, nil
}

func fioActionArgs(base []string, action string) []string {
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
	return args
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

type fioVMIList struct {
	Items []fioVMI `json:"items"`
}

type fioVMI struct {
	Metadata fioVMIMetadata `json:"metadata"`
	Status   fioVMIStatus   `json:"status"`
}

type fioVMIMetadata struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
}

type fioVMIStatus struct {
	Phase      string            `json:"phase"`
	Interfaces []fioVMIInterface `json:"interfaces"`
}

type fioVMIInterface struct {
	IPAddress string `json:"ipAddress"`
}

func waitForFIOVMs(ctx context.Context, rc *core.RunCtx, tr core.TestRequirement) error {
	timeout, err := fioVMReadyTimeout(tr)
	if err != nil {
		return fmt.Errorf("virtbench: %s: %w", tr.ID, err)
	}
	start, end, err := fioVMRange(tr)
	if err != nil {
		return fmt.Errorf("virtbench: %s: %w", tr.ID, err)
	}
	prefix := strParamOr(tr, "namespace_prefix", "fio-latency")
	wanted := make(map[string]struct{}, end-start+1)
	for index := start; index <= end; index++ {
		wanted[fmt.Sprintf("%s-%d/%s", prefix, index, defaultFIOVMName)] = struct{}{}
	}
	deadline := time.Now().Add(timeout)
	kubeconfig := strParamOr(tr, "kubeconfig", "")
	for {
		out, ok, runErr := runKubectl(ctx, kubeconfig, "get", "vmi", "--all-namespaces", "-o", "json")
		if runErr != nil {
			return fmt.Errorf("virtbench fio: list VM instances: %w", runErr)
		}
		if !ok {
			return fmt.Errorf("virtbench fio: list VM instances: %s", strings.TrimSpace(out))
		}
		ready, failed, err := fioVMStates([]byte(out), wanted)
		if err != nil {
			return err
		}
		if failed > 0 {
			return fmt.Errorf("virtbench fio: %d VM instances entered Failed state", failed)
		}
		if ready == len(wanted) {
			rc.Logger.Info("virtbench fio: all VMs ready", "vms", ready)
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("virtbench fio: %d of %d VMs ready after %s", ready, len(wanted), timeout)
		}
		rc.Logger.Info("virtbench fio: waiting for VM readiness", "ready", ready, "total", len(wanted), "timeout", timeout)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Second):
		}
	}
}

func fioVMStates(data []byte, wanted map[string]struct{}) (ready, failed int, err error) {
	var list fioVMIList
	if err := json.Unmarshal(data, &list); err != nil {
		return 0, 0, fmt.Errorf("virtbench fio: parse VM instance list: %w", err)
	}
	for _, item := range list.Items {
		if _, ok := wanted[item.Metadata.Namespace+"/"+item.Metadata.Name]; !ok {
			continue
		}
		switch item.Status.Phase {
		case "Running":
			if !fioVMIHasIP(item) {
				continue
			}
			ready++
		case "Failed":
			failed++
		}
	}
	return ready, failed, nil
}

func fioVMIHasIP(item fioVMI) bool {
	for _, iface := range item.Status.Interfaces {
		if iface.IPAddress != "" {
			return true
		}
	}
	return false
}

func fioValidate(tr core.TestRequirement) error {
	if _, _, err := fioVMRange(tr); err != nil {
		return err
	}
	if _, err := fioProfile(tr); err != nil {
		return err
	}
	_, err := fioTemplate(tr)
	return err
}

// stageFIOTemplate stages the embedded default or a caller-supplied custom
// template into the run workdir. virtbench mutates the staged file while
// substituting the storage class, so neither the embedded bytes nor a custom
// source file are ever passed to it directly.
func stageFIOTemplate(resultsRoot string, tr core.TestRequirement) (string, error) {
	template, err := fioTemplate(tr)
	if err != nil {
		return "", err
	}
	if template == "" {
		dst := filepath.Join(resultsRoot, fioVMTemplateFile)
		if err := safefs.WriteFile(dst, []byte(fioVMTemplate), 0o600); err != nil {
			return "", fmt.Errorf("stage embedded fio vm template: %w", err)
		}
		return filepath.Abs(dst)
	}
	return stageTemplate(resultsRoot, template)
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
func execVirtbench(ctx context.Context, rc *core.RunCtx, dir string, args []string) error {
	cmd := exec.CommandContext(ctx, binary, args...) // #nosec G204 -- arguments are assembled by the harness scenario.
	cmd.Dir = dir
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
