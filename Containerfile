# Python runtime is required by the packaged virtbench FIO CLI. The harness
# binary must be built before this image build and is copied from bin/harness.
# Image references are supplied by ci/config/images.env through the build scripts.
# Keep the harness build version distinct from the base image version.
ARG HARNESS_VERSION=0.0.0-dev
ARG IMAGE_VERSION=0.0.0-dev
ARG TARGETARCH=amd64
ARG BUILD_IMAGE
ARG GO_IMAGE
ARG RUNTIME_IMAGE
ARG KUBE_BURNER_VERSION=v2.8.5
ARG KUBE_BURNER_OCP_VERSION=v1.12.7
ARG KUBE_BURNER_CORE_REF=main
ARG OPENSHIFT_CLIENT_VERSION=4.22.15
ARG VIRTBENCH_VERSION=v2.0.0
ARG KUBEVIRT_VERSION=v1.9.0

FROM ${BUILD_IMAGE} AS builder
ARG TARGETARCH
ARG KUBE_BURNER_VERSION
ARG KUBEVIRT_VERSION
USER 0
WORKDIR /src

RUN set -eux; \
    case "${TARGETARCH}" in amd64) kube_arch=x86_64; virtctl_arch=amd64 ;; arm64) kube_arch=arm64; virtctl_arch=arm64 ;; *) echo "unsupported TARGETARCH=${TARGETARCH}" >&2; exit 1 ;; esac; \
    kube_ver="${KUBE_BURNER_VERSION#v}"; \
    curl -sSfL "https://github.com/kube-burner/kube-burner/releases/download/${KUBE_BURNER_VERSION}/kube-burner-V${kube_ver}-linux-${kube_arch}.tar.gz" -o /tmp/kube-burner.tar.gz; \
    tar xz -C /tmp -f /tmp/kube-burner.tar.gz kube-burner; \
    install -m 0755 /tmp/kube-burner /kube-burner; \
    curl -sSfL "https://github.com/kubevirt/kubevirt/releases/download/${KUBEVIRT_VERSION}/virtctl-${KUBEVIRT_VERSION}-linux-${virtctl_arch}" \
      -o /tmp/virtctl; \
    install -m 0755 /tmp/virtctl /virtctl; \
    rm -f /tmp/kube-burner.tar.gz /tmp/kube-burner /tmp/virtctl
USER 65532

# Keep the OCP workload release, but consume the node-health fix from upstream core.
FROM ${GO_IMAGE} AS kube-burner-ocp-builder
ARG TARGETARCH
ARG KUBE_BURNER_OCP_VERSION
ARG KUBE_BURNER_CORE_REF
USER 0
WORKDIR /src/kube-burner-ocp
RUN set -eux; \
    case "${TARGETARCH}" in amd64|arm64) ;; *) echo "unsupported TARGETARCH=${TARGETARCH}" >&2; exit 1 ;; esac; \
    git init .; \
    git remote add origin https://github.com/kube-burner/kube-burner-ocp.git; \
    git fetch --depth 1 origin "${KUBE_BURNER_OCP_VERSION}"; \
    git checkout --detach FETCH_HEAD; \
    GOPROXY=https://proxy.golang.org,direct \
      go get "github.com/kube-burner/kube-burner/v2@${KUBE_BURNER_CORE_REF}"; \
    core_version="$(go list -m -f '{{.Version}}' github.com/kube-burner/kube-burner/v2)"; \
    GOPROXY=https://proxy.golang.org,direct GOOS=linux GOARCH="${TARGETARCH}" CGO_ENABLED=0 \
      make ARCH="${TARGETARCH}" VERSION="${KUBE_BURNER_OCP_VERSION#v}-core.${core_version}" build; \
    install -m 0755 "bin/${TARGETARCH}/kube-burner-ocp" /kube-burner-ocp
USER 65532

FROM ${BUILD_IMAGE} AS virtbench-builder
ARG TARGETARCH
ARG OPENSHIFT_CLIENT_VERSION
ARG VIRTBENCH_VERSION
USER 0
RUN set -eux; \
    mkdir -p /opt/virtbench; \
    curl -sSfL "https://github.com/portworx/kubevirt-benchmark/archive/refs/tags/${VIRTBENCH_VERSION}.tar.gz" -o /tmp/virtbench.tar.gz; \
    tar xz --strip-components=1 -C /opt/virtbench -f /tmp/virtbench.tar.gz; \
    python3 -m venv /opt/virtbench-venv; \
    /opt/virtbench-venv/bin/python -m pip install --no-cache-dir --upgrade 'pip==26.2.1' 'setuptools>=78.1.1'; \
    /opt/virtbench-venv/bin/pip install --no-cache-dir /opt/virtbench; \
    /opt/virtbench-venv/bin/virtbench --help >/dev/null; \
    test "$(/opt/virtbench-venv/bin/virtbench --version)" = "virtbench, version ${VIRTBENCH_VERSION#v}"; \
    rm -rf /opt/virtbench/docs; \
    archive="openshift-client-linux-${TARGETARCH}-rhel9-${OPENSHIFT_CLIENT_VERSION}.tar.gz"; \
    mkdir -p /tmp/openshift-client; \
    curl -sSfL "https://mirror.openshift.com/pub/openshift-v4/x86_64/clients/ocp/${OPENSHIFT_CLIENT_VERSION}/${archive}" -o "/tmp/${archive}"; \
    tar xz -C /tmp/openshift-client -f "/tmp/${archive}"; \
    install -m 0755 /tmp/openshift-client/oc /oc; \
    install -m 0755 /tmp/openshift-client/kubectl /kubectl; \
    rm -rf /tmp/virtbench.tar.gz /tmp/openshift-client
USER 65532

FROM ${RUNTIME_IMAGE}
ARG IMAGE_VERSION
ARG VIRTBENCH_VERSION
ARG HARNESS_VERSION
USER 0
LABEL org.opencontainers.image.version="${IMAGE_VERSION}"
LABEL io.storage-cert-harness.virtbench.version="${VIRTBENCH_VERSION}"
LABEL io.storage-cert-harness.harness.version="${HARNESS_VERSION}"
# Multiple sources are allowed when the destination is a directory (trailing slash).
COPY bin/harness /usr/bin/
COPY --from=builder /kube-burner /virtctl /usr/bin/
COPY --from=kube-burner-ocp-builder /kube-burner-ocp /usr/bin/
COPY --from=virtbench-builder /oc /kubectl /opt/virtbench-venv/bin/virtbench /usr/bin/
COPY --from=virtbench-builder /opt/virtbench /opt/virtbench-runtime
COPY container-patches/virtbench/examples/utilities/ssh-pod.yaml /opt/virtbench-runtime/examples/utilities/ssh-pod.yaml
COPY --from=virtbench-builder /opt/virtbench-venv /opt/virtbench-venv
COPY container-entrypoint.sh /usr/local/bin/
# Refresh UBI RPMs at build time; keep /var/lib/rpm so Trivy can detect OS packages.
RUN python3 -m pip install --no-cache-dir --upgrade 'pip==26.2.1' 'setuptools>=78.1.1' \
  && rm -rf /opt/virtbench-runtime/docs \
  && mkdir -p /home/harness/.config /home/harness/.local/share/containers \
  && chmod 0755 /usr/local/bin/container-entrypoint.sh \
  && chown -R 65532:65532 /opt/virtbench-runtime /home/harness \
  && if command -v microdnf >/dev/null 2>&1; then \
       microdnf update -y --nodocs; \
       microdnf clean all; \
     fi \
  && if command -v dnf >/dev/null 2>&1; then \
       dnf clean all; \
     fi \
  && rm -rf /var/lib/dnf/history* /var/cache/dnf
ENV PATH="/opt/virtbench-venv/bin:${PATH}"
ENV HOME=/home/harness
# The image runs as the non-root image user by default. Callers that need a
# different bind-mount mapping can override the container UID at runtime.
USER 65532
ENTRYPOINT ["/usr/local/bin/container-entrypoint.sh"]
