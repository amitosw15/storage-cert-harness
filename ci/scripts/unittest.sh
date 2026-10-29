#!/usr/bin/env bash
# Unit tests only: go test ./... (package tests and golden-file parser tests).
# No cluster, no e2e, no live smoke. Offline replay-smoke is ci/scripts/run-harness/replay-smoke.sh.
set -euo pipefail
# shellcheck source=ci-utils.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/ci-utils.sh"
ci_log_init "unittest"
require_cmd go
if [[ "${GOPROXY}" == "off" ]]; then
	export GOPROXY="${GO_TOOLCHAIN_GOPROXY:-https://proxy.golang.org,direct}"
fi
go test ./...
echo "unit tests ok"
