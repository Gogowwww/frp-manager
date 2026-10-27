#!/bin/sh
# Publie des paquets .deb dans le dépôt APT statique signé apt.frp-manager.org,
# servi par Cloudflare Pages (projet frp-manager-apt, envoi direct avec wrangler).
# Publishes .deb packages in the signed static APT repository apt.frp-manager.org,
# served by Cloudflare Pages (frp-manager-apt project, direct upload with wrangler).
#
#   sh packaging/deb/publish-pages.sh <distribution> fichier.deb…
#   APT_SOURCE_URL=<url> sh packaging/deb/publish-pages.sh --copy
#
# Pages n'a pas de mémoire entre deux envois : chaque publication relit d'abord le
# dépôt servi par le déploiement de production du projet (ou APT_SOURCE_URL),
# vérifie ses signatures (packaging/deb/apt-repository.asc) et ses sommes SHA-256,
# ajoute les paquets, régénère et signe la seule distribution publiée (les autres
# restent octet pour octet), puis envoie le tout. --copy recopie APT_SOURCE_URL
# telle quelle (amorçage depuis l'ancien serveur, retour arrière).
# Pages has no memory between two uploads: each publication first reads back the
# repository served by the project's production deployment (or APT_SOURCE_URL),
# checks its signatures (packaging/deb/apt-repository.asc) and SHA-256 sums, adds
# the packages, regenerates and signs only the published distribution (the others
# stay byte for byte), then uploads everything. --copy copies APT_SOURCE_URL as is
# (bootstrap from the old server, rollback).
#
# Variables : APT_GPG_KEY (clé privée GPG armorée, sans phrase de passe ; inutile
# avec --copy), CLOUDFLARE_API_TOKEN (permission « Cloudflare Pages : Edit »),
# CLOUDFLARE_ACCOUNT_ID, APT_PAGES_PROJECT (par défaut frp-manager-apt),
# APT_SOURCE_URL (dépôt de départ ; par défaut le déploiement de production du
# projet ; « - » : dépôt vide), APT_DISTS (distributions relues, par défaut
# « stable testing dev »). Essai local sans Cloudflare : PAGES_LOCAL=<dossier>
# (le dépôt est construit dans ce dossier, sans envoi ; relu depuis APT_SOURCE_URL
# si elle est donnée), avec APT_GPG_KEY.
# Variables: APT_GPG_KEY (armored private GPG key, no passphrase; not needed with
# --copy), CLOUDFLARE_API_TOKEN ("Cloudflare Pages: Edit" permission),
# CLOUDFLARE_ACCOUNT_ID, APT_PAGES_PROJECT (default frp-manager-apt),
# APT_SOURCE_URL (starting repository; default the project's production
# deployment; "-": empty repository), APT_DISTS (distributions read back, default
# "stable testing dev"). Local trial without Cloudflare: PAGES_LOCAL=<folder> (the
# repository is built in that folder, no upload; read back from APT_SOURCE_URL if
# given), with APT_GPG_KEY.
#
# Disposition : pool/<distribution>/*.deb, dists/<distribution>/ (Release,
# InRelease, Release.gpg, Packages par architecture), repository.key, _headers
# (types MIME) et _redirects (la racine renvoie vers frp-manager.org par une 301).
# Ni index.html ni 404.html : sans eux, Pages répond une vraie 404 pour un fichier
# absent, ce qu'attend apt. Le canal « dev » ne garde que les 5 versions les plus
# récentes par architecture ; stable et testing gardent tout. Limites de Pages :
# 20 000 fichiers et 25 Mio par fichier, vérifiées avant l'envoi.
# Layout: pool/<distribution>/*.deb, dists/<distribution>/ (Release, InRelease,
# Release.gpg, Packages per architecture), repository.key, _headers (MIME types)
# and _redirects (the root redirects to frp-manager.org with a 301). Neither
# index.html nor 404.html: without them, Pages answers a real 404 for a missing
# file, which is what apt expects. The "dev" channel only keeps the 5 most recent
# versions per architecture; stable and testing keep everything. Pages limits:
# 20,000 files and 25 MiB per file, checked before the upload.
#
# Outils : curl, gpg, gpgv, apt-ftparchive (apt-utils), jq, npx (Node.js 22 ou plus).
# Tools: curl, gpg, gpgv, apt-ftparchive (apt-utils), jq, npx (Node.js 22 or later).
#
# Côté machine :
#   deb [signed-by=/etc/apt/keyrings/frp-manager.asc] https://apt.frp-manager.org <distribution> main
set -eu

DIST="$1"
shift
ARCHS="amd64 arm64 armhf"
APT_DISTS="${APT_DISTS:-stable testing dev}"
APT_PAGES_PROJECT="${APT_PAGES_PROJECT:-frp-manager-apt}"
# Version de wrangler épinglée, essayée avec ce script ; la changer pour une version
# publiée depuis au moins deux semaines (une version toute neuve peut être piégée).
# Pinned wrangler version, tried with this script; change it for a version
# published at least two weeks ago (a brand new version may be booby-trapped).
WRANGLER="npx --yes wrangler@4.149.0"
HERE=$(cd "$(dirname "$0")" && pwd)

if [ "$DIST" = --copy ]; then
    : "${APT_SOURCE_URL:?APT_SOURCE_URL manquant (dépôt à recopier)}"
    [ "$APT_SOURCE_URL" != - ] || { echo "--copy a besoin d'un dépôt à recopier, pas de « - »" >&2; exit 1; }
else
    : "${APT_GPG_KEY:?APT_GPG_KEY manquant}"
    # Une distribution hors de APT_DISTS ne serait pas relue à l'envoi suivant
    # A distribution outside APT_DISTS would not be read back on the next upload
    case " $APT_DISTS " in
        *" $DIST "*) ;;
        *) echo "Distribution inconnue : $DIST (attendu : $APT_DISTS)" >&2; exit 1 ;;
    esac
    command -v apt-ftparchive >/dev/null 2>&1 || sudo apt-get install -y -qq apt-utils >/dev/null
fi
if [ -z "${PAGES_LOCAL:-}" ]; then
    : "${CLOUDFLARE_API_TOKEN:?CLOUDFLARE_API_TOKEN manquant}"
    : "${CLOUDFLARE_ACCOUNT_ID:?CLOUDFLARE_ACCOUNT_ID manquant}"
fi

# Trousseau jetable : la clé privée ne reste pas sur le disque
# Throwaway keyring: the private key does not stay on disk
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
GNUPGHOME="$WORK/gnupg"
export GNUPGHOME
mkdir -m 700 "$GNUPGHOME"
# Clé publique de confiance pour relire le dépôt : celle du dépôt git, pas celle servie
# Trusted public key to read the repository back: the git repository's, not the served one
gpg --batch --quiet --dearmor < "$HERE/apt-repository.asc" > "$WORK/trusted.gpg"

# URL du déploiement de production du projet (vide s'il n'y en a pas). Un refus de
# l'API (jeton, compte, projet) arrête le script : sinon il passerait pour un
# projet vide.
# URL of the project's production deployment (empty if there is none). An API
# refusal (token, account, project) stops the script: otherwise it would pass for
# an empty project.
production_url() {
    curl -fsS --retry 3 -o "$WORK/project.json" -H "Authorization: Bearer $CLOUDFLARE_API_TOKEN" \
        "https://api.cloudflare.com/client/v4/accounts/$CLOUDFLARE_ACCOUNT_ID/pages/projects/$APT_PAGES_PROJECT" \
        || { echo "API Cloudflare : projet $APT_PAGES_PROJECT illisible" >&2; exit 1; }
    jq -r '.result.canonical_deployment.url // empty' "$WORK/project.json"
}

# Relit le dépôt servi à l'adresse $1 dans le dossier $2 : seulement les fichiers
# cités par les index signés, chacun vérifié (signature, puis SHA-256).
# Reads the repository served at address $1 into folder $2: only the files listed
# by the signed indexes, each one checked (signature, then SHA-256).
mirror() {
    src="${1%/}"
    dst="$2"
    mkdir -p "$dst"
    curl -fsSL --retry 3 -o "$dst/repository.key" "$src/repository.key" \
        || { echo "Pas de repository.key sur $src : ce n'est pas le dépôt apt" >&2; return 1; }
    for d in $APT_DISTS; do
        code=$(curl -sS --retry 3 -o /dev/null -w '%{http_code}' "$src/dists/$d/InRelease")
        case "$code" in
            200) ;;
            404) echo "$d : absente de $src"; continue ;;
            *) echo "$src/dists/$d/InRelease : HTTP $code" >&2; return 1 ;;
        esac
        mkdir -p "$dst/dists/$d"
        for f in InRelease Release Release.gpg; do
            curl -fsS --retry 3 -o "$dst/dists/$d/$f" "$src/dists/$d/$f"
        done
        gpgv --quiet --keyring "$WORK/trusted.gpg" "$dst/dists/$d/InRelease" 2>/dev/null \
            || { echo "$d : signature InRelease invalide" >&2; return 1; }
        gpgv --quiet --keyring "$WORK/trusted.gpg" "$dst/dists/$d/Release.gpg" "$dst/dists/$d/Release" 2>/dev/null \
            || { echo "$d : signature Release.gpg invalide" >&2; return 1; }
        # Index cités par Release (section SHA256), sauf Release lui-même
        # Indexes listed by Release (SHA256 section), except Release itself
        awk '/^SHA256:/ {s=1; next} /^[^ ]/ {s=0} s && $3 != "Release" {print $1, $3}' "$dst/dists/$d/Release" \
            > "$WORK/index.lst"
        while read -r sum path; do
            mkdir -p "$(dirname "$dst/dists/$d/$path")"
            curl -fsS --retry 3 -o "$dst/dists/$d/$path" "$src/dists/$d/$path"
            printf '%s  %s\n' "$sum" "$dst/dists/$d/$path" | sha256sum -c --quiet - \
                || { echo "dists/$d/$path : somme SHA-256 fausse" >&2; return 1; }
        done < "$WORK/index.lst"
        # Paquets cités par les Packages des architectures
        # Packages listed by the per-architecture Packages files
        for p in "$dst/dists/$d"/main/binary-*/Packages; do
            [ -f "$p" ] || continue
            awk '/^Filename: / {f=$2} /^SHA256: / {print $2, f}' "$p"
        done | sort -u > "$WORK/pool.lst"
        while read -r sum path; do
            mkdir -p "$(dirname "$dst/$path")"
            curl -fsS --retry 3 -o "$dst/$path" "$src/$path"
            printf '%s  %s\n' "$sum" "$dst/$path" | sha256sum -c --quiet - \
                || { echo "$path : somme SHA-256 fausse" >&2; return 1; }
        done < "$WORK/pool.lst"
        echo "$d : relue ($(wc -l < "$WORK/pool.lst") paquets)"
    done
}

# Dépôt de départ / Starting repository
if [ -n "${PAGES_LOCAL:-}" ]; then
    REPO="$PAGES_LOCAL"
    mkdir -p "$REPO"
    if [ -n "${APT_SOURCE_URL:-}" ] && [ "$APT_SOURCE_URL" != - ]; then
        mirror "$APT_SOURCE_URL" "$REPO"
    fi
else
    REPO="$WORK/site"
    SRC="${APT_SOURCE_URL:-}"
    [ -n "$SRC" ] || SRC=$(production_url)
    # Sans déploiement en production, partir de rien effacerait les autres
    # distributions : il faut le demander (APT_SOURCE_URL=-) ou faire --copy.
    # Without a production deployment, starting from nothing would erase the other
    # distributions: it has to be asked for (APT_SOURCE_URL=-) or done with --copy.
    [ -n "$SRC" ] || { echo "Projet $APT_PAGES_PROJECT sans déploiement en production : amorcer avec --copy ou APT_SOURCE_URL=-" >&2; exit 1; }
    mkdir -p "$REPO"
    if [ "$SRC" != - ]; then
        echo "Relecture de $SRC"
        mirror "$SRC" "$REPO"
    fi
fi

if [ "$DIST" != --copy ]; then
    printf '%s\n' "$APT_GPG_KEY" | gpg --batch --quiet --import
    KEYID=$(gpg --list-secret-keys --with-colons | awk -F: '/^sec/ {print $5; exit}')
    [ -n "$KEYID" ] || { echo "Aucune clé privée dans APT_GPG_KEY" >&2; exit 1; }

    # Chemins absolus avant de changer de dossier / Absolute paths before changing folder
    DEBS="$WORK/debs"
    mkdir -p "$DEBS"
    for f in "$@"; do
        cp "$f" "$DEBS/"
    done

    cd "$REPO"
    mkdir -p "pool/$DIST"
    cp "$DEBS"/*.deb "pool/$DIST/"

    # Canal dev : 5 versions les plus récentes par architecture, triées par numéro de
    # version (les dates des fichiers relus ne disent rien de leur âge).
    # dev channel: 5 most recent versions per architecture, sorted by version number
    # (the dates of the files read back say nothing about their age).
    if [ "$DIST" = dev ]; then
        for arch in $ARCHS; do
            ls "pool/dev/"*"_${arch}.deb" 2>/dev/null | sort -rV | tail -n +6 | while read -r old; do rm -f "$old"; done
        done
    fi

    for arch in $ARCHS; do
        dir="dists/$DIST/main/binary-$arch"
        mkdir -p "$dir"
        apt-ftparchive --arch "$arch" packages "pool/$DIST" > "$dir/Packages"
        gzip -9kf "$dir/Packages"
    done

    # Ancien Release retiré avant : sinon apt-ftparchive le cite dans le nouveau
    # Old Release removed first: otherwise apt-ftparchive lists it in the new one
    rm -f "dists/$DIST/Release" "dists/$DIST/InRelease" "dists/$DIST/Release.gpg"
    apt-ftparchive \
        -o "APT::FTPArchive::Release::Origin=frp-manager" \
        -o "APT::FTPArchive::Release::Label=frp-manager" \
        -o "APT::FTPArchive::Release::Suite=$DIST" \
        -o "APT::FTPArchive::Release::Codename=$DIST" \
        -o "APT::FTPArchive::Release::Architectures=$ARCHS" \
        -o "APT::FTPArchive::Release::Components=main" \
        release "dists/$DIST" > "$WORK/Release"
    mv "$WORK/Release" "dists/$DIST/Release"
    gpg --batch --yes --local-user "$KEYID" --clearsign -o "dists/$DIST/InRelease" "dists/$DIST/Release"
    gpg --batch --yes --local-user "$KEYID" --armor --detach-sign -o "dists/$DIST/Release.gpg" "dists/$DIST/Release"
    gpg --batch --yes --armor --export "$KEYID" > repository.key
fi

cd "$REPO"
# Restes de l'ancien hébergement GitHub Pages / Leftovers of the old GitHub Pages hosting
rm -f index.html 404.html CNAME .nojekyll

# Racine : 301 vers le site. Types MIME : .key serait servi comme une présentation
# Keynote, les index comme des binaires.
# Root: 301 to the website. MIME types: .key would be served as a Keynote
# presentation, the indexes as binaries.
printf '/ https://frp-manager.org/ 301\n' > _redirects
cat > _headers <<'HEADERS'
/repository.key
  Content-Type: application/pgp-keys
/dists/:dist/Release
  Content-Type: text/plain; charset=utf-8
/dists/:dist/InRelease
  Content-Type: text/plain; charset=utf-8
/dists/:dist/Release.gpg
  Content-Type: application/pgp-signature
/dists/:dist/main/:arch/Packages
  Content-Type: text/plain; charset=utf-8
/pool/*
  Content-Type: application/vnd.debian.binary-package
HEADERS

# Limites de Cloudflare Pages / Cloudflare Pages limits
files=$(find . -type f | wc -l)
[ "$files" -le 20000 ] || { echo "$files fichiers : au-delà des 20 000 de Pages" >&2; exit 1; }
big=$(find . -type f -size +26214400c)
[ -z "$big" ] || { echo "Fichiers de plus de 25 Mio, refusés par Pages : $big" >&2; exit 1; }
echo "Dépôt : $files fichiers, $(du -sh . | cut -f1)"

if [ -n "${PAGES_LOCAL:-}" ]; then
    echo "APT $DIST : dépôt construit dans $REPO"
    exit 0
fi

# Envoi depuis un dossier vide : wrangler ne doit lire aucune configuration du dépôt git
# Upload from an empty folder: wrangler must not read any configuration of the git repository
mkdir "$WORK/run"
cd "$WORK/run"
$WRANGLER pages deploy "$REPO" --project-name "$APT_PAGES_PROJECT" --branch main \
    --commit-dirty=true --commit-message "Dépôt APT : $DIST ($(date -u +%Y-%m-%dT%H:%MZ))"

# Le déploiement de production sert-il bien ce qui vient d'être envoyé, à son
# adresse propre puis à l'adresse de production du projet (celle que suivent les
# domaines) ? Cette dernière peut suivre avec quelques secondes de retard.
# Is the production deployment really serving what was just uploaded, at its own
# address then at the project's production address (the one the domains follow)?
# The latter may lag a few seconds behind.
NEW="$(production_url)"
for base in "$NEW" "https://$APT_PAGES_PROJECT.pages.dev"; do
    for d in $APT_DISTS; do
        [ -f "$REPO/dists/$d/InRelease" ] || continue
        i=0
        until curl -fsS --retry 3 -H 'Cache-Control: no-cache' "$base/dists/$d/InRelease" \
            | cmp -s - "$REPO/dists/$d/InRelease"; do
            i=$((i + 1))
            [ "$i" -lt 12 ] || { echo "$base ne sert pas le nouveau dists/$d/InRelease" >&2; exit 1; }
            sleep 5
        done
    done
done
if [ "$DIST" = --copy ]; then
    echo "APT : $APT_SOURCE_URL recopié sur Pages ($APT_PAGES_PROJECT, $NEW)"
else
    echo "APT $DIST : publié sur Pages ($APT_PAGES_PROJECT, $NEW, $(ls "$REPO/pool/$DIST" | wc -l) paquets)"
fi
