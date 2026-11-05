#!/usr/bin/env bash
# Advance both version tracks after a successful image build+scan on main.
# Called by the CI version-bump job. Commits all four files with [skip ci],
# unless VERSION_BUMP_PR=1 is set for GitHub's pull-request flow.
#
# Preconditions (enforced):
#   - Running on main or test-ci push (is_version_bump_push)
#   - NEXT-VERSION > VERSION (both tracks)
#   - Valid semver in all files
set -euo pipefail
# shellcheck source=ci-utils.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/ci-utils.sh"
ci_log_init "advance-version"

is_version_bump_push || die "advance-version.sh runs only on main or test-ci push"

valid_semver() {
	[[ "$1" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]
}

bump_patch() {
	local v="$1"
	local major minor patch
	IFS='.' read -r major minor patch <<< "${v}"
	echo "${major}.${minor}.$((patch + 1))"
}

semver_gt() {
	local a_major a_minor a_patch b_major b_minor b_patch
	IFS='.' read -r a_major a_minor a_patch <<< "$1"
	IFS='.' read -r b_major b_minor b_patch <<< "$2"
	if [[ "${a_major}" -gt "${b_major}" ]]; then return 0; fi
	if [[ "${a_major}" -lt "${b_major}" ]]; then return 1; fi
	if [[ "${a_minor}" -gt "${b_minor}" ]]; then return 0; fi
	if [[ "${a_minor}" -lt "${b_minor}" ]]; then return 1; fi
	[[ "${a_patch}" -gt "${b_patch}" ]]
}

cur_bin="$(tr -d '[:space:]' < "${REPO_ROOT}/VERSION")"
next_bin="$(tr -d '[:space:]' < "${REPO_ROOT}/NEXT-VERSION")"
cur_img="$(tr -d '[:space:]' < "${REPO_ROOT}/IMAGE-VERSION")"
next_img="$(tr -d '[:space:]' < "${REPO_ROOT}/NEXT-IMAGE-VERSION")"

valid_semver "${cur_bin}" || die "VERSION is not valid semver: ${cur_bin}"
valid_semver "${next_bin}" || die "NEXT-VERSION is not valid semver: ${next_bin}"
valid_semver "${cur_img}" || die "IMAGE-VERSION is not valid semver: ${cur_img}"
valid_semver "${next_img}" || die "NEXT-IMAGE-VERSION is not valid semver: ${next_img}"

semver_gt "${next_bin}" "${cur_bin}" || die "NEXT-VERSION (${next_bin}) must be > VERSION (${cur_bin})"
semver_gt "${next_img}" "${cur_img}" || die "NEXT-IMAGE-VERSION (${next_img}) must be > IMAGE-VERSION (${cur_img})"

echo "advancing binary: VERSION ${cur_bin} -> ${next_bin}, NEXT-VERSION -> $(bump_patch "${next_bin}")"
echo "advancing image:  IMAGE-VERSION ${cur_img} -> ${next_img}, NEXT-IMAGE-VERSION -> $(bump_patch "${next_img}")"

echo "${next_bin}" > "${REPO_ROOT}/VERSION"
echo "$(bump_patch "${next_bin}")" > "${REPO_ROOT}/NEXT-VERSION"
echo "${next_img}" > "${REPO_ROOT}/IMAGE-VERSION"
echo "$(bump_patch "${next_img}")" > "${REPO_ROOT}/NEXT-IMAGE-VERSION"

export ALLOW_VERSION_CHANGE=1
if [[ -n "${GITHUB_ACTIONS:-}" ]]; then
	git -C "${REPO_ROOT}" config user.name "github-actions[bot]"
	git -C "${REPO_ROOT}" config user.email "41898282+github-actions[bot]@users.noreply.github.com"
fi
git -C "${REPO_ROOT}" add VERSION NEXT-VERSION IMAGE-VERSION NEXT-IMAGE-VERSION

if [[ "${VERSION_BUMP_PR:-}" == "1" ]]; then
	echo "staged version bump for pull request"
else
	git -C "${REPO_ROOT}" commit -m "chore: advance binary ${next_bin} image ${next_img} [skip ci]"

	if [[ -n "${GITHUB_ACTIONS:-}" ]]; then
		git -C "${REPO_ROOT}" push
	else
		echo "note: committed locally but did not push (run in GitHub Actions to push)"
	fi
fi
echo "advance-version ok"
