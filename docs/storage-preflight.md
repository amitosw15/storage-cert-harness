# Storage placement and preflight

A VM workload can use three StorageClasses: one for its target disk, one for
its existing boot image, and one for CDI scratch space. Set the harness
backend's `storage_class` to the target under test; this does not move the boot
image or configure scratch storage.

## Before a run

| Storage | What to configure and check |
| --- | --- |
| Target | Set the backend `storage_class` to the intended class. If a workload omits a class, OpenShift Virtualization or Kubernetes defaults can apply. |
| Boot image | Select a ready DataSource backed by a PVC or snapshot suitable for the workload. Its storage was chosen when the image was created, possibly using an earlier default. |
| CDI scratch | If an import or upload needs scratch space, ensure the selected scratch class supports `Filesystem` and `ReadWriteOnce`. CDI chooses scratch independently of the boot image and target. |

Source and scratch storage do not always need to be on the target array, but
placement can affect both compatibility and results. Confirm the scenario's
requirements with your storage administrator; matching StorageClass names or
CSI drivers do not prove that volumes share a backend.

Inspect the relevant configuration:

```bash
oc get storageclass
oc get cdiconfig config -o yaml
oc get datasource <image-name> -n <image-namespace> -o yaml
oc get storageprofile <target-storage-class> -o yaml
```

Follow the DataSource to its backing PVC or snapshot. The effective scratch
class appears in `CDIConfig.status.scratchSpaceStorageClass`; if no scratch
class is resolved, CDI can fall back to the DataVolume's class. See the
[cluster setup guide](cluster-setup-guide.md#step-3--install-the-csi-driver-and-define-required-storageclasses),
[default storage class guidance](https://kubevirt.io/monitoring/runbooks/CDIDefaultStorageClassDegraded.html),
and [CDI configuration reference](https://kubevirt.io/cdi-api-reference/v1.59.0/definitions.html).

### StorageProfile defaults

When a DataVolume omits access modes or volume mode, CDI uses the target
StorageProfile. Check `status.claimPropertySets` for a combination supported
by the driver and workload. If it is missing or unsuitable, the administrator
can set `spec.claimPropertySets`, for example:

```yaml
spec:
  claimPropertySets:
    - accessModes: [ReadWriteMany]
      volumeMode: Filesystem
```

This example is not suitable for every driver. Check the reconciled status
after a change. Deleting a profile does not guarantee CDI will fill in missing
values when it recreates it.

### NetApp Trident NAS Economy

For an Economy target where snapshot cloning is unsupported, one tested image
clone configuration is a **PVC source on regular `ontap-nas`** and a target
StorageProfile with host-assisted copying:

```yaml
spec:
  cloneStrategy: copy
```

Set this on the Economy target's StorageProfile, and select a DataSource whose
`spec.source.pvc` points to the regular NAS image PVC:

```yaml
spec:
  source:
    pvc:
      name: <regular-nas-image-pvc>
      namespace: <image-namespace>
```

A DataSource backed by a
snapshot is a different source; choosing it alone does not establish that CDI
will use a host-assisted PVC copy. The setting applies to other workloads using
that StorageProfile, so coordinate it with the administrator. Copying can take
longer than storage-assisted cloning and affect measured VM startup times.
Validate the full workload: a successful copy does not prove snapshot-based
operations will work. See the [CDI clone strategy reference](https://github.com/kubevirt/containerized-data-importer/blob/main/doc/storageprofile.md).

For Trident 26.06, the `ontap-nas-economy` snapshot path requires snapshot
directory access; a disabled setting can produce `snapshots cannot be taken if
snapdir access is disabled`. Inspect the backend default and the effective
setting on an existing volume:

```bash
oc get tridentbackendconfig <backend-name> -n trident -o yaml
oc get tridentvolume <pv-name> -n trident -o yaml
```

The backend field is `spec.defaults.snapshotDir`; the Trident volume field is
`config.snapshotDirectory`. Configure new volumes according to the [Trident
ONTAP NAS documentation](https://docs.netapp.com/us-en/trident-2606/trident-use/ontap-nas-examples.html).
Changing the default does not update existing volumes. Economy qtrees can share
a FlexVol, so coordinate changes to existing pools with the administrator.
Standard `ontap-nas` uses a different snapshot path. `.snapshot` access is
vendor-specific, not a Kubernetes PVC or VolumeSnapshot property, and the
harness does not inspect it. Snapshot creation alone does not establish that
writable cloning or restore is supported.

## What preflight checks

- **Target:** checks that each resolved target class exists and matches the
  backend, reporting an error otherwise. It reports StorageProfile defaults and
  does not reject explicit settings solely for differing from them.
- **Source:** checks that the image exists and is ready. It logs a matching
  source class and warns on a mismatch or unknown class; these findings do not
  block execution. A different source class may be intentional.
- **Snapshot compatibility:** compares the bound snapshot's driver with the
  target provisioner when available. A mismatch is an error for a direct
  snapshot restore and a warning for CDI. It reports source volume mode but
  does not validate target compatibility.
- **Permissions and scratch:** checks cross-namespace clone authorization;
  denial blocks the run and an unavailable decision produces a warning. For
  imports or uploads needing scratch, it reports the resolved class and the
  required `Filesystem` and `ReadWriteOnce` properties.

For a PVC source, preflight reads its StorageClass directly. For a snapshot,
it first checks whether the original PVC and bound snapshot content identify
the same volume. Otherwise, for CDI-managed imports it may infer the class
from consistent DataImportCron and snapshot metadata. Unknown or inconsistent
provenance produces a warning; an inference does not prove physical placement.
A ready snapshot can outlive its source PVC.

Preflight cannot prove that provisioning, capacity, connectivity, admission, or
a chosen clone strategy will work. Validate the actual workload with the
storage configuration and consult the vendor where needed.
