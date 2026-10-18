#!/usr/bin/env bash
# Trusted CI entry for live-cluster smoke on secret-bearing runners.
# Installed from the default branch only; PR code is read from LIVE_SMOKE_SRC_DIR.
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
trusted_root="$(cd "${script_dir}/../.." && pwd)"
# shellcheck source=ci-utils.sh
source "${script_dir}/ci-utils.sh"
ci_log_init "live-cluster-smoke"

: "${LIVE_SMOKE_SRC_DIR:?LIVE_SMOKE_SRC_DIR must point at the extracted PR artifact}"
: "${LIVE_SMOKE_HEAD_SHA:?LIVE_SMOKE_HEAD_SHA is required}"
: "${LIVE_SMOKE_PR_NUMBER:?LIVE_SMOKE_PR_NUMBER is required}"

src_root="$(cd "${LIVE_SMOKE_SRC_DIR}" && pwd)"
[[ -f "${src_root}/plans/harness-smoke-plan.yaml" ]] || die "PR source missing plans/harness-smoke-plan.yaml"

echo "live-cluster-smoke: pr=${LIVE_SMOKE_PR_NUMBER} head_sha=${LIVE_SMOKE_HEAD_SHA}"
echo "trusted_runner_root=${trusted_root} pr_source=${src_root}"

# TODO(ECOPROJECT-5488): materialize secrets, acquire/release lease, load image, run harness from src_root.
echo "note: full live-cluster smoke (lease, secrets, podman run) is not implemented yet"
exit 0
