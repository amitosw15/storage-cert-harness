// Package plan loads test plans (YAML): a curated, parameterized selection of
// catalog tests plus run settings and the active backend. A plan carries no
// secrets. See decisions/0005.
package plan

import (
	"fmt"
	"regexp"

	"gopkg.in/yaml.v3"

	"github.com/openshift-storage-cert/storage-cert-harness/internal/config"
	"github.com/openshift-storage-cert/storage-cert-harness/internal/safefs"
)

// SupportedSchemaVersion is the plan schema this build understands.
const SupportedSchemaVersion = "1"

// Selector chooses which catalog test requirements the plan runs. Empty fields
// match all; fields are ANDed, values within a field ORed. Set All to run the
// whole catalog explicitly. See decisions/0006 (TR-centric v2.0 model).
type Selector struct {
	All           bool     `yaml:"all,omitempty"`
	IDs           []string `yaml:"ids,omitempty"`
	Tools         []string `yaml:"tools,omitempty"`
	PartnerLevels []int    `yaml:"partner_levels,omitempty"`
}

// Plan is a run recipe.
type Plan struct {
	SchemaVersion string   `yaml:"schema_version"`
	Name          string   `yaml:"name"`
	Description   string   `yaml:"description,omitempty"`
	Backend       string   `yaml:"backend,omitempty"`  // name of the active backend (optional)
	Scenario      string   `yaml:"scenario,omitempty"` // run label stamped onto verdicts (e.g. "under-pressure")
	Concurrency   int      `yaml:"concurrency,omitempty"`
	Select        Selector `yaml:"select"`
	// ReportMetrics adds informational metrics by TR id (name or name@percentile).
	ReportMetrics map[string][]string `yaml:"report_metrics,omitempty"`
	// Overrides is TR id -> param overrides, merged onto each selected TR.
	Overrides map[string]map[string]any   `yaml:"overrides,omitempty"`
	Variants  map[string][]config.Variant `yaml:"variants,omitempty"`
	// Scheduling overrides a tool's built-in parallel-safety/exclusivity for this
	// run (tool name -> override). See decisions/0006.
	Scheduling map[string]ToolSchedule `yaml:"scheduling,omitempty"`
}

// ToolSchedule is a plan's per-tool override of the ToolIntegration scheduling
// defaults. ParallelSafe nil keeps the code default.
type ToolSchedule struct {
	ParallelSafe      *bool    `yaml:"parallel_safe,omitempty"`
	ExclusivityGroups []string `yaml:"exclusivity_groups,omitempty"`
}

// Load reads and validates a plan YAML file.
func Load(path string) (*Plan, error) {
	data, err := safefs.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read plan %s: %w", path, err)
	}
	var p Plan
	if err := yaml.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("parse plan %s: %w", path, err)
	}
	if err := p.validate(); err != nil {
		return nil, fmt.Errorf("invalid plan %s: %w", path, err)
	}
	return &p, nil
}

func (p *Plan) validate() error {
	if p.SchemaVersion != SupportedSchemaVersion {
		return fmt.Errorf("unsupported schema_version %q (want %q)", p.SchemaVersion, SupportedSchemaVersion)
	}
	if p.Name == "" {
		return fmt.Errorf("missing name")
	}
	for id, variants := range p.Variants {
		if id == "" || len(variants) == 0 {
			return fmt.Errorf("variants[%q] must contain at least one named variant", id)
		}
		seen := map[string]bool{}
		for _, v := range variants {
			if !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`).MatchString(v.Name) {
				return fmt.Errorf("variants[%s]: invalid name %q (use letters, digits, underscores or hyphens)", id, v.Name)
			}
			if seen[v.Name] {
				return fmt.Errorf("variants[%s]: duplicate name %q", id, v.Name)
			}
			seen[v.Name] = true
		}
	}
	s := p.Select
	if !s.All && len(s.IDs) == 0 && len(s.Tools) == 0 && len(s.PartnerLevels) == 0 {
		return fmt.Errorf("select is empty: set all: true or at least one of ids/tools/partner_levels")
	}
	if p.Concurrency < 0 {
		return fmt.Errorf("concurrency must be >= 0")
	}
	return nil
}

// Filter converts the plan's selector to a config.Filter. An All selector yields
// an empty filter (matches everything).
func (p *Plan) Filter() config.Filter {
	if p.Select.All {
		return config.Filter{}
	}
	return config.Filter{
		Tools:         p.Select.Tools,
		IDs:           p.Select.IDs,
		PartnerLevels: p.Select.PartnerLevels,
	}
}

// SchedulingOverrides converts the plan's per-tool scheduling to config.
func (p *Plan) SchedulingOverrides() map[string]config.ToolSchedule {
	if len(p.Scheduling) == 0 {
		return nil
	}
	out := make(map[string]config.ToolSchedule, len(p.Scheduling))
	for tool, s := range p.Scheduling {
		out[tool] = config.ToolSchedule{ParallelSafe: s.ParallelSafe, ExclusivityGroups: s.ExclusivityGroups}
	}
	return out
}
