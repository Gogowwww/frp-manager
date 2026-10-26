#!/bin/sh
# Publie des paquets .deb dans le dépôt APT du Forgejo (registre de paquets
# Debian, signé par Forgejo).
#
#   sh packaging/deb/publish-apt.sh <distribution> fichier.deb…
#
# Variables : FORGEJO_URL (https://forgejo.gogow.fr), APT_OWNER (administrator),
# et un ou plusieurs jetons essayés dans l'ordre : PACKAGES_TOKEN (jeton
# personnel, portée write:package), puis ACTIONS_TOKEN (jeton automatique du
# workflow). Un paquet déjà publié (même version) n'est pas une erreur.
#
# Côté machine, le dépôt s'ajoute ainsi (voir frp-manager.org/docs/installation/) :
#   deb [signed-by=/etc/apt/keyrings/frp-manager.asc] $FORGEJO_URL/api/packages/$APT_OWNER/debian <distribution> main
set -eu

DIST="$1"
shift
: "${FORGEJO_URL:?FORGEJO_URL manquant}"
: "${APT_OWNER:?APT_OWNER manquant}"
URL="$FORGEJO_URL/api/packages/$APT_OWNER/debian/pool/$DIST/main/upload"

for f in "$@"; do
    name=$(basename "$f")
    done_one=""
    for token in "${PACKAGES_TOKEN:-}" "${ACTIONS_TOKEN:-}"; do
        [ -n "$token" ] || continue
        code=$(curl -s -o /tmp/apt-upload.out -w '%{http_code}' -X PUT \
            -H "Authorization: token $token" --upload-file "$f" "$URL")
        case "$code" in
            200|201) echo "APT $DIST : $name publié"; done_one=1; break ;;
            409)     echo "APT $DIST : $name déjà présent"; done_one=1; break ;;
            401|403) continue ;;  # jeton refusé : le suivant
            *)       echo "APT $DIST : $name refusé (HTTP $code) : $(head -c 300 /tmp/apt-upload.out)" >&2; exit 1 ;;
        esac
    done
    if [ -z "$done_one" ]; then
        echo "APT $DIST : aucun jeton accepté pour publier $name." >&2
        echo "Créez un jeton Forgejo (Réglages → Applications, portée write:package) et ajoutez-le" >&2
        echo "aux secrets du dépôt sous le nom FORGEJO_PACKAGES_TOKEN, puis relancez le job." >&2
        exit 1
    fi
done
