package common

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"

	"github.com/openshift-storage-cert/storage-cert-harness/internal/clustercheck"
	"github.com/openshift-storage-cert/storage-cert-harness/internal/core"
)

// PreflightCheck is one prerequisite declared by a tool workload.
type PreflightCheck struct {
	Name    string
	Key     string
	Applies func() (bool, string)
	Run     func(context.Context) []core.Finding
}

// SnapshotSupport describes snapshot requirements and how to retain the selection.
type SnapshotSupport struct {
	Name            string
	StorageClass    string
	SnapshotClass   string
	Kubeconfig      string
	VMSnapshot      bool
	RequireVMClient bool
	SnapshotCount   int
	SkipReason      string
	Record          func(clustercheck.SnapshotSelection)
}

// CommandRequirement describes an executable and its required arguments.
type CommandRequirement struct {
	Name     string
	Binary   string
	Args     []string
	Workload string
}

// PathRequirement describes an accessible path and optional file restrictions.
type PathRequirement struct {
	Name            string
	Path            string
	RegularFile     bool
	RejectDirectory bool
}

// WorkerRequirement describes a minimum worker count and optional pinned worker.
type WorkerRequirement struct {
	Name       string
	Kubeconfig string
	Node       string
	MinWorkers int
}

// NamespacedSource identifies a namespaced Kubernetes object source.
type NamespacedSource struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
}

// DataSourceRequirement describes a DataSource lookup and its resolved sources.
type DataSourceRequirement struct {
	Source     NamespacedSource
	Kubeconfig string
	Record     func(map[string]NamespacedSource)
}

// SnapshotSourceRequirement describes a PVC or VolumeSnapshot class comparison.
type SnapshotSourceRequirement struct {
	Source        NamespacedSource
	SnapshotClass string
	Kubeconfig    string
}

// BinaryRequirement describes one host executable needed by a tool.
type BinaryRequirement struct {
	Name     string
	Workload string
}

// ToolIdentityCheck verifies that a TR names the selected tool.
func ToolIdentityCheck(tr core.TestRequirement, expected core.ToolName) PreflightCheck {
	return PreflightCheck{
		Name: "automation tool",
		Run: func(context.Context) []core.Finding {
			if tr.AutomationTool != "" && tr.AutomationTool != string(expected) {
				return []core.Finding{{Level: "error", Message: fmt.Sprintf("%s: automation_tool %q must be %q", tr.ID, tr.AutomationTool, expected)}}
			}
			return nil
		},
	}
}

// StorageClassCheck requires a storage class in the selected backend.
func StorageClassCheck(rc *core.RunCtx) PreflightCheck {
	return PreflightCheck{
		Name: "storage class",
		Run: func(context.Context) []core.Finding {
			storageClass := BackendStorageClass(rc)
			if storageClass == "" {
				return []core.Finding{{Level: "error", Message: "storage_class required (pass --backends/--backend)"}}
			}
			finding := core.Finding{Level: "info", Message: "storage_class=" + storageClass}
			return []core.Finding{finding}
		},
	}
}

// BinaryCheck requests one executable, shared with other requests for that name.
func BinaryCheck(binary BinaryRequirement) PreflightCheck {
	return PreflightCheck{
		Name: binary.Name,
		Key:  "binary:" + binary.Name,
		Run: func(context.Context) []core.Finding {
			if _, err := exec.LookPath(binary.Name); err != nil {
				prefix := "host execution needs"
				if binary.Workload != "" {
					prefix = "workload " + binary.Workload + " needs"
				}
				return []core.Finding{{Level: "error", Message: fmt.Sprintf("%s %q on PATH: %v", prefix, binary.Name, err)}}
			}
			return []core.Finding{{Level: "info", Message: "host binary found: " + binary.Name}}
		},
	}
}

// CommandCheck runs one executable with fixed arguments and shares exact requests.
func CommandCheck(requirement CommandRequirement) PreflightCheck {
	identity, _ := json.Marshal([]any{requirement.Binary, requirement.Args})
	name := requirement.Name
	if name == "" {
		name = strings.TrimSpace(requirement.Binary + " " + strings.Join(requirement.Args, " "))
	}
	return PreflightCheck{
		Name: name,
		Key:  "command:" + string(identity),
		Run: func(ctx context.Context) []core.Finding {
			path, err := exec.LookPath(requirement.Binary)
			if err != nil {
				prefix := "host execution needs"
				if requirement.Workload != "" {
					prefix = "workload " + requirement.Workload + " needs"
				}
				return []core.Finding{{Level: "error", Message: fmt.Sprintf("%s command %q on PATH: %v", prefix, name, err)}}
			}
			cmd := exec.CommandContext(ctx, path, requirement.Args...)
			output, err := cmd.CombinedOutput()
			if err != nil {
				return []core.Finding{{Level: "error", Message: fmt.Sprintf("command %q unavailable: %v: %s", name, err, strings.TrimSpace(string(output)))}}
			}
			return []core.Finding{{Level: "info", Message: "command available: " + name}}
		},
	}
}

// PathCheck checks accessibility and the requested file restrictions.
func PathCheck(requirement PathRequirement) PreflightCheck {
	return PreflightCheck{
		Name: requirement.Name,
		Run: func(context.Context) []core.Finding {
			info, err := os.Stat(requirement.Path)
			if err != nil {
				return []core.Finding{{Level: "error", Message: fmt.Sprintf("%s %q not accessible: %v", requirement.Name, requirement.Path, err)}}
			}
			if requirement.RegularFile && !info.Mode().IsRegular() {
				return []core.Finding{{Level: "error", Message: fmt.Sprintf("%s %q is not a regular file", requirement.Name, requirement.Path)}}
			}
			if requirement.RejectDirectory && info.IsDir() {
				return []core.Finding{{Level: "error", Message: fmt.Sprintf("%s %q is a directory", requirement.Name, requirement.Path)}}
			}
			return []core.Finding{{Level: "info", Message: requirement.Name + " accessible: " + requirement.Path}}
		},
	}
}

// RequiredValueCheck reports whether a supplied value is nonempty.
func RequiredValueCheck(name, value string) PreflightCheck {
	return PreflightCheck{
		Name: name,
		Run: func(context.Context) []core.Finding {
			if value == "" {
				return []core.Finding{{Level: "error", Message: name + " is required"}}
			}
			return []core.Finding{{Level: "info", Message: name + "=" + value}}
		},
	}
}

// IntegerBoundsCheck validates an integer against an inclusive lower and optional upper bound.
func IntegerBoundsCheck(name string, value, min, max int) PreflightCheck {
	return PreflightCheck{
		Name: name,
		Run: func(context.Context) []core.Finding {
			if value < min || (max > 0 && value > max) {
				if max > 0 {
					return []core.Finding{{Level: "error", Message: fmt.Sprintf("%s must be between %d and %d", name, min, max)}}
				}
				return []core.Finding{{Level: "error", Message: fmt.Sprintf("%s must be >= %d", name, min)}}
			}
			return []core.Finding{{Level: "info", Message: fmt.Sprintf("%s=%d", name, value)}}
		},
	}
}

// EnumCheck validates a value against the supplied choices.
func EnumCheck(name, value string, choices []string, allowEmpty bool) PreflightCheck {
	return PreflightCheck{
		Name: name,
		Run: func(context.Context) []core.Finding {
			if value == "" && allowEmpty {
				return []core.Finding{{Level: "info", Message: name + " uses its default"}}
			}
			for _, choice := range choices {
				if value == choice {
					return []core.Finding{{Level: "info", Message: name + "=" + value}}
				}
			}
			return []core.Finding{{Level: "error", Message: fmt.Sprintf("%s must be one of %s", name, strings.Join(choices, ", "))}}
		},
	}
}

// TemplateDiskCountCheck validates an expected disk count against a template or default.
func TemplateDiskCountCheck(name string, diskCount, defaultDiskCount int, template string, parseDiskCount func(string) (int, error)) PreflightCheck {
	return PreflightCheck{
		Name: name,
		Run: func(context.Context) []core.Finding {
			if diskCount < 1 {
				return []core.Finding{{Level: "error", Message: name + ": disk count must be >= 1"}}
			}
			if template == "" {
				if diskCount != defaultDiskCount {
					return []core.Finding{{Level: "error", Message: fmt.Sprintf("%s: disk count %d requires a template; the default defines %d", name, diskCount, defaultDiskCount)}}
				}
				return []core.Finding{{Level: "info", Message: fmt.Sprintf("%s: disk count %d matches the default template", name, diskCount)}}
			}
			if parseDiskCount == nil {
				return []core.Finding{{Level: "error", Message: name + ": template disk-count parser is required"}}
			}
			actual, err := parseDiskCount(template)
			if err != nil {
				return []core.Finding{{Level: "error", Message: fmt.Sprintf("%s: cannot count template disks: %v", name, err)}}
			}
			if actual != diskCount {
				return []core.Finding{{Level: "error", Message: fmt.Sprintf("%s: disk count %d does not match template count %d", name, diskCount, actual)}}
			}
			return []core.Finding{{Level: "info", Message: fmt.Sprintf("%s: disk count %d matches template", name, diskCount)}}
		},
	}
}

// SchedulableWorkerCheck checks worker role labels and cordon state with kubectl.
func SchedulableWorkerCheck(requirement WorkerRequirement) PreflightCheck {
	return PreflightCheck{
		Name: requirement.Name,
		Run: func(ctx context.Context) []core.Finding {
			args := []string{"get", "nodes", "-o", "json"}
			if requirement.Kubeconfig != "" {
				args = append([]string{"--kubeconfig", requirement.Kubeconfig}, args...)
			}
			cmd := exec.CommandContext(ctx, "kubectl", args...)
			out, err := cmd.CombinedOutput()
			if err != nil {
				return []core.Finding{{Level: "error", Message: fmt.Sprintf("%s: list worker nodes failed: %v: %s", requirement.Name, err, strings.TrimSpace(string(out)))}}
			}
			workers, err := schedulableWorkers(out)
			if err != nil {
				return []core.Finding{{Level: "error", Message: fmt.Sprintf("%s: parse worker nodes: %v", requirement.Name, err)}}
			}
			var findings []core.Finding
			if requirement.MinWorkers > 0 {
				if len(workers) < requirement.MinWorkers {
					findings = append(findings, core.Finding{Level: "error", Message: fmt.Sprintf("%s: need >=%d schedulable workers; found %d", requirement.Name, requirement.MinWorkers, len(workers))})
				} else {
					findings = append(findings, core.Finding{Level: "info", Message: fmt.Sprintf("%s: %d schedulable workers available", requirement.Name, len(workers))})
				}
			}
			if requirement.Node != "" {
				if !slices.Contains(workers, requirement.Node) {
					findings = append(findings, core.Finding{Level: "error", Message: fmt.Sprintf("%s: node %q is not a schedulable worker", requirement.Name, requirement.Node)})
				} else {
					findings = append(findings, core.Finding{Level: "info", Message: fmt.Sprintf("%s: node %q is a schedulable worker", requirement.Name, requirement.Node)})
				}
			}
			return findings
		},
	}
}

func schedulableWorkers(data []byte) ([]string, error) {
	var nodes struct {
		Items []struct {
			Metadata struct {
				Name   string            `json:"name"`
				Labels map[string]string `json:"labels"`
			} `json:"metadata"`
			Spec struct {
				Unschedulable bool `json:"unschedulable"`
			} `json:"spec"`
		} `json:"items"`
	}
	if err := json.Unmarshal(data, &nodes); err != nil {
		return nil, err
	}
	workers := make([]string, 0, len(nodes.Items))
	for _, node := range nodes.Items {
		_, worker := node.Metadata.Labels["node-role.kubernetes.io/worker"]
		_, controlPlane := node.Metadata.Labels["node-role.kubernetes.io/control-plane"]
		_, master := node.Metadata.Labels["node-role.kubernetes.io/master"]
		if worker && !controlPlane && !master && !node.Spec.Unschedulable {
			workers = append(workers, node.Metadata.Name)
		}
	}
	return workers, nil
}

// DataSourceCheck resolves a DataSource's source map without changing cluster state.
func DataSourceCheck(requirement DataSourceRequirement) PreflightCheck {
	return PreflightCheck{
		Name: "DataSource source",
		Run: func(ctx context.Context) []core.Finding {
			var dataSource struct {
				Spec struct {
					Source map[string]NamespacedSource `json:"source"`
				} `json:"spec"`
			}
			if err := getNamespacedResource(ctx, requirement.Kubeconfig, "datasource", requirement.Source, &dataSource); err != nil {
				return []core.Finding{{Level: "error", Message: err.Error()}}
			}
			if requirement.Record != nil {
				requirement.Record(dataSource.Spec.Source)
			}
			return []core.Finding{{Level: "info", Message: fmt.Sprintf("DataSource %s/%s source resolved", requirement.Source.Namespace, requirement.Source.Name)}}
		},
	}
}

// PVCSnapshotCompatibilityCheck verifies the PVC's storage class supports the selected snapshot class.
func PVCSnapshotCompatibilityCheck(requirement SnapshotSourceRequirement) PreflightCheck {
	return PreflightCheck{
		Name: "PVC snapshot compatibility",
		Run: func(ctx context.Context) []core.Finding {
			var pvc struct {
				Spec struct {
					StorageClass string `json:"storageClassName"`
				} `json:"spec"`
			}
			if err := getNamespacedResource(ctx, requirement.Kubeconfig, "pvc", requirement.Source, &pvc); err != nil {
				return []core.Finding{{Level: "error", Message: err.Error()}}
			}
			if _, err := clustercheck.ResolveSnapshot(ctx, pvc.Spec.StorageClass, requirement.SnapshotClass, requirement.Kubeconfig, false); err != nil {
				return []core.Finding{{Level: "error", Message: fmt.Sprintf("source PVC %s/%s: %v", requirement.Source.Namespace, requirement.Source.Name, err)}}
			}
			return []core.Finding{{Level: "info", Message: fmt.Sprintf("source PVC %s/%s supports snapshot class %q", requirement.Source.Namespace, requirement.Source.Name, requirement.SnapshotClass)}}
		},
	}
}

// VolumeSnapshotCompatibilityCheck verifies a source snapshot uses the selected class.
func VolumeSnapshotCompatibilityCheck(requirement SnapshotSourceRequirement) PreflightCheck {
	return PreflightCheck{
		Name: "VolumeSnapshot compatibility",
		Run: func(ctx context.Context) []core.Finding {
			var snapshot struct {
				Spec struct {
					SnapshotClass string `json:"volumeSnapshotClassName"`
				} `json:"spec"`
			}
			if err := getNamespacedResource(ctx, requirement.Kubeconfig, "volumesnapshot", requirement.Source, &snapshot); err != nil {
				return []core.Finding{{Level: "error", Message: err.Error()}}
			}
			if snapshot.Spec.SnapshotClass != requirement.SnapshotClass {
				return []core.Finding{{Level: "error", Message: fmt.Sprintf("source snapshot %s/%s uses snapshot class %q, requested %q", requirement.Source.Namespace, requirement.Source.Name, snapshot.Spec.SnapshotClass, requirement.SnapshotClass)}}
			}
			return []core.Finding{{Level: "info", Message: fmt.Sprintf("source snapshot %s/%s uses snapshot class %q", requirement.Source.Namespace, requirement.Source.Name, requirement.SnapshotClass)}}
		},
	}
}

func getNamespacedResource(ctx context.Context, kubeconfig, kind string, source NamespacedSource, result any) error {
	if source.Name == "" || source.Namespace == "" {
		return fmt.Errorf("%s source requires name and namespace", kind)
	}
	out, err := clustercheck.SnapshotCommand(ctx, kubeconfig, nil, "get", kind, source.Name, "-n", source.Namespace, "-o", "json")
	if err != nil {
		return err
	}
	if err := json.Unmarshal(out, result); err != nil {
		return err
	}
	return nil
}

// CapabilityCheck requests a cluster capability in the current preflight call.
func CapabilityCheck(capability clustercheck.Capability, storageClass string) PreflightCheck {
	return PreflightCheck{
		Name: capability.Name,
		Key:  "capability:" + capability.Name + ":" + storageClass,
		Run: func(ctx context.Context) []core.Finding {
			return clustercheck.CheckCapabilities(ctx, []clustercheck.Capability{capability})
		},
	}
}

// SnapshotSupportCheck resolves a compatible snapshot class and optionally checks client support.
func SnapshotSupportCheck(request SnapshotSupport) PreflightCheck {
	check := PreflightCheck{
		Name: request.Name,
		Run: func(ctx context.Context) []core.Finding {
			if request.StorageClass == "" || request.SnapshotCount <= 0 {
				return nil
			}
			selection, err := clustercheck.ResolveSnapshot(ctx, request.StorageClass, request.SnapshotClass, request.Kubeconfig, request.VMSnapshot)
			if err != nil {
				return []core.Finding{{Level: "error", Message: err.Error()}}
			}
			if request.Record != nil {
				request.Record(selection)
			}
			findings := []core.Finding{selection.Finding()}
			if request.RequireVMClient {
				findings = append(findings, clustercheck.WaitForCreateSupported(ctx, request.Kubeconfig))
			}
			return findings
		},
	}
	if request.SkipReason != "" {
		check.Applies = func() (bool, string) {
			return request.SnapshotCount != 0, request.SkipReason
		}
	}
	return check
}

// VolumePopulatorCheck detects the CDI import path and records it for the execution.
func VolumePopulatorCheck(record func(bool)) PreflightCheck {
	return PreflightCheck{
		Name: "volume populator",
		Run: func(ctx context.Context) []core.Finding {
			return clustercheck.CheckCapabilities(ctx, []clustercheck.Capability{clustercheck.VolumeImportSourcePopulator(record)})
		},
	}
}

// RunPreflightChecks runs unkeyed checks each time and keyed checks once per call.
func RunPreflightChecks(ctx context.Context, checks []PreflightCheck) []core.Finding {
	findings := make([]core.Finding, 0, len(checks))
	seen := make(map[string]bool, len(checks))
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
		findings = append(findings, check.Run(ctx)...)
	}
	return findings
}
