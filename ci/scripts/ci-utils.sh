#!/usr/bin/env bash
# Shared helpers for ci/scripts/*.sh. Source this file; do not execute it.
#
# Offline Go contract: GOPROXY=off and -mod=vendor (ECOPROJECT-5274 / ADR-0010).

set -euo pipefail

CI_SCRIPTS_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CI_CONFIG_DIR="$(cd "${CI_SCRIPTS_DIR}/../config" && pwd)"
REPO_ROOT="$(cd "${CI_SCRIPTS_DIR}/../.." && pwd)"
export CI_SCRIPTS_DIR CI_CONFIG_DIR REPO_ROOT
cd "${REPO_ROOT}"

# shellcheck source=images.sh
source "${CI_SCRIPTS_DIR}/images.sh"

export GOPROXY="${GOPROXY:-off}"
export GOFLAGS="${GOFLAGS:--mod=vendor}"

IMAGE_TAR="${IMAGE_TAR:-${REPO_ROOT}/dist/harness-image.tar}"
LOG_DIR="${REPO_ROOT}/logs"
mkdir -p "${LOG_DIR}"

die() {
	echo "error: $*" >&2
	exit 1
}

require_cmd() {
	command -v "$1" >/dev/null 2>&1 || die "required command not found: $1 (see docs/ci.md)"
}

git_sha() {
	local sha="${CI_COMMIT_SHA:-${GITHUB_SHA:-}}"
	if [[ -n "${sha}" ]]; then
		echo "${sha:0:12}"
		return
	fi
	git rev-parse --short=12 HEAD 2>/dev/null || echo "dev"
}

image_ref() {
	echo "${QUAY_IMAGE}:$(image_version)"
}

is_main_push() {
	if [[ -n "${CI_COMMIT_BRANCH:-}" && "${CI_COMMIT_BRANCH}" == "main" && "${CI_PIPELINE_SOURCE:-}" == "push" ]]; then
		return 0
	fi
	if [[ "${GITHUB_REF:-}" == "refs/heads/main" && "${GITHUB_EVENT_NAME:-}" == "push" ]]; then
		return 0
	fi
	return 1
}

is_version_bump_push() {
	if is_main_push; then
		return 0
	fi
	if [[ "${CI_COMMIT_BRANCH:-}" == "test-ci" && "${CI_PIPELINE_SOURCE:-}" == "push" ]]; then
		return 0
	fi
	if [[ "${GITHUB_REF:-}" == "refs/heads/test-ci" && "${GITHUB_EVENT_NAME:-}" == "push" ]]; then
		return 0
	fi
	if [[ "${GITHUB_REF:-}" == "refs/heads/main" || "${GITHUB_REF:-}" == "refs/heads/test-ci" ]] &&
		[[ "${GITHUB_EVENT_NAME:-}" == "workflow_dispatch" ]]; then
		return 0
	fi
	return 1
}

binary_version() {
	if [[ -n "${HARNESS_VERSION:-}" ]]; then
		echo "${HARNESS_VERSION}"
		return
	fi
	local next
	next="$(tr -d '[:space:]' < "${REPO_ROOT}/NEXT-VERSION")"
	if is_main_push; then
		echo "${next}"
	else
		echo "${next}-$(git_sha)"
	fi
}

image_version() {
	if [[ -n "${HARNESS_IMAGE_VERSION:-}" ]]; then
		echo "${HARNESS_IMAGE_VERSION}"
		return
	fi
	local next
	next="$(tr -d '[:space:]' < "${REPO_ROOT}/NEXT-IMAGE-VERSION")"
	if is_main_push; then
		echo "${next}"
	else
		echo "${next}-$(git_sha)"
	fi
}

# Tee stdout/stderr to logs/<name>.log for the rest of the process.
_ci_log_tee=0
ci_log_init() {
	local name="$1"
	local log="${LOG_DIR}/${name}.log"
	if [[ -n "${LOG_TS:-}" ]]; then
		log="${LOG_DIR}/${name}-$(date -u +%Y%m%dT%H%M%SZ).log"
	fi
	: >"${log}"
	# GitLab k8s executor packs artifacts as soon as the script exits and does
	# not wait for process-substitution tee. Close FDs on EXIT so the log is complete.
	exec > >(tee -a "${log}") 2>&1
	_ci_log_tee=1
	echo "==> ${name}  $(date -u +%Y-%m-%dT%H:%M:%SZ)  sha=$(git_sha)"
}

_ci_log_flush() {
	if [[ "${_ci_log_tee:-0}" -eq 1 ]]; then
		exec 1>&- 2>&- || true
		sleep 0.2 || true
		_ci_log_tee=0
	fi
}
trap '_ci_log_flush' EXIT

merge_base() {
	if [[ -n "${CI_MERGE_REQUEST_DIFF_BASE_SHA:-}" ]]; then
		echo "${CI_MERGE_REQUEST_DIFF_BASE_SHA}"
		return
	fi
	if git rev-parse --verify origin/main >/dev/null 2>&1; then
		if base="$(git merge-base HEAD origin/main 2>/dev/null)"; then
			echo "${base}"
			return
		fi
	fi
	if git rev-parse --verify main >/dev/null 2>&1; then
		if base="$(git merge-base HEAD main 2>/dev/null)"; then
			echo "${base}"
			return
		fi
	fi
	# No origin/main (initial commit, shallow clone): parent of HEAD, or HEAD itself.
	if git rev-parse --verify HEAD~1 >/dev/null 2>&1; then
		git rev-parse HEAD~1
		return
	fi
	git rev-parse HEAD
}

# Default container build/run tool is podman (local). Wrappers override:
# GitHub image jobs use Podman; local runs also default to Podman.
container_engine() {
	local eng="${CONTAINER_ENGINE:-podman}"
	case "${eng}" in
	podman | buildah | docker) ;;
	*) die "unsupported CONTAINER_ENGINE=${eng} (want podman, buildah, or docker)" ;;
	esac
	command -v "${eng}" >/dev/null 2>&1 || die "required command not found: ${eng} (default is podman; see docs/ci.md / ./ci/install-tools.sh)"
	echo "${eng}"
}

acquire_local_image_lock() {
	# image-build-local.sh holds the lock for the whole local image rebuild.
	if [[ "${LOCAL_IMAGE_LOCK_HELD:-}" == "1" ]]; then
		return 0
	fi
	require_cmd flock
	mkdir -p "${REPO_ROOT}/work"
	exec 9>"${REPO_ROOT}/work/local-image.lock"
	flock -x 9
	export LOCAL_IMAGE_LOCK_HELD=1
}

# Serialize concurrent harness smoke runs (shared log archive + work/report dirs).
acquire_smoke_run_lock() {
	if [[ "${SMOKE_RUN_LOCK_HELD:-}" == "1" ]]; then
		return 0
	fi
	require_cmd flock
	mkdir -p "${REPO_ROOT}/work"
	exec 8>"${REPO_ROOT}/work/smoke-run.lock"
	flock -x 8
	export SMOKE_RUN_LOCK_HELD=1
}

# Drop the previous local image pointer, its engine image, and shared dist
# metadata before starting a new build. Caller must hold acquire_local_image_lock.
invalidate_previous_local_image_build() {
	local local_ref_file="${REPO_ROOT}/work/local-image-ref" old_ref eng

	if [[ -s "${local_ref_file}" ]]; then
		old_ref="$(tr -d '[:space:]' < "${local_ref_file}")"
		if [[ -n "${old_ref}" ]] && command -v podman >/dev/null 2>&1; then
			if podman image exists "${old_ref}" >/dev/null 2>&1; then
				echo "==> removing previous local image ${old_ref}"
				podman rmi -f "${old_ref}" >/dev/null 2>&1 || true
			fi
		fi
	fi

	rm -f -- "${local_ref_file}"
	rm -f -- "${IMAGE_TAR}"
	rm -f -- "${REPO_ROOT}/dist/image-version.txt" "${REPO_ROOT}/dist/binary-version.txt"
}

# Archive the previous run-image-tests log (if any) before ci_log_init truncates it.
archive_run_image_tests_log() {
	local log="${LOG_DIR}/run-image-tests.log"
	if [[ ! -f "${log}" || ! -s "${log}" ]]; then
		echo "smoke hygiene: no previous run-image-tests log to archive"
		return 0
	fi
	local archived
	archived="${LOG_DIR}/run-image-tests-$(date -u +%Y%m%dT%H%M%SZ)-$$.log"
	echo "smoke hygiene: archiving previous log ${log} -> ${archived}"
	mv -- "${log}" "${archived}"
	echo "smoke hygiene: archived previous smoke log"
}

# Drop harness smoke work/report trees under work/ (kubeconfig, backends, catalog, image ref kept).
clean_harness_smoke_artifacts() {
	local work="${REPO_ROOT}/work"
	local removed=0
	echo "smoke hygiene: removing prior harness smoke artifacts under ${work}/"
	for dir in harness-smoke-work harness-smoke-report; do
		local path="${work:?}/${dir}"
		if [[ -d "${path}" ]]; then
			echo "smoke hygiene: deleting ${path}"
			rm -rf "${path}"
			removed=1
		else
			echo "smoke hygiene: ${path} not present (skip)"
		fi
	done
	if [[ "${removed}" -eq 1 ]]; then
		echo "smoke hygiene: prior work/report trees removed"
	else
		echo "smoke hygiene: no prior work/report directories to remove"
	fi
}

# Remove local harness image tarball, tags, and engine build cache. Caller must
# already hold acquire_local_image_lock (or run via clean-image-cache.sh).
clean_local_image_cache() {
	local eng img_ver ref tag
	eng="$(container_engine)"
	img_ver="$(image_version)"
	ref="${QUAY_IMAGE}:${img_ver}"

	invalidate_previous_local_image_build

	echo "==> removing dist image tarball"
	rm -f -- "${IMAGE_TAR}"

	echo "==> removing local harness image tags (${eng})"
	for tag in "${ref}" "${QUAY_IMAGE}:local" "${QUAY_IMAGE}:main"; do
		if "${eng}" image exists "${tag}" >/dev/null 2>&1; then
			"${eng}" rmi -f "${tag}" >/dev/null 2>&1 || true
		fi
	done

	echo "==> pruning ${eng} build cache"
	case "${eng}" in
	podman)
		podman builder prune -af >/dev/null 2>&1 || true
		podman image prune -af >/dev/null 2>&1 || true
		;;
	buildah)
		buildah rm -af >/dev/null 2>&1 || true
		buildah rmi -af >/dev/null 2>&1 || true
		;;
	docker)
		docker builder prune -af >/dev/null 2>&1 || true
		docker image prune -af >/dev/null 2>&1 || true
		;;
	esac
}
