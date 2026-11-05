#!/usr/bin/env bash
# Retag the pinned Trivy, Dive, and Hadolint images into quay.io/virtarraycert/ci_tools.
# That repo is public and starts empty — this is how it gets its tags
# (ECOPROJECT-5411 / ECOPROJECT-5417).
#
# Requires: a host that can pull Docker Hub *and* push to virtarraycert
# (podman login quay.io). Run from a host that can pull Docker Hub when mirroring.
#
# Usage:
#   ./ci/mirror-ci-tools.sh           # pull Hub, tag, push
#   PUSH=0 ./ci/mirror-ci-tools.sh    # pull + tag only (no push)
set -euo pipefail
# shellcheck source=ci-utils.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/ci-utils.sh"
ci_log_init "mirror-ci-tools"

eng="$(container_engine)"
PUSH="${PUSH:-1}"

mirror() {
	local src="$1"
	local dest="$2"
	echo "mirroring ${src} -> ${dest} with ${eng}"
	"${eng}" pull "${src}"
	"${eng}" tag "${src}" "${dest}"
	if [[ "${PUSH}" == "1" ]]; then
		"${eng}" push "${dest}"
	else
		echo "PUSH=0: skipped push of ${dest}"
	fi
}

mirror "${TRIVY_SRC}" "${CI_TOOLS_IMAGE}:${TRIVY_TAG}"
mirror "${DIVE_SRC}" "${CI_TOOLS_IMAGE}:${DIVE_TAG}"
mirror "${HADOLINT_SRC}" "${CI_TOOLS_IMAGE}:${HADOLINT_TAG}"
echo "ci_tools mirrors: ${CI_TOOLS_IMAGE}:${TRIVY_TAG} ${CI_TOOLS_IMAGE}:${DIVE_TAG} ${CI_TOOLS_IMAGE}:${HADOLINT_TAG}"
