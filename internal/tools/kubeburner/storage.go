package kubeburner

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
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
	templateSource, err := templates.ReadFile(storageClassTemplatePath)
	if err != nil {
		return "", err
	}
	data, err := manifest.Render(manifest.GoTemplate{}, "storageclass.yaml", templateSource, map[string]any{"STORAGE_CLASS": sc})
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
	if _, err := clustercheck.SnapshotCommand(ctx, s.Kubeconfig, nil, "patch", "storageprofile", created, "--type=merge", "-p", string(patch)); err != nil {
		return created, err
	}
	return created, waitForStorageProfile(ctx, s, created)
}

// waitForStorageProfile checks effective defaults before creating VM disks that omit their modes.
func waitForStorageProfile(ctx context.Context, s clustercheck.SnapshotSelection, name string) error {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	issues := []string{"effective status has not been observed"}
	notReady := func() error {
		return fmt.Errorf("StorageProfile %q (source StorageClass %q) is not ready: %s; check the source StorageProfile's claimPropertySets and CDI reconciliation: %w", name, s.StorageClass, strings.Join(issues, "; "), ctx.Err())
	}
	for {
		data, err := clustercheck.SnapshotCommand(ctx, s.Kubeconfig, nil, "get", "storageprofile", name, "-o", "json")
		if err != nil {
			if ctx.Err() != nil {
				return notReady()
			}
			return fmt.Errorf("read StorageProfile %q: %w", name, err)
		}
		var profile struct {
			Status struct {
				SnapshotClass     string `json:"snapshotClass"`
				ClaimPropertySets []struct {
					VolumeMode  string   `json:"volumeMode"`
					AccessModes []string `json:"accessModes"`
				} `json:"claimPropertySets"`
			} `json:"status"`
		}
		if err := json.Unmarshal(data, &profile); err != nil {
			return fmt.Errorf("decode StorageProfile %q: %w", name, err)
		}
		issues = nil
		if profile.Status.SnapshotClass != s.SnapshotClass {
			issues = append(issues, fmt.Sprintf("status.snapshotClass: expected %q, observed %q", s.SnapshotClass, profile.Status.SnapshotClass))
		}
		if len(profile.Status.ClaimPropertySets) == 0 {
			issues = append(issues, "missing status.claimPropertySets with volumeMode and accessModes")
		}
		for i, properties := range profile.Status.ClaimPropertySets {
			if properties.VolumeMode == "" {
				issues = append(issues, fmt.Sprintf("missing status.claimPropertySets[%d].volumeMode", i))
			}
			if len(properties.AccessModes) == 0 {
				issues = append(issues, fmt.Sprintf("missing status.claimPropertySets[%d].accessModes", i))
			}
			for _, mode := range properties.AccessModes {
				if mode == "" {
					issues = append(issues, fmt.Sprintf("empty status.claimPropertySets[%d].accessModes entry", i))
					break
				}
			}
		}
		if len(issues) == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return notReady()
		case <-ticker.C:
		}
	}
}
