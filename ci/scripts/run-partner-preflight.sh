#!/usr/bin/env bash
# Host-side cluster/backends checks, then harness preflight in the published Quay image.
set -euo pipefail

usage() {
	cat <<'EOF'
Usage:
  ./ci/scripts/run-partner-preflight.sh [KUBECONFIG]

Environment:
  KUBECONFIG          Path to kubeconfig (default: value of $KUBECONFIG or ./kubeconfig)
  BACKEND             Backend name in backends.yaml (required)
  BACKENDS            Path to backends file (default: backends.yaml)
  HARNESS_IMAGE       Container image (default: quay.io/virtarraycert/storage-cert-harness:main)
  PLAN                Plan for tool selection (default: plans/level3-customer-no-load-80.yaml)
  PARTNER_KEEP_WORK   When true, do not archive prior logs (default: false)

Runs host validation (API reachability, node Ready, StorageClass / VolumeSnapshotClass
from backends.yaml), then `harness preflight` inside the published image.
EOF
}

if [[ "${1:-}" == "-h" || "${1:-}" == "--help" ]]; then
	usage
	exit 0
fi
if [[ "$#" -gt 1 ]]; then
	usage >&2
	exit 2
fi

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${repo_root}"
# shellcheck source=ci-utils.sh
source "${repo_root}/ci/scripts/ci-utils.sh"
# shellcheck source=partner-harness-common.sh
source "${repo_root}/ci/scripts/partner-harness-common.sh"

archive_partner_log "run-partner-preflight"
ci_log_init "run-partner-preflight"

kubeconfig="${1:-${KUBECONFIG:-kubeconfig}}"
backends_file="${BACKENDS:-backends.yaml}"
backend="${BACKEND:-}"
plan="${PLAN:-${PARTNER_PLAN_NO_LOAD_80:-plans/level3-customer-no-load-80.yaml}}"
image="$(partner_default_image)"

[[ -n "${backend}" ]] || {
	echo "error: set BACKEND to the name of your entry in ${backends_file}" >&2
	exit 1
}

validate_partner_host_tools
validate_partner_kubeconfig "${kubeconfig}"
validate_partner_backends_file "${backends_file}"
thresholds_file="${repo_root}/${PARTNER_THRESHOLDS_FILE}"
plan_path="${plan}"
if [[ "${plan_path}" != /* ]]; then
	plan_path="${repo_root}/${plan_path}"
fi
validate_partner_cert_inputs "${repo_root}/catalog.json" "${thresholds_file}" "${plan_path}"
validate_partner_backend_entry "${backends_file}" "${backend}"
validate_partner_cluster_connectivity "${kubeconfig}" "${plan_path}"
if [[ "$(basename "${plan_path}")" == "level3-customer-no-load-80.yaml" ]]; then
	validate_partner_level3_no_load_plan "${plan_path}"
fi
partner_pull_image "${image}"

echo "==> harness preflight (container)"
partner_run_harness_container "${image}" "${kubeconfig}" "${backends_file}" "${backend}" preflight \
	--plan "/plans/$(basename "${plan}")" \
	--verbose
