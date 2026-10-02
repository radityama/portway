# syntax=docker/dockerfile:1
# Build versioned assets first; no network install or secrets in image layers.
ARG RUNTIME_IMAGE=gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
FROM ${RUNTIME_IMAGE}
ARG VERSION
ARG TARGETARCH
ARG REVISION
LABEL org.opencontainers.image.source="https://github.com/radityama/portway" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${REVISION}"
COPY --chmod=0755 dist/releases/${VERSION}/portway_${VERSION}_linux_${TARGETARCH} /usr/local/bin/portway
COPY --chmod=0755 dist/releases/${VERSION}/portway-relay_${VERSION}_linux_${TARGETARCH} /usr/local/bin/portway-relay
COPY --chmod=0755 dist/releases/${VERSION}/portway-cert_${VERSION}_linux_${TARGETARCH} /usr/local/bin/portway-cert
USER 65532:65532
ENTRYPOINT ["/usr/local/bin/portway-relay"]
