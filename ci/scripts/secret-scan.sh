#!/usr/bin/env bash
# Secret scan: run reports that could carry graded customer numbers, and
# editor/workspace env vars that look like API keys or tokens.
# Used by GitHub Actions, make test, and the pre-commit hook.
#
# SLA thresholds are public (ADR-0017): thresholds.json may be committed and
# numeric gate values in code/plans/fixtures are no longer scanned. This gate
# now covers only run reports and token-shaped credentials.
set -euo pipefail
# shellcheck source=ci-utils.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/ci-utils.sh"

errors=0

fail() {
	echo "FAIL: $*"
	errors=$((errors + 1))
}

if [[ -n "${PRE_COMMIT:-}" ]]; then
	ci_log_init "pre-commit-secret-scan"
else
	ci_log_init "secret-scan"
fi

# Two independent gates: forbidden tracked reports and token-shaped values in
# editor config. Keeping them separate makes a failure actionable.
# --- tracked forbidden filenames ---
echo "==> tracked-files  (refuse report.json / report.md)"
tracked_hits=0
while IFS= read -r f; do
	base="$(basename "${f}")"
	case "${base}" in
	report.json | report.md)
		fail "tracked harness run report: ${f}"
		echo "      why: reports can contain graded numbers; do not commit report.json / report.md"
		tracked_hits=$((tracked_hits + 1))
		;;
	esac
done < <(git ls-files)
echo "    tracked-files: ${tracked_hits} issue(s)"

# --- editor / workspace env tokens ---
echo "==> editor-env  (.vscode / *.code-workspace / .cursor / .devcontainer / .env)"
secret_name_re='(api[_-]?key|token|secret|password|passwd|credential|auth|bearer|private[_-]?key|access[_-]?key)'
secret_val_re='(glpat-|ghp_|gho_|github_pat_|sk-|AKIA|eyJ[A-Za-z0-9_-]{10,})'

scan_editor_file() {
	local f="$1"
	[[ -f "${f}" ]] || return 0
	git ls-files --error-unmatch "${f}" >/dev/null 2>&1 || return 0
	if grep -Eiq "${secret_name_re}" "${f}"; then
		# empty / placeholder values are allowed
		if grep -Ei "${secret_name_re}" "${f}" | grep -Evq '""|'"''"'|<set-locally>|changeme|YOUR_.*HERE|placeholder'; then
			if grep -Eq "${secret_val_re}" "${f}"; then
				fail "token-shaped value in editor/workspace file: ${f}"
				echo "      why: looks like glpat-/ghp_/sk-/AKIA/JWT; unset or use a placeholder"
				return
			fi
			if grep -Ei "${secret_name_re}" "${f}" | grep -Eq ':[[:space:]]*"[^"<][^"]{7,}"'; then
				fail "possible secret env assignment in ${f}"
				echo "      why: secret-looking name with a non-empty quoted value (not a known placeholder)"
			fi
		fi
	fi
	if grep -Eq "${secret_val_re}" "${f}"; then
		fail "token-shaped value in editor/workspace file: ${f}"
		echo "      why: file contains glpat-/ghp_/sk-/AKIA/JWT-shaped text"
	fi
}

while IFS= read -r f; do
	scan_editor_file "${f}"
done < <(git ls-files -- '.vscode/*.json' '*.code-workspace' '.cursor/**' '.devcontainer/**' '.env' '.env.*' ':!.env.example' || true)

if command -v gitleaks >/dev/null 2>&1; then
	echo "==> gitleaks detect --no-git --source ${REPO_ROOT}"
	gitleaks detect --no-git --source "${REPO_ROOT}" --verbose || fail "gitleaks reported findings (see gitleaks output above)"
else
	echo "==> gitleaks not installed; skipping (optional, see docs/ci.md)"
fi

if [[ "${errors}" -gt 0 ]]; then
	echo "--- secret-scan summary: ${errors} issue(s) ---"
	echo "    re-run: ./ci/secret-scan.sh"
	die "secret-scan found ${errors} issue(s)"
fi
echo "secret-scan ok"
