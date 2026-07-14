# ── Étape 1 : compiler go-mmproxy (option « IP réelle » du panel) ────────────
# Binaire statique embarqué dans l'image ; le panel le copie sur l'hôte
# (/usr/local/bin) quand l'utilisateur active l'option depuis l'onglet Ports.
# On applique le patch UDP « par session » (mmproxy-patch/) pour supporter
# l'IP réelle en UDP avec frp >= 0.67 (en-tête PROXY seulement au 1er paquet).
FROM golang:1.22-bookworm AS mmproxy-build
RUN apt-get update \
    && apt-get install -y --no-install-recommends git ca-certificates \
    && rm -rf /var/lib/apt/lists/*
COPY mmproxy-patch/ /mmproxy-patch/
RUN sh /mmproxy-patch/build.sh /go-mmproxy-bin

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
COPY mmproxy-patch/ mmproxy-patch/
COPY --from=mmproxy-build /go-mmproxy-bin bin/go-mmproxy

# Injecter la version du panel dans l'image via ARG/ENV
ARG PANEL_VERSION=unknown
ENV PANEL_DOCKER_VERSION=${PANEL_VERSION}

EXPOSE 8765

CMD ["python3", "app.py"]
