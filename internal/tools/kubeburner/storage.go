package kubeburner

import (
	"context"
	"time"

	"github.com/openshift-storage-cert/storage-cert-harness/internal/clustercheck"
	"github.com/openshift-storage-cert/storage-cert-harness/internal/manifest"
)

// prepareVMStorage creates isolated storage settings and returns the copy's name even on later failure.
func prepareVMStorage(ctx context.Context, s clustercheck.SnapshotSelection) (created string, err error) {
	sc, err := clustercheck.GetStorageClass(ctx, s.Kubeconfig, s.StorageClass)
	if err != nil {
		return "", err
	}
	templateSource, err := templates.ReadFile(storageClassTemplatePath)
	if err != nil {
		return "", err
	}
	data, err := manifest.Render(manifest.GoTemplate{}, "storageclass.yaml", templateSource, map[string]any{"STORAGE_CLASS": sc})
	if err != nil {
		return "", err
	}
	created, err = clustercheck.CreateStorageClass(ctx, s.Kubeconfig, data)
	if err != nil {
		return created, err
	}
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	if err := clustercheck.WaitForStorageProfileCreated(ctx, s.Kubeconfig, created, 90*time.Second); err != nil {
		return created, err
	}
	source, err := clustercheck.GetStorageProfile(ctx, s.Kubeconfig, s.StorageClass)
	if err != nil {
		return created, err
	}
	templateSource, err = templates.ReadFile(storageProfilePatchTemplatePath)
	if err != nil {
		return created, err
	}
	patch, err := manifest.Render(manifest.GoTemplate{}, "storageprofile-patch.yaml", templateSource, map[string]any{
		"SNAPSHOT_CLASS": s.SnapshotClass, "CLAIM_PROPERTY_SETS": source.Status.ClaimPropertySets,
	})
	if err != nil {
		return created, err
	}
	if err := clustercheck.PatchStorageProfile(ctx, s.Kubeconfig, created, patch); err != nil {
		return created, err
	}
	return created, clustercheck.WaitForStorageProfile(ctx, s, created)
}
