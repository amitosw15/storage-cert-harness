#!/usr/bin/env bash
set -euo pipefail

SMOKE_CLUSTER_LEASE_EXIT_BUSY="${SMOKE_CLUSTER_LEASE_EXIT_BUSY:-2}"

SMOKE_CLUSTER_LEASE_NAMESPACE="${SMOKE_CLUSTER_LEASE_NAMESPACE:-storage-cert-ci}"
SMOKE_CLUSTER_LEASE_NAME="${SMOKE_CLUSTER_LEASE_NAME:-storage-cert-harness-smoke}"
SMOKE_CLUSTER_LEASE_LABEL_KEY="${SMOKE_CLUSTER_LEASE_LABEL_KEY:-storage-cert-harness/ci-smoke}"
SMOKE_CLUSTER_LEASE_ANNOT_PREFIX="${SMOKE_CLUSTER_LEASE_ANNOT_PREFIX:-storage-cert-harness/}"

SMOKE_CLUSTER_LEASE_DURATION_SECONDS="${SMOKE_CLUSTER_LEASE_DURATION_SECONDS:-15300}"
SMOKE_CLUSTER_LEASE_RENEW_INTERVAL_SECONDS="${SMOKE_CLUSTER_LEASE_RENEW_INTERVAL_SECONDS:-300}"

SMOKE_CLUSTER_LEASE_HELD="${SMOKE_CLUSTER_LEASE_HELD:-0}"
SMOKE_CLUSTER_LEASE_HOLDER_IDENTITY="${SMOKE_CLUSTER_LEASE_HOLDER_IDENTITY:-}"
_smoke_cluster_lease_renew_pid=""

smoke_cluster_lease_die() {
	echo "error: $*" >&2
	exit 1
}

smoke_cluster_lease_busy() {
	echo "error: cluster smoke lease held by run ${1:-<unknown>}" >&2
	smoke_cluster_lease_log_holder_annotations || true
	exit "${SMOKE_CLUSTER_LEASE_EXIT_BUSY}"
}

smoke_cluster_lease_oc_args() {
	local -n _out=$1
	_out=()
	if [[ -n "${SMOKE_CLUSTER_LEASE_KUBECONFIG:-}" ]]; then
		_out+=(--kubeconfig "${SMOKE_CLUSTER_LEASE_KUBECONFIG}")
	elif [[ -n "${KUBECONFIG:-}" ]]; then
		_out+=(--kubeconfig "${KUBECONFIG}")
	fi
	if [[ "${KUBECTL_INSECURE_SKIP_TLS_VERIFY:-false}" == "true" ]]; then
		_out+=(--insecure-skip-tls-verify=true)
	fi
}

smoke_cluster_lease_run_id() {
	if [[ -n "${GITHUB_RUN_ID:-}" ]]; then
		echo "${GITHUB_RUN_ID}"
		return
	fi
	if [[ -n "${CI_PIPELINE_ID:-}" ]]; then
		echo "${CI_PIPELINE_ID}"
		return
	fi
	if [[ -n "${HARNESS_SMOKE_LEASE_RUN_ID:-}" ]]; then
		echo "${HARNESS_SMOKE_LEASE_RUN_ID}"
		return
	fi
	echo "local-$(whoami)-$(date +%s)-$$-${RANDOM}"
}

smoke_cluster_lease_rfc3339_now() {
	date -u +%Y-%m-%dT%H:%M:%SZ
}

smoke_cluster_lease_api_time_now() {
	date -u +%Y-%m-%dT%H:%M:%S.000000Z
}

smoke_cluster_lease_template_path() {
	echo "${REPO_ROOT}/ci/config/smoke/cluster-smoke-lease.yaml.template"
}

smoke_cluster_lease_render_template() {
	local run_id="$1"
	local template now
	template="$(smoke_cluster_lease_template_path)"
	[[ -f "${template}" ]] || smoke_cluster_lease_die "smoke lease: missing template ${template}"
	now="$(smoke_cluster_lease_api_time_now)"
	sed \
		-e "s|@LEASE_NAME@|${SMOKE_CLUSTER_LEASE_NAME}|g" \
		-e "s|@LEASE_NAMESPACE@|${SMOKE_CLUSTER_LEASE_NAMESPACE}|g" \
		-e "s|@HOLDER_IDENTITY@|${run_id}|g" \
		-e "s|@LEASE_DURATION@|${SMOKE_CLUSTER_LEASE_DURATION_SECONDS}|g" \
		-e "s|@ACQUIRE_TIME@|${now}|g" \
		-e "s|@RENEW_TIME@|${now}|g" \
		"${template}"
}

smoke_cluster_lease_api_server_host() {
	local oc_args=()
	smoke_cluster_lease_oc_args oc_args
	local server
	server="$(oc "${oc_args[@]}" whoami --show-server 2>/dev/null || true)"
	server="${server#https://}"
	server="${server#http://}"
	server="${server%%/*}"
	echo "${server}"
}

smoke_cluster_lease_workflow_url() {
	if [[ -n "${GITHUB_SERVER_URL:-}" && -n "${GITHUB_REPOSITORY:-}" && -n "${GITHUB_RUN_ID:-}" ]]; then
		echo "${GITHUB_SERVER_URL}/${GITHUB_REPOSITORY}/actions/runs/${GITHUB_RUN_ID}"
		return
	fi
	if [[ -n "${CI_PIPELINE_URL:-}" ]]; then
		echo "${CI_PIPELINE_URL}"
		return
	fi
	echo ""
}

smoke_cluster_lease_runner_host() {
	local host=""
	host="$(hostname -f 2>/dev/null || true)"
	if [[ -z "${host}" ]]; then
		host="$(hostname 2>/dev/null || true)"
	fi
	echo "${host}"
}

smoke_cluster_lease_collect_annotations() {
	local run_id="$1"
	local plan_name="${2:-harness-smoke}"
	local plan_file="${3:-plans/harness-smoke-plan.yaml}"
	local -n _anns=$4
	_anns=()
	_anns+=("${SMOKE_CLUSTER_LEASE_ANNOT_PREFIX}run-id=${run_id}")
	_anns+=("${SMOKE_CLUSTER_LEASE_ANNOT_PREFIX}plan=${plan_name}")
	_anns+=("${SMOKE_CLUSTER_LEASE_ANNOT_PREFIX}plan-file=${plan_file}")
	_anns+=("${SMOKE_CLUSTER_LEASE_ANNOT_PREFIX}git-sha=$(git_sha)")
	_anns+=("${SMOKE_CLUSTER_LEASE_ANNOT_PREFIX}started-at=$(smoke_cluster_lease_rfc3339_now)")

	local pr="${LIVE_SMOKE_PR_NUMBER:-${GITHUB_PR_NUMBER:-${CI_MERGE_REQUEST_IID:-}}}"
	if [[ -n "${pr}" ]]; then
		_anns+=("${SMOKE_CLUSTER_LEASE_ANNOT_PREFIX}pr-number=${pr}")
	fi

	local wf_url
	wf_url="$(smoke_cluster_lease_workflow_url)"
	if [[ -n "${wf_url}" ]]; then
		_anns+=("${SMOKE_CLUSTER_LEASE_ANNOT_PREFIX}workflow-url=${wf_url}")
	fi

	local runner
	runner="$(smoke_cluster_lease_runner_host)"
	if [[ -n "${runner}" ]]; then
		_anns+=("${SMOKE_CLUSTER_LEASE_ANNOT_PREFIX}runner=${runner}")
		_anns+=("${SMOKE_CLUSTER_LEASE_ANNOT_PREFIX}bootstrap-host=${runner}")
	fi

	local api_host
	api_host="$(smoke_cluster_lease_api_server_host)"
	if [[ -n "${api_host}" ]]; then
		_anns+=("${SMOKE_CLUSTER_LEASE_ANNOT_PREFIX}api-server=${api_host}")
	fi
}

smoke_cluster_lease_ensure_namespace() {
	local oc_args=()
	smoke_cluster_lease_oc_args oc_args
	if oc "${oc_args[@]}" get namespace "${SMOKE_CLUSTER_LEASE_NAMESPACE}" >/dev/null 2>&1; then
		return 0
	fi
	local err=""
	local status=0
	set +e
	err="$(oc "${oc_args[@]}" create namespace "${SMOKE_CLUSTER_LEASE_NAMESPACE}" 2>&1)"
	status=$?
	set -e
	if [[ "${status}" -eq 0 ]]; then
		return 0
	fi
	if oc "${oc_args[@]}" get namespace "${SMOKE_CLUSTER_LEASE_NAMESPACE}" >/dev/null 2>&1; then
		return 0
	fi
	smoke_cluster_lease_die "smoke lease: namespace ${SMOKE_CLUSTER_LEASE_NAMESPACE} is missing and could not be created (${err}). Create it once with cluster admin or grant namespace create."
}

smoke_cluster_lease_exists() {
	local oc_args=()
	smoke_cluster_lease_oc_args oc_args
	oc "${oc_args[@]}" get lease "${SMOKE_CLUSTER_LEASE_NAME}" \
		-n "${SMOKE_CLUSTER_LEASE_NAMESPACE}" >/dev/null 2>&1
}

smoke_cluster_lease_field() {
	local jsonpath="$1"
	local oc_args=()
	smoke_cluster_lease_oc_args oc_args
	oc "${oc_args[@]}" get lease "${SMOKE_CLUSTER_LEASE_NAME}" \
		-n "${SMOKE_CLUSTER_LEASE_NAMESPACE}" \
		-o "jsonpath=${jsonpath}" 2>/dev/null || true
}

smoke_cluster_lease_holder() {
	smoke_cluster_lease_field '{.spec.holderIdentity}'
}

smoke_cluster_lease_require_jq() {
	command -v jq >/dev/null 2>&1 ||
		smoke_cluster_lease_die "jq is required for atomic cluster smoke lease updates"
}

smoke_cluster_lease_fetch_json() {
	local oc_args=()
	smoke_cluster_lease_oc_args oc_args
	oc "${oc_args[@]}" get lease "${SMOKE_CLUSTER_LEASE_NAME}" \
		-n "${SMOKE_CLUSTER_LEASE_NAMESPACE}" -o json
}

smoke_cluster_lease_verify_holder_json() {
	local json="$1"
	local holder run_id
	run_id="${SMOKE_CLUSTER_LEASE_HOLDER_IDENTITY}"
	holder="$(jq -r '.spec.holderIdentity // ""' <<<"${json}")"
	[[ -n "${holder}" ]] || {
		echo "smoke lease: object has no holderIdentity" >&2
		return 1
	}
	if [[ "${holder}" != "${run_id}" ]]; then
		echo "smoke lease: holder is ${holder} (this run=${run_id})" >&2
		return 1
	fi
	return 0
}

smoke_cluster_lease_is_expired() {
	local holder renew duration renew_epoch now_epoch
	holder="$(smoke_cluster_lease_holder)"
	[[ -n "${holder}" ]] || return 0
	renew="$(smoke_cluster_lease_field '{.spec.renewTime}')"
	duration="$(smoke_cluster_lease_field '{.spec.leaseDurationSeconds}')"
	[[ -n "${renew}" && -n "${duration}" ]] || return 0
	renew_epoch="$(date -u -d "${renew}" +%s 2>/dev/null || echo 0)"
	now_epoch="$(date -u +%s)"
	(( now_epoch > renew_epoch + duration ))
}

smoke_cluster_lease_abort_partial_acquire() {
	if [[ "${SMOKE_CLUSTER_LEASE_HELD:-0}" == "1" ]]; then
		smoke_cluster_lease_release || true
	fi
}

smoke_cluster_lease_mark_held_and_refresh() {
	local run_id="$1"
	local plan_name="${2:-harness-smoke}"
	local plan_file="${3:-plans/harness-smoke-plan.yaml}"
	export SMOKE_CLUSTER_LEASE_HELD=1
	if ! smoke_cluster_lease_renew_heartbeat; then
		return 1
	fi
	if ! smoke_cluster_lease_apply_metadata "${run_id}" "${plan_name}" "${plan_file}"; then
		return 1
	fi
	return 0
}

smoke_cluster_lease_log_holder_annotations() {
	local oc_args=()
	smoke_cluster_lease_oc_args oc_args
	if ! oc "${oc_args[@]}" get lease "${SMOKE_CLUSTER_LEASE_NAME}" \
		-n "${SMOKE_CLUSTER_LEASE_NAMESPACE}" >/dev/null 2>&1; then
		return 0
	fi
	echo "smoke lease: current lease object annotations:" >&2
	oc "${oc_args[@]}" get lease "${SMOKE_CLUSTER_LEASE_NAME}" \
		-n "${SMOKE_CLUSTER_LEASE_NAMESPACE}" \
		-o 'go-template={{range $k, $v := .metadata.annotations}}{{$k}}={{$v}}{{"\n"}}{{end}}' 2>/dev/null |
		while IFS= read -r line; do
			[[ -n "${line}" ]] && echo "  ${line}" >&2
		done || true
}

smoke_cluster_lease_apply_metadata() {
	local run_id="$1"
	local plan_name="${2:-harness-smoke}"
	local plan_file="${3:-plans/harness-smoke-plan.yaml}"
	local oc_args=()
	smoke_cluster_lease_oc_args oc_args
	local json
	smoke_cluster_lease_require_jq
	json="$(smoke_cluster_lease_fetch_json)" || return 1
	smoke_cluster_lease_verify_holder_json "${json}" || return 1
	local anns=()
	smoke_cluster_lease_collect_annotations "${run_id}" "${plan_name}" "${plan_file}" anns

	local ann_obj updated err st
	ann_obj="$(printf '%s\n' "${anns[@]}" | jq -R -s '
		split("\n") | map(select(length > 0)) |
		map(capture("^(?<k>[^=]+)=(?<v>.*)$")) |
		map({(.k): .v}) | add // {}')"
	updated="$(jq \
		--arg run_id "${run_id}" \
		--arg label_key "${SMOKE_CLUSTER_LEASE_LABEL_KEY}" \
		--argjson new_anns "${ann_obj}" \
		'.metadata.labels["app.kubernetes.io/managed-by"] = "storage-cert-harness" |
		.metadata.labels[$label_key] = $run_id |
		.metadata.annotations = ((.metadata.annotations // {}) + $new_anns)' <<<"${json}")"
	set +e
	err="$(printf '%s' "${updated}" | oc "${oc_args[@]}" replace -f - 2>&1)"
	st=$?
	set -e
	if [[ "${st}" -ne 0 ]]; then
		echo "smoke lease metadata: replace failed (holder or resourceVersion changed?): ${err}" >&2
		return 1
	fi
	return 0
}

smoke_cluster_lease_renew_heartbeat() {
	[[ "${SMOKE_CLUSTER_LEASE_HELD:-0}" == "1" ]] || return 0
	local oc_args=()
	smoke_cluster_lease_oc_args oc_args
	local json now updated err st
	smoke_cluster_lease_require_jq
	now="$(smoke_cluster_lease_api_time_now)"
	set +e
	json="$(smoke_cluster_lease_fetch_json 2>&1)"
	st=$?
	set -e
	if [[ "${st}" -ne 0 ]]; then
		echo "smoke lease renew: fetch failed: ${json}" >&2
		return 1
	fi
	smoke_cluster_lease_verify_holder_json "${json}" || return 1
	updated="$(jq --arg now "${now}" --argjson dur "${SMOKE_CLUSTER_LEASE_DURATION_SECONDS}" \
		'.spec.renewTime = $now | .spec.leaseDurationSeconds = $dur' <<<"${json}")"
	set +e
	err="$(printf '%s' "${updated}" | oc "${oc_args[@]}" replace -f - 2>&1)"
	st=$?
	set -e
	if [[ "${st}" -ne 0 ]]; then
		echo "smoke lease renew: replace failed (resourceVersion conflict?): ${err}" >&2
		return 1
	fi
	return 0
}

smoke_cluster_lease_delete_lease() {
	local oc_args=()
	smoke_cluster_lease_oc_args oc_args
	oc "${oc_args[@]}" delete lease "${SMOKE_CLUSTER_LEASE_NAME}" \
		-n "${SMOKE_CLUSTER_LEASE_NAMESPACE}" \
		--ignore-not-found >/dev/null
}

smoke_cluster_lease_create() {
	local run_id="$1"
	local plan_name="${2:-harness-smoke}"
	local plan_file="${3:-plans/harness-smoke-plan.yaml}"
	local oc_args=()
	smoke_cluster_lease_oc_args oc_args

	local manifest create_err holder
	manifest="$(smoke_cluster_lease_render_template "${run_id}")"
	local create_status=0
	set +e
	create_err="$(printf '%s\n' "${manifest}" | oc "${oc_args[@]}" create -f - 2>&1)"
	create_status=$?
	set -e
	if [[ "${create_status}" -eq 0 ]]; then
		export SMOKE_CLUSTER_LEASE_HELD=1
		if ! smoke_cluster_lease_mark_held_and_refresh "${run_id}" "${plan_name}" "${plan_file}"; then
			smoke_cluster_lease_abort_partial_acquire
			smoke_cluster_lease_die "smoke lease: post-create refresh failed"
		fi
		return 0
	fi

	if smoke_cluster_lease_exists; then
		holder="$(smoke_cluster_lease_holder)"
		if [[ -z "${holder}" ]] || smoke_cluster_lease_is_expired; then
			echo "smoke lease: replacing expired or empty lease (prior holder=${holder:-<empty>})"
			smoke_cluster_lease_delete_lease
			manifest="$(smoke_cluster_lease_render_template "${run_id}")"
			set +e
			create_err="$(printf '%s\n' "${manifest}" | oc "${oc_args[@]}" create -f - 2>&1)"
			create_status=$?
			set -e
			if [[ "${create_status}" -ne 0 ]]; then
				smoke_cluster_lease_die "smoke lease: recreate after expiry failed: ${create_err}"
			fi
			export SMOKE_CLUSTER_LEASE_HELD=1
			if ! smoke_cluster_lease_mark_held_and_refresh "${run_id}" "${plan_name}" "${plan_file}"; then
				smoke_cluster_lease_abort_partial_acquire
				smoke_cluster_lease_die "smoke lease: post-recreate refresh failed"
			fi
			return 0
		fi
		if [[ "${holder}" != "${run_id}" ]]; then
			smoke_cluster_lease_busy "${holder}"
		fi
		export SMOKE_CLUSTER_LEASE_HELD=1
		if ! smoke_cluster_lease_mark_held_and_refresh "${run_id}" "${plan_name}" "${plan_file}"; then
			smoke_cluster_lease_abort_partial_acquire
			smoke_cluster_lease_die "smoke lease: refresh failed for existing lease"
		fi
		return 0
	fi

	if [[ -n "${create_err}" ]]; then
		smoke_cluster_lease_die "smoke lease: create failed: ${create_err}"
	fi
	smoke_cluster_lease_die "smoke lease: create failed (unknown error)"
}

smoke_cluster_lease_acquire() {
	local plan_name="${1:-harness-smoke}"
	local plan_file="${2:-plans/harness-smoke-plan.yaml}"

	if [[ "${HARNESS_SMOKE_SKIP_CLUSTER_LEASE:-false}" == "true" ]]; then
		echo "smoke lease: skipped (HARNESS_SMOKE_SKIP_CLUSTER_LEASE=true)"
		return 0
	fi

	command -v oc >/dev/null 2>&1 || smoke_cluster_lease_die "oc is required for cluster smoke lease"

	local run_id holder
	run_id="$(smoke_cluster_lease_run_id)"
	export SMOKE_CLUSTER_LEASE_HOLDER_IDENTITY="${run_id}"

	echo "smoke lease: acquiring ${SMOKE_CLUSTER_LEASE_NAME} in ${SMOKE_CLUSTER_LEASE_NAMESPACE} (holder=${run_id})"
	smoke_cluster_lease_ensure_namespace

	if smoke_cluster_lease_exists; then
		holder="$(smoke_cluster_lease_holder)"
		if [[ -n "${holder}" && "${holder}" != "${run_id}" ]]; then
			if smoke_cluster_lease_is_expired; then
				echo "smoke lease: holder ${holder} expired; replacing lease"
				smoke_cluster_lease_delete_lease
				smoke_cluster_lease_create "${run_id}" "${plan_name}" "${plan_file}"
			else
				smoke_cluster_lease_busy "${holder}"
			fi
		else
			export SMOKE_CLUSTER_LEASE_HELD=1
			if ! smoke_cluster_lease_mark_held_and_refresh "${run_id}" "${plan_name}" "${plan_file}"; then
				smoke_cluster_lease_abort_partial_acquire
				smoke_cluster_lease_die "smoke lease: refresh failed"
			fi
		fi
	else
		smoke_cluster_lease_create "${run_id}" "${plan_name}" "${plan_file}"
	fi

	echo "smoke lease: acquired (renew every ${SMOKE_CLUSTER_LEASE_RENEW_INTERVAL_SECONDS}s)"
}

smoke_cluster_lease_release() {
	if [[ "${SMOKE_CLUSTER_LEASE_HELD:-0}" != "1" ]]; then
		return 0
	fi

	local oc_args=()
	smoke_cluster_lease_oc_args oc_args
	local json rv err st
	smoke_cluster_lease_require_jq
	set +e
	json="$(smoke_cluster_lease_fetch_json 2>&1)"
	st=$?
	set -e
	if [[ "${st}" -ne 0 ]]; then
		echo "smoke lease: release skipped; fetch failed: ${json}" >&2
		export SMOKE_CLUSTER_LEASE_HELD=0
		return 0
	fi
	if ! smoke_cluster_lease_verify_holder_json "${json}"; then
		export SMOKE_CLUSTER_LEASE_HELD=0
		return 0
	fi
	rv="$(jq -r '.metadata.resourceVersion // ""' <<<"${json}")"
	[[ -n "${rv}" ]] || {
		echo "smoke lease: release skipped; empty resourceVersion" >&2
		export SMOKE_CLUSTER_LEASE_HELD=0
		return 0
	}

	echo "smoke lease: releasing ${SMOKE_CLUSTER_LEASE_NAME} (resourceVersion=${rv})"
	set +e
	if oc delete --help 2>&1 | grep -q 'resource-version'; then
		err="$(oc "${oc_args[@]}" delete lease "${SMOKE_CLUSTER_LEASE_NAME}" \
			-n "${SMOKE_CLUSTER_LEASE_NAMESPACE}" \
			--resource-version="${rv}" 2>&1)"
		st=$?
	else
		err="$(oc "${oc_args[@]}" patch lease "${SMOKE_CLUSTER_LEASE_NAME}" \
			-n "${SMOKE_CLUSTER_LEASE_NAMESPACE}" \
			--type=json \
			-p="[{\"op\":\"test\",\"path\":\"/metadata/resourceVersion\",\"value\":\"${rv}\"},{\"op\":\"test\",\"path\":\"/spec/holderIdentity\",\"value\":\"${SMOKE_CLUSTER_LEASE_HOLDER_IDENTITY}\"}]" 2>&1)"
		st=$?
		if [[ "${st}" -eq 0 ]]; then
			err="$(oc "${oc_args[@]}" delete lease "${SMOKE_CLUSTER_LEASE_NAME}" \
				-n "${SMOKE_CLUSTER_LEASE_NAMESPACE}" 2>&1)"
			st=$?
		fi
	fi
	set -e
	if [[ "${st}" -ne 0 ]]; then
		echo "smoke lease: warning: delete failed (object may have changed): ${err}" >&2
	fi
	export SMOKE_CLUSTER_LEASE_HELD=0
}

smoke_cluster_lease_start_renewal_loop() {
	if [[ "${SMOKE_CLUSTER_LEASE_HELD:-0}" != "1" ]]; then
		return 0
	fi
	if [[ -n "${_smoke_cluster_lease_renew_pid}" ]] && kill -0 "${_smoke_cluster_lease_renew_pid}" 2>/dev/null; then
		return 0
	fi

	local guard="${REPO_ROOT:-.}/work/.smoke-cluster-lease-renew-$$"
	mkdir -p "$(dirname "${guard}")"
	: >"${guard}"

	set -m
	(
		trap 'rm -f "${guard}"; exit 0' TERM INT
		while [[ -f "${guard}" ]]; do
			if ! read -r -t "${SMOKE_CLUSTER_LEASE_RENEW_INTERVAL_SECONDS}" _ </dev/null; then
				:
			fi
			[[ -f "${guard}" ]] || exit 0
			if ! smoke_cluster_lease_renew_heartbeat; then
				echo "smoke lease: heartbeat renew failed (will retry on next interval)" >&2
			fi
		done
	) &
	_smoke_cluster_lease_renew_pid=$!
	set +m
	echo "smoke lease: background renewal pid=${_smoke_cluster_lease_renew_pid}"
}

smoke_cluster_lease_stop_renewal_loop() {
	local guard="${REPO_ROOT:-.}/work/.smoke-cluster-lease-renew-$$"
	rm -f "${guard}" 2>/dev/null || true
	if [[ -n "${_smoke_cluster_lease_renew_pid}" ]]; then
		kill -TERM "-${_smoke_cluster_lease_renew_pid}" 2>/dev/null ||
			kill -TERM "${_smoke_cluster_lease_renew_pid}" 2>/dev/null || true
		wait "${_smoke_cluster_lease_renew_pid}" 2>/dev/null || true
		_smoke_cluster_lease_renew_pid=""
	fi
}

smoke_cluster_lease_on_exit() {
	smoke_cluster_lease_stop_renewal_loop
	smoke_cluster_lease_release
}

smoke_cluster_lease_install_traps() {
	local after="${1:-}"
	if [[ -n "${after}" ]]; then
		# shellcheck disable=SC2064
		trap "smoke_cluster_lease_on_exit; ${after}" EXIT
		# shellcheck disable=SC2064
		trap "smoke_cluster_lease_on_exit; ${after}; exit 130" INT
		# shellcheck disable=SC2064
		trap "smoke_cluster_lease_on_exit; ${after}; exit 143" TERM
	else
		trap 'smoke_cluster_lease_on_exit' EXIT
		trap 'smoke_cluster_lease_on_exit; exit 130' INT
		trap 'smoke_cluster_lease_on_exit; exit 143' TERM
	fi
}
