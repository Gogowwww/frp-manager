#!/usr/bin/env bash
# ╔═══════════════════════════════════════════════════════════════════╗
# ║              FRP Manager — Script d'installation                  ║
# ╚═══════════════════════════════════════════════════════════════════╝
set -euo pipefail

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'
CYAN='\033[0;36m'; BOLD='\033[1m'; RESET='\033[0m'

# Langue des messages : français si la langue du système l'est, anglais sinon
case "${LC_ALL:-${LC_MESSAGES:-${LANG:-}}}" in
    fr*) FR=1 ;;
    *)   FR=0 ;;
esac
m() { if [[ $FR == 1 ]]; then printf '%s' "$1"; else printf '%s' "$2"; fi; }

info()  { echo -e "${CYAN}[INFO]${RESET}  $*"; }
ok()    { echo -e "${GREEN}[OK]${RESET}    $*"; }
warn()  { echo -e "${YELLOW}[WARN]${RESET}  $*"; }
error() { echo -e "${RED}[ERROR]${RESET} $*" >&2; exit 1; }
title() { echo -e "\n${BOLD}${CYAN}═══ $* ═══${RESET}\n"; }

[[ $EUID -ne 0 ]] && error "$(m "Ce script doit être lancé en root (sudo bash install.sh)." \
                               "This script must be run as root (sudo bash install.sh).")"
command -v python3   &>/dev/null || error "$(m "Python3 requis (apt install python3)." "Python3 required (apt install python3).")"
command -v systemctl &>/dev/null || error "$(m "systemd requis." "systemd required.")"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

echo -e "${BOLD}"
echo "  ███████╗██████╗ ██████╗     ███╗   ███╗  ██████╗ ██████╗ "
echo "  ██╔════╝██╔══██╗██╔══██╗    ████╗ ████║ ██╔════╝ ██╔══██╗"
echo "  █████╗  ██████╔╝██████╔╝    ██╔████╔██║ ██║  ███╗██████╔╝"
echo "  ██╔══╝  ██╔══██╗██╔═══╝     ██║╚██╔╝██║ ██║   ██║██╔══██╗"
echo "  ██║     ██║  ██║██║         ██║ ╚═╝ ██║ ╚██████╔╝██║  ██║"
echo "  ╚═╝     ╚═╝  ╚═╝╚═╝         ╚═╝     ╚═╝  ╚═════╝ ╚═╝  ╚═╝"
echo -e "${RESET}"
echo -e "  $(m "Interface web de gestion pour" "Web interface to manage") ${CYAN}frpc${RESET} $(m "et" "and") ${CYAN}frps${RESET}"
echo ""

INSTALL_DIR="/opt/frp-manager"
FRP_CONF_DIR="/etc/frp"
LOG_DIR="/var/log/frp"
STATE_DIR="/var/lib/frp-manager"
VENV_DIR="${INSTALL_DIR}/venv"
MANAGER_PORT="${FRP_MANAGER_PORT:-8765}"

title "$(m "Vérification des dépendances" "Checking dependencies")"

if ! python3 -m venv --help &>/dev/null; then
    info "$(m "Installation de python3-venv…" "Installing python3-venv…")"
    apt-get install -y python3-venv &>/dev/null || error "$(m "Impossible d'installer python3-venv." "Could not install python3-venv.")"
fi
ok "$(m "python3-venv disponible." "python3-venv available.")"

command -v curl &>/dev/null || apt-get install -y curl &>/dev/null

title "$(m "Environnement Python" "Python environment")"

mkdir -p "$INSTALL_DIR"
python3 -m venv "$VENV_DIR"
"$VENV_DIR/bin/pip" install --quiet --upgrade pip

if [[ -f "$SCRIPT_DIR/requirements.txt" ]]; then
    # argon2-cffi peut manquer de paquet précompilé sur certaines architectures :
    # sans lui, le panel hache le mot de passe avec scrypt (bibliothèque standard).
    "$VENV_DIR/bin/pip" install --quiet -r "$SCRIPT_DIR/requirements.txt" || {
        warn "$(m "Certaines dépendances n'ont pas pu être installées : installation du minimum." \
                  "Some dependencies could not be installed: installing the minimum.")"
        "$VENV_DIR/bin/pip" install --quiet flask requests flask-sock
    }
else
    "$VENV_DIR/bin/pip" install --quiet flask requests flask-sock
fi

"$VENV_DIR/bin/pip" install --quiet cryptography 2>/dev/null || \
    warn "$(m "cryptography non installé — SSL utilisera openssl en fallback." \
              "cryptography not installed: SSL will fall back to openssl.")"

ok "$(m "Dépendances Python installées dans" "Python dependencies installed in") $VENV_DIR"

title "$(m "Déploiement des fichiers" "Deploying files")"

mkdir -p "$INSTALL_DIR/templates" "$LOG_DIR" "$STATE_DIR"

cp "$SCRIPT_DIR/app.py"            "$INSTALL_DIR/app.py"
cp "$SCRIPT_DIR/frp-autoupdate.py" "$INSTALL_DIR/frp-autoupdate.py"
chmod +x "$INSTALL_DIR/frp-autoupdate.py"

rm -rf "$INSTALL_DIR/templates"
cp -r "$SCRIPT_DIR/templates" "$INSTALL_DIR/templates"

# Option « IP réelle » (go-mmproxy) retirée en 0.0.26 : le panel migre les
# tunnels concernés au démarrage ; on supprime juste l'ancien dossier du patch.
rm -rf "$INSTALL_DIR/mmproxy-patch"

ok "$(m "Fichiers copiés dans" "Files copied to") $INSTALL_DIR"

title "$(m "Service systemd : frp-manager" "systemd service: frp-manager")"

cat > /etc/systemd/system/frp-manager.service <<EOF
[Unit]
Description=FRP Manager Web Interface
After=network.target

[Service]
Type=simple
Restart=on-failure
RestartSec=5s
WorkingDirectory=${INSTALL_DIR}
ExecStart=${VENV_DIR}/bin/python3 ${INSTALL_DIR}/app.py
ExecStartPost=/bin/bash -c '${VENV_DIR}/bin/python3 ${INSTALL_DIR}/frp-autoupdate.py >> ${LOG_DIR}/autoupdate.log 2>&1 &'
StandardOutput=journal
StandardError=journal

[Install]
WantedBy=multi-user.target
EOF

ok "$(m "Service frp-manager créé." "frp-manager service created.")"

title "$(m "Téléchargement des binaires frp (frps / frpc)" "Downloading frp binaries (frps / frpc)")"

if [[ -x /usr/local/bin/frps && -x /usr/local/bin/frpc ]]; then
    ok "$(m "Binaires frps/frpc déjà présents dans /usr/local/bin." "frps/frpc binaries already in /usr/local/bin.")"
else
    info "$(m "Récupération de la dernière version de frp depuis GitHub…" "Fetching the latest frp version from GitHub…")"
    if "$VENV_DIR/bin/python3" "$INSTALL_DIR/frp-autoupdate.py" >> "$LOG_DIR/autoupdate.log" 2>&1; then
        ok "$(m "Binaires frps/frpc installés dans /usr/local/bin." "frps/frpc binaries installed in /usr/local/bin.")"
    else
        warn "$(m "Téléchargement des binaires échoué (réseau ?) — voir" "Binary download failed (network?): see") $LOG_DIR/autoupdate.log."
        warn "$(m "Le panel relancera la tentative à son démarrage." "The panel will try again when it starts.")"
    fi
fi

title "$(m "Configs frp par défaut (/etc/frp)" "Default frp configs (/etc/frp)")"

mkdir -p "$FRP_CONF_DIR"

if [[ -f "$FRP_CONF_DIR/frps.toml" ]]; then
    ok "$(m "frps.toml existe déjà — conservé." "frps.toml already exists: kept.")"
else
    cat > "$FRP_CONF_DIR/frps.toml" <<'EOF'
bindAddr = "0.0.0.0"
bindPort = 7000

auth.method = "token"
auth.token = "changeme"

log.to = "/var/log/frp/frps.log"
log.level = "info"
log.maxDays = 3
EOF
    ok "$(m "Config par défaut créée :" "Default config created:") $FRP_CONF_DIR/frps.toml"
fi

if [[ -f "$FRP_CONF_DIR/frpc.toml" ]]; then
    ok "$(m "frpc.toml existe déjà — conservé." "frpc.toml already exists: kept.")"
else
    cat > "$FRP_CONF_DIR/frpc.toml" <<'EOF'
serverAddr = ""
serverPort = 7000

auth.method = "token"
auth.token = "changeme"

log.to = "/var/log/frp/frpc.log"
log.level = "info"
log.maxDays = 3
EOF
    ok "$(m "Config par défaut créée :" "Default config created:") $FRP_CONF_DIR/frpc.toml"
fi

title "$(m "Services systemd : frps & frpc" "systemd services: frps & frpc")"

cat > /etc/systemd/system/frps.service <<EOF
[Unit]
Description=frp server (frps)
After=network.target

[Service]
Type=simple
Restart=on-failure
RestartSec=5s
ExecStart=/usr/local/bin/frps -c ${FRP_CONF_DIR}/frps.toml
LimitNOFILE=1048576

[Install]
WantedBy=multi-user.target
EOF
ok "$(m "Service frps.service créé." "frps.service created.")"

cat > /etc/systemd/system/frpc.service <<EOF
[Unit]
Description=frp client (frpc)
After=network.target

[Service]
Type=simple
Restart=on-failure
RestartSec=5s
ExecStart=/usr/local/bin/frpc -c ${FRP_CONF_DIR}/frpc.toml
LimitNOFILE=1048576

[Install]
WantedBy=multi-user.target
EOF
ok "$(m "Service frpc.service créé." "frpc.service created.")"

warn "$(m "frps/frpc installés mais NON activés/démarrés — gérez-les depuis le panel." \
          "frps/frpc installed but NOT enabled/started: manage them from the panel.")"

title "$(m "Tâche cron de mise à jour de frp (tous les jours à 03h00)" "frp auto-update cron job (daily at 3:00 AM)")"

cat > /etc/cron.d/frp-autoupdate <<EOF
0 3 * * * root ${VENV_DIR}/bin/python3 ${INSTALL_DIR}/frp-autoupdate.py >> ${LOG_DIR}/autoupdate.log 2>&1
EOF
chmod 644 /etc/cron.d/frp-autoupdate
ok "$(m "Tâche cron créée." "Cron job created.")"

title "$(m "Activation et démarrage" "Enabling and starting")"

systemctl daemon-reload
systemctl enable frp-manager --quiet

if systemctl is-active --quiet frp-manager; then
    systemctl restart frp-manager
    ok "$(m "frp-manager redémarré." "frp-manager restarted.")"
else
    systemctl start frp-manager
    ok "$(m "frp-manager démarré." "frp-manager started.")"
fi

LOCAL_IP=$(hostname -I 2>/dev/null | awk '{print $1}' || echo "localhost")
PROTO="https"
BIND_HOST="127.0.0.1"
# Le panel crée sa config au premier démarrage : on lui laisse un instant
for _ in 1 2 3 4 5; do [[ -f /etc/frp-manager/frp-manager.json ]] && break; sleep 1; done
if [[ -f /etc/frp-manager/frp-manager.json ]]; then
    read -r SSL_EN BIND_HOST < <(python3 -c "
import json
try:
    d=json.load(open('/etc/frp-manager/frp-manager.json'))
    print('false' if d.get('ssl_enabled',True)==False else 'true', d.get('bind_host') or '127.0.0.1')
except Exception: print('true 127.0.0.1')
" 2>/dev/null || echo "true 127.0.0.1")
    [[ "$SSL_EN" == "false" ]] && PROTO="http"
fi
if [[ "$BIND_HOST" == "127.0.0.1" || "$BIND_HOST" == "::1" ]]; then
    PANEL_URL="${PROTO}://127.0.0.1:${MANAGER_PORT}"
else
    PANEL_URL="${PROTO}://${LOCAL_IP}:${MANAGER_PORT}"
fi

echo ""
echo -e "${BOLD}${GREEN}╔══════════════════════════════════════════════════════╗"
if [[ $FR == 1 ]]; then
    echo "║          Installation / Mise à jour terminée         ║"
else
    echo "║           Installation / update complete             ║"
fi
echo -e "╚══════════════════════════════════════════════════════╝${RESET}"
echo ""
echo -e "  $(m "Interface web :" "Web interface:") ${BOLD}${PANEL_URL}${RESET}"
[[ "$PROTO" == "https" ]] && echo -e "  ${YELLOW}$(m "Certificat auto-signé : acceptez l'avertissement du navigateur." \
                                                     "Self-signed certificate: accept the browser warning.")${RESET}"
if [[ "$PANEL_URL" == *"127.0.0.1"* ]]; then
    echo -e "  ${YELLOW}$(m "Le panel n'écoute que sur cette machine (127.0.0.1)." "The panel only listens on this machine (127.0.0.1).")${RESET}"
    echo -e "  $(m "Depuis un autre poste :" "From another computer:") ${CYAN}ssh -L ${MANAGER_PORT}:127.0.0.1:${MANAGER_PORT} root@${LOCAL_IP}${RESET}"
    echo -e "  $(m "puis ouvrez" "then open") ${PROTO}://127.0.0.1:${MANAGER_PORT}. $(m "Pour ouvrir au réseau : bind_host = 0.0.0.0" "To open it to the network: bind_host = 0.0.0.0")"
    echo -e "  $(m "dans Réglages ou dans /etc/frp-manager/frp-manager.json, puis redémarrez le panel." \
                   "in Settings or in /etc/frp-manager/frp-manager.json, then restart the panel.")"
fi
echo -e "  $(m "Première ouverture : créez l'identifiant administrateur (12 caractères minimum)." \
               "First launch: create the administrator account (12 characters minimum).")"
echo ""
echo -e "  $(m "Config panel  :" "Panel config  :") ${CYAN}/etc/frp-manager/frp-manager.json${RESET}"
echo -e "  $(m "Journaux      :" "Logs          :") ${CYAN}${LOG_DIR}/${RESET}"
echo -e "  $(m "Mise à jour   :" "Auto-update   :") ${CYAN}/etc/cron.d/frp-autoupdate${RESET} $(m "(03h00)" "(3:00 AM)")"
echo -e "  $(m "Services frp  :" "frp services  :") ${CYAN}frps.service${RESET} + ${CYAN}frpc.service${RESET} $(m "créés (non démarrés)" "created (not started)")"
echo -e "                  $(m "→ configurez-les puis démarrez-les depuis le panel." "→ configure them, then start them from the panel.")"
echo ""
echo -e "  $(m "Commandes utiles :" "Useful commands:")"
echo -e "    ${YELLOW}systemctl status frp-manager${RESET}"
echo -e "    ${YELLOW}journalctl -u frp-manager -f${RESET}"
echo ""
