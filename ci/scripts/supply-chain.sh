#!/usr/bin/env bash
# Module integrity (vendor), govulncheck, gosec, and trivy fs (repo tree).
# Used by GitLab, GitHub Actions, and make test. GitHub Actions gates on
# failures; GitLab keeps this job allow-failure pending ECOPROJECT-5419.
set -euo pipefail
# shellcheck source=ci-utils.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/ci-utils.sh"
ci_log_init "supply-chain"
# Tools installed by ci/scripts/install-tools.sh may live outside the default PATH.
export PATH="${HOME}/.local/bin:$(go env GOPATH)/bin:${PATH}"
require_cmd go
require_cmd govulncheck
require_cmd gosec
require_cmd trivy

# Each step is announced so a GitLab log is not a single GOPROXY line + exit 1.
step() { echo "==> $*"; }

# go mod verify / go mod vendor read the module cache (and the proxy if a
# module is missing). GOPROXY=off + a cold runner has an empty cache, so Go
# prints e.g. "module lookup disabled by GOPROXY=off" and exits 1 before
# govulncheck. A laptop with a warm GOMODCACHE succeeds — same script, different
# cache. Unset GOFLAGS for those two: -mod=vendor is a build flag, not a
# verify/vendor flag.
if [[ "${GOPROXY}" == "off" ]]; then
	step "go mod verify (GOPROXY=off; skip if the module cache is empty)"
	if ! env -u GOFLAGS go mod verify; then
		echo "note: go mod verify failed under GOPROXY=off (typical on GitLab:"
		echo "      empty GOMODCACHE). Integrity is committed vendor/ + go.sum."
		echo "      A local run with a warm cache still verifies. Continuing."
	fi
	step "go mod vendor (GOPROXY=off; skip if the module cache is empty)"
	if env -u GOFLAGS go mod vendor; then
		if ! git diff --exit-code -- go.sum vendor/; then
			die "vendor/ or go.sum is out of sync; run go mod vendor and commit"
		fi
	else
		echo "note: go mod vendor failed under GOPROXY=off (same cold-cache case)."
		echo "      Not rewriting vendor/; checking -mod=vendor instead."
	fi
else
	step "go mod verify"
	env -u GOFLAGS go mod verify
	step "go mod vendor"
	env -u GOFLAGS go mod vendor
	if ! git diff --exit-code -- go.sum vendor/; then
		die "vendor/ or go.sum is out of sync; run go mod vendor and commit"
	fi
fi

step "go list ./...  (GOFLAGS=${GOFLAGS}, GOPROXY=${GOPROXY})"
if ! go list ./... >/dev/null; then
	die "go list ./... failed under -mod=vendor (vendor/ incomplete?)"
fi

step "govulncheck ./..."
govulncheck ./...

step "gosec ./cmd/... ./internal/..."
gosec ./cmd/... ./internal/...

# Repo tree (no container). Image-layer secrets are ci/image-scan-trivy.sh.
step "trivy fs --scanners vuln,secret --severity HIGH,CRITICAL"
trivy fs --scanners vuln,secret --severity HIGH,CRITICAL --exit-code 1 --ignore-unfixed --ignorefile "${CI_CONFIG_DIR}/trivyignore" .

echo "supply-chain ok"
