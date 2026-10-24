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
# InRelease, Release.gpg, Packages par architecture), repository.key, CNAME,
# index.html et 404.html (page d'accueil). La
# branche est réécrite en un seul commit à chaque publication (l'historique des
# binaires ne s'accumule pas). Le canal « dev » ne garde que les 5 versions les
# plus récentes par architecture.
# Layout: pool/<distribution>/*.deb, dists/<distribution>/ (Release, InRelease,
# Release.gpg, Packages per architecture), repository.key, CNAME, index.html and
# 404.html (home page). The branch is
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

# Page d'accueil : comment ajouter le dépôt, avec l'empreinte de la clé
# Home page: how to add the repository, with the key fingerprint
FPR=$(gpg --list-keys --with-colons --fingerprint "$KEYID" | awk -F: '/^fpr/ {print $10; exit}' | sed 's/\(....\)/\1 /g; s/ $//')
DOMAIN="${APT_DOMAIN:-apt.frp-manager.org}"
cat > index.html <<HTML
<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="color-scheme" content="light dark">
<title>FRP Manager apt repository</title>
<meta name="description" content="Signed apt repository of FRP Manager, the web panel for frp (frps and frpc).">
<link rel="canonical" href="https://$DOMAIN/">
<style>
:root{--bg:#fff;--fg:#14202b;--mut:#5b6b79;--card:#f3f6f8;--acc:#0e9f6e;--line:#d9e1e7}
@media (prefers-color-scheme:dark){:root{--bg:#0e1319;--fg:#e6edf3;--mut:#93a1ae;--card:#161d25;--line:#26313c}}
*{box-sizing:border-box}
body{margin:0;background:var(--bg);color:var(--fg);font:16px/1.6 system-ui,-apple-system,"Segoe UI",sans-serif}
main{max-width:46rem;margin:0 auto;padding:2.5rem 1.25rem 4rem}
h1{font-size:1.8rem;margin:0 0 .25rem}
h2{font-size:1.15rem;margin:2rem 0 .5rem}
p{margin:.5rem 0}
.mut{color:var(--mut)}
pre{background:var(--card);border:1px solid var(--line);border-radius:8px;padding:.9rem 1rem;overflow-x:auto;font:.9rem/1.5 ui-monospace,SFMono-Regular,Menlo,Consolas,monospace}
code{font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;font-size:.92em}
a{color:var(--acc)}
table{border-collapse:collapse;width:100%;font-size:.95rem}
td,th{border-bottom:1px solid var(--line);padding:.45rem .5rem;text-align:left;vertical-align:top}
.fpr{word-break:break-word}
hr{border:0;border-top:1px solid var(--line);margin:2.5rem 0}
</style>
</head>
<body>
<main>
<h1>FRP Manager apt repository</h1>
<p class="mut">Signed packages for Debian and Ubuntu (amd64, arm64, armhf). <a href="https://frp-manager.org">frp-manager.org</a> · <a href="https://github.com/Gogowwww/frp-manager">GitHub</a></p>

<h2>Install</h2>
<pre>sudo install -d -m 0755 /etc/apt/keyrings
sudo curl -fsSL https://$DOMAIN/repository.key -o /etc/apt/keyrings/frp-manager.asc
echo "deb [signed-by=/etc/apt/keyrings/frp-manager.asc] https://$DOMAIN stable main" \
  | sudo tee /etc/apt/sources.list.d/frp-manager.list
sudo apt update &amp;&amp; sudo apt install frp-manager</pre>
<p>Updates then arrive with <code>sudo apt update &amp;&amp; sudo apt upgrade</code>.</p>

<h2>Channels</h2>
<table>
<tr><th>Distribution</th><th>Content</th></tr>
<tr><td><code>stable</code></td><td>Final releases. This is the one to use.</td></tr>
<tr><td><code>testing</code></td><td>Pre-releases, before they become final.</td></tr>
<tr><td><code>dev</code></td><td>Snapshots of the main branch, for testing only.</td></tr>
</table>

<h2>Check the key</h2>
<p>Packages are signed. Compare the fingerprint of the downloaded key with this one:</p>
<pre class="fpr">$FPR</pre>
<pre>gpg --show-keys --with-fingerprint /etc/apt/keyrings/frp-manager.asc</pre>

<hr>
<h2>Dépôt apt de FRP Manager</h2>
<p>Paquets signés pour Debian et Ubuntu (amd64, arm64, armhf). Les commandes ci-dessus sont les mêmes : ajoutez la clé et la source, puis <code>sudo apt install frp-manager</code>. La distribution <code>stable</code> reçoit les versions définitives, <code>testing</code> les pré-releases et <code>dev</code> des instantanés de test. Comparez l'empreinte de la clé téléchargée avec celle ci-dessus avant de lui faire confiance.</p>
<p class="mut">Ce dépôt est généré automatiquement depuis <a href="https://github.com/Gogowwww/frp-manager">Gogowwww/frp-manager</a> : ne le modifiez pas à la main.</p>
</main>
</body>
</html>
HTML
cp index.html 404.html
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
