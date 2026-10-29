# Harness image run entry points (local cluster smoke, Quay image smoke, offline replay).
# Repo root Makefile delegates here for run-harness-smoke and run-quay-image-tests.

REPO_ROOT := $(abspath $(dir $(lastword $(MAKEFILE_LIST)))/../../..)
SCRIPTS := $(REPO_ROOT)/ci/scripts/run-harness

KUBECONFIG ?= work/kubeconfig
VERSION ?= latest

.PHONY: help replay-smoke run-harness-smoke run-quay-image-tests

help:
	@echo "Harness run targets (prefer repo root: make <target>):"
	@echo "  run-harness-smoke    Live cluster smoke via local Podman image"
	@echo "  run-quay-image-tests Pull and run published Quay image (VERSION=$(VERSION))"
	@echo "  replay-smoke         Offline validate+run (example catalog, no cluster)"

replay-smoke:
	cd "$(REPO_ROOT)" && "$(SCRIPTS)/replay-smoke.sh"

run-harness-smoke:
	cd "$(REPO_ROOT)" && "$(SCRIPTS)/run-image-tests.sh" "$(KUBECONFIG)"

run-quay-image-tests:
	cd "$(REPO_ROOT)" && "$(SCRIPTS)/run-quay-image-tests.sh" "$(VERSION)" "$(KUBECONFIG)"
