package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/openshift-storage-cert/storage-cert-harness/internal/catalog"
)

type planExecution = struct {
	ID       string `json:"id"`
	Tool     string `json:"tool"`
	Variant  string `json:"variant,omitempty"`
	Scenario string `json:"scenario,omitempty"`
}

func TestPlanInspect(t *testing.T) {
	catalogPath := writePreflightCatalog(t, []catalog.TR{
		{ID: "TR-1", AutomationTool: "tool-a", PartnerLevel: 1},
		{ID: "TR-2", AutomationTool: "tool-b", PartnerLevel: 2},
		{ID: "TR-3", AutomationTool: "tool-a", PartnerLevel: 3},
	})
	writePlan := func(name, content string) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), name+".yaml")
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	multi := writePlan("multi", `schema_version: "1"
name: multi-tool
scenario: load
select:
  ids: [TR-1, TR-2]
variants:
  TR-2:
    - name: small
    - name: large
`)
	single := writePlan("single", `schema_version: "1"
name: one-tool
select:
  tools: [tool-a]
`)
	cases := []struct {
		name, path string
		flags      []string
		want       []planExecution
	}{
		{"multiple tools and variants", multi, nil, []planExecution{{ID: "TR-1", Tool: "tool-a", Scenario: "load"}, {ID: "TR-2", Tool: "tool-b", Variant: "small", Scenario: "load"}, {ID: "TR-2", Tool: "tool-b", Variant: "large", Scenario: "load"}}},
		{"another plan", single, nil, []planExecution{{ID: "TR-1", Tool: "tool-a"}, {ID: "TR-3", Tool: "tool-a"}}},
		{"all tests overrides plan", single, []string{"--all-tests"}, []planExecution{{ID: "TR-1", Tool: "tool-a"}, {ID: "TR-2", Tool: "tool-b"}, {ID: "TR-3", Tool: "tool-a"}}},
		{"all tests with focus", single, []string{"--all-tests", "--focus", "TR-2"}, []planExecution{{ID: "TR-2", Tool: "tool-b"}}},
		{"all tests with skip", single, []string{"--all-tests", "--skip", "TR-2"}, []planExecution{{ID: "TR-1", Tool: "tool-a"}, {ID: "TR-3", Tool: "tool-a"}}},
		{"all tests false preserves plan", single, []string{"--all-tests=false"}, []planExecution{{ID: "TR-1", Tool: "tool-a"}, {ID: "TR-3", Tool: "tool-a"}}},
		{"filter replaces plan selector", single, []string{"--id", "TR-2", "--scenario", "custom"}, []planExecution{{ID: "TR-2", Tool: "tool-b", Scenario: "custom"}}},
		{"tool filter", multi, []string{"--tool", "tool-b"}, []planExecution{{ID: "TR-2", Tool: "tool-b", Variant: "small", Scenario: "load"}, {ID: "TR-2", Tool: "tool-b", Variant: "large", Scenario: "load"}}},
		{"partner level filter", single, []string{"--partner-level", "2"}, []planExecution{{ID: "TR-2", Tool: "tool-b"}}},
		{"id and focus select outside original plan", single, []string{"--id", "TR-2", "--focus", "TR-2"}, []planExecution{{ID: "TR-2", Tool: "tool-b"}}},
		{"tool and focus", single, []string{"--tool", "tool-b", "--focus", "TR-2"}, []planExecution{{ID: "TR-2", Tool: "tool-b"}}},
		{"partner level and focus", single, []string{"--partner-level", "2", "--focus", "TR-2"}, []planExecution{{ID: "TR-2", Tool: "tool-b"}}},
		{"id tool and skip", single, []string{"--id", "TR-1,TR-2,TR-3", "--tool", "tool-a", "--skip", "TR-3"}, []planExecution{{ID: "TR-1", Tool: "tool-a"}}},
		{"focus excludes another test with variants", multi, []string{"--focus", "TR-1"}, []planExecution{{ID: "TR-1", Tool: "tool-a", Scenario: "load"}}},
		{"focus includes all variants", multi, []string{"--focus", "TR-2"}, []planExecution{{ID: "TR-2", Tool: "tool-b", Variant: "small", Scenario: "load"}, {ID: "TR-2", Tool: "tool-b", Variant: "large", Scenario: "load"}}},
		{"focus and outside plan", multi, []string{"--focus", "TR-1,TR-3"}, []planExecution{{ID: "TR-1", Tool: "tool-a", Scenario: "load"}}},
		{"skip removes every variant", multi, []string{"--skip", "TR-2"}, []planExecution{{ID: "TR-1", Tool: "tool-a", Scenario: "load"}}},
		{"focus wins on overlap", multi, []string{"--focus", "TR-1,TR-2", "--skip", "TR-2,TR-3"}, []planExecution{{ID: "TR-1", Tool: "tool-a", Scenario: "load"}, {ID: "TR-2", Tool: "tool-b", Variant: "small", Scenario: "load"}, {ID: "TR-2", Tool: "tool-b", Variant: "large", Scenario: "load"}}},
		{"unmatched skip leaves selection", single, []string{"--skip", "TR-2"}, []planExecution{{ID: "TR-1", Tool: "tool-a"}, {ID: "TR-3", Tool: "tool-a"}}},
		{"focus with no match", multi, []string{"--focus", "TR-3"}, []planExecution{}},
		{"skip all", multi, []string{"--skip", "TR-1,TR-2"}, []planExecution{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			cmd := newRootCmd()
			cmd.SetOut(&out)
			args := append([]string{"run", "--inspect-plan", "--catalog", catalogPath, "--plan", tc.path}, tc.flags...)
			cmd.SetArgs(args)
			if err := cmd.Execute(); err != nil {
				t.Fatal(err)
			}
			var got struct {
				SchemaVersion string `json:"schema_version"`
				Executions    []struct {
					ID       string `json:"id"`
					Tool     string `json:"tool"`
					Variant  string `json:"variant,omitempty"`
					Scenario string `json:"scenario,omitempty"`
				} `json:"executions"`
			}
			if err := json.Unmarshal(out.Bytes(), &got); err != nil {
				t.Fatalf("invalid JSON %q: %v", out.String(), err)
			}
			if got.SchemaVersion != "1" || !reflect.DeepEqual(got.Executions, tc.want) {
				t.Fatalf("got %+v; executions want %+v", got, tc.want)
			}
		})
	}
	prior := filepath.Join(t.TempDir(), "prior.json")
	if err := os.WriteFile(prior, []byte(`{"executions":[{"tr":"TR-2","variant":"small","scenario":"load","state":"completed","outcome":"pass"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Run("resume one focused variant", func(t *testing.T) {
		var out bytes.Buffer
		cmd := newRootCmd()
		cmd.SetOut(&out)
		cmd.SetArgs([]string{"run", "--inspect-plan", "--catalog", catalogPath, "--plan", multi, "--focus", "TR-2", "--continue-from", prior})
		if err := cmd.Execute(); err != nil {
			t.Fatal(err)
		}
		var result struct {
			Executions []planExecution `json:"executions"`
		}
		if err := json.Unmarshal(out.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		want := []planExecution{{ID: "TR-2", Tool: "tool-b", Variant: "large", Scenario: "load"}}
		if !reflect.DeepEqual(result.Executions, want) {
			t.Fatalf("got %+v, want %+v", result.Executions, want)
		}
	})
	for _, tc := range []struct {
		name, plan string
		flags      []string
		message    string
	}{
		{"no matching selection", multi, []string{"--id", "absent"}, "no test requirements selected"},
		{"empty focus", multi, []string{"--focus", ""}, "requires nonempty values"},
		{"empty skip", multi, []string{"--skip", ""}, "requires nonempty values"},
		{"missing plan", filepath.Join(t.TempDir(), "missing.yaml"), nil, "read plan"},
		{"invalid plan", writePlan("invalid", "invalid: ["), nil, "parse plan"},
		{"unknown variant ID", writePlan("unknown", "schema_version: '1'\nname: unknown\nselect:\n  all: true\nvariants:\n  MISSING:\n    - name: one\n"), nil, "unknown TR"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			cmd := newRootCmd()
			cmd.SetOut(&out)
			cmd.SetArgs(append([]string{"run", "--inspect-plan", "--catalog", catalogPath, "--plan", tc.plan}, tc.flags...))
			err := cmd.Execute()
			if err == nil || !strings.Contains(err.Error(), tc.message) {
				t.Fatalf("error = %v, want %q", err, tc.message)
			}
			if out.Len() != 0 {
				t.Fatalf("unexpected partial JSON: %s", out.String())
			}
		})
	}
}
