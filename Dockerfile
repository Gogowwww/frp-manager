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

# ── Image ───────────────────────────────────────────────────────────────────
# nsenter (util-linux-misc) : commandes sur l'hôte (systemd, journal, nftables)
# avec pid: host + privileged, voir docker-compose.yml.
FROM alpine:3.22
RUN apk add --no-cache ca-certificates util-linux-misc tzdata
COPY --from=build /out/frp-manager /usr/local/bin/frp-manager
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
