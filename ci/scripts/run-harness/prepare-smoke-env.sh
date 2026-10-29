#!/usr/bin/env bash
# Stage ci/config/smoke into work/ for a harness smoke run (kubeconfig is not copied).
set -euo pipefail

_run_harness_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=../ci-utils.sh
source "${_run_harness_dir}/../ci-utils.sh"

smoke_config="${CI_CONFIG_DIR}/smoke"
work="${REPO_ROOT}/work"

require_smoke_bundle_file() {
	local name="$1"
	[[ -f "${smoke_config}/${name}" ]] || die "missing smoke bundle file: ${smoke_config}/${name}"
}

echo "==> prepare-smoke-env: staging ${smoke_config} -> ${work}/"
acquire_smoke_run_lock_for_hygiene
require_smoke_bundle_file backends.local.yaml
require_smoke_bundle_file thresholds-smoke.json
require_smoke_bundle_file thresholds.json
[[ -f "${REPO_ROOT}/catalog.json" ]] || die "missing repo catalog.json"

mkdir -p "${work}"
if [[ -s "${work}/backends.local.yaml" ]]; then
	echo "prepare-smoke-env: preserving existing ${work}/backends.local.yaml"
else
	cp -f "${smoke_config}/backends.local.yaml" "${work}/backends.local.yaml"
fi
cp -f "${smoke_config}/thresholds-smoke.json" "${work}/harness-run-thresholds.json"
cp -f "${smoke_config}/thresholds.json" "${work}/thresholds.json"
cp -f "${REPO_ROOT}/catalog.json" "${work}/catalog.json"

default_plan="${REPO_ROOT}/plans/harness-smoke-plan.yaml"
[[ -s "${default_plan}" ]] || die "missing repo smoke plan: ${default_plan}"
if [[ -s "${work}/container-smoke.yaml" ]]; then
	echo "prepare-smoke-env: preserving existing ${work}/container-smoke.yaml"
else
	cp -f "${default_plan}" "${work}/container-smoke.yaml"
fi

echo "prepare-smoke-env: staged backends, thresholds, catalog, and container-smoke plan under ${work}/"
if [[ ! -s "${work}/kubeconfig" ]]; then
	echo "prepare-smoke-env: note: ${work}/kubeconfig is missing — copy your cluster kubeconfig before running smoke"
else
	echo "prepare-smoke-env: found ${work}/kubeconfig"
fi
echo "prepare-smoke-env: ok"
