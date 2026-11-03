#!/usr/bin/env bash
# Stage ci/config/smoke into work/ for a harness smoke run (kubeconfig is not copied).
set -euo pipefail

_run_harness_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=../ci-utils.sh
source "${_run_harness_dir}/../ci-utils.sh"

smoke_config="$(smoke_config_dir)"
work="${REPO_ROOT}/work"

echo "==> prepare-smoke-env: staging ${smoke_config} -> ${work}/"
acquire_smoke_run_lock_for_hygiene
require_smoke_bundle_file backends.local.yaml

mkdir -p "${work}"
if [[ -s "${work}/backends.local.yaml" ]]; then
	echo "prepare-smoke-env: preserving existing ${work}/backends.local.yaml"
else
	cp -f "${smoke_config}/backends.local.yaml" "${work}/backends.local.yaml"
fi

stage_smoke_work_config

default_plan="${REPO_ROOT}/plans/harness-smoke-plan.yaml"
[[ -s "${default_plan}" ]] || die "missing repo smoke plan: ${default_plan}"
if [[ -s "${work}/container-smoke.yaml" ]]; then
	echo "prepare-smoke-env: preserving existing ${work}/container-smoke.yaml (Quay path only; live smoke uses plans/harness-smoke-plan.yaml)"
else
	cp -f "${default_plan}" "${work}/container-smoke.yaml"
fi

echo "prepare-smoke-env: staged catalog.json; thresholds under work/harness-run-thresholds.json (harness --thresholds path)"
if [[ ! -s "${work}/kubeconfig" ]]; then
	echo "prepare-smoke-env: note: ${work}/kubeconfig is missing — copy your cluster kubeconfig before running smoke"
else
	echo "prepare-smoke-env: found ${work}/kubeconfig"
fi
echo "prepare-smoke-env: ok"
