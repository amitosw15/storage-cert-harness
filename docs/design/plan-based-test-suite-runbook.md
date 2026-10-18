# Run a Test Suite from a Plan

This is a development smoke run for a lab OpenShift cluster. It is not a
certification-scale execution. Use **private** runbooks for site-specific hostnames,
kubeconfig paths, and credentials.

## Test selection

Use these four IDs in `plans/harness-smoke-plan.yaml` to exercise every
runtime tool shipped in the image:

| Test | Workload | Tool |
| --- | --- | --- |
| `TR-VIRT-001` | Boot Storm | `virtbench` |
| `TR-STOR-006` | PVC provisioning density | `kube-burner-ocp` |
| `TR-VIRT-010` | VM snapshot creation | `kube-burner` |
| `TR-VIRT-019` | Parallel VM lifecycle | `kube-burner-ocp` |

Keep the plan's `backend` and node overrides aligned with your cluster. The plan
sets `backend: oc-edge-112`; `work/backends.local.yaml` must define a backend
with that **exact** name (or pass `BACKEND=<name>` when running).

For node-scoped TRs, set `node_name` / `target_node` in plan overrides to worker
nodes that exist in **your** cluster (for example `<worker-0>.<lab-domain>`).

## Run the plan

From `storage-cert-harness`, build the harness binary and local image, then run
smoke against your kubeconfig:

```sh
export KUBECONFIG=/path/to/your/kubeconfig
export KUBECTL_INSECURE_SKIP_TLS_VERIFY=true   # only if your lab requires it

make build
make image-build-local
BACKEND=oc-edge-112 make run-harness-smoke
```

Equivalent manual `podman run` (same plan and backend as CI):

```sh
podman run --rm --network host --userns=keep-id \
  --user "$(id -u):$(id -g)" \
  -v "$(pwd)/work/kubeconfig:/kubeconfig:ro,Z" \
  -e KUBECONFIG=/kubeconfig \
  -e KUBECTL_INSECURE_SKIP_TLS_VERIFY="${KUBECTL_INSECURE_SKIP_TLS_VERIFY:-false}" \
  -v "$(pwd)/work:/work:Z" \
  -v "$(pwd)/plans:/plans:ro,Z" \
  -v "$(pwd)/work/backends.local.yaml:/backends.yaml:ro,Z" \
  -w /work \
  "$(tr -d '[:space:]' < work/local-image-ref)" run \
  --plan /plans/harness-smoke-plan.yaml \
  --catalog /work/catalog.json \
  --thresholds /work/thresholds.json \
  --backends /backends.yaml \
  --backend oc-edge-112 \
  --workdir /work/harness-smoke-work \
  --output /work/harness-smoke-report \
  --no-attestations \
  --verbose
```

Reports and tool artifacts are written to `work/harness-smoke-report/`. The Make
target `BACKEND=oc-edge-112 KUBECONFIG=... make run-harness-smoke` performs the
same preflight checks and runs this plan.

## Validate the cluster first

```sh
export KUBECONFIG=/path/to/your/kubeconfig
export KUBECTL_INSECURE_SKIP_TLS_VERIFY=true   # if required by your lab
oc whoami --show-server
oc whoami
oc get nodes -l node-role.kubernetes.io/worker
oc get clusterversion
oc get storageclass <your-storage-class>
oc get volumesnapshotclass <your-snapshot-class>
```

Use `oc config view --minify` to inspect the active context and cluster
endpoint. Do not commit kubeconfig or backend credentials.
