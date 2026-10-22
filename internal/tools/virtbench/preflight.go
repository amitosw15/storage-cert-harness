package virtbench

import (
	"context"

	"github.com/openshift-storage-cert/storage-cert-harness/internal/core"
)

func (s *session) Preflight(ctx context.Context, rc *core.RunCtx, trs []core.TestRequirement) ([]core.Finding, error) {
	s.trs = trs
	checks := workloadChecks(rc, s, trs)
	findings := make([]core.Finding, 0, len(checks))
	seen := make(map[string]bool)
	for _, check := range checks {
		if check.Applies != nil {
			if applies, reason := check.Applies(); !applies {
				findings = append(findings, core.Finding{Level: "skip", Message: check.Name + ": " + reason})
				continue
			}
		}
		if check.Key != "" {
			if seen[check.Key] {
				continue
			}
			seen[check.Key] = true
		}
		for _, finding := range check.Run(ctx) {
			findings = append(findings, finding)
			if finding.Level == "error" {
				return findings, nil
			}
		}
	}
	return findings, nil
}
