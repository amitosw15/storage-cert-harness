package kubeburner

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/openshift-storage-cert/storage-cert-harness/internal/clustercheck"
	"github.com/openshift-storage-cert/storage-cert-harness/internal/manifest"
)

// prepareVMStorage creates isolated storage settings and returns the copy's name even on later failure.
func prepareVMStorage(ctx context.Context, s clustercheck.SnapshotSelection) (created string, err error) {
	var sc map[string]any
	sourceData, err := clustercheck.SnapshotCommand(ctx, s.Kubeconfig, nil, "get", "storageclass", s.StorageClass, "-o", "json")
	if err != nil {
		return "", err
	}
	if err := json.Unmarshal(sourceData, &sc); err != nil {
		return "", err
	}
	data, err := manifest.Render(manifest.GoTemplate{}, "storageclass.yaml", []byte(storageClassTemplate), map[string]any{"STORAGE_CLASS": sc})
	if err != nil {
		return "", err
	}
	var made struct {
		Metadata struct {
			Name string `json:"name"`
		} `json:"metadata"`
	}
	out, err := clustercheck.SnapshotCommand(ctx, s.Kubeconfig, data, "create", "-f", "-", "-o", "json")
	if err != nil {
		return "", err
	}
	if err := json.Unmarshal(out, &made); err != nil {
		return "", err
	}
	created = made.Metadata.Name
	if created == "" {
		return "", fmt.Errorf("created storage class has no name")
	}
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	if _, err := clustercheck.SnapshotCommand(ctx, s.Kubeconfig, nil, "wait", "--for=create", "storageprofile/"+created, "--timeout=90s"); err != nil {
		return created, err
	}
	// Preserve claim defaults from the backend's profile for unknown CSI drivers.
	var source struct {
		Status struct {
			ClaimPropertySets []any `json:"claimPropertySets"`
		} `json:"status"`
	}
	sourceData, err = clustercheck.SnapshotCommand(ctx, s.Kubeconfig, nil, "get", "storageprofile", s.StorageClass, "-o", "json")
	if err != nil {
		return created, err
	}
	if err := json.Unmarshal(sourceData, &source); err != nil {
		return created, err
	}
	patch, err := manifest.Render(manifest.GoTemplate{}, "storageprofile-patch.yaml", []byte(storageProfilePatchTemplate), map[string]any{
		"SNAPSHOT_CLASS": s.SnapshotClass, "CLAIM_PROPERTY_SETS": source.Status.ClaimPropertySets,
	})
	if err != nil {
		return created, err
	}
	if _, err := clustercheck.SnapshotCommand(ctx, s.Kubeconfig, nil, "patch", "storageprofile", created, "--type=merge", "-p", string(patch)); err != nil {
		return created, err
	}
	_, err = clustercheck.SnapshotCommand(ctx, s.Kubeconfig, nil, "wait", "storageprofile/"+created, "--for=jsonpath={.status.snapshotClass}="+s.SnapshotClass, "--timeout=90s")
	return created, err
}
