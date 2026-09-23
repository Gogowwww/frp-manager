#!/usr/bin/env python3
"""FRP Manager — backend Flask multi-instances"""

import os, re, sys, json, subprocess, threading, shutil, shlex, tarfile, tempfile, platform, time, secrets, hashlib, ssl, queue
import socket as _socket, http.client as _http_client
from pathlib import Path
from datetime import datetime
from functools import wraps

from flask import Flask, render_template, request, jsonify, Response, session, redirect, url_for
import requests as req

try:
    # Journaux en direct par WebSocket (wss:// en HTTPS). Facultatif : sans ce
    # module (ancienne installation pas encore mise à jour), l'interface
    # retombe sur le flux SSE /api/logs/stream.
    from flask_sock import Sock
except ImportError:
    Sock = None

# ── Version du panel ─────────────────────────────────────────────────────────
_PANEL_VERSION_FALLBACK = "0.0.23"   # Version hardcodée — écrasée par state.json
PANEL_GITHUB_REPO = "Gogowwww/frp-manager"
PANEL_GITHUB_API  = f"https://api.github.com/repos/{PANEL_GITHUB_REPO}/releases/latest"

def _load_panel_version():
    """
    Priorité :
    1. Variable d'env PANEL_DOCKER_VERSION (injectée au build Docker via ARG)
    2. state.json panel_version (mis à jour par auto-update hors Docker)
    3. Fallback hardcodé
    """
    # 1. Version injectée dans l'image Docker au build
    docker_ver = os.environ.get("PANEL_DOCKER_VERSION", "").strip()
    if docker_ver and docker_ver != "unknown":
        return docker_ver
    # 2. Version sauvegardée dans state.json (auto-update hors Docker)
    try:
        p = Path("/var/lib/frp-manager/state.json")
        if p.exists():
            d = json.loads(p.read_text())
            v = d.get("panel_version")
            if v:
                return v
    except Exception:
        pass
    return _PANEL_VERSION_FALLBACK

PANEL_VERSION = _load_panel_version()

# Détecter si le panel tourne dans un container Docker
# (présence de /.dockerenv ou variable d'env DOCKER_MODE)
IN_DOCKER = Path("/.dockerenv").exists() or os.environ.get("DOCKER_MODE", "") == "true"

# ── Config fichier manager ────────────────────────────────────────────────────
MGR_CONF_FILE = Path("/etc/frp-manager/frp-manager.json")
MGR_CONF_DIR  = MGR_CONF_FILE.parent

SSL_CERT_DIR  = MGR_CONF_DIR / "ssl"
SSL_CERT_FILE = SSL_CERT_DIR / "cert.pem"
SSL_KEY_FILE  = SSL_CERT_DIR / "key.pem"

def _default_manager_config():
    return {
        "bind_host":       "0.0.0.0",
        "bind_port":       8765,
        "username":        "admin",
        "password_hash":   "",
        "secret_key":      secrets.token_hex(32),
        "session_timeout": 3600,
        "ssl_enabled":     True,
        "nicknames":       {},
    }

def load_manager_config():
    if MGR_CONF_FILE.exists():
        try:
            data = json.loads(MGR_CONF_FILE.read_text())
            return {**_default_manager_config(), **data}
        except Exception:
            pass
    return _default_manager_config()

def save_manager_config(cfg):
    MGR_CONF_DIR.mkdir(parents=True, exist_ok=True)
    MGR_CONF_FILE.write_text(json.dumps(cfg, indent=2))

MGR_CFG = load_manager_config()

# ── SSL auto-signé ────────────────────────────────────────────────────────────
def generate_self_signed_cert():
    SSL_CERT_DIR.mkdir(parents=True, exist_ok=True)
    try:
        from cryptography import x509
        from cryptography.x509.oid import NameOID
        from cryptography.hazmat.primitives import hashes, serialization
        from cryptography.hazmat.primitives.asymmetric import rsa
        from cryptography.hazmat.backends import default_backend
        import datetime as dt, ipaddress
        key = rsa.generate_private_key(public_exponent=65537, key_size=2048, backend=default_backend())
        SSL_KEY_FILE.write_bytes(key.private_bytes(
            serialization.Encoding.PEM, serialization.PrivateFormat.TraditionalOpenSSL,
            serialization.NoEncryption()))
        subj = x509.Name([x509.NameAttribute(NameOID.COMMON_NAME, u"frp-manager")])
        cert = (x509.CertificateBuilder()
            .subject_name(subj).issuer_name(subj)
            .public_key(key.public_key())
            .serial_number(x509.random_serial_number())
            .not_valid_before(dt.datetime.utcnow())
            .not_valid_after(dt.datetime.utcnow() + dt.timedelta(days=3650))
            .add_extension(x509.SubjectAlternativeName([
                x509.DNSName(u"localhost"),
                x509.IPAddress(ipaddress.IPv4Address("127.0.0.1")),
            ]), critical=False)
            .sign(key, hashes.SHA256(), default_backend()))
        SSL_CERT_FILE.write_bytes(cert.public_bytes(serialization.Encoding.PEM))
        return True
    except ImportError:
        pass
    try:
        r = subprocess.run([
            "openssl", "req", "-x509", "-nodes", "-newkey", "rsa:2048",
            "-keyout", str(SSL_KEY_FILE), "-out", str(SSL_CERT_FILE),
            "-days", "3650", "-subj", "/CN=frp-manager/O=FRP Manager",
        ], capture_output=True, timeout=30)
        return r.returncode == 0
    except Exception:
        return False

def get_ssl_context():
    if not MGR_CFG.get("ssl_enabled", True):
        return None
    if not SSL_CERT_FILE.exists() or not SSL_KEY_FILE.exists():
        if not generate_self_signed_cert():
            print("[WARN] Impossible de générer le certificat SSL — démarrage en HTTP")
            return None
    try:
        ctx = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
        ctx.load_cert_chain(str(SSL_CERT_FILE), str(SSL_KEY_FILE))
        return ctx
    except Exception as e:
        print(f"[WARN] SSL context invalide ({e}) — démarrage en HTTP")
        return None

# ── Flask ─────────────────────────────────────────────────────────────────────
# Les assets (CSS/JS/traductions) vivent dans templates/assets/ : l'auto-update
# du panel recopie déjà tout le dossier templates/, y compris depuis les
# anciennes versions qui ne connaissent pas de dossier static/.
app = Flask(__name__, static_folder="templates/assets", static_url_path="/assets")
app.secret_key = MGR_CFG.get("secret_key") or secrets.token_hex(32)
# Les modules JS importés par main.js n'ont pas de ?v=version dans leur URL :
# max-age=0 force le navigateur à les revalider (304 si inchangés), sans quoi
# il pourrait garder d'anciens modules en cache après une mise à jour du panel.
app.config["SEND_FILE_MAX_AGE_DEFAULT"] = 0
# Ping toutes les 25 s : évite qu'un reverse proxy ou un tunnel frp ne coupe
# une connexion WebSocket restée silencieuse (journal sans nouvelle ligne).
app.config["SOCK_SERVER_OPTIONS"] = {"ping_interval": 25}
sock = Sock(app) if Sock else None

# ── Paths ─────────────────────────────────────────────────────────────────────
FRP_BIN_DIR    = Path("/usr/local/bin")
FRP_CONF_DIR   = Path("/etc/frp")
FRP_LOG_DIR    = Path("/var/log/frp")
FRP_STATE_FILE = Path("/var/lib/frp-manager/state.json")

BINARY_SEARCH_PATHS = [
    Path("/usr/local/bin"), Path("/usr/bin"), Path("/usr/sbin"),
    Path("/opt/frp"), Path("/opt/frp/bin"), Path("/root/frp"), Path("/srv/frp"),
]

# Support Docker : /host/usr/local/bin est le /usr/local/bin de l'hôte monté
# via docker-compose. Si présent, on l'utilise en priorité pour lire ET écrire
# les binaires frp sur le système hôte (et non dans le container).
_DOCKER_HOST_BIN = Path("/host/usr/local/bin")
if _DOCKER_HOST_BIN.exists():
    FRP_BIN_DIR = _DOCKER_HOST_BIN
    BINARY_SEARCH_PATHS = [_DOCKER_HOST_BIN] + BINARY_SEARCH_PATHS
CONFIG_SEARCH_PATHS = [
    Path("/etc/frp"), Path("/usr/local/etc/frp"), Path("/opt/frp"), Path("/root/frp"),
]

FALLBACK_VERSION_SOURCES = [
    ("github", "https://api.github.com/repos/fatedier/frp/releases/latest"),
]
FALLBACK_DOWNLOAD_MIRRORS = [
    "https://github.com/fatedier/frp/releases/download/{tag}/{filename}",
    "https://mirror.ghproxy.com/https://github.com/fatedier/frp/releases/download/{tag}/{filename}",
    "https://ghfast.top/https://github.com/fatedier/frp/releases/download/{tag}/{filename}",
    "https://gh-proxy.com/https://github.com/fatedier/frp/releases/download/{tag}/{filename}",
]

# Pas de configs par défaut créées automatiquement — l'utilisateur les crée lui-même
DEFAULT_CONFIGS = {
    "frps": 'bindAddr = "0.0.0.0"\nbindPort = 7000\n\nauth.method = "token"\nauth.token = "changeme"\n\nlog.to = "/var/log/frp/frps.log"\nlog.level = "info"\nlog.maxDays = 3\n',
    "frpc": 'serverAddr = ""\nserverPort = 7000\n\nauth.method = "token"\nauth.token = "changeme"\n\nlog.to = "/var/log/frp/frpc.log"\nlog.level = "info"\nlog.maxDays = 3\n',
}

# ── Auth ──────────────────────────────────────────────────────────────────────
def hash_password(pw):
    return hashlib.sha256(pw.encode()).hexdigest()

def check_password(pw):
    stored = MGR_CFG.get("password_hash", "")
    if not stored:
        return True
    return hashlib.sha256(pw.encode()).hexdigest() == stored

def login_required(f):
    @wraps(f)
    def decorated(*args, **kwargs):
        if not MGR_CFG.get("password_hash"):
            return f(*args, **kwargs)
        if not session.get("authenticated"):
            if request.path.startswith("/api/"):
                return jsonify({"ok": False, "msg": "Non authentifié"}), 401
            return redirect(url_for("login_page"))
        return f(*args, **kwargs)
    return decorated

@app.route("/login", methods=["GET"])
def login_page():
    if not MGR_CFG.get("password_hash"):
        return redirect(url_for("index"))
    return render_template("login.html")

@app.route("/api/login", methods=["POST"])
def api_login():
    data = request.get_json() or {}
    user = data.get("username", "")
    pw   = data.get("password", "")
    if user == MGR_CFG.get("username", "admin") and check_password(pw):
        session["authenticated"] = True
        session.permanent = True
        from datetime import timedelta
        app.permanent_session_lifetime = timedelta(seconds=MGR_CFG.get("session_timeout", 3600))
        return jsonify({"ok": True})
    return jsonify({"ok": False, "msg": "Identifiants incorrects"}), 401

@app.route("/api/logout", methods=["POST"])
def api_logout():
    session.clear()
    return jsonify({"ok": True})

# ── State ─────────────────────────────────────────────────────────────────────
def load_state():
    try:
        if FRP_STATE_FILE.exists():
            return json.loads(FRP_STATE_FILE.read_text())
    except Exception:
        pass
    return {"installed_version": None, "last_update_check": None, "last_update_result": None}

def save_state(state):
    FRP_STATE_FILE.parent.mkdir(parents=True, exist_ok=True)
    FRP_STATE_FILE.write_text(json.dumps(state, indent=2))

def build_version_sources():
    return list(FALLBACK_VERSION_SOURCES)

def build_download_mirrors(tag, filename):
    return [tpl.format(tag=tag, filename=filename) for tpl in FALLBACK_DOWNLOAD_MIRRORS]

# ── Docker ──────────────────────────────────────────────────────────────────
# Détection Docker : /.dockerenv est créé par Docker dans chaque container
_IN_DOCKER = Path("/.dockerenv").exists()

# ── Helpers système ───────────────────────────────────────────────────────────
def run_cmd(cmd, timeout=15):
    actual = list(cmd)
    # Dans Docker, on utilise nsenter pour atteindre le systemd/journalctl/ufw de l'hôte
    if _IN_DOCKER and actual and actual[0] in ("systemctl", "journalctl", "ufw", "iptables", "ip6tables"):
        actual = ["nsenter", "-t", "1", "-m", "-u", "-i", "-n", "-p", "--"] + actual
    try:
        r = subprocess.run(actual, capture_output=True, text=True, timeout=timeout)
        return r.returncode == 0, r.stdout.strip(), r.stderr.strip()
    except subprocess.TimeoutExpired:
        return False, "", "timeout"
    except Exception as e:
        return False, "", str(e)

def run_host(cmd, timeout=15, input_text=None):
    """
    Exécute une commande arbitraire sur l'HÔTE.
    Hors Docker : exécution directe. En Docker : via nsenter dans les
    namespaces de PID 1 (comme run_cmd, mais sans liste blanche).
    """
    actual = list(cmd)
    if _IN_DOCKER:
        actual = ["nsenter", "-t", "1", "-m", "-u", "-i", "-n", "-p", "--"] + actual
    try:
        r = subprocess.run(actual, capture_output=True, text=True,
                           timeout=timeout, input=input_text)
        return r.returncode == 0, r.stdout.strip(), r.stderr.strip()
    except subprocess.TimeoutExpired:
        return False, "", "timeout"
    except Exception as e:
        return False, "", str(e)

def host_read_file(path):
    """Lit un fichier sur l'hôte (None si absent/illisible)."""
    if not _IN_DOCKER:
        try:
            return Path(path).read_text()
        except Exception:
            return None
    ok, out, _ = run_host(["cat", str(path)])
    return out if ok else None

def host_write_file(path, content):
    """Écrit un fichier sur l'hôte (via nsenter en Docker)."""
    if not _IN_DOCKER:
        try:
            Path(path).parent.mkdir(parents=True, exist_ok=True)
            Path(path).write_text(content)
            return True
        except Exception:
            return False
    ok, _, _ = run_host(["sh", "-c", f"cat > {shlex.quote(str(path))}"],
                        input_text=content)
    return ok

def host_remove_file(path):
    """Supprime un fichier sur l'hôte (silencieux si absent)."""
    if not _IN_DOCKER:
        try:
            Path(path).unlink(missing_ok=True)
            return True
        except Exception:
            return False
    ok, _, _ = run_host(["rm", "-f", str(path)])
    return ok

def get_arch():
    m = platform.machine().lower()
    return {"x86_64": "amd64", "aarch64": "arm64", "armv7l": "arm"}.get(m, "amd64")

def service_status(name):
    _, active, _  = run_cmd(["systemctl", "is-active",  name])
    _, enabled, _ = run_cmd(["systemctl", "is-enabled", name])
    return {"active": active.strip(), "enabled": enabled.strip() == "enabled",
            "running": active.strip() == "active"}

def service_action(name, action):
    ok, out, err = run_cmd(["systemctl", action, name])
    return ok, err or out

# ── Docker socket (gestion des containers frpc/frps) ─────────────────────────
_DOCKER_SOCK = Path("/var/run/docker.sock")

class _UnixHTTPConn(_http_client.HTTPConnection):
    """HTTPConnection sur un socket Unix Domain."""
    def __init__(self, path):
        super().__init__("localhost")
        self._path = path
    def connect(self):
        s = _socket.socket(_socket.AF_UNIX, _socket.SOCK_STREAM)
        s.connect(self._path)
        self.sock = s

def _docker_api(method, url_path, body=None, timeout=10):
    """Requête REST vers le socket Docker. Retourne (http_status, data)."""
    if not _DOCKER_SOCK.exists():
        return 0, None
    try:
        conn = _UnixHTTPConn(str(_DOCKER_SOCK))
        conn.timeout = timeout
        hdrs, payload = {}, None
        if body is not None:
            payload = json.dumps(body).encode()
            hdrs["Content-Type"] = "application/json"
        conn.request(method, f"/v1.41{url_path}", body=payload, headers=hdrs)
        resp = conn.getresponse()
        raw  = resp.read()
        try:    data = json.loads(raw)
        except: data = raw.decode(errors="replace")
        return resp.status, data
    except Exception as e:
        return 0, str(e)

def _is_docker_frame(buf):
    """Début d'une trame du flux multiplexé Docker (conteneur SANS tty) :
    type 0/1/2 puis 3 octets nuls. Un conteneur lancé avec tty: true envoie
    au contraire le texte brut, sans en-têtes."""
    return len(buf) >= 4 and buf[0] in (0, 1, 2) and buf[1:4] == b"\x00\x00\x00"

def _docker_demux(buf, tty):
    """Extrait le texte complet de buf → (texte, reste non consommé)."""
    if tty:
        cut = buf.rfind(b"\n") + 1          # ne pas couper un caractère UTF-8
        return buf[:cut].decode("utf-8", errors="replace"), buf[cut:]
    out = []
    # Trame : type[1] + padding[3] + taille[4] (big-endian) + contenu
    while len(buf) >= 8:
        size = int.from_bytes(buf[4:8], "big")
        if len(buf) < 8 + size:
            break
        out.append(buf[8:8 + size].decode("utf-8", errors="replace"))
        buf = buf[8 + size:]
    return "".join(out), buf

def _docker_logs_raw(container_name, tail=200):
    """200 dernières lignes de logs d'un container Docker, décodées."""
    if not _DOCKER_SOCK.exists():
        return ""
    try:
        conn = _UnixHTTPConn(str(_DOCKER_SOCK))
        conn.timeout = 15
        conn.request("GET",
            f"/v1.41/containers/{container_name}/logs?stdout=1&stderr=1&tail={tail}")
        resp = conn.getresponse()
        if resp.status != 200:
            return ""
        raw = resp.read()
        tty = bool(raw) and not _is_docker_frame(raw)
        text, rest = _docker_demux(raw, tty)
        return text + rest.decode("utf-8", errors="replace") if tty else text
    except Exception:
        return ""

class LiveLog:
    """Journal en direct d'une instance, ligne par ligne : journal systemd
    (journalctl -f), fichier de log (tail -F) ou logs Docker en follow.
    close() peut être appelé depuis un autre thread : il arrête le processus
    lu / coupe la connexion Docker, ce qui termine lines()."""

    def __init__(self, inst, source="journal", history=50):
        self.inst = inst
        self.source = "file" if source == "file" else "journal"
        self.history = history   # lignes déjà écrites à renvoyer d'abord (0 à la reconnexion)
        self._proc = None
        self._conn = None

    def close(self):
        if self._proc:
            try: self._proc.terminate()
            except Exception: pass
        if self._conn:
            try:
                if self._conn.sock:
                    self._conn.sock.shutdown(_socket.SHUT_RDWR)   # débloque le recv en cours
            except Exception: pass
            try: self._conn.close()
            except Exception: pass

    def lines(self):
        try:
            if self.inst.get("source") == "docker":
                yield from self._docker_lines(_resolve_container(self.inst["container_name"]))
            elif self.source == "file":
                path = self.inst.get("log")
                if not path:
                    yield "[frp-manager] aucun fichier de log connu pour cette instance"
                    return
                # -F : suit le fichier même après une rotation des logs
                yield from self._command_lines(["tail", f"-n{self.history}", "-F", str(path)])
            else:
                yield from self._command_lines(["journalctl", "-u", self.inst.get("service", ""),
                                                "-f", f"-n{self.history}", "--no-pager", "-o", "short-iso"])
        except Exception:
            pass
        finally:
            self.close()

    def _command_lines(self, cmd):
        if _IN_DOCKER:
            # Comme run_cmd : journal et fichiers de log sont ceux de l'hôte
            cmd = ["nsenter", "-t", "1", "-m", "-u", "-i", "-n", "-p", "--"] + cmd
        self._proc = subprocess.Popen(cmd, stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
                                      text=True, errors="replace")
        for line in self._proc.stdout:
            yield line.rstrip("\n")

    def _docker_lines(self, container):
        """http.client décode le « chunked encoding » de la réponse Docker ;
        les trames multiplexées (ou le texte brut si tty) sont ensuite découpées."""
        if not _DOCKER_SOCK.exists():
            yield "[frp-manager] socket Docker indisponible"
            return
        self._conn = _UnixHTTPConn(str(_DOCKER_SOCK))
        self._conn.request("GET",
            f"/v1.41/containers/{container}/logs?stdout=1&stderr=1&follow=1&tail={self.history}")
        resp = self._conn.getresponse()
        if resp.status != 200:
            yield f"[frp-manager] logs Docker indisponibles (HTTP {resp.status})"
            return
        buf, tty, pending = b"", None, ""
        while True:
            chunk = resp.read1(4096)
            if not chunk:
                break
            buf += chunk
            if tty is None:
                if len(buf) < 4:
                    continue
                tty = not _is_docker_frame(buf)
            text, buf = _docker_demux(buf, tty)
            pending += text
            *lines, pending = pending.split("\n")
            for line in lines:
                yield line.rstrip("\r")

def _resolve_container(container):
    """Nom du container, ou son id court s'il n'est pas trouvé sous ce nom."""
    status, _ = _docker_api("GET", f"/containers/{container}/json")
    if status == 200:
        return container
    s2, data2 = _docker_api("GET", "/containers/json?all=true")
    if s2 == 200 and isinstance(data2, list):
        for c in data2:
            names = [n.lstrip("/") for n in (c.get("Names") or [])]
            if container in names:
                return (c.get("Id") or container)[:12]
    return container

def _detect_docker_frp_containers():
    """Détecte les containers frpc/frps via le socket Docker."""
    if not _DOCKER_SOCK.exists():
        return {}
    status, containers = _docker_api("GET", "/containers/json?all=true")
    if status != 200 or not isinstance(containers, list):
        return {}
    instances = {}
    for c in containers:
        names = c.get("Names") or []
        name  = names[0].lstrip("/") if names else (c.get("Id") or "")[:12]
        image = c.get("Image", "")
        state = c.get("State", "")
        # Ignorer frp-manager lui-même
        if "frp-manager" in name.lower() or "frp-manager" in image.lower():
            continue
        # Détecter frpc ou frps dans le nom ou l'image
        bin_type = None
        for bt in ("frps", "frpc"):
            if bt in name.lower() or bt in image.lower():
                bin_type = bt; break
        if not bin_type:
            continue
        running = state.lower() == "running"
        # Mode réseau du container (affiché dans le tableau de bord)
        net_mode = (c.get("HostConfig") or {}).get("NetworkMode", "")
        iid = f"docker_{name}"
        instances[iid] = {
            "type":           bin_type,
            "source":         "docker",
            "container_name": name,
            "network_mode":   net_mode,
            "image":          image,
            "binary":         Path(f"/docker/{name}"),
            "version":        None,
            "config":         None,
            "service":        name,
            "log":            None,
            "_running":       running,
        }
    return instances

# ── Détection multi-instances ─────────────────────────────────────────────────
INSTANCES          = {}
_detect_cache      = {}
_detect_cache_time = 0
_detect_lock       = threading.Lock()
DETECT_CACHE_TTL   = 6

def _find_binary(name):
    for d in BINARY_SEARCH_PATHS:
        c = d / name
        if c.exists() and os.access(c, os.X_OK):
            return c
    found = shutil.which(name)
    if found:
        return Path(found)
    ok, out, _ = run_cmd(["pgrep", "-a", name])
    if ok and out:
        for line in out.splitlines():
            parts = line.split()
            if len(parts) >= 2:
                c = Path(parts[1])
                if c.exists() and os.access(c, os.X_OK):
                    return c
    return None

def _read_version(binary_path):
    ok, out, _ = run_cmd([str(binary_path), "--version"])
    return out.strip() if ok else None

def _find_systemd_units(bin_name):
    ok, out, _ = run_cmd(["systemctl", "list-unit-files", "--type=service",
                           "--no-pager", "--plain", "--no-legend"])
    candidates = []
    for line in (out or "").splitlines():
        parts = line.split()
        if not parts: continue
        unit = parts[0]
        if not unit.endswith(".service"): continue
        stem = unit[:-8]
        if re.match(rf'^{re.escape(bin_name)}\d*$', stem):
            candidates.append(unit)

    units = []
    for unit in candidates:
        _, prop, _ = run_cmd(["systemctl", "show", unit, "--property=ExecStart", "--value"])
        if f"/{bin_name}" not in prop and f" {bin_name}" not in prop:
            continue
        m = re.search(r'-c\s+(\S+)', prop)
        cfg = Path(m.group(1)) if m else None
        units.append((unit[:-8], cfg))
    return units

def _find_all_configs(bin_type):
    found = []
    for d in CONFIG_SEARCH_PATHS:
        if not d.is_dir(): continue
        for ext in (".toml", ".ini", ".yaml", ".yml"):
            for p in sorted(d.glob(f"{bin_type}*{ext}")):
                if p not in found:
                    found.append(p)
    return found

def _build_instances():
    instances = {}
    for bin_type in ("frps", "frpc"):
        binary  = _find_binary(bin_type)
        version = _read_version(binary) if binary else None
        units   = _find_systemd_units(bin_type)
        configs = _find_all_configs(bin_type)

        if units:
            for unit_name, unit_cfg in units:
                iid = unit_name
                cfg = unit_cfg if (unit_cfg and unit_cfg.exists()) else None
                if not cfg:
                    suffix = re.sub(rf'^{bin_type}', '', unit_name).strip("-_")
                    for c in configs:
                        if suffix and suffix in c.stem:
                            cfg = c; break
                    if not cfg and configs:
                        cfg = configs[0]
                instances[iid] = {
                    "type": bin_type, "binary": binary or FRP_BIN_DIR / bin_type,
                    "version": version, "config": cfg or FRP_CONF_DIR / f"{iid}.toml",
                    "service": iid, "log": FRP_LOG_DIR / f"{iid}.log",
                }
        elif binary:
            if not configs:
                configs = [FRP_CONF_DIR / f"{bin_type}.toml"]
            for i, cfg in enumerate(configs):
                iid = bin_type if i == 0 else f"{bin_type}{i+1}"
                instances[iid] = {
                    "type": bin_type, "binary": binary, "version": version,
                    "config": cfg, "service": iid, "log": FRP_LOG_DIR / f"{iid}.log",
                }
        else:
            # Rien trouvé → pas de stub, ni frps ni frpc
            # En mode Docker, ne pas créer d'instances depuis les binaires hôte
            # sans service systemd associé — ça crée des fantômes non gérables
            if not IN_DOCKER and bin_type == "frps":
                pass  # on ne crée pas de stub non plus
    # Ajouter les containers Docker (sans doublon avec les instances systemd)
    for iid, inst in _detect_docker_frp_containers().items():
        if iid not in instances:
            instances[iid] = inst
    return instances

def detect_frp(force=False):
    global INSTANCES, _detect_cache, _detect_cache_time
    now = time.time()
    with _detect_lock:
        if not force and _detect_cache and (now - _detect_cache_time) < DETECT_CACHE_TTL:
            result = {}
            for iid, inst in _detect_cache.items():
                exists = Path(inst["binary_path"]).exists()
                st = service_status(inst["service"]) if exists else {
                    "active": "not-installed", "enabled": False, "running": False}
                result[iid] = {**inst, "status": st}
            return result

        instances = _build_instances()
        INSTANCES = dict(instances)
        result = {}
        for iid, inst in instances.items():
            # ── Container Docker ──────────────────────────────────────────────
            if inst.get("source") == "docker":
                running = inst.get("_running", False)
                result[iid] = {
                    "id": iid, "type": inst["type"],
                    "source": "docker",
                    "container_name": inst["container_name"],
                    "network_mode": inst.get("network_mode", ""),
                    "image": inst["image"],
                    "binary_path": f"docker:{inst['container_name']}",
                    "binary_found": True,
                    "version": None,
                    "config_path": None,
                    "config_exists": False,
                    "service": inst["service"],
                    "status": {
                        "active": "active" if running else "inactive",
                        "enabled": False,
                        "running": running,
                    },
                    "log_path": None,
                }
                continue
            # ── Instance systemd ──────────────────────────────────────────────
            binary = Path(inst["binary"])
            exists = binary.exists() and os.access(binary, os.X_OK)
            st = service_status(inst["service"]) if exists else {
                "active": "not-installed", "enabled": False, "running": False}
            cfg = Path(inst["config"]) if inst["config"] else None
            result[iid] = {
                "id": iid, "type": inst["type"],
                "source": "systemd",
                "binary_path": str(binary), "binary_found": exists,
                "version": inst["version"],
                "config_path": str(cfg) if cfg else None,
                "config_exists": cfg.exists() if cfg else False,
                "service": inst["service"], "status": st,
                "log_path": str(inst["log"]),
            }
        _detect_cache = result
        _detect_cache_time = now
        return result

_status_changed = threading.Condition()

def _invalidate_cache():
    global _detect_cache_time
    _detect_cache_time = 0
    # Réveille les WebSocket /ws/status : l'état est renvoyé tout de suite après une action
    with _status_changed:
        _status_changed.notify_all()

# ── Download / install ────────────────────────────────────────────────────────
update_lock    = threading.Lock()
update_log_buf = []

def _log(msg):
    ts = datetime.now().strftime("%H:%M:%S")
    update_log_buf.append(f"[{ts}] {msg}")

def fetch_latest_version():
    for name, url in build_version_sources():
        try:
            r = req.get(url, timeout=12, headers={"Accept": "application/vnd.github.v3+json"})
            r.raise_for_status()
            data = r.json()
            tag  = data.get("tag_name") or data.get("tag")
            if tag:
                return tag.lstrip("v"), tag, name
        except Exception:
            continue
    return None, None, "toutes les sources inaccessibles"

def fetch_panel_release(include_prereleases=False):
    """Release GitHub cible du panel (JSON de l'API), ou None.
    Canal stable : la release « latest ». Canal pré-release : la version la plus
    récente parmi toutes les releases publiées, pré-releases comprises."""
    headers = {"Accept": "application/vnd.github.v3+json"}
    if not include_prereleases:
        r = req.get(PANEL_GITHUB_API, timeout=10, headers=headers)
        r.raise_for_status()
        return r.json()
    r = req.get(f"https://api.github.com/repos/{PANEL_GITHUB_REPO}/releases?per_page=30",
                timeout=10, headers=headers)
    r.raise_for_status()
    candidates = [rel for rel in r.json()
                  if not rel.get("draft") and _version_key(rel.get("tag_name", ""))]
    return max(candidates, key=lambda rel: _version_key(rel["tag_name"]), default=None)

def fetch_panel_latest(include_prereleases=False):
    """(version, url) de la release cible du panel, ou (None, None)."""
    if "VOTRE_USER" in PANEL_GITHUB_REPO:
        return None, None   # Repo pas encore configuré
    try:
        data = fetch_panel_release(include_prereleases)
        if not data:
            return None, None
        ver = data.get("tag_name", "").lstrip("v")
        url = data.get("html_url", f"https://github.com/{PANEL_GITHUB_REPO}/releases")
        return ver, url
    except Exception:
        return None, None

def download_archive(version, tag, log_fn):
    arch     = get_arch()
    filename = f"frp_{version}_linux_{arch}.tar.gz"
    for url in build_download_mirrors(tag, filename):
        source = url.split("/")[2]
        log_fn(f"[INFO] Tentative : {source} …")
        try:
            with req.get(url, stream=True, timeout=120, allow_redirects=True) as r:
                r.raise_for_status()
                with tempfile.NamedTemporaryFile(suffix=".tar.gz", delete=False) as tmp:
                    for chunk in r.iter_content(65536):
                        tmp.write(chunk)
                log_fn(f"[OK] Téléchargé depuis {source}")
                return Path(tmp.name), filename
        except Exception as e:
            log_fn(f"[WARN] {source} : {e}")
    return None, filename

def _stop_running_frp_services():
    ok, out, _ = run_cmd(["systemctl", "list-units", "--type=service",
                           "--state=active", "--no-pager", "--plain", "--no-legend"])
    running = []
    for line in (out or "").splitlines():
        parts = line.split()
        if not parts: continue
        unit = parts[0].strip("●▶ ")
        if not unit.endswith(".service"): continue
        name = unit[:-8]
        _, prop, _ = run_cmd(["systemctl", "show", unit, "--property=ExecStart", "--value"])
        if any(b in prop for b in ("/frps", "/frpc")):
            running.append(name)
    for svc in running:
        run_cmd(["systemctl", "stop", svc])
    return running

def install_from_archive(tmp_path, version, log_fn):
    log_fn("[INFO] Arrêt des services frp …")
    running = _stop_running_frp_services()
    if running:
        log_fn(f"[INFO] Stoppés : {', '.join(running)}")
    try:
        log_fn("[INFO] Extraction …")
        with tempfile.TemporaryDirectory() as tmpdir:
            with tarfile.open(tmp_path, "r:gz") as tf:
                tf.extractall(tmpdir)
            extracted = next(Path(tmpdir).iterdir())
            installed = []
            for b in ("frps", "frpc"):
                src = extracted / b
                dst = FRP_BIN_DIR / b
                if src.exists():
                    shutil.copy2(str(src), str(dst))
                    dst.chmod(0o755)
                    log_fn(f"[INFO] {b} → {dst}")
                    installed.append(b)
        if not installed:
            log_fn("[ERROR] Aucun binaire trouvé dans l'archive.")
            return False
        FRP_CONF_DIR.mkdir(parents=True, exist_ok=True)
        FRP_LOG_DIR.mkdir(parents=True, exist_ok=True)
        # On ne crée PAS de configs par défaut — l'utilisateur les gère lui-même
        state = load_state()
        state.update({"installed_version": version,
                      "last_update_check": datetime.now().isoformat(),
                      "last_update_result": f"Installed {version}"})
        save_state(state)
        _invalidate_cache()
        log_fn(f"[OK] frp {version} installé.")
        return True
    except Exception as e:
        log_fn(f"[ERROR] {e}")
        return False
    finally:
        if running:
            log_fn(f"[INFO] Redémarrage : {', '.join(running)} …")
            for svc in running:
                r_ok, _, err = run_cmd(["systemctl", "start", svc])
                log_fn(f"[{'OK' if r_ok else 'WARN'}] {svc}{'' if r_ok else ' : ' + err}")

# ── Routes ────────────────────────────────────────────────────────────────────
@app.route("/")
@login_required
def index():
    return render_template("index.html", panel_version=PANEL_VERSION)

@app.route("/api/detect")
@login_required
def api_detect():
    return jsonify({"ok": True, "instances": detect_frp(force=True), "in_docker": IN_DOCKER})

def _get_frp_installed_version():
    """
    Retourne la version de frp installée.
    Priorité : state.json → binaire frps → binaire frpc → None
    """
    state = load_state()
    v = state.get("installed_version")
    if v:
        return v
    # Lire depuis le binaire directement
    for bin_name in ("frps", "frpc"):
        for d in BINARY_SEARCH_PATHS:
            b = d / bin_name
            if b.exists() and os.access(b, os.X_OK):
                ok, out, _ = run_cmd([str(b), "--version"])
                if ok and out.strip():
                    # Extraire juste le numéro de version (ex: "frps version 0.61.1")
                    m = re.search(r'(\d+\.\d+\.\d+)', out)
                    if m:
                        ver = m.group(1)
                        # Sauvegarder pour éviter de relire le binaire à chaque fois
                        state["installed_version"] = ver
                        save_state(state)
                        return ver
    return None

@app.route("/api/status")
@login_required
def api_status():
    instances = detect_frp(force=False)
    state     = load_state()
    return jsonify({
        "ok": True, "instances": instances,
        "installed_version": _get_frp_installed_version(),
        "last_update_check": state.get("last_update_check"),
    })

@app.route("/api/service/<iid>/<action>", methods=["POST"])
@login_required
def api_service_action(iid, action):
    detect_frp(force=False)
    if iid not in INSTANCES:
        return jsonify({"ok": False, "msg": f"Instance inconnue : {iid}"}), 404
    inst = INSTANCES[iid]
    # ── Container Docker ──────────────────────────────────────────────────────
    if inst.get("source") == "docker":
        if action not in ("start", "stop", "restart"):
            return jsonify({"ok": False,
                "msg": f"Action '{action}' non supportée pour les containers Docker (start/stop/restart uniquement)"}), 400
        container = inst["container_name"]
        status, _ = _docker_api("POST", f"/containers/{container}/{action}")
        ok = status in (200, 204, 304)
        _invalidate_cache()
        return jsonify({"ok": ok, "msg": "OK" if ok else f"Erreur Docker (HTTP {status})"})
    # ── Instance systemd ──────────────────────────────────────────────────────
    if action not in ("start","stop","restart","reload","enable","disable"):
        return jsonify({"ok": False, "msg": "Action invalide"}), 400
    ok, msg = service_action(inst["service"], action)
    _invalidate_cache()
    return jsonify({"ok": ok, "msg": msg or f"{action} {'OK' if ok else 'FAILED'}"})

@app.route("/api/config/<iid>", methods=["GET"])
@login_required
def api_config_get(iid):
    detect_frp(force=False)
    if iid not in INSTANCES:
        return jsonify({"ok": False, "msg": "Instance inconnue"}), 404
    inst = INSTANCES[iid]
    # ── Container Docker : lire la config depuis les volumes montés ───────────
    if inst.get("source") == "docker":
        cfg_content = _get_docker_frpc_config(inst["container_name"])
        if cfg_content:
            return jsonify({"ok": True, "content": cfg_content, "exists": True, "docker": True})
        # Pas de config trouvée → retourner un template vide
        return jsonify({"ok": True, "content": DEFAULT_CONFIGS.get(inst["type"], ""),
                        "exists": False, "docker": True,
                        "msg": "Config non trouvée — assurez-vous que le volume /etc/frp est monté."})
    # ── Instance systemd / binaire ────────────────────────────────────────────
    cfg = Path(inst["config"])
    if not cfg.exists():
        return jsonify({"ok": True, "content": DEFAULT_CONFIGS.get(inst["type"], ""), "exists": False})
    return jsonify({"ok": True, "content": cfg.read_text(), "exists": True})

@app.route("/api/config/<iid>", methods=["POST"])
@login_required
def api_config_save(iid):
    detect_frp(force=False)
    if iid not in INSTANCES:
        return jsonify({"ok": False, "msg": "Instance inconnue"}), 404
    ok, msg = write_instance_config(INSTANCES[iid], (request.get_json() or {}).get("content", ""))
    return jsonify({"ok": ok, "msg": msg}), (200 if ok else 500)

def write_instance_config(inst, content_str):
    """Écrit la config TOML d'une instance (fichier monté pour un container). → (ok, message)"""
    # ── Container Docker : écrire dans le fichier monté ──────────────────────
    if inst.get("source") == "docker":
        container_name = inst.get("container_name", "")
        cfg_path = None
        # Chercher via docker inspect → Mounts
        status, inspect = _docker_api("GET", f"/containers/{container_name}/json")
        if status == 200 and isinstance(inspect, dict):
            for mount in inspect.get("Mounts", []):
                src = mount.get("Source", "")
                if not src:
                    continue
                p = Path(src)
                if p.suffix in (".toml", ".ini") and "frpc" in p.name.lower():
                    cfg_path = p; break
                if p.is_dir():
                    for f in sorted(p.glob("frpc*.toml")):
                        cfg_path = f; break
                if cfg_path:
                    break
        # Fallback : scanner CONFIG_SEARCH_PATHS
        if not cfg_path:
            for search_dir in CONFIG_SEARCH_PATHS:
                if search_dir.is_dir():
                    for f in sorted(search_dir.glob("frpc*.toml")):
                        cfg_path = f; break
                if cfg_path:
                    break
        # Fallback final
        if not cfg_path:
            cfg_path = FRP_CONF_DIR / "frpc.toml"
        try:
            cfg_path.parent.mkdir(parents=True, exist_ok=True)
            cfg_path.write_text(content_str)
            return True, f"Sauvegardé : {cfg_path}"
        except Exception as e:
            return False, f"Erreur écriture : {e}"

    # ── Instance systemd / binaire ────────────────────────────────────────────
    if not inst.get("config"):
        return False, "Aucun fichier de config associé"
    cfg = Path(inst["config"])
    try:
        cfg.parent.mkdir(parents=True, exist_ok=True)
        cfg.write_text(content_str)
        return True, f"Sauvegardé : {cfg}"
    except Exception as e:
        return False, f"Erreur écriture : {e}"

@app.route("/api/logs/<iid>")
@login_required
def api_logs(iid):
    detect_frp(force=False)
    if iid not in INSTANCES:
        return jsonify({"ok": False}), 404
    inst = INSTANCES[iid]
    # ── Container Docker ──────────────────────────────────────────────────────
    if inst.get("source") == "docker":
        container = inst["container_name"]
        # Essayer d'abord via le socket Docker (toujours dispo)
        content = _docker_logs_raw(container, tail=200)
        if not content:
            # Fallback : chercher l'id du container et réessayer
            status, data = _docker_api("GET", f"/containers/{container}/json")
            if status == 200 and isinstance(data, dict):
                cid = data.get("Id", "")[:12]
                content = _docker_logs_raw(cid, tail=200)
        return jsonify({"ok": True, "content": content or "(aucun log disponible)"})
    # ── Instance systemd ──────────────────────────────────────────────────────
    if request.args.get("source") == "file":
        # run_host : en mode Docker, le fichier de log est sur l'hôte, pas dans le container
        ok, out, err = run_host(["tail", "-n200", str(inst["log"])])
        return jsonify({"ok": True, "content": out if ok else (err or f"Fichier illisible : {inst['log']}")})
    ok, out, err = run_cmd(["journalctl", "-u", inst["service"],
                             "-n200", "--no-pager", "-o", "short-iso"])
    return jsonify({"ok": True, "content": out if ok else err})

# Un reverse proxy coupe une connexion qu'il juge inactive (nginx : 60 s par
# défaut) : sans nouvelle ligne ni changement d'état, on envoie un petit message
# applicatif — le ping WebSocket de bas niveau n'est pas compté par tous.
KEEPALIVE_SECONDS  = 20
WS_KEEPALIVE_LOGS  = "\x00"                  # ignoré par l'interface
WS_KEEPALIVE_STATE = '{"keepalive": true}'

def _live_log_from_request(inst):
    try:
        history = max(0, min(200, int(request.args.get("history", 50))))
    except (TypeError, ValueError):
        history = 50
    return LiveLog(inst, request.args.get("source", "journal"), history)

def _pump(live):
    """Lit le journal dans un thread : le consommateur peut attendre avec un délai
    (maintien de connexion, détection de la fermeture) même si rien n'arrive."""
    q = queue.Queue()
    def run():
        try:
            for line in live.lines():
                q.put(line)
        finally:
            q.put(None)
    threading.Thread(target=run, daemon=True).start()
    return q

@app.route("/api/logs/stream/<iid>")
@login_required
def api_logs_stream(iid):
    """Journal en direct en SSE — repli quand le WebSocket n'est pas disponible."""
    detect_frp(force=False)
    inst = INSTANCES.get(iid)
    if not inst:
        return jsonify({"ok": False, "msg": "Instance inconnue"}), 404
    live = _live_log_from_request(inst)
    lines = _pump(live)
    def generate():
        try:
            while True:
                try:
                    line = lines.get(timeout=KEEPALIVE_SECONDS)
                except queue.Empty:
                    yield ": keepalive\n\n"          # commentaire SSE, ignoré par le navigateur
                    continue
                if line is None:
                    break
                yield f"data: {line}\n\n"
        finally:
            live.close()
    return Response(generate(), mimetype="text/event-stream",
                    headers={"Cache-Control": "no-cache", "X-Accel-Buffering": "no"})

def _ws_authenticated(ws):
    if MGR_CFG.get("password_hash") and not session.get("authenticated"):
        ws.close(reason=1008, message="Non authentifié")
        return False
    return True

if sock:
    @sock.route("/ws/status")
    def ws_status(ws):
        """État des instances poussé au navigateur : vérifié toutes les 2 s côté
        serveur (et aussitôt après une action), envoyé seulement s'il a changé.
        Remplace l'appel périodique de /api/status par l'interface."""
        if not _ws_authenticated(ws):
            return
        last, last_sent = None, time.time()
        try:
            while ws.connected:
                try:
                    payload = json.dumps({"instances": detect_frp(force=False), "in_docker": IN_DOCKER},
                                         sort_keys=True, default=str)
                except Exception:
                    payload = None
                if payload and payload != last:
                    ws.send(payload)
                    last, last_sent = payload, time.time()
                elif time.time() - last_sent >= KEEPALIVE_SECONDS:
                    ws.send(WS_KEEPALIVE_STATE)
                    last_sent = time.time()
                with _status_changed:
                    _status_changed.wait(timeout=2)
        except Exception:
            pass

    @sock.route("/ws/logs/<iid>")
    def ws_logs(ws, iid):
        """Journal en direct par WebSocket : une ligne par message texte."""
        if not _ws_authenticated(ws):
            return
        detect_frp(force=False)
        inst = INSTANCES.get(iid)
        if not inst:
            ws.close(reason=1008, message="Instance inconnue")
            return
        live = _live_log_from_request(inst)
        lines = _pump(live)
        last_sent = time.time()
        try:
            # Attente avec délai : on s'aperçoit de la fermeture de l'onglet
            # même quand le journal reste silencieux.
            while ws.connected:
                try:
                    line = lines.get(timeout=1)
                except queue.Empty:
                    if time.time() - last_sent >= KEEPALIVE_SECONDS:
                        ws.send(WS_KEEPALIVE_LOGS)
                        last_sent = time.time()
                    continue
                if line is None:
                    break
                ws.send(line)
                last_sent = time.time()
        except Exception:
            pass
        finally:
            live.close()

@app.route("/api/manager/config", methods=["GET"])
@login_required
def api_manager_config_get():
    safe = {k: v for k, v in MGR_CFG.items() if k not in ("password_hash","secret_key")}
    safe["has_password"] = bool(MGR_CFG.get("password_hash"))
    return jsonify({"ok": True, "config": safe})

@app.route("/api/manager/config", methods=["POST"])
@login_required
def api_manager_config_set():
    global MGR_CFG
    data = request.get_json() or {}
    cfg  = dict(MGR_CFG)
    for k in ("bind_host", "username"):
        if k in data: cfg[k] = str(data[k]).strip()
    for k in ("bind_port", "session_timeout"):
        if k in data: cfg[k] = int(data[k])
    if "ssl_enabled" in data: cfg["ssl_enabled"] = bool(data["ssl_enabled"])
    if data.get("new_password"):
        cfg["password_hash"] = hash_password(data["new_password"])
    save_manager_config(cfg)
    MGR_CFG = cfg
    return jsonify({"ok": True, "msg": "Sauvegardé. Redémarrez frp-manager pour appliquer bind_host/port."})

@app.route("/api/nicknames", methods=["GET"])
@login_required
def api_nicknames_get():
    return jsonify({"ok": True, "nicknames": MGR_CFG.get("nicknames", {})})

@app.route("/api/nickname/<iid>", methods=["POST"])
@login_required
def api_nickname_set(iid):
    global MGR_CFG
    data = request.get_json() or {}
    nick = str(data.get("nickname", "")).strip()[:64]
    cfg  = dict(MGR_CFG)
    nicks = dict(cfg.get("nicknames", {}))
    if nick:
        nicks[iid] = nick
    else:
        nicks.pop(iid, None)
    cfg["nicknames"] = nicks
    save_manager_config(cfg)
    MGR_CFG = cfg
    return jsonify({"ok": True, "msg": "Surnom mis à jour"})

_VERSION_RE = re.compile(r'^v?(\d+)\.(\d+)\.(\d+)(?:-([0-9A-Za-z.]+))?$')

def _version_key(v):
    """'0.1.0' → ((0,1,0), 1, ()) ; '0.1.0-pre.2' → ((0,1,0), 0, …) ; None si non reconnue.
    Une pré-release passe avant la release du même numéro (semver)."""
    m = _VERSION_RE.match(str(v or "").strip())
    if not m:
        return None
    core = tuple(int(x) for x in m.group(1, 2, 3))
    if not m.group(4):
        return (core, 1, ())
    parts = tuple((0, int(p), "") if p.isdigit() else (1, 0, p) for p in m.group(4).split("."))
    return (core, 0, parts)

def is_prerelease_version(v):
    """Tout ce qui n'est pas une release X.Y.Z : 0.1.0-pre.2, dev-abc1234, inconnue…"""
    k = _version_key(v)
    return k is None or k[1] == 0

def version_newer(candidate, current):
    a, b = _version_key(candidate), _version_key(current)
    return bool(a and b and a > b)

_release_status_cache = {}   # version → (horodatage, True/False/None)

def github_release_is_prerelease(version):
    """Statut de la release GitHub v<version> : True/False, ou None si inconnu
    (pas de release, GitHub injoignable). Mis en cache 10 minutes."""
    now = time.time()
    cached = _release_status_cache.get(version)
    if cached and now - cached[0] < 600:
        return cached[1]
    status = None
    try:
        r = req.get(f"https://api.github.com/repos/{PANEL_GITHUB_REPO}/releases/tags/v{version}",
                    timeout=8, headers={"Accept": "application/vnd.github.v3+json"})
        if r.status_code == 200:
            status = bool(r.json().get("prerelease"))
    except Exception:
        pass
    _release_status_cache[version] = (now, status)
    return status

def panel_is_prerelease():
    """Pré-release si le numéro l'indique (dev-<sha>, X.Y.Z-suffixe) ou si la release
    GitHub de cette version est marquée pré-release. Après promotion en release
    définitive, les mises à jour redeviennent possibles sans rien réinstaller."""
    return is_prerelease_version(PANEL_VERSION) or github_release_is_prerelease(PANEL_VERSION) is True

@app.route("/api/panel/version")
@login_required
def api_panel_version():
    """Retourne la version actuelle du panel et vérifie si une mise à jour est dispo.
    Canal stable : releases uniquement. Pré-release en installation classique :
    pré-releases et releases plus récentes. Pré-release sous Docker : rien
    (la mise à jour passe par l'image)."""
    repo_configured = "VOTRE_USER" not in PANEL_GITHUB_REPO
    prerelease = panel_is_prerelease()
    latest_ver, release_url = fetch_panel_latest(include_prereleases=prerelease)
    update_available = bool(latest_ver and repo_configured
                            and not (prerelease and IN_DOCKER)
                            and version_newer(latest_ver, PANEL_VERSION))
    return jsonify({
        "ok":               True,
        "current":          PANEL_VERSION,
        "latest":           latest_ver,
        "release_url":      release_url,
        "update_available": update_available,
        "prerelease":       prerelease,
        "repo":             PANEL_GITHUB_REPO,
        "repo_configured":  repo_configured,
        "in_docker":        IN_DOCKER,
    })

panel_update_log = []
panel_update_lock = threading.Lock()

def _panel_log(msg):
    ts = datetime.now().strftime("%H:%M:%S")
    panel_update_log.append(f"[{ts}] {msg}")

@app.route("/api/panel/update", methods=["POST"])
@login_required
def api_panel_update():
    """Télécharge la dernière release du panel et relance frp-manager."""
    if "VOTRE_USER" in PANEL_GITHUB_REPO:
        return jsonify({"ok": False, "msg": "Repo GitHub du panel non configuré."})
    prerelease = panel_is_prerelease()
    if prerelease and IN_DOCKER:
        return jsonify({"ok": False, "msg": "Pré-release sous Docker : mettez à jour l'image du conteneur."})
    if not panel_update_lock.acquire(blocking=False):
        return jsonify({"ok": False, "msg": "Mise à jour du panel déjà en cours."})

    global panel_update_log
    panel_update_log = []

    def run():
        try:
            _panel_log("[INFO] Récupération des infos de release…")
            try:
                # Une installation en pré-release suit aussi les pré-releases suivantes
                data = fetch_panel_release(include_prereleases=prerelease)
                if not data:
                    _panel_log("[ERROR] Aucune release trouvée.")
                    return
                tag = data.get("tag_name", "")
                assets = data.get("assets", [])
            except Exception as e:
                _panel_log(f"[ERROR] GitHub inaccessible : {e}")
                return
            if not version_newer(tag, PANEL_VERSION):
                _panel_log(f"[ERROR] {tag} n'est pas plus récente que v{PANEL_VERSION} : rien à faire.")
                return

            # Chercher l'asset zip (frp-manager.zip ou frp-manager-vX.X.X.zip)
            zip_url = None
            for a in assets:
                if a["name"].endswith(".zip") and "frp-manager" in a["name"]:
                    zip_url = a["browser_download_url"]
                    break
            # Fallback : source code zip
            if not zip_url:
                zip_url = data.get("zipball_url")

            if not zip_url:
                _panel_log("[ERROR] Aucun asset .zip trouvé dans la release.")
                return

            _panel_log(f"[INFO] Téléchargement de {tag}…")
            try:
                with req.get(zip_url, stream=True, timeout=120) as resp:
                    resp.raise_for_status()
                    with tempfile.NamedTemporaryFile(suffix=".zip", delete=False) as tmp:
                        for chunk in resp.iter_content(65536):
                            tmp.write(chunk)
                        tmp_path = Path(tmp.name)
            except Exception as e:
                _panel_log(f"[ERROR] Téléchargement échoué : {e}")
                return

            _panel_log("[INFO] Extraction…")
            install_dir = Path("/opt/frp-manager")
            try:
                import zipfile
                with zipfile.ZipFile(tmp_path, "r") as zf:
                    members = zf.namelist()
                    # Normaliser les backslashes Windows → / dans les noms d'entrées
                    # (Compress-Archive stocke templates\index.html au lieu de templates/index.html,
                    # ce qui fait que Python extrait un fichier littéralement nommé
                    # "templates\index.html" au lieu de créer le sous-dossier templates/)
                    norm_members = [m.replace("\\", "/") for m in members]

                    # Détecter le préfixe en cherchant app.py dans la liste normalisée
                    prefix = ""
                    for m in norm_members:
                        if m == "app.py" or m.endswith("/app.py"):
                            prefix = m[: m.rfind("/") + 1] if "/" in m else ""
                            break

                    with tempfile.TemporaryDirectory() as tmpdir:
                        # Extraire manuellement avec chemins normalisés
                        for info, norm_name in zip(zf.infolist(), norm_members):
                            info.filename = norm_name
                            zf.extract(info, tmpdir)
                        src_dir = Path(tmpdir) / prefix if prefix else Path(tmpdir)
                        _panel_log(f"[INFO] Source zip : {src_dir} — contenu : {[p.name for p in src_dir.iterdir()] if src_dir.exists() else '?'}")

                        # Copier app.py, templates/, frp-autoupdate.py, install.sh
                        for item in ["app.py", "frp-autoupdate.py", "templates", "install.sh"]:
                            src = src_dir / item
                            dst = install_dir / item
                            if not src.exists():
                                _panel_log(f"[WARN] Absent du zip : {item}")
                                continue
                            if src.is_dir():
                                if dst.exists():
                                    shutil.rmtree(dst)
                                shutil.copytree(str(src), str(dst))
                                n = sum(1 for _ in dst.rglob("*") if _.is_file())
                                _panel_log(f"[INFO] Mis à jour : {item}/ ({n} fichiers)")
                            elif src.is_file():
                                shutil.copy2(str(src), str(dst))
                                _panel_log(f"[INFO] Mis à jour : {item}")

                        # Dépendances Python de la nouvelle version, dans le même
                        # environnement que le panel (venv de l'installation classique)
                        reqs = src_dir / "requirements.txt"
                        if reqs.exists():
                            _panel_log("[INFO] Installation des dépendances Python…")
                            try:
                                rc = subprocess.run([sys.executable, "-m", "pip", "install", "-q", "-r", str(reqs)],
                                                    capture_output=True, text=True, timeout=300)
                                if rc.returncode == 0:
                                    _panel_log("[INFO] Dépendances à jour")
                                else:
                                    _panel_log(f"[WARN] pip : {(rc.stderr or rc.stdout).strip()[-300:]} — le panel démarrera quand même")
                            except Exception as e:
                                _panel_log(f"[WARN] pip indisponible ({e}) — le panel démarrera quand même")
            except Exception as e:
                _panel_log(f"[ERROR] Extraction : {e}")
                return
            finally:
                try: tmp_path.unlink()
                except: pass

            # Sauvegarder la version installée dans state.json
            try:
                p = Path("/var/lib/frp-manager/state.json")
                p.parent.mkdir(parents=True, exist_ok=True)
                state = json.loads(p.read_text()) if p.exists() else {}
                state["panel_version"] = tag.lstrip("v")
                p.write_text(json.dumps(state, indent=2))
                _panel_log(f"[INFO] Version {tag} sauvegardée dans state.json")
            except Exception as e:
                _panel_log(f"[WARN] Impossible de sauvegarder la version : {e}")

            _panel_log(f"[OK] Panel {tag} installé. Redémarrage dans 2s…")
            def restart():
                time.sleep(2)
                _panel_log("[INFO] Redémarrage de frp-manager…")
                subprocess.Popen(["systemctl", "restart", "frp-manager"])
            threading.Thread(target=restart, daemon=True).start()

        finally:
            panel_update_lock.release()

    threading.Thread(target=run, daemon=True).start()
    return jsonify({"ok": True})

@app.route("/api/panel/update/log")
@login_required
def api_panel_update_log():
    return jsonify({"ok": True, "lines": panel_update_log})

@app.route("/api/connectivity")
@login_required
def api_connectivity():
    results = {}
    sources = build_version_sources()
    def test(name, url):
        try:
            r = req.get(url, timeout=8, headers={"Accept": "application/vnd.github.v3+json"})
            r.raise_for_status()
            results[name] = {"ok": True, "version": r.json().get("tag_name","?")}
        except Exception as e:
            results[name] = {"ok": False, "error": str(e)[:120]}
    threads = [threading.Thread(target=test, args=(n,u)) for n,u in sources]
    for t in threads: t.start()
    for t in threads: t.join()
    return jsonify({"ok": True, "sources": results})

@app.route("/api/update/check")
@login_required
def api_update_check():
    version, tag, source = fetch_latest_version()
    if not version:
        return jsonify({"ok": False, "msg": "Toutes les sources inaccessibles."})
    installed = load_state().get("installed_version")
    state = load_state()
    state["last_update_check"] = datetime.now().isoformat()
    save_state(state)
    return jsonify({"ok": True, "latest": version, "tag": tag, "installed": installed,
                    "source": source, "update_available": installed != version if installed else True})

@app.route("/api/update/install", methods=["POST"])
@login_required
def api_update_install():
    if not update_lock.acquire(blocking=False):
        return jsonify({"ok": False, "msg": "Mise à jour déjà en cours"})
    global update_log_buf
    update_log_buf = []
    def run():
        try:
            version, tag, source = fetch_latest_version()
            if not version:
                _log("[ERROR] Toutes les sources inaccessibles. Utilisez l'upload manuel.")
                return
            _log(f"[INFO] Version : {tag} via {source}")
            tmp, _ = download_archive(version, tag, _log)
            if not tmp:
                _log("[ERROR] Tous les miroirs ont échoué. Utilisez l'upload manuel.")
                return
            try:
                install_from_archive(tmp, version, _log)
            finally:
                try: tmp.unlink()
                except: pass
        finally:
            update_lock.release()
    threading.Thread(target=run, daemon=True).start()
    return jsonify({"ok": True})

@app.route("/api/update/upload", methods=["POST"])
@login_required
def api_update_upload():
    if not update_lock.acquire(blocking=False):
        return jsonify({"ok": False, "msg": "Mise à jour déjà en cours"})
    global update_log_buf
    update_log_buf = []
    if "file" not in request.files:
        update_lock.release()
        return jsonify({"ok": False, "msg": "Aucun fichier reçu"})
    f       = request.files["file"]
    version = request.form.get("version","").strip().lstrip("v") or "manual"
    with tempfile.NamedTemporaryFile(suffix=".tar.gz", delete=False) as tmp:
        f.save(tmp.name)
        tmp_path = Path(tmp.name)
    def run():
        try:
            install_from_archive(tmp_path, version, _log)
        finally:
            try: tmp_path.unlink()
            except: pass
            update_lock.release()
    threading.Thread(target=run, daemon=True).start()
    return jsonify({"ok": True})

@app.route("/api/update/log")
@login_required
def api_update_log():
    return jsonify({"ok": True, "lines": update_log_buf})

# ── Ports ────────────────────────────────────────────────────────────────────
def _extract_ports_from_config(content, bin_type):
    """Extrait les numéros de port d'une config frp TOML."""
    ports = []
    if bin_type == "frps":
        top_patterns = [
            (r'^bindPort\s*=\s*(\d+)', "tcp", "Connexion frpc"),
            (r'^kcpBindPort\s*=\s*(\d+)', "udp", "KCP"),
            (r'^quicBindPort\s*=\s*(\d+)', "udp", "QUIC"),
            (r'^vhostHTTPPort\s*=\s*(\d+)', "tcp", "vhost HTTP"),
            (r'^vhostHTTPSPort\s*=\s*(\d+)', "tcp", "vhost HTTPS"),
        ]
        for pat, proto, label in top_patterns:
            m = re.search(pat, content, re.MULTILINE | re.IGNORECASE)
            if m:
                ports.append({"port": int(m.group(1)), "proto": proto, "label": label})
    elif bin_type == "frpc":
        m = re.search(r'^serverPort\s*=\s*(\d+)', content, re.MULTILINE | re.IGNORECASE)
        if m:
            ports.append({"port": int(m.group(1)), "proto": "tcp", "label": "Connexion serveur"})
        # Extraire les remotePort de chaque [[proxies]] (ports exposés côté serveur frps)
        in_proxy = False
        proxy_name = ""
        proxy_type = "tcp"
        for line in content.splitlines():
            s = line.strip()
            if s == "[[proxies]]":
                in_proxy = True
                proxy_name = ""
                proxy_type = "tcp"
                continue
            if s.startswith("[") and not s.startswith("[[proxies]]"):
                in_proxy = False
                continue
            if in_proxy:
                nm = re.match(r'name\s*=\s*["\']?([^"\']+)["\']?', s)
                if nm:
                    proxy_name = nm.group(1).strip()
                tm = re.match(r'type\s*=\s*["\']?(\w+)["\']?', s)
                if tm:
                    proxy_type = tm.group(1).strip()
                rm = re.match(r'remotePort\s*=\s*(\d+)', s)
                if rm:
                    proto = "udp" if proxy_type == "udp" else "tcp"
                    label = f"Tunnel {proxy_name or proxy_type} (remotePort)"
                    ports.append({"port": int(rm.group(1)), "proto": proto, "label": label})
        # Extraire les bindPort de chaque [[visitors]] (écoute LOCALE côté frpc).
        # On ne les remonte que si bindAddr n'est pas loopback : un visiteur sur
        # 127.0.0.1 n'a pas besoin d'ouverture firewall.
        in_visitor = False
        vis_name = ""
        vis_bind_addr = "127.0.0.1"
        vis_bind_port = None
        def _flush_visitor():
            if vis_bind_port and vis_bind_addr not in ("127.0.0.1", "::1", "localhost"):
                ports.append({"port": vis_bind_port, "proto": "tcp",
                              "label": f"Visiteur {vis_name or ''} (bindPort)".strip()})
        for line in content.splitlines():
            s = line.strip()
            if s == "[[visitors]]":
                _flush_visitor()
                in_visitor, vis_name, vis_bind_addr, vis_bind_port = True, "", "127.0.0.1", None
                continue
            if s.startswith("[") and s != "[[visitors]]":
                _flush_visitor()
                in_visitor = False
                continue
            if in_visitor:
                nm = re.match(r'name\s*=\s*["\']?([^"\']+)["\']?', s)
                if nm:
                    vis_name = nm.group(1).strip()
                am = re.match(r'bindAddr\s*=\s*["\']?([^"\']+)["\']?', s)
                if am:
                    vis_bind_addr = am.group(1).strip()
                bm = re.match(r'bindPort\s*=\s*(\d+)', s)
                if bm:
                    vis_bind_port = int(bm.group(1))
        _flush_visitor()
    # Port du webServer (section [webServer]) — frps & frpc
    in_ws = False
    for line in content.splitlines():
        s = line.strip()
        if s == "[webServer]":
            in_ws = True
        elif s.startswith("["):
            in_ws = False
        elif in_ws:
            m = re.match(r'port\s*=\s*(\d+)', s)
            if m:
                ports.append({"port": int(m.group(1)), "proto": "tcp", "label": "Dashboard web"})
    return ports

def _ufw_allowed_ports():
    """Retourne (ufw_disponible, ensemble_des_ports_autorisés)."""
    ok, out, err = run_cmd(["ufw", "status"])
    combined = (out + err).lower()
    if any(x in combined for x in ("not found", "command not found", "no such file")):
        return False, set()
    allowed = set()
    if "inactive" in out.lower():
        return True, allowed  # UFW dispo mais inactif
    for line in out.splitlines():
        # Lignes comme : "7000/tcp    ALLOW IN    Anywhere"
        m = re.match(r'\s*(\d+)(?:/(\w+))?\s+ALLOW', line, re.IGNORECASE)
        if m:
            port = int(m.group(1))
            proto = (m.group(2) or "tcp").lower()
            allowed.add((port, proto))
            allowed.add((port, "any"))
    return True, allowed

def _get_docker_frpc_config(container_name):
    """
    Trouve et lit la config TOML d un container frpc.
    Stratégies par ordre de priorité :
    1. Mounts du container : cherche frpc*.toml dans les sources montées
    2. Dossiers standards (/etc/frp, /etc/frp-manager) accessibles depuis le panel
    3. Args du container (-c /path)
    """
    status, data = _docker_api("GET", f"/containers/{container_name}/json")
    if status != 200 or not isinstance(data, dict):
        # Essayer avec le nom sans préfixe docker_
        alt = container_name.replace("docker_", "")
        status, data = _docker_api("GET", f"/containers/{alt}/json")
        if status != 200 or not isinstance(data, dict):
            return None

    # 1. Chercher dans les Mounts (chemins hôte directement lisibles)
    mounts = data.get("Mounts", [])
    for mount in mounts:
        src = mount.get("Source", "")
        if not src:
            continue
        p = Path(src)
        # Fichier toml direct
        if p.suffix in (".toml", ".ini") and "frpc" in p.name.lower():
            try:
                return p.read_text()
            except Exception:
                pass
        # Dossier : scanner les frpc*.toml
        if p.is_dir():
            for f in sorted(p.glob("frpc*.toml")):
                try:
                    return f.read_text()
                except Exception:
                    pass

    # 2. Chercher dans les dossiers standards (accessibles via volumes partagés)
    for search_dir in CONFIG_SEARCH_PATHS:
        if search_dir.is_dir():
            for f in sorted(search_dir.glob("frpc*.toml")):
                try:
                    return f.read_text()
                except Exception:
                    pass

    # 3. Chercher dans les args du container (-c /path/frpc.toml)
    cmd  = data.get("Config", {}).get("Cmd") or []
    args = data.get("Args") or []
    for lst in (cmd, args):
        for i, arg in enumerate(lst):
            if arg in ("-c", "--config") and i + 1 < len(lst):
                p = Path(lst[i + 1])
                try:
                    return p.read_text()
                except Exception:
                    pass

    return None

@app.route("/api/ports")
@login_required
def api_ports():
    instances = detect_frp(force=False)
    ports = []
    for iid, inst in instances.items():
        # ── Instance systemd / binaire classique ──────────────────────────
        if inst.get("source") != "docker":
            cfg_path = inst.get("config_path")
            if not cfg_path or not inst.get("config_exists"):
                continue
            try:
                cfg_content = Path(cfg_path).read_text()
                for p in _extract_ports_from_config(cfg_content, inst["type"]):
                    p.update({"iid": iid, "type": inst["type"], "service": inst["service"]})
                    ports.append(p)
            except Exception:
                pass
        # ── Container Docker frpc ─────────────────────────────────────────
        elif inst.get("type") == "frpc":
            container_name = inst.get("container_name", iid)
            cfg_content = _get_docker_frpc_config(container_name)
            if cfg_content:
                for p in _extract_ports_from_config(cfg_content, "frpc"):
                    p.update({"iid": iid, "type": "frpc", "service": container_name,
                               "source": "docker"})
                    ports.append(p)
    ufw_ok, allowed = _ufw_allowed_ports()
    for p in ports:
        p["ufw_allowed"] = (p["port"], p["proto"]) in allowed or (p["port"], "any") in allowed
    return jsonify({"ok": True, "ports": ports, "ufw_available": ufw_ok})

@app.route("/api/ports/open", methods=["POST"])
@login_required
def api_ports_open():
    data = request.get_json() or {}
    try:
        port = int(data.get("port", 0))
    except (ValueError, TypeError):
        return jsonify({"ok": False, "msg": "Port invalide"}), 400
    if not (1 <= port <= 65535):
        return jsonify({"ok": False, "msg": "Port invalide"}), 400
    proto = data.get("proto", "tcp").lower()
    if proto not in ("tcp", "udp"):
        proto = "tcp"
    ok, out, err = run_cmd(["ufw", "allow", f"{port}/{proto}"])
    msg = (out or err or "").strip()
    return jsonify({"ok": ok, "msg": msg or f"Port {port}/{proto} {'ouvert' if ok else 'erreur'}"})

# ── Retrait de go-mmproxy (option « IP réelle », supprimée en 0.0.26) ───────
# Les installations qui l'utilisaient ont des tunnels frpc pointant vers un relais
# local (127.0.0.1:18xxx) avec le PROXY protocol v2 activé automatiquement. Au
# démarrage, on remet chaque tunnel sur son vrai service, on redémarre frpc,
# puis on supprime relais, règles de routage, binaire et fichiers d'état.
# Si une étape échoue, l'état est gardé et la migration réessaie au démarrage suivant.
MMPROXY_STATE_FILE  = MGR_CONF_DIR / "mmproxy.json"
MMPROXY_ROUTES_UNIT = "frp-mmproxy-routes"
SYSTEMD_UNIT_DIR    = "/etc/systemd/system"

def _unrelay_frpc_config(text, relays):
    """Dans chaque [[proxies]] relayé (nom présent dans `relays` ET localPort égal au
    port du relais), remet localIP/localPort sur la cible et retire le PROXY protocol
    ajouté pour le relais. Retourne (texte, nombre de tunnels modifiés)."""
    out, block, changed = [], [], 0

    def flush():
        nonlocal changed
        if not block:
            return
        name = port = None
        for line in block:
            m = re.match(r'\s*name\s*=\s*"([^"]*)"', line)
            if m:
                name = m.group(1)
            m = re.match(r'\s*localPort\s*=\s*(\d+)', line)
            if m:
                port = int(m.group(1))
        e = relays.get(name)
        if e and port and port == e.get("listen_port"):
            changed += 1
            for line in block:
                if re.match(r'\s*localIP\s*=', line):
                    out.append(f'localIP = "{e.get("target_ip", "127.0.0.1")}"\n')
                elif re.match(r'\s*localPort\s*=', line):
                    out.append(f'localPort = {e.get("target_port")}\n')
                elif re.match(r'\s*transport\.proxyProtocolVersion\s*=', line):
                    continue
                else:
                    out.append(line)
        else:
            out.extend(block)
        block.clear()

    in_proxy = False
    for line in text.splitlines(keepends=True):
        s = line.strip()
        if s.startswith("["):
            if s == "[[proxies]]":
                flush()
                in_proxy = True
                block.append(line)
                continue
            if not (in_proxy and s.startswith("[proxies.")):
                flush()
                in_proxy = False
        (block if in_proxy else out).append(line)
    flush()
    return "".join(out), changed

def _read_instance_config(inst):
    if inst.get("source") == "docker":
        return _get_docker_frpc_config(inst.get("container_name", ""))
    try:
        return Path(inst["config"]).read_text() if inst.get("config") else None
    except Exception:
        return None

def migrate_away_from_mmproxy():
    if not MMPROXY_STATE_FILE.exists():
        return
    try:
        st = json.loads(MMPROXY_STATE_FILE.read_text())
    except Exception:
        st = {}
    instances = dict(st.get("instances") or {})
    print(f"[INFO] Retrait de go-mmproxy : {sum(len(v) for v in instances.values())} relais à migrer")
    detect_frp(force=True)
    remaining = {}

    for iid, relays in instances.items():
        inst = INSTANCES.get(iid)
        text = _read_instance_config(inst) if inst else None
        if not relays:
            continue
        if text is None:
            print(f"[WARN] go-mmproxy : config de {iid} illisible, relais conservés pour l'instant")
            remaining[iid] = relays
            continue
        new_text, changed = _unrelay_frpc_config(text, relays)
        if changed:
            ok, msg = write_instance_config(inst, new_text)
            if not ok:
                print(f"[WARN] go-mmproxy : {msg} — relais de {iid} conservés")
                remaining[iid] = relays
                continue
            if inst.get("source") == "docker":
                _docker_api("POST", f"/containers/{inst['container_name']}/restart")
            else:
                service_action(inst["service"], "restart")
            print(f"[OK] go-mmproxy : {changed} tunnel(s) de {iid} remis en connexion directe, frpc redémarré")
        for name, e in relays.items():
            unit = e.get("unit")
            if unit:
                run_cmd(["systemctl", "disable", "--now", f"{unit}.service"])
                host_remove_file(f"{SYSTEMD_UNIT_DIR}/{unit}.service")

    if remaining:
        st["instances"] = remaining
        MMPROXY_STATE_FILE.write_text(json.dumps(st, indent=2))
        run_cmd(["systemctl", "daemon-reload"])
        return

    run_cmd(["systemctl", "disable", "--now", f"{MMPROXY_ROUTES_UNIT}.service"])
    host_remove_file(f"{SYSTEMD_UNIT_DIR}/{MMPROXY_ROUTES_UNIT}.service")
    run_cmd(["systemctl", "daemon-reload"])
    host_remove_file("/usr/local/bin/go-mmproxy")
    for f in (MMPROXY_STATE_FILE, MGR_CONF_DIR / "mmproxy-allowed.txt"):
        try:
            f.unlink()
        except FileNotFoundError:
            pass
    print("[OK] go-mmproxy retiré (relais, règles de routage, binaire et état)")

if __name__ == "__main__":
    host = MGR_CFG.get("bind_host", os.environ.get("FRP_MANAGER_HOST", "0.0.0.0"))
    port = MGR_CFG.get("bind_port", int(os.environ.get("FRP_MANAGER_PORT", 8765)))
    ssl_ctx = get_ssl_context()
    proto = "https" if ssl_ctx else "http"
    print(f"[INFO] FRP Manager démarré sur {proto}://{host}:{port}")
    threading.Thread(target=migrate_away_from_mmproxy, daemon=True).start()
    app.run(host=host, port=port, debug=False, ssl_context=ssl_ctx)
