# 0018. Runtime manifest adapters

- **Status:** Proposed
- **Date:** 2026-10-29

## Context

Virtbench inputs were prepared through string replacement, inserted YAML
fragments, nested map mutation, and upstream placeholder substitution. This
spread value ownership across the harness and released tools. The proposed source layout
is `internal/tools/<tool>/templates/<TR-ID>/`, with shared resources under
`templates/shared/`.

## Decision

Use a `ManifestRenderer` interface with `Render(source, values)`. Keep file IO,
cluster lookups, value resolution, and CLI construction in the tool integration. Kube-burner and kube-burner-ocp retain
native workload rendering; local KB workload templates pass through a no-op renderer and KBO
uses its released CLI's bundled templates. Virtbench receives fully rendered VM
manifests from the harness. Follow kube-burner's Go `text/template` Parse/Execute
model with strict missing keys and explicit values. Keep structural choices in
templates and resolve cluster-dependent values in the tool adapter.

Use the final CLI arguments and centralized defaults for VB VM and FIO values.
Pass resolved execution defaults to the CLI as well. Provisioning resources use
the same renderer with their own values. Sources remain immutable; rendered
files are private run artifacts. Backend credentials are not template values.

Reject unresolved placeholders and malformed rendered YAML before invoking the
consumer. VB VM templates contain one VM with an identity matching the CLI.
Legacy VB tokens are supported as aliases in the same engine. The renderer
validates YAML structure, not Kubernetes API schemas. Core orchestration keeps
depending on stage interfaces as required by ADR-0003.

## Decision notes

### Options considered

- Go templates: matches kube-burner's approach, supports typed values and
  conditional structure, and adds no runtime executable dependency.
- Delegate to each wrapped tool: sufficient for KB/KBO, but VB's token support
  varies by command and does not cover all harness provisioning resources.
- String replacement or resource-map mutation: leaves several competing paths
  for preparing manifests and obscures the resulting resource structure.

### Consequences

The renderer uses Go `text/template` and Sprig v3 with one added `toYaml` helper. New dynamic values require an explicit resolver
entry. Renderer tests cover typed values, control flow, legacy aliases, native passthrough,
and invalid input. Tool migrations will add coverage at their execution boundaries.

