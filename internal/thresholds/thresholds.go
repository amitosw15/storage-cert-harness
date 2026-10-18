// Package thresholds loads and validates the runtime-supplied sensitive
// thresholds.json — the "what passing means" file (sla numbers + full check
// definitions). It is never committed in the clear; the harness receives it via
// --thresholds / HARNESS_THRESHOLDS. See decisions/0004, 0006, and 5273.
package thresholds

import (
	"encoding/json"
	"fmt"

	"github.com/openshift-storage-cert/storage-cert-harness/internal/core"
	"github.com/openshift-storage-cert/storage-cert-harness/internal/safefs"
)

// SupportedSchemaVersion is the export schema this build understands.
const SupportedSchemaVersion = "2.0"

// TR is one thresholds per-TR record: the sensitive numeric bars for a TR. Bars
// may be when-scoped (multiple per metric, disjoint). Post-ADR-0013 thresholds
// carries no checks (they moved to the catalog). List order is preserved.
type TR struct {
	ID  string     `json:"id"`
	SLA []core.SLA `json:"sla,omitempty"`
}

// Doc is the top-level thresholds.json document.
type Doc struct {
	SchemaVersion    string          `json:"schema_version"`
	Provenance       core.Provenance `json:"provenance"`
	TestRequirements []TR            `json:"test_requirements"`
}

// Load reads, validates, and version-checks a thresholds bundle from path.
func Load(path string) (*Doc, error) {
	b, err := safefs.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("thresholds: read %s: %w", path, err)
	}
	var d Doc
	if err := json.Unmarshal(b, &d); err != nil {
		return nil, fmt.Errorf("thresholds: parse %s: %w", path, err)
	}
	if err := d.Validate(); err != nil {
		return nil, fmt.Errorf("thresholds: %s: %w", path, err)
	}
	return &d, nil
}

// Validate performs structural + version checks. Unknown sla types / check kinds
// are tolerated (forward-compatibility); only clearly-broken records fail.
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
		for j, s := range tr.SLA {
			if s.Metric == "" {
				return fmt.Errorf("%s sla[%d]: missing metric", tr.ID, j)
			}
			switch s.Operator {
			case "<=", "<", ">=", ">", "==":
			default:
				return fmt.Errorf("%s sla[%d] (%s): unknown operator %q", tr.ID, j, s.Metric, s.Operator)
			}
		}
	}
	return nil
}
