// Package catalog loads and validates the KB-exported catalog.json — the
// publishable "what tests exist" file (per-TR metadata + summary counts, no
// threshold numbers). See decisions/0006 and 5273.
package catalog

import (
	"encoding/json"
	"fmt"

	"github.com/openshift-storage-cert/storage-cert-harness/internal/core"
	"github.com/openshift-storage-cert/storage-cert-harness/internal/safefs"
)

// SupportedSchemaVersion is the export schema this build understands.
const SupportedSchemaVersion = "2.0"

// TR is one catalog per-TR record: publishable metadata plus the self-describing
// run/grade descriptors (params, sla metric descriptors, checks) — but never a
// numeric bar. See decisions/0006 and 0013.
type TR struct {
	ID             string                    `json:"id"`
	Title          string                    `json:"title"`
	Description    string                    `json:"description,omitempty"`
	Category       string                    `json:"category,omitempty"`
	PartnerLevel   int                       `json:"partner_level"`
	Verified       string                    `json:"verified,omitempty"`
	AutomationTool string                    `json:"automation_tool,omitempty"`
	Scenario       string                    `json:"scenario,omitempty"`
	SLAStatus      string                    `json:"sla_status,omitempty"`
	SLACount       int                       `json:"sla_count,omitempty"`
	ChecksStatus   string                    `json:"checks_status,omitempty"`
	ChecksCount    int                       `json:"checks_count,omitempty"`
	Params         map[string]core.ParamSpec `json:"params,omitempty"`
	SLA            []core.SLA                `json:"sla,omitempty"`    // metric descriptors (no value/operator)
	Checks         []core.Check              `json:"checks,omitempty"` // full check descriptors (ADR-0013)
}

// Doc is the top-level catalog.json document.
type Doc struct {
	SchemaVersion    string          `json:"schema_version"`
	Provenance       core.Provenance `json:"provenance"`
	TestRequirements []TR            `json:"test_requirements"`
}

// Load reads and validates catalog.json from path.
func Load(path string) (*Doc, error) {
	b, err := safefs.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("catalog: read %s: %w", path, err)
	}
	var d Doc
	if err := json.Unmarshal(b, &d); err != nil {
		return nil, fmt.Errorf("catalog: parse %s: %w", path, err)
	}
	if err := d.Validate(); err != nil {
		return nil, fmt.Errorf("catalog: %s: %w", path, err)
	}
	return &d, nil
}

// Validate performs structural + version checks. Full JSON Schema validation
// (schemas/catalog.schema.json) is a follow-up.
func (d *Doc) Validate() error {
	if d.SchemaVersion == "" {
		return fmt.Errorf("missing schema_version")
	}
	if d.SchemaVersion != SupportedSchemaVersion {
		return fmt.Errorf("unsupported schema_version %q (want %q)", d.SchemaVersion, SupportedSchemaVersion)
	}
	seen := map[string]bool{}
	for i, tr := range d.TestRequirements {
		if tr.ID == "" {
			return fmt.Errorf("test_requirements[%d]: missing id", i)
		}
		if seen[tr.ID] {
			return fmt.Errorf("duplicate test requirement id %q", tr.ID)
		}
		seen[tr.ID] = true
		// Invariant (ADR-0013): the catalog is publishable — it must never carry a
		// numeric bar. Bars live only in thresholds.json.
		for j, s := range tr.SLA {
			if s.Value != nil || s.Operator != "" {
				return fmt.Errorf("%s sla[%d] (%s): catalog must not carry a numeric bar (value/operator belong in thresholds.json)", tr.ID, j, s.Metric)
			}
		}
	}
	return nil
}
