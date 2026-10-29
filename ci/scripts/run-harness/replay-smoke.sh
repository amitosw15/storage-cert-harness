#!/usr/bin/env bash
# Offline CLI smoke: example tool + fake thresholds. No cluster.
set -euo pipefail
_run_harness_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=../ci-utils.sh
source "${_run_harness_dir}/../ci-utils.sh"
ci_log_init "replay-smoke"

"${CI_SCRIPTS_DIR}/build.sh"
bin="${REPO_ROOT}/bin/harness"
tmp="$(mktemp -d "${TMPDIR:-/tmp}/harness-replay.XXXXXX")"
trap 'rm -rf "${tmp}"' EXIT

"${bin}" validate \
	--catalog "${REPO_ROOT}/examples/catalog.example.json" \
	--thresholds "${REPO_ROOT}/thresholds.example.json"

"${bin}" run \
	--catalog "${REPO_ROOT}/examples/catalog.example.json" \
	--thresholds "${REPO_ROOT}/thresholds.example.json" \
	--plan "${REPO_ROOT}/plans/example-smoke.yaml" \
	--output "${tmp}"

[[ -f "${tmp}/report.json" ]] || die "replay-smoke did not write report.json"
[[ -f "${tmp}/report.md" ]] || die "replay-smoke did not write report.md"
echo "replay-smoke ok (reports in temp dir, not committed)"
