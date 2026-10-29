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
# Real stdout (terminal or make pipe) before the first tee; reused when switching log files.
_ci_log_out_fd=""

_ci_log_bind_out_fd() {
	if [[ -n "${_ci_log_out_fd}" ]]; then
		return 0
	fi
	exec 3>&1
	_ci_log_out_fd=3
}

_ci_log_start_tee() {
	local log="$1"
	_ci_log_bind_out_fd
	# Tee must write to the saved fd: after a prior tee, fd 1 is a pipe, not the terminal.
	exec > >(tee -a "${log}" >&"${_ci_log_out_fd}") 2>&1
	_ci_log_tee=1
}

ci_log_init() {
	local name="$1"
	local log="${LOG_DIR}/${name}.log"
	if [[ -n "${LOG_TS:-}" ]]; then
		log="${LOG_DIR}/${name}-$(date -u +%Y%m%dT%H%M%SZ).log"
	fi
	: >"${log}"
	# GitLab k8s executor packs artifacts as soon as the script exits and does
	# not wait for process-substitution tee. Close FDs on EXIT so the log is complete.
	_ci_log_start_tee "${log}"
	echo "==> ${name}  $(date -u +%Y-%m-%dT%H:%M:%SZ)  sha=$(git_sha)"
}

_ci_log_flush() {
	if [[ "${_ci_log_tee:-0}" -eq 1 ]]; then
		# Do not exec 1>&- (closes tee stdin and can block until the subshell drains).
		# Replacing stdout ends the prior process-substitution pipeline.
		exec 1>/dev/null 2>&1
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
	command -v "${eng}" >/dev/null 2>&1 || die "required command not found: ${eng} (default is podman; see docs/ci.md / ./ci/scripts/install-tools.sh)"
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

# Resolve work/local-image-ref and confirm the image exists in Podman (stdout = image ref).
harness_smoke_resolve_local_image() {
	local ref="${REPO_ROOT}/work/local-image-ref"
	local image
	[[ -s "${ref}" ]] || die "work/local-image-ref is missing; run 'make image-build-local' first"
	image="$(tr -d '[:space:]' < "${ref}")"
	[[ -n "${image}" ]] || die "work/local-image-ref is empty; run 'make image-build-local' first"
	command -v podman >/dev/null 2>&1 || die "podman is required"
	podman image exists "${image}" >/dev/null 2>&1 ||
		die "local image ${image} is missing (stale work/local-image-ref); run 'make image-build-local' first"
	echo "${image}"
}

# Hold local-image.lock through podman run -d (container create), then stream logs.
harness_smoke_run_podman() {
	require_cmd flock
	mkdir -p "${REPO_ROOT}/work"
	local lock="${REPO_ROOT}/work/local-image.lock"
	local -a podman_opts=()
	local -a harness_args=()
	local arg saw_sep=0
	for arg in "$@"; do
		if [[ "${arg}" == "--" ]]; then
			saw_sep=1
			continue
		fi
		if [[ "${saw_sep}" -eq 0 ]]; then
			podman_opts+=("${arg}")
		else
			harness_args+=("${arg}")
		fi
	done
	[[ "${saw_sep}" -eq 1 ]] || die "harness_smoke_run_podman: expected '--' before harness run arguments"
	local exit_code=0
	(
		exec 200>"${lock}"
		flock -x 200
		local image cid logs_pid wait_status inspect_ec
		image="$(harness_smoke_resolve_local_image)"
		echo "validation passed: local harness image '${image}' present (image lock held through podman create)"
		cid="$(podman run -d "${podman_opts[@]}" "${image}" run "${harness_args[@]}")"
		[[ -n "${cid}" ]] || {
			echo "error: podman run -d did not return a container id" >&2
			exit 1
		}
		flock -u 200
		echo "smoke: container ${cid:0:12} started; streaming harness logs"
		podman logs -f "${cid}" &
		logs_pid=$!
		local wait_rc=0
		local container_exit=""
		container_exit="$(podman wait "${cid}" 2>/dev/null)" || wait_rc=$?
		wait "${logs_pid}" 2>/dev/null || true
		container_exit="${container_exit//$'\r'/}"
		container_exit="${container_exit//$'\n'/}"
		if inspect_ec="$(podman inspect -f '{{.State.ExitCode}}' "${cid}" 2>/dev/null)"; then
			:
		elif [[ -n "${container_exit}" && "${container_exit}" =~ ^[0-9]+$ ]]; then
			inspect_ec="${container_exit}"
		else
			inspect_ec="${wait_rc}"
		fi
		exit "${inspect_ec}"
	)
	exit_code=$?
	return "${exit_code}"
}

export SMOKE_RUN_LOCK_EXIT_BUSY="${SMOKE_RUN_LOCK_EXIT_BUSY:-3}"

acquire_smoke_run_lock_for_hygiene() {
	if [[ "${SMOKE_RUN_LOCK_HELD:-}" == "1" ]]; then
		return 0
	fi
	require_cmd flock
	mkdir -p "${REPO_ROOT}/work"
	exec 7>"${REPO_ROOT}/work/smoke-run.lock"
	if ! flock -x -n 7; then
		die "smoke is already running (work/smoke-run.lock held); wait for it to finish before clean/prepare"
	fi
	export SMOKE_RUN_LOCK_HELD=1
}

# Exit when another local smoke holds work/smoke-run.lock (distinct from lease busy).
SMOKE_RUN_LOCK_EXIT_BUSY="${SMOKE_RUN_LOCK_EXIT_BUSY:-3}"

smoke_run_lock_busy() {
	echo "error: local harness smoke is already running (work/smoke-run.lock is held)" >&2
	echo "error: wait for the active run to finish before starting another; see logs/run-image-tests.log" >&2
	exit "${SMOKE_RUN_LOCK_EXIT_BUSY}"
}

# Serialize concurrent harness smoke runs (shared work/report dirs). Never blocks.
acquire_smoke_run_lock() {
	if [[ "${SMOKE_RUN_LOCK_HELD:-}" == "1" ]]; then
		return 0
	fi
	require_cmd flock
	mkdir -p "${REPO_ROOT}/work"
	exec 8>"${REPO_ROOT}/work/smoke-run.lock"
	if ! flock -x -n 8; then
		smoke_run_lock_busy
	fi
	export SMOKE_RUN_LOCK_HELD=1
}

# Preflight logging before a smoke run is committed (lock + lease acquired).
ci_log_init_temp() {
	local name="$1"
	local log="${LOG_DIR}/${name}-temp.log"
	: >"${log}"
	_ci_log_start_tee "${log}"
	echo "==> ${name} (preflight)  $(date -u +%Y-%m-%dT%H:%M:%SZ)  sha=$(git_sha)"
	echo "smoke hygiene: logging to ${log} until smoke run is committed"
}

# After lock and cluster lease: archive prior smoke log and tee to logs/run-image-tests.log.
commit_harness_smoke_run_log() {
	local name="${1:-run-image-tests}"
	echo "smoke hygiene: smoke run committed; archiving prior ${name}.log if present"
	archive_run_image_tests_log
	# Switch tee target: ci_log_init replaces stdout (ends temp tee) without _ci_log_flush.
	ci_log_init "${name}"
	echo "smoke hygiene: preflight details are in ${LOG_DIR}/${name}-temp.log"
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

# Remove run-image-tests log files under logs/ (not other CI job logs).
clean_smoke_run_logs() {
	echo "smoke hygiene: removing smoke run logs under ${LOG_DIR}/"
	local removed=0
	for pattern in run-image-tests.log run-image-tests-temp.log; do
		local path="${LOG_DIR}/${pattern}"
		if [[ -f "${path}" ]]; then
			rm -f -- "${path}"
			removed=1
		fi
	done
	shopt -s nullglob
	local archived=( "${LOG_DIR}"/run-image-tests-*.log )
	shopt -u nullglob
	if [[ "${#archived[@]}" -gt 0 ]]; then
		rm -f -- "${archived[@]}"
		removed=1
	fi
	if [[ "${removed}" -eq 1 ]]; then
		echo "smoke hygiene: smoke run logs removed"
	else
		echo "smoke hygiene: no smoke run logs to remove"
	fi
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
