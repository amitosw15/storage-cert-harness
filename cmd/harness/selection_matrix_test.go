package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/openshift-storage-cert/storage-cert-harness/internal/catalog"
	"github.com/openshift-storage-cert/storage-cert-harness/internal/config"
	"github.com/openshift-storage-cert/storage-cert-harness/internal/core"
	"github.com/openshift-storage-cert/storage-cert-harness/internal/orchestrator"
)

func TestFocusSkipSelectionMatrix(t *testing.T) {
	trs := []core.TestRequirement{{ID: "A"}, {ID: "B"}, {ID: "C"}}
	tests := []struct {
		name              string
		focus, skip, want []string
		warnings          []string
		err               string
	}{
		{"none", nil, nil, []string{"A", "B", "C"}, nil, ""},
		{"focus", []string{"A", "B"}, nil, []string{"A", "B"}, nil, ""},
		{"skip", nil, []string{"B"}, []string{"A", "C"}, nil, ""},
		{"overlap", []string{"A", "B"}, []string{"B"}, []string{"A", "B"}, []string{"focus takes precedence over skip"}, ""},
		{"overlap and unselected skip", []string{"A", "B"}, []string{"B", "C"}, []string{"A", "B"}, []string{"focus takes precedence over skip"}, ""},
		{"skip outside focus", []string{"A"}, []string{"C"}, []string{"A"}, nil, ""},
		{"focus mixed unknown", []string{"A", "D"}, nil, []string{"A"}, []string{"focus ID is not in the selected tests"}, ""},
		{"focus unknown", []string{"D"}, nil, nil, []string{"focus ID is not in the selected tests"}, ""},
		{"skip unknown", nil, []string{"D"}, []string{"A", "B", "C"}, []string{"skip ID is not in the selected tests"}, ""},
		{"skip all", nil, []string{"A", "B", "C"}, nil, nil, ""},
		{"unknown overlap", []string{"D"}, []string{"D"}, nil, []string{"focus ID is not in the selected tests", "skip ID is not in the selected tests"}, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			logger := slog.New(slog.NewTextHandler(&logs, nil))
			cfg := config.Config{}
			err := configureFocusSkip(&cfg, trs, tc.focus, tc.skip, logger)
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("error = %v, want %q", err, tc.err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, tr := range orchestrator.SelectTRs(trs, cfg.Filter) {
				got = append(got, tr.ID)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("selected = %v, want %v", got, tc.want)
			}
			for _, warning := range tc.warnings {
				if !strings.Contains(logs.String(), warning) {
					t.Fatalf("missing warning %q in %s", warning, logs.String())
				}
			}
			if count := strings.Count(logs.String(), "level=WARN"); count != len(tc.warnings) {
				t.Fatalf("got %d warnings, want %d: %s", count, len(tc.warnings), logs.String())
			}
		})
	}
}

func TestEmptySelectionFlags(t *testing.T) {
	for _, command := range []string{"run", "preflight"} {
		for _, flag := range []string{"id", "tool", "focus", "skip"} {
			for _, value := range []string{"", " ", "A,"} {
				t.Run(command+"/"+flag+"/"+value, func(t *testing.T) {
					cmd := newRootCmd()
					cmd.SetOut(&bytes.Buffer{})
					cmd.SetArgs([]string{command, "--catalog", "missing.json", "--" + flag, value})
					if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "--"+flag+" requires nonempty values") {
						t.Fatalf("error = %v", err)
					}
				})
			}
		}
	}
}

func TestContinueCompletedPreviewIsEmpty(t *testing.T) {
	catalogPath := writePreflightCatalog(t, []catalog.TR{{ID: "EX-001", AutomationTool: "example"}})
	priorPath := filepath.Join(t.TempDir(), "prior.json")
	if err := os.WriteFile(priorPath, []byte(`{"executions":[{"tr":"EX-001","state":"completed","outcome":"pass"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	cmd := newRootCmd()
	cmd.SetOut(&output)
	cmd.SetArgs([]string{"run", "--catalog", catalogPath, "--continue-from", priorPath, "--inspect-plan"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var result struct {
		Executions []planExecution `json:"executions"`
	}
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Executions) != 0 {
		t.Fatalf("executions = %v", result.Executions)
	}
}

func TestAllTestsSelectorConflicts(t *testing.T) {
	for _, command := range []string{"run", "preflight"} {
		for _, tc := range []struct{ flag, value string }{{"id", "TR-1"}, {"tool", "tool-a"}, {"partner-level", "3"}} {
			t.Run(command+"/"+tc.flag, func(t *testing.T) {
				cmd := newRootCmd()
				cmd.SetOut(&bytes.Buffer{})
				cmd.SetArgs([]string{command, "--catalog", "missing.json", "--all-tests", "--" + tc.flag, tc.value})
				err := cmd.Execute()
				if err == nil || !strings.Contains(err.Error(), "--all-tests cannot be combined with --"+tc.flag) {
					t.Fatalf("error = %v", err)
				}
			})
		}
	}
}

func TestRepeatedEmptySelectorOccurrences(t *testing.T) {
	for _, command := range []string{"run", "preflight"} {
		for _, tc := range []struct{ flag, value string }{{"id", "TR-1"}, {"tool", "example"}, {"partner-level", "3"}, {"focus", "TR-1"}, {"skip", "TR-1"}} {
			for _, emptyFirst := range []bool{true, false} {
				t.Run(fmt.Sprintf("%s/%s/emptyFirst=%t", command, tc.flag, emptyFirst), func(t *testing.T) {
					values := []string{tc.value, ""}
					if emptyFirst {
						values[0], values[1] = values[1], values[0]
					}
					cmd := newRootCmd()
					cmd.SetOut(&bytes.Buffer{})
					cmd.SetArgs([]string{command, "--catalog", "missing.json", "--" + tc.flag, values[0], "--" + tc.flag, values[1]})
					err := cmd.Execute()
					if err == nil || !strings.Contains(err.Error(), "--"+tc.flag) {
						t.Fatalf("error = %v", err)
					}
					if strings.Contains(err.Error(), "catalog: read") {
						t.Fatalf("empty flag reached catalog load: %v", err)
					}
				})
			}
		}
	}
}

func TestMixedEmptySelectorOccurrences(t *testing.T) {
	for _, command := range []string{"run", "preflight"} {
		for _, tc := range []struct {
			name      string
			args      []string
			emptyFlag string
		}{
			{"id then empty tool", []string{"--id", "TR-1", "--tool", ""}, "tool"},
			{"tool then empty id", []string{"--tool", "example", "--id", ""}, "id"},
			{"id then empty partner level", []string{"--id", "TR-1", "--partner-level", ""}, "partner-level"},
			{"id then empty focus", []string{"--id", "TR-1", "--focus", ""}, "focus"},
			{"tool then empty skip", []string{"--tool", "example", "--skip", ""}, "skip"},
			{"focus then repeated empty id", []string{"--focus", "TR-1", "--id", "TR-1", "--id", ""}, "id"},
		} {
			t.Run(command+"/"+tc.name, func(t *testing.T) {
				cmd := newRootCmd()
				cmd.SetOut(&bytes.Buffer{})
				cmd.SetArgs(append([]string{command, "--catalog", "missing.json"}, tc.args...))
				err := cmd.Execute()
				if err == nil || !strings.Contains(err.Error(), "--"+tc.emptyFlag) {
					t.Fatalf("error = %v", err)
				}
				if strings.Contains(err.Error(), "catalog: read") {
					t.Fatalf("empty flag reached catalog load: %v", err)
				}
			})
		}
	}
}
