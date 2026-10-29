#!/usr/bin/env bash
# Local fast path: GitLab lint + test stages, then replay-smoke.
# Does not build or scan the container. Pass --image to also build + scan sequentially.
set -euo pipefail
# shellcheck source=ci-utils.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/ci-utils.sh"
ci_log_init "ci"

with_image=0
if [[ "${1:-}" == "--image" ]]; then
	with_image=1
fi

make -C "${REPO_ROOT}" --no-print-directory lint
make -C "${REPO_ROOT}" --no-print-directory test
"${CI_SCRIPTS_DIR}/run-harness/replay-smoke.sh"

if [[ "${with_image}" -eq 1 ]]; then
	"${CI_SCRIPTS_DIR}/image-build.sh"
	"${CI_SCRIPTS_DIR}/image-contents-check.sh"
	"${CI_SCRIPTS_DIR}/image-scan-trivy.sh"
	"${CI_SCRIPTS_DIR}/image-scan-dive.sh"
fi
echo "ci ok"
