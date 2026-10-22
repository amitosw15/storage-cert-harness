package common

import (
	"github.com/openshift-storage-cert/storage-cert-harness/internal/clustercheck"
	"github.com/openshift-storage-cert/storage-cert-harness/internal/core"
)

// WorkloadRequirements declares the host binaries and cluster capabilities a workload needs.
type WorkloadRequirements struct {
	Binaries     []BinaryRequirement
	Capabilities []clustercheck.Capability
}

// BackendStorageClass returns the configured backend storage class, if any.
func BackendStorageClass(rc *core.RunCtx) string {
	if rc == nil || rc.Backend == nil {
		return ""
	}
	return rc.Backend.StorageClass
}

// BuildRequirementChecks binds binary and capability requirements in order.
func BuildRequirementChecks(binaries []BinaryRequirement, capabilities []clustercheck.Capability, storageClass string) []PreflightCheck {
	checks := make([]PreflightCheck, 0, len(binaries)+len(capabilities))
	for _, binary := range binaries {
		checks = append(checks, BinaryCheck(binary))
	}
	for _, capability := range capabilities {
		checks = append(checks, CapabilityCheck(capability, storageClass))
	}
	return checks
}
