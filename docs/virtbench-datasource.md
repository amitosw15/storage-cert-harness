# Virtbench DataSource selection

`harness run` and `harness preflight` accept `--datasource name` or
`--datasource namespace/name`. An unqualified name uses
`openshift-virtualization-os-images`. Selection precedence is CLI flag, resolved
per-test or variant `datasource` parameter, then the original template source.
The image namespace default applies only to explicit selections. Without an
override, an omitted or empty template `sourceRef.namespace` follows CDI's
default: the receiving DataVolume's namespace. Preflight checks each VM's actual
namespace, as selected by the workload's namespace prefix and VM range.
For example:

```sh
harness preflight --catalog catalog.json --plan plans/virtbench-fio-live-smoke.yaml \
  --backends backends.local.yaml --backend tlvbm-nas --datasource fedora-nas
harness run --catalog catalog.json --thresholds thresholds.json \
  --plan plans/virtbench-fio-live-smoke.yaml --backends backends.local.yaml \
  --backend tlvbm-nas --datasource fedora-nas --concurrency 1 \
  --workdir out/nas-fio/work --output out/nas-fio/report --no-attestations
```

A plan can set the same selection:

```yaml
overrides:
  TR-STOR-002:
    datasource: fedora-nas
  TR-VIRT-002:
    datasource: openshift-virtualization-os-images/rhel9-nas
```

This option affects Virtbench only. Other adapters retain their existing image
sources, and the CLI reports this boundary when they are selected. In particular,
kube-burner VM snapshots and kube-burner-ocp lifecycle workloads still use their
existing `vm_image` sources.

Live preflight validates the chosen source, checks the DataSource Ready
condition, and checks that its referenced PVC is Bound or its VolumeSnapshot is
readyToUse. It reports the source and target storage class. The override is
applied before `use_snapshot` source resolution. Source DataSources, PVCs and
snapshots remain read-only. Replay (`results_dir`) does not contact the cluster.
Snapshot cloning stages one shared source template. If namespace-local
DataSources resolve to different backing sources across VMs, preflight asks for
an explicit shared `sourceRef.namespace` or `--datasource namespace/name`.

Templates are staged in each execution's work directory. Only DataSource
`sourceRef` entries in DataVolume templates receive the override; blank disks,
VM names, cloud-init and storage-class placeholders are preserved. An explicit
selection fails if the template contains no DataSource reference. Custom
`vm_template` and `fio_vm_template` files retain their storage properties.
Harness defaults and staged upstream defaults omit `volumeMode`, allowing CDI
to use StorageProfile defaults; required access modes, including RWX, remain.

Upstream default templates are located through `VIRTBENCH_REPO`, the packaged
`/opt/virtbench-runtime`, or the current directory. A missing upstream default
requires setting `VIRTBENCH_REPO` or supplying `vm_template`. FIO and the
harness-owned two-disk, drain and failure-recovery templates are embedded.

## TLVBM NAS smoke procedure

Set `KUBECONFIG` to the NAS test cluster's kubeconfig and define two
credential-free backend entries:

```yaml
schema_version: "1"
backends:
  - name: tlvbm-nas
    storage_class: rhos-ontap-nas-rwx
    snapshot_class: csi-trident-volumesnapshotclass
  - name: tlvbm-nas-economy
    storage_class: rhos-ontap-nas-economy
    snapshot_class: csi-trident-volumesnapshotclass
```

Check tool availability, both StorageProfiles, the Ready DataSources and backing
snapshots, and the snapshot class driver before workloads. Run one test at a time
with concurrency 1, separate work/output roots, and fresh namespaces. Complete
cleanup before advancing. Use `fedora-nas` for FIO and `rhel9-nas` for cloning.
Start regular NAS before Economy. Each class must pass the basic one-VM FIO
smoke (30 seconds, two filesystem disks, existing FIO smoke settings) before
advancing to three PVCs (TR-STOR-006), two clones (TR-VIRT-002), two lifecycle VMs
(TR-VIRT-019, migration disabled), and one VM with three snapshots (TR-VIRT-010).
Exclude drain and failure injection. Economy restores from a regular NAS source
are compatibility probes: capture CDI events on failure and stop further
source-dependent Economy runs.

Separate functional completion, SLA verdicts, unsupported capabilities and
infrastructure errors. These reduced-scale runs provide smoke evidence only;
they are not certification results. Run-specific commands, plans, logs and
cleanup evidence are retained under `out/nas-smoke/` for this worktree.
