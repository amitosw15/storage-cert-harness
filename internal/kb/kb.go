// Package kb joins the two KB export files (catalog.json + thresholds.json) into
// the harness's execution model. The files are joinable on TR id and MUST share
// the same provenance.kb_git_commit — a mismatch means one is stale. See
// decisions/0006 and 5273.
package kb

import (
	"fmt"
	"sort"

	"github.com/openshift-storage-cert/storage-cert-harness/internal/catalog"
	"github.com/openshift-storage-cert/storage-cert-harness/internal/core"
	"github.com/openshift-storage-cert/storage-cert-harness/internal/thresholds"
)

// FromCatalog builds TRs from the catalog alone (no sla/checks). Used by
// catalog-only operations such as `list` and `preflight`.
func FromCatalog(cat *catalog.Doc) []core.TestRequirement {
	out := make([]core.TestRequirement, 0, len(cat.TestRequirements))
	for _, c := range cat.TestRequirements {
		out = append(out, trFromCatalog(c))
	}
	return out
}

// Join merges catalog metadata with thresholds definitions. It verifies both
// files share schema_version and provenance.kb_git_commit, and that their id
// sets match 1:1, then returns the merged, execution-ready TRs (sorted by id).
func Join(cat *catalog.Doc, th *thresholds.Doc) ([]core.TestRequirement, error) {
	if cat.SchemaVersion != th.SchemaVersion {
		return nil, fmt.Errorf("schema_version mismatch: catalog %q vs thresholds %q",
			cat.SchemaVersion, th.SchemaVersion)
	}
	if cat.Provenance.KBGitCommit != th.Provenance.KBGitCommit {
		return nil, fmt.Errorf("stale export: catalog kb_git_commit %q != thresholds %q — re-export from the same KB commit",
			cat.Provenance.KBGitCommit, th.Provenance.KBGitCommit)
	}

	thByID := make(map[string]thresholds.TR, len(th.TestRequirements))
	for _, t := range th.TestRequirements {
		thByID[t.ID] = t
	}
	catIDs := make(map[string]bool, len(cat.TestRequirements))

	out := make([]core.TestRequirement, 0, len(cat.TestRequirements))
	for _, c := range cat.TestRequirements {
		catIDs[c.ID] = true
		tr := trFromCatalog(c)
		t, ok := thByID[c.ID]
		if !ok {
			return nil, fmt.Errorf("TR %q present in catalog but missing from thresholds", c.ID)
		}
		tr.Bars = t.SLA // sensitive numeric bars; descriptors + checks come from the catalog
		out = append(out, tr)
	}
	for id := range thByID {
		if !catIDs[id] {
			return nil, fmt.Errorf("TR %q present in thresholds but missing from catalog", id)
		}
	}

	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func trFromCatalog(c catalog.TR) core.TestRequirement {
	tr := core.TestRequirement{
		ID:             c.ID,
		Title:          c.Title,
		Description:    c.Description,
		Category:       c.Category,
		PartnerLevel:   c.PartnerLevel,
		Verified:       c.Verified,
		AutomationTool: c.AutomationTool,
		SLAStatus:      c.SLAStatus,
		SLACount:       c.SLACount,
		ChecksStatus:   c.ChecksStatus,
		ChecksCount:    c.ChecksCount,
		SLAs:           descriptors(c),
		Checks:         c.Checks,
		ParamSpec:      c.Params,
		Params:         paramDefaults(c.Params),
	}
	return tr
}

// descriptors returns the catalog SLA metric descriptors with measured_by
// defaulted: absent ⇒ [automation_tool] (the TR's own tool measures it); an
// explicit [] is kept (nothing measures it yet). See decisions/0013.
func descriptors(c catalog.TR) []core.SLA {
	if len(c.SLA) == 0 {
		return nil
	}
	out := make([]core.SLA, len(c.SLA))
	copy(out, c.SLA)
	for i := range out {
		if out[i].MeasuredBy == nil && c.AutomationTool != "" {
			out[i].MeasuredBy = []string{c.AutomationTool}
		}
	}
	return out
}

// paramDefaults seeds resolved run params from the catalog defaults. A plan's
// overrides are merged on top later (orchestrator.applyOverrides). A required
// param with no default is intentionally absent — the plan must supply it.
func paramDefaults(spec map[string]core.ParamSpec) map[string]any {
	if len(spec) == 0 {
		return nil
	}
	out := make(map[string]any, len(spec))
	for name, ps := range spec {
		if ps.Default != nil {
			out[name] = ps.Default
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
