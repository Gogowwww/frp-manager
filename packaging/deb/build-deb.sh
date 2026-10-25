#!/bin/sh
# Paquets .deb de FRP Manager, un par architecture, à partir des binaires
# compilés (Dockerfile, cible « binaries »).
#
#   sh packaging/deb/build-deb.sh 0.0.60 dist/bin dist/deb
#
# Le paquet installe /usr/bin/frp-manager et son service ; postinst prépare
# frps/frpc comme install.sh (services, configurations par défaut, binaires).
set -eu

VERSION="$1"
BIN_DIR="$2"
OUT_DIR="$3"
HERE="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$HERE/../.." && pwd)"

mkdir -p "$OUT_DIR"
for pair in amd64:amd64 arm64:arm64 armv7:armhf; do
    goarch="${pair%%:*}"
    debarch="${pair#*:}"
    src="$BIN_DIR/frp-manager-linux-$goarch"
    [ -f "$src" ] || { echo "binaire absent : $src" >&2; exit 1; }

    stage="$(mktemp -d)"
    install -D -m 755 "$src" "$stage/usr/bin/frp-manager"
    install -D -m 644 "$HERE/frp-manager.service" "$stage/lib/systemd/system/frp-manager.service"
    install -D -m 644 "$ROOT/LICENSE" "$stage/usr/share/doc/frp-manager/copyright"
    install -D -m 644 "$ROOT/README.md" "$stage/usr/share/doc/frp-manager/README.md"
    # Clé publique du dépôt apt (apt.frp-manager.org) : le postinst la pose chez les
    # machines encore abonnées à l'ancien dépôt
    # Public key of the apt repository (apt.frp-manager.org): the postinst puts it on
    # machines still subscribed to the old repository
    install -D -m 644 "$HERE/apt-repository.asc" "$stage/usr/share/frp-manager/apt-repository.asc"
    mkdir -p "$stage/DEBIAN"
    for script in postinst prerm postrm; do
        install -m 755 "$HERE/$script" "$stage/DEBIAN/$script"
    done
    size=$(du -sk "$stage" | cut -f1)
    sed -e "s/@VERSION@/$VERSION/" -e "s/@ARCH@/$debarch/" -e "s/@SIZE@/$size/" \
        "$HERE/control" > "$stage/DEBIAN/control"

    out="$OUT_DIR/frp-manager_${VERSION}_${debarch}.deb"
    dpkg-deb --root-owner-group -Zxz --build "$stage" "$out" >/dev/null
    rm -rf "$stage"
    echo "$out"
done
