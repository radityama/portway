# syntax=docker/dockerfile:1
ARG BASE_IMAGE=postgres:17-alpine
FROM ${BASE_IMAGE}
# Proxy trust is supplied only during the package-install layer, never copied
# into the image. Docker's inherited client proxy settings remain in effect.
RUN --mount=type=secret,id=proxy_ca \
    if [ -s /run/secrets/proxy_ca ]; then \
      SSL_CERT_FILE=/run/secrets/proxy_ca apk add --no-cache iproute2 iptables; \
    else apk add --no-cache iproute2 iptables; fi
COPY netem-proxy /usr/local/bin/netem-proxy
ENTRYPOINT ["/usr/local/bin/netem-proxy"]
