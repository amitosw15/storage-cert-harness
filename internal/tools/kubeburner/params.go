package kubeburner

import (
	"fmt"
	"strconv"

	"github.com/openshift-storage-cert/storage-cert-harness/internal/core"
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

type snapshotScenario struct {
	TemplateDir        string
	SnapshotsPerVolume bool
	SingleSourceVolume bool
	AllowZeroSnapshots bool
}

var snapshotScenarios = map[string]snapshotScenario{
	"TR-VIRT-010": {TemplateDir: workloadTemplatesDir},
	"TR-VIRT-027": {
		TemplateDir:        workloadTemplatesDir,
		SnapshotsPerVolume: true,
		SingleSourceVolume: true,
		AllowZeroSnapshots: true,
	},
}

func scenarioFor(trID string) snapshotScenario {
	return snapshotScenarios[trID]
}

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

func resolveParams(trs []core.TestRequirement) Params {
	p := Params{VMImage: DefaultVMImage, DiskSize: defaultDiskSize, QPS: defaultQPS, Burst: defaultBurst, MaxWait: defaultMaxWait, NS: defaultNS, UsePopulator: true}
	if len(trs) == 0 || trs[0].Params == nil {
		return p
	}
	raw := trs[0].Params
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

func (p Params) validate() error {
	return p.validateFor("")
}

func (p Params) validateFor(trID string) error {
	scenario := scenarioFor(trID)
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

// buildEnv supplies scalar workload values to kube-burner's native renderer.
func buildEnv(p Params, storageClass string) []string {
	return []string{
		"REPLICAS=" + strconv.Itoa(p.Replicas),
		"SNAPSHOT_COUNT=" + strconv.Itoa(p.SnapshotCount),
		"LEGACY_SNAPSHOT_JOB=" + strconv.FormatBool(!p.snapshotCountSet && p.SnapshotCount == p.Replicas),
		"VM_IMAGE=" + p.VMImage,
		"DISK_SIZE=" + p.DiskSize,
		"STORAGE_CLASS=" + storageClass,
		"NAMESPACE=" + p.NS,
		"USE_POPULATOR=" + strconv.FormatBool(p.UsePopulator),
		"MAX_WAIT=" + p.MaxWait,
		"QPS=" + strconv.Itoa(p.QPS),
		"BURST=" + strconv.Itoa(p.Burst),
	}
}
