#!/usr/bin/env bash
# Lint YAML (plans, backends example, CI wrappers, CodeRabbit).
# Used by GitHub Actions, make lint, and the pre-commit hook.
set -euo pipefail
# shellcheck source=ci-utils.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/ci-utils.sh"
if [[ -n "${PRE_COMMIT:-}" ]]; then
	ci_log_init "pre-commit-yaml"
else
	ci_log_init "lint-yaml"
fi
require_cmd yamllint
yamllint -c "${CI_CONFIG_DIR}/yamllint.yaml" .
"${CI_SCRIPTS_DIR}/layout-check.sh"
echo "yaml lint ok"
