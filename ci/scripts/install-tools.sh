#!/usr/bin/env bash
# Install local CI tools. Idempotent: a second run is a no-op when versions match.
# Does not source ci/lib.sh (that file sets GOPROXY=off, which would break go install).
#
# Prerequisites (not installed here): Go 1.26.9+, git, python3, pip, npm.
# Podman is the default container engine; OS-package it if missing.
#
# Usage:
#   ./ci/install-tools.sh          # install only what is missing or wrong version
#   ./ci/install-tools.sh --check  # print status only (no installs)
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${REPO_ROOT}"

# Pins from ci/config/images.env. Do not source ci-utils.sh (that file sets GOPROXY=off).
# shellcheck source=images.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/images.sh"

GO_PROXY="${GO_INSTALL_GOPROXY:-https://proxy.golang.org,direct}"
USER_BIN="${HOME}/.local/bin"
GOBIN="${GOBIN:-}"
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

need() {
	have "$1" || die "prerequisite not found: $1"
}

# PATH or ~/.local/bin. Empty if not installed.
resolve_bin() {
	local name="$1"
	if have "${name}"; then
		command -v "${name}"
		return 0
	fi
	if [[ -x "${USER_BIN}/${name}" ]]; then
		echo "${USER_BIN}/${name}"
		return 0
	fi
	return 1
}

# True if `bin [args...]` output (stdout+stderr) contains needle.
version_has() {
	local bin="$1"
	local needle="$2"
	shift 2
	[[ -n "${bin}" && -x "${bin}" ]] || return 1
	local out
	out="$("${bin}" "$@" 2>&1 || true)"
	[[ "${out}" == *"${needle}"* ]]
}

go_bin_dir() {
	if [[ -n "${GOBIN}" ]]; then
		echo "${GOBIN}"
		return
	fi
	need go
	echo "$(go env GOPATH)/bin"
}

ensure_dir_on_path() {
	local dir="$1"
	mkdir -p "${dir}"
	case ":${PATH}:" in
	*":${dir}:"*) ;;
	*) export PATH="${dir}:${PATH}" ;;
	esac
}

skip_or_missing() {
	local bin="$1"
	local extra="${2:-}"
	if have "${bin}"; then
		echo "ok  ${bin}  $(command -v "${bin}")${extra:+  ${extra}}"
		return 0
	fi
	if [[ "${check_only}" -eq 1 ]]; then
		echo "missing  ${bin}${extra:+  ${extra}}"
		return 0
	fi
	return 1
}

install_go_pkg() {
	local bin="$1"
	local spec="$2"
	local needle="${3:-}"
	if ! have go; then
		if [[ "${check_only}" -eq 1 ]]; then
			echo "missing  ${bin}  (need go)"
			return
		fi
			die "Go 1.26.9+ is required to install ${bin}"
	fi
	local dest
	dest="$(go_bin_dir)/${bin}"
	if [[ -x "${dest}" ]] && { [[ -z "${needle}" ]] || version_has "${dest}" "${needle}" version || version_has "${dest}" "${needle}" --version; }; then
		echo "ok  ${bin}  ${dest}"
		return
	fi
	if have "${bin}" && [[ -z "${needle}" ]]; then
		echo "ok  ${bin}  $(command -v "${bin}")"
		return
	fi
	if [[ "${check_only}" -eq 1 ]]; then
		echo "missing  ${bin}"
		return
	fi
	echo "installing ${spec}"
	GOPROXY="${GO_PROXY}" GOFLAGS= GOBIN="$(go_bin_dir)" go install "${spec}"
	echo "ok  ${bin}  $(command -v "${bin}")"
}

# Official release binary is OK only if the pin matches and it was compiled
# with Go >= 1.26 (go.mod). Distro copies (often go1.24) must not count.
golangci_bin_ok() {
	local bin="$1"
	local ver="$2"
	[[ -n "${bin}" && -x "${bin}" ]] || return 1
	local out
	out="$("${bin}" version 2>&1 || true)"
	[[ "${out}" == *"${ver}"* ]] || return 1
	[[ "${out}" =~ built\ with\ go1\.([0-9]+) ]] || return 1
	[[ "${BASH_REMATCH[1]}" -ge 26 ]]
}

install_golangci_lint() {
	local ver="${GOLANGCI_LINT_VERSION#v}"
	local dest="${USER_BIN}/golangci-lint"
	if golangci_bin_ok "${dest}" "${ver}"; then
		echo "ok  golangci-lint  ${dest}  v${ver}"
		return
	fi
	if have go && golangci_bin_ok "$(go_bin_dir)/golangci-lint" "${ver}"; then
		echo "ok  golangci-lint  $(go_bin_dir)/golangci-lint  v${ver}"
		return
	fi
	if [[ "${check_only}" -eq 1 ]]; then
			echo "missing  golangci-lint  (want v${ver} built with go1.26.9+)"
		return
	fi
	need curl
	local mach tar_arch
	mach="$(uname -m)"
	case "${mach}" in
	x86_64 | amd64) tar_arch="linux-amd64" ;;
	aarch64 | arm64) tar_arch="linux-arm64" ;;
	*) die "unsupported architecture for golangci-lint: ${mach}" ;;
	esac
	echo "installing golangci-lint v${ver} (GitHub release binary)"
	local tmp member
	tmp="$(mktemp -d)"
	member="golangci-lint-${ver}-${tar_arch}/golangci-lint"
	curl -sSfL "https://github.com/golangci/golangci-lint/releases/download/${GOLANGCI_LINT_VERSION}/golangci-lint-${ver}-${tar_arch}.tar.gz" |
		tar xz -C "${tmp}" "${member}"
	install -m 0755 "${tmp}/${member}" "${dest}"
	rm -rf "${tmp}"
	echo "ok  golangci-lint  ${dest}"
}

install_actionlint() {
	local ver="${ACTIONLINT_VERSION#v}"
	local mach tar_arch
	mach="$(uname -m)"
	case "${mach}" in
	x86_64 | amd64) tar_arch="linux_amd64" ;;
	aarch64 | arm64) tar_arch="linux_arm64" ;;
	*) die "unsupported architecture for actionlint: ${mach}" ;;
	esac
	ensure_release_bin actionlint "${ver}" \
		"https://github.com/rhysd/actionlint/releases/download/${ACTIONLINT_VERSION}/actionlint_${ver}_${tar_arch}.tar.gz" \
		actionlint
}

# Skip the network when the binary already exists (PATH or ~/.local/bin).
ensure_release_bin() {
	local name="$1"
	local needle="$2"
	local url="$3"
	local tar_member="$4"
	local bin
	bin="$(resolve_bin "${name}" || true)"
	if [[ -n "${bin}" ]]; then
		if [[ -z "${needle}" ]] || version_has "${bin}" "${needle}" --version || version_has "${bin}" "${needle}" version; then
			echo "ok  ${name}  ${bin}${needle:+  v${needle}}"
			return
		fi
		echo "ok  ${name}  ${bin}  (present; could not confirm v${needle})"
		return
	fi
	if [[ "${check_only}" -eq 1 ]]; then
		echo "missing  ${name}${needle:+  (want v${needle})}"
		return
	fi
	need curl
	echo "installing ${name} v${needle}"
	local tmp
	tmp="$(mktemp -d)"
	curl -sSfL "${url}" | tar xz -C "${tmp}" "${tar_member}"
	install -m 0755 "${tmp}/${tar_member}" "${USER_BIN}/${name}"
	rm -rf "${tmp}"
	echo "ok  ${name}  ${USER_BIN}/${name}"
}

install_gosec() {
	local ver="${GOSEC_VERSION#v}"
	local mach tar_arch
	mach="$(uname -m)"
	case "${mach}" in
	x86_64 | amd64) tar_arch="linux_amd64" ;;
	aarch64 | arm64) tar_arch="linux_arm64" ;;
	*) die "unsupported architecture for gosec: ${mach}" ;;
	esac
	ensure_release_bin gosec "${ver}" \
		"https://github.com/securego/gosec/releases/download/${GOSEC_VERSION}/gosec_${ver}_${tar_arch}.tar.gz" \
		gosec
}

install_trivy() {
	local ver="${TRIVY_VERSION#v}"
	local mach tar_arch
	mach="$(uname -m)"
	case "${mach}" in
	x86_64 | amd64) tar_arch="Linux-64bit" ;;
	aarch64 | arm64) tar_arch="Linux-ARM64" ;;
	*) die "unsupported architecture for trivy: ${mach}" ;;
	esac
	ensure_release_bin trivy "${ver}" \
		"https://github.com/aquasecurity/trivy/releases/download/${TRIVY_VERSION}/trivy_${ver}_${tar_arch}.tar.gz" \
		trivy
}

install_dive() {
	local ver="${DIVE_VERSION#v}"
	local mach tar_arch
	mach="$(uname -m)"
	case "${mach}" in
	x86_64 | amd64) tar_arch="linux_amd64" ;;
	aarch64 | arm64) tar_arch="linux_arm64" ;;
	*) die "unsupported architecture for dive: ${mach}" ;;
	esac
	ensure_release_bin dive "${ver}" \
		"https://github.com/wagoodman/dive/releases/download/${DIVE_VERSION}/dive_${ver}_${tar_arch}.tar.gz" \
		dive
}

install_hadolint() {
	local ver="${HADOLINT_VERSION#v}"
	local mach asset bin
	bin="$(resolve_bin hadolint || true)"
	if [[ -n "${bin}" ]] && version_has "${bin}" "${ver}" --version; then
		echo "ok  hadolint  ${bin}  v${ver}"
		return
	fi
	if [[ "${check_only}" -eq 1 ]]; then
		echo "missing  hadolint  (want v${ver})"
		return
	fi
	need curl
	mach="$(uname -m)"
	case "${mach}" in
	x86_64 | amd64) asset="hadolint-Linux-x86_64" ;;
	aarch64 | arm64) asset="hadolint-Linux-arm64" ;;
	*) die "unsupported architecture for hadolint: ${mach}" ;;
	esac
	echo "installing hadolint v${ver} (GitHub release binary)"
	curl -sSfL \
		-o "${USER_BIN}/hadolint" \
		"https://github.com/hadolint/hadolint/releases/download/${HADOLINT_VERSION}/${asset}"
	chmod 0755 "${USER_BIN}/hadolint"
	echo "ok  hadolint  ${USER_BIN}/hadolint  v${ver}"
}

install_yamllint() {
	skip_or_missing yamllint && return
	need python3
	echo "installing yamllint (pip --user)"
	python3 -m pip install --user --quiet --disable-pip-version-check yamllint
	ensure_dir_on_path "${HOME}/.local/bin"
	echo "ok  yamllint  $(command -v yamllint)"
}

install_markdownlint() {
	if have markdownlint-cli2 || have markdownlint; then
		echo "ok  markdownlint-cli2  $(command -v markdownlint-cli2 || command -v markdownlint)"
		return
	fi
	if [[ "${check_only}" -eq 1 ]]; then
		echo "missing  markdownlint-cli2"
		return
	fi
	need npm
	echo "installing markdownlint-cli2 (npm prefix ${HOME}/.local)"
	npm install --global --prefix "${HOME}/.local" markdownlint-cli2
	echo "ok  markdownlint-cli2  $(command -v markdownlint-cli2)"
}

install_virtbench() {
	local ver="${VIRTBENCH_VERSION}"
	local repo_dir="${HOME}/.local/opt/virtbench"
	local bin="${USER_BIN}/virtbench"
	local profile_line="export VIRTBENCH_REPO=${repo_dir}"

	if have virtbench && [[ -d "${repo_dir}" ]]; then
		echo "ok  virtbench  $(command -v virtbench)  ${ver}"
		return
	fi
	if [[ "${check_only}" -eq 1 ]]; then
		echo "missing  virtbench  (want ${ver})"
		return
	fi
	need curl
	need python3
	echo "installing virtbench ${ver}"
	mkdir -p "${repo_dir}" "${USER_BIN}"
	curl -sSfL "https://github.com/portworx/kubevirt-benchmark/archive/refs/tags/${ver}.tar.gz" \
		| tar xz --strip-components=1 -C "${repo_dir}"
	python3 -m pip install --user --quiet --disable-pip-version-check "${repo_dir}"
	[[ -f "${bin}" ]] || ln -sf "$(python3 -m site --user-base)/bin/virtbench" "${bin}" 2>/dev/null || true
	for rc in "${HOME}/.bashrc" "${HOME}/.bash_profile" "${HOME}/.profile"; do
		[[ -f "${rc}" ]] || continue
		grep -qF "VIRTBENCH_REPO" "${rc}" && continue
		echo "${profile_line}" >>"${rc}"
		echo "  added VIRTBENCH_REPO to ${rc}"
		break
	done
	export VIRTBENCH_REPO="${repo_dir}"
	echo "ok  virtbench  ${bin}  ${ver}  (VIRTBENCH_REPO=${repo_dir})"
}

install_podman() {
	skip_or_missing podman "(default container engine)" && return
	if have dnf; then
		echo "installing podman (dnf)"
		if have sudo; then
			sudo dnf install -y podman
		else
			dnf install -y podman
		fi
		echo "ok  podman  $(command -v podman)"
		return
	fi
	if have apt-get; then
		echo "installing podman (apt)"
		sudo apt-get update -qq
		sudo apt-get install -y podman
		echo "ok  podman  $(command -v podman)"
		return
	fi
	die "podman is the default container engine. Install it (Fedora: sudo dnf install -y podman) or set CONTAINER_ENGINE=docker|buildah"
}

echo "==> CI tools  $(date -u +%Y-%m-%dT%H:%M:%SZ)"
need git
if have go; then
	echo "ok  go  $(go version)"
	ensure_dir_on_path "$(go_bin_dir)"
else
	[[ "${check_only}" -eq 1 ]] && echo "missing  go  (need 1.26.9+)"
	[[ "${check_only}" -eq 0 ]] && die "Go 1.26.9+ is required (see go.mod)"
fi
ensure_dir_on_path "${USER_BIN}"

install_yamllint
install_markdownlint
install_golangci_lint
install_actionlint
install_go_pkg govulncheck "golang.org/x/vuln/cmd/govulncheck@latest"
install_gosec
install_trivy
install_dive
install_hadolint
install_podman
install_virtbench

if [[ "${check_only}" -eq 1 ]]; then
	missing=0
	for c in yamllint markdownlint-cli2 golangci-lint actionlint govulncheck gosec trivy dive hadolint podman go git; do
		if [[ "${c}" == "golangci-lint" ]]; then
			golangci_bin_ok "${USER_BIN}/golangci-lint" "${GOLANGCI_LINT_VERSION#v}" ||
				{ have go && golangci_bin_ok "$(go_bin_dir)/golangci-lint" "${GOLANGCI_LINT_VERSION#v}"; } ||
				missing=1
			continue
		fi
		if [[ "${c}" == "markdownlint-cli2" ]]; then
			have markdownlint-cli2 || have markdownlint || missing=1
			continue
		fi
		have "${c}" || missing=1
	done
	[[ "${missing}" -eq 0 ]] || die "one or more tools missing (run ./ci/install-tools.sh)"
	echo "all CI tools present"
	exit 0
fi

echo "install-tools ok"
