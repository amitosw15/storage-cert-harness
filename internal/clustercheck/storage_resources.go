package clustercheck

import (
	"context"
	"encoding/json"
)

// StorageClass contains selection metadata without discarding the full-object getter used for copying.
type StorageClass struct {
	Metadata    ResourceMetadata `json:"metadata"`
	Provisioner string           `json:"provisioner"`
}

// CDIConfig contains the effective scratch configuration reconciled by CDI.
type CDIConfig struct {
	Status struct {
		ScratchSpaceStorageClass string `json:"scratchSpaceStorageClass"`
	} `json:"status"`
}

func ListStorageClasses(ctx context.Context, kubeconfig string) ([]StorageClass, error) {
	var list struct {
		Items []StorageClass `json:"items"`
	}
	data, err := runKubeCommand(ctx, kubeconfig, nil, "get", "storageclass", "-o", "json")
	if err != nil {
		return nil, err
	}
	err = json.Unmarshal(data, &list)
	return list.Items, err
}

func GetCDIConfig(ctx context.Context, kubeconfig string) (CDIConfig, error) {
	var config CDIConfig
	err := getResource(ctx, kubeconfig, "cdiconfig", "config", "", &config)
	return config, err
}

// VolumeSnapshotContent exposes concrete driver facts without interpreting opaque handles.
type VolumeSnapshotContent struct {
	Spec struct {
		Driver           string `json:"driver"`
		SourceVolumeMode string `json:"sourceVolumeMode"`
		Source           struct {
			VolumeHandle string `json:"volumeHandle"`
		} `json:"source"`
		VolumeSnapshotRef ResourceMetadata `json:"volumeSnapshotRef"`
	} `json:"spec"`
}

// PersistentVolume supplies the identity used to verify a surviving snapshot source PVC.
type PersistentVolume struct {
	Spec struct {
		StorageClassName string `json:"storageClassName"`
		CSI              *struct {
			Driver       string `json:"driver"`
			VolumeHandle string `json:"volumeHandle"`
		} `json:"csi"`
	} `json:"spec"`
}

func GetVolumeSnapshotContent(ctx context.Context, kubeconfig, name string) (VolumeSnapshotContent, error) {
	var content VolumeSnapshotContent
	err := getResource(ctx, kubeconfig, "volumesnapshotcontent", name, "", &content)
	return content, err
}

func GetPersistentVolume(ctx context.Context, kubeconfig, name string) (PersistentVolume, error) {
	var volume PersistentVolume
	err := getResource(ctx, kubeconfig, "pv", name, "", &volume)
	return volume, err
}

// DataImportCron records the current CDI image import and its storage provenance.
type DataImportCron struct {
	Metadata ResourceMetadata     `json:"metadata"`
	Spec     DataImportCronSpec   `json:"spec"`
	Status   DataImportCronStatus `json:"status"`
}

type DataImportCronSpec struct {
	ManagedDataSource string `json:"managedDataSource"`
}

type DataImportCronStatus struct {
	CurrentImports  []DataImportCronImport `json:"currentImports"`
	LastImportedPVC NamespacedName         `json:"lastImportedPVC"`
	SourceFormat    string                 `json:"sourceFormat"`
}

type DataImportCronImport struct {
	DataVolumeName string `json:"DataVolumeName"`
}

// GetDataImportCron reads an existing CDI import in an explicit namespace.
func GetDataImportCron(ctx context.Context, kubeconfig string, ref NamespacedName) (DataImportCron, error) {
	var value DataImportCron
	if err := validateReference("dataimportcron", ref); err != nil {
		return value, err
	}
	err := getResource(ctx, kubeconfig, "dataimportcron", ref.Name, ref.Namespace, &value)
	return value, err
}
