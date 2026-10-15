# 0009. Scoped setup: run / group / individual

- **Status:** Accepted
- **Date:** 2026-08-24

## Context

Separate virtbench scenario integrations share an SSH helper pod. Per-job
provision/teardown would repeatedly recreate it and cannot own shared cleanup.

## Decision

- Add registered `stages.SetupProvider`s with `SetupInfo{Scope, Key}`,
  `Setup(ctx, rc, trs)`, and `Teardown(ctx, rc, trs)`. Deduplicate providers by
  `(scope, key)`.
- **Run scope** receives all runnable TRs. **Group scope** receives runnable TRs
  whose tools list its key in `ToolIntegration.SetupGroups`; activate it only
  when at least one such TR exists.
- After selection, set up active providers once in **run → group** order, execute
  tool jobs, then tear down successful providers once in reverse **group → run**
  order. Providers obtain configuration from their in-scope TRs/run context.
- Failed setup emits error verdicts for affected TRs and prevents their jobs
  from running. Do not call teardown on a provider whose setup failed.
- **Individual scope** remains `Provisioner`, once per tool job with that job's
  TRs; it needs no new mechanism.

## Decision notes

### Options considered

- **Scoped providers (chosen):** explicit shared ownership and cleanup timing;
  require a small core lifecycle addition.
- **Idempotent per-job creation:** still recreates resources between jobs.
- **Collapsed scenarios:** breaks the exact registry-key contract.

### Consequences

virtbench's `virtbench` group provider creates the `ssh-test-pod` only if absent
and deletes it on successful group teardown, including pods it reused from prior
runs. That delete can disrupt another concurrent harness run sharing the same
cluster helper name. Failed setup rolls back a pod this setup created (the
orchestrator does not call teardown after setup failure). Replay-only batches
skip shared cluster setup and teardown; mixed replay/live batches still run
live setup/teardown for the cluster-using TRs.
Expensive shared images, RBAC, or operators should use providers instead of
per-job work. virtbench's own `--cleanup` handles its workload namespaces.

A first-class kubeconfig field and in-cluster execution remain outside scope.

Related: [ADR-0003](0003-harness-architecture-ports-and-adapters.md),
[ADR-0007](0007-virtbench-local-exec-and-replay.md),
[ECOPROJECT-5331](https://redhat.atlassian.net/browse/ECOPROJECT-5331).
