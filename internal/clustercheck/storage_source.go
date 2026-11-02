package clustercheck

import (
	"context"
	"errors"
	"fmt"
)

type storageSource struct {
	Class      string
	Driver     string
	VolumeMode string
	Kind       string
	Reference  storageReference
	Notes      []string
}

func inspectStorageSource(ctx context.Context, kubeconfig, kind string, ref storageReference) (storageSource, error) {
	result := storageSource{Reference: ref}
	switch kind {
	case "DataSource":
		ds, err := readPreflightDataSource(ctx, kubeconfig, ref)
		if err != nil {
			return result, err
		}
		ready := false
		for _, condition := range ds.Status.Conditions {
			ready = ready || (condition.Type == "Ready" && condition.Status == "True")
		}
		if !ready {
			return result, fmt.Errorf("DataSource %s/%s is not Ready: %w", ref.Namespace, ref.Name, errStorageSourceNotReady)
		}
		sources := ds.Spec.Source
		if _, nested := sources["dataSource"]; nested {
			sources = ds.Status.Source
		}
		for _, candidate := range []string{"pvc", "snapshot"} {
			if source, ok := sources[candidate]; ok && source.Name != "" {
				if source.Namespace == "" {
					source.Namespace = ref.Namespace
				}
				return inspectStorageSource(ctx, kubeconfig, candidate, source)
			}
		}
		return result, fmt.Errorf("DataSource %s/%s has no resolved PVC or snapshot source: %w", ref.Namespace, ref.Name, errStorageSourceNotReady)
	case "pvc", "PersistentVolumeClaim":
		result.Kind = "pvc"
		pvc, err := readPreflightPVC(ctx, kubeconfig, ref)
		if err != nil {
			return result, err
		}
		if pvc.Status.Phase != "Bound" {
			return result, fmt.Errorf("source PVC %s/%s is not Bound: %w", ref.Namespace, ref.Name, errStorageSourceNotReady)
		}
		result.Class = pvc.Spec.StorageClassName
		result.VolumeMode = pvc.Spec.VolumeMode
		if result.VolumeMode == "" {
			result.VolumeMode = "Filesystem"
		}
		return result, nil
	case "snapshot", "VolumeSnapshot":
		result.Kind = "snapshot"
		snapshot, err := readPreflightSnapshot(ctx, kubeconfig, ref)
		if err != nil {
			return result, err
		}
		if !snapshot.Status.ReadyToUse {
			return result, fmt.Errorf("source snapshot %s/%s is not readyToUse: %w", ref.Namespace, ref.Name, errStorageSourceNotReady)
		}
		result, err = inspectSnapshotVolume(ctx, kubeconfig, ref, snapshot)
		if err != nil {
			return result, err
		}
		if result.Class == "" {
			class, note := inferCDISnapshotClass(ctx, kubeconfig, ref, snapshot)
			result.Class = class
			if note != "" {
				result.Notes = append(result.Notes, note)
			}
		}
		return result, nil
	default:
		return result, fmt.Errorf("unsupported source kind %q", kind)
	}
}

func inspectSnapshotVolume(ctx context.Context, kubeconfig string, ref storageReference, snapshot preflightSnapshot) (storageSource, error) {
	result := storageSource{Kind: "snapshot", Reference: ref}
	contentName := snapshot.Status.BoundVolumeSnapshotContentName
	if contentName == "" {
		result.Notes = append(result.Notes, "ready snapshot has no bound content reference; snapshot-content provenance is unavailable")
		return result, nil
	}
	content, err := readPreflightSnapshotContent(ctx, kubeconfig, contentName)
	if err != nil {
		result.Notes = append(result.Notes, "snapshot is ready; snapshot-content provenance could not be read: "+err.Error())
		return result, nil
	}
	bound := content.Spec.VolumeSnapshotRef
	if (bound.Name != "" && bound.Name != ref.Name) || (bound.Namespace != "" && bound.Namespace != ref.Namespace) {
		return result, fmt.Errorf("snapshot content %q references another snapshot: %w", contentName, errStorageSourceNotReady)
	}
	result.Driver = content.Spec.Driver
	result.VolumeMode = content.Spec.SourceVolumeMode
	name := snapshot.Spec.Source.PersistentVolumeClaimName
	if name == "" || content.Spec.Source.VolumeHandle == "" {
		return result, nil
	}
	pvc, err := readPreflightPVC(ctx, kubeconfig, storageReference{Name: name, Namespace: ref.Namespace})
	if errors.Is(err, errPreflightNotFound) {
		return result, nil
	}
	if err != nil {
		result.Notes = append(result.Notes, "snapshot is ready; original PVC provenance could not be read")
		return result, nil
	}
	if pvc.Spec.VolumeName == "" {
		return result, nil
	}
	pv, err := readPreflightPV(ctx, kubeconfig, pvc.Spec.VolumeName)
	if err != nil {
		result.Notes = append(result.Notes, "snapshot is ready; original PV provenance could not be read")
		return result, nil
	}
	if pv.Spec.CSI != nil && pv.Spec.CSI.Driver == result.Driver && pv.Spec.CSI.VolumeHandle == content.Spec.Source.VolumeHandle {
		result.Class = pv.Spec.StorageClassName
	}
	return result, nil
}
