#!/usr/bin/env bash
# ╔═══════════════════════════════════════════════════════════════════╗
# ║              FRP Manager — Script d'installation                  ║
# ║  Installe frp, l'interface web de gestion, et l'auto-update      ║
# ╚═══════════════════════════════════════════════════════════════════╝
set -euo pipefail

# ── Couleurs ─────────────────────────────────────────────────────────
RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'
CYAN='\033[0;36m'; BOLD='\033[1m'; RESET='\033[0m'

info()    { echo -e "${CYAN}[INFO]${RESET}  $*"; }
ok()      { echo -e "${GREEN}[OK]${RESET}    $*"; }
warn()    { echo -e "${YELLOW}[WARN]${RESET}  $*"; }
error()   { echo -e "${RED}[ERROR]${RESET} $*" >&2; exit 1; }
title()   { echo -e "\n${BOLD}${CYAN}═══ $* ═══${RESET}\n"; }

# ── Vérifications ────────────────────────────────────────────────────
[[ $EUID -ne 0 ]] && error "Ce script doit être lancé en root (sudo)."
command -v python3 &>/dev/null || error "Python3 requis."
command -v pip3 &>/dev/null    || { apt-get install -y python3-pip &>/dev/null; }
command -v curl &>/dev/null    || { apt-get install -y curl &>/dev/null; }
command -v systemctl &>/dev/null || error "systemd requis."

# ── Variables ────────────────────────────────────────────────────────
INSTALL_DIR="/opt/frp-manager"
BIN_DIR="/usr/local/bin"
CONF_DIR="/etc/frp"
LOG_DIR="/var/log/frp"
STATE_DIR="/var/lib/frp-manager"
MANAGER_PORT="${FRP_MANAGER_PORT:-8765}"
DISCORD_WEBHOOK="${FRP_DISCORD_WEBHOOK:-}"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

echo -e "${BOLD}"
echo "  ███████╗██████╗ ██████╗     ███╗   ███╗ ██████╗ ██████╗ "
echo "  ██╔════╝██╔══██╗██╔══██╗    ████╗ ████║██╔════╝ ██╔══██╗"
echo "  █████╗  ██████╔╝██████╔╝    ██╔████╔██║██║  ███╗██████╔╝"
echo "  ██╔══╝  ██╔══██╗██╔═══╝     ██║╚██╔╝██║██║   ██║██╔══██╗"
echo "  ██║     ██║  ██║██║         ██║ ╚═╝ ██║╚██████╔╝██║  ██║"
echo "  ╚═╝     ╚═╝  ╚═╝╚═╝         ╚═╝     ╚═╝ ╚═════╝ ╚═╝  ╚═╝"
echo -e "${RESET}"
echo -e "  Interface web de gestion pour ${CYAN}frpc${RESET} et ${CYAN}frps${RESET}"
echo ""

# ── Dépendances Python ───────────────────────────────────────────────
title "Installation des dépendances Python"

VENV_DIR="${INSTALL_DIR}/venv"
python3 -m venv "$VENV_DIR"
"$VENV_DIR/bin/pip" install --quiet flask requests
ok "Flask et requests installés dans le venv."

# ── Copie des fichiers ───────────────────────────────────────────────
title "Déploiement des fichiers"
mkdir -p "$INSTALL_DIR" "$CONF_DIR" "$LOG_DIR" "$STATE_DIR"

cp "$SCRIPT_DIR/app.py"             "$INSTALL_DIR/app.py"
cp "$SCRIPT_DIR/frp-autoupdate.py"  "$INSTALL_DIR/frp-autoupdate.py"
chmod +x "$INSTALL_DIR/frp-autoupdate.py"

# Templates
mkdir -p "$INSTALL_DIR/templates"
cp "$SCRIPT_DIR/templates/index.html" "$INSTALL_DIR/templates/index.html"

ok "Fichiers copiés dans $INSTALL_DIR"

# ── Systemd : frp-manager ────────────────────────────────────────────
title "Service systemd : frp-manager (interface web)"

SECRET=$(python3 -c "import secrets; print(secrets.token_hex(24))")

cat > /etc/systemd/system/frp-manager.service <<EOF
[Unit]
Description=FRP Manager Web Interface
After=network.target

[Service]
Type=simple
Restart=on-failure
RestartSec=5s
WorkingDirectory=${INSTALL_DIR}
Environment=FRP_MANAGER_PORT=${MANAGER_PORT}
Environment=FRP_MANAGER_SECRET=${SECRET}
Environment=FRP_DISCORD_WEBHOOK=${DISCORD_WEBHOOK}
ExecStart=${INSTALL_DIR}/venv/bin/python3 ${INSTALL_DIR}/app.py
ExecStartPost=/bin/bash -c '${INSTALL_DIR}/venv/bin/python3 ${INSTALL_DIR}/frp-autoupdate.py >> ${LOG_DIR}/autoupdate.log 2>&1 &'
StandardOutput=journal
StandardError=journal

[Install]
WantedBy=multi-user.target
EOF

ok "Service frp-manager créé."

# ── Cron : auto-update quotidien ────────────────────────────────────
title "Cron job auto-update (quotidien)"

CRON_LINE="0 3 * * * root FRP_DISCORD_WEBHOOK='${DISCORD_WEBHOOK}' ${INSTALL_DIR}/venv/bin/python3 ${INSTALL_DIR}/frp-autoupdate.py >> ${LOG_DIR}/autoupdate.log 2>&1"
echo "$CRON_LINE" > /etc/cron.d/frp-autoupdate
chmod 644 /etc/cron.d/frp-autoupdate
ok "Cron job créé : tous les jours à 03h00."

# ── Premier lancement frp ────────────────────────────────────────────
title "Installation initiale de frp"
info "Téléchargement et installation de la dernière version de frp…"
python3 "$INSTALL_DIR/frp-autoupdate.py" && ok "frp installé avec succès." || warn "L'auto-update initial a échoué. Relancez manuellement."

# ── Activation des services ──────────────────────────────────────────
title "Activation et démarrage"
systemctl daemon-reload
systemctl enable frp-manager
systemctl restart frp-manager
ok "frp-manager démarré."

# Proposer d'activer frps ou frpc
echo ""
read -rp "$(echo -e ${CYAN})Activer et démarrer frps (serveur) ? [o/N] $(echo -e ${RESET})" ans
[[ "$ans" =~ ^[oOyY]$ ]] && systemctl enable --now frps && ok "frps démarré." || true

read -rp "$(echo -e ${CYAN})Activer et démarrer frpc (client) ? [o/N] $(echo -e ${RESET})" ans
[[ "$ans" =~ ^[oOyY]$ ]] && systemctl enable --now frpc && ok "frpc démarré." || true

# ── Résumé ───────────────────────────────────────────────────────────
LOCAL_IP=$(hostname -I | awk '{print $1}')
echo ""
echo -e "${BOLD}${GREEN}╔══════════════════════════════════════════════════╗"
echo    "║            Installation terminée ✓               ║"
echo -e "╚══════════════════════════════════════════════════╝${RESET}"
echo ""
echo -e "  Interface web : ${BOLD}http://${LOCAL_IP}:${MANAGER_PORT}${RESET}"
echo -e "  Configs frp   : ${CYAN}${CONF_DIR}/${RESET}"
echo -e "  Logs          : ${CYAN}${LOG_DIR}/${RESET}"
echo -e "  Auto-update   : ${CYAN}/etc/cron.d/frp-autoupdate${RESET} (03h00 chaque nuit)"
echo ""
echo -e "  Commandes utiles :"
echo -e "    ${YELLOW}systemctl status frp-manager${RESET}"
echo -e "    ${YELLOW}systemctl status frps${RESET}"
echo -e "    ${YELLOW}systemctl status frpc${RESET}"
echo -e "    ${YELLOW}journalctl -u frp-manager -f${RESET}"
echo ""
echo -e "  Pour configurer un webhook Discord pour les notifs :"
echo -e "    ${YELLOW}export FRP_DISCORD_WEBHOOK='https://discord.com/api/webhooks/...'${RESET}"
echo -e "    puis relancez : ${YELLOW}sudo bash install.sh${RESET}"
echo ""
