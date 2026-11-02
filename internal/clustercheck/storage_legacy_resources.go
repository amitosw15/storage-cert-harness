package clustercheck

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

var errPreflightNotFound = errors.New("preflight resource not found")

type storageReference struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
}

type storageMetadata struct {
	CreationTimestamp time.Time         `json:"creationTimestamp"`
	UID               string            `json:"uid"`
	DeletionTimestamp string            `json:"deletionTimestamp"`
	Labels            map[string]string `json:"labels"`
	Name              string            `json:"name"`
	Namespace         string            `json:"namespace"`
	Annotations       map[string]string `json:"annotations"`
}

type preflightPVC struct {
	Spec   preflightPVCSpec   `json:"spec"`
	Status preflightPVCStatus `json:"status"`
}

type preflightPVCSpec struct {
	StorageClassName string `json:"storageClassName"`
	VolumeMode       string `json:"volumeMode"`
	VolumeName       string `json:"volumeName"`
}

type preflightPVCStatus struct {
	Phase string `json:"phase"`
}

type preflightSnapshot struct {
	Metadata storageMetadata         `json:"metadata"`
	Spec     preflightSnapshotSpec   `json:"spec"`
	Status   preflightSnapshotStatus `json:"status"`
}

type preflightSnapshotSpec struct {
	VolumeSnapshotClassName string `json:"volumeSnapshotClassName"`
	Source                  struct {
		PersistentVolumeClaimName string `json:"persistentVolumeClaimName"`
	} `json:"source"`
}

type preflightSnapshotStatus struct {
	ReadyToUse                     bool   `json:"readyToUse"`
	BoundVolumeSnapshotContentName string `json:"boundVolumeSnapshotContentName"`
}

type preflightDataSource struct {
	Spec   preflightDataSourceSpec   `json:"spec"`
	Status preflightDataSourceStatus `json:"status"`
}

type preflightDataSourceSpec struct {
	Source map[string]storageReference `json:"source"`
}

type preflightDataSourceStatus struct {
	Source     map[string]storageReference `json:"source"`
	Conditions []preflightCondition        `json:"conditions"`
}

type preflightCondition struct {
	Type   string `json:"type"`
	Status string `json:"status"`
}

type preflightStorageProfile struct {
	Status preflightStorageProfileStatus `json:"status"`
}

type preflightStorageProfileStatus struct {
	SnapshotClass     string            `json:"snapshotClass"`
	ClaimPropertySets []json.RawMessage `json:"claimPropertySets"`
}

func validatePreflightReference(kind string, ref storageReference) error {
	if ref.Name == "" || ref.Namespace == "" || strings.HasPrefix(ref.Name, "-") || strings.HasPrefix(ref.Namespace, "-") {
		return fmt.Errorf("%s source requires name and namespace", kind)
	}
	return nil
}
func readPreflightResource(ctx context.Context, kubeconfig, kind, name, namespace string, target any) error {
	if name == "" || strings.HasPrefix(name, "-") {
		return fmt.Errorf("%s requires an explicit resource name", kind)
	}
	args := []string{"get", kind, name}
	if namespace != "" {
		args = append(args, "-n", namespace)
	}
	args = append(args, "-o", "json", "--ignore-not-found")
	data, err := SnapshotCommand(ctx, kubeconfig, nil, args...)
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(data)) == "" {
		return fmt.Errorf("%s %s/%s: %w", kind, namespace, name, errPreflightNotFound)
	}
	if err := json.Unmarshal(data, target); err != nil {
		return fmt.Errorf("decode %s %s/%s: %w", kind, namespace, name, err)
	}
	return nil
}
func readPreflightStorageClass(ctx context.Context, kubeconfig, name string) (map[string]any, error) {
	var result map[string]any
	err := readPreflightResource(ctx, kubeconfig, "storageclass", name, "", &result)
	return result, err
}
func readPreflightStorageProfile(ctx context.Context, kubeconfig, name string) (preflightStorageProfile, error) {
	var result preflightStorageProfile
	err := readPreflightResource(ctx, kubeconfig, "storageprofile", name, "", &result)
	return result, err
}
func readPreflightPVC(ctx context.Context, kubeconfig string, ref storageReference) (preflightPVC, error) {
	var result preflightPVC
	if err := validatePreflightReference("pvc", ref); err != nil {
		return result, err
	}
	err := readPreflightResource(ctx, kubeconfig, "pvc", ref.Name, ref.Namespace, &result)
	return result, err
}
func readPreflightSnapshot(ctx context.Context, kubeconfig string, ref storageReference) (preflightSnapshot, error) {
	var result preflightSnapshot
	if err := validatePreflightReference("volumesnapshot", ref); err != nil {
		return result, err
	}
	err := readPreflightResource(ctx, kubeconfig, "volumesnapshot", ref.Name, ref.Namespace, &result)
	return result, err
}
func readPreflightDataSource(ctx context.Context, kubeconfig string, ref storageReference) (preflightDataSource, error) {
	var result preflightDataSource
	if err := validatePreflightReference("datasource", ref); err != nil {
		return result, err
	}
	err := readPreflightResource(ctx, kubeconfig, "datasource", ref.Name, ref.Namespace, &result)
	return result, err
}
