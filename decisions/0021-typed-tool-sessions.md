# 0021. Own tool job state in typed sessions

- **Status:** Proposed
- **Date:** 2026-10-22

## Context

The stage interfaces pass a `Bag` containing string keys and arbitrary values.
KB and KBO depend on resolved parameters, result directories, and execution
records written by earlier stages. Runtime assertions and shared slice pointers
hide those dependencies. The orchestrator must remain independent of concrete
tool types.

## Decision

`ToolIntegration.NewSession` creates a fresh `stages.Session` for each pipeline
invocation. Its preflight, provision, run, collect, parse, and teardown methods
operate on typed adapter-owned state. The orchestrator controls stage ordering,
scheduling, reporting, and grading; sessions carry no separate scheduler. The
session interface replaces the individual Bag-based stage interfaces without
a compatibility wrapper.

A session may own several TR execution records in an ordinary tool batch.
Named variants receive separate pipeline invocations and sessions. Each record
retains its resolved parameters, validation result, resources, artifact paths,
and execution status. Registry entries contain factories and immutable tool
metadata rather than mutable job state. Shared preflight returns typed resolved
parameters and findings, with optional callbacks bound to the owning session.

Stage failures stop the pipeline, and teardown still runs with whatever state
was recorded before the failure. Cleanup must tolerate cancellation and partial
setup. Standalone preflight uses a fresh session and invokes only preflight,
which must remain free of resource creation. Existing artifact attribution,
grading, batching, and scheduling behavior is preserved.

All sessions complete preflight before shared run/group setup creates resources.
The same session continues through execution, and failed preflight excludes its
TRs from shared setup. Session and shared teardown errors retain the original
test verdicts and add error verdicts and certification-blocking incidents to the
report and checkpoints.

This refines ADR-0003 and the state contract described in ADR-0020.

## Decision notes

### Options considered

- Typed per-tool sessions: chosen to keep heterogeneous tool state behind a
  common lifecycle interface while retaining one orchestrator.
- Typed outputs between every stage: would also require a mechanism to hide
  each adapter's stage-specific types from the heterogeneous registry.
- Bag accessors or a compatibility wrapper: rejected because they preserve the
  implicit state contract or duplicate lifecycle APIs.

### Consequences

KB and KBO carry their state in one typed execution collection per session.
Adapters and callers implement one lifecycle contract. The reference adapter
and lifecycle tests demonstrate fresh state per job and cleanup after failure.
