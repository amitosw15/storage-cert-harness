package kubeburner

import (
	"context"
	"fmt"

	"github.com/openshift-storage-cert/storage-cert-harness/internal/clustercheck"
	"github.com/openshift-storage-cert/storage-cert-harness/internal/core"
	"github.com/openshift-storage-cert/storage-cert-harness/internal/manifest"
)

func workloadStoragePreflight(ctx context.Context, tr core.TestRequirement, storageClass string) []core.Finding {
	p := resolveParams([]core.TestRequirement{tr})
	source, err := templates.ReadFile(scenarioFor(tr.ID).TemplateDir + "/vm.yaml")
	if err != nil {
		return []core.Finding{{Level: "error", Message: fmt.Sprintf("%s storage template: %v", tr.ID, err)}}
	}
	rendered, err := manifest.Render(manifest.GoTemplate{}, "vm.yaml", source, map[string]any{
		"Replica": 1, "storageClass": storageClass, "diskSize": p.DiskSize,
		"vmImage": p.VMImage, "usePopulator": p.UsePopulator,
	})
	if err != nil {
		return []core.Finding{{Level: "error", Message: fmt.Sprintf("%s storage render: %v", tr.ID, err)}}
	}
	requests, err := clustercheck.StorageRequests(rendered, storageClass, p.NS)
	if err != nil {
		return []core.Finding{{Level: "error", Message: fmt.Sprintf("%s storage: %v", tr.ID, err)}}
	}
	if err := clustercheck.ApplyStoragePolicy(requests, tr); err != nil {
		return []core.Finding{{Level: "error", Message: err.Error()}}
	}
	return clustercheck.CheckStorage(ctx, "", requests)
}
