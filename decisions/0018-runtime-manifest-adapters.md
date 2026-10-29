# 0018. Runtime manifest adapters

- **Status:** Proposed
- **Date:** 2026-10-29

## Context

Virtbench inputs were prepared through string replacement, inserted YAML
fragments, nested map mutation, and upstream placeholder substitution. This
spread value ownership across the harness and released tools. The source layout
is now `internal/tools/<tool>/templates/<TR-ID>/`, with shared resources under
`templates/shared/`.

## Decision

Use a manifest adapter at tool execution. Kube-burner and kube-burner-ocp retain
native rendering through a no-op adapter. Virtbench receives fully rendered VM
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

The renderer has one strict contract and a small explicit helper set rather
than importing all of Sprig. New dynamic values require an explicit resolver
entry. Tests render every owned VB template and exercise the execution boundary;
KB's snapshot-job list is rendered by its native template from explicit JSON
values. Raw Go-template YAML is excluded from yamllint. See
[adapter authoring](../docs/adapter-authoring.md#runtime-manifest-rendering).
