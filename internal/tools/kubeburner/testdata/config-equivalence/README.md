# KB configuration equivalence baseline

These inputs and outputs were captured from the unchanged production implementation
at commit `56a3c45`, before changing the JSON snapshot-job transport.

| Case | VM replicas | Snapshot count | Expected snapshot batches |
| --- | --- | --- | --- |
| legacy | 5 | omitted | 5, using the legacy job and object template |
| explicit-equal | 5 | 5 | 5, using batch 1 |
| partial | 10 | 25 | 10, 10, 5 |
| fewer-than-vms | 5 | 3 | 3 |
| exact | 10 | 20 | 10, 10 |
| zero | 1 | 0 | No snapshot jobs; valid for TR-VIRT-027 |
| large | 1 | 1250 | 1250 batches of 1 |
| overrides | 3 | 7 | 3, 3, 1, with custom namespace, image, disk size, QPS, burst and timeout |

`cases.json` supplies plan parameters and independently specified expected batch
sizes. The golden JSON files contain the complete rendered configurations after
YAML decoding and canonical JSON encoding. Comparisons cover all fields, job
order, types, and values; YAML whitespace and comments are intentionally ignored.
The large case freezes a SHA-256 digest of the complete canonical output instead
of checking in a very large configuration. Its job names, batch numbers and sizes
are also checked explicitly.

The production configuration now generates batches using scalar inputs and the
native Go/Sprig template loop. The test renderer uses `text/template` with
`sprig.GenericFuncMap`, as kube-burner v2.8.5 does. `TestConfigMatchesBaseline` checks its output
against the unchanged golden files captured before this implementation.

```sh
go test ./internal/tools/kubeburner -run TestConfigMatchesBaseline -v
```

This is an offline rendering comparison. It does not run kube-burner against a
cluster or verify Kubernetes admission. The previous implementation could render the large case in-process, but carrying
its JSON through the process environment exceeded Linux's per-entry execution
limit on a 4 KiB page system. A separate regression test starts a real subprocess
with the scalar environment for the same 1250-snapshot input.

The expected outputs are frozen. Do not regenerate them from a changed
implementation when assessing equivalence.
