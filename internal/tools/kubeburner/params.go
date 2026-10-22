package kubeburner

import (
	"crypto/sha256"
	"fmt"
	"strings"

	"github.com/openshift-storage-cert/storage-cert-harness/internal/core"
	"github.com/openshift-storage-cert/storage-cert-harness/internal/tools/common"
)

const (
	resultsSubdir = "kubeburner-results"

	// DefaultVMImage is the neutral fallback containerDisk when no vm_image is set.
	DefaultVMImage  = "quay.io/kubevirt/cirros-container-disk-demo:latest"
	defaultNS       = "storage-cert-vmsnapshot"
	defaultMaxWait  = "1h"
	defaultQPS      = 20
	defaultBurst    = 20
	defaultDiskSize = "10Gi"
)

// Params are the harness-local per-TR knobs (plan overrides) that shape the kube-burner run; storage class comes from the backend, not here. See decisions/0012.
type Params struct {
	Replicas      int
	SnapshotCount int
	VMImage       string
	DiskSize      string
	QPS           int
	Burst         int
	MaxWait       string
	NS            string
	UsePopulator  bool

	snapshotCountSet bool
}

var _ common.RequirementValidator = Params{}

func resolveParams(tr core.TestRequirement) Params {
	p := Params{VMImage: DefaultVMImage, DiskSize: defaultDiskSize, QPS: defaultQPS, Burst: defaultBurst, MaxWait: defaultMaxWait, NS: defaultNS, UsePopulator: true}
	if tr.Params == nil {
		return p
	}
	raw := tr.Params
	if n, ok := core.AsInt(raw["replicas"]); ok {
		p.Replicas = n
	}
	_, p.snapshotCountSet = raw["snapshot_count"]
	if n, ok := core.AsInt(raw["snapshot_count"]); ok {
		p.SnapshotCount = n
	}
	if !p.snapshotCountSet {
		p.SnapshotCount = p.Replicas
	}
	if s, ok := core.AsString(raw["disk_size"]); ok && s != "" {
		p.DiskSize = s
	}
	if s, ok := core.AsString(raw["vm_image"]); ok && s != "" {
		p.VMImage = s
	}
	if n, ok := core.AsInt(raw["qps"]); ok {
		p.QPS = n
	}
	if n, ok := core.AsInt(raw["burst"]); ok {
		p.Burst = n
	}
	if s, ok := core.AsString(raw["max_wait"]); ok && s != "" {
		p.MaxWait = s
	}
	if s, ok := core.AsString(raw["namespace"]); ok && s != "" {
		p.NS = s
	}
	return p
}

// Isolate colliding namespaces while keeping each TR's other parameters intact.
func assignNamespaces(resolved []common.ResolvedRequirement[Params]) {
	counts := make(map[string]int, len(resolved))
	for _, requirement := range resolved {
		counts[requirement.Params.NS]++
	}
	for i := range resolved {
		params := &resolved[i].Params
		if counts[params.NS] < 2 {
			continue
		}
		tr := resolved[i].TR
		digest := sha256.Sum256([]byte(tr.ID + "\x00" + tr.Variant))
		prefix := strings.TrimRight(params.NS[:min(len(params.NS), 54)], "-")
		params.NS = fmt.Sprintf("%s-%x", prefix, digest[:4])
	}
}

func (p Params) validate() error {
	return p.validateFor("")
}

// Validate checks the params using the rules for this specific test requirement.
func (p Params) Validate(tr core.TestRequirement) error {
	return p.validateFor(tr.ID)
}

func (p Params) validateFor(trID string) error {
	scenario := snapshotScenarios[trID]
	if p.Replicas < 1 {
		return fmt.Errorf("kube-burner: param %q must be >= 1", "replicas")
	}
	if scenario.SingleSourceVolume && p.Replicas != 1 {
		return fmt.Errorf("kube-burner: TR-VIRT-027 requires exactly one source volume (replicas=1)")
	}
	if p.SnapshotCount < 0 || (p.SnapshotCount == 0 && !scenario.AllowZeroSnapshots) {
		return fmt.Errorf("kube-burner: param %q must be >= 1", "snapshot_count")
	}
	return nil
}
