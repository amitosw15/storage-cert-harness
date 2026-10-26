#!/usr/bin/env bash
# Pull and run the published container smoke plan against the local kubeconfig.
set -euo pipefail

usage() {
	cat <<'EOF'
Usage:
  ./ci/scripts/run-quay-image-tests.sh [VERSION] [KUBECONFIG]
	BACKEND=<name> ./ci/scripts/run-quay-image-tests.sh [VERSION] [KUBECONFIG]
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

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${repo_root}"
# shellcheck source=ci-utils.sh
source "${repo_root}/ci/scripts/ci-utils.sh"
# shellcheck source=partner-harness-common.sh
source "${repo_root}/ci/scripts/partner-harness-common.sh"
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