# Storage placement and preflight

VM image operations can use storage in three roles: the target disk, an
existing boot source, and temporary scratch space. These roles can use different
StorageClasses. Before a certification run, check their effective placement
against the requirements of the selected scenario. Setting the backend's
`storage_class` does not automatically move an existing image or configure CDI
scratch space.

## Select and inspect storage

| Role | StorageClass selection | What to verify |
| --- | --- | --- |
| Target disk | The workload can specify a class. If it does not, the applicable virtualization or Kubernetes default can be used. | The effective target class must be the selected backend's `storage_class`, for the storage under test. |
| Boot source | A DataSource refers to an existing PVC or snapshot. Its storage was selected when that image was created, potentially using the default at the time. | Inspect the source and select or prepare an image on suitable storage. Changing today's default does not move an existing source. |
| CDI scratch | Some image imports and uploads need a temporary scratch PVC. CDI selects its class independently, using its configured scratch class or a fallback. | When scratch is required, confirm the effective class can provision a PVC with `Filesystem` volume mode and `ReadWriteOnce` access. Check placement against the scenario. |

The default virtualization class takes precedence over the Kubernetes default
when creating a VM disk image. CDI's scratch-class selection has its own
fallback order. See [KubeVirt's default storage class guidance](https://kubevirt.io/monitoring/runbooks/CDIDefaultStorageClassDegraded.html)
and the [CDI configuration reference](https://kubevirt.io/cdi-api-reference/v1.59.0/definitions.html).
The [cluster setup guide](cluster-setup-guide.md#step-3--install-the-csi-driver-and-define-required-storageclasses)
shows how to configure storage and scratch space.

For example, inspect the configured classes and a DataSource with:

```bash
oc get storageclass
oc get cdiconfig config -o yaml
oc get datasource <name> -n <namespace> -o yaml
```

Follow the DataSource reference to its PVC or snapshot and inspect its storage.
For a snapshot, the original PVC may no longer exist; inspect the available
snapshot and snapshot content as well. The effective scratch class is reported
in `CDIConfig.status.scratchSpaceStorageClass`; CDI can fall back to the
DataVolume's class when no scratch class is resolved.

The target must use the intended storage under test. Source and scratch do not
universally have to use the same physical array as the target, but their
placement can affect the operation and what a scenario measures. Check the
scenario's requirements and the storage vendor's configuration. A StorageClass
name or shared CSI driver alone does not establish which physical array backs a
volume: different classes can use the same array, and a class can be configured
to provision from multiple backends.

## What preflight checks

Storage preflight inspects the prepared VM, DataVolume, and PVC manifests. It
checks whether each resolved target class exists and matches the selected
backend, reporting an error when it does not. For DataVolume `spec.storage`, it
reports effective defaults from the class's CDI StorageProfile. It does not
reject explicit settings solely because they differ from profile recommendations.

Source images must exist and be ready. Preflight also investigates their
original StorageClass: it logs a match as information, and logs a mismatch or
unknown class as a warning. These source-class findings do not block execution;
an administrator may intentionally use another class. Clone behavior depends on
the source, target, configuration, and storage capabilities. A matching class
does not prove that a storage-assisted clone will be used, and a mismatch does
not prove an operation will fail.

For a PVC source, preflight reads the StorageClass directly from the bound PVC.
For a snapshot source, when the original PVC still exists, it compares that
PVC's PV CSI volume identity with the bound snapshot content before using its
class. If that does not establish the class, preflight can infer it for
CDI-managed image imports from the DataImportCron's recorded import-class
annotation, after checking that related snapshot and import metadata agree.
This is an inference, not proof of physical placement; other configurations and
sources are possible. Missing, inaccessible, or inconsistent provenance leaves
the class unknown and produces a warning. A ready snapshot can remain usable
after its original PVC is deleted.

When available, preflight compares the snapshot driver from bound snapshot
content with the target provisioner: a mismatch is an error for a direct
snapshot restore and a warning for CDI. It reports the source volume mode, but
does not validate its compatibility with the target. Preflight also checks
cross-namespace CDI clone permissions using the current identity. Denied clone
authorization blocks the run; an unavailable authorization decision produces a
warning. For registry imports and uploads requiring scratch, preflight checks
the resolved scratch class and reports the required Filesystem and
ReadWriteOnce properties. It does not infer provisioning support from a
StorageProfile alone.

These checks do not prove that provisioning, capacity, backend connectivity,
admission policies, or a particular clone strategy will work. Validate the actual
workload and consult the storage vendor when placement or compatibility is in
question.
