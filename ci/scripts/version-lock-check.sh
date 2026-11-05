#!/usr/bin/env bash
# Pre-commit hook: block staged changes to version files unless overridden.
set -euo pipefail
if [[ "${ALLOW_VERSION_CHANGE:-}" == "1" ]] || [[ -n "${GITHUB_ACTIONS:-}" ]]; then
	exit 0
fi
echo "error: version files are locked. Set ALLOW_VERSION_CHANGE=1 to commit changes."
echo "  ALLOW_VERSION_CHANGE=1 git add VERSION NEXT-VERSION IMAGE-VERSION NEXT-IMAGE-VERSION"
echo "  ALLOW_VERSION_CHANGE=1 git commit -m \"chore: set version\""
exit 1
