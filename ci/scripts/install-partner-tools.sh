#!/usr/bin/env bash
# Install or verify host tools for partner certification (make partner-*).
# This is not a Makefile target; run it directly from the repository root.
#
# Usage:
#   ./ci/scripts/install-partner-tools.sh          # install missing tools where possible
#   ./ci/scripts/install-partner-tools.sh --check  # report status only (exit 1 if required tools missing)
#
# If installation fails, install packages manually for your OS (see PARTNER-CERTIFICATION-GUIDE.md).
# For Red Hat developer CI tooling (Go, linters, scanners), use ./ci/scripts/install-tools.sh instead.
#
# Package management used by this script:
#   yq, jq     — curl download into ~/.local/bin only (per user; not shared across OS users or shells until PATH is set)
#   podman     — dnf on Fedora/RHEL; apt on Debian/Ubuntu; brew on macOS
#   git, make  — not installed automatically; suggests dnf / apt / brew
#   oc         — not installed; OpenShift client from Red Hat only
# Partner yq/jq live under ~/.local/bin only; remove with rm when testing locally.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${REPO_ROOT}"

# shellcheck source=partner-host-env.sh
source "${REPO_ROOT}/ci/scripts/partner-host-env.sh"

USER_BIN="${PARTNER_USER_BIN}"
YQ_VERSION="${PARTNER_YQ_VERSION:-v4.44.6}"
JQ_VERSION="${PARTNER_JQ_VERSION:-jq-1.7.1}"
check_only=0
if [[ "${1:-}" == "--check" ]]; then
	check_only=1
fi

die() {
	echo "error: $*" >&2
	exit 1
}

have() {
	command -v "$1" >/dev/null 2>&1
}

ensure_path() {
	mkdir -p "${USER_BIN}"
	case ":${PATH}:" in
	*":${USER_BIN}:"*) ;;
	*) export PATH="${USER_BIN}:${PATH}" ;;
	esac
}

os_family() {
	local u
	u="$(uname -s)"
	case "${u}" in
	Linux) echo "linux" ;;
	Darwin) echo "macos" ;;
	MINGW* | MSYS* | CYGWIN* | Windows*) echo "windows" ;;
	*) echo "unknown" ;;
	esac
}

check_platform() {
	local fam
	fam="$(os_family)"
	case "${fam}" in
	windows)
		die "Windows is not supported for partner certification hosts. Use a Linux VM or WSL2 is not validated; use Fedora/RHEL/Ubuntu Linux."
		;;
	unknown)
		die "unsupported operating system: $(uname -s)"
		;;
	macos)
		echo "note: macOS has partial support (Podman Machine, oc, yq). Prefer Linux for certification runs."
		;;
	linux)
		echo "platform: Linux ($(uname -m))"
		;;
	esac
}

# Map uname -m to GitHub release asset suffixes for yq / jq.
release_arch() {
	local mach="$1"
	local os="$2"
	case "${mach}" in
	x86_64 | amd64)
		case "${os}" in
		linux) echo "linux_amd64" ;;
		macos) echo "darwin_amd64" ;;
		*) return 1 ;;
		esac
		;;
	aarch64 | arm64)
		case "${os}" in
		linux) echo "linux_arm64" ;;
		macos) echo "darwin_arm64" ;;
		*) return 1 ;;
		esac
		;;
	*) return 1 ;;
	esac
}

# Download a single-file release binary; must be used with explicit || return 1 (errexit is off under cmd || …).
fetch_release_binary() {
	local url="$1" dest="$2" label="$3"
	local tmp
	tmp="$(mktemp)"
	if ! curl -sSfL "${url}" -o "${tmp}"; then
		rm -f "${tmp}"
		echo "error: ${label} download failed: ${url}" >&2
		return 1
	fi
	if [[ ! -s "${tmp}" ]]; then
		rm -f "${tmp}"
		echo "error: ${label} download empty: ${url}" >&2
		return 1
	fi
	mkdir -p "$(dirname "${dest}")"
	install -m 0755 "${tmp}" "${dest}"
	rm -f "${tmp}"
	return 0
}

partner_local_yq_ok() {
	[[ -x "${USER_BIN}/yq" ]] && "${USER_BIN}/yq" --version 2>&1 | grep -q mikefarah
}

partner_local_jq_ok() {
	[[ -x "${USER_BIN}/jq" ]] && "${USER_BIN}/jq" --version >/dev/null 2>&1
}

install_yq() {
	local dest="${USER_BIN}/yq"
	if partner_local_yq_ok; then
		export YQ="${dest}"
		echo "ok  yq  ${dest}"
		return 0
	fi
	if [[ "${check_only}" -eq 1 ]]; then
		echo "missing  yq  (${dest}; re-run without --check to download)"
		return 1
	fi
	have curl || die "curl is required to download yq"
	local os arch asset url
	os="$(os_family)"
	[[ "${os}" == "macos" ]] || [[ "${os}" == "linux" ]] || die "cannot auto-install yq on this OS"
	arch="$(release_arch "$(uname -m)" "${os}")" || die "unsupported architecture for yq: $(uname -m)"
	# mikefarah/yq assets: yq_linux_amd64, yq_darwin_arm64, … (not yq_linux_linux_amd64).
	asset="yq_${arch}"
	url="https://github.com/mikefarah/yq/releases/download/${YQ_VERSION}/${asset}"
	ensure_path
	echo "installing yq ${YQ_VERSION} (${asset}) -> ${dest}"
	fetch_release_binary "${url}" "${dest}" "yq" || return 1
	if ! "${dest}" --version >/dev/null 2>&1; then
		rm -f "${dest}"
		echo "error: installed yq at ${dest} does not run (--version failed)" >&2
		return 1
	fi
	export YQ="${dest}"
	hash -d yq 2>/dev/null || true
	echo "ok  yq  ${dest}"
}

install_jq() {
	local dest="${USER_BIN}/jq"
	if partner_local_jq_ok; then
		echo "ok  jq  ${dest}"
		return 0
	fi
	if [[ "${check_only}" -eq 1 ]]; then
		echo "optional-missing  jq  (${dest}; re-run without --check to download)"
		return 0
	fi
	have curl || die "curl is required to download jq"
	local os mach name url
	os="$(os_family)"
	[[ "${os}" == "macos" ]] || [[ "${os}" == "linux" ]] || die "cannot auto-install jq on this OS"
	mach="$(uname -m)"
	case "${mach}" in
	x86_64 | amd64)
		[[ "${os}" == "linux" ]] && name="jq-linux-amd64" || name="jq-macos-amd64"
		;;
	aarch64 | arm64)
		[[ "${os}" == "linux" ]] && name="jq-linux-arm64" || name="jq-macos-arm64"
		;;
	*) die "unsupported architecture for jq: ${mach}" ;;
	esac
	url="https://github.com/jqlang/jq/releases/download/${JQ_VERSION}/${name}"
	ensure_path
	echo "installing jq ${JQ_VERSION} (${name}) -> ${dest}"
	fetch_release_binary "${url}" "${dest}" "jq" || return 1
	if ! "${dest}" --version >/dev/null 2>&1; then
		rm -f "${dest}"
		echo "error: installed jq at ${dest} does not run (--version failed)" >&2
		return 1
	fi
	hash -d jq 2>/dev/null || true
	echo "ok  jq  ${dest}"
}

suggest_pkg() {
	local tool="$1"
	local fam
	fam="$(os_family)"
	if [[ "${fam}" == "linux" ]] && have dnf; then
		echo "  try: sudo dnf install -y ${tool}"
	elif [[ "${fam}" == "linux" ]] && have apt-get; then
		echo "  try: sudo apt-get update && sudo apt-get install -y ${tool}"
	elif [[ "${fam}" == "macos" ]] && have brew; then
		echo "  try: brew install ${tool}"
	else
		echo "  install ${tool} using your OS package manager or vendor documentation"
	fi
}

ensure_git() {
	if have git; then
		echo "ok  git  $(command -v git)"
		return 0
	fi
	if [[ "${check_only}" -eq 1 ]]; then
		echo "missing  git"
		return 1
	fi
	echo "git is not installed."
	suggest_pkg git
	die "install git, then re-run this script"
}

ensure_make() {
	if have make; then
		echo "ok  make  $(command -v make)"
		return 0
	fi
	if [[ "${check_only}" -eq 1 ]]; then
		echo "missing  make"
		return 1
	fi
	echo "make is not installed."
	suggest_pkg make
	die "install make, then re-run this script"
}

ensure_python3() {
	if have python3; then
		echo "ok  python3  $(command -v python3)"
		return 0
	fi
	if [[ "${check_only}" -eq 1 ]]; then
		echo "optional-missing  python3  (optional; useful for local automation)"
		return 0
	fi
	echo "python3 is not installed (optional for partner runs)."
	suggest_pkg python3
	echo "note: partner make targets do not require python3; install only if you need it locally"
}

ensure_podman() {
	if have podman; then
		echo "ok  podman  $(command -v podman)"
		return 0
	fi
	if [[ "${check_only}" -eq 1 ]]; then
		echo "missing  podman"
		return 1
	fi
	local fam
	fam="$(os_family)"
	if [[ "${fam}" == "linux" ]] && have dnf; then
		echo "installing podman (dnf)"
		if have sudo; then
			sudo dnf install -y podman
		else
			dnf install -y podman
		fi
		echo "ok  podman  $(command -v podman)"
		return 0
	fi
	if [[ "${fam}" == "linux" ]] && have apt-get; then
		echo "installing podman (apt)"
		sudo apt-get update -qq
		sudo apt-get install -y podman
		echo "ok  podman  $(command -v podman)"
		return 0
	fi
	if [[ "${fam}" == "macos" ]] && have brew; then
		echo "installing podman (brew); you may need: podman machine init && podman machine start"
		brew install podman
		echo "ok  podman  $(command -v podman)"
		return 0
	fi
	echo "podman is required but could not be installed automatically."
	suggest_pkg podman
	die "install podman, then re-run this script"
}

# Run a smoke command for each tool; used after install and with --check.
verify_partner_tools() {
	local failed=0 yq_bin jq_bin out
	echo "==> verifying tool binaries"

	verify_one() {
		local label="$1"
		shift
		local out
		if out="$("$@" 2>&1)"; then
			echo "verify ok  ${label}  $(printf '%s' "${out}" | head -n1)"
			return 0
		fi
		echo "verify FAIL  ${label}" >&2
		printf '%s\n' "${out}" | sed 's/^/  /' >&2
		failed=1
		return 1
	}

	verify_one git git --version || true
	verify_one make make --version || true
	verify_one bash bash --version || true
	verify_one podman podman --version || true
	verify_one tar tar --version || true

	if ! partner_local_yq_ok; then
		echo "verify FAIL  yq  (need mikefarah yq at ${USER_BIN}/yq; run ./ci/scripts/install-partner-tools.sh)" >&2
		failed=1
	else
		yq_bin="${USER_BIN}/yq"
		if out="$("${yq_bin}" --version 2>&1)"; then
			echo "verify ok  yq  $(printf '%s' "${out}" | head -n1)  (${yq_bin})"
		else
			echo "verify FAIL  yq  (${yq_bin})" >&2
			failed=1
		fi
		if [[ "${failed}" -eq 0 ]]; then
			if printf '%s\n' 'probe: partner-yq' | "${yq_bin}" e -r '.probe' - 2>/dev/null | grep -qx partner-yq; then
				echo "verify ok  yq-expression  .probe parse"
			else
				echo "verify FAIL  yq-expression  (expected mikefarah/yq; distro python-yq is not supported)" >&2
				failed=1
			fi
		fi
	fi

	if partner_local_jq_ok; then
		jq_bin="${USER_BIN}/jq"
	else
		jq_bin=""
	fi
	if [[ -n "${jq_bin}" ]]; then
		verify_one jq "${jq_bin}" --version || true
		if [[ "${failed}" -eq 0 ]]; then
			if printf '%s' '{}' | "${jq_bin}" . >/dev/null 2>&1; then
				echo "verify ok  jq-expression  JSON parse"
			else
				echo "verify FAIL  jq  (JSON parse smoke test)" >&2
				failed=1
			fi
		fi
	else
		echo "verify skip jq  (optional; recommended for troubleshooting)"
	fi

	if have python3; then
		verify_one python3 python3 --version || true
	else
		echo "verify skip python3  (optional)"
	fi

	if have oc; then
		verify_one oc oc version --client || true
	else
		echo "verify FAIL  oc  (required)" >&2
		failed=1
	fi

	if [[ "${failed}" -ne 0 ]]; then
		echo "error: one or more tool verification checks failed" >&2
		return 1
	fi
	echo "verify ok  all required partner tools"
	return 0
}

print_oc_guidance() {
	if have oc; then
		echo "ok  oc  $(command -v oc)"
		return 0
	fi
	echo "missing  oc  (required for partner-preflight and partner-cert-* host checks)"
	echo "  Download the OpenShift CLI for your platform:"
	echo "  https://console.redhat.com/openshift/downloads"
	echo "  or copy oc from an OpenShift cluster release image matching your cluster version."
	if [[ "${check_only}" -eq 1 ]]; then
		return 1
	fi
	echo "  Install oc on PATH, then run: ./ci/scripts/install-partner-tools.sh --check"
	return 1
}

echo "==> partner host tools  $(date -u +%Y-%m-%dT%H:%M:%SZ)"
check_platform
ensure_path

missing_required=0
ensure_git || missing_required=1
ensure_make || missing_required=1
ensure_podman || missing_required=1
install_yq || missing_required=1
install_jq || true
ensure_python3 || true
print_oc_guidance || missing_required=1

if [[ "${check_only}" -eq 1 ]]; then
	if [[ "${missing_required}" -ne 0 ]]; then
		echo ""
		echo "One or more required tools are missing."
		echo "Run ./ci/scripts/install-partner-tools.sh without --check to install what can be automated,"
		echo "then install remaining tools manually (especially oc)."
		echo "Developer CI tooling: ./ci/scripts/install-tools.sh (requires Go; not for typical partners)."
		exit 1
	fi
	verify_partner_tools || exit 1
	echo "all required partner tools present"
	exit 0
fi

if [[ "${missing_required}" -ne 0 ]]; then
	die "one or more required tools could not be installed; fix errors above and re-run --check"
fi

verify_partner_tools || die "tool verification failed; fix binaries above and re-run --check"

echo ""
echo "For your current shell (fixes stale /usr/bin/yq after removing system yq):"
echo "  source ./ci/scripts/partner-host-env.sh"
echo "  yq --version"
echo ""
echo "install-partner-tools ok"
echo "  Next: make partner-preflight (with KUBECONFIG and BACKEND set)"
