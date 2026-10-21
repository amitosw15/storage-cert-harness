#!/usr/bin/env bash
# Build the harness static-ish binary into bin/harness.
set -euo pipefail
# shellcheck source=ci-utils.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/ci-utils.sh"
ci_log_init "build"
require_cmd go
if [[ "${GOPROXY}" == "off" ]]; then
	export GOPROXY="${GO_TOOLCHAIN_GOPROXY:-https://proxy.golang.org,direct}"
fi
VERSION="$(binary_version)"
mkdir -p "${REPO_ROOT}/bin" "${REPO_ROOT}/dist"
go build -ldflags "-X main.version=${VERSION}" -o "${REPO_ROOT}/bin/harness" ./cmd/harness
echo "${VERSION}" > "${REPO_ROOT}/dist/binary-version.txt"
echo "built bin/harness version=${VERSION}"
