#!/usr/bin/env bash
set -euo pipefail

LIVE_SMOKE_EXIT_NOT_IMPLEMENTED="${LIVE_SMOKE_EXIT_NOT_IMPLEMENTED:-5}"

_run_harness_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=../ci-utils.sh
source "${_run_harness_dir}/../ci-utils.sh"
# shellcheck source=smoke-cluster-lease.sh
source "${_run_harness_dir}/smoke-cluster-lease.sh"
ci_log_init "live-cluster-smoke"

: "${LIVE_SMOKE_SRC_DIR:?LIVE_SMOKE_SRC_DIR must point at the extracted PR artifact}"
: "${LIVE_SMOKE_HEAD_SHA:?LIVE_SMOKE_HEAD_SHA is required}"
: "${LIVE_SMOKE_PR_NUMBER:?LIVE_SMOKE_PR_NUMBER is required}"

src_root="$(cd "${LIVE_SMOKE_SRC_DIR}" && pwd)"
[[ -f "${src_root}/plans/harness-smoke-plan.yaml" ]] || die "PR source missing plans/harness-smoke-plan.yaml"

echo "live-cluster-smoke: pr=${LIVE_SMOKE_PR_NUMBER} head_sha=${LIVE_SMOKE_HEAD_SHA}"
echo "trusted_runner_root=${REPO_ROOT} pr_source=${src_root}"

kubeconfig="${LIVE_SMOKE_KUBECONFIG:-${KUBECONFIG:-${REPO_ROOT}/work/kubeconfig}}"
if [[ ! -s "${kubeconfig}" || ! -r "${kubeconfig}" ]]; then
	echo "error: kubeconfig missing for live-cluster smoke: ${kubeconfig}" >&2
	exit "${LIVE_SMOKE_EXIT_NOT_IMPLEMENTED}"
fi

export KUBECONFIG="${kubeconfig}"
export SMOKE_CLUSTER_LEASE_KUBECONFIG="${kubeconfig}"
plan_file="${src_root}/plans/harness-smoke-plan.yaml"
smoke_cluster_lease_acquire "harness-smoke" "${plan_file}"
smoke_cluster_lease_install_traps "_ci_log_flush"
smoke_cluster_lease_start_renewal_loop

echo "error: live-cluster harness execution is not implemented yet (ECOPROJECT-5488)" >&2
echo "error: secrets materialization, image load, and podman run from pr-src are pending" >&2
exit "${LIVE_SMOKE_EXIT_NOT_IMPLEMENTED}"
