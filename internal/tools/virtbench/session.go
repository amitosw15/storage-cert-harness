package virtbench

import (
	"github.com/openshift-storage-cert/storage-cert-harness/internal/core"
	"github.com/openshift-storage-cert/storage-cert-harness/internal/stages"
)

// session owns the state of one VirtBench pipeline invocation.
type session struct {
	sc            Scenario
	trs           []core.TestRequirement
	resultsRoot   string
	replay        bool
	nsPrefix      string
	kubeconfig    string
	pickedNode    string
	snapshotClone *snapshotClone
}

var _ stages.Session = (*session)(nil)
