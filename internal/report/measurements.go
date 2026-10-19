package report

import (
	"fmt"
	"slices"
	"strings"

	"github.com/openshift-storage-cert/storage-cert-harness/internal/core"
)

// SelectMeasurements keeps catalog SLA metrics plus explicitly requested extras.
// A bare name includes all its percentiles. Missing extras are informational;
// grading always uses the full parser results independently of this selection.
func SelectMeasurements(trs []core.TestRequirement, measurements []core.Measurement, extras []string, perTR map[string][]string) ([]core.Measurement, []string) {
	selected := map[string][]core.SLA{}
	requested := map[string]map[string]bool{}
	for _, tr := range trs {
		key := core.ExecutionLabel(tr.ID, tr.Variant)
		selected[key] = slices.Clone(tr.SLAs)
		requested[key] = map[string]bool{}
		for _, list := range [][]string{extras, perTR[tr.ID]} {
			for _, selector := range list {
				requested[key][selector] = false
				name, percentile, _ := strings.Cut(selector, "@")
				selected[key] = append(selected[key], core.SLA{Metric: name, Percentile: percentile})
			}
		}
	}
	var out []core.Measurement
	for _, m := range measurements {
		key := core.ExecutionLabel(m.TR, m.Variant)
		for selector := range requested[key] {
			name, percentile, _ := strings.Cut(selector, "@")
			if m.Name == name && (percentile == "" || m.Percentile == percentile) {
				requested[key][selector] = true
			}
		}
		for _, descriptor := range selected[key] {
			if m.Name == descriptor.Metric && (descriptor.Percentile == "" || m.Percentile == descriptor.Percentile) {
				out = append(out, m)
				break
			}
		}
	}
	var warnings []string
	for tr, selectors := range requested {
		for selector, found := range selectors {
			if !found {
				warnings = append(warnings, fmt.Sprintf("TR %s: requested report metric %q was not produced", tr, selector))
			}
		}
	}
	slices.Sort(warnings)
	return out, warnings
}
