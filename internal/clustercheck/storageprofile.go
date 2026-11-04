package clustercheck

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type storageProfile struct {
	Status storageProfileStatus `json:"status"`
}

type storageProfileStatus struct {
	SnapshotClass     string             `json:"snapshotClass"`
	ClaimPropertySets []claimPropertySet `json:"claimPropertySets"`
}

type claimPropertySet struct {
	VolumeMode  string   `json:"volumeMode"`
	AccessModes []string `json:"accessModes"`
}

// WaitForStorageProfile waits for the requested snapshot class and complete effective disk modes.
func WaitForStorageProfile(ctx context.Context, s SnapshotSelection, name string) error {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	issues := []string{"effective status has not been observed"}
	notReady := func() error {
		return fmt.Errorf("StorageProfile %q (source StorageClass %q) is not ready: %s; check the source StorageProfile's claimPropertySets and CDI reconciliation: %w", name, s.StorageClass, strings.Join(issues, "; "), ctx.Err())
	}
	for {
		data, err := SnapshotCommand(ctx, s.Kubeconfig, nil, "get", "storageprofile", name, "-o", "json")
		if err != nil {
			if ctx.Err() != nil {
				return notReady()
			}
			return fmt.Errorf("read StorageProfile %q: %w", name, err)
		}
		var profile storageProfile
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
