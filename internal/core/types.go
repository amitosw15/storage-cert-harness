// Package core holds the harness domain types. It has no dependency on any
// other internal package — everything else depends on it, nothing the other
// way. See decisions/0003 (architecture) and 0006 (KB export v2.0 contract).
package core

import (
	"encoding/json"
	"io"
	"log/slog"
)

// Outcome is the result of scoring a single item after evaluation.
type Outcome string

const (
	OutcomePass  Outcome = "pass"
	OutcomeFail  Outcome = "fail"
	OutcomeError Outcome = "error"
	OutcomeSkip  Outcome = "skip"
)

// RunStatus describes the lifecycle state of the complete harness run.
type RunStatus string

const (
	RunStatusRunning   RunStatus = "running"
	RunStatusComplete  RunStatus = "complete"
	RunStatusPartial   RunStatus = "partial"
	RunStatusFailed    RunStatus = "failed"
	RunStatusAbandoned RunStatus = "abandoned"
)

// RunHealth describes whether the execution itself was clean, independently of
// the pass/fail outcomes of individual certification checks.
type RunHealth string

const (
	RunHealthHealthy   RunHealth = "healthy"
	RunHealthDegraded  RunHealth = "degraded"
	RunHealthUnhealthy RunHealth = "unhealthy"
)

// ExecutionState is the persisted state of one selected TR execution. It is
// intentionally separate from Verdict: a queued or interrupted execution may
// have no graded verdicts yet.
type ExecutionState string

const (
	ExecutionQueued    ExecutionState = "queued"
	ExecutionRunning   ExecutionState = "running"
	ExecutionCompleted ExecutionState = "completed"
	ExecutionAborted   ExecutionState = "aborted"
)

// RunExecution records progress for one selected TR/variant.
type RunExecution struct {
	TR        string         `json:"tr"`
	Variant   string         `json:"variant,omitempty"`
	Scenario  string         `json:"scenario,omitempty"`
	State     ExecutionState `json:"state"`
	Outcome   Outcome        `json:"outcome,omitempty"`
	Stage     string         `json:"stage,omitempty"`
	StartedAt string         `json:"started_at,omitempty"`
	EndedAt   string         `json:"ended_at,omitempty"`
	Reason    string         `json:"reason,omitempty"`
}

// RunPart identifies one harness process/invocation that contributed to a
// composed report. A resumed run adds another part rather than erasing the
// original execution history.
type RunPart struct {
	PartID    string    `json:"part_id"`
	Kind      string    `json:"kind,omitempty"` // initial | resume | recovery | retry
	StartedAt string    `json:"started_at,omitempty"`
	EndedAt   string    `json:"ended_at,omitempty"`
	Status    RunStatus `json:"status"`
	Reason    string    `json:"reason,omitempty"`
}

// RunComposition describes how many independent run parts produced a report.
type RunComposition struct {
	PartsCount int       `json:"parts_count"`
	Parts      []RunPart `json:"parts,omitempty"`
}

// Incident records an abnormal run condition, such as a stage timeout or a
// cancelled execution. Details belong in JSON; JUnit exposes only summary flags
// and the report.json filename.
type Incident struct {
	ID                  string  `json:"id"`
	Type                string  `json:"type"`
	Severity            string  `json:"severity,omitempty"`
	TR                  string  `json:"tr,omitempty"`
	Variant             string  `json:"variant,omitempty"`
	Tool                string  `json:"tool,omitempty"`
	Stage               string  `json:"stage,omitempty"`
	Message             string  `json:"message"`
	StartedAt           string  `json:"started_at,omitempty"`
	EndedAt             string  `json:"ended_at,omitempty"`
	DurationS           float64 `json:"duration_s,omitempty"`
	Resolved            bool    `json:"resolved,omitempty"`
	BlocksCertification bool    `json:"blocks_certification,omitempty"`
	Impact              string  `json:"impact,omitempty"`
	Evidence            string  `json:"evidence,omitempty"`
}

// KB status values (catalog sla_status/checks_status, and per-check status).
const (
	StatusDefined       = "defined"
	StatusToBeValidated = "to-be-validated"
)

// Check kinds (thresholds.json checks[].kind).
const (
	CheckCapability = "capability"
	CheckSuite      = "suite"
	CheckScenario   = "scenario"
	CheckManual     = "manual"
)

// SLA is one quantitative gate within a TR. Post-ADR-0013 it serves two roles,
// distinguished by which fields are set:
//
//   - a catalog **descriptor** (catalog.json sla[]): Metric, Type, Unit,
//     Percentile, MeasuredBy — the "what is measured", no number.
//   - a thresholds **bar** (thresholds.json sla[]): Metric, Percentile, When,
//     Operator, Value, Unit, Condition — the "what passing means", the only
//     place a number ever lives.
//
// A TR carries its descriptors in SLAs and its bars in Bars; the grader pairs a
// descriptor with the one bar whose When ⊆ run params. Value is a pointer so a
// null bar is distinguishable from 0. See decisions/0006 and 0013.
type SLA struct {
	Metric     string         `json:"metric"`
	Type       string         `json:"type,omitempty"`     // latency|throughput|duration|count|ratio
	Operator   string         `json:"operator,omitempty"` // bar: < <= > >= ==
	Value      *float64       `json:"value,omitempty"`    // bar: null => not set
	Unit       string         `json:"unit,omitempty"`
	Percentile string         `json:"percentile,omitempty"`  // e.g. p99
	Condition  string         `json:"condition,omitempty"`   // bar: free-text caveat
	MeasuredBy []string       `json:"measured_by,omitempty"` // descriptor: tools that produce this metric
	When       map[string]any `json:"when,omitempty"`        // bar: applies iff When ⊆ run params
}

// ParamSpec is the catalog-declared spec for one run parameter: whether a run
// must supply it and the KB-owned default. A required param with no default MUST
// be provided by a plan override (preflight error otherwise). See decisions/0013.
type ParamSpec struct {
	Required bool `json:"required,omitempty"`
	Default  any  `json:"default,omitempty"`
}

// Check is one non-quantitative criterion within a TR (thresholds.json checks[]).
// Dispatched on Kind. Expectation is free-text success criteria — always shown.
type Check struct {
	ID          string   `json:"id"`
	Kind        string   `json:"kind"` // capability|suite|scenario|manual
	Name        string   `json:"name,omitempty"`
	Expectation string   `json:"expectation,omitempty"`
	Blocking    *bool    `json:"blocking,omitempty"`
	Status      string   `json:"status,omitempty"` // defined | to-be-validated
	Tool        string   `json:"tool,omitempty"`
	References  []string `json:"references,omitempty"`
}

// TestRequirement is the merged execution view of a KB test requirement: catalog
// metadata joined with its thresholds definitions. SLAs/Checks are empty on a
// catalog-only load. See decisions/0006.
type TestRequirement struct {
	Variant        string  `json:"-"`
	ID             string  `json:"id"`
	Title          string  `json:"title,omitempty"`
	Description    string  `json:"description,omitempty"`
	Category       string  `json:"category,omitempty"`
	PartnerLevel   int     `json:"partner_level,omitempty"`
	Verified       string  `json:"verified,omitempty"`
	AutomationTool string  `json:"automation_tool,omitempty"`
	SLAStatus      string  `json:"sla_status,omitempty"`
	SLACount       int     `json:"sla_count,omitempty"`
	ChecksStatus   string  `json:"checks_status,omitempty"`
	ChecksCount    int     `json:"checks_count,omitempty"`
	SLAs           []SLA   `json:"sla,omitempty"`    // catalog metric descriptors
	Bars           []SLA   `json:"-"`                // thresholds bars (sensitive; joined at run time)
	Checks         []Check `json:"checks,omitempty"` // catalog check descriptors (ADR-0013)

	// ParamSpec is the catalog-declared param spec (required/default) per name.
	// Resolved run values live in Params (catalog defaults merged with plan
	// overrides). See decisions/0013.
	ParamSpec map[string]ParamSpec `json:"-"`

	// Params are the resolved per-TR tool params: catalog defaults overlaid with a
	// plan's overrides — never serialized to the KB.
	Params map[string]any `json:"-"`
}

// Metric is a single normalized measurement emitted by a ResultParser. It is
// matched to an SLA by Name (and Percentile when the SLA sets one).
type Metric struct {
	Name       string  `json:"name"`
	Value      float64 `json:"value"`
	Unit       string  `json:"unit,omitempty"`
	Percentile string  `json:"percentile,omitempty"`
}

// LogBundle is the raw material a LogCollector hands to a ResultParser.
type LogBundle struct {
	TRID string            `json:"tr_id,omitempty"`
	Refs []string          `json:"refs,omitempty"` // artifact references (paths, object keys)
	Data map[string][]byte `json:"-"`              // in-memory payloads keyed by name
}

// TestResult is the normalized output of a ResultParser for one TR. Metrics feed
// SLA evaluation; Checks maps a check id to the tool's native outcome for
// capability/suite/scenario checks. See decisions/0006.
type TestResult struct {
	TRID      string             `json:"tr_id"`
	Metrics   []Metric           `json:"metrics,omitempty"`
	Checks    map[string]Outcome `json:"checks,omitempty"`
	Native    Outcome            `json:"native,omitempty"`
	Artifacts []string           `json:"artifacts,omitempty"`
	Raw       json.RawMessage    `json:"raw,omitempty"`
}

// Verdict is the graded result of one scorable item (a single SLA or check)
// within a TR.
type Verdict struct {
	Variant   string  `json:"variant,omitempty"`
	TR        string  `json:"tr"`                 // TR id
	Level     int     `json:"level,omitempty"`    // partner level of the TR (stamped by the orchestrator)
	Scenario  string  `json:"scenario,omitempty"` // run label (e.g. "under-pressure"); distinguishes merged runs of the same TR
	Item      string  `json:"item"`               // e.g. "sla:latency_ms@p99" or "check:snapshot-restore"
	Outcome   Outcome `json:"outcome"`
	DurationS float64 `json:"duration_s"` // wall-clock of the TR's tool run; always reported, SLA or not
	Reason    string  `json:"reason,omitempty"`
	Expected  string  `json:"expected,omitempty"`
	Actual    string  `json:"actual,omitempty"`
}

// Provenance records where the KB export came from, stamped into every report
// for auditability (5273 / decisions/0004, 0006).
type Provenance struct {
	KBGitCommit   string `json:"kb_git_commit,omitempty"`
	KBCommitDate  string `json:"kb_commit_date,omitempty"`
	GeneratedDate string `json:"generated_date,omitempty"`
}

// Finding is one preflight check result. Successful checks use info, failed
// checks use error, and checks that did not run use skip with a reason.
// Legacy warn findings are non-fatal and displayed as SKIP by the CLI.
type Finding struct {
	Level   string `json:"level"` // info | error | skip | warn
	Message string `json:"message"`
}

// Report is the certification run output.
type Report struct {
	RunID            string         `json:"run_id"`
	Status           RunStatus      `json:"status"`
	Health           RunHealth      `json:"health"`
	Certifiable      bool           `json:"certifiable"`
	CompletionReason string         `json:"completion_reason,omitempty"`
	SchemaVersion    string         `json:"schema_version,omitempty"`
	Provenance       Provenance     `json:"provenance"`             // where the requirements came from (KB)
	Environment      Environment    `json:"environment"`            // where the run executed (cluster) — ADR-0014
	Attestations     []Attestation  `json:"attestations,omitempty"` // self-reported, unobservable-only — ADR-0014
	Composition      RunComposition `json:"composition"`
	Executions       []RunExecution `json:"executions,omitempty"`
	Incidents        []Incident     `json:"incidents,omitempty"`
	Verdicts         []Verdict      `json:"verdicts"`
	Measurements     []Measurement  `json:"measurements,omitempty"` // catalog metrics plus requested extras
	Warnings         []string       `json:"warnings,omitempty"`     // informational; never affects grading
	Levels           []LevelRollup  `json:"levels,omitempty"`       // verdicts grouped by partner level
	Summary          Summary        `json:"summary"`
}

// Measurement is a value the tool measured, surfaced in the report whether or not
// an SLA bar exists for it (e.g. VM boot time, p99 latency). When an SLA does
// exist, the matching Verdict additionally carries the pass/fail. See ADR-0014.
type Measurement struct {
	Variant    string  `json:"variant,omitempty"`
	TR         string  `json:"tr"`
	Level      int     `json:"level,omitempty"`
	Scenario   string  `json:"scenario,omitempty"`
	Name       string  `json:"name"`
	Value      float64 `json:"value"`
	Unit       string  `json:"unit,omitempty"`
	Percentile string  `json:"percentile,omitempty"`
}

// Summary aggregates verdict counts and the earned certification tier.
type Summary struct {
	Total          int `json:"total"`
	Pass           int `json:"pass"`
	Fail           int `json:"fail"`
	Error          int `json:"error"`
	Skip           int `json:"skip"`
	CertifiedLevel int `json:"certified_level"` // highest cumulative all-pass partner level (0 = none)
}

// LevelRollup is the aggregate outcome of one partner level (all its verdicts).
type LevelRollup struct {
	Level   int     `json:"level"`
	Outcome Outcome `json:"outcome"` // fail if any member fail/error; else pass (skips ignored)
}

// Environment is the run origin, auto-collected from the cluster (ADR-0014). It is
// the run-provenance counterpart to Provenance (the requirements origin).
type Environment struct {
	Hardware Hardware `json:"hardware"`
	Platform Platform `json:"platform"`
}

// Hardware is the physical infra observed on the cluster.
type Hardware struct {
	Nodes int    `json:"nodes"`
	Facts []Fact `json:"facts,omitempty"` // cpu, ram, arch, os_image, ...
}

// Platform is the software-under-test observed on the cluster.
type Platform struct {
	OCPVersion     string             `json:"ocp_version,omitempty"`
	KubeVersion    string             `json:"kube_version,omitempty"`
	CNVVersion     string             `json:"cnv_version,omitempty"`
	CSIDriver      CSIDriverInfo      `json:"csi_driver"`
	Operators      []OperatorInfo     `json:"operators,omitempty"`
	StorageClasses []StorageClassInfo `json:"storage_classes,omitempty"`
	Backend        string             `json:"backend,omitempty"` // active ResolvedBackend name
	Facts          []Fact             `json:"facts,omitempty"`
}

// Fact is a source-tagged environment datum. Source records how it was obtained.
type Fact struct {
	Key    string `json:"key"`
	Value  string `json:"value"`
	Source string `json:"source"`
}

// CSIDriverInfo is read from the CSIDriver object of the StorageClass under test.
type CSIDriverInfo struct {
	Name                 string   `json:"name"`
	Version              string   `json:"version,omitempty"`
	AttachRequired       bool     `json:"attach_required"`
	FSGroupPolicy        string   `json:"fs_group_policy,omitempty"`
	VolumeLifecycleModes []string `json:"volume_lifecycle_modes,omitempty"`
	Capabilities         []string `json:"capabilities,omitempty"`
}

type OperatorInfo struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
	Channel string `json:"channel,omitempty"`
	Source  string `json:"source,omitempty"`
}

type StorageClassInfo struct {
	Name              string            `json:"name"`
	Provisioner       string            `json:"provisioner"`
	IsDefault         bool              `json:"is_default"`
	ReclaimPolicy     string            `json:"reclaim_policy,omitempty"`
	VolumeBindingMode string            `json:"volume_binding_mode,omitempty"`
	Parameters        map[string]string `json:"parameters,omitempty"`
}

// Attestation is a partner claim the harness CANNOT observe from the cluster
// (e.g. the storage-array model, a Reference Architecture document, or a
// published SLA page). Anything observable belongs in Environment instead —
// hence no cross-check field.
type Attestation struct {
	Claim    string `json:"claim"`
	Value    string `json:"value"`
	SignedBy string `json:"signed_by,omitempty"`
}

// RunCtx carries stable, run-scoped dependencies through every stage. It is the
// immutable counterpart to Bag (mutable stage-to-stage state) and to
// context.Context (cancellation/deadlines). A live Kubernetes client will be
// added here once internal/kube is implemented.
type RunCtx struct {
	RunID    string
	PartKind string // initial | resume | recovery | retry
	WorkDir  string
	Logger   *slog.Logger
	// LogOutput receives subprocess stdout/stderr when the CLI is configured to
	// persist a complete run log. It is deliberately not part of the report.
	LogOutput io.Writer
	// Backend is the active storage array configuration for this run, with its
	// secrets already resolved. It is optional (may be nil / empty). See
	// decisions/0005.
	Backend *ResolvedBackend
	// Checkpoint receives durable report snapshots while a run is in progress.
	// The callback must be safe for concurrent calls from scheduled tools.
	Checkpoint func(Report)
	// RecordIncident records a run-level incident without requiring a concrete
	// report implementation in the orchestrator or tool adapters.
	RecordIncident func(Incident)
	// SetStage marks the stage currently executing for one or more TRs. The
	// callback is used to make a stale checkpoint explain where the run stopped.
	SetStage func(string, []TestRequirement)
}

// ToolOutput mirrors a subprocess stream to the terminal and, when configured,
// to the run log. A nil receiver or LogOutput keeps the existing terminal-only
// behavior used by tests and non-CLI callers.
func (r *RunCtx) ToolOutput(terminal io.Writer) io.Writer {
	if r == nil || r.LogOutput == nil {
		return terminal
	}
	return io.MultiWriter(terminal, r.LogOutput)
}

// ExecutionLabel distinguishes named executions while preserving catalog TR IDs.
func ExecutionLabel(id, variant string) string {
	if variant == "" {
		return id
	}
	return id + " [" + variant + "]"
}
