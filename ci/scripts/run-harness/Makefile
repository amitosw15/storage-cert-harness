REPO_ROOT := $(abspath $(dir $(lastword $(MAKEFILE_LIST)))/../../..)
SCRIPTS := $(REPO_ROOT)/ci/scripts/run-harness

KUBECONFIG ?= work/kubeconfig
VERSION ?= latest

.NOTPARALLEL: clean-smoke-env prepare-smoke-env smoke-env smoke-ci run-harness-smoke

.PHONY: help replay-smoke prepare-smoke-env clean-smoke-env smoke-env smoke-ci \
	run-harness-smoke run-quay-image-tests

help:
	@echo "Harness run targets (prefer repo root: make <target>):"
	@echo "  prepare-smoke-env    Stage ci/config/smoke (+ catalog) into work/"
	@echo "  clean-smoke-env      Remove smoke artifacts, logs, staged config copies"
	@echo "  smoke-env            clean-smoke-env then prepare-smoke-env"
	@echo "  smoke-ci             smoke-env then run-harness-smoke (CI / first-time flow)"
	@echo "  run-harness-smoke    Live cluster smoke via local Podman image"
	@echo "  run-quay-image-tests Pull and run published Quay image (VERSION=$(VERSION))"
	@echo "  replay-smoke         Offline validate+run (example catalog, no cluster)"

prepare-smoke-env:
	cd "$(REPO_ROOT)" && "$(SCRIPTS)/prepare-smoke-env.sh"

clean-smoke-env:
	cd "$(REPO_ROOT)" && "$(SCRIPTS)/clean-smoke-env.sh"

smoke-env:
	cd "$(REPO_ROOT)" && "$(SCRIPTS)/clean-smoke-env.sh" && "$(SCRIPTS)/prepare-smoke-env.sh"

smoke-ci:
	cd "$(REPO_ROOT)" && "$(SCRIPTS)/clean-smoke-env.sh" && "$(SCRIPTS)/prepare-smoke-env.sh" && "$(SCRIPTS)/run-image-tests.sh" "$(KUBECONFIG)"

replay-smoke:
	cd "$(REPO_ROOT)" && "$(SCRIPTS)/replay-smoke.sh"

run-harness-smoke:
	cd "$(REPO_ROOT)" && "$(SCRIPTS)/run-image-tests.sh" "$(KUBECONFIG)"

run-quay-image-tests:
	cd "$(REPO_ROOT)" && "$(SCRIPTS)/run-quay-image-tests.sh" "$(VERSION)" "$(KUBECONFIG)"
