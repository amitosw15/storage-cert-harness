#!/usr/bin/env bash
# Go format, vet, and golangci-lint. Fails if tools are missing (unlike the old Makefile).
# Prefer ~/.local/bin and GOPATH/bin over a distro golangci-lint built with older Go.
# Used by GitLab, GitHub Actions, make lint, and the pre-commit hook.
set -euo pipefail
# shellcheck source=ci-utils.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/ci-utils.sh"
if [[ -n "${PRE_COMMIT:-}" ]]; then
	ci_log_init "pre-commit-lint-go"
else
	ci_log_init "lint-go"
fi
require_cmd go
# ci-utils sets GOPROXY=off for vendor builds; allow toolchain auto-download when
# the local `go` binary is older than go.mod (developer laptops without go-toolset).
if [[ "${GOPROXY}" == "off" ]]; then
	export GOPROXY="${GO_TOOLCHAIN_GOPROXY:-https://proxy.golang.org,direct}"
fi
export PATH="${HOME}/.local/bin:$(go env GOPATH)/bin:${PATH}"
require_cmd golangci-lint

gcl_ver="$(golangci-lint version 2>&1 || true)"
if [[ "${gcl_ver}" =~ built\ with\ go1\.([0-9]+) ]] && [[ "${BASH_REMATCH[1]}" -lt 26 ]]; then
	die "golangci-lint at $(command -v golangci-lint) was built with go1.${BASH_REMATCH[1]} (need go1.26.9+ for this module). Run ./ci/install-tools.sh"
fi

dirty="$(git ls-files '*.go' | grep -v '^vendor/' | xargs -r gofmt -l || true)"
if [[ -n "${dirty}" ]]; then
	echo "gofmt needed on:"
	echo "${dirty}"
	exit 1
fi
go vet ./...
golangci-lint run -c "${CI_CONFIG_DIR}/golangci.yml" ./...
echo "go lint ok"
