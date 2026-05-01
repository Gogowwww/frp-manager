#!/usr/bin/env bash
# ╔═══════════════════════════════════════════════════════════════════╗
# ║              FRP Manager — Script d'installation                  ║
# ╚═══════════════════════════════════════════════════════════════════╝
set -euo pipefail

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'
CYAN='\033[0;36m'; BOLD='\033[1m'; RESET='\033[0m'

info()  { echo -e "${CYAN}[INFO]${RESET}  $*"; }
ok()    { echo -e "${GREEN}[OK]${RESET}    $*"; }
warn()  { echo -e "${YELLOW}[WARN]${RESET}  $*"; }
error() { echo -e "${RED}[ERROR]${RESET} $*" >&2; exit 1; }
title() { echo -e "\n${BOLD}${CYAN}═══ $* ═══${RESET}\n"; }

[[ $EUID -ne 0 ]] && error "Ce script doit être lancé en root (sudo bash install.sh)."
command -v python3   &>/dev/null || error "Python3 requis (apt install python3)."
command -v systemctl &>/dev/null || error "systemd requis."

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

echo -e "${BOLD}"
echo "  ███████╗██████╗ ██████╗     ███╗   ███╗  ██████╗ ██████╗ "
echo "  ██╔════╝██╔══██╗██╔══██╗    ████╗ ████║ ██╔════╝ ██╔══██╗"
echo "  █████╗  ██████╔╝██████╔╝    ██╔████╔██║ ██║  ███╗██████╔╝"
echo "  ██╔══╝  ██╔══██╗██╔═══╝     ██║╚██╔╝██║ ██║   ██║██╔══██╗"
echo "  ██║     ██║  ██║██║         ██║ ╚═╝ ██║ ╚██████╔╝██║  ██║"
echo "  ╚═╝     ╚═╝  ╚═╝╚═╝         ╚═╝     ╚═╝  ╚═════╝ ╚═╝  ╚═╝"
echo -e "${RESET}"
echo -e "  Interface web de gestion pour ${CYAN}frpc${RESET} et ${CYAN}frps${RESET}"
echo ""

INSTALL_DIR="/opt/frp-manager"
LOG_DIR="/var/log/frp"
STATE_DIR="/var/lib/frp-manager"
VENV_DIR="${INSTALL_DIR}/venv"
MANAGER_PORT="${FRP_MANAGER_PORT:-8765}"

title "Vérification des dépendances"

if ! python3 -m venv --help &>/dev/null; then
    info "Installation de python3-venv…"
    apt-get install -y python3-venv &>/dev/null || error "Impossible d'installer python3-venv."
fi
ok "python3-venv disponible."

command -v curl &>/dev/null || apt-get install -y curl &>/dev/null

title "Environnement Python"

mkdir -p "$INSTALL_DIR"
python3 -m venv "$VENV_DIR"
"$VENV_DIR/bin/pip" install --quiet --upgrade pip

if [[ -f "$SCRIPT_DIR/requirements.txt" ]]; then
    "$VENV_DIR/bin/pip" install --quiet -r "$SCRIPT_DIR/requirements.txt"
else
    "$VENV_DIR/bin/pip" install --quiet flask requests
fi

"$VENV_DIR/bin/pip" install --quiet cryptography 2>/dev/null || \
    warn "cryptography non installé — SSL utilisera openssl en fallback."

ok "Dépendances Python installées dans $VENV_DIR"

title "Déploiement des fichiers"

mkdir -p "$INSTALL_DIR/templates" "$LOG_DIR" "$STATE_DIR"

cp "$SCRIPT_DIR/app.py"            "$INSTALL_DIR/app.py"
cp "$SCRIPT_DIR/frp-autoupdate.py" "$INSTALL_DIR/frp-autoupdate.py"
chmod +x "$INSTALL_DIR/frp-autoupdate.py"

cp "$SCRIPT_DIR/templates/index.html" "$INSTALL_DIR/templates/index.html"
cp "$SCRIPT_DIR/templates/login.html" "$INSTALL_DIR/templates/login.html"

ok "Fichiers copiés dans $INSTALL_DIR"

title "Service systemd : frp-manager"

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

ok "Service frp-manager créé."

title "Cron job auto-update frp (quotidien 03h00)"

cat > /etc/cron.d/frp-autoupdate <<EOF
0 3 * * * root ${VENV_DIR}/bin/python3 ${INSTALL_DIR}/frp-autoupdate.py >> ${LOG_DIR}/autoupdate.log 2>&1
EOF
chmod 644 /etc/cron.d/frp-autoupdate
ok "Cron job créé."

title "Activation et démarrage"

systemctl daemon-reload
systemctl enable frp-manager --quiet

if systemctl is-active --quiet frp-manager; then
    systemctl restart frp-manager
    ok "frp-manager redémarré."
else
    systemctl start frp-manager
    ok "frp-manager démarré."
fi

LOCAL_IP=$(hostname -I 2>/dev/null | awk '{print $1}' || echo "localhost")
PROTO="https"
if [[ -f /etc/frp-manager/frp-manager.json ]]; then
    SSL_EN=$(python3 -c "
import json
try:
    d=json.load(open('/etc/frp-manager/frp-manager.json'))
    print('false' if d.get('ssl_enabled',True)==False else 'true')
except: print('true')
" 2>/dev/null || echo "true")
    [[ "$SSL_EN" == "false" ]] && PROTO="http"
fi

echo ""
echo -e "${BOLD}${GREEN}╔══════════════════════════════════════════════════════╗"
echo    "║          Installation / Mise à jour terminée ✓       ║"
echo -e "╚══════════════════════════════════════════════════════╝${RESET}"
echo ""
echo -e "  Interface web : ${BOLD}${PROTO}://${LOCAL_IP}:${MANAGER_PORT}${RESET}"
[[ "$PROTO" == "https" ]] && echo -e "  ${YELLOW}⚠ Certificat auto-signé — acceptez l'avertissement navigateur.${RESET}"
echo ""
echo -e "  Config panel  : ${CYAN}/etc/frp-manager/frp-manager.json${RESET}"
echo -e "  Logs          : ${CYAN}${LOG_DIR}/${RESET}"
echo -e "  Auto-update   : ${CYAN}/etc/cron.d/frp-autoupdate${RESET} (03h00)"
echo ""
echo -e "  Commandes utiles :"
echo -e "    ${YELLOW}systemctl status frp-manager${RESET}"
echo -e "    ${YELLOW}journalctl -u frp-manager -f${RESET}"
echo ""
