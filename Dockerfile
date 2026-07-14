# ── Étape 1 : compiler go-mmproxy (option « IP réelle » du panel) ────────────
# Binaire statique embarqué dans l'image ; le panel le copie sur l'hôte
# (/usr/local/bin) quand l'utilisateur active l'option depuis l'onglet Ports.
FROM golang:1.22-bookworm AS mmproxy-build
RUN CGO_ENABLED=0 go install github.com/path-network/go-mmproxy@latest

FROM debian:bookworm-slim

RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        python3 \
        python3-flask \
        python3-requests \
        openssl \
        util-linux \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /opt/frp-manager

COPY app.py .
COPY frp-autoupdate.py .
COPY templates/ templates/
COPY --from=mmproxy-build /go/bin/go-mmproxy bin/go-mmproxy

# Injecter la version du panel dans l'image via ARG/ENV
ARG PANEL_VERSION=unknown
ENV PANEL_DOCKER_VERSION=${PANEL_VERSION}

EXPOSE 8765

CMD ["python3", "app.py"]
