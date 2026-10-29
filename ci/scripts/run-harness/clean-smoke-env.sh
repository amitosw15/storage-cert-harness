#!/usr/bin/env bash
set -euo pipefail

_run_harness_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=../ci-utils.sh
source "${_run_harness_dir}/../ci-utils.sh"

echo "==> clean-smoke-env"
acquire_smoke_run_lock_for_hygiene
clean_harness_smoke_artifacts
clean_smoke_run_logs

work="${REPO_ROOT}/work"
for f in harness-run-thresholds.json thresholds.json catalog.json; do
	path="${work}/${f}"
	if [[ -f "${path}" ]]; then
		echo "clean-smoke-env: removing staged ${path}"
		rm -f -- "${path}"
	fi
done

echo "clean-smoke-env: ok (kubeconfig, backends.local.yaml, local-image-ref preserved)"
