package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/openshift-storage-cert/storage-cert-harness/internal/catalog"
	"github.com/openshift-storage-cert/storage-cert-harness/internal/core"
	"github.com/openshift-storage-cert/storage-cert-harness/internal/registry"
	"github.com/openshift-storage-cert/storage-cert-harness/internal/stages"
)

func TestEmptySelectorRejectedBeforeLoadingInputs(t *testing.T) {
	for _, flag := range []string{"id", "tool"} {
		for _, command := range []string{"run", "preflight"} {
			for _, tc := range []struct {
				name string
				args []string
			}{
				{"empty", []string{"--" + flag, ""}},
				{"whitespace", []string{"--" + flag, " "}},
				{"empty comma entry", []string{"--" + flag, "value,"}},
			} {
				t.Run(command+"/"+flag+"/"+tc.name, func(t *testing.T) {
					cmd := newRootCmd()
					cmd.SetOut(&bytes.Buffer{})
					cmd.SetArgs(append([]string{command, "--catalog", "missing.json"}, tc.args...))
					err := cmd.Execute()
					if err == nil || !strings.Contains(err.Error(), "--"+flag+" requires nonempty values") {
						t.Fatalf("error = %v; want empty selector error", err)
					}
				})
			}
		}
	}
}

func TestRepeatedSelectorWithEmptyOccurrencePreservesValidSelection(t *testing.T) {
	dir := t.TempDir()
	catalogPath := filepath.Join(dir, "catalog.json")
	thresholdsPath := filepath.Join(dir, "thresholds.json")
	planPath := filepath.Join(dir, "plan.yaml")
	catalogData := `{"schema_version":"2.0","provenance":{"kb_git_commit":"fixture"},"test_requirements":[{"id":"EX-001","automation_tool":"example","sla_status":"defined","sla_count":1,"sla":[{"metric":"latency_ms","type":"latency","unit":"ms","percentile":"p99","measured_by":["example"]}]},{"id":"EX-003","automation_tool":"example","sla_status":"defined","sla_count":1,"sla":[{"metric":"read_throughput","type":"throughput","unit":"MiB/s","measured_by":["example"]}]}]}`
	thresholdsData := `{"schema_version":"2.0","provenance":{"kb_git_commit":"fixture"},"test_requirements":[{"id":"EX-001","sla":[{"metric":"latency_ms","type":"latency","operator":"<=","value":1,"unit":"ms","percentile":"p99"}]},{"id":"EX-003","sla":[{"metric":"read_throughput","type":"throughput","operator":">=","value":1000,"unit":"MiB/s"}]}]}`
	for path, content := range map[string]string{catalogPath: catalogData, thresholdsPath: thresholdsData, planPath: "schema_version: '1'\nname: proof\nselect:\n  ids: [EX-001]\n"} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct{ flag, value string }{{"id", "EX-MISSING"}, {"tool", "nonexistent-tool"}} {
		for _, command := range []string{"run", "preflight"} {
			t.Run(command+"/unknown-"+tc.flag, func(t *testing.T) {
				var out bytes.Buffer
				cmd := newRootCmd()
				cmd.SetOut(&out)
				cmd.SetArgs([]string{command, "--catalog", catalogPath, "--plan", planPath, "--" + tc.flag, tc.value})
				err := cmd.Execute()
				if command == "run" && (err == nil || !strings.Contains(err.Error(), "no test requirements selected")) {
					t.Fatalf("run error = %v", err)
				}
				if command == "preflight" && (err != nil || !strings.Contains(out.String(), "No test requirements selected.")) {
					t.Fatalf("preflight error = %v, output = %s", err, out.String())
				}
			})
		}
	}
	for _, command := range []string{"run", "preflight"} {
		t.Run(command+"/id-and-tool-intersection", func(t *testing.T) {
			var out bytes.Buffer
			outDir := filepath.Join(t.TempDir(), "out")
			args := []string{command, "--catalog", catalogPath, "--plan", planPath, "--id", "EX-003,EX-MISSING", "--tool", "example,nonexistent-tool"}
			if command == "run" {
				args = append(args, "--thresholds", thresholdsPath, "--skip-env", "--no-attestations", "--output", outDir)
			}
			cmd := newRootCmd()
			cmd.SetOut(&out)
			cmd.SetArgs(args)
			if err := cmd.Execute(); err != nil {
				t.Fatalf("execute: %v; output %s", err, out.String())
			}
			if command == "preflight" {
				if !strings.Contains(out.String(), "example integration ready") {
					t.Fatalf("output = %s", out.String())
				}
				return
			}
			data, err := os.ReadFile(filepath.Join(outDir, "report.json"))
			if err != nil {
				t.Fatal(err)
			}
			var report core.Report
			if err := json.Unmarshal(data, &report); err != nil {
				t.Fatal(err)
			}
			if len(report.Executions) != 1 || report.Executions[0].TR != "EX-003" {
				t.Fatalf("executions = %+v", report.Executions)
			}
		})
		t.Run(command+"/id-and-tool-no-intersection", func(t *testing.T) {
			var out bytes.Buffer
			cmd := newRootCmd()
			cmd.SetOut(&out)
			cmd.SetArgs([]string{command, "--catalog", catalogPath, "--plan", planPath, "--id", "EX-003", "--tool", "nonexistent-tool"})
			err := cmd.Execute()
			if command == "run" && (err == nil || !strings.Contains(err.Error(), "no test requirements selected")) {
				t.Fatalf("run error = %v", err)
			}
			if command == "preflight" && (err != nil || !strings.Contains(out.String(), "No test requirements selected.")) {
				t.Fatalf("preflight error = %v, output = %s", err, out.String())
			}
		})
	}
	for _, tc := range []struct {
		flag, value string
		want        []string
	}{
		{"id", "EX-003", []string{"EX-003"}},
		{"tool", "example", []string{"EX-001", "EX-003"}},
		{"id", "EX-003,EX-MISSING", []string{"EX-003"}},
		{"tool", "example,nonexistent-tool", []string{"EX-001", "EX-003"}},
	} {
		for _, emptyFirst := range []bool{true, false} {
			for _, command := range []string{"run", "preflight"} {
				t.Run(fmt.Sprintf("%s/%s/emptyFirst=%t", command, tc.flag, emptyFirst), func(t *testing.T) {
					values := []string{tc.value, ""}
					if emptyFirst {
						values[0], values[1] = values[1], values[0]
					}
					args := []string{command, "--catalog", catalogPath, "--plan", planPath, "--" + tc.flag, values[0], "--" + tc.flag, values[1]}
					outDir := filepath.Join(t.TempDir(), "out")
					if command == "run" {
						args = append(args, "--thresholds", thresholdsPath, "--skip-env", "--no-attestations", "--output", outDir)
					}
					var output bytes.Buffer
					root := newRootCmd()
					root.SetOut(&output)
					root.SetArgs(args)
					if err := root.Execute(); err != nil {
						t.Fatalf("execute: %v\n%s", err, output.String())
					}
					selectedCmd, _, err := root.Find([]string{command})
					if err != nil {
						t.Fatal(err)
					}
					parsed, err := selectedCmd.Flags().GetStringSlice(tc.flag)
					if err != nil {
						t.Fatal(err)
					}
					if !reflect.DeepEqual(parsed, strings.Split(tc.value, ",")) {
						t.Fatalf("parsed = %v", parsed)
					}
					if command == "preflight" {
						return
					}
					data, err := os.ReadFile(filepath.Join(outDir, "report.json"))
					if err != nil {
						t.Fatal(err)
					}
					var report core.Report
					if err := json.Unmarshal(data, &report); err != nil {
						t.Fatal(err)
					}
					var got []string
					for _, execution := range report.Executions {
						got = append(got, execution.TR)
					}
					if !reflect.DeepEqual(got, tc.want) {
						t.Fatalf("executed %v, want %v", got, tc.want)
					}
				})
			}
		}
	}
}

type selectorPreflightObserver struct{ ids *[]string }

func (s selectorPreflightObserver) Check(_ context.Context, _ *core.RunCtx, _ *core.Bag, trs []core.TestRequirement) ([]core.Finding, error) {
	for _, tr := range trs {
		*s.ids = append(*s.ids, tr.ID)
	}
	return []core.Finding{{Level: "info", Message: "selected"}}, nil
}

func TestPreflightIDAndToolIntersection(t *testing.T) {
	var got []string
	registry.Register(stages.ToolIntegration{Name: "selector-observer", Preflight: selectorPreflightObserver{ids: &got}})
	catalogPath := writePreflightCatalog(t, []catalog.TR{
		{ID: "EX-001", AutomationTool: "selector-observer"},
		{ID: "EX-003", AutomationTool: "selector-observer"},
	})
	cmd := newRootCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs([]string{"preflight", "--catalog", catalogPath, "--id", "EX-003,EX-MISSING", "--tool", "selector-observer,nonexistent-tool"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []string{"EX-003"}) {
		t.Fatalf("preflight checked %v, want EX-003", got)
	}
}
