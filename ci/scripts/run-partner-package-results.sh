#!/usr/bin/env bash
# Archive partner harness work/ and report/ trees plus helper logs for submission.
set -euo pipefail

usage() {
	cat <<'EOF'
Usage:
  ./ci/scripts/run-partner-package-results.sh [OUTPUT_TAR]

Environment:
  PARTNER_PACKAGE_MODE   level3 | smoke | all (default: level3)
  CERT_RESULTS_ROOT      Base directory (default: certification-results)
  PARTNER_PACKAGE_LOGS   When true, include logs/run-partner-*.log (default: true)

Modes:
  level3  Package load-80 and no-load-80 only (Level 3 certification).
          Default archive: certification-results-level3.tar.gz
          Fails if either profile tree is missing.
  smoke   Package smoke/ only (partner script lab test).
          Default archive: certification-results-smoke.tar.gz
  all     Package every present profile among load-80, no-load-80, smoke.
          Default archive: certification-results-full.tar.gz

Make targets set MODE and default tarball names:
  make partner-package-results   -> level3
  make partner-smoke-package     -> smoke
EOF
}

if [[ "${1:-}" == "-h" || "${1:-}" == "--help" ]]; then
	usage
	exit 0
fi
if [[ "$#" -gt 1 ]]; then
	usage >&2
	exit 2
fi

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${repo_root}"
# shellcheck source=ci-utils.sh
source "${repo_root}/ci/scripts/ci-utils.sh"

results_root="${CERT_RESULTS_ROOT:-certification-results}"
mode="${PARTNER_PACKAGE_MODE:-level3}"

case "${mode}" in
	level3) profiles=(load-80 no-load-80) default_tar="certification-results-level3.tar.gz" ;;
	smoke) profiles=(smoke) default_tar="certification-results-smoke.tar.gz" ;;
	all) profiles=(load-80 no-load-80 smoke) default_tar="certification-results-full.tar.gz" ;;
	*)
		echo "error: invalid PARTNER_PACKAGE_MODE=${mode} (want level3, smoke, or all)" >&2
		exit 2
		;;
esac

out_tar="${1:-${default_tar}}"
[[ "${out_tar}" = /* ]] || out_tar="${repo_root}/${out_tar}"

paths=()
missing=()
for profile in "${profiles[@]}"; do
	for sub in work report; do
		dir="${results_root}/${profile}/${sub}"
		if [[ -d "${dir}" ]]; then
			paths+=("${results_root}/${profile}/${sub}")
		else
			missing+=("${dir}")
		fi
	done
done

if [[ "${#missing[@]}" -gt 0 ]]; then
	if [[ "${mode}" == "all" ]]; then
		for dir in "${missing[@]}"; do
			echo "note: ${dir} not present (skipped)" >&2
		done
	elif [[ "${#paths[@]}" -eq 0 ]]; then
		echo "error: no ${mode} results under ${results_root}/" >&2
		exit 1
	else
		echo "error: incomplete ${mode} results; missing:" >&2
		for dir in "${missing[@]}"; do
			echo "  - ${dir}" >&2
		done
		case "${mode}" in
			level3)
				echo "hint: run make partner-cert-load-80 and make partner-cert-no-load-80 first" >&2
				;;
			smoke)
				echo "hint: run make partner-smoke-run first" >&2
				;;
		esac
		exit 1
	fi
fi

if [[ "${PARTNER_PACKAGE_LOGS:-true}" == "true" ]]; then
	partner_logs=()
	if compgen -G "${LOG_DIR}/run-partner-"*.log >/dev/null 2>&1; then
		while IFS= read -r log; do
			partner_logs+=("${log}")
		done < <(find "${LOG_DIR}" -maxdepth 1 -name 'run-partner-*.log' -type f | sort)
	fi
	for log in "${partner_logs[@]}"; do
		paths+=("${log#"${repo_root}/"}")
	done
fi

if [[ "${#paths[@]}" -eq 0 ]]; then
	echo "error: nothing to package for mode=${mode} under ${results_root}/" >&2
	exit 1
fi

echo "==> packaging mode=${mode} (${#paths[@]} path(s)) -> ${out_tar}"
tar -czf "${out_tar}" -C "${repo_root}" "${paths[@]}"
echo "==> done: ${out_tar}"
echo "    Include each profile's report/run.log when submitting failures or certification evidence."
