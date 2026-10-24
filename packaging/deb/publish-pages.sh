#!/bin/sh
# Publie des paquets .deb dans un dépôt APT statique signé, servi par GitHub
# Pages depuis un dépôt dédié (Gogowwww/frp-manager-apt, branche gh-pages).
# Publishes .deb packages in a signed static APT repository, served by GitHub
# Pages from a dedicated repository (Gogowwww/frp-manager-apt, gh-pages branch).
#
#   sh packaging/deb/publish-pages.sh <distribution> fichier.deb…
#
# Variables : APT_GPG_KEY (clé privée GPG armorée, sans phrase de passe),
# APT_DEPLOY_KEY (clé SSH de déploiement, écriture sur le dépôt APT_PAGES_REPO),
# APT_PAGES_REPO (par défaut Gogowwww/frp-manager-apt), APT_DOMAIN (par défaut
# apt.frp-manager.org, écrit dans le fichier CNAME). Essai local sans GitHub :
# PAGES_LOCAL=<dossier> (le dépôt est construit dans ce dossier, sans clone ni
# push), avec APT_GPG_KEY.
# Variables: APT_GPG_KEY (armored private GPG key, no passphrase),
# APT_DEPLOY_KEY (SSH deploy key, write access to the APT_PAGES_REPO repository),
# APT_PAGES_REPO (default Gogowwww/frp-manager-apt), APT_DOMAIN (default
# apt.frp-manager.org, written to the CNAME file). Local trial without GitHub:
# PAGES_LOCAL=<folder> (the repository is built in that folder, no clone and no
# push), with APT_GPG_KEY.
#
# Disposition : pool/<distribution>/*.deb, dists/<distribution>/ (Release,
# InRelease, Release.gpg, Packages par architecture), repository.key, CNAME et
# index.html (redirection vers frp-manager.org). La
# branche est réécrite en un seul commit à chaque publication (l'historique des
# binaires ne s'accumule pas). Le canal « dev » ne garde que les 5 versions les
# plus récentes par architecture.
# Layout: pool/<distribution>/*.deb, dists/<distribution>/ (Release, InRelease,
# Release.gpg, Packages per architecture), repository.key, CNAME and index.html
# (redirect to frp-manager.org). The branch is
# rewritten as a single commit on each publication (the binaries history does
# not pile up). The "dev" channel only keeps the 5 most recent versions per
# architecture.
#
# Côté machine :
#   deb [signed-by=/etc/apt/keyrings/frp-manager.asc] https://apt.frp-manager.org <distribution> main
set -eu

DIST="$1"
shift
: "${APT_GPG_KEY:?APT_GPG_KEY manquant}"
ARCHS="amd64 arm64 armhf"
APT_PAGES_REPO="${APT_PAGES_REPO:-Gogowwww/frp-manager-apt}"

command -v apt-ftparchive >/dev/null 2>&1 || sudo apt-get install -y -qq apt-utils >/dev/null

# Trousseau jetable : la clé privée ne reste pas sur le disque
# Throwaway keyring: the private key does not stay on disk
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
GNUPGHOME="$WORK/gnupg"
export GNUPGHOME
mkdir -m 700 "$GNUPGHOME"
printf '%s\n' "$APT_GPG_KEY" | gpg --batch --quiet --import
KEYID=$(gpg --list-secret-keys --with-colons | awk -F: '/^sec/ {print $5; exit}')
[ -n "$KEYID" ] || { echo "Aucune clé privée dans APT_GPG_KEY" >&2; exit 1; }

if [ -n "${PAGES_LOCAL:-}" ]; then
    REPO="$PAGES_LOCAL"
    mkdir -p "$REPO"
else
    : "${APT_DEPLOY_KEY:?APT_DEPLOY_KEY manquant}"
    URL="git@github.com:$APT_PAGES_REPO.git"
    umask 077
    printf '%s\n' "$APT_DEPLOY_KEY" > "$WORK/deploy"
    GIT_SSH_COMMAND="ssh -i $WORK/deploy -o IdentitiesOnly=yes -o StrictHostKeyChecking=accept-new -o UserKnownHostsFile=$WORK/known_hosts"
    export GIT_SSH_COMMAND
    REPO="$WORK/pages"
    if git ls-remote --exit-code --heads "$URL" gh-pages >/dev/null 2>&1; then
        git clone --quiet --depth 1 --branch gh-pages "$URL" "$REPO"
    else
        mkdir -p "$REPO"
    fi
fi

# Chemins absolus avant de changer de dossier / Absolute paths before changing folder
DEBS="$WORK/debs"
mkdir -p "$DEBS"
for f in "$@"; do
    cp "$f" "$DEBS/"
done

cd "$REPO"
mkdir -p "pool/$DIST"
cp "$DEBS"/*.deb "pool/$DIST/"

# Canal dev : 5 versions les plus récentes par architecture / dev channel: 5 most recent versions per architecture
if [ "$DIST" = dev ]; then
    for arch in $ARCHS; do
        ls -t "pool/dev/"*"_${arch}.deb" 2>/dev/null | tail -n +6 | while read -r old; do rm -f "$old"; done
    done
fi

for arch in $ARCHS; do
    dir="dists/$DIST/main/binary-$arch"
    mkdir -p "$dir"
    apt-ftparchive --arch "$arch" packages "pool/$DIST" > "$dir/Packages"
    gzip -9kf "$dir/Packages"
done

apt-ftparchive \
    -o "APT::FTPArchive::Release::Origin=frp-manager" \
    -o "APT::FTPArchive::Release::Label=frp-manager" \
    -o "APT::FTPArchive::Release::Suite=$DIST" \
    -o "APT::FTPArchive::Release::Codename=$DIST" \
    -o "APT::FTPArchive::Release::Architectures=$ARCHS" \
    -o "APT::FTPArchive::Release::Components=main" \
    release "dists/$DIST" > "dists/$DIST/Release.new"
mv "dists/$DIST/Release.new" "dists/$DIST/Release"
rm -f "dists/$DIST/InRelease" "dists/$DIST/Release.gpg"
gpg --batch --yes --local-user "$KEYID" --clearsign -o "dists/$DIST/InRelease" "dists/$DIST/Release"
gpg --batch --yes --local-user "$KEYID" --armor --detach-sign -o "dists/$DIST/Release.gpg" "dists/$DIST/Release"
gpg --batch --yes --armor --export "$KEYID" > repository.key
touch .nojekyll

# Racine : redirection vers le site du projet (Pages ne sait pas faire de 301).
# Les chemins du dépôt (dists, pool, repository.key) ne sont pas touchés ; pas de
# 404.html : un fichier absent doit rester une vraie 404 pour apt.
# Root: redirect to the project website (Pages cannot do a 301). The repository
# paths (dists, pool, repository.key) are untouched; no 404.html: a missing file
# must stay a real 404 for apt.
rm -f 404.html
cat > index.html <<'HTML'
<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>FRP Manager</title>
<meta name="robots" content="noindex">
<meta http-equiv="refresh" content="0; url=https://frp-manager.org/">
<link rel="canonical" href="https://frp-manager.org/">
</head>
<body>
<p>Redirecting to <a href="https://frp-manager.org/">frp-manager.org</a>…</p>
</body>
</html>
HTML
printf '%s\n' "${APT_DOMAIN:-apt.frp-manager.org}" > CNAME

if [ -n "${PAGES_LOCAL:-}" ]; then
    echo "APT $DIST : dépôt construit dans $REPO"
    exit 0
fi

# Un seul commit, poussé de force : seule la branche gh-pages est réécrite
# A single commit, force-pushed: only the gh-pages branch is rewritten
rm -rf .git
git init -q -b gh-pages
git add -A
git -c user.name="FRP Manager (apt)" -c user.email="apt@frp-manager.invalid" \
    commit -q -m "Dépôt APT : $DIST ($(date -u +%Y-%m-%dT%H:%MZ))"
git push -q --force "$URL" gh-pages
echo "APT $DIST : publié sur $APT_PAGES_REPO, gh-pages ($(ls "pool/$DIST" | wc -l) paquets)"
