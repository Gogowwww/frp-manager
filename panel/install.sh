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
MANAGER_PORT="${FRP_MANAGER_PORT:-8765}"
FRPS_PORT_CREATED=""

# ── Port d'écoute de frps ───────────────────────────────────────────────────
# Les robots qui balaient Internet visent le port 7000 de frp : un port tiré au
# hasard dans 10000–32767 (sous la plage des ports éphémères de Linux, libre au
# moment de l'installation) les évite. FRPS_BIND_PORT=7000 (ou un autre numéro,
# ou « random ») répond à la question sans la poser.
port_in_use() {
    command -v ss &>/dev/null && ss -Hltn "sport = :$1" 2>/dev/null | grep -q .
}

random_port() {
    local p
    for _ in $(seq 1 50); do
        p=$(( 10000 + $(od -An -N2 -tu2 /dev/urandom | tr -d ' ') % 22768 ))
        port_in_use "$p" || { echo "$p"; return; }
    done
    echo 7000
}

valid_port() { [[ "$1" =~ ^[0-9]{1,5}$ ]] && (( 10#$1 >= 1 && 10#$1 <= 65535 )); }

choose_frps_port() {
    local suggested answer
    suggested=$(random_port)
    case "${FRPS_BIND_PORT:-}" in
        "")      ;;
        random)  echo "$suggested"; return ;;
        *)       valid_port "$FRPS_BIND_PORT" || error "$(m "FRPS_BIND_PORT invalide :" "Invalid FRPS_BIND_PORT:") $FRPS_BIND_PORT"
                 echo $(( 10#$FRPS_BIND_PORT )); return ;;
    esac
    # Pas de terminal (installation automatisée) : le port aléatoire
    if ! { : </dev/tty; } 2>/dev/null; then echo "$suggested"; return; fi
    {
        echo ""
        echo -e "  ${BOLD}$(m "Port d'écoute de frps" "frps listening port")${RESET} $(m "(les clients frpc s'y connectent)" "(frpc clients connect to it)")"
        echo -e "    $(m "Entrée" "Enter")   ${CYAN}${suggested}${RESET}  $(m "port aléatoire, recommandé : les robots visent 7000" "random port, recommended: bots target 7000")"
        echo -e "    7000    $(m "port standard de frp" "frp's standard port")"
        echo -e "    $(m "ou un autre numéro (1–65535)" "or any other number (1–65535)")"
    } >/dev/tty
    exec 3</dev/tty
    for _ in 1 2 3; do
        read -r -u 3 -p "  $(m "Port" "Port") [${suggested}] : " answer || answer=""
        answer="${answer// /}"
        [[ -z "$answer" ]] && { echo "$suggested"; return; }
        if valid_port "$answer"; then
            answer=$(( 10#$answer ))
            port_in_use "$answer" && echo -e "  ${YELLOW}$(m "Port déjà utilisé sur cette machine : frps ne pourra pas démarrer tant qu'il l'est."                                                          "Port already in use on this machine: frps won't start while it is.")${RESET}" >/dev/tty
            echo "$answer"; return
        fi
        echo -e "  ${YELLOW}$(m "Numéro de port invalide." "Invalid port number.")${RESET}" >/dev/tty
    done
    echo "$suggested"
}

title "$(m "Programme frp-manager" "frp-manager program")"

# Binaire de cette architecture : bin/ de l'archive de release, ou frp-manager
# à côté du script (compilé à la main : go build -o frp-manager ./panel)
case "$(uname -m)" in
    x86_64|amd64)          ARCH="amd64" ;;
    aarch64|arm64)         ARCH="arm64" ;;
    armv7l|armv7*|armv8l)  ARCH="armv7" ;;
    *) error "$(m "Architecture non prise en charge :" "Unsupported architecture:") $(uname -m)" ;;
esac
BINARY="$SCRIPT_DIR/bin/frp-manager-linux-$ARCH"
[[ -f "$BINARY" ]] || BINARY="$SCRIPT_DIR/frp-manager"
[[ -f "$BINARY" ]] || error "$(m "Programme introuvable :" "Program not found:") bin/frp-manager-linux-$ARCH"

mkdir -p "$INSTALL_DIR" "$LOG_DIR" "$STATE_DIR"
# Fichier voisin puis renommage : le panel en marche n'est jamais à moitié remplacé
install -m 755 "$BINARY" "$INSTALL_DIR/frp-manager.new"
mv -f "$INSTALL_DIR/frp-manager.new" "$INSTALL_DIR/frp-manager"
"$INSTALL_DIR/frp-manager" --version >/dev/null \
    || error "$(m "Le programme ne démarre pas sur cette machine." "The program does not run on this machine.")"
cp "$SCRIPT_DIR/install.sh" "$INSTALL_DIR/install.sh" 2>/dev/null || true

# Ancienne installation Python (jusqu'à 0.0.57) : environnement et fichiers
# devenus inutiles ; la configuration (/etc/frp-manager) est gardée telle quelle.
if [[ -d "$INSTALL_DIR/venv" || -f "$INSTALL_DIR/app.py" ]]; then
    rm -rf "$INSTALL_DIR/venv" "$INSTALL_DIR/app.py" "$INSTALL_DIR/templates" "$INSTALL_DIR/__pycache__" \
           "$INSTALL_DIR/requirements.txt"
    ok "$(m "Ancienne version Python retirée (configuration conservée)." "Old Python version removed (configuration kept).")"
fi
# Mise à jour automatique de frp retirée en 0.0.53, go-mmproxy en 0.0.26
rm -f "$INSTALL_DIR/frp-autoupdate.py" /etc/cron.d/frp-autoupdate
rm -rf "$INSTALL_DIR/mmproxy-patch"

ok "$(m "Programme installé :" "Program installed:") $INSTALL_DIR/frp-manager ($ARCH, v$("$INSTALL_DIR/frp-manager" --version))"

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
ExecStart=${INSTALL_DIR}/frp-manager
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
    # Même code que Réglages → Mises à jour dans le panel (somme SHA-256 vérifiée)
    if "$INSTALL_DIR/frp-manager" --install-frp; then
        ok "$(m "Binaires frps/frpc installés dans /usr/local/bin." "frps/frpc binaries installed in /usr/local/bin.")"
    else
        warn "$(m "Téléchargement des binaires échoué (réseau ?)." "Binary download failed (network?).")"
        warn "$(m "Installez frp plus tard depuis Réglages → Mises à jour dans le panel." "Install frp later from Settings → Updates in the panel.")"
    fi
fi

title "$(m "Configs frp par défaut (/etc/frp)" "Default frp configs (/etc/frp)")"

mkdir -p "$FRP_CONF_DIR"

if [[ -f "$FRP_CONF_DIR/frps.toml" ]]; then
    ok "$(m "frps.toml existe déjà — conservé." "frps.toml already exists: kept.")"
else
    FRPS_PORT_CREATED=$(choose_frps_port)
    cat > "$FRP_CONF_DIR/frps.toml" <<EOF
bindAddr = "0.0.0.0"
bindPort = ${FRPS_PORT_CREATED}

auth.method = "token"
auth.token = "changeme"

log.to = "/var/log/frp/frps.log"
log.level = "info"
log.maxDays = 3
EOF
    ok "$(m "Config par défaut créée :" "Default config created:") $FRP_CONF_DIR/frps.toml ($(m "port" "port") ${FRPS_PORT_CREATED})"
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
    grep -Eq '"ssl_enabled": *false' /etc/frp-manager/frp-manager.json && SSL_EN=false || SSL_EN=true
    BIND_HOST=$(sed -n 's/.*"bind_host": *"\([^"]*\)".*/\1/p' /etc/frp-manager/frp-manager.json | head -n1)
    BIND_HOST="${BIND_HOST:-127.0.0.1}"
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
echo -e "  $(m "Mises à jour  :" "Updates       :") $(m "Réglages → Mises à jour dans le panel (frp et panel)" "Settings → Updates in the panel (frp and panel)")"
echo -e "  $(m "Services frp  :" "frp services  :") ${CYAN}frps.service${RESET} + ${CYAN}frpc.service${RESET} $(m "créés (non démarrés)" "created (not started)")"
echo -e "                  $(m "→ configurez-les puis démarrez-les depuis le panel." "→ configure them, then start them from the panel.")"
if [[ -n "$FRPS_PORT_CREATED" ]]; then
    echo -e "  $(m "Port frps     :" "frps port     :") ${BOLD}${FRPS_PORT_CREATED}${RESET} $(m "(TCP) : à ouvrir dans le pare-feu de l'hébergeur si cette machine" "(TCP): open it in your host's firewall if this machine")"
    echo -e "                  $(m "sert de serveur frp, et à reporter dans serverPort des clients frpc." "acts as the frp server, and set it as serverPort on frpc clients.")"
fi
echo ""
echo -e "  $(m "Commandes utiles :" "Useful commands:")"
echo -e "    ${YELLOW}systemctl status frp-manager${RESET}"
echo -e "    ${YELLOW}journalctl -u frp-manager -f${RESET}"
echo ""
