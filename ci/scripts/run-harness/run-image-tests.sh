#!/usr/bin/env bash
# Run the harness smoke plan after validating the local cluster and backend.
set -euo pipefail

_run_harness_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=../ci-utils.sh
source "${_run_harness_dir}/../ci-utils.sh"
repo_root="${REPO_ROOT}"
acquire_smoke_run_lock
ci_log_init_temp "run-image-tests"

command -v podman >/dev/null 2>&1 || {
	echo "error: podman is required" >&2
	exit 1
}
echo "validation passed: podman is available"
KUBECONFIG="${1:-work/kubeconfig}"
[[ -s "${KUBECONFIG}" && -r "${KUBECONFIG}" ]] || {
	echo "error: kubeconfig is missing or unreadable: ${KUBECONFIG}" >&2
	exit 1
}
echo "validation passed: kubeconfig is readable (${KUBECONFIG})"
[[ -s work/local-image-ref ]] || {
	echo "error: work/local-image-ref is missing; run 'make image-build-local' first" >&2
	exit 1
}
echo "validation passed: local image reference is available"
[[ -s work/backends.local.yaml ]] || {
	echo "error: backend configuration is missing or empty: work/backends.local.yaml" >&2
	exit 1
}
echo "validation passed: backend configuration is available"

plan="plans/harness-smoke-plan.yaml"
[[ -s "${plan}" ]] || {
	echo "error: harness smoke plan is missing or empty: ${plan}" >&2
	exit 1
}
echo "validation passed: harness smoke plan is available (${plan})"

yq_bin="${YQ:-$(command -v yq || true)}"
[[ -n "${yq_bin}" && -x "${yq_bin}" ]] || {
	echo "error: yq is required to validate the harness smoke plan and backend" >&2
	exit 1
}
echo "validation passed: yq is available"

thresholds_file="thresholds-smoke.json"
[[ -s "${thresholds_file}" ]] || {
	echo "error: smoke thresholds bundle missing: ${thresholds_file}" >&2
	exit 1
}
harness_thresholds="work/harness-run-thresholds.json"
cp -f "${thresholds_file}" "${harness_thresholds}"
echo "validation passed: using ${thresholds_file} -> ${harness_thresholds}"

backend="${BACKEND:-$(${yq_bin} e -r '.backend // ""' "${plan}")}"
[[ -n "${backend}" ]] || {
	echo "error: no backend is specified by ${plan}; set BACKEND explicitly" >&2
	exit 1
}
echo "validation passed: plan backend is '${backend}'"
export YQ_BACKEND="${backend}"
backend_name="$(${yq_bin} e -r '.backends[] | select(.name == strenv(YQ_BACKEND)) | .name' work/backends.local.yaml)"
[[ "${backend_name}" == "${backend}" ]] || {
	echo "error: backend '${backend}' is not defined in work/backends.local.yaml" >&2
	exit 1
}
echo "validation passed: backend '${backend}' is defined"
storage_class="$(${yq_bin} e -r '.backends[] | select(.name == strenv(YQ_BACKEND)) | .storage_class // ""' work/backends.local.yaml)"
snapshot_class="$(${yq_bin} e -r '.backends[] | select(.name == strenv(YQ_BACKEND)) | .snapshot_class // ""' work/backends.local.yaml)"
[[ -n "${storage_class}" ]] || {
	echo "error: backend '${backend}' has no storage_class" >&2
	exit 1
}
echo "validation passed: backend storage class is '${storage_class}'"
[[ -n "${snapshot_class}" ]] || {
	echo "error: backend '${backend}' has no snapshot_class" >&2
	exit 1
}
echo "validation passed: backend snapshot class is '${snapshot_class}'"

command -v oc >/dev/null 2>&1 || {
	echo "error: oc is required for cluster preflight validation" >&2
	exit 1
}
echo "validation passed: oc is available"
oc_args=(--kubeconfig "${KUBECONFIG}")
if [[ "${KUBECTL_INSECURE_SKIP_TLS_VERIFY:-false}" == "true" ]]; then
	oc_args+=(--insecure-skip-tls-verify=true)
fi
oc "${oc_args[@]}" whoami --show-server >/dev/null || {
	echo "error: cluster is not reachable with ${KUBECONFIG}" >&2
	exit 1
}
echo "validation passed: cluster is reachable"

commit_harness_smoke_run_log "run-image-tests"

if ! nodes="$(oc "${oc_args[@]}" get nodes -o jsonpath='{range .items[*]}{.metadata.name}{"\t"}{range .status.conditions[?(@.type=="Ready")]}{.status}{end}{"\n"}{end}')"; then
	echo "error: unable to read cluster nodes" >&2
	exit 1
fi
[[ -n "${nodes}" ]] || {
	echo "error: cluster returned no nodes" >&2
	exit 1
}
while IFS=$'\t' read -r node ready; do
	[[ -n "${node}" && "${ready}" == "True" ]] || {
		echo "error: node '${node:-<unknown>}' is not Ready" >&2
		exit 1
	}
done <<< "${nodes}"
echo "validation passed: all cluster nodes are Ready"

oc "${oc_args[@]}" get storageclass "${storage_class}" >/dev/null || {
	echo "error: storage class '${storage_class}' does not exist" >&2
	exit 1
}
echo "validation passed: storage class '${storage_class}' exists"
oc "${oc_args[@]}" get volumesnapshotclass "${snapshot_class}" >/dev/null || {
	echo "error: snapshot class '${snapshot_class}' does not exist" >&2
	exit 1
}
echo "validation passed: snapshot class '${snapshot_class}' exists"

plan_nodes="$(${yq_bin} e -r '.. | select(tag == "!!map") | select(has("node_name") or has("target_node")) | (.node_name // .target_node) | select(. != null and . != "")' "${plan}")"
while IFS= read -r plan_node; do
	[[ -z "${plan_node}" ]] && continue
	oc "${oc_args[@]}" get node "${plan_node}" >/dev/null || {
		echo "error: plan node '${plan_node}' does not exist in the cluster" >&2
		exit 1
	}
done <<< "${plan_nodes}"
echo "validation passed: all plan-specified nodes exist"

if [[ "${HARNESS_SMOKE_KEEP_WORK:-false}" == "true" ]]; then
	echo "smoke hygiene: skipping work cleanup (HARNESS_SMOKE_KEEP_WORK=true)"
else
	echo "smoke hygiene: preflight complete; cleaning artifacts from previous smoke runs"
	clean_harness_smoke_artifacts
fi
mkdir -p work/harness-smoke-work work/harness-smoke-report
echo "validation passed: harness smoke work directories ready"

arch="${GOARCH:-$(uname -m)}"
case "${arch}" in
	x86_64) arch=amd64 ;;
	aarch64) arch=arm64 ;;
esac
harness_smoke_run_podman --rm --userns=keep-id --user "$(id -u):$(id -g)" --network=host \
	--platform "linux/${arch}" \
	-v "${repo_root}/work:/work:Z" \
	-v "${repo_root}/plans:/plans:ro,Z" \
	-v "$(realpath "${KUBECONFIG}"):/work/kubeconfig:ro,Z" \
	-w /work \
	-e KUBECONFIG=/work/kubeconfig \
	-e KUBECTL_INSECURE_SKIP_TLS_VERIFY="${KUBECTL_INSECURE_SKIP_TLS_VERIFY:-false}" \
	-- \
	--catalog /work/catalog.json \
	--thresholds /work/harness-run-thresholds.json \
	--plan /plans/harness-smoke-plan.yaml \
	--backends /work/backends.local.yaml \
	--backend "${backend}" \
	--workdir /work/harness-smoke-work \
	--output /work/harness-smoke-report \
	--no-attestations \
	--verbose
