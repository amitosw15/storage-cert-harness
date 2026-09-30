#!/usr/bin/env bash
# Verify the built image contains harness, kube-burner, kube-burner-ocp, kubectl, virtctl, and virtbench.
# Runs after image-build.sh. Same script locally and in GitLab.
set -euo pipefail
# shellcheck source=ci-utils.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/ci-utils.sh"
ci_log_init "image-contents-check"

eng="$(container_engine)"
img_ver="$(image_version)"
ref="${QUAY_IMAGE}:${img_ver}"

[[ -f "${IMAGE_TAR}" ]] || die "missing ${IMAGE_TAR}; run ci/scripts/image-build.sh first"

# Load the image if not already available
if [[ "${eng}" == "buildah" ]]; then
	img_id="$(buildah pull "docker-archive:${IMAGE_TAR}" 2>/dev/null || true)"
	run_cmd() { buildah run "${img_id}" -- "$@"; }
else
	"${eng}" load -i "${IMAGE_TAR}" >/dev/null 2>&1 || true
	run_cmd() { "${eng}" run --rm --platform linux/amd64 --entrypoint "" "${ref}" "$@"; }
fi

errors=0
fail() {
	echo "FAIL: $*"
	errors=$((errors + 1))
}

echo "==> checking dist/harness-image.tar"
test -s "${IMAGE_TAR}" || fail "image tarball is empty"

echo "==> checking image architecture"
if command -v skopeo >/dev/null 2>&1; then
	arch="$(skopeo inspect --raw "docker-archive:${IMAGE_TAR}" 2>/dev/null | grep -o '"architecture":"[^"]*"' | head -1 || true)"
	if [[ -n "${arch}" ]] && [[ "${arch}" != *"amd64"* ]]; then
		fail "image architecture is not amd64: ${arch}"
	fi
fi

echo "==> checking image version"
if [[ -f "${REPO_ROOT}/dist/image-version.txt" ]]; then
	file_ver="$(tr -d '[:space:]' < "${REPO_ROOT}/dist/image-version.txt")"
	if [[ "${file_ver}" != "${img_ver}" ]]; then
		fail "dist/image-version.txt (${file_ver}) != image_version() (${img_ver})"
	fi
fi

echo "==> checking /usr/bin/harness"
if run_cmd /usr/bin/harness version >/dev/null 2>&1; then
	harness_ver="$(run_cmd /usr/bin/harness version 2>&1 || true)"
	echo "  harness version: ${harness_ver}"
	if [[ -f "${REPO_ROOT}/dist/binary-version.txt" ]]; then
		bin_ver="$(tr -d '[:space:]' < "${REPO_ROOT}/dist/binary-version.txt")"
		if [[ "${harness_ver}" != *"${bin_ver}"* ]]; then
			fail "harness version in image (${harness_ver}) does not contain binary track (${bin_ver})"
		fi
	fi
else
	fail "/usr/bin/harness is missing or cannot run"
fi

echo "==> checking /usr/bin/kube-burner"
if run_cmd /usr/bin/kube-burner version >/dev/null 2>&1 || run_cmd /usr/bin/kube-burner --help >/dev/null 2>&1; then
	echo "  kube-burner: present"
else
	fail "/usr/bin/kube-burner is missing or cannot run"
fi

echo "==> checking /usr/bin/kube-burner-ocp"
if run_cmd /usr/bin/kube-burner-ocp version >/dev/null 2>&1 || run_cmd /usr/bin/kube-burner-ocp --help >/dev/null 2>&1; then
	echo "  kube-burner-ocp: present"
else
	fail "/usr/bin/kube-burner-ocp is missing or cannot run"
fi

echo "==> checking /usr/bin/kubectl"
if run_cmd /usr/bin/kubectl version --client >/dev/null 2>&1; then
	echo "  kubectl: present"
else
	fail "/usr/bin/kubectl is missing or cannot run"
fi

echo "==> checking /usr/bin/virtctl"
if run_cmd /usr/bin/virtctl version >/dev/null 2>&1 || run_cmd /usr/bin/virtctl --help >/dev/null 2>&1; then
	echo "  virtctl: present"
else
	fail "/usr/bin/virtctl is missing or cannot run"
fi

echo "==> checking /usr/bin/virtbench fio"
if run_cmd env VIRTBENCH_REPO=/opt/virtbench-runtime /usr/bin/virtbench fio --help >/dev/null 2>&1; then
	echo "  virtbench fio: present"
else
	fail "/usr/bin/virtbench fio is missing or cannot run"
fi

if [[ "${errors}" -gt 0 ]]; then
	die "image-contents-check found ${errors} issue(s)"
fi
echo "image-contents-check ok"
