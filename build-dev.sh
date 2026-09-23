#!/usr/bin/env bash
# ╔═══════════════════════════════════════════════════════════════════╗
# ║   build-dev.sh — Image Docker :dev construite sur le homelab       ║
# ╚═══════════════════════════════════════════════════════════════════╝
#
# Remplace GitHub Actions pour la branche dev (Actions bloqué sur le compte) :
# récupère la branche dev du dépôt privé et, s'il y a un nouveau commit,
# construit et pousse ghcr.io/gogowwww/frp-manager:dev (+ :dev-<sha>).
# Ne touche jamais à :latest.
#
# Usage :
#   ./build-dev.sh            # construit seulement s'il y a un nouveau commit
#   ./build-dev.sh --force    # reconstruit même sans nouveau commit
#
# Configuration : /etc/frp-manager-build.env (chmod 600), par exemple :
#   GH_TOKEN=ghp_...     # token *classic* : scopes « repo » (cloner le dépôt privé)
#                        # et « write:packages » (GHCR n'accepte pas les fine-grained)
#   GH_USER=Gogowwww
#
# Automatique toutes les 10 minutes (crontab -e de l'utilisateur, membre du groupe docker) :
#   */10 * * * * /opt/frp-manager-build/build-dev.sh >> /var/log/frp-manager-build.log 2>&1

set -euo pipefail

ENV_FILE="${ENV_FILE:-/etc/frp-manager-build.env}"
[[ -f "$ENV_FILE" ]] && source "$ENV_FILE"

REPO="${REPO:-Gogowwww/frp-manager-dev}"
BRANCH="${BRANCH:-dev}"
IMAGE="${IMAGE:-ghcr.io/gogowwww/frp-manager}"
WORKDIR="${WORKDIR:-$HOME/.cache/frp-manager-build}"
GH_USER="${GH_USER:-Gogowwww}"
FORCE=0
[[ "${1:-}" == "--force" ]] && FORCE=1

log() { echo "[$(date '+%F %T')] $*"; }
die() { log "ERREUR : $*"; exit 1; }

[[ -n "${GH_TOKEN:-}" ]] || die "GH_TOKEN manquant (voir $ENV_FILE)"
command -v git >/dev/null || die "git requis"
command -v docker >/dev/null || die "docker requis"

# Une seule exécution à la fois (cron + lancement manuel)
mkdir -p "$WORKDIR"
exec 9>"$WORKDIR/.lock"
flock -n 9 || { log "Déjà en cours, abandon."; exit 0; }

# Le jeton passe dans un en-tête HTTP : il n'est jamais écrit dans .git/config
AUTH_HEADER="Authorization: Basic $(printf '%s:%s' "$GH_USER" "$GH_TOKEN" | base64 | tr -d '\n')"
git_auth() { git -c "http.https://github.com/.extraheader=$AUTH_HEADER" "$@"; }

SRC="$WORKDIR/src"
if [[ ! -d "$SRC/.git" ]]; then
    log "Clonage de $REPO ($BRANCH)…"
    git_auth clone --quiet --branch "$BRANCH" "https://github.com/$REPO.git" "$SRC"
else
    git_auth -C "$SRC" fetch --quiet origin "$BRANCH"
    git -C "$SRC" checkout --quiet -B "$BRANCH" "origin/$BRANCH"
    git -C "$SRC" clean -fdq
fi

SHA="$(git -C "$SRC" rev-parse --short=7 HEAD)"
STATE="$WORKDIR/last-built"
if [[ $FORCE -eq 0 && -f "$STATE" && "$(cat "$STATE")" == "$SHA" ]]; then
    exit 0   # rien de nouveau : silencieux pour ne pas remplir le log du cron
fi

VERSION="dev-$SHA"
log "Nouveau commit $SHA : $(git -C "$SRC" log -1 --format=%s)"

log "Connexion à GHCR…"
echo "$GH_TOKEN" | docker login ghcr.io -u "$GH_USER" --password-stdin >/dev/null
trap 'docker logout ghcr.io >/dev/null 2>&1 || true' EXIT

log "Build $IMAGE:$VERSION…"
# --network=host : l'apt-get du Dockerfile a besoin du réseau de l'hôte sur ce homelab
docker build --network=host --pull \
    --build-arg PANEL_VERSION="$VERSION" \
    -t "$IMAGE:dev" -t "$IMAGE:$VERSION" \
    "$SRC"

log "Push…"
docker push --quiet "$IMAGE:$VERSION"
docker push --quiet "$IMAGE:dev"

echo "$SHA" > "$STATE"
log "OK : $IMAGE:dev = $VERSION"
