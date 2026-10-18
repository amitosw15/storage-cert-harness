// Package example is the reference tool integration. Copy this package to add a
// real tool: implement the stage adapters you need, declare the tool's released
// image and the test ids it Provides, and register it in init(). Everything
// here runs with no live cluster so it is safe to use as a smoke test.
//
// Bag contract (see decisions/0003):
//   - provisioner writes  "namespace" (string)
//   - runner       writes "job"       (string), reads "namespace"
//   - teardown     reads  "namespace"
package example

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"

	"github.com/openshift-storage-cert/storage-cert-harness/internal/core"
	"github.com/openshift-storage-cert/storage-cert-harness/internal/registry"
	"github.com/openshift-storage-cert/storage-cert-harness/internal/stages"
)

// sampleResults stands in for output a real Runner's image would produce and a
// LogCollector would fetch from the cluster.
//
//go:embed testdata/sample-results.json
var sampleResults []byte

func init() {
	registry.Register(stages.ToolIntegration{
		Name:         "example",
		Image:        "quay.io/example/harness-example:latest",         // overridable; fallback Containerfile only if no upstream image
		Provides:     []string{"EX-001", "EX-002", "EX-003", "EX-004"}, // TR ids this integration runs
		Preflight:    preflight{},
		Provisioner:  provisioner{},
		Runner:       runner{},
		LogCollector: collector{},
		ResultParser: parser{},
		Teardown:     teardown{},
		ParallelSafe: true, // offline demo tool; safe to overlap
	})
}

type preflight struct{}

func (preflight) Check(_ context.Context, rc *core.RunCtx, _ *core.Bag, _ []core.TestRequirement) ([]core.Finding, error) {
	rc.Logger.Debug("example: preflight ok")
	return []core.Finding{{Level: "info", Message: "example integration ready"}}, nil
}

type provisioner struct{}

func (provisioner) Provision(_ context.Context, rc *core.RunCtx, bag *core.Bag, _ []core.TestRequirement) error {
	ns := "example-" + rc.RunID
	bag.Set("namespace", ns)
	rc.Logger.Debug("example: provisioned", "namespace", ns)
	return nil
}

type runner struct{}

func (runner) Run(_ context.Context, rc *core.RunCtx, bag *core.Bag, _ []core.TestRequirement) (stages.RunHandle, error) {
	ns, _ := core.GetAs[string](bag, "namespace")
	job := "example-job-" + rc.RunID
	bag.Set("job", job)
	rc.Logger.Debug("example: launched", "namespace", ns, "job", job)
	// A real runner would create a Job from ToolIntegration.Image here.
	return stages.RunHandle{ID: job}, nil
}

type collector struct{}

func (collector) Collect(_ context.Context, _ *core.RunCtx, _ *core.Bag, h stages.RunHandle) (core.LogBundle, error) {
	// A real collector would fetch pod logs / artifacts for h.ID.
	return core.LogBundle{
		Refs: []string{h.ID},
		Data: map[string][]byte{"results.json": sampleResults},
	}, nil
}

type parser struct{}

func (parser) Parse(_ context.Context, _ *core.RunCtx, _ *core.Bag, logs core.LogBundle) ([]core.TestResult, error) {
	return ParseResults(logs.Data["results.json"])
}

type teardown struct{}

func (teardown) Teardown(_ context.Context, rc *core.RunCtx, bag *core.Bag) error {
	if ns, ok := core.GetAs[string](bag, "namespace"); ok {
		rc.Logger.Debug("example: torn down", "namespace", ns)
	}
	return nil
}

// rawResults is the on-disk shape the example tool emits. Results are keyed by
// TR id; a TR carries measured Metrics (for sla scoring) and/or Checks (checkID
// -> native outcome, for capability/suite/scenario checks). See decisions/0006.
type rawResults struct {
	Tests []struct {
		TR      string                  `json:"tr"`
		Metrics []core.Metric           `json:"metrics,omitempty"`
		Checks  map[string]core.Outcome `json:"checks,omitempty"`
		Native  core.Outcome            `json:"native,omitempty"`
	} `json:"tests"`
}

// ParseResults converts the example tool's JSON output into normalized results.
// It is exported so it can be unit-tested against fixtures with no cluster —
// the pattern every tool's ResultParser should follow.
func ParseResults(data []byte) ([]core.TestResult, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("example: empty results payload")
	}
	var rr rawResults
	if err := json.Unmarshal(data, &rr); err != nil {
		return nil, fmt.Errorf("example: parse results: %w", err)
	}
	out := make([]core.TestResult, 0, len(rr.Tests))
	for _, t := range rr.Tests {
		out = append(out, core.TestResult{
			TRID:    t.TR,
			Metrics: t.Metrics,
			Checks:  t.Checks,
			Native:  t.Native,
		})
	}
	return out, nil
}
