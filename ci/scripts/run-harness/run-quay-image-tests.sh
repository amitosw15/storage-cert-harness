#!/usr/bin/env bash
# Pull and run the published container smoke plan against the local kubeconfig.
set -euo pipefail

usage() {
	cat <<'EOF'
Usage:
  ./ci/scripts/run-harness/run-quay-image-tests.sh [VERSION] [KUBECONFIG]
	BACKEND=<name> ./ci/scripts/run-harness/run-quay-image-tests.sh [VERSION] [KUBECONFIG]
	BACKEND=<name> make run-quay-image-tests [VERSION=<version>] [KUBECONFIG=<path>]

Pull and run quay.io/virtarraycert/storage-cert-harness:<version>.
VERSION defaults to latest and KUBECONFIG defaults to work/kubeconfig.
BACKEND defaults to oc-edge-112 and can be used to select another configured backend.
The YAML parser `yq` is required to validate the selected backend.
Assumption: Podman has access to valid quay.io credentials, such as an
existing Docker-compatible auth configuration, a Quay token, or `podman login`.
EOF
}

if [[ "${1:-}" == "-h" || "${1:-}" == "--help" ]]; then
	usage
	exit 0
fi
if [[ "$#" -gt 2 ]]; then
	usage >&2
	exit 2
fi

_run_harness_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=../ci-utils.sh
source "${_run_harness_dir}/../ci-utils.sh"
repo_root="${REPO_ROOT}"
cd "${repo_root}"
# shellcheck source=../partner-harness-common.sh
source "${_run_harness_dir}/../partner-harness-common.sh"
# shellcheck source=smoke-cluster-lease.sh
source "${_run_harness_dir}/smoke-cluster-lease.sh"
archive_partner_log "run-quay-image-tests"
ci_log_init "run-quay-image-tests"

command -v podman >/dev/null 2>&1 || {
	echo "error: podman is required" >&2
	exit 1
}
version="${1:-latest}"
KUBECONFIG="${2:-work/kubeconfig}"
[[ -n "${version}" ]] || {
	echo "error: image version must not be empty" >&2
	exit 1
}
[[ -s "${KUBECONFIG}" && -r "${KUBECONFIG}" ]] || {
	echo "error: kubeconfig is missing or unreadable: ${KUBECONFIG}" >&2
	exit 1
}
[[ -s work/backends.local.yaml ]] || {
	echo "error: backend configuration is missing or empty: work/backends.local.yaml" >&2
	exit 1
}

backend="${BACKEND:-oc-edge-112}"
command -v yq >/dev/null 2>&1 || {
	echo "error: yq is required to validate backend '${backend}' in work/backends.local.yaml" >&2
	exit 1
}
command -v jq >/dev/null 2>&1 || {
	echo "error: jq is required for cluster smoke lease updates" >&2
	exit 1
}
export YQ_BACKEND="${backend}"
backend_name="$(yq e -r '.backends[] | select(.name == strenv(YQ_BACKEND)) | .name' work/backends.local.yaml)"
[[ "${backend_name}" == "${backend}" ]] || {
	echo "error: backend '${backend}' is not defined in work/backends.local.yaml" >&2
	exit 1
}
[[ -n "${backend}" ]] || {
	echo "error: no backend name found in work/backends.local.yaml; set BACKEND explicitly" >&2
	exit 1
}

export SMOKE_CLUSTER_LEASE_KUBECONFIG="$(realpath "${KUBECONFIG}")"
quay_plan="work/container-smoke.yaml"
[[ -s "${quay_plan}" ]] || quay_plan="plans/harness-smoke-plan.yaml"
smoke_cluster_lease_acquire "container-smoke" "${quay_plan}"
smoke_cluster_lease_install_traps "_ci_log_flush"
smoke_cluster_lease_start_renewal_loop

image="${HARNESS_IMAGE:-${QUAY_IMAGE}:${version}}"
# Assumption: Podman can read valid quay.io credentials from its configured
# authentication sources, which may include Docker-compatible settings.
podman pull "${image}"
mkdir -p work/smoke-work work/smoke-report

arch="${GOARCH:-$(uname -m)}"
case "${arch}" in
	x86_64) arch=amd64 ;;
	aarch64) arch=arm64 ;;
esac
podman_args=(
	run
	--rm
	--userns=keep-id
	--user "$(id -u):$(id -g)"
	--network=host
	--platform "linux/${arch}"
	-v "${repo_root}/work:/work:Z"
	-v "$(realpath "${KUBECONFIG}"):/work/kubeconfig:ro,Z"
	-w /work
	-e KUBECONFIG=/work/kubeconfig
	"${image}"
	run
	--catalog /work/catalog.json
	--thresholds /work/thresholds.json
	--plan /work/container-smoke.yaml
	--backends /work/backends.local.yaml
	--backend "${backend}"
	--workdir /work/smoke-work
	--output /work/smoke-report
)
podman "${podman_args[@]}"