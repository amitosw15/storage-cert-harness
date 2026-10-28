package virtbench

import (
	"fmt"

	"github.com/openshift-storage-cert/storage-cert-harness/internal/core"
)

var (
	cloneVMRange      = parameterVMRange("start", 100, "iteration_clones", "num_vms")
	scaleVMRange      = parameterVMRange("start", 100, "iteration_clones")
	diskOpsVMRange    = parameterVMRange("start", 100, "count", "end")
	drainVMRange      = parameterVMRange("start", drainDefaultCount, "count", "end")
	singleNodeVMRange = parameterVMRange("start", singleNodeDefaultCount, "attempt_vms")
	recoveryVMRange   = parameterVMRange("", 3, "vm_count")
)

// parameterVMRange applies end parameters in increasing precedence order.
func parameterVMRange(startParam string, defaultEnd int, endParams ...string) func(core.TestRequirement) (int, int, error) {
	return func(tr core.TestRequirement) (int, int, error) {
		start, end := 1, defaultEnd
		if startParam != "" {
			start = intParam(tr, startParam, start)
		}
		for _, key := range endParams {
			end = intParam(tr, key, end)
		}
		if start < 1 || end < start {
			return 0, 0, fmt.Errorf("%s: invalid VM namespace range %d..%d", tr.ID, start, end)
		}
		return start, end, nil
	}
}
