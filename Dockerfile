FROM python:3.11-slim

# openssl : génération du certificat TLS auto-signé
# util-linux : fournit nsenter (nécessaire pour contrôler systemd de l'hôte depuis Docker)
RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        openssl \
        util-linux \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /opt/frp-manager

RUN pip install --no-cache-dir --timeout 300 --retries 5 flask requests

COPY app.py .
COPY frp-autoupdate.py .
COPY templates/ templates/

EXPOSE 8765

CMD ["python", "app.py"]
