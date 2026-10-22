# 0020. Declare preflight checks by workload

- **Status:** Proposed
- **Date:** 2026-10-22

State ownership and the lifecycle interface are refined by [ADR-0021](0021-typed-tool-sessions.md).

## Context

KB chooses most prerequisites inside its preflight function. KBO declares workload prerequisites but assembles and deduplicates them in adapter-specific code. A TR's effective parameters can change which checks apply. KB also resolves the first TR's parameters into singleton state and rejects multi-TR jobs. ADR-0018 shared individual probes but deferred a common check runner.

## Decision

Workload definitions beside each adapter declare preflight checks for resolved TR executions. KB's scenario entries declare capabilities and snapshot applicability, including the reduced-mode skip reason; its preflight driver does not branch on TR IDs. KBO's workload entries declare their required binaries and cluster capabilities. Both use shared binary and capability request construction. `internal/tools/common` runs the resulting checks in declaration order. A check may declare an applicability rule that emits a skip finding without running the probe. A check may also declare a reuse key; matching keys run once within one preflight call. Checks without a key run each time.

`internal/tools/common/checks.go` contains the check implementations consumed by all adapters. Each adapter's `workloads.go` declares `common.WorkloadRequirements` for an execution. KB and KBO register the shared `common.Preflight` stage, which calls `common.ResolveParams`, builds tool identity, binary, backend storage-class, and declared prerequisite checks, and executes them. Adapter-wide requirements and per-TR workload requirements use the same check construction. The optional `Checks` hook supplies additional checks and state-recording callbacks; KB uses it for snapshot selection and CDI import mode, while KBO needs only its declarations. Their individual `preflight.go` files are removed. Parameter decoding and its typed validation remain with the adapter. Cluster probes remain in `internal/clustercheck`.

One snapshot-support check resolves the compatible class and records the selection. Its request explicitly declares whether VM client support must also be checked, independently of the snapshot selection mode. KB requests that client prerequisite to preserve its behavior; direct snapshot consumers can omit it. VirtBench migration remains separate.

Shared source checks resolve a CDI DataSource, verify a source PVC's storage-class driver against the selected snapshot class, and compare an existing VolumeSnapshot's class with the selected class. PVC and target storage-class names may differ. These checks read cluster objects without staging resources or changing VM templates. DataSource results are delivered through an uncached callback; template inspection and aggregate source requirements remain adapter responsibilities.

Reuse keys must include every input that changes the probe result. Cluster reads are scoped to a single preflight call, whose executions use the same cluster context. Tool-specific state updates remain uncached and are applied to each job's typed session (ADR-0021). The core continues to depend only on stage interfaces and never imports an adapter or the shared support package.

Parameter resolution produces one typed entry per selected TR, in selection order, retaining the TR identity alongside its parameters and validation result. The shared driver stores the collection once; check callbacks update the matching entry. KB retains per-TR snapshot selection, private storage class, command result, and artifact state, and executes every selected TR sequentially within its existing tool job. Collection and parsing attribute results and files to that same TR. Namespace collisions within a KB job receive deterministic execution-specific suffixes so its workloads do not share resources or cleanup.

An error finding still prevents the entire tool job from proceeding. Independent checks continue so the standalone preflight command reports all findings. The KB one-TR restriction is intentionally removed; the orchestrator's grouping and scheduling are unchanged. KBO consumes the same per-TR parameter collection and retains its existing workload prerequisites and duplicate-workload skip behavior. VirtBench's existing scenario and preflight declarations are co-located in its `workloads.go` as an initial structural step; it will adopt the common check execution and parameter model separately.

## Decision notes

### Options considered

- Workload declarations with a shared runner: chosen so requirements stay near the workload and repeated pure probes are explicit.
- A central TR-to-check table: rejected because each adapter owns its workload semantics.
- A persistent cross-job cache: deferred until cluster identity and invalidation are represented explicitly.

### Consequences

KB and KBO use one check execution mechanism without changing the stage interface. Reuse is limited to a preflight call; checks in separate tool jobs may still repeat. Each new workload declares the checks it needs. This refines ADR-0018.
