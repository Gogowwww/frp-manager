#!/usr/bin/env bash
# ╔═══════════════════════════════════════════════════════════════════╗
# ║   release-docker.sh — Build & push de l'image frp-manager (GHCR)  ║
# ╚═══════════════════════════════════════════════════════════════════╝
#
# Contournement manuel du workflow GitHub Actions (utile tant que le
# compte GitHub est bloqué pour facturation : Actions ne tourne pas,
# mais GHCR accepte toujours les push manuels).
#
# Usage :
#   ./release-docker.sh 0.0.19     # version explicite
#   ./release-docker.sh            # déduit la version du dernier tag git
#
# Prérequis : être connecté à GHCR au préalable —
#   echo "TON_TOKEN" | docker login ghcr.io -u Gogowwww --password-stdin
#   (token classique avec le scope write:packages)

set -euo pipefail

IMAGE="ghcr.io/gogowwww/frp-manager"

# ── 1. Déterminer la version ─────────────────────────────────────────
if [[ $# -ge 1 ]]; then
    VERSION="${1#v}"                       # retire un éventuel "v" en préfixe
else
    git fetch --tags --quiet || true
    VERSION="$(git describe --tags --abbrev=0 2>/dev/null | sed 's/^v//' || true)"
    if [[ -z "$VERSION" ]]; then
        echo "❌ Impossible de déduire la version. Passe-la en argument :"
        echo "   ./release-docker.sh 0.0.19"
        exit 1
    fi
fi
echo "==> Version ciblée : $VERSION"

# ── 2. Récupérer le code à jour ──────────────────────────────────────
echo "==> git pull…"
git pull --ff-only

# ── 3. Build (les deux tags en un seul build) ────────────────────────
echo "==> Build $IMAGE:$VERSION (+ latest)…"
docker build --network=host \
    --build-arg PANEL_VERSION="$VERSION" \
    -t "$IMAGE:latest" \
    -t "$IMAGE:$VERSION" \
    .

# ── 4. Push ──────────────────────────────────────────────────────────
echo "==> Push…"
if ! docker push "$IMAGE:latest" || ! docker push "$IMAGE:$VERSION"; then
    echo
    echo "❌ Push refusé. Connecte-toi à GHCR puis relance ce script :"
    echo '   echo "TON_TOKEN" | docker login ghcr.io -u Gogowwww --password-stdin'
    exit 1
fi

echo
echo "✅ Image publiée : $IMAGE:$VERSION  (+ latest)"
echo "   Sur le serveur cible : docker pull $IMAGE:latest && docker restart frp-manager"
