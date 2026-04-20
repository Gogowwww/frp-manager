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

# Injecter la version du panel dans l'image via ARG/ENV
ARG PANEL_VERSION=unknown
ENV PANEL_DOCKER_VERSION=${PANEL_VERSION}

EXPOSE 8765

CMD ["python3", "app.py"]
