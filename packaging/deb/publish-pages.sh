#!/bin/sh
# Publie des paquets .deb dans un dépôt APT statique signé, servi par GitHub
# Pages depuis un dépôt dédié (Gogowwww/frp-manager-apt, branche gh-pages).
# Publishes .deb packages in a signed static APT repository, served by GitHub
# Pages from a dedicated repository (Gogowwww/frp-manager-apt, gh-pages branch).
#
#   sh packaging/deb/publish-pages.sh <distribution> fichier.deb…
#
# Variables : APT_GPG_KEY (clé privée GPG armorée, sans phrase de passe),
# APT_DEPLOY_KEY (clé SSH de déploiement, écriture sur le dépôt APT_REPO),
# APT_REPO (par défaut Gogowwww/frp-manager-apt), APT_DOMAIN (par défaut
# apt.frp-manager.org, écrit dans le fichier CNAME). Essai local sans GitHub :
# PAGES_LOCAL=<dossier> (le dépôt est construit dans ce dossier, sans clone ni
# push), avec APT_GPG_KEY.
# Variables: APT_GPG_KEY (armored private GPG key, no passphrase),
# APT_DEPLOY_KEY (SSH deploy key, write access to the APT_REPO repository),
# APT_REPO (default Gogowwww/frp-manager-apt), APT_DOMAIN (default
# apt.frp-manager.org, written to the CNAME file). Local trial without GitHub:
# PAGES_LOCAL=<folder> (the repository is built in that folder, no clone and no
# push), with APT_GPG_KEY.
#
# Disposition : pool/<distribution>/*.deb, dists/<distribution>/ (Release,
# InRelease, Release.gpg, Packages par architecture), repository.key, CNAME. La
# branche est réécrite en un seul commit à chaque publication (l'historique des
# binaires ne s'accumule pas). Le canal « dev » ne garde que les 5 versions les
# plus récentes par architecture.
# Layout: pool/<distribution>/*.deb, dists/<distribution>/ (Release, InRelease,
# Release.gpg, Packages per architecture), repository.key, CNAME. The branch is
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
APT_REPO="${APT_REPO:-Gogowwww/frp-manager-apt}"

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
    URL="git@github.com:$APT_REPO.git"
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
echo "APT $DIST : publié sur $APT_REPO, gh-pages ($(ls "pool/$DIST" | wc -l) paquets)"
