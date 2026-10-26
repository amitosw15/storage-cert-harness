include ci/config/images.env

BINARY := bin/harness
PKG := ./...
LOCAL_GOARCH := $(shell uname -m)
ifeq ($(LOCAL_GOARCH),x86_64)
LOCAL_GOARCH := amd64
else ifeq ($(LOCAL_GOARCH),aarch64)
LOCAL_GOARCH := arm64
endif

.PHONY: all build test unittest vet lint lint-yaml lint-actions lint-md lint-go tidy cover \
	run-example run-tr-stor-006 clean clean-image-cache ci ci-image replay-smoke image-build \
	image-build-fresh image-build-local run-harness-smoke \
	run-quay-image-tests partner-preflight partner-cert-load-80 partner-cert-no-load-80 \
	partner-package-results \
	image-contents-check image-scan-trivy image-scan-dive image-scan image-push \
	secret-scan supply-chain install-tools mirror-ci-tools sync-images set-next-version

all: build

build:
	./ci/scripts/build.sh

# 1:1 with GitLab job unittest (stage: test).
unittest:
	./ci/scripts/unittest.sh

vet:
	go vet $(PKG)

lint-yaml:
	./ci/scripts/lint-yaml.sh

lint-actions:
	./ci/scripts/lint-actions.sh

lint-md:
	./ci/scripts/lint-md.sh

lint-go:
	./ci/scripts/lint-go.sh

# GitLab stage: lint
lint: lint-yaml lint-actions lint-md lint-go

# GitLab stage: test
test: unittest secret-scan supply-chain

tidy:
	go mod tidy

cover:
	go test -coverprofile=coverage.out $(PKG)
	go tool cover -func=coverage.out

run-example: build
	$(BINARY) run --catalog examples/catalog.example.json --thresholds thresholds.example.json --partner-level 3

ci:
	./ci/scripts/ci.sh

ci-image:
	./ci/scripts/ci.sh --image

install-tools:
	./ci/scripts/install-tools.sh

# Intentionally a no-op: plans/example-smoke.yaml is not shipped in this repo.
# Offline replay smoke runs via ./ci/scripts/ci.sh (which calls replay-smoke.sh
# directly) or GitHub Actions when vars.CI_RUN_REPLAY_SMOKE=true.
# the hosted runner and secrets to access the test cluster are not available.
replay-smoke:
	@echo "replay-smoke skipped by design (use ./ci/scripts/replay-smoke.sh or make ci)"

secret-scan:
	./ci/scripts/secret-scan.sh

supply-chain:
	./ci/scripts/supply-chain.sh

image-build:
	./ci/scripts/image-build.sh

# Drop cached container layers and rebuild without cache (use after Containerfile cleanup changes).
# Hold the local image lock across cleanup and rebuild (IMAGE_BUILD_FRESH).
image-build-fresh:
	IMAGE_BUILD_NO_CACHE=1 IMAGE_BUILD_FRESH=1 ./ci/scripts/image-build.sh

clean-image-cache:
	./ci/scripts/clean-image-cache.sh

# Rebuild the binary and a uniquely tagged local image under one exclusive lock.
image-build-local: export GOOS = linux
image-build-local: export GOARCH = $(LOCAL_GOARCH)
image-build-local:
	./ci/scripts/image-build-local.sh

# Run the live harness smoke plan; override KUBECONFIG to use another kubeconfig.
run-harness-smoke:
	./ci/scripts/run-image-tests.sh "$(or $(KUBECONFIG),work/kubeconfig)"

# Pull and run the published Quay image; override VERSION and KUBECONFIG as needed.
# Example: make run-quay-image-tests VERSION=1.0.0
run-quay-image-tests:
	./ci/scripts/run-quay-image-tests.sh "$(or $(VERSION),latest)" "$(or $(KUBECONFIG),work/kubeconfig)"

# Partner certification against the published image (no local build).
# Example:
#   export KUBECONFIG=/path/to/kubeconfig BACKEND=partner-storage
#   make partner-preflight
#   make partner-cert-load-80
#   make partner-cert-no-load-80
partner-preflight:
	./ci/scripts/run-partner-preflight.sh "$(or $(KUBECONFIG),kubeconfig)"

partner-cert-load-80:
	./ci/scripts/run-partner-cert.sh load-80 "$(or $(KUBECONFIG),kubeconfig)"

partner-cert-no-load-80:
	./ci/scripts/run-partner-cert.sh no-load-80 "$(or $(KUBECONFIG),kubeconfig)"

partner-package-results:
	PARTNER_PACKAGE_MODE=level3 ./ci/scripts/run-partner-package-results.sh

image-contents-check:
	./ci/scripts/image-contents-check.sh

image-lint:
	./ci/scripts/image-lint.sh

image-scan-trivy:
	./ci/scripts/image-scan-trivy.sh

image-scan-dive:
	./ci/scripts/image-scan-dive.sh

image-scan: image-build image-contents-check image-scan-trivy image-scan-dive

image-push:
	PUSH=1 ./ci/scripts/image-push.sh

# One-time (or refresh) Hub → quay.io/virtarraycert/ci_tools retags. Needs
# Docker Hub pull + Quay push credentials. Not run in GitLab.
mirror-ci-tools:
	./ci/scripts/mirror-ci-tools.sh

# After editing ci/config/images.env. lint-yaml.sh --check fails if this is stale.
sync-images:
	./ci/scripts/sync-images-yml.sh

# Set next binary or image version (see docs/ci.md Versioning).
#   make set-next-version BINARY=1.0.0
#   make set-next-version IMAGE=2.0.0
set-next-version:
ifdef BINARY
	./ci/scripts/set-next-version.sh --binary $(BINARY)
else ifdef IMAGE
	./ci/scripts/set-next-version.sh --image $(IMAGE)
else
	$(error specify BINARY=x.y.z or IMAGE=x.y.z)
endif

# TR-STOR-006 as authored: live pvc-density on the cluster.
run-tr-stor-006: build
	$(BINARY) run \
		--catalog examples/catalog.tr-stor-006.json \
		--plan plans/tr-stor-006-pvc-density.yaml \
		--backends backends.example.yaml \
		--workdir ./out/tr-stor-006 \
		--output ./out/tr-stor-006 \
		-v

clean:
	./ci/scripts/clean.sh
