package clustercheck

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// NamespacedName identifies an existing namespaced resource.
type NamespacedName struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
}

// ResourceMetadata contains the metadata needed by the cluster operations.
type ResourceMetadata struct {
	UID               string            `json:"uid"`
	DeletionTimestamp string            `json:"deletionTimestamp"`
	Labels            map[string]string `json:"labels"`
	Name              string            `json:"name"`
	Namespace         string            `json:"namespace"`
	Annotations       map[string]string `json:"annotations"`
}

// VolumeSnapshotClass contains the driver and default-class metadata used in selection.
type VolumeSnapshotClass struct {
	Metadata ResourceMetadata `json:"metadata"`
	Driver   string           `json:"driver"`
}

// PersistentVolumeClaim contains the storage selection and readiness fields used by adapters.
type PersistentVolumeClaim struct {
	Spec   PVCSpec   `json:"spec"`
	Status PVCStatus `json:"status"`
}
type PVCSpec struct {
	StorageClassName string `json:"storageClassName"`
	VolumeMode       string `json:"volumeMode"`
	VolumeName       string `json:"volumeName"`
}
type PVCStatus struct {
	Phase string `json:"phase"`
}

// VolumeSnapshot contains the identity, class and readiness fields used by adapters.
type VolumeSnapshot struct {
	Metadata NamespacedName       `json:"metadata"`
	Spec     VolumeSnapshotSpec   `json:"spec"`
	Status   VolumeSnapshotStatus `json:"status"`
}
type VolumeSnapshotSpec struct {
	VolumeSnapshotClassName string `json:"volumeSnapshotClassName"`
	Source                  struct {
		PersistentVolumeClaimName string `json:"persistentVolumeClaimName"`
	} `json:"source"`
}
type VolumeSnapshotStatus struct {
	ReadyToUse                     bool   `json:"readyToUse"`
	BoundVolumeSnapshotContentName string `json:"boundVolumeSnapshotContentName"`
}

// DataSource contains CDI source references and readiness conditions.
type DataSource struct {
	Spec   DataSourceSpec   `json:"spec"`
	Status DataSourceStatus `json:"status"`
}
type DataSourceSpec struct {
	Source map[string]NamespacedName `json:"source"`
}
type DataSourceStatus struct {
	Source     map[string]NamespacedName `json:"source"`
	Conditions []ResourceCondition       `json:"conditions"`
}
type ResourceCondition struct {
	Type   string `json:"type"`
	Status string `json:"status"`
}

func getResource(ctx context.Context, kubeconfig, kind, name, namespace string, target any) error {
	if err := validateResourceName(kind, name); err != nil {
		return err
	}
	args := []string{"get", kind, name}
	if namespace != "" {
		args = append(args, "-n", namespace)
	}
	args = append(args, "-o", "json", "--ignore-not-found")
	data, err := runKubeCommand(ctx, kubeconfig, nil, args...)
	if err != nil {
		return err
	}
	identity := name
	if namespace != "" {
		identity = namespace + "/" + name
	}
	if strings.TrimSpace(string(data)) == "" {
		return fmt.Errorf("%s %s: %w", kind, identity, ErrNotFound)
	}
	if err := json.Unmarshal(data, target); err != nil {
		return fmt.Errorf("decode %s %s: %w", kind, identity, err)
	}
	return nil
}
func validateResourceName(kind, name string) error {
	if name == "" || strings.HasPrefix(name, "-") {
		return fmt.Errorf("%s requires an explicit resource name, got %q", kind, name)
	}
	return nil
}

func validateReference(kind string, ref NamespacedName) error {
	if ref.Name == "" || ref.Namespace == "" {
		return fmt.Errorf("%s source requires name and namespace", kind)
	}
	if err := validateResourceName(kind, ref.Name); err != nil {
		return err
	}
	return validateResourceName("namespace", ref.Namespace)
}

// GetStorageClass reads the full source object so copying preserves vendor-specific fields.
func GetStorageClass(ctx context.Context, kubeconfig, name string) (map[string]any, error) {
	var value map[string]any
	err := getResource(ctx, kubeconfig, "storageclass", name, "", &value)
	return value, err
}

// GetStorageProfile reads a required profile; absence returns ErrNotFound.
func GetStorageProfile(ctx context.Context, kubeconfig, name string) (StorageProfile, error) {
	var value StorageProfile
	err := getResource(ctx, kubeconfig, "storageprofile", name, "", &value)
	return value, err
}

// GetVolumeSnapshotClass reads an existing snapshot class.
func GetVolumeSnapshotClass(ctx context.Context, kubeconfig, name string) (VolumeSnapshotClass, error) {
	var value VolumeSnapshotClass
	err := getResource(ctx, kubeconfig, "volumesnapshotclass", name, "", &value)
	return value, err
}

// ListVolumeSnapshotClasses reads all classes for driver/default selection.
func ListVolumeSnapshotClasses(ctx context.Context, kubeconfig string) ([]VolumeSnapshotClass, error) {
	data, err := runKubeCommand(ctx, kubeconfig, nil, "get", "volumesnapshotclass", "-o", "json")
	if err != nil {
		return nil, err
	}
	var value struct {
		Items []VolumeSnapshotClass `json:"items"`
	}
	if err := json.Unmarshal(data, &value); err != nil {
		return nil, fmt.Errorf("decode volumesnapshotclass list: %w", err)
	}
	return value.Items, nil
}

// GetPVC reads an existing claim in an explicit namespace.
func GetPVC(ctx context.Context, kubeconfig string, ref NamespacedName) (PersistentVolumeClaim, error) {
	var value PersistentVolumeClaim
	if err := validateReference("pvc", ref); err != nil {
		return value, err
	}
	err := getResource(ctx, kubeconfig, "pvc", ref.Name, ref.Namespace, &value)
	return value, err
}

// GetVolumeSnapshot reads an existing snapshot in an explicit namespace.
func GetVolumeSnapshot(ctx context.Context, kubeconfig string, ref NamespacedName) (VolumeSnapshot, error) {
	var value VolumeSnapshot
	if err := validateReference("volumesnapshot", ref); err != nil {
		return value, err
	}
	err := getResource(ctx, kubeconfig, "volumesnapshot", ref.Name, ref.Namespace, &value)
	return value, err
}

// GetDataSource reads an existing CDI source in an explicit namespace.
func GetDataSource(ctx context.Context, kubeconfig string, ref NamespacedName) (DataSource, error) {
	var value DataSource
	if err := validateReference("datasource", ref); err != nil {
		return value, err
	}
	err := getResource(ctx, kubeconfig, "datasource", ref.Name, ref.Namespace, &value)
	return value, err
}

func createResource(ctx context.Context, kubeconfig string, data []byte, kind string, target any) (bool, error) {
	var resource struct {
		Kind     string `yaml:"kind"`
		Metadata struct {
			Name      string `yaml:"name"`
			Namespace string `yaml:"namespace"`
		} `yaml:"metadata"`
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&resource); err != nil {
		return false, fmt.Errorf("decode create manifest: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return false, fmt.Errorf("create %s requires exactly one YAML document", kind)
	}
	if resource.Kind != kind {
		return false, fmt.Errorf("create %s requires a %s manifest, got %q", kind, kind, resource.Kind)
	}
	if kind == "VolumeSnapshot" || kind == "Pod" {
		if resource.Metadata.Namespace == "" {
			return false, fmt.Errorf("%s requires an explicit metadata.namespace", kind)
		}
		if kind == "Pod" {
			if err := validateResourceName("pod", resource.Metadata.Name); err != nil {
				return false, err
			}
		}
	}
	result, err := runKubeCommand(ctx, kubeconfig, data, "create", "-f", "-", "-o", "json")
	if err != nil {
		return false, err
	}
	if err := json.Unmarshal(result, target); err != nil {
		return true, fmt.Errorf("decode created %s: %w", kind, err)
	}
	return true, nil
}

// CreateStorageClass strictly creates a new class and returns its generated name.
func CreateStorageClass(ctx context.Context, kubeconfig string, data []byte) (string, error) {
	var result struct {
		Metadata ResourceMetadata `json:"metadata"`
	}
	if _, err := createResource(ctx, kubeconfig, data, "StorageClass", &result); err != nil {
		return "", err
	}
	if result.Metadata.Name == "" {
		return "", fmt.Errorf("created storage class has no name")
	}
	return result.Metadata.Name, nil
}

// CreateVolumeSnapshot strictly creates a new snapshot from rendered bytes.
func CreateVolumeSnapshot(ctx context.Context, kubeconfig string, data []byte) (VolumeSnapshot, error) {
	var result VolumeSnapshot
	_, err := createResource(ctx, kubeconfig, data, "VolumeSnapshot", &result)
	return result, err
}

// PatchStorageProfile applies an explicit JSON merge patch to an existing profile.
func PatchStorageProfile(ctx context.Context, kubeconfig, name string, patch []byte) error {
	if err := validateResourceName("storageprofile", name); err != nil {
		return err
	}
	_, err := runKubeCommand(ctx, kubeconfig, nil, "patch", "storageprofile", name, "--type=merge", "-p", string(patch))
	return err
}

// WaitForStorageProfileCreated waits for CDI's profile object without creating it.
func WaitForStorageProfileCreated(ctx context.Context, kubeconfig, name string, timeout time.Duration) error {
	if err := validateResourceName("storageprofile", name); err != nil {
		return err
	}
	_, err := runKubeCommand(ctx, kubeconfig, nil, "wait", "--for=create", "storageprofile/"+name, "--timeout="+timeout.String())
	return err
}

// WaitForVolumeSnapshotReady waits for the named snapshot's readyToUse condition.
func WaitForVolumeSnapshotReady(ctx context.Context, kubeconfig string, ref NamespacedName, timeout time.Duration) error {
	if err := validateReference("volumesnapshot", ref); err != nil {
		return err
	}
	_, err := runKubeCommand(ctx, kubeconfig, nil, "wait", "volumesnapshot/"+ref.Name, "-n", ref.Namespace, "--for=jsonpath={.status.readyToUse}=true", "--timeout="+timeout.String())
	return err
}

// DeleteStorageClass deletes only the named class; an absent class is already cleaned up.
func DeleteStorageClass(ctx context.Context, kubeconfig, name string) error {
	if err := validateResourceName("storageclass", name); err != nil {
		return err
	}
	_, err := runKubeCommand(ctx, kubeconfig, nil, "delete", "storageclass", name, "--ignore-not-found", "--wait=false")
	return err
}

// DeleteVolumeSnapshot deletes only the named snapshot; absence is a successful cleanup.
func DeleteVolumeSnapshot(ctx context.Context, kubeconfig string, ref NamespacedName) error {
	if err := validateReference("volumesnapshot", ref); err != nil {
		return err
	}
	_, err := runKubeCommand(ctx, kubeconfig, nil, "delete", "volumesnapshot", ref.Name, "-n", ref.Namespace, "--ignore-not-found", "--wait=false")
	return err
}

// GetNamespace reads an existing namespace without creating it.
func GetNamespace(ctx context.Context, kubeconfig, name string) (ResourceMetadata, error) {
	var value struct {
		Metadata ResourceMetadata `json:"metadata"`
	}
	err := getResource(ctx, kubeconfig, "namespace", name, "", &value)
	return value.Metadata, err
}

// CreateNamespace strictly creates a namespace from a rendered manifest.
func CreateNamespace(ctx context.Context, kubeconfig string, data []byte) (ResourceMetadata, error) {
	var value struct {
		Metadata ResourceMetadata `json:"metadata"`
	}
	_, err := createResource(ctx, kubeconfig, data, "Namespace", &value)
	return value.Metadata, err
}

// EnsureNamespace returns created=true only when this call created the namespace.
func EnsureNamespace(ctx context.Context, kubeconfig, name string, data []byte) (created bool, err error) {
	_, err = GetNamespace(ctx, kubeconfig, name)
	if err == nil {
		return false, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return false, err
	}
	var resource struct {
		Metadata struct {
			Name string `yaml:"name"`
		} `yaml:"metadata"`
	}
	if err := yaml.Unmarshal(data, &resource); err != nil {
		return false, err
	}
	if resource.Metadata.Name != name {
		return false, fmt.Errorf("namespace manifest name %q does not match %q", resource.Metadata.Name, name)
	}
	var result struct {
		Metadata ResourceMetadata `json:"metadata"`
	}
	created, err = createResource(ctx, kubeconfig, data, "Namespace", &result)
	if errors.Is(err, ErrAlreadyExists) {
		_, err = GetNamespace(ctx, kubeconfig, name)
		return false, err
	}
	return created, err
}
