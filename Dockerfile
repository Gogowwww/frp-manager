# FRP Manager : un binaire Go, sans dépendance, dans une petite image Alpine.
#
#   docker buildx build --platform linux/amd64,linux/arm64,linux/arm/v7 --build-arg PANEL_VERSION=0.0.60 .
#
# La compilation tourne sur l'architecture du constructeur (compilation croisée
# de Go, sans émulation) ; seule l'image finale est propre à chaque architecture.
# --target binaries exporte les binaires des trois architectures (archive de release) :
#   docker buildx build --target binaries --output type=local,dest=dist/bin .

FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS source
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY internal/ internal/
COPY panel/ panel/
ARG PANEL_VERSION=unknown
ENV CGO_ENABLED=0 LDFLAGS="-s -w -X main.buildVersion=${PANEL_VERSION}"

# Tests (release.yml les lance avant toute publication) : docker buildx build --target test .
FROM source AS test
RUN go vet ./panel && go test -count=1 ./panel

# Binaire de l'architecture de l'image
FROM source AS build
ARG TARGETOS TARGETARCH TARGETVARIANT
RUN GOOS=$TARGETOS GOARCH=$TARGETARCH GOARM=${TARGETVARIANT#v} \
    go build -trimpath -ldflags "$LDFLAGS" -o /out/frp-manager ./panel

# Binaires des trois architectures, pour l'archive frp-manager.zip et les paquets
FROM source AS all-binaries
RUN GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "$LDFLAGS" -o /out/frp-manager-linux-amd64 ./panel \
 && GOOS=linux GOARCH=arm64 go build -trimpath -ldflags "$LDFLAGS" -o /out/frp-manager-linux-arm64 ./panel \
 && GOOS=linux GOARCH=arm GOARM=7 go build -trimpath -ldflags "$LDFLAGS" -o /out/frp-manager-linux-armv7 ./panel

FROM scratch AS binaries
COPY --from=all-binaries /out/ /

# frps et frpc embarqués (mode autonome) : archive officielle de fatedier/frp pour
# l'architecture de l'image, vérifiée avec la somme SHA-256 publiée avec la
# release. --build-arg FRP_VERSION=… choisit une autre version de frp.
# Embedded frps and frpc (standalone mode): official fatedier/frp archive for
# the image's architecture, checked against the SHA-256 published with the
# release. --build-arg FRP_VERSION=… picks another frp version.
FROM --platform=$BUILDPLATFORM alpine:3.22 AS frp
ARG FRP_VERSION=0.71.1
ARG TARGETARCH
RUN set -eu; \
    case "$TARGETARCH" in \
      amd64|arm64) arch="$TARGETARCH" ;; \
      arm) arch=arm ;; \
      *) echo "architecture non prise en charge / unsupported architecture: $TARGETARCH" >&2; exit 1 ;; \
    esac; \
    name="frp_${FRP_VERSION}_linux_${arch}"; \
    url="https://github.com/fatedier/frp/releases/download/v${FRP_VERSION}"; \
    cd /tmp; \
    wget -q -O "$name.tar.gz" "$url/$name.tar.gz"; \
    wget -q -O sums.txt "$url/frp_sha256_checksums.txt"; \
    sum=$(awk -v f="$name.tar.gz" '$2 == f { print $1 }' sums.txt); \
    [ -n "$sum" ] || { echo "pas de somme SHA-256 pour / no SHA-256 for $name.tar.gz" >&2; exit 1; }; \
    echo "$sum  $name.tar.gz" | sha256sum -c -; \
    tar -xzf "$name.tar.gz"; \
    mkdir -p /out; \
    install -m 0755 "$name/frps" "$name/frpc" /out/

# ── Image ───────────────────────────────────────────────────────────────────
# Deux modes (voir docs/installation.md) :
# - autonome (docker-compose.yml) : le panel lance frps et frpc de l'image ;
#   nftables (pare-feu, capacité NET_ADMIN) et ss (ports écoutés) servent dans
#   le conteneur ;
# - hôte (docker-compose.host.yml) : nsenter (util-linux-misc) lance les
#   commandes sur l'hôte (systemd, journal, nftables) avec pid: host + privileged.
# Two modes (see docs/installation.md):
# - standalone (docker-compose.yml): the panel runs the image's frps and frpc;
#   nftables (firewall, NET_ADMIN capability) and ss (listening ports) are used
#   inside the container;
# - host (docker-compose.host.yml): nsenter (util-linux-misc) runs the commands
#   on the host (systemd, journal, nftables) with pid: host + privileged.
FROM alpine:3.22
RUN apk add --no-cache ca-certificates util-linux-misc tzdata nftables iproute2-ss
COPY --from=build /out/frp-manager /usr/local/bin/frp-manager
COPY --from=frp /out/ /opt/frp-manager/bin/
ARG PANEL_VERSION=unknown
ENV PANEL_DOCKER_VERSION=${PANEL_VERSION} DOCKER_MODE=true
# source : GitHub relie le package ghcr.io au dépôt (onglet Packages du dépôt) ;
# les workflows posent les mêmes annotations sur l'index multi-architecture.
LABEL org.opencontainers.image.source="https://github.com/Gogowwww/frp-manager" \
      org.opencontainers.image.url="https://frp-manager.org" \
      org.opencontainers.image.documentation="https://frp-manager.org/docs/" \
      org.opencontainers.image.title="FRP Manager" \
      org.opencontainers.image.description="Web panel to manage frp (frps and frpc): ports, firewall, logs, updates" \
      org.opencontainers.image.licenses="Apache-2.0" \
      org.opencontainers.image.version="${PANEL_VERSION}"
EXPOSE 8765
ENTRYPOINT ["/usr/local/bin/frp-manager"]
