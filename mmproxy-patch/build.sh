#!/usr/bin/env sh
# ╔═══════════════════════════════════════════════════════════════════╗
# ║  build.sh — compile go-mmproxy avec le patch UDP « par session »   ║
# ╚═══════════════════════════════════════════════════════════════════╝
#
# Clone go-mmproxy au commit épinglé, remplace udp.go par notre version
# session-aware (nécessaire pour l'IP réelle en UDP avec frp >= 0.67), puis
# compile un binaire statique.
#
# Usage :
#   ./build.sh /chemin/de/sortie/go-mmproxy
# Variables d'env respectées : GOOS, GOARCH, GOARM (cross-compilation).
set -e

PIN_COMMIT="006247ca7ec618d2aff02052bac839ca769991a1"
PATCH_DIR="$(cd "$(dirname "$0")" && pwd)"

OUT="${1:-$PWD/go-mmproxy}"
case "$OUT" in
  /*) : ;;                       # déjà absolu
  *)  OUT="$PWD/$OUT" ;;         # rendre absolu (on va cd ailleurs)
esac

command -v git >/dev/null 2>&1 || { echo "git requis" >&2; exit 1; }
command -v go  >/dev/null 2>&1 || { echo "go requis"  >&2; exit 1; }

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

git clone --quiet https://github.com/path-network/go-mmproxy.git "$TMP/src"
cd "$TMP/src"
git checkout --quiet "$PIN_COMMIT"
cp "$PATCH_DIR/udp.go" udp.go

CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o "$OUT" .
echo "go-mmproxy (patch UDP) compilé : $OUT"
