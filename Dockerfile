FROM debian:bookworm-slim

# On utilise apt-get uniquement (pas pip) pour éviter les problèmes de proxy PyPI
# python3-flask et python3-requests sont dans les dépôts Debian officiels
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

EXPOSE 8765

CMD ["python3", "app.py"]
