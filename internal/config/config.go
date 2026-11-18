// Package config holds run configuration assembled from CLI flags, a plan, and
// env. See decisions/0005 (plans/backends) and 0006 (KB v2.0).
package config

// Filter narrows which test requirements a run selects. Empty fields match all;
// fields are ANDed together, values within a field are ORed.
type Filter struct {
	Tools         []string
	IDs           []string // TR ids
	FocusIDs      []string // restrict the selected TRs by exact id
	SkipIDs       []string // omit selected TRs unless explicitly focused
	PartnerLevels []int
}

// ToolSchedule overrides a tool's built-in scheduling defaults for one run.
type ToolSchedule struct {
	ParallelSafe      *bool // nil => keep the ToolIntegration default
	ExclusivityGroups []string
}

// Variant is one named execution of a TR with additional parameter overrides.
type Variant struct {
	Name   string         `yaml:"name"`
	Params map[string]any `yaml:"params,omitempty"`
}

// Config is the resolved configuration for a run.
type Config struct {
	CatalogPath    string
	ThresholdsPath string // sensitive bundle; may be empty for catalog-only/list runs
	WorkDir        string
	Concurrency    int
	Filter         Filter

	// Scenario is an optional run label (e.g. "under-pressure", "idle") stamped
	// onto every verdict/measurement so runs of the same TR stay distinct when
	// their reports are merged. See ADR-0014.
	Scenario string

	// ExtraMetrics adds report-only metric selectors to every selected TR.
	ExtraMetrics []string
	// ReportMetrics adds report-only metric selectors by TR id.
	ReportMetrics map[string][]string

	// Overrides are per-TR tool param overrides (TR id -> params), merged onto
	// the TR before the run. See decisions/0005.
	Overrides map[string]map[string]any
	Variants  map[string][]Variant

	// Scheduling overrides a tool's built-in parallel-safety/exclusivity for
	// this run (tool name -> override). See decisions/0005 + 0006.
	Scheduling map[string]ToolSchedule
}
