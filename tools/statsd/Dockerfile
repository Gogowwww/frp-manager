# Image du récepteur de statistiques (tools/statsd) : un programme statique dans
# une image vide, qui tourne sans privilège. À construire depuis la racine du dépôt :
#   docker build -f tools/statsd/Dockerfile -t frp-manager-statsd .
# Image of the statistics receiver (tools/statsd): a static program in an empty
# image, running unprivileged. Build it from the repository root:
#   docker build -f tools/statsd/Dockerfile -t frp-manager-statsd .

FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build
ARG TARGETOS TARGETARCH
WORKDIR /src
COPY go.mod ./
COPY tools/statsd ./tools/statsd
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags "-s -w" -o /out/frp-manager-statsd ./tools/statsd \
 && mkdir -p /out/data

FROM scratch
COPY --from=build /out/frp-manager-statsd /frp-manager-statsd
COPY --from=build --chown=65532:65532 /out/data /data
ENV LISTEN=:8080 STATS_FILE=/data/stats.json
USER 65532:65532
VOLUME /data
EXPOSE 8080
HEALTHCHECK --interval=60s --timeout=5s --retries=3 CMD ["/frp-manager-statsd", "-healthcheck"]
ENTRYPOINT ["/frp-manager-statsd"]
LABEL org.opencontainers.image.source="https://github.com/Gogowwww/frp-manager" \
      org.opencontainers.image.description="Receiver of FRP Manager's opt-in anonymous statistics" \
      org.opencontainers.image.licenses="Apache-2.0"
