# PVC density: workload and KB contract

TR-STOR-006 uses the `kube-burner-ocp pvc-density` adapter introduced in
[MR !4](https://gitlab.cee.redhat.com/eco-special-projects/storage-cert-harness/-/merge_requests/4).
That MR predated the KB's numeric latency bar and named functional check.

Verified against the released kube-burner-ocp v1.12.3 workload
([configuration](https://github.com/kube-burner/kube-burner-ocp/blob/v1.12.3/cmd/config/pvc-density/pvc-density.yml)):
each iteration creates one ReadWriteOnce PVC and one pod referencing it, in
`pvc-density`. The storage class comes from the harness backend; claim size and
iterations come from resolved catalog defaults and plan overrides. The workload
waits for readiness and verifies the created objects. PVC and pod latency
measurements are enabled. Creation uses API rate limits; the iteration count is
not a promise that every API request starts simultaneously.

The parser preserves upstream quantile names and additionally maps the Bound
P99 in `pvcLatencyQuantilesMeasurement-pvc-density.json` to the catalog's
`pvc_bind_latency@p99`. The source unit is milliseconds; the core grader converts
units when comparing with the KB's seconds-valued bar. Pod Ready latency is a
separate measurement and is not used as PVC bind latency.

The catalog check `pvc-1000-bound` maps to the PVC-density job summary. A pass
requires a successful job with no execution errors, positive iterations,
`waitWhenFinished`, `verifyObjects`, and `errorOnVerify`. The parser also
requires the recorded iteration count to equal the resolved plan count. This
uses the released workload's object verification/readiness result; it does not
infer the number of successful binds from the p99 alone.

Grading uses the existing `NativeOrGraded` evaluator, just like the other
workloads. It selects applicable KB bars and performs unit conversion and
pass/fail comparisons. There is no PVC-specific grading or verdict formatting.

A latency bar whose `when.iterations` does not match a reduced-scale plan remains
report-only. To exercise grading at reduced scale, use private copies of the KB
inputs with explicitly documented applicability and check-expectation adjustments.
The standard grader displays the catalog check expectation unchanged, so a smoke
catalog should describe the configured scale rather than claim the full scale.
Record these changes with the run plan and artifacts. Never track the private
inputs or reports, and never treat a smoke as full-scale certification.

For live verification, run `bin/harness run` with a plan, catalog, thresholds and
backend configuration. Save `oc get pvc,pod -n pvc-density` before cleanup when
retaining objects with `gc: false`. This namespace is shared by the upstream
workload: check that it is absent before starting, and remove only the resources
owned by your run afterward.
