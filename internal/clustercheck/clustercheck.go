package clustercheck

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"gitlab.cee.redhat.com/eco-special-projects/storage-cert-harness/internal/core"
)

// KubeCLI returns "oc" or "kubectl" if present on PATH, else "".
func KubeCLI() string {
	if _, err := exec.LookPath("oc"); err == nil {
		return "oc"
	}
	if _, err := exec.LookPath("kubectl"); err == nil {
		return "kubectl"
	}
	return ""
}

// ResolveKubeCLIPath returns the absolute, symlink-resolved path of oc/kubectl,
// suitable for mounting into a tool container. Error if neither is on PATH.
func ResolveKubeCLIPath() (string, error) {
	for _, name := range []string{"oc", "kubectl"} {
		path, err := exec.LookPath(name)
		if err != nil {
			continue
		}
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil {
			resolved = path
		}
		return filepath.Abs(resolved)
	}
	return "", fmt.Errorf("clustercheck: need kubectl or oc on PATH")
}

// Capability is a named cluster prerequisite that emits a core.Finding.
type Capability struct {
	Name  string
	Check func(ctx context.Context, cli string) core.Finding
}

// CRD returns a Capability that verifies a CRD exists on the cluster.
func CRD(crdName string) Capability {
	return Capability{
		Name: crdName,
		Check: func(ctx context.Context, cli string) core.Finding {
			out, err := exec.CommandContext(ctx, cli, "get", "crd", crdName, "-o", "name").CombinedOutput() // #nosec G204 -- CLI is selected by cluster detection and args are structured.
			if err != nil {
				return core.Finding{Level: "error", Message: fmt.Sprintf("CRD %s not found: %v: %s", crdName, err, strings.TrimSpace(string(out)))}
			}
			return core.Finding{Level: "info", Message: fmt.Sprintf("CRD %s present", crdName)}
		},
	}
}

// Predefined capabilities.
var (
	// CDI is the Containerized Data Importer (DataVolume) capability.
	CDI = CRD("datavolumes.cdi.kubevirt.io")
	// KubeVirt is the KubeVirt/OpenShift Virtualization capability.
	KubeVirt = CRD("kubevirts.kubevirt.io")
)

// HasVolumePopulator reports whether the cluster has registered a populator for
// the requested CDI source kind. Older CDI versions expose the CRDs but do not
// register every source kind, so checking the registration is necessary.
func HasVolumePopulator(ctx context.Context, cli, group, kind string) (bool, error) {
	out, err := exec.CommandContext(ctx, cli, "get", "volumepopulator", "-o", "json").CombinedOutput() // #nosec G204 -- CLI is selected by cluster detection and args are structured.
	if err != nil {
		message := strings.ToLower(string(out))
		if strings.Contains(message, "the server doesn't have a resource type") || strings.Contains(message, "not found") {
			return false, nil
		}
		return false, fmt.Errorf("get volume populators: %v: %s", err, strings.TrimSpace(string(out)))
	}
	var list struct {
		Items []struct {
			// VolumePopulator is a cluster-scoped API whose sourceKind is
			// top-level, unlike namespaced Kubernetes specs.
			SourceKind struct {
				Group string `json:"group"`
				Kind  string `json:"kind"`
			} `json:"sourceKind"`
		} `json:"items"`
	}
	if err := json.Unmarshal(out, &list); err != nil {
		return false, fmt.Errorf("decode volume populators: %w", err)
	}
	for _, item := range list.Items {
		if item.SourceKind.Group == group && item.SourceKind.Kind == kind {
			return true, nil
		}
	}
	return false, nil
}

// Preflight checks each capability and the StorageClass, reporting individual
// skips when the CLI or StorageClass is not configured.
func Preflight(ctx context.Context, caps []Capability, storageClass string) []core.Finding {
	cli := KubeCLI()
	var findings []core.Finding
	for _, c := range caps {
		if cli == "" {
			findings = append(findings, core.Finding{Level: "skip", Message: c.Name + ": neither kubectl nor oc found on PATH"})
		} else {
			findings = append(findings, c.Check(ctx, cli))
		}
	}
	switch {
	case storageClass == "":
		findings = append(findings, core.Finding{Level: "skip", Message: "storage class existence: no storage_class configured"})
	case cli == "":
		findings = append(findings, core.Finding{Level: "skip", Message: fmt.Sprintf("storage class %q existence: neither kubectl nor oc found on PATH", storageClass)})
	default:
		findings = append(findings, StorageClassExists(ctx, cli, storageClass))
	}
	return findings
}

// StorageClassExists checks a StorageClass exists on the cluster.
func StorageClassExists(ctx context.Context, cli, name string) core.Finding {
	out, err := exec.CommandContext(ctx, cli, "get", "storageclass", name, "-o", "name").CombinedOutput() // #nosec G204 -- CLI is selected by cluster detection and args are structured.
	if err != nil {
		return core.Finding{Level: "error", Message: fmt.Sprintf("storage class %q lookup failed: %v: %s", name, err, strings.TrimSpace(string(out)))}
	}
	return core.Finding{Level: "info", Message: fmt.Sprintf("storage class %q present", name)}
}
