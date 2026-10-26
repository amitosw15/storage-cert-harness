#!/usr/bin/env bash
# Run one Level-3 certification profile (load-80 or no-load-80) via the published Quay image.
set -euo pipefail

usage() {
	cat <<'EOF'
Usage:
  ./ci/scripts/run-partner-cert.sh <load-80|no-load-80> [KUBECONFIG]

Environment:
  KUBECONFIG          Path to kubeconfig (default: value of $KUBECONFIG or ./kubeconfig)
  BACKEND             Backend name in backends.yaml (required)
  BACKENDS            Path to backends file (default: backends.yaml)
  HARNESS_IMAGE       Container image (default: quay.io/virtarraycert/storage-cert-harness:main)
  CERT_RESULTS_ROOT   Base directory for results (default: certification-results)
  CONTINUE_FROM       For no-load-80: prior report.json (default: <CERT_RESULTS_ROOT>/load-80/report/report.json)
  PARTNER_KEEP_WORK   When true, do not archive prior result directories (default: false)
  SKIP_HOST_VALIDATE  When true, skip host oc connectivity checks (default: false)

Creates separate work/ and report output trees under certification-results/<profile>/.
Archives any previous result directory for that profile with a UTC timestamp before running.
EOF
}

if [[ "${1:-}" == "-h" || "${1:-}" == "--help" ]]; then
	usage
	exit 0
fi
if [[ "$#" -lt 1 || "$#" -gt 2 ]]; then
	usage >&2
	exit 2
fi

profile="$1"
kubeconfig="${2:-${KUBECONFIG:-kubeconfig}}"
case "${profile}" in
	load-80) scenario="load-80" ;;
	no-load-80) scenario="no-load-80" ;;
	*)
		echo "error: profile must be load-80 or no-load-80" >&2
		exit 2
		;;
esac

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${repo_root}"
# shellcheck source=ci-utils.sh
source "${repo_root}/ci/scripts/ci-utils.sh"
# shellcheck source=partner-harness-common.sh
source "${repo_root}/ci/scripts/partner-harness-common.sh"

plan="$(partner_plan_for_profile "${profile}")" || {
	echo "error: unknown profile ${profile}" >&2
	exit 2
}
thresholds_file="${repo_root}/${PARTNER_THRESHOLDS_FILE}"

log_name="run-partner-cert-${profile}"
archive_partner_log "${log_name}"
ci_log_init "${log_name}"

backends_file="${BACKENDS:-backends.yaml}"
backend="${BACKEND:-}"
results_root="${CERT_RESULTS_ROOT:-certification-results}"
out_dir="${results_root}/${profile}"
work_dir="${out_dir}/work"
report_dir="${out_dir}/report"
image="$(partner_default_image)"

[[ -n "${backend}" ]] || {
	echo "error: set BACKEND to the name of your entry in ${backends_file}" >&2
	exit 1
}

validate_partner_host_tools
validate_partner_kubeconfig "${kubeconfig}"
validate_partner_backends_file "${backends_file}"
plan_path="${repo_root}/${plan}"
validate_partner_cert_inputs "${repo_root}/catalog.json" "${thresholds_file}" "${plan_path}"
partner_print_run_inputs "${repo_root}" "${plan}" "${profile}"
validate_partner_backend_entry "${backends_file}" "${backend}"
if [[ "${SKIP_HOST_VALIDATE:-false}" != "true" ]]; then
	validate_partner_cluster_connectivity "${kubeconfig}" "${plan_path}"
fi
if [[ "$(basename "${plan_path}")" == "level3-customer-no-load-80.yaml" ]]; then
	validate_partner_level3_no_load_plan "${plan_path}"
fi
partner_pull_image "${image}"

partner_prepare_result_dir "${out_dir}"
work_dir="$(partner_abs_dir "${work_dir}")"
report_dir="$(partner_abs_dir "${report_dir}")"

continue_args=()
prior_mount=""
if [[ "${profile}" == "no-load-80" ]]; then
	prior="${CONTINUE_FROM:-${results_root}/load-80/report/report.json}"
	[[ -s "${prior}" ]] || {
		echo "error: no-load-80 requires a prior load-80 report at ${prior} (or set CONTINUE_FROM)" >&2
		exit 1
	}
	continue_args=(--continue-from "/prior-load/report.json")
	prior_mount="$(dirname "$(realpath "${prior}")")"
fi

echo "==> harness run profile=${profile} scenario=${scenario}"
arch="$(partner_podman_arch)"
podman_args=(
	run --rm --userns=keep-id --user "$(id -u):$(id -g)" --network=host
	--platform "linux/${arch}"
	-v "${repo_root}/catalog.json:/inputs/catalog.json:ro,Z"
	-v "${thresholds_file}:/inputs/thresholds.json:ro,Z"
	-v "${repo_root}/plans:/plans:ro,Z"
	-v "$(realpath "${backends_file}"):/inputs/backends.yaml:ro,Z"
	-v "$(realpath "${kubeconfig}"):/kubeconfig:ro,Z"
	-v "${work_dir}:/work:Z"
	-v "${report_dir}:/results:Z"
	-e KUBECONFIG=/kubeconfig
	-e KUBECTL_INSECURE_SKIP_TLS_VERIFY="${KUBECTL_INSECURE_SKIP_TLS_VERIFY:-false}"
)
if [[ -n "${prior_mount}" ]]; then
	podman_args+=(-v "${prior_mount}:/prior-load:ro,Z")
fi
podman_args+=(
	"${image}" run
	--catalog /inputs/catalog.json
	--thresholds /inputs/thresholds.json
	--plan "/plans/$(basename "${plan}")"
	--backends /inputs/backends.yaml
	--backend "${backend}"
	--scenario "${scenario}"
	--workdir /work
	--output /results
	--no-attestations
	--verbose
)
if [[ "${#continue_args[@]}" -gt 0 ]]; then
	podman_args+=("${continue_args[@]}")
fi
podman "${podman_args[@]}"

echo "==> report output (all graded reports and run.log): ${report_dir}/"
echo "==> work output (all tool artifacts): ${work_dir}/"
echo "    On failure, send Red Hat: ${report_dir}/run.log and logs/${log_name}.log"
