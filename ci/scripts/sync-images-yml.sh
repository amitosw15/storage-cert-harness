#!/usr/bin/env bash
# Project ci/images.env into ci/images.yml for GitLab `include:`.
# Usage:
#   ./ci/sync-images-yml.sh          # rewrite ci/images.yml
#   ./ci/sync-images-yml.sh --check  # fail if generated image data is stale
set -euo pipefail

_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
_config="$(cd "${_dir}/../config" && pwd)"
out="${_config}/images.yml"

# Always emit from the file, not from GitLab-injected CI variables.
unset TRIVY_TAG DIVE_TAG HADOLINT_TAG TRIVY_SRC DIVE_SRC HADOLINT_SRC TRIVY_IMAGE DIVE_IMAGE HADOLINT_IMAGE 2>/dev/null || true
# shellcheck source=images.sh
CI_IMAGES_FORCE_FILE=1 source "${_dir}/images.sh"

body="$(
	cat <<EOF
# Generated from ci/config/images.env by ci/scripts/sync-images-yml.sh. Do not edit.
# GitLab \`image:\` is resolved at parse time and cannot source dotenv.
variables:
  GO_IMAGE: "${GO_IMAGE}"
  BUILD_IMAGE: "${BUILD_IMAGE}"
  RUNTIME_IMAGE: "${RUNTIME_IMAGE}"
  YAML_LINT_IMAGE: "${YAML_LINT_IMAGE}"
  MD_LINT_IMAGE: "${MD_LINT_IMAGE}"
  BUILDAH_IMAGE: "${BUILDAH_IMAGE}"
  CI_TOOLS_IMAGE: "${CI_TOOLS_IMAGE}"
  QUAY_IMAGE: "${QUAY_IMAGE}"
  GOLANGCI_LINT_VERSION: "${GOLANGCI_LINT_VERSION}"
  TRIVY_VERSION: "${TRIVY_VERSION}"
  DIVE_VERSION: "${DIVE_VERSION}"
  HADOLINT_VERSION: "${HADOLINT_VERSION}"
  GOSEC_VERSION: "${GOSEC_VERSION}"
  TRIVY_TAG: "${TRIVY_TAG}"
  DIVE_TAG: "${DIVE_TAG}"
  HADOLINT_TAG: "${HADOLINT_TAG}"
  TRIVY_SRC: "${TRIVY_SRC}"
  DIVE_SRC: "${DIVE_SRC}"
  HADOLINT_SRC: "${HADOLINT_SRC}"
  TRIVY_IMAGE: "${TRIVY_IMAGE}"
  DIVE_IMAGE: "${DIVE_IMAGE}"
  HADOLINT_IMAGE: "${HADOLINT_IMAGE}"
EOF
)"

check_containerfile_arg() {
  local name="$1"
  local want="$2"
  local line="ARG ${name}=${want}"
  if ! grep -qxF "${line}" "${_dir}/../../Containerfile"; then
    echo "error: Containerfile is missing '${line}' (must match the pinned tool versions)" >&2
    return 1
  fi
}

if [[ "${1:-}" == "--check" ]]; then
	# Do not use diff(1): the yamllint job image has no diffutils.
	if [[ "$(cat "${out}")" != "${body}" ]]; then
		echo "error: ci/config/images.yml is stale; run ./ci/scripts/sync-images-yml.sh" >&2
		exit 1
	fi
	check_containerfile_arg KUBE_BURNER_VERSION "${KUBE_BURNER_VERSION}"
	check_containerfile_arg KUBE_BURNER_OCP_VERSION "${KUBE_BURNER_OCP_VERSION}"
	check_containerfile_arg KUBE_BURNER_CORE_REF "${KUBE_BURNER_CORE_REF}"
	check_containerfile_arg OPENSHIFT_CLIENT_VERSION "${OPENSHIFT_CLIENT_VERSION}"
	check_containerfile_arg VIRTBENCH_VERSION "${VIRTBENCH_VERSION}"
	check_containerfile_arg KUBEVIRT_VERSION "${KUBEVIRT_VERSION}"
  echo "ci/config/images.yml and Containerfile tool-version ARGs are synchronized"
	exit 0
fi

printf '%s\n' "${body}" >"${out}"
echo "wrote ${out}"
