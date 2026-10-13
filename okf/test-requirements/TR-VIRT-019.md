---
type: test-requirement
title: "TR-VIRT-019: Parallel VM Lifecycle at Progressive Scale"
description: >-
  Validate storage backend stability and latency under progressive-scale parallel
  VM lifecycle operations — create, resize, restart, snapshot, migrate, delete —
  with increasing VM counts.
tags: [openshift-virt, storage, vm, lifecycle, parallel, progressive, kube-burner, partner-level-3]
resource: TR-VIRT-019
timestamp: 2026-09-03T00:00:00Z
---

# TR-VIRT-019: Parallel VM Lifecycle at Progressive Scale

## Summary

Progressive-scale parallel VM lifecycle via the upstream
[virt-parallel](../workloads/virt-parallel.md) workload. Each iteration creates
N VMs with DataVolumes, resizes volumes, restarts VMs, snapshots, live-migrates every VM in the current iteration, and deletes all — then increments N by the configured step. Runs
until failure or `max-iterations` reached.

| Field | Value |
|-------|-------|
| Partner level | 3 |
| Verified | unverified |
| Automation tool | [kube-burner-ocp](../adapters/kube-burner-ocp.md) |
| SLA status | to-be-validated (`sla: []` in KB) |
| Checks status | to-be-validated |

## Target SLA (draft)

Key metrics under evaluation (not yet gated in harness):

- Per-operation latency at each scale tier (create, resize, restart, snapshot, migrate)
- Maximum sustainable VM count before failure
- Storage backend throughput under concurrent resize + snapshot + migration I/O

Acceptable latency limits are **to-be-validated**.

## Risk & failure modes

- Cascading failures when parallel VM operations overwhelm virt-controller or CDI
- Storage backend collapse under concurrent resize + snapshot + migration I/O
- Migration QPS limits exceeded causing queue backup and timeouts
- Resource fragmentation preventing VM placement at higher scale tiers
- Non-linear latency degradation (fast at 5 VMs, fails at 50)

## Harness integration

- Catalog entry: `automation_tool: kube-burner-ocp`, `sla_count: 0`
- Plan param `workload: virt-parallel` selects the subcommand
- Native grading: all kube-burner jobs `passed` ([jobs_passed](../output-schema/harness-parsed-metrics.md))
- Example plans: [tr-virt-019-parallel-vm-lifecycle](../plans/tr-virt-019-parallel-vm-lifecycle.md) (progressive),
  [smoke](../plans/tr-virt-019-parallel-vm-lifecycle-smoke.md) (2 VMs, 1 iter)

## Cluster prerequisites

- OpenShift Virtualization (KubeVirt CRD)
- CDI (DataVolume CRD)
- VolumeSnapshotClass on the backend storage class (required for snapshot phase)
- ≥ 3 schedulable worker nodes (required for migration phase)

## Related

- Automation: [kube-burner-ocp](../adapters/kube-burner-ocp.md) → [virt-parallel](../workloads/virt-parallel.md)
- Output: [latency quantiles](../output-schema/latency-quantiles.md), [jobSummary](../output-schema/jobSummary.md)
- Pipeline: [stage flow](../pipeline/kube-burner-ocp-stages.md)
- Related TRs: TR-VIRT-003 (sequential migration), TR-VIRT-010 (snapshot at scale), TR-VIRT-016 (resource limits)
