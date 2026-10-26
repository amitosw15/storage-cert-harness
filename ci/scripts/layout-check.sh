#!/usr/bin/env bash
# Verify the CI reorder layout: scripts in ci/scripts/, configs in ci/config/,
# version files at root, no leftover job scripts at ci/*.sh.
# Called from lint-yaml.sh so pre-commit, make lint, and GitLab lint-yaml all cover it.
set -euo pipefail
# shellcheck source=ci-utils.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/ci-utils.sh"

errors=0
fail() {
	echo "FAIL: $*"
	errors=$((errors + 1))
}

echo "==> layout-check: ci/scripts/"
for f in ci-utils.sh build.sh ci.sh image-build.sh image-push.sh \
	image-scan-dive.sh image-scan-trivy.sh images.sh install-tools.sh \
	lint-go.sh lint-md.sh lint-yaml.sh mirror-ci-tools.sh replay-smoke.sh \
	secret-scan.sh supply-chain.sh sync-images-yml.sh unittest.sh \
	set-next-version.sh advance-version.sh layout-check.sh image-contents-check.sh \
	install-partner-tools.sh partner-host-env.sh; do
	[[ -f "${REPO_ROOT}/ci/scripts/${f}" ]] || fail "missing ci/scripts/${f}"
done

echo "==> layout-check: ci/config/"
for f in golangci.yml yamllint.yaml markdownlint-cli2.jsonc trivyignore \
	images.env images.yml secret-scan-allowlist.txt; do
	[[ -f "${REPO_ROOT}/ci/config/${f}" ]] || fail "missing ci/config/${f}"
done

echo "==> layout-check: version files"
for f in VERSION NEXT-VERSION IMAGE-VERSION NEXT-IMAGE-VERSION; do
	[[ -f "${REPO_ROOT}/${f}" ]] || fail "missing ${f}"
done

echo "==> layout-check: no leftover scripts at ci/*.sh"
leftover="$(find "${REPO_ROOT}/ci" -maxdepth 1 -name '*.sh' -type f 2>/dev/null || true)"
if [[ -n "${leftover}" ]]; then
	fail "leftover script(s) at ci/*.sh (should be in ci/scripts/):\n${leftover}"
fi

echo "==> layout-check: binary_version() and image_version()"
bv="$(binary_version 2>/dev/null || true)"
iv="$(image_version 2>/dev/null || true)"
[[ -n "${bv}" ]] || fail "binary_version() returned empty"
[[ -n "${iv}" ]] || fail "image_version() returned empty"
echo "  binary_version=${bv}  image_version=${iv}"

echo "==> layout-check: set-next-version.sh --self-test"
"${REPO_ROOT}/ci/scripts/set-next-version.sh" --self-test || fail "set-next-version.sh --self-test failed"

if [[ "${errors}" -gt 0 ]]; then
	die "layout-check found ${errors} issue(s)"
fi
echo "layout-check ok"
