# 0019. Separate catalog tool identity from scenario selection

- **Status:** Proposed
- **Date:** 2026-10-22

## Context

The KB `automation_tool` field mixed executable names, workloads, command flags, and scenario labels. For example, `virtbench datasource-clone --num-disks 2` identified both a tool and the TR-VIRT-018 scenario. Exact registry lookup worked for VirtBench, while KubeBurner OCP relied on a TR ID fallback. Grading also compared these mixed strings with `measured_by`.

## Decision

The shipped catalog uses a closed `automation_tool` set: `example`, `manual`, `virtbench`, `kube-burner`, and `kube-burner-ocp`. A separate optional `scenario` selects a VirtBench integration. KubeBurner OCP's workload remains a TR parameter. The TR-VIRT-018 two-disk default is explicit in `params.num_disks`; its adapter still validates that the VM template has the same disk count.

The orchestrator resolves a scenario-specific registry key when `scenario` is set and groups jobs by the resolved integration name. Catalog `measured_by` values name the normalized tool. A unit test in the CI `unittest` job reads every shipped catalog file, checks the enum, and confirms each TR maps to the declared integration. Catalog loading itself remains permissive for temporary or external integrations.

This refines ADR-0007's exact registry key contract and ADR-0008's scenario table. The core still does not import concrete adapters.

## Decision notes

### Options considered

- Separate tool and scenario fields: chosen to preserve distinct VirtBench scenarios and stop encoding workloads in the tool name.
- Keep free-text `automation_tool` with TR ID fallback: leaves tool identity ambiguous and cannot be checked as an enum.

### Consequences

Adding a shipped integration requires updating the enum and the CI catalog check. New KB exports must carry the normalized tool and scenario fields. Legacy external catalogs remain readable, but the shipped catalog follows the new contract.
