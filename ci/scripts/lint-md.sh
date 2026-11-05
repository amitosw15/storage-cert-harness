#!/usr/bin/env bash
# Lint Markdown (docs, ADRs, README). Mermaid fences are allowed (MD013 off).
# Used by GitHub Actions, make lint, and the pre-commit hook.
set -euo pipefail
# shellcheck source=ci-utils.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/ci-utils.sh"
if [[ -n "${PRE_COMMIT:-}" ]]; then
	ci_log_init "pre-commit-lint-md"
else
	ci_log_init "lint-md"
fi
if command -v markdownlint-cli2 >/dev/null 2>&1; then
	markdownlint-cli2 --config "${CI_CONFIG_DIR}/markdownlint-cli2.jsonc"
elif command -v markdownlint >/dev/null 2>&1; then
	markdownlint -c "${CI_CONFIG_DIR}/markdownlint.yaml" .
else
	die "required command not found: markdownlint-cli2 (or markdownlint)"
fi
echo "markdown lint ok"
