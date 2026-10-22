package orchestrator

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"testing"

	"github.com/openshift-storage-cert/storage-cert-harness/internal/config"
	"github.com/openshift-storage-cert/storage-cert-harness/internal/core"
	"github.com/openshift-storage-cert/storage-cert-harness/internal/registry"
	"github.com/openshift-storage-cert/storage-cert-harness/internal/stages"
)

type storageRecordingRunner struct{}

func (storageRecordingRunner) Run(_ context.Context, rc *core.RunCtx, _ *core.Bag, trs []core.TestRequirement) (stages.RunHandle, error) {
	rc.RecordStorageSelection(core.StorageSelection{TR: trs[0].ID, Variant: trs[0].Variant, StorageClass: "backend", SnapshotClass: "selected"})
	return stages.RunHandle{}, fmt.Errorf("failure after storage selection")
}

func TestStorageSelectionSurvivesFailureAndCheckpoint(t *testing.T) {
	registry.Register(stages.ToolIntegration{Name: "storage-recording-test", Runner: storageRecordingRunner{}})
	var checkpoint core.Report
	var recorded []core.StorageSelection
	rc := &core.RunCtx{
		Logger:                 slog.New(slog.NewTextHandler(io.Discard, nil)),
		Checkpoint:             func(r core.Report) { checkpoint = r },
		RecordStorageSelection: func(s core.StorageSelection) { recorded = append(recorded, s) },
	}
	rep, err := Run(context.Background(), config.Config{Scenario: "idle"}, []core.TestRequirement{{ID: "TEST-STORAGE", AutomationTool: "storage-recording-test"}}, core.Provenance{}, "2.0", rc)
	if err != nil {
		t.Fatal(err)
	}
	for _, selections := range [][]core.StorageSelection{rep.StorageSelections, checkpoint.StorageSelections, recorded} {
		if len(selections) != 1 || selections[0].SnapshotClass != "selected" || selections[0].Scenario != "idle" {
			t.Fatalf("selections = %+v", selections)
		}
	}
}
