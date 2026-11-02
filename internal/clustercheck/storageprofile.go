package clustercheck

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// StorageProfile is the effective status projection used by selection and setup.
type StorageProfile struct {
	Status StorageProfileStatus `json:"status"`
}

// StorageProfileStatus retains raw claim sets for lossless copying.
type StorageProfileStatus struct {
	SnapshotClass     string            `json:"snapshotClass"`
	ClaimPropertySets []json.RawMessage `json:"claimPropertySets"`
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
		profile, err := GetStorageProfile(ctx, s.Kubeconfig, name)
		if err != nil {
			if ctx.Err() != nil {
				return notReady()
			}
			if !errors.Is(err, ErrNotFound) {
				return err
			}
		}
		issues = nil
		if profile.Status.SnapshotClass != s.SnapshotClass {
			issues = append(issues, fmt.Sprintf("status.snapshotClass: expected %q, observed %q", s.SnapshotClass, profile.Status.SnapshotClass))
		}
		if len(profile.Status.ClaimPropertySets) == 0 {
			issues = append(issues, "missing status.claimPropertySets with volumeMode and accessModes")
		}
		for i, raw := range profile.Status.ClaimPropertySets {
			var properties claimPropertySet
			if err := json.Unmarshal(raw, &properties); err != nil {
				return fmt.Errorf("decode StorageProfile %q claimPropertySets[%d]: %w", name, i, err)
			}
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
