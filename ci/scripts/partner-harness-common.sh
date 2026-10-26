#!/usr/bin/env bash
# Shared helpers for partner certification runs against the published Quay image.
# Source from run-partner-*.sh; do not execute directly.
set -euo pipefail

# shellcheck source=partner-host-env.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/partner-host-env.sh"

# Level-3 customer certification inputs (paths relative to repository root).
PARTNER_THRESHOLDS_FILE="${PARTNER_THRESHOLDS_FILE:-thresholds.json}"
PARTNER_PLAN_LOAD_80="${PARTNER_PLAN_LOAD_80:-plans/level3-customer-load-80.yaml}"
PARTNER_PLAN_NO_LOAD_80="${PARTNER_PLAN_NO_LOAD_80:-plans/level3-customer-no-load-80.yaml}"

partner_default_image() {
	if [[ -n "${HARNESS_IMAGE:-}" ]]; then
		echo "${HARNESS_IMAGE}"
		return
	fi
	local base="${QUAY_IMAGE:-quay.io/virtarraycert/storage-cert-harness}"
	if [[ "${base}" == *@* || "${base}" == *:* ]]; then
		echo "${base}"
	else
		echo "${base}:main"
	fi
}

partner_plan_for_profile() {
	case "$1" in
		load-80) echo "${PARTNER_PLAN_LOAD_80}" ;;
		no-load-80) echo "${PARTNER_PLAN_NO_LOAD_80}" ;;
		*) return 1 ;;
	esac
}

partner_print_run_inputs() {
	local repo_root="$1" plan_rel="$2" profile="${3:-}"
	local plan_display="${plan_rel}"
	[[ "${plan_display}" = /* ]] || plan_display="${repo_root}/${plan_display}"
	echo "==> harness run inputs (repository root: ${repo_root})"
	echo "    profile:    ${profile:-certification}"
	echo "    plan:       ${plan_display}"
	echo "    thresholds: ${repo_root}/${PARTNER_THRESHOLDS_FILE}"
	echo "    catalog:    ${repo_root}/catalog.json"
}

# Podman treats non-absolute -v sources as named volumes; bind mounts need absolute paths.
partner_abs_dir() {
	local dir="$1"
	mkdir -p "${dir}"
	realpath "${dir}"
}

# If path exists (file or directory), rename it with a UTC timestamp suffix.
archive_path_with_timestamp() {
	local path="$1"
	local base archived ts
	[[ -e "${path}" ]] || return 0
	ts="$(date -u +%Y%m%dT%H%M%SZ)"
	if [[ -d "${path}" ]]; then
		base="${path%/}"
		archived="${base}-${ts}"
	else
		archived="${path%.log}-${ts}.log"
		[[ "${archived}" == "${path}" ]] && archived="${path}-${ts}"
	fi
	echo "partner hygiene: archiving ${path} -> ${archived}"
	mv -- "${path}" "${archived}"
}

archive_partner_log() {
	local name="$1"
	local log="${LOG_DIR}/${name}.log"
	archive_path_with_timestamp "${log}"
}

# Archive prior certification result trees before a new run (unless PARTNER_KEEP_WORK=true).
partner_prepare_result_dir() {
	local dir="$1"
	if [[ "${PARTNER_KEEP_WORK:-false}" == "true" ]]; then
		echo "partner hygiene: keeping existing ${dir} (PARTNER_KEEP_WORK=true)"
		mkdir -p "${dir}"
		return 0
	fi
	archive_path_with_timestamp "${dir}"
	mkdir -p "${dir}"
}

validate_partner_host_tools() {
	command -v podman >/dev/null 2>&1 || {
		echo "error: podman is required to run the published harness image" >&2
		exit 1
	}
	echo "validation passed: podman is available"
}

validate_partner_kubeconfig() {
	local kubeconfig="$1"
	[[ -s "${kubeconfig}" && -r "${kubeconfig}" ]] || {
		echo "error: kubeconfig is missing or unreadable: ${kubeconfig}" >&2
		exit 1
	}
	echo "validation passed: kubeconfig is readable (${kubeconfig})"
}

validate_partner_backends_file() {
	local backends_file="$1"
	[[ -s "${backends_file}" ]] || {
		echo "error: backends file is missing or empty: ${backends_file}" >&2
		exit 1
	}
	echo "validation passed: backends file is available (${backends_file})"
}

validate_partner_cert_inputs() {
	local catalog="$1" thresholds="$2" plan="$3"
	[[ -s "${catalog}" ]] || {
		echo "error: catalog missing: ${catalog}" >&2
		exit 1
	}
	[[ -s "${thresholds}" ]] || {
		echo "error: thresholds missing: ${thresholds}" >&2
		exit 1
	}
	[[ -s "${plan}" ]] || {
		echo "error: plan missing: ${plan}" >&2
		exit 1
	}
	echo "validation passed: catalog, thresholds, and plan are present"
}

validate_partner_backend_entry() {
	local backends_file="$1" backend="$2"
	local yq_bin
	yq_bin="$(partner_resolve_yq || true)"
	[[ -n "${yq_bin}" && -x "${yq_bin}" ]] || {
		echo "error: yq is required to validate the backends file (install: ./ci/scripts/install-partner-tools.sh; then: source ./ci/scripts/partner-host-env.sh)" >&2
		exit 1
	}
	echo "validation passed: yq is available"
	export YQ_BACKEND="${backend}"
	local backend_name
	backend_name="$("${yq_bin}" e -r '.backends[] | select(.name == strenv(YQ_BACKEND)) | .name' "${backends_file}")"
	[[ "${backend_name}" == "${backend}" ]] || {
		echo "error: backend '${backend}' is not defined in ${backends_file}" >&2
		exit 1
	}
	echo "validation passed: backend '${backend}' is defined"
	local storage_class snapshot_class
	storage_class="$("${yq_bin}" e -r '.backends[] | select(.name == strenv(YQ_BACKEND)) | .storage_class // ""' "${backends_file}")"
	snapshot_class="$("${yq_bin}" e -r '.backends[] | select(.name == strenv(YQ_BACKEND)) | .snapshot_class // ""' "${backends_file}")"
	[[ -n "${storage_class}" ]] || {
		echo "error: backend '${backend}' has no storage_class in ${backends_file}" >&2
		exit 1
	}
	echo "validation passed: backend storage_class is '${storage_class}'"
	[[ -n "${snapshot_class}" ]] || {
		echo "error: backend '${backend}' has no snapshot_class in ${backends_file}" >&2
		exit 1
	}
	echo "validation passed: backend snapshot_class is '${snapshot_class}'"
	PARTNER_STORAGE_CLASS="${storage_class}"
	PARTNER_SNAPSHOT_CLASS="${snapshot_class}"
	export PARTNER_STORAGE_CLASS PARTNER_SNAPSHOT_CLASS
}

validate_partner_cluster_connectivity() {
	local kubeconfig="$1" plan="${2:-}"
	command -v oc >/dev/null 2>&1 || {
		echo "error: oc is required for cluster connectivity checks on the host" >&2
		exit 1
	}
	echo "validation passed: oc is available"
	local oc_args=(--kubeconfig "${kubeconfig}")
	if [[ "${KUBECTL_INSECURE_SKIP_TLS_VERIFY:-false}" == "true" ]]; then
		oc_args+=(--insecure-skip-tls-verify=true)
	fi
	oc "${oc_args[@]}" whoami --show-server >/dev/null || {
		echo "error: cluster is not reachable with ${kubeconfig}" >&2
		exit 1
	}
	echo "validation passed: cluster API is reachable (connectivity)"

	local nodes
	if ! nodes="$(oc "${oc_args[@]}" get nodes -o jsonpath='{range .items[*]}{.metadata.name}{"\t"}{range .status.conditions[?(@.type=="Ready")]}{.status}{end}{"\n"}{end}')"; then
		echo "error: unable to read cluster nodes (cluster health)" >&2
		exit 1
	fi
	[[ -n "${nodes}" ]] || {
		echo "error: cluster returned no nodes" >&2
		exit 1
	}
	while IFS=$'\t' read -r node ready; do
		[[ -n "${node}" && "${ready}" == "True" ]] || {
			echo "error: node '${node:-<unknown>}' is not Ready (cluster health)" >&2
			exit 1
		}
	done <<< "${nodes}"
	echo "validation passed: all cluster nodes are Ready (cluster health)"

	oc "${oc_args[@]}" get storageclass "${PARTNER_STORAGE_CLASS}" >/dev/null || {
		echo "error: storage class '${PARTNER_STORAGE_CLASS}' from backends does not exist in the cluster" >&2
		exit 1
	}
	echo "validation passed: storage class '${PARTNER_STORAGE_CLASS}' exists in the cluster"
	oc "${oc_args[@]}" get volumesnapshotclass "${PARTNER_SNAPSHOT_CLASS}" >/dev/null || {
		echo "error: volume snapshot class '${PARTNER_SNAPSHOT_CLASS}' from backends does not exist in the cluster" >&2
		exit 1
	}
	echo "validation passed: volume snapshot class '${PARTNER_SNAPSHOT_CLASS}' exists in the cluster"

	if [[ -n "${plan}" && -s "${plan}" ]]; then
		local yq_bin
		yq_bin="$(partner_resolve_yq)"
		local plan_nodes
		plan_nodes="$("${yq_bin}" e -r '.. | select(tag == "!!map") | select(has("node_name") or has("target_node")) | (.node_name // .target_node) | select(. != null and . != "")' "${plan}")"
		while IFS= read -r plan_node; do
			[[ -z "${plan_node}" ]] && continue
			[[ "${plan_node}" == *"<"* ]] && continue
			oc "${oc_args[@]}" get node "${plan_node}" >/dev/null || {
				echo "error: plan node '${plan_node}' does not exist in the cluster" >&2
				exit 1
			}
		done <<< "${plan_nodes}"
		echo "validation passed: plan-specified nodes exist (when configured)"
	fi
}

# Fail fast when the Level-3 no-load plan still has template placeholders or missing TR-VIRT-010 params.
validate_partner_level3_no_load_plan() {
	local plan_path="$1"
	local yq_bin
	yq_bin="$(partner_resolve_yq || true)"
	[[ -n "${yq_bin}" && -x "${yq_bin}" ]] || return 0

	local placeholders
	placeholders="$("${yq_bin}" e -r '
		.overrides
		| .. | select(tag == "!!str")
		| select(test("<") and test(">"))
	' "${plan_path}")"
	if [[ -n "${placeholders}" ]]; then
		echo "error: plan still has placeholders; edit ${plan_path} before preflight (section 3 of PARTNER-CERTIFICATION-GUIDE.md):" >&2
		while IFS= read -r line; do
			[[ -z "${line}" ]] && continue
			echo "  - ${line}" >&2
		done <<< "${placeholders}"
		exit 1
	fi

	local replicas snapshot_count
	replicas="$("${yq_bin}" e -r '.overrides."TR-VIRT-010".replicas // 0' "${plan_path}")"
	snapshot_count="$("${yq_bin}" e -r '.overrides."TR-VIRT-010".snapshot_count // 0' "${plan_path}")"
	if [[ ! "${replicas}" =~ ^[0-9]+$ || "${replicas}" -lt 1 ]]; then
		echo "error: set overrides.TR-VIRT-010.replicas (>= 1) in ${plan_path}" >&2
		exit 1
	fi
	if [[ ! "${snapshot_count}" =~ ^[0-9]+$ || "${snapshot_count}" -lt 1 ]]; then
		echo "error: set overrides.TR-VIRT-010.snapshot_count (>= 1) in ${plan_path}" >&2
		exit 1
	fi
	echo "validation passed: Level-3 no-load plan overrides are customized (no placeholders; TR-VIRT-010 params set)"
}

partner_pull_image() {
	local image="$1"
	echo "==> pulling ${image}"
	podman pull "${image}"
}

partner_podman_arch() {
	local arch="${GOARCH:-$(uname -m)}"
	case "${arch}" in
		x86_64) arch=amd64 ;;
		aarch64) arch=arm64 ;;
	esac
	echo "${arch}"
}

# Run harness in the published image. Args: image kubeconfig backends_file backend subcommand extra harness args...
partner_run_harness_container() {
	local image="$1" kubeconfig="$2" backends_file="$3" backend="$4" subcommand="$5"
	shift 5
	local arch repo_root
	arch="$(partner_podman_arch)"
	local repo_root="${REPO_ROOT:?source ci-utils.sh before partner-harness-common.sh}"
	local thresholds="${repo_root}/${PARTNER_THRESHOLDS_FILE}"
	podman run --rm --userns=keep-id --user "$(id -u):$(id -g)" --network=host \
		--platform "linux/${arch}" \
		-v "${repo_root}/catalog.json:/inputs/catalog.json:ro,Z" \
		-v "${thresholds}:/inputs/thresholds.json:ro,Z" \
		-v "${repo_root}/plans:/plans:ro,Z" \
		-v "$(realpath "${backends_file}"):/inputs/backends.yaml:ro,Z" \
		-v "$(realpath "${kubeconfig}"):/kubeconfig:ro,Z" \
		-e KUBECONFIG=/kubeconfig \
		-e KUBECTL_INSECURE_SKIP_TLS_VERIFY="${KUBECTL_INSECURE_SKIP_TLS_VERIFY:-false}" \
		"${image}" "${subcommand}" \
		--catalog /inputs/catalog.json \
		--backends /inputs/backends.yaml \
		--backend "${backend}" \
		"$@"
}
