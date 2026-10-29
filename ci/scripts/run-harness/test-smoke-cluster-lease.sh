#!/usr/bin/env bash
# Exercise cluster smoke lease acquire, renew, busy conflict, and trap release.
set -euo pipefail

_run_harness_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=../ci-utils.sh
source "${_run_harness_dir}/../ci-utils.sh"
# shellcheck source=smoke-cluster-lease.sh
source "${_run_harness_dir}/smoke-cluster-lease.sh"

kubeconfig="${KUBECONFIG:-${REPO_ROOT}/work/kubeconfig}"
[[ -s "${kubeconfig}" && -r "${kubeconfig}" ]] || die "kubeconfig missing or unreadable: ${kubeconfig}"
export KUBECONFIG="$(realpath "${kubeconfig}")"
export SMOKE_CLUSTER_LEASE_KUBECONFIG="${KUBECONFIG}"

command -v oc >/dev/null 2>&1 || die "oc is required"
command -v jq >/dev/null 2>&1 || die "jq is required for lease self-test"

oc_args=(--kubeconfig "${KUBECONFIG}")
if [[ "${KUBECTL_INSECURE_SKIP_TLS_VERIFY:-false}" == "true" ]]; then
	oc_args+=(--insecure-skip-tls-verify=true)
fi

test_run_id="lease-selftest-$$-$(date +%s)"
export HARNESS_SMOKE_LEASE_RUN_ID="${test_run_id}"
export SMOKE_CLUSTER_LEASE_NAME="storage-cert-harness-smoke-selftest"
export SMOKE_CLUSTER_LEASE_DURATION_SECONDS=120
export SMOKE_CLUSTER_LEASE_RENEW_INTERVAL_SECONDS=2

lease_get() {
	local name="$1" jsonpath="$2"
	oc "${oc_args[@]}" get lease "${name}" -n "${SMOKE_CLUSTER_LEASE_NAMESPACE}" -o "jsonpath=${jsonpath}"
}

cleanup() {
	smoke_cluster_lease_stop_renewal_loop 2>/dev/null || true
	smoke_cluster_lease_release 2>/dev/null || true
}
trap cleanup EXIT

echo "==> smoke lease self-test: holder=${test_run_id} lease=${SMOKE_CLUSTER_LEASE_NAME}"

oc "${oc_args[@]}" whoami --show-server >/dev/null || die "cluster not reachable"

smoke_cluster_lease_acquire "harness-smoke" "plans/harness-smoke-plan.yaml"
[[ "${SMOKE_CLUSTER_LEASE_HELD:-0}" == "1" ]] || die "expected SMOKE_CLUSTER_LEASE_HELD=1 after acquire"

holder="$(smoke_cluster_lease_holder)"
[[ "${holder}" == "${test_run_id}" ]] || die "holder mismatch after acquire: got '${holder}'"

echo "==> renew heartbeat (manual)"
before="$(lease_get "${SMOKE_CLUSTER_LEASE_NAME}" '{.spec.renewTime}')"
sleep 1
smoke_cluster_lease_renew_heartbeat
after="$(lease_get "${SMOKE_CLUSTER_LEASE_NAME}" '{.spec.renewTime}')"
[[ "${after}" != "${before}" ]] || die "renewTime did not change after manual renew"

echo "==> background renewal loop"
before_loop="$(lease_get "${SMOKE_CLUSTER_LEASE_NAME}" '{.spec.renewTime}')"
smoke_cluster_lease_start_renewal_loop
sleep $((SMOKE_CLUSTER_LEASE_RENEW_INTERVAL_SECONDS + 1))
mid_loop="$(lease_get "${SMOKE_CLUSTER_LEASE_NAME}" '{.spec.renewTime}')"
[[ "${mid_loop}" != "${before_loop}" ]] || die "renewTime did not change after first renewal interval"
sleep $((SMOKE_CLUSTER_LEASE_RENEW_INTERVAL_SECONDS + 1))
after_loop="$(lease_get "${SMOKE_CLUSTER_LEASE_NAME}" '{.spec.renewTime}')"
[[ "${after_loop}" != "${mid_loop}" ]] || die "renewTime did not advance again during background renewal loop"
smoke_cluster_lease_stop_renewal_loop

echo "==> busy conflict (second holder must fail with exit ${SMOKE_CLUSTER_LEASE_EXIT_BUSY})"
busy_env=(
	"HARNESS_SMOKE_LEASE_RUN_ID=other-holder-$$"
	"SMOKE_CLUSTER_LEASE_HELD=0"
	"KUBECONFIG=${KUBECONFIG}"
	"SMOKE_CLUSTER_LEASE_KUBECONFIG=${KUBECONFIG}"
	"SMOKE_CLUSTER_LEASE_NAME=${SMOKE_CLUSTER_LEASE_NAME}"
	"KUBECTL_INSECURE_SKIP_TLS_VERIFY=${KUBECTL_INSECURE_SKIP_TLS_VERIFY:-false}"
)
if env "${busy_env[@]}" bash -c "
	set -euo pipefail
	source '${_run_harness_dir}/../ci-utils.sh'
	source '${_run_harness_dir}/smoke-cluster-lease.sh'
	smoke_cluster_lease_acquire harness-smoke plans/harness-smoke-plan.yaml
" 2>/dev/null; then
	die "expected second acquire to fail"
else
	status=$?
	[[ "${status}" == "${SMOKE_CLUSTER_LEASE_EXIT_BUSY}" ]] || die "expected exit ${SMOKE_CLUSTER_LEASE_EXIT_BUSY}, got ${status}"
fi
echo "==> busy conflict ok (exit ${status})"

echo "==> trap release (subshell)"
(
	export HARNESS_SMOKE_LEASE_RUN_ID="${test_run_id}"
	export SMOKE_CLUSTER_LEASE_NAME="storage-cert-harness-smoke-selftest-trap"
	export SMOKE_CLUSTER_LEASE_DURATION_SECONDS=120
	smoke_cluster_lease_acquire "harness-smoke" "plans/harness-smoke-plan.yaml"
	smoke_cluster_lease_install_traps
	exit 0
)
set +e
trap_get_err="$(oc "${oc_args[@]}" get lease storage-cert-harness-smoke-selftest-trap \
	-n "${SMOKE_CLUSTER_LEASE_NAMESPACE}" 2>&1)"
trap_get_st=$?
set -e
if [[ "${trap_get_st}" -eq 0 ]]; then
	die "trap subshell should have deleted storage-cert-harness-smoke-selftest-trap"
fi
if ! grep -qiE 'notfound|\(NotFound\)|not found' <<<"${trap_get_err}"; then
	die "trap release check: expected NotFound, got exit ${trap_get_st}: ${trap_get_err}"
fi
echo "==> trap release ok"

echo "smoke lease self-test: ok"
