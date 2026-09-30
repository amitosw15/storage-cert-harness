// Command harness is the CSI/storage certification test harness CLI.
//
// Tool integrations are registered by blank-importing their packages below —
// that single import line is all it takes to add a compiled-in tool. See
// decisions/0003.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"gitlab.cee.redhat.com/eco-special-projects/storage-cert-harness/internal/attestation"
	"gitlab.cee.redhat.com/eco-special-projects/storage-cert-harness/internal/backend"
	"gitlab.cee.redhat.com/eco-special-projects/storage-cert-harness/internal/catalog"
	"gitlab.cee.redhat.com/eco-special-projects/storage-cert-harness/internal/clustercheck"
	"gitlab.cee.redhat.com/eco-special-projects/storage-cert-harness/internal/config"
	"gitlab.cee.redhat.com/eco-special-projects/storage-cert-harness/internal/core"
	"gitlab.cee.redhat.com/eco-special-projects/storage-cert-harness/internal/kb"
	"gitlab.cee.redhat.com/eco-special-projects/storage-cert-harness/internal/orchestrator"
	"gitlab.cee.redhat.com/eco-special-projects/storage-cert-harness/internal/plan"
	"gitlab.cee.redhat.com/eco-special-projects/storage-cert-harness/internal/registry"
	"gitlab.cee.redhat.com/eco-special-projects/storage-cert-harness/internal/report"
	"gitlab.cee.redhat.com/eco-special-projects/storage-cert-harness/internal/safefs"
	"gitlab.cee.redhat.com/eco-special-projects/storage-cert-harness/internal/thresholds"

	// Tool integrations — add a line here to compile in a new tool.
	_ "gitlab.cee.redhat.com/eco-special-projects/storage-cert-harness/internal/tools/example"
	_ "gitlab.cee.redhat.com/eco-special-projects/storage-cert-harness/internal/tools/kubeburner"
	_ "gitlab.cee.redhat.com/eco-special-projects/storage-cert-harness/internal/tools/kubeburnerocp"
	_ "gitlab.cee.redhat.com/eco-special-projects/storage-cert-harness/internal/tools/virtbench"
)

// version is overridable at build time: -ldflags "-X main.version=..."
var version = "0.0.0-dev"

func main() {
	if err := newRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err) //nolint:errcheck // follow-up: check CLI writes
		os.Exit(1)
	}
}

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "harness",
		Short:         "Storage certification test harness",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(newVersionCmd(), newListCmd(), newValidateCmd(), newPreflightCmd(), newRunCmd(), newAttestCmd(), newRecoverCmd(), newSimulateReportCmd())
	return root
}

func newLogger(verbose bool) *slog.Logger {
	return newLoggerWithOutput(verbose, nil)
}

func newLoggerWithOutput(verbose bool, logOutput io.Writer) *slog.Logger {
	return newLoggerTo(verbose, os.Stderr, logOutput)
}

func newLoggerTo(verbose bool, terminal io.Writer, logOutput io.Writer) *slog.Logger {
	lvl := slog.LevelInfo
	if verbose {
		lvl = slog.LevelDebug
	}
	output := terminal
	if logOutput != nil {
		output = io.MultiWriter(terminal, logOutput)
	}
	return slog.New(slog.NewTextHandler(output, &slog.HandlerOptions{Level: lvl}))
}

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the harness version",
		RunE: func(cmd *cobra.Command, _ []string) error {
			fmt.Fprintln(cmd.OutOrStdout(), version) //nolint:errcheck // follow-up: check CLI writes
			return nil
		},
	}
}

func newListCmd() *cobra.Command {
	var catalogPath string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List registered tools and (optionally) catalog test requirements",
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := cmd.OutOrStdout()
			fmt.Fprintln(out, "Registered tools:") //nolint:errcheck // follow-up: check CLI writes
			for _, name := range registry.Names() {
				ti, _ := registry.Get(name)
				fmt.Fprintf(out, "  - %s (image: %s) provides %v\n", ti.Name, ti.Image, ti.Provides) //nolint:errcheck // follow-up: check CLI writes
			}
			if catalogPath != "" {
				cat, err := catalog.Load(catalogPath)
				if err != nil {
					return err
				}
				fmt.Fprintf(out, "\nCatalog test requirements (%d), schema %s, KB commit %s:\n", //nolint:errcheck // follow-up: check CLI writes
					len(cat.TestRequirements), cat.SchemaVersion, cat.Provenance.KBGitCommit)
				for _, tr := range cat.TestRequirements {
					fmt.Fprintf(out, "  - %s L%d [tool=%s] %q (sla=%d/%s, checks=%d/%s)\n", //nolint:errcheck // follow-up: check CLI writes
						tr.ID, tr.PartnerLevel, tr.AutomationTool, tr.Title,
						tr.SLACount, tr.SLAStatus, tr.ChecksCount, tr.ChecksStatus)
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&catalogPath, "catalog", "", "path to catalog.json (publishable)")
	return cmd
}

func newValidateCmd() *cobra.Command {
	var catalogPath, thresholdsPath string
	cmd := &cobra.Command{
		Use:   "validate",
		Short: "Validate a catalog and (optionally) a thresholds bundle, verifying they join",
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := cmd.OutOrStdout()
			cat, err := catalog.Load(catalogPath)
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "catalog OK: %d test requirements, schema %s, KB commit %s\n", //nolint:errcheck // follow-up: check CLI writes
				len(cat.TestRequirements), cat.SchemaVersion, cat.Provenance.KBGitCommit)
			if thresholdsPath != "" {
				th, err := thresholds.Load(thresholdsPath)
				if err != nil {
					return err
				}
				fmt.Fprintf(out, "thresholds OK: %d test requirements, schema %s, KB commit %s\n", //nolint:errcheck // follow-up: check CLI writes
					len(th.TestRequirements), th.SchemaVersion, th.Provenance.KBGitCommit)
				trs, err := kb.Join(cat, th)
				if err != nil {
					return err
				}
				fmt.Fprintf(out, "join OK: %d joined test requirements (catalog + thresholds agree)\n", len(trs)) //nolint:errcheck // follow-up: check CLI writes
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&catalogPath, "catalog", "", "path to catalog.json (required)")
	cmd.Flags().StringVar(&thresholdsPath, "thresholds", "", "path to thresholds.json (optional)")
	_ = cmd.MarkFlagRequired("catalog")
	return cmd
}

func newPreflightCmd() *cobra.Command {
	var (
		catalogPath  string
		filter       config.Filter
		planPath     string
		backendsPath string
		backendName  string
	)
	var verbose bool
	cmd := &cobra.Command{
		Use:   "preflight",
		Short: "Run preflight checks for the tools selected by the catalog/filter",
		RunE: func(cmd *cobra.Command, _ []string) error {
			logger := newLogger(verbose)
			cfg := config.Config{Filter: filter}
			resolved, err := configureExecution(cmd, &cfg, planPath, backendsPath, &backendName, false, logger)
			if err != nil {
				return err
			}
			cat, err := catalog.Load(catalogPath)
			if err != nil {
				return err
			}
			trs, err := orchestrator.ResolveExecutions(kb.FromCatalog(cat), cfg)
			if err != nil {
				return err
			}
			rc := &core.RunCtx{RunID: "preflight", Logger: logger, Backend: resolved}
			out := cmd.OutOrStdout()
			byTool := map[string][]core.TestRequirement{}
			for _, tr := range trs {
				key := tr.AutomationTool
				if tr.Variant != "" {
					key = core.ExecutionLabel(key+":"+tr.ID, tr.Variant)
				}
				byTool[key] = append(byTool[key], tr)
			}
			results := preflightOutput{out: out}
			tools := make([]string, 0, len(byTool))
			for tool := range byTool {
				tools = append(tools, tool)
			}
			sort.Strings(tools)
			for _, tool := range tools {
				ts := byTool[tool]
				if ts[0].AutomationTool == "" || ts[0].AutomationTool == core.CheckManual {
					for _, tr := range ts {
						results.add("SKIP", core.ExecutionLabel(tr.ID, tr.Variant), "no automation tool; manual check required")
					}
					continue
				}
				ti, ok := orchestrator.ResolveTool(ts[0])
				if !ok {
					results.add("FAIL", tool, "tool not registered")
					continue
				}
				if ti.Preflight == nil {
					results.add("SKIP", tool, "no preflight checks implemented")
					continue
				}
				findings, err := ti.Preflight.Check(cmd.Context(), rc, core.NewBag(), ts)
				for _, f := range findings {
					switch f.Level {
					case "info":
						results.add("PASS", tool, f.Message)
					case "skip", "warn":
						results.add("SKIP", tool, f.Message)
					default:
						results.add("FAIL", tool, f.Message)
					}
				}
				if err != nil {
					results.add("FAIL", tool, err.Error())
				} else if len(findings) == 0 {
					results.add("SKIP", tool, "preflight returned no check results")
				}
			}
			if len(trs) == 0 {
				fmt.Fprintln(out, "No test requirements selected.") //nolint:errcheck // follow-up: check CLI writes
			}
			return results.finish()
		},
	}
	cmd.Flags().StringVar(&catalogPath, "catalog", "", "path to catalog.json (required)")
	cmd.Flags().StringVar(&planPath, "plan", "", "path to a test plan (YAML); provides selection/backend defaults")
	cmd.Flags().StringVar(&backendsPath, "backends", "", "path to a backends file (YAML)")
	cmd.Flags().StringVar(&backendName, "backend", "", "name of the active storage backend (overrides the plan)")
	cmd.Flags().StringSliceVar(&filter.Tools, "tool", nil, "restrict to these automation tools")
	cmd.Flags().StringSliceVar(&filter.IDs, "id", nil, "restrict to these TR ids")
	cmd.Flags().IntSliceVar(&filter.PartnerLevels, "partner-level", nil, "restrict to these partner levels (1|2|3)")
	cmd.Flags().BoolVarP(&verbose, "verbose", "v", false, "debug logging")
	_ = cmd.MarkFlagRequired("catalog")
	return cmd
}

func newRunCmd() *cobra.Command {
	var (
		cfg              config.Config
		thresholdsPath   string
		outputDir        string
		verbose          bool
		planPath         string
		backendsPath     string
		backendName      string
		attestationsPath string
		attnQuestions    string
		noAttestations   bool
		scenario         string
		continueFrom     string
		retryFailed      bool
		skipEnv          bool
		runTimeout       time.Duration
	)
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Run selected certification tests and emit a report",
		RunE: func(cmd *cobra.Command, _ []string) (runErr error) {
			var logFile io.WriteCloser
			var closeLog func()
			if outputDir != "" {
				root, err := safefs.OpenRootDir(outputDir, 0o755)
				if err != nil {
					return err
				}
				f, err := root.OpenFile("run.log", os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
				if err != nil {
					_ = root.Close()
					return err
				}
				logFile = f
				closeLog = func() {
					_ = f.Close()
					_ = root.Close()
				}
				defer func() {
					if runErr != nil {
						_, _ = fmt.Fprintf(logFile, "error: %v\n", runErr)
					}
					closeLog()
				}()
			}
			logger := newLoggerWithOutput(verbose, logFile)
			resolved, err := configureExecution(cmd, &cfg, planPath, backendsPath, &backendName, true, logger)
			if err != nil {
				return err
			}
			if cmd.Flags().Changed("scenario") {
				cfg.Scenario = scenario
			}

			cat, err := catalog.Load(cfg.CatalogPath)
			if err != nil {
				return err
			}

			// Resolve thresholds: --thresholds flag, then HARNESS_THRESHOLDS.
			path := thresholdsPath
			if path == "" {
				path = os.Getenv("HARNESS_THRESHOLDS")
			}

			// Build the execution TRs: join if a thresholds bundle is supplied
			// (verifies provenance/commit match + id parity), else catalog-only.
			var trs []core.TestRequirement
			if path != "" {
				th, err := thresholds.Load(path)
				if err != nil {
					return err
				}
				trs, err = kb.Join(cat, th)
				if err != nil {
					return err
				}
			} else {
				trs = kb.FromCatalog(cat)
			}

			selected := orchestrator.SelectTRs(trs, cfg.Filter)
			if len(selected) == 0 {
				return fmt.Errorf("no test requirements selected")
			}
			// Refuse a run that needs SLA/check numbers without a bundle (5273).
			if path == "" {
				for _, tr := range selected {
					if tr.SLACount > 0 || tr.ChecksCount > 0 {
						return fmt.Errorf("TR %q has scorable criteria (%d sla, %d checks); supply --thresholds or HARNESS_THRESHOLDS",
							tr.ID, tr.SLACount, tr.ChecksCount)
					}
				}
			}

			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			runID := time.Now().UTC().Format("20060102-150405")
			workdir := cfg.WorkDir
			if workdir == "" {
				workdir = filepath.Join(os.TempDir(), "harness-"+runID)
			}
			// Absolute: tools use it as both cmd.Dir and a results path (must agree).
			if abs, err := filepath.Abs(workdir); err == nil {
				workdir = abs
			}
			rc := &core.RunCtx{RunID: runID, WorkDir: workdir, Logger: logger, LogOutput: logFile, Backend: resolved}

			// Resolve the prior report and self attestations before running cluster
			// tests, so the human gate is the first run phase.
			var prior *core.Report
			if continueFrom != "" {
				p, err := report.ReadJSON(continueFrom)
				if err != nil {
					return err
				}
				if resolved != nil && p.Environment.Platform.Backend != "" && p.Environment.Platform.Backend != resolved.Name {
					return fmt.Errorf("--continue-from: prior report backend %q != current backend %q; refusing to merge mismatched runs",
						p.Environment.Platform.Backend, resolved.Name)
				}
				prior = &p
			}
			if prior != nil {
				before := len(selected)
				selected = orchestrator.ResumeExecutionsWithOptions(selected, *prior, cfg.Scenario, retryFailed)
				logger.Info("continue-from: selecting incomplete executions", "previously_selected", before, "resuming", len(selected), "skipped_completed", before-len(selected), "scenario", cfg.Scenario)
			}
			var attestations []core.Attestation
			switch {
			case prior != nil && len(prior.Attestations) > 0:
				logger.Info("continue-from: inheriting attestations from prior report", "count", len(prior.Attestations))
			case attestationsPath != "":
				atts, err := loadAttestations(attestationsPath)
				if err != nil {
					return err
				}
				attestations = atts
			case noAttestations:
			case isInteractive(os.Stdin):
				qs, err := attestation.LoadQuestions(attnQuestions)
				if err != nil {
					return err
				}
				atts, err := attestation.Prompt(ctx, qs, os.Stdin, cmd.ErrOrStderr())
				if err != nil {
					return err
				}
				attestations = atts
			default:
				logger.Warn("no attestations provided and not interactive; skipping (use --attestations or --no-attestations to silence)")
			}

			partKind := "initial"
			if prior != nil {
				partKind = "resume"
			}
			startedAt := time.Now().UTC().Format(time.RFC3339)
			initial := report.NewRunning(runID, cat.SchemaVersion, cat.Provenance, selected, partKind, startedAt)
			for i := range initial.Executions {
				initial.Executions[i].Scenario = cfg.Scenario
			}
			if prior != nil {
				initial.Attestations = prior.Attestations
				merged, mergeErr := report.Merge(*prior, initial)
				if mergeErr != nil {
					return mergeErr
				}
				initial = merged
			} else {
				initial.Attestations = attestations
			}
			checkpoint := newReportCheckpoint(outputDir, initial)
			if outputDir != "" {
				checkpoint.Save(initial)
				if checkpointErr := checkpoint.Err(); checkpointErr != nil {
					return fmt.Errorf("write initial report checkpoint: %w", checkpointErr)
				}
			}
			finalOutputWritten := false
			defer func() {
				if !finalOutputWritten && runErr != nil {
					checkpoint.Finalize(core.RunStatusPartial, runErr.Error())
				}
			}()

			rc.PartKind = partKind
			rc.Checkpoint = checkpoint.Save
			rc.RecordIncident = checkpoint.RecordIncident
			runCtx := ctx
			var cancelRun context.CancelFunc
			if runTimeout > 0 {
				runCtx, cancelRun = context.WithTimeout(ctx, runTimeout)
				defer cancelRun()
			}
			rep, err := orchestrator.Run(runCtx, cfg, trs, cat.Provenance, cat.SchemaVersion, rc)
			if err != nil {
				return err
			}
			if checkpointErr := checkpoint.Err(); checkpointErr != nil {
				return fmt.Errorf("write report checkpoint: %w", checkpointErr)
			}

			// Optionally continue from a prior report: reuse its environment (no
			// second collection) and merge this run's verdicts into it. See ADR-0014.
			// Enrich the report with the run environment (auto-collected from the
			// cluster). Best-effort; see ADR-0014. The CSI driver is resolved from the
			// StorageClass under test (the active backend), not the cluster default.
			// Skipped when continuing from a prior report (its environment is reused)
			// or when --skip-env is set.
			switch {
			case prior != nil:
				logger.Info("continue-from: reusing environment from prior report", "path", continueFrom)
			case skipEnv:
				logger.Info("skipping environment collection (--skip-env)")
			default:
				scUnderTest := ""
				if resolved != nil {
					scUnderTest = resolved.StorageClass
				}
				if cli := clustercheck.KubeCLI(); cli != "" {
					rep.Environment = clustercheck.CollectEnvironment(ctx, cli, scUnderTest)
				}
				if resolved != nil {
					rep.Environment.Platform.Backend = resolved.Name
				}
			}

			rep.Attestations = attestations

			if prior != nil {
				merged, err := report.Merge(*prior, rep)
				if err != nil {
					return err
				}
				rep = merged
			}

			if outputDir != "" {
				var savedAttestations []core.Attestation
				if len(attestations) > 0 && attestationsPath == "" {
					savedAttestations = attestations
				}
				if err := writeReportFiles(outputDir, rep, savedAttestations); err != nil {
					return err
				}
				finalOutputWritten = true
			}
			if err := report.WriteMarkdown(cmd.OutOrStdout(), rep); err != nil {
				return err
			}

			if rep.Status != core.RunStatusComplete {
				return fmt.Errorf("run did not complete: status=%s reason=%s", rep.Status, rep.CompletionReason)
			}
			if rep.Summary.Fail > 0 || rep.Summary.Error > 0 {
				return fmt.Errorf("certification failed: %d fail, %d error", rep.Summary.Fail, rep.Summary.Error)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&cfg.CatalogPath, "catalog", "", "path to catalog.json (required)")
	cmd.Flags().StringVar(&thresholdsPath, "thresholds", "", "path to real thresholds.json (or set HARNESS_THRESHOLDS)")
	cmd.Flags().StringVar(&cfg.WorkDir, "workdir", "", "working directory for run artifacts")
	cmd.Flags().IntVar(&cfg.Concurrency, "concurrency", 1, "max tools run in parallel (respects per-tool parallel_safe/exclusivity_groups)")
	cmd.Flags().StringSliceVar(&cfg.ExtraMetrics, "report-metric", nil, "add report metrics for each selected TR (name or name@percentile; repeat or comma-separate)")
	cmd.Flags().StringVar(&outputDir, "output", "", "directory to write reports and run.log")
	cmd.Flags().StringVar(&planPath, "plan", "", "path to a test plan (YAML); provides selection/concurrency/backend defaults")
	cmd.Flags().StringVar(&backendsPath, "backends", "", "path to a backends file (YAML)")
	cmd.Flags().StringVar(&backendName, "backend", "", "name of the active storage backend (overrides the plan)")
	cmd.Flags().StringVar(&attestationsPath, "attestations", "", "path to attestation JSON (self-reported, unobservable claims)")
	cmd.Flags().StringVar(&attnQuestions, "attestation-questions", "", "path to a custom attestation question set (YAML); defaults to the built-in set")
	cmd.Flags().BoolVar(&noAttestations, "no-attestations", false, "do not prompt for manual attestations")
	cmd.Flags().StringVar(&scenario, "scenario", "", "run label stamped onto every verdict (e.g. under-pressure); overrides the plan")
	cmd.Flags().StringVar(&continueFrom, "continue-from", "", "path to a prior report.json to reuse its environment and merge this run into")
	cmd.Flags().BoolVar(&retryFailed, "retry-failed", false, "with --continue-from, rerun previously completed fail/error executions")
	cmd.Flags().BoolVar(&skipEnv, "skip-env", false, "skip cluster environment collection")
	cmd.Flags().DurationVar(&runTimeout, "timeout", 0, "cancel the run after this duration (0 means no harness timeout)")
	cmd.Flags().StringSliceVar(&cfg.Filter.Tools, "tool", nil, "restrict to these automation tools")
	cmd.Flags().StringSliceVar(&cfg.Filter.IDs, "id", nil, "restrict to these TR ids")
	cmd.Flags().IntSliceVar(&cfg.Filter.PartnerLevels, "partner-level", nil, "restrict to these partner levels (1|2|3)")
	cmd.Flags().BoolVarP(&verbose, "verbose", "v", false, "debug logging")
	_ = cmd.MarkFlagRequired("catalog")
	return cmd
}

func newAttestCmd() *cobra.Command {
	var (
		reportPath       string
		attestationsPath string
		attnQuestions    string
		outputDir        string
	)
	cmd := &cobra.Command{
		Use:   "attest",
		Short: "Add attestations to an existing report without rerunning tests",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			rep, err := report.ReadJSON(reportPath)
			if err != nil {
				return err
			}

			var attestations []core.Attestation
			switch {
			case attestationsPath != "":
				attestations, err = loadAttestations(attestationsPath)
				if err != nil {
					return err
				}
			case isInteractive(os.Stdin):
				qs, loadErr := attestation.LoadQuestions(attnQuestions)
				if loadErr != nil {
					return loadErr
				}
				attestations, err = attestation.Prompt(cmd.Context(), qs, os.Stdin, cmd.ErrOrStderr())
				if err != nil {
					return err
				}
			default:
				return fmt.Errorf("provide --attestations or run interactively to add attestations")
			}
			if len(attestations) == 0 {
				return fmt.Errorf("no attestations provided")
			}

			rep.Attestations = attestations
			if outputDir == "" {
				outputDir = filepath.Dir(reportPath)
			}
			if err := writeReportFiles(outputDir, rep, attestations); err != nil {
				return err
			}
			return report.WriteMarkdown(cmd.OutOrStdout(), rep)
		},
	}
	cmd.Flags().StringVar(&reportPath, "report", "", "path to an existing report.json")
	cmd.Flags().StringVar(&attestationsPath, "attestations", "", "path to attestation JSON (self-reported, unobservable claims)")
	cmd.Flags().StringVar(&attnQuestions, "attestation-questions", "", "path to a custom attestation question set (YAML); defaults to the built-in set")
	cmd.Flags().StringVar(&outputDir, "output", "", "directory to write report.json / report.md / report.junit.xml (defaults to the input report directory)")
	_ = cmd.MarkFlagRequired("report")
	return cmd
}

// isInteractive reports whether f is a terminal (character device), so the run
// only prompts for attestations when a human can answer — CI and replay skip it.
func isInteractive(f *os.File) bool {
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

func writeReportFiles(outputDir string, rep core.Report, attestations []core.Attestation) error {
	report.RefreshLifecycle(&rep)
	if len(attestations) > 0 {
		if err := attestation.WriteFile(filepath.Join(outputDir, "attestations.json"), attestations); err != nil {
			return err
		}
	}
	if err := safefs.WriteWriterAtomic(filepath.Join(outputDir, "report.json"), 0o600, func(w io.Writer) error {
		return report.WriteJSON(w, rep)
	}); err != nil {
		return err
	}
	if err := safefs.WriteWriterAtomic(filepath.Join(outputDir, "report.md"), 0o600, func(w io.Writer) error {
		return report.WriteMarkdown(w, rep)
	}); err != nil {
		return err
	}
	return safefs.WriteWriterAtomic(filepath.Join(outputDir, "report.junit.xml"), 0o600, func(w io.Writer) error {
		return (report.JUnitExporter{}).Export(w, rep)
	})
}

// loadAttestations reads a partner attestation file of the form
// {"signed_by": "...", "claims": {"<claim>": "<value>", ...}} into sorted
// core.Attestation entries. These are self-reported, unobservable claims only
// (ADR-0014).
func loadAttestations(path string) ([]core.Attestation, error) {
	b, err := safefs.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("attestations: %w", err)
	}
	var doc struct {
		SignedBy string            `json:"signed_by"`
		Claims   map[string]string `json:"claims"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, fmt.Errorf("attestations: %w", err)
	}
	keys := make([]string, 0, len(doc.Claims))
	for k := range doc.Claims {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]core.Attestation, 0, len(keys))
	for _, k := range keys {
		out = append(out, core.Attestation{Claim: k, Value: doc.Claims[k], SignedBy: doc.SignedBy})
	}
	return out, nil
}

func configureExecution(cmd *cobra.Command, cfg *config.Config, planPath, backendsPath string, backendName *string, applyConcurrency bool, logger *slog.Logger) (*core.ResolvedBackend, error) {
	if planPath != "" {
		p, err := plan.Load(planPath)
		if err != nil {
			return nil, err
		}
		if !cmd.Flags().Changed("tool") && !cmd.Flags().Changed("id") && !cmd.Flags().Changed("partner-level") {
			cfg.Filter = p.Filter()
		}
		if applyConcurrency && !cmd.Flags().Changed("concurrency") && p.Concurrency > 0 {
			cfg.Concurrency = p.Concurrency
		}
		if !cmd.Flags().Changed("backend") && p.Backend != "" {
			*backendName = p.Backend
		}
		cfg.Scenario = p.Scenario
		cfg.Overrides = p.Overrides
		cfg.Variants = p.Variants
		cfg.Scheduling = p.SchedulingOverrides()
		cfg.ReportMetrics = p.ReportMetrics
		logger.Info("loaded plan", "name", p.Name, "path", planPath)
	}
	if *backendName == "" {
		return nil, nil
	}
	if backendsPath == "" {
		return nil, fmt.Errorf("backend %q selected but no --backends file given", *backendName)
	}
	set, err := backend.Load(backendsPath)
	if err != nil {
		return nil, err
	}
	b, ok := set.Get(*backendName)
	if !ok {
		return nil, fmt.Errorf("backend %q not found in %s", *backendName, backendsPath)
	}
	resolved, err := backend.Resolve(b, nil, logger)
	if err != nil {
		return nil, err
	}
	logger.Info("using backend", "name", resolved.Name, "vendor", resolved.Vendor, "storage_class", resolved.StorageClass)
	return resolved, nil
}
