package clustercheck

import (
	"context"
	"fmt"
)

const dataImportCronLabel = "cdi.kubevirt.io/dataImportCron"
const importStorageClassAnnotation = "cdi.kubevirt.io/storage.import.storageClass"

func inferCDISnapshotClass(ctx context.Context, kubeconfig string, ref NamespacedName, snapshot VolumeSnapshot) (string, string) {
	name := snapshot.Metadata.Labels[dataImportCronLabel]
	if name == "" {
		return "", ""
	}
	cronRef := NamespacedName{Name: name, Namespace: ref.Namespace}
	cron, err := GetDataImportCron(ctx, kubeconfig, cronRef)
	if err != nil {
		return "", fmt.Sprintf("CDI import StorageClass could not be investigated: %v", err)
	}
	if cron.Metadata.DeletionTimestamp != "" || snapshot.Metadata.DeletionTimestamp != "" {
		return "", "CDI import StorageClass was not inferred because the import or snapshot is being deleted"
	}
	current := false
	for _, imported := range cron.Status.CurrentImports {
		current = current || imported.DataVolumeName == ref.Name
	}
	original := snapshot.Spec.Source.PersistentVolumeClaimName
	if cron.Status.SourceFormat != "snapshot" || !current || original == "" ||
		cron.Status.LastImportedPVC.Name != original || cron.Status.LastImportedPVC.Namespace != ref.Namespace {
		return "", "CDI import StorageClass was not inferred because the snapshot does not match the cron's current import"
	}
	class := cron.Metadata.Annotations[importStorageClassAnnotation]
	if class == "" || cron.Spec.ManagedDataSource == "" {
		return "", "CDI import has no recorded StorageClass or managed DataSource"
	}
	ds, err := GetDataSource(ctx, kubeconfig, NamespacedName{Name: cron.Spec.ManagedDataSource, Namespace: ref.Namespace})
	if err != nil {
		return "", fmt.Sprintf("CDI managed DataSource could not be investigated: %v", err)
	}
	ready := false
	for _, condition := range ds.Status.Conditions {
		ready = ready || (condition.Type == "Ready" && condition.Status == "True")
	}
	source, ok := ds.Spec.Source["snapshot"]
	if source.Namespace == "" {
		source.Namespace = ref.Namespace
	}
	if !ok || !ready || source != ref {
		return "", "CDI import StorageClass was not inferred because its managed DataSource does not reference this ready snapshot"
	}
	// A recreated snapshot must not inherit provenance from an earlier read.
	currentSnapshot, err := GetVolumeSnapshot(ctx, kubeconfig, ref)
	if err != nil || snapshot.Metadata.UID == "" || currentSnapshot.Metadata.UID != snapshot.Metadata.UID ||
		currentSnapshot.Metadata.DeletionTimestamp != "" || !currentSnapshot.Status.ReadyToUse ||
		currentSnapshot.Metadata.Labels[dataImportCronLabel] != name {
		return "", "CDI import StorageClass was not inferred because the snapshot identity or readiness changed during investigation"
	}
	return class, fmt.Sprintf("source StorageClass %q inferred from CDI DataImportCron %s/%s annotation %s for current import %s; this is CDI metadata, not a StorageClass field on the snapshot", class, ref.Namespace, name, importStorageClassAnnotation, ref.Name)
}
