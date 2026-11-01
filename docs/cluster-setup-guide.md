# Cluster Setup Guide for Partners

This guide walks a storage partner through preparing an OpenShift cluster to
run the storage certification harness from scratch.

---

## Prerequisites Checklist

Complete every item before you begin. The harness `preflight` command also
verifies most of these automatically.

- [ ] OpenShift cluster **4.20 or later**, multi-node (3 control-plane + ≥ 3 workers)
- [ ] SNO cluster **not** used
- [ ] Worker nodes meet hardware minimums (see [cluster-requirements.md](cluster-requirements.md#hardware-minimums))
- [ ] `cluster-admin` credentials available (kubeconfig or token)
- [ ] `oc` / `kubectl` installed on the machine that will run the harness
- [ ] `podman` (≥ 4.x) installed on the machine that will run the harness
- [ ] Storage array is cabled / routed; all worker nodes can reach it
- [ ] `multipathd` enabled and configured on all worker nodes (iSCSI and FC plans)
- [ ] `iscsid` enabled on all worker nodes (iSCSI plans only)
- [ ] CSI driver installed and **all driver pods are Running/Ready**
- [ ] `StorageClass` for the storage under test exists in the cluster
- [ ] `VolumeSnapshotClass` created and exists in the cluster
- [ ] CDI scratch space `StorageClass` (filesystem-mode, backed by the storage under test) created and configured (VM / CDI test plans)
- [ ] `thresholds.json` present with the repository certification inputs
- [ ] `backends.yaml` prepared with your array's details (copy `backends.example.yaml`)
- [ ] A test-plan YAML chosen from `plans/` (or a custom plan)
- [ ] Image registry reachable from the execution host (or internal mirror configured)

---

## Step 1 — Confirm a Healthy Qualifying Cluster

Ensure you have an OCP cluster that satisfies all requirements in
[cluster-requirements.md](cluster-requirements.md) before proceeding. Cluster
provisioning itself is out of scope here — refer to the
[official OpenShift installation documentation](https://docs.openshift.com/container-platform/latest/installing/index.html)
if you still need to deploy one.

Verify the cluster is fully healthy:

```bash
export KUBECONFIG=/path/to/kubeconfig

# All nodes Ready
oc get nodes

# No degraded cluster operators
oc get co | grep -v "True.*False.*False"

# No failing Pods in kube-system / openshift-*
oc get pods -A | grep -Ev "(Running|Completed|Succeeded)"
```

Expected: all nodes `Ready`, all cluster operators `Available=True / Degraded=False / Progressing=False`. Resolve any issues before continuing.

---

## Step 2 — Configure Storage Transport Prerequisites

> Skip if your test plan uses NFS or NVMe-oF/TCP only.

For **iSCSI** and **FC** plans, `multipathd` (and `iscsid` for iSCSI) must be
active on all worker nodes before running certification. Configure them via
whichever method suits your setup:

- **Vendor CSI auto-configuration** — some drivers (e.g. HPE / NetApp)
can deploy the required `MachineConfig` objects automatically.
Check your CSI driver's documentation first; if supported, this can be
handled as part of [Step 3](#step-3--install-the-csi-driver-and-define-required-storageclasses).
- **Manual** `MachineConfig` — if your driver does not handle it, apply the
appropriate `MachineConfig` objects yourself. See the
[OCP documentation on enabling iSCSI initiators](https://docs.openshift.com/container-platform/latest/storage/persistent_storage/persistent-storage-iscsi.html)
and your CSI driver's multipath configuration guide for the exact settings.

---

## Step 3 — Install the CSI Driver and Define Required StorageClasses

Install your storage vendor's CSI driver following the vendor's documentation.
After installation, confirm the driver is healthy:

```bash
# All CSI driver pods should be Running
oc get pods -n <csi-driver-namespace>
```

### StorageClass under test

Create the `StorageClass`(es) that the certification plan will target. The
class name(s) must match what you configure in `backends.yaml` (Step 5). Refer
to your CSI driver's documentation for the correct provisioner name and
parameters.

```bash
# Confirm the StorageClass is present and has the correct provisioner
oc get storageclass <your-storage-class> -o wide
```

Annotate a default storage class for OCP/CNV:

```bash
oc annotate storageclass <your-storage-class> \
  storageclass.kubevirt.io/is-default-virt-class="true"
```

### VolumeSnapshotClass

Create a `VolumeSnapshotClass` backed by the same CSI driver and confirm it
exists:

```bash
oc get volumesnapshotclass
```

### CDI scratch space (VM / CDI test plans only)

CDI requires a filesystem-mode `StorageClass` for scratch volumes during import
— block-mode classes are not usable. Create a filesystem-mode `StorageClass`
backed by the storage under test, then set it via `CDIConfig` or through the
`HyperConverged` CR if OpenShift Virtualization is installed:

```bash
# Via CDIConfig directly
oc patch cdiconfig config --type=merge \
  -p '{"spec":{"scratchSpaceStorageClass":"<filesystem-storageclass-name>"}}'
```

```yaml
# Via HyperConverged CR (OpenShift Virtualization)
apiVersion: hco.kubevirt.io/v1beta1
kind: HyperConverged
metadata:
  name: kubevirt-hyperconverged
  namespace: openshift-cnv
spec:
  scratchSpaceStorageClass: "<filesystem-storageclass-name>"
```

**Optional smoke test** — launch a pod with a PVC attached and confirm it mounts
cleanly before investing time in a full certification run.

---

## Step 4 — Configure Node Remediation (HA test plans only)

Install and configure node remediation with your preferred remediation provider.
See [HA Testing — Node Remediation](cluster-requirements.md#ha-testing--node-remediation)
in the cluster requirements for operator choices and guidance.

---

## Step 5 — Prepare the Backend Configuration

Copy the example file and fill in your array's details:

```bash
cp backends.example.yaml backends.yaml
```

Edit `backends.yaml`:

```yaml
backends:
  - name: my-array          # referenced by --backend or plan backend:
    vendor: <vendor-name>
    storage_class: <storage-class-name>
    snapshot_class: <snapshot-class-name>
    params:
      # vendor-specific connection params (e.g. management endpoint, SVM name)
    secrets:
      # use file refs or k8s Secret refs — never plaintext credentials here
      username:
        from_file: /run/secrets/storage-username
      password:
        from_file: /run/secrets/storage-password
```

See [ADR-0005](../decisions/0005-test-plans-and-backend-config.md) for the full
credential-reference syntax. **Do not commit** `backends.yaml` — it is
gitignored.

`snapshot_class` selects the VolumeSnapshotClass for VM snapshot tests
(TR-VIRT-010 and TR-VIRT-027) and datasource-clone tests with `use_snapshot: true`.
Preflight checks that the class exists and that its driver matches the storage
class. If omitted, direct snapshots use the matching driver's default class;
VM snapshots use KubeVirt's StorageProfile/default resolution.

KubeVirt does not expose a snapshot-class selector on a VirtualMachineSnapshot.
For an explicit selection the harness creates a temporary copy of the backend
StorageClass, preserving its provisioning settings, and configures that copy's
CDI StorageProfile. This requires permission to create/delete StorageClasses and
read/patch StorageProfiles. Existing classes, profiles, and default annotations
are left untouched. Teardown deletes the temporary class.

For `use_snapshot: true`, the harness creates snapshots of the template's source
PVCs using the resolved class, then stages a VM template referencing those
snapshots. This requires snapshot create/delete permissions in the source PVC
namespaces. Source PVCs and DataSources are read-only; blank data disks remain
blank. Existing snapshot sources must already use the selected class. Teardown
removes snapshots created by the harness, subject to the class's deletion policy.

Reports include `storage_selections` for each snapshot-backed execution: the
backend storage class, resolved snapshot class, and any temporary storage class.
These also appear in the Markdown report and JUnit properties, including when
environment collection is skipped.

---

## Step 6 — Confirm `thresholds.json`

The repository certification inputs include `thresholds.json`. Keep the
catalog and thresholds from the same export and pass the file to the harness
with `--thresholds`.

---

## Step 7 — Choose a Test Plan

Pick a pre-made plan from `plans/` or author a custom one:

```bash
ls plans/
# example-smoke.yaml     — quick sanity run, subset of TRs
# example-performance.yaml — full performance tier
```

Open the plan and confirm the `backend:` field matches the name you chose in
`backends.yaml`.

---

## Step 8 — Run Preflight

The `preflight` sub-command validates connectivity, CSI health, and
configuration before committing to a full run:

```bash
bin/harness preflight \
  --catalog catalog.json \
  --plan plans/example-smoke.yaml \
  --backends backends.yaml \
  --backend my-array
```

All preflight checks must pass (exit 0) before proceeding.

---

## Step 9 — Run Certification

```bash
bin/harness run \
  --catalog catalog.json \
  --plan plans/example-performance.yaml \
  --backends backends.yaml \
  --backend my-array \
  --thresholds thresholds.json \
  --workdir ./reports \
  --output ./reports \
  --verbose
```

When the run completes, `reports/` will contain `report.json`, `report.md`,
`report.junit.xml`, `report.json`, `run.log`, and tool-generated result artifacts. Create the
submission archive described in
[`PARTNER-CERTIFICATION-GUIDE.md`](../PARTNER-CERTIFICATION-GUIDE.md).

---

### Storage preflight findings

Storage preflight inspects target disks in prepared VM, DataVolume, and PVC
manifests. It compares each resolved target StorageClass with the selected
backend, and reports an error if the disk would use another class or a missing
class. For DataVolume `spec.storage`, it reports the effective defaults from the
selected class's CDI StorageProfile. Explicit disk settings are not rejected
merely because they differ from the profile's recommendations.

Source images must exist and be ready. By default each TR requires the source
and target StorageClasses to match (`require_same_source_storage_class: true`).
A mismatch, or an original source StorageClass that cannot be established, blocks
preflight. To allow different classes for a particular TR, explicitly opt out:

```yaml
overrides:
  TR-VIRT-001:
    require_same_source_storage_class: false
```

With this override a known class difference produces a nonfatal warning that
cloning might not be accelerated and could require host-assisted copying.
Matching classes do not prove acceleration. A ready snapshot remains a valid
source after its original PVC is deleted, but strict mode cannot accept it
without reliable evidence of its original StorageClass. Surviving PVC names
alone are not evidence: preflight checks the source PV's CSI volume identity
against the bound snapshot content before using its class.

Snapshot driver and source volume mode are read from bound snapshot content.
A shared CSI driver does not establish compatibility across backend types.
Cross-namespace CDI cloning is checked using the current kubeconfig identity
and CDI's source permission rules. Permission checks use non-persisted
SelfSubjectAccessReview requests. Denied clone authorization blocks the run;
an unavailable authorization decision produces a warning. These checks do not
prove that all admission policies or subsequent controller operations will pass.

CDI scratch storage may use a different class from source and target storage.
For registry imports and uploads that require scratch space, preflight reads
`CDIConfig.status.scratchSpaceStorageClass`, falling back to the target class
when unset. It checks that the class exists and reports the required Filesystem
and ReadWriteOnce properties. It does not infer provisioning support from the
StorageProfile alone. Preflight is read-only; actual provisioning, capacity,
backend connectivity, and clone acceleration still require a workload test.

Live checks are opt-in tests in `internal/clustercheck`. Set
`STORAGE_PREFLIGHT_LIVE_KUBECONFIG`, `STORAGE_PREFLIGHT_LIVE_NAS_CLASS`,
`STORAGE_PREFLIGHT_LIVE_ECONOMY_CLASS`, `STORAGE_PREFLIGHT_LIVE_NAS_SOURCE`, and
`STORAGE_PREFLIGHT_LIVE_ECONOMY_SOURCE` to run `TestStoragePreflightLive` against
existing resources. Source values use `namespace/name`.

`TestStoragePreflightPatchedLive` additionally requires
`STORAGE_PREFLIGHT_LIVE_MUTATIONS=true` and
`STORAGE_PREFLIGHT_LIVE_SNAPSHOT_CLASS`. It creates disposable namespaces,
classes, profiles, sources, a small PVC/snapshot, and temporary reader RBAC. It
covers the source/target/scratch class combinations and patches those fixtures
to check failures and recovery. Cleanup removes the fixtures even after a test
failure. Existing defaults, CDI scratch configuration, classes, profiles, and
boot sources are not modified. These tests validate preflight behavior; they do
not claim that every class combination can boot an OS or use accelerated clones.
