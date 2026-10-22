# 0018. Share tool preflight checks beside adapters

- **Status:** Proposed
- **Date:** 2026-10-22

## Context

KubeBurner and KubeBurner OCP repeat parameter validation, binary checks, and cluster capability checks. Their preflight behavior diverged: KubeBurner required a storage class, while KubeBurner OCP reported it only when present even though every current workload needs one at execution time. The core must not import a concrete tool (ADR-0003).

## Decision

Shared preflight operations live in `internal/tools/common` and `internal/clustercheck`, beside the adapters. Parameter validation processes every selected test requirement and returns its resolved parameters with a finding. Binary and cluster capability checks run each named requirement once. Both tools require a configured storage class and check that it exists before provisioning.

Each adapter declares its required binaries and capabilities. KubeBurner retains its snapshot, reduced-mode, and CDI populator checks because those checks produce tool-specific run state. Preflight continues independent checks after a finding with level `error`; the orchestrator decides whether the job may proceed.

KubeBurner OCP gathers binaries and capabilities in one pass over its workload specs. It requires an available Kubernetes CLI for cluster checks and rejects TRs whose nonempty tool identity does not match the adapter.

This refines the adapter structure in ADR-0003. The stage API and its `Bag` remain subject to the separate typed-session refactor.

## Decision notes

### Options considered

- Shared operations with adapter-owned requirements: chosen to centralize behavior while keeping workload rules near their tool.
- One shared `Check` implementation with callbacks for every difference: deferred until the state passed between stages has a typed contract.
- Duplicate checks in each adapter: retained inconsistent preflight behavior.

### Consequences

KubeBurner OCP now fails preflight when its backend has no storage class or the configured class does not exist. Successful parameter findings use the same form in both tools. The redundant KubeBurner OCP params write to `Bag` is removed. KubeBurner preflight rejects jobs containing anything other than one TR, matching its runner. Typed per-TR run state remains deferred.
