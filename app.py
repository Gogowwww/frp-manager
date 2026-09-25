#!/usr/bin/env python3
"""FRP Manager — backend Flask multi-instances"""

import os, re, sys, json, subprocess, threading, shutil, shlex, tarfile, tempfile, platform, time, secrets, hashlib, ssl, queue, gzip
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
# Gravée dans le zip de chaque release par release.yml ; vide dans les sources.
_PANEL_VERSION_FALLBACK = ""
PANEL_GITHUB_REPO = "Gogowwww/frp-manager"
PANEL_GITHUB_API  = f"https://api.github.com/repos/{PANEL_GITHUB_REPO}/releases/latest"

def _load_panel_version():
    """
    Priorité :
    1. Variable d'env PANEL_DOCKER_VERSION (injectée au build Docker via ARG)
    2. Version gravée dans ce code par release.yml : c'est celle des fichiers
       réellement installés, même posés à la main (install.sh ne touche pas
       à state.json, qui garderait sinon l'ancienne version)
    3. state.json panel_version (lancement depuis les sources, sans gravure)
    """
    # 1. Version injectée dans l'image Docker au build
    docker_ver = os.environ.get("PANEL_DOCKER_VERSION", "").strip()
    if docker_ver and docker_ver != "unknown":
        return docker_ver
    # 2. Version gravée dans le zip de la release
    if _PANEL_VERSION_FALLBACK:
        return _PANEL_VERSION_FALLBACK
    # 3. Version notée par la mise à jour automatique
    try:
        p = Path("/var/lib/frp-manager/state.json")
        if p.exists():
            v = json.loads(p.read_text()).get("panel_version")
            if v:
                return v
    except Exception:
        pass
    return "0.0.0"

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

# ── Fichiers du site (CSS, JS, traductions) ──────────────────────────────────
# Servis sous une adresse qui contient leur empreinte (/v/<empreinte>/js/main.js) :
# le navigateur les garde un an sans rien redemander, et toute modification
# (mise à jour du panel) change l'empreinte, donc l'adresse. Les imports
# relatifs entre modules restent sous la même empreinte. Chaque fichier est lu
# et compressé (gzip) une seule fois, au démarrage.
# L'ancienne adresse /assets/… reste servie (pages encore ouvertes pendant une
# mise à jour).
_ASSETS_DIR   = Path(app.root_path) / "templates" / "assets"
_ASSET_TYPES  = {".js": "text/javascript", ".css": "text/css", ".json": "application/json",
                 ".svg": "image/svg+xml", ".png": "image/png", ".woff2": "font/woff2"}
_COMPRESSIBLE = {".js", ".css", ".json", ".svg"}

def _load_assets():
    files, digest = {}, hashlib.sha256()
    for f in sorted(_ASSETS_DIR.rglob("*")):
        if not f.is_file() or f.suffix not in _ASSET_TYPES:
            continue
        rel, data = f.relative_to(_ASSETS_DIR).as_posix(), f.read_bytes()
        digest.update(rel.encode() + b"\0" + data)
        gz = gzip.compress(data, 9, mtime=0) if f.suffix in _COMPRESSIBLE and len(data) > 512 else None
        files[rel] = (data, gz if gz and len(gz) < len(data) else None, _ASSET_TYPES[f.suffix])
    return files, digest.hexdigest()[:12]

_ASSETS, ASSETS_VERSION = _load_assets()
# Modules chargés par la page, annoncés d'un coup (<link rel="modulepreload">) :
# sans ça, le navigateur les découvre import après import, en cascade.
_PRELOAD_MODULES = sorted(r for r in _ASSETS if r.startswith("js/") and r.endswith(".js")) + ["locales/fr.js"]

def _accepts_gzip():
    return "gzip" in request.headers.get("Accept-Encoding", "")

@app.route("/v/<version>/<path:filename>")
def versioned_asset(version, filename):
    entry = _ASSETS.get(filename)
    if not entry:
        return Response("Introuvable", status=404, mimetype="text/plain")
    data, gz, mime = entry
    resp = Response(gz if gz and _accepts_gzip() else data, mimetype=mime)
    if gz:
        resp.headers["Vary"] = "Accept-Encoding"
        if _accepts_gzip():
            resp.headers["Content-Encoding"] = "gzip"
    # Une empreinte périmée (page ouverte avant une mise à jour) : pas de cache
    resp.headers["Cache-Control"] = ("public, max-age=31536000, immutable"
                                     if version == ASSETS_VERSION else "no-cache")
    return resp

@app.context_processor
def _asset_helpers():
    return {"asset": lambda rel: f"/v/{ASSETS_VERSION}/{rel}", "preload_modules": _PRELOAD_MODULES}

_GZIP_TYPES = {"application/json", "text/html", "text/plain"}

@app.after_request
def _compress_response(resp):
    """Réponses de l'API et pages HTML compressées au-delà de 1 Ko (listes de
    ports, états, journaux…). Flux (SSE, WebSocket) et fichiers laissés tels quels."""
    if (resp.status_code != 200 or resp.direct_passthrough or resp.is_streamed
            or resp.mimetype not in _GZIP_TYPES or "Content-Encoding" in resp.headers
            or request.path.startswith("/ws/") or not _accepts_gzip()):
        return resp
    data = resp.get_data()
    if len(data) < 1024:
        return resp
    resp.set_data(gzip.compress(data, 5))
    resp.headers["Content-Encoding"] = "gzip"
    resp.headers["Vary"] = "Accept-Encoding"
    return resp

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
# Deux étages, pour ne pas lancer une douzaine de processus toutes les 2 s :
#   - la DÉCOUVERTE (services systemd, programmes, configs, containers) change
#     rarement : refaite toutes les 30 s, ou aussitôt après une action qui la
#     modifie (installation, suppression, configuration enregistrée) ;
#   - l'ÉTAT (en marche, démarrage automatique) : un seul « systemctl show »
#     pour toutes les instances et une seule liste Docker, partagés 1,5 s entre
#     tous les appelants (onglets ouverts, WebSocket, requêtes).
INSTANCES          = {}
_detect_cache      = {}          # résultat de la découverte, sans l'état
_detect_cache_time = 0
_status_cache      = {}
_status_time       = 0
_detect_lock       = threading.Lock()
DISCOVERY_TTL      = 30
STATUS_TTL         = 1.5
_version_cache     = {}          # binaire → (mtime, sortie de --version)
_STOPPED = {"active": "inactive", "enabled": False, "running": False}
_MISSING = {"active": "not-installed", "enabled": False, "running": False}

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
    """« frps --version », relu seulement si le binaire a changé (mise à jour)."""
    try:
        mtime = Path(binary_path).stat().st_mtime
    except OSError:
        return None
    cached = _version_cache.get(str(binary_path))
    if cached and cached[0] == mtime:
        return cached[1]
    ok, out, _ = run_cmd([str(binary_path), "--version"])
    ver = out.strip() if ok else None
    _version_cache[str(binary_path)] = (mtime, ver)
    return ver

def _systemctl_show(units, props):
    """« systemctl show » de plusieurs unités en un seul appel → {unité: {propriété: valeur}}."""
    if not units:
        return {}
    args = ["systemctl", "show", "--no-pager", "-p", "Id"]
    for prop in props:
        args += ["-p", prop]
    _, out, _ = run_cmd(args + list(units))
    result, block = {}, {}
    for line in (out or "").splitlines() + [""]:
        if not line.strip():
            if block.get("Id"):
                result[block["Id"]] = block
            block = {}
            continue
        key, _, value = line.partition("=")
        block[key] = value
    return result

def _find_systemd_units():
    """Services frps*/frpc* dont la commande lance vraiment frps/frpc :
    une liste des unités et un « systemctl show » pour toutes, pas un par unité.
    → {"frps": [(nom, config)], "frpc": [...]}"""
    _, out, _ = run_cmd(["systemctl", "list-unit-files", "--type=service",
                         "--no-pager", "--plain", "--no-legend"])
    candidates = [line.split()[0] for line in (out or "").splitlines()
                  if line.split() and re.match(r"^frp[sc]\d*\.service$", line.split()[0])]
    execs = _systemctl_show(candidates, ["ExecStart"])
    found = {"frps": [], "frpc": []}
    for unit in candidates:
        bin_name = unit[:4]
        prop = execs.get(unit, {}).get("ExecStart", "")
        if f"/{bin_name}" not in prop and f" {bin_name}" not in prop:
            continue
        m = re.search(r"-c\s+([^\s;]+)", prop)
        found[bin_name].append((unit[:-8], Path(m.group(1)) if m else None))
    return found

def _find_all_configs(bin_type):
    found = []
    for d in CONFIG_SEARCH_PATHS:
        if not d.is_dir(): continue
        for ext in (".toml", ".ini", ".yaml", ".yml"):
            for p in sorted(d.glob(f"{bin_type}*{ext}")):
                if p not in found:
                    found.append(p)
    return found

_SERVER_ADDR_RE = re.compile(r"""^\s*(?:serverAddr|server_addr)\s*[=:]\s*["']?([^"'\s#]+)""", re.MULTILINE)

_SERVER_ADDR_RE = re.compile(r"""^\s*(?:serverAddr|server_addr)\s*[=:]\s*["']?([^"'\s#]+)""", re.MULTILINE)

def _frpc_configured(cfg):
    """Un serveur est-il renseigné ? install.sh crée partout un frpc.toml avec
    serverAddr = "" : sans ça, un frpc jamais utilisé compterait comme un vrai."""
    try:
        return bool(cfg and cfg.exists() and _SERVER_ADDR_RE.search(cfg.read_text()))
    except Exception:
        return False

def _build_instances():
    instances = {}
    all_units = _find_systemd_units()
    for bin_type in ("frps", "frpc"):
        binary  = _find_binary(bin_type)
        version = _read_version(binary) if binary else None
        units   = all_units[bin_type]
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
            # Programme sans service systemd : une instance par config existante,
            # sauf celles dont le service a été supprimé depuis le tableau de bord
            # (sinon la carte réapparaîtrait aussitôt). Pas d'instance sans fichier.
            dismissed = set(MGR_CFG.get("dismissed_configs", []))
            configs = [c for c in configs if str(c) not in dismissed]
            for i, cfg in enumerate(configs):
                iid = bin_type if i == 0 else f"{bin_type}{i+1}"
                instances[iid] = {
                    "type": bin_type, "binary": binary, "version": version,
                    "config": cfg, "service": iid, "log": FRP_LOG_DIR / f"{iid}.log",
                }
    # Ajouter les containers Docker (sans doublon avec les instances systemd)
    for iid, inst in _detect_docker_frp_containers().items():
        if iid not in instances:
            instances[iid] = inst
    return instances

def _discover():
    """Découverte complète → (INSTANCES, description de chaque instance sans son état)."""
    instances = _build_instances()
    result = {}
    for iid, inst in instances.items():
        if inst.get("source") == "docker":
            result[iid] = {
                "id": iid, "type": inst["type"], "source": "docker",
                "container_name": inst["container_name"],
                "network_mode": inst.get("network_mode", ""),
                "image": inst["image"],
                "binary_path": f"docker:{inst['container_name']}", "binary_found": True,
                "version": None, "config_path": None, "config_exists": False,
                "service": inst["service"], "log_path": None, "configured": True,
            }
            continue
        binary = Path(inst["binary"])
        cfg = Path(inst["config"]) if inst["config"] else None
        result[iid] = {
            "id": iid, "type": inst["type"], "source": "systemd",
            "binary_path": str(binary), "binary_found": binary.exists() and os.access(binary, os.X_OK),
            "version": inst["version"],
            "config_path": str(cfg) if cfg else None,
            "config_exists": cfg.exists() if cfg else False,
            "service": inst["service"], "log_path": str(inst["log"]),
            "configured": _frpc_configured(cfg) if inst["type"] == "frpc" else bool(cfg and cfg.exists()),
        }
    return instances, result

def _read_statuses(found):
    """État de toutes les instances : un « systemctl show » et une liste Docker en tout."""
    units = [f"{i['service']}.service" for i in found.values()
             if i["source"] == "systemd" and i["binary_found"]]
    shown = _systemctl_show(units, ["ActiveState", "UnitFileState"])
    containers = {}
    if any(i["source"] == "docker" for i in found.values()):
        st, data = _docker_api("GET", "/containers/json?all=true")
        if st == 200 and isinstance(data, list):
            for c in data:
                running = (c.get("State") or "").lower() == "running"
                for name in c.get("Names") or []:
                    containers[name.lstrip("/")] = running
    statuses = {}
    for iid, inst in found.items():
        if inst["source"] == "docker":
            running = containers.get(inst["container_name"], False)
            statuses[iid] = {"active": "active" if running else "inactive", "enabled": False, "running": running}
        elif not inst["binary_found"]:
            statuses[iid] = dict(_MISSING)
        else:
            props = shown.get(f"{inst['service']}.service", {})
            active = props.get("ActiveState", "")
            statuses[iid] = {"active": active, "enabled": props.get("UnitFileState") == "enabled",
                             "running": active == "active"}
    return statuses

def detect_frp(force=False):
    global INSTANCES, _detect_cache, _detect_cache_time, _status_cache, _status_time
    with _detect_lock:
        now = time.time()
        if force or not _detect_cache_time or now - _detect_cache_time >= DISCOVERY_TTL:
            INSTANCES, _detect_cache = _discover()
            _detect_cache_time = now
            _status_time = 0
        if now - _status_time >= STATUS_TTL:
            _status_cache = _read_statuses(_detect_cache)
            _status_time = time.time()
        return {iid: {**inst, "status": _status_cache.get(iid, dict(_STOPPED))}
                for iid, inst in _detect_cache.items()}

_status_changed = threading.Condition()

def _invalidate_cache(discovery=False):
    """État relu au prochain appel ; discovery=True refait aussi la découverte
    (installation, suppression, configuration modifiée)."""
    global _detect_cache_time, _status_time
    _status_time = 0
    if discovery:
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

# Réponses de GitHub gardées 10 min (1 min en cas d'échec) : l'interface vérifie
# les mises à jour à chaque ouverture de page, et l'API GitHub n'accepte que 60
# appels par heure sans compte. Le bouton « Vérifier » passe outre (?fresh=1).
_github_cache = {}

def _github_cached(key, fetch, fresh=False):
    now, hit = time.time(), _github_cache.get(key)
    if hit and not fresh and now - hit[0] < hit[2]:
        return hit[1]
    value = fetch()
    failed = not value or value[0] is None
    _github_cache[key] = (now, value, 60 if failed else 600)
    return value

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
        _invalidate_cache(discovery=True)
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
    # État initial fourni avec la page : l'interface s'affiche sans attendre
    # d'aller-retour vers l'API (détection en cache, pas une découverte forcée).
    boot = {"instances": detect_frp(force=False), "in_docker": IN_DOCKER,
            "nicknames": MGR_CFG.get("nicknames", {}),
            "has_password": bool(MGR_CFG.get("password_hash"))}
    return render_template("index.html", panel_version=PANEL_VERSION, boot=boot)

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

# Seules les unités créées par l'admin sont supprimées ; celles d'un paquet
# (/usr/lib, /lib) seraient réinstallées à la prochaine mise à jour.
_UNIT_DIR = "/etc/systemd/system/"

def _forget_instance(iid, kept_config=None):
    """Oublie le surnom d'une instance supprimée ; une config conservée est
    écartée de la détection (voir _build_instances)."""
    global MGR_CFG
    cfg = dict(MGR_CFG)
    nicks = dict(cfg.get("nicknames", {}))
    changed = nicks.pop(iid, None) is not None
    cfg["nicknames"] = nicks
    dismissed = list(cfg.get("dismissed_configs", []))
    if kept_config and kept_config not in dismissed:
        dismissed.append(kept_config)
        changed = True
    cfg["dismissed_configs"] = dismissed
    if changed:
        save_manager_config(cfg)
        MGR_CFG = cfg

_CONFIG_SUFFIXES = (".toml", ".ini", ".yaml", ".yml")

def _docker_mounted_config(inspect, bin_type):
    """
    Chemin HÔTE du fichier de config monté dans un container, ou None.
    Volontairement strict (contrairement à _get_docker_frpc_config) : il sert
    à supprimer le fichier, donc seul un fichier réellement monté compte.
    """
    mounts = [m for m in (inspect.get("Mounts") or [])
              if m.get("Type") == "bind" and m.get("Source") and m.get("Destination")]
    # 1. Le -c/--config du container, traduit via ses montages
    args = list((inspect.get("Config") or {}).get("Cmd") or []) + list(inspect.get("Args") or [])
    for i, arg in enumerate(args):
        target = None
        if arg in ("-c", "--config") and i + 1 < len(args):
            target = args[i + 1]
        elif arg.startswith("--config="):
            target = arg.split("=", 1)[1]
        if not target:
            continue
        for m in sorted(mounts, key=lambda m: len(m["Destination"]), reverse=True):
            dest = m["Destination"].rstrip("/")
            if target == dest:
                return m["Source"]
            if target.startswith(dest + "/"):
                return m["Source"].rstrip("/") + target[len(dest):]
    # 2. Un fichier monté seul, au nom du type (frpc.toml, frps.ini…)
    for m in mounts:
        name = Path(m["Source"]).name.lower()
        if name.startswith(bin_type) and name.endswith(_CONFIG_SUFFIXES):
            return m["Source"]
    return None

def _config_users(path, except_iid):
    """Autres instances frp qui utilisent ce fichier de config."""
    users = []
    for o, other in INSTANCES.items():
        if o == except_iid:
            continue
        if other.get("source") == "docker":
            st, ins = _docker_api("GET", f"/containers/{other['container_name']}/json")
            if st == 200 and isinstance(ins, dict) and _docker_mounted_config(ins, other["type"]) == path:
                users.append(o)
        elif other.get("config") and str(other["config"]) == path:
            users.append(o)
    return users

@app.route("/api/instance/<iid>/delete-info")
@login_required
def api_instance_delete_info(iid):
    """Ce que la suppression d'un container emporterait (affiché dans la confirmation)."""
    detect_frp(force=False)
    inst = INSTANCES.get(iid)
    if not inst:
        return jsonify({"ok": False, "msg": f"Instance inconnue : {iid}"}), 404
    if inst.get("source") != "docker":
        return jsonify({"ok": True, "image": None, "config_path": None})
    st, ins = _docker_api("GET", f"/containers/{inst['container_name']}/json")
    if st != 200 or not isinstance(ins, dict):
        return jsonify({"ok": False, "msg": f"Conteneur introuvable (HTTP {st})"}), 404
    return jsonify({"ok": True,
                    "image": (ins.get("Config") or {}).get("Image") or inst.get("image"),
                    "config_path": _docker_mounted_config(ins, inst["type"])})

def _delete_container(iid, inst, delete_image, delete_config):
    container = inst["container_name"]
    st, ins = _docker_api("GET", f"/containers/{container}/json")
    if st != 200 or not isinstance(ins, dict):
        return False, f"Conteneur introuvable (HTTP {st})", 404
    image_id = ins.get("Image")
    image_name = (ins.get("Config") or {}).get("Image") or image_id
    cfg = _docker_mounted_config(ins, inst["type"]) if delete_config else None
    if delete_config and not cfg:
        return False, "Aucun fichier de configuration monté n'a été trouvé pour ce conteneur.", 400
    if cfg:
        shared = _config_users(cfg, iid)
        if shared:
            return False, f"{cfg} est aussi utilisé par {', '.join(shared)} : rien n'a été supprimé.", 400

    status, data = _docker_api("DELETE", f"/containers/{container}?force=true")
    if status not in (204, 404):
        detail = data.get("message") if isinstance(data, dict) else data
        return False, f"Erreur Docker (HTTP {status}) : {detail or ''}".strip(), 500
    done, notes = [f"conteneur {container}"], []

    if delete_image and image_id:
        status, data = _docker_api("DELETE", f"/images/{image_id}", timeout=60)
        if status == 200:
            done.append(f"image {image_name}")
        elif status == 409:
            notes.append(f"image {image_name} conservée : un autre conteneur l'utilise")
        elif status != 404:
            notes.append(f"image {image_name} non supprimée (HTTP {status})")
    if cfg:
        if host_remove_file(cfg):
            done.append(cfg)
        else:
            notes.append(f"impossible de supprimer {cfg}")
    msg = f"Supprimé : {', '.join(done)}"
    if notes:
        msg += " — " + " ; ".join(notes)
    return True, msg, 200

@app.route("/api/instance/<iid>", methods=["DELETE"])
@login_required
def api_instance_delete(iid):
    detect_frp(force=True)
    if iid not in INSTANCES:
        return jsonify({"ok": False, "msg": f"Instance inconnue : {iid}"}), 404
    inst = INSTANCES[iid]
    opts = request.get_json(silent=True) or {}
    delete_config = bool(opts.get("delete_config"))

    # ── Container Docker : suppression, image et config montée en option ─────
    if inst.get("source") == "docker":
        ok, msg, code = _delete_container(iid, inst, bool(opts.get("delete_image")), delete_config)
        _invalidate_cache(discovery=True)
        if ok:
            _forget_instance(iid)
        return jsonify({"ok": ok, "msg": msg}), code

    # ── Instance systemd ──────────────────────────────────────────────────────
    unit = f"{inst['service']}.service"
    _, fragment, _ = run_cmd(["systemctl", "show", unit, "--property=FragmentPath", "--value"])
    fragment = fragment.strip()
    if fragment and not fragment.startswith(_UNIT_DIR):
        return jsonify({"ok": False,
            "msg": f"{fragment} appartient à un paquet système : supprimez-le avec le gestionnaire de paquets."}), 400

    cfg = Path(inst["config"]) if inst.get("config") else None
    if cfg and delete_config:
        shared = _config_users(str(cfg), iid)
        if shared:
            return jsonify({"ok": False,
                "msg": f"{cfg} est aussi utilisé par {', '.join(shared)} : il n'a pas été supprimé."}), 400
    done = []
    if fragment:
        run_cmd(["systemctl", "disable", "--now", unit], timeout=30)
        if not host_remove_file(fragment):
            _invalidate_cache(discovery=True)
            return jsonify({"ok": False, "msg": f"Service arrêté, mais impossible de supprimer {fragment}"}), 500
        run_host(["rm", "-rf", f"{_UNIT_DIR}{unit}.d"])
        run_cmd(["systemctl", "daemon-reload"])
        run_cmd(["systemctl", "reset-failed", unit])
        done.append(f"service {unit}")
    if delete_config and cfg and cfg.exists():
        try:
            cfg.unlink()
            done.append(str(cfg))
        except Exception as e:
            _invalidate_cache(discovery=True)
            return jsonify({"ok": False, "msg": f"Service supprimé, mais pas {cfg} : {e}"}), 500
    kept = str(cfg) if cfg and cfg.exists() else None
    if kept and not done:
        done.append(f"{iid} retiré du tableau de bord ({kept} conservé)")
    _forget_instance(iid, kept_config=kept)
    _invalidate_cache(discovery=True)
    return jsonify({"ok": True, "msg": f"Supprimé : {', '.join(done)}"})

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
    if ok:
        _invalidate_cache(discovery=True)      # serverAddr, fichier créé…
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
    latest_ver, release_url = _github_cached(("panel", prerelease),
                                             lambda: fetch_panel_latest(include_prereleases=prerelease),
                                             fresh=request.args.get("fresh") == "1")
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
    version, tag, source = _github_cached("frp", fetch_latest_version, fresh=request.args.get("fresh") == "1")
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

# ── Pare-feu frps (nftables) ─────────────────────────────────────────────────
# Filtre, dans le noyau de la machine frps, qui peut se connecter aux ports
# qu'ouvre frps. Tout vit dans une table dédiée, remplacée d'un bloc à chaque
# application (nft -f est atomique : une erreur ne change rien) :
#   - hook prerouting, priorité -150 : avant le DNAT de Docker, donc un frps en
#     container est filtré aussi, et on voit la vraie IP du client ;
#   - la table ne fait que JETER des connexions, elle n'en autorise aucune :
#     ufw/Docker restent seuls juges de ce qui est ouvert ;
#   - seules les nouvelles connexions vers une adresse de la machine sont
#     examinées ; la boucle locale ne l'est jamais, le port du panel non plus.
# Une connexion doit passer chaque règle active qui concerne son port.
# Si le panel s'arrête, la table reste en place ; au démarrage il la réapplique.
import ipaddress
try:
    import tomllib as _tomllib
except ImportError:          # Python < 3.11 (installation classique ancienne)
    _tomllib = None

FW_TABLE        = "frp_manager"
FW_SYNC_SECONDS = 60
FW_MODES        = ("allow", "block")
FW_SEEN_TIMEOUT = 86400      # une adresse bloquée reste listée 24 h après sa dernière tentative
FW_SEEN_SIZE    = 4096       # adresses retenues au plus, par règle et par famille
FW_LIVE_SECONDS = 1          # connexions bloquées : relecture au plus une fois par seconde
_fw_lock        = threading.Lock()
_fw_applied     = None       # dernier jeu de règles chargé avec succès
_fw_seen_ok     = True       # False si ce nftables refuse les ensembles dynamiques

# ── Systèmes autonomes (AS) : préfixes annoncés, via RIPEstat ────────────────
# Une source « AS16276 » vaut tous les préfixes que cet AS annonce. Ils sont
# gardés sur disque et rafraîchis chaque jour ; si RIPEstat est injoignable,
# les derniers préfixes connus restent utilisés.
FW_ASN_FILE    = Path("/var/lib/frp-manager/asn-cache.json")
FW_ASN_MAX_AGE = 86400
RIPESTAT       = "https://stat.ripe.net/data"
_asn_lock      = threading.Lock()
_asn_cache     = None        # {"16276": {"holder", "v4": [...], "v6": [...], "fetched"}}
_ip_asn_cache  = {}          # "1.2.3.4" → {"asn", "holder"}, ou None si l'adresse n'a pas d'AS
_ip_asn_busy   = threading.Lock()

# ── Listes de blocage communautaires ─────────────────────────────────────────
# Catalogue publié sur la branche « blocklists » du dépôt GitHub public, pas sur
# main : le main public n'avance qu'en avance rapide, à la promotion d'une
# release. On y propose une liste par une issue pré-remplie depuis le panel ;
# une fois acceptée, les panels abonnés la reçoivent. Une règle abonnée
# (champ « list ») filtre ses propres adresses plus celles de la liste. Le
# catalogue est gardé sur disque : sans GitHub, la dernière version sert.
FW_LISTS_BRANCH     = "blocklists"
FW_LISTS_URL        = (os.environ.get("FRP_MANAGER_BLOCKLISTS_URL") or
                       f"https://raw.githubusercontent.com/{PANEL_GITHUB_REPO}/{FW_LISTS_BRANCH}/blocklists.json")
FW_LISTS_FILE       = Path("/var/lib/frp-manager/blocklists-cache.json")
FW_LISTS_MAX_AGE    = 6 * 3600
FW_LISTS_RETRY      = 60         # après un échec, pas de nouvel essai avant 1 min
FW_LIST_MAX_SOURCES = 20000
FW_LIST_MAX_ASNS    = 50
FW_LIST_ID          = re.compile(r"[a-z0-9][a-z0-9-]{0,39}")
_lists_lock         = threading.Lock()
_lists_cache        = None       # {"fetched", "lists": {id: {"id", "name", …, "sources"}}}
_lists_failure      = (0, "")    # (moment, message) du dernier échec

def _fw_source(text, note=""):
    """« 203.0.113.4 », « 198.51.100.0/24 » ou « AS16276 » → source de règle. ValueError sinon."""
    text = str(text).strip()
    m = re.fullmatch(r"(?i)AS\s*(\d{1,10})", text)
    if m:
        asn = int(m.group(1))
        if not (1 <= asn <= 4294967295):
            raise ValueError(f"« {text} » n'est pas un numéro d'AS valide")
        return {"asn": asn, "note": note}
    try:
        net = ipaddress.ip_network(text, strict=False)
    except ValueError:
        raise ValueError(f"« {text} » n'est ni une adresse IP, ni un réseau "
                         f"(203.0.113.0/24), ni un AS (AS16276)")
    cidr = str(net.network_address) if net.prefixlen == net.max_prefixlen else str(net)
    return {"cidr": cidr, "note": note}

def _fw_list_sources(items, skipped=None):
    """Entrées d'une liste communautaire (« AS16276  # note », ou {cidr|asn, note})
    → sources de règle. Sont écartés, et notés dans skipped : ce qui est illisible,
    les réseaux privés ou réservés (on se couperait de son réseau local), ceux plus
    larges qu'un /8 (/16 en IPv6), et les AS au-delà de 50."""
    sources, seen, asns = [], set(), 0
    skip = (lambda text, why: skipped.append({"entry": text, "reason": why})) if skipped is not None else (lambda *a: None)
    for item in items or []:
        if len(sources) >= FW_LIST_MAX_SOURCES:
            skip("…", f"liste limitée à {FW_LIST_MAX_SOURCES} entrées")
            break
        if isinstance(item, dict):
            text = f"AS{item['asn']}" if item.get("asn") else str(item.get("cidr") or "")
            note = str(item.get("note") or "")
        else:
            text, _, note = str(item).partition("#")
        text = text.strip()
        if not text:
            continue
        try:
            src = _fw_source(text, note.strip()[:60])
        except ValueError as e:
            skip(text, str(e))
            continue
        if src.get("asn"):
            if asns >= FW_LIST_MAX_ASNS:
                skip(text, f"{FW_LIST_MAX_ASNS} AS au plus par liste")
                continue
            asns += 1
        else:
            net = ipaddress.ip_network(src["cidr"])
            if not net.is_global:
                skip(text, "adresse privée ou réservée")
                continue
            if net.prefixlen < (8 if net.version == 4 else 16):
                skip(text, "réseau trop large")
                continue
        key = src.get("asn") or src["cidr"]
        if key in seen:
            continue
        seen.add(key)
        sources.append(src)
    return sources

def _fw_list_normalize(raw):
    """Une liste du catalogue → liste propre, ou None si elle est inutilisable."""
    if not isinstance(raw, dict) or not FW_LIST_ID.fullmatch(str(raw.get("id") or "")):
        return None
    sources = _fw_list_sources(raw.get("sources"))
    if not sources:
        return None
    return {"id": raw["id"],
            "name": str(raw.get("name") or raw["id"]).strip()[:60],
            "description": str(raw.get("description") or "").strip()[:300],
            "author": str(raw.get("author") or "").strip()[:40],
            "updated": str(raw.get("updated") or "").strip()[:10],
            "sources": sources}

def _lists_all():
    global _lists_cache
    if _lists_cache is None:
        try:
            _lists_cache = json.loads(FW_LISTS_FILE.read_text())
        except Exception:
            _lists_cache = {"fetched": 0, "lists": {}}
    return _lists_cache

def _lists_fetch():
    """Télécharge le catalogue. Exception si GitHub échoue ou si le fichier est illisible."""
    global _lists_cache
    r = req.get(FW_LISTS_URL, timeout=15)
    r.raise_for_status()
    lists = {}
    for raw in r.json().get("lists") or []:
        lst = _fw_list_normalize(raw)
        if lst and lst["id"] not in lists:
            lists[lst["id"]] = lst
    cache = {"fetched": int(time.time()), "lists": lists}
    with _lists_lock:
        _lists_cache = cache
        try:
            FW_LISTS_FILE.parent.mkdir(parents=True, exist_ok=True)
            FW_LISTS_FILE.write_text(json.dumps(cache))
        except Exception as e:
            print(f"[WARN] Catalogue des listes non enregistré : {e}")
    return cache

def _lists_get(max_age=FW_LISTS_MAX_AGE):
    """Catalogue de moins de max_age secondes, retéléchargé au besoin.
    → (catalogue, erreur) : en cas d'échec, le dernier connu et le message."""
    global _lists_failure
    cache = _lists_all()
    if time.time() - cache.get("fetched", 0) < max_age:
        return cache, None
    if time.time() - _lists_failure[0] < FW_LISTS_RETRY:
        return cache, _lists_failure[1]
    try:
        return _lists_fetch(), None
    except Exception as e:
        _lists_failure = (time.time(), str(e) or type(e).__name__)
        return cache, _lists_failure[1]

def _fw_rule_list(rule):
    """Liste communautaire d'une règle (dernière version connue), ou None."""
    return _lists_all()["lists"].get(rule["list"]) if rule.get("list") else None

def _fw_rule_sources(rule):
    """Sources d'une règle : les siennes, plus celles de sa liste communautaire."""
    lst = _fw_rule_list(rule)
    return list(rule.get("sources") or []) + (lst["sources"] if lst else [])

def _fw_ensure_lists(rules):
    """Catalogue à jour pour les listes des règles. → message d'erreur, ou None."""
    wanted = {r["list"] for r in rules if r.get("list")}
    if not wanted:
        return None
    cache, err = _lists_get()
    if wanted - set(cache["lists"]) and not err:
        cache, err = _lists_get(0)          # ajoutée depuis le dernier téléchargement ?
    missing = sorted(wanted - set(cache["lists"]))
    if not missing:
        return None
    return (f"Liste communautaire introuvable dans le catalogue : {', '.join(missing)}"
            + (f" (catalogue injoignable : {err})" if err else " — retirée ? supprimez la règle qui s'y abonne"))

def _lists_info(rules):
    """{id: résumé} des listes auxquelles des règles s'abonnent (None si inconnue)."""
    info = {}
    for rule in rules:
        if rule.get("list"):
            lst = _fw_rule_list(rule)
            info[rule["list"]] = ({"name": lst["name"], "author": lst["author"], "updated": lst["updated"],
                                   "count": len(lst["sources"])} if lst else None)
    return info

def _asn_all():
    global _asn_cache
    if _asn_cache is None:
        try:
            _asn_cache = json.loads(FW_ASN_FILE.read_text())
        except Exception:
            _asn_cache = {}
    return _asn_cache

def _asn_store(asn, entry):
    with _asn_lock:
        cache = _asn_all()
        cache[str(asn)] = entry
        try:
            FW_ASN_FILE.parent.mkdir(parents=True, exist_ok=True)
            FW_ASN_FILE.write_text(json.dumps(cache))
        except Exception as e:
            print(f"[WARN] Cache des AS non enregistré : {e}")

def _ripestat(endpoint, resource, timeout=20):
    r = req.get(f"{RIPESTAT}/{endpoint}/data.json",
                params={"resource": resource, "sourceapp": "frp-manager"}, timeout=timeout)
    r.raise_for_status()
    return r.json().get("data") or {}

def _asn_holder(asn):
    try:
        holder = (_ripestat("as-overview", f"AS{asn}", timeout=8).get("holder") or "").strip()
        return re.sub(r"^AS\d+\s+", "", holder)       # « AS3215 Orange S.A. » → « Orange S.A. »
    except Exception:
        return ""

def _asn_fetch(asn):
    """Préfixes annoncés par un AS (regroupés), et son nom. Exception si RIPEstat échoue."""
    data = _ripestat("announced-prefixes", f"AS{asn}")
    nets = []
    for p in data.get("prefixes") or []:
        try:
            nets.append(ipaddress.ip_network(p["prefix"], strict=False))
        except (KeyError, ValueError):
            continue
    v4 = [str(n) for n in ipaddress.collapse_addresses(n for n in nets if n.version == 4)]
    v6 = [str(n) for n in ipaddress.collapse_addresses(n for n in nets if n.version == 6)]
    entry = {"holder": _asn_holder(asn), "v4": v4, "v6": v6, "fetched": int(time.time())}
    _asn_store(asn, entry)
    return entry

def _asn_get(asn):
    """Préfixes d'un AS : depuis le cache, sinon chez RIPEstat."""
    return _asn_all().get(str(asn)) or _asn_fetch(asn)

def _fw_rule_asns(rules):
    return sorted({s["asn"] for r in rules for s in _fw_rule_sources(r) if s.get("asn")})

def _asn_info(asns):
    info = {}
    for asn in asns:
        e = _asn_all().get(str(asn))
        info[str(asn)] = ({"holder": e.get("holder", ""), "prefixes": len(e["v4"]) + len(e["v6"]),
                           "fetched": e.get("fetched")} if e else None)
    return info

def _ip_asn_lookup(ips):
    """AS des adresses déjà connues. Les inconnues sont cherchées en arrière-plan
    (réponse au prochain rafraîchissement) : la page n'attend jamais RIPEstat."""
    todo = [ip for ip in ips if ip not in _ip_asn_cache]
    if todo and _ip_asn_busy.acquire(blocking=False):
        def work(batch):
            try:
                for ip in batch:
                    try:
                        d = _ripestat("network-info", ip, timeout=6)
                        asn = int(d["asns"][0]) if d.get("asns") else None
                        holder = ""
                        if asn:
                            known = _asn_all().get(str(asn))
                            holder = known.get("holder", "") if known else _asn_holder(asn)
                        _ip_asn_cache[ip] = {"asn": asn, "holder": holder} if asn else None
                    except Exception:
                        pass              # réessayé au prochain rafraîchissement
            finally:
                _ip_asn_busy.release()
        threading.Thread(target=work, args=(todo[:25],), daemon=True).start()
    return {ip: _ip_asn_cache[ip] for ip in ips if ip in _ip_asn_cache}

def _fw_cfg():
    fw = MGR_CFG.get("firewall") or {}
    return {"enabled": bool(fw.get("enabled")), "rules": list(fw.get("rules") or [])}

def _frps_instances():
    return {iid: inst for iid, inst in INSTANCES.items() if inst.get("type") == "frps"}

def _fw_panel_port():
    try:
        return int(MGR_CFG.get("bind_port") or 8765)
    except (TypeError, ValueError):
        return 8765

def _frps_config_text(inst):
    if inst.get("source") == "docker":
        st, ins = _docker_api("GET", f"/containers/{inst['container_name']}/json")
        path = _docker_mounted_config(ins, "frps") if st == 200 and isinstance(ins, dict) else None
        return host_read_file(path) if path else None
    cfg = inst.get("config")
    try:
        return Path(cfg).read_text() if cfg and Path(cfg).exists() else None
    except Exception:
        return None

def _frps_active_proxies(conf):
    """Proxys connectés, via l'API du tableau de bord frps (si webServer est configuré)."""
    ws = conf.get("webServer") or {}
    port = ws.get("port")
    if not port:
        return []
    addr = ws.get("addr") or "127.0.0.1"
    if addr in ("0.0.0.0", "::", ""):
        addr = "127.0.0.1"
    auth = (ws["user"], ws.get("password", "")) if ws.get("user") else None
    found = []
    for ptype in ("tcp", "udp"):
        try:
            r = req.get(f"http://{addr}:{port}/api/proxy/{ptype}", auth=auth, timeout=2)
            proxies = r.json().get("proxies") or [] if r.ok else []
        except Exception:
            continue
        for p in proxies:
            rp = (p.get("conf") or {}).get("remotePort") or p.get("remotePort")
            if rp:
                found.append({"port": int(rp), "proto": ptype, "name": p.get("name", ""),
                              "online": p.get("status") == "online"})
    return found

FW_KNOWN_TTL = 15
_fw_known = {"at": 0, "ports": []}

def _fw_known_ports():
    """Ports ouverts par le(s) frps de la machine, gardés 15 s : la liste sert à
    chaque affichage et à chaque application, et l'API du tableau de bord frps
    peut mettre jusqu'à 2 s à répondre."""
    if time.time() - _fw_known["at"] >= FW_KNOWN_TTL:
        _fw_known.update({"ports": _fw_known_ports_read(), "at": time.time()})
    return _fw_known["ports"]

def _fw_known_ports_read():
    """[{start, end, proto, label, kind, online}]."""
    known, seen = [], set()
    def add(start, end, proto, label, kind, online=None):
        key = (start, end, proto)
        if key in seen or not (1 <= start <= end <= 65535):
            return
        seen.add(key)
        known.append({"start": start, "end": end, "proto": proto, "label": label,
                      "kind": kind, "online": online})
    for inst in _frps_instances().values():
        text = _frps_config_text(inst)
        if text is None:
            continue
        conf = None
        if _tomllib:
            try:
                conf = _tomllib.loads(text)
            except Exception:
                conf = None
        if conf is None:
            # Ancienne installation sans tomllib, ou config illisible : lecture simple
            for p in _extract_ports_from_config(text, "frps"):
                add(p["port"], p["port"], p["proto"], p["label"], "config")
            continue
        add(int(conf.get("bindPort") or 7000), int(conf.get("bindPort") or 7000), "tcp", "Connexion des clients frpc", "config")
        for key, proto, label in (("kcpBindPort", "udp", "KCP"), ("quicBindPort", "udp", "QUIC"),
                                  ("vhostHTTPPort", "tcp", "Sites HTTP (vhost)"),
                                  ("vhostHTTPSPort", "tcp", "Sites HTTPS (vhost)"),
                                  ("tcpmuxHTTPConnectPort", "tcp", "TCP mux")):
            if conf.get(key):
                add(int(conf[key]), int(conf[key]), proto, label, "config")
        ws = conf.get("webServer") or {}
        if ws.get("port") and ws.get("addr", "127.0.0.1") not in ("127.0.0.1", "localhost", "::1"):
            add(int(ws["port"]), int(ws["port"]), "tcp", "Tableau de bord frps", "config")
        for rng in conf.get("allowPorts") or []:
            if rng.get("single"):
                add(int(rng["single"]), int(rng["single"]), "any", "Port réservé aux clients", "range")
            elif rng.get("start") and rng.get("end"):
                add(int(rng["start"]), int(rng["end"]), "any", "Plage réservée aux clients", "range")
        for p in _frps_active_proxies(conf):
            add(p["port"], p["port"], p["proto"], f"Port « {p['name']} »", "proxy", p["online"])
    known.sort(key=lambda k: (k["start"], k["end"]))
    return known

def _fw_parse_ports(spec):
    """'7000, 25565, 30000-30010' → [(7000, 7000), …]. ValueError si invalide."""
    ranges = []
    for part in re.split(r"[,\s]+", str(spec).strip()):
        if not part:
            continue
        m = re.fullmatch(r"(\d{1,5})(?:-(\d{1,5}))?", part)
        if not m:
            raise ValueError(f"« {part} » n'est pas un port ni une plage (ex. 30000-30010)")
        a, b = int(m.group(1)), int(m.group(2) or m.group(1))
        if not (1 <= a <= b <= 65535):
            raise ValueError(f"« {part} » : les ports vont de 1 à 65535, dans l'ordre")
        ranges.append((a, b))
    if not ranges:
        raise ValueError("Indiquez au moins un port")
    return ranges

def _fw_rule_ranges(rule, known):
    """Plages de ports d'une règle, sans le port du panel (jamais filtré)."""
    ranges = ([(k["start"], k["end"]) for k in known] if rule["ports"] == "*"
              else _fw_parse_ports(rule["ports"]))
    panel, out = _fw_panel_port(), []
    for a, b in ranges:
        if a <= panel <= b:
            if a < panel:
                out.append((a, panel - 1))
            if panel < b:
                out.append((panel + 1, b))
        else:
            out.append((a, b))
    return out

def _fw_normalize_rule(raw, index):
    """Valide une règle venue de l'interface. → règle propre, ou ValueError."""
    where = f"Règle {index + 1}"
    name = str(raw.get("name") or "").strip()[:60]
    if name:
        where = f"Règle « {name} »"
    mode = raw.get("mode")
    if mode not in FW_MODES:
        raise ValueError(f"{where} : action inconnue")
    ports = str(raw.get("ports") or "").strip()
    if ports != "*":
        try:
            ranges = _fw_parse_ports(ports)
        except ValueError as e:
            raise ValueError(f"{where} : {e}")
        panel = _fw_panel_port()
        if ranges == [(panel, panel)]:
            raise ValueError(f"{where} : le port du panel ({panel}) n'est jamais filtré ici")
        ports = ", ".join(str(a) if a == b else f"{a}-{b}" for a, b in ranges)
    sources = []
    for src in raw.get("sources") or []:
        note = str(src.get("note") or "").strip()[:60]
        if src.get("asn"):
            text = f"AS{src['asn']}"
        else:
            text = str(src.get("cidr") or "").strip()
        if not text:
            continue
        try:
            sources.append(_fw_source(text, note))
        except ValueError as e:
            raise ValueError(f"{where} : {e}")
    lst = str(raw.get("list") or "").strip()
    if lst and not FW_LIST_ID.fullmatch(lst):
        raise ValueError(f"{where} : liste communautaire « {lst} » invalide")
    if mode == "block" and not sources and not lst:
        raise ValueError(f"{where} : indiquez au moins une adresse à bloquer")
    return {"id": re.sub(r"[^0-9a-f]", "", str(raw.get("id") or ""))[:12] or secrets.token_hex(4),
            "name": name, "mode": mode, "ports": ports, "sources": sources,
            **({"list": lst} if lst else {}),
            "enabled": raw.get("enabled", True) is not False}

_fw_nets_cache = {}

def _fw_rule_nets(rule):
    """Réseaux d'une règle : adresses et réseaux saisis, ceux de sa liste
    communautaire, plus les préfixes des AS. Mis en cache tant que ni les
    sources, ni la liste, ni les préfixes des AS ne changent."""
    key = json.dumps([[[s.get("asn"), s.get("cidr")] for s in rule["sources"]],
                      [rule.get("list"), _lists_all().get("fetched") if rule.get("list") else None],
                      [(_asn_all().get(str(a)) or {}).get("fetched") for a in _fw_rule_asns([rule])]])
    cached = _fw_nets_cache.get(key)
    if cached is None:
        cached = _fw_nets_cache[key] = _fw_rule_nets_build(rule)
        if len(_fw_nets_cache) > 64:
            _fw_nets_cache.pop(next(iter(_fw_nets_cache)))
    return cached

def _fw_rule_nets_build(rule):
    nets = []
    for s in _fw_rule_sources(rule):
        if s.get("asn"):
            e = _asn_all().get(str(s["asn"]))
            if e:
                nets += [ipaddress.ip_network(x) for x in e["v4"] + e["v6"]]
        else:
            nets.append(ipaddress.ip_network(s["cidr"], strict=False))
    return nets

def _fw_ruleset(cfg, known, seen=True):
    """Texte nft qui remplace la table (ou la supprime si rien n'est à filtrer)."""
    head = f"table inet {FW_TABLE}\ndelete table inet {FW_TABLE}\n"
    rules = [r for r in cfg["rules"] if r.get("enabled", True)] if cfg["enabled"] else []
    sets, chain = [], []
    for n, rule in enumerate(rules):
        try:
            ranges = _fw_rule_ranges(rule, known)
        except ValueError:
            continue
        if not ranges:
            continue
        rid = f"r{n}"
        nets = _fw_rule_nets(rule)
        ports = ", ".join(str(a) if a == b else f"{a}-{b}" for a, b in ranges)
        sets.append(f"  set {rid}_ports {{ type inet_service; flags interval; auto-merge; elements = {{ {ports} }} }}")
        families = []
        for fam, sel, version, stype in (("ipv4", "ip", 4, "ipv4_addr"), ("ipv6", "ip6", 6, "ipv6_addr")):
            elems = ", ".join(str(x) for x in ipaddress.collapse_addresses(x for x in nets if x.version == version))
            body = f"type {stype}; flags interval; auto-merge;" + (f" elements = {{ {elems} }}" if elems else "")
            sets.append(f"  set {rid}_v{version} {{ {body} }}")
            if rule["mode"] == "allow":
                families.append((version, sel, f"meta nfproto {fam} th dport @{rid}_ports {sel} saddr != @{rid}_v{version}"))
            elif elems:
                families.append((version, sel, f"meta nfproto {fam} th dport @{rid}_ports {sel} saddr @{rid}_v{version}"))
        for version, sel, match in families:
            if seen:
                # Retient l'adresse bloquée (protocole, port, dernière tentative) : c'est la
                # liste « Connexions bloquées », sans dépendre du journal du noyau. Règle à
                # part : un ensemble plein ne doit jamais empêcher le blocage.
                seen_set = f"seen{version}_{rule['id']}"
                addr = "ipv4_addr" if version == 4 else "ipv6_addr"
                sets.append(f"  set {seen_set} {{ type {addr} . inet_proto . inet_service; flags dynamic, timeout; "
                            f"timeout {FW_SEEN_TIMEOUT}s; size {FW_SEEN_SIZE}; }}")
                chain.append(f"    {match} update @{seen_set} {{ {sel} saddr . meta l4proto . th dport }}")
            chain.append(f'    {match} counter drop comment "{rule["id"]}"')
    if not chain:
        return head
    return head + "\n".join([
        f"table inet {FW_TABLE} {{",
        *sets,
        "  chain filter {",
        "    type filter hook prerouting priority -150; policy accept;",
        '    iifname "lo" return',
        "    ct state != new return",
        "    fib daddr type != local return",
        "    meta l4proto != { tcp, udp } return",
        *chain,
        "  }",
        "}",
    ]) + "\n"

def _fw_apply(cfg=None):
    """Charge la table nft correspondant à la config. → (ok, message d'erreur)."""
    global _fw_applied, _fw_seen_ok
    with _fw_lock:
        cfg = cfg or _fw_cfg()
        known = _fw_known_ports()
        text = _fw_ruleset(cfg, known, seen=_fw_seen_ok)
        ok, out, err = run_host(["nft", "-f", "-"], input_text=text)
        if not ok and _fw_seen_ok and "flags dynamic" in text:
            # nftables trop ancien pour les ensembles dynamiques : on filtre quand
            # même, sans la liste des connexions bloquées.
            text = _fw_ruleset(cfg, known, seen=False)
            ok, out, err = run_host(["nft", "-f", "-"], input_text=text)
            if ok:
                _fw_seen_ok = False
                print("[WARN] Pare-feu : ensembles dynamiques refusés par nftables, liste des blocages désactivée")
        if ok:
            _fw_applied = text
            _fw_live_reset()
        return ok, (err or out).strip()

def _fw_table_present():
    ok, out, _ = run_host(["nft", "list", "tables", "inet"])
    return ok and f"table inet {FW_TABLE}" in out

_L4_NAMES = {6: "tcp", 17: "udp"}

def _fw_parse_state(items, now):
    """JSON de « nft -j list table » → (compteurs {id: paquets jetés}, adresses bloquées)."""
    counts, seen = {}, []
    for item in items:
        rule = item.get("rule")
        if rule and rule.get("comment"):
            for expr in rule.get("expr", []):
                if isinstance(expr, dict) and "counter" in expr:
                    counts[rule["comment"]] = counts.get(rule["comment"], 0) + int(expr["counter"].get("packets", 0))
        st = item.get("set")
        if not st or not re.fullmatch(r"seen[46]_[0-9a-f]+", st.get("name", "")):
            continue
        rid = st["name"].split("_", 1)[1]
        for el in st.get("elem") or []:
            # Avec délai d'expiration : {"elem": {"val": {"concat": [...]}, "timeout", "expires"}}
            meta = el.get("elem", el) if isinstance(el, dict) else {}
            val = meta.get("val", el) if isinstance(meta, dict) else el
            parts = val.get("concat") if isinstance(val, dict) else None
            if not parts or len(parts) != 3:
                continue
            src, proto, port = parts
            proto = _L4_NAMES.get(proto, str(proto)) if isinstance(proto, int) else str(proto)
            timeout = meta.get("timeout", FW_SEEN_TIMEOUT)
            expires = meta.get("expires", timeout)
            seen.append({"src": str(src), "proto": proto, "port": int(port), "rule": rid,
                         "last": int(now - max(0, timeout - expires))})
    seen.sort(key=lambda e: e["last"], reverse=True)
    return counts, seen

def _nft_json(*args):
    ok, out, _ = run_host(["nft", "-j", "list", *args])
    if not ok:
        return None
    try:
        return json.loads(out).get("nftables", [])
    except Exception:
        return None

def _fw_counters():
    """{id de règle: paquets jetés} : lecture de la seule chaîne, sans les ensembles
    (ceux des AS peuvent compter des milliers de préfixes)."""
    items = _nft_json("chain", "inet", FW_TABLE, "filter")
    return _fw_parse_state(items, time.time())[0] if items else {}

# Lecture « en direct », partagée par tous les onglets ouverts. Les adresses
# retenues ne changent que si des paquets sont jetés : on ne relit les ensembles
# d'une règle que quand son compteur a bougé, et tous une fois par minute (pour
# les expirations au bout de 24 h).
FW_SEEN_FULL_EVERY = 60
_fw_live = {"at": 0, "counts": None, "seen": {}, "full_at": 0, "result": ({}, [])}
_fw_live_lock = threading.Lock()

def _fw_table_state():
    """→ (compteurs {id: paquets jetés}, adresses bloquées retenues), au plus une
    lecture par seconde quel que soit le nombre d'appelants."""
    with _fw_live_lock:
        now = time.time()
        live = _fw_live
        if now - live["at"] < FW_LIVE_SECONDS:
            return live["result"]
        live["at"] = now
        counts = _fw_counters()
        full = live["counts"] is None or now - live["full_at"] >= FW_SEEN_FULL_EVERY
        changed = set(counts) if full else {rid for rid, n in counts.items() if live["counts"].get(rid) != n}
        for key in [k for k in live["seen"] if k[0] not in counts]:
            del live["seen"][key]          # règle supprimée ou table recréée
        if _fw_seen_ok:
            for rid in changed:
                for version in (4, 6):
                    items = _nft_json("set", "inet", FW_TABLE, f"seen{version}_{rid}")
                    live["seen"][(rid, version)] = _fw_parse_state(items, now)[1] if items else []
        if full:
            live["full_at"] = now
        live["counts"] = counts
        seen = sorted((e for lst in live["seen"].values() for e in lst), key=lambda e: e["last"], reverse=True)
        live["result"] = (counts, seen)
        return live["result"]

def _fw_live_reset():
    """Table remplacée (nouvelles règles) : tout relire au prochain appel."""
    with _fw_live_lock:
        _fw_live.update({"at": 0, "counts": None, "seen": {}})

def _fw_verdicts(ip_text, cfg, known):
    """Pour une adresse : chaque port frp connu, et les règles qui la bloqueraient."""
    ip = ipaddress.ip_address(ip_text)
    rules = []
    for rule in cfg["rules"]:
        if not rule.get("enabled", True):
            continue
        try:
            ranges = _fw_rule_ranges(rule, known)
        except ValueError:
            continue
        listed = any(ip in net for net in _fw_rule_nets(rule) if net.version == ip.version)
        if (rule["mode"] == "allow") != listed:           # cette règle bloquerait l'adresse
            rules.append((ranges, rule.get("name") or rule["id"]))
    return [{**k, "blocked_by": [name for ranges, name in rules
                                 if any(a <= k["end"] and k["start"] <= b for a, b in ranges)]}
            for k in known]

def _fw_sync_loop():
    """Garde la table à jour : ports des proxys qui changent, table effacée
    (rechargement de nftables, redémarrage), démarrage du panel."""
    time.sleep(5)
    while True:
        try:
            cfg = _fw_cfg()
            if cfg["enabled"]:
                if any(r.get("list") and r.get("enabled", True) for r in cfg["rules"]):
                    _, err = _lists_get()
                    if err:
                        print(f"[WARN] Pare-feu : catalogue des listes communautaires non rafraîchi ({err})")
                for asn in _fw_rule_asns(cfg["rules"]):
                    entry = _asn_all().get(str(asn))
                    if not entry or time.time() - entry.get("fetched", 0) > FW_ASN_MAX_AGE:
                        try:
                            _asn_fetch(asn)
                        except Exception as e:
                            print(f"[WARN] Pare-feu : préfixes de AS{asn} non rafraîchis ({e})")
                detect_frp(force=False)
                text = _fw_ruleset(cfg, _fw_known_ports())
                expect_table = "chain filter" in text
                if text != _fw_applied or (expect_table and not _fw_table_present()):
                    ok, msg = _fw_apply(cfg)
                    if not ok:
                        print(f"[WARN] Pare-feu : application impossible ({msg})")
        except Exception as e:
            print(f"[WARN] Pare-feu : {e}")
        time.sleep(FW_SYNC_SECONDS)

def _client_ip():
    try:
        ip = ipaddress.ip_address(request.remote_addr or "")
    except ValueError:
        return None
    return None if ip.is_loopback else str(ip)

@app.route("/api/firewall", methods=["GET"])
@login_required
def api_firewall_get():
    detect_frp(force=False)
    has_frps = bool(_frps_instances())
    cfg = _fw_cfg()
    ok, out, err = run_host(["nft", "--version"]) if has_frps else (False, "", "")
    known = _fw_known_ports() if has_frps and ok else []
    client = _client_ip()
    return jsonify({
        "ok": True, "has_frps": has_frps,
        "available": ok, "nft": out.strip() if ok else (err or "nft introuvable").strip(),
        "enabled": cfg["enabled"], "rules": cfg["rules"], "ports": known,
        "active": ok and _fw_table_present(),
        "counters": _fw_counters() if ok else {},
        "asns": _asn_info(_fw_rule_asns(cfg["rules"])),
        "lists": _lists_info(cfg["rules"]),
        "client_ip": client,
        "client_verdicts": _fw_verdicts(client, cfg, known) if client and known and cfg["enabled"] else [],
        "panel_port": _fw_panel_port(), "in_docker": IN_DOCKER,
    })

@app.route("/api/firewall", methods=["POST"])
@login_required
def api_firewall_save():
    global MGR_CFG
    detect_frp(force=False)
    if not _frps_instances():
        return jsonify({"ok": False, "msg": "Le pare-feu ne filtre que les ports d'un frps : aucun frps sur cette machine."}), 400
    data = request.get_json() or {}
    try:
        rules = [_fw_normalize_rule(r, i) for i, r in enumerate(data.get("rules") or [])]
    except ValueError as e:
        return jsonify({"ok": False, "msg": str(e)}), 400
    err = _fw_ensure_lists(rules)
    if err:
        return jsonify({"ok": False, "msg": f"{err}. Rien n'a changé."}), 502
    for asn in _fw_rule_asns(rules):
        try:
            _asn_get(asn)
        except Exception as e:
            return jsonify({"ok": False, "msg": f"Impossible de récupérer les préfixes de AS{asn} "
                                                f"auprès de RIPEstat, rien n'a changé : {e}"}), 502
    new = {"enabled": bool(data.get("enabled")), "rules": rules}
    ok, msg = _fw_apply(new)
    if not ok:
        return jsonify({"ok": False, "msg": f"nftables a refusé les règles, rien n'a changé : {msg or 'erreur inconnue'}"}), 500
    MGR_CFG = {**MGR_CFG, "firewall": new}
    save_manager_config(MGR_CFG)
    return jsonify({"ok": True, "rules": rules,
                    "msg": "Pare-feu appliqué" if new["enabled"] else "Pare-feu désactivé : plus aucun filtrage"})

@app.route("/api/firewall/test", methods=["POST"])
@login_required
def api_firewall_test():
    """Ce que les règles (brouillon compris) feraient d'une adresse."""
    detect_frp(force=False)
    data = request.get_json() or {}
    try:
        ipaddress.ip_address(str(data.get("ip", "")).strip())
        rules = [_fw_normalize_rule(r, i) for i, r in enumerate(data.get("rules") or [])]
    except ValueError as e:
        return jsonify({"ok": False, "msg": str(e) if "Règle" in str(e) else "Adresse IP invalide"}), 400
    err = _fw_ensure_lists(rules)
    if err:
        return jsonify({"ok": False, "msg": err}), 502
    for asn in _fw_rule_asns(rules):
        try:
            _asn_get(asn)
        except Exception as e:
            return jsonify({"ok": False, "msg": f"Impossible de récupérer les préfixes de AS{asn} : {e}"}), 502
    verdicts = _fw_verdicts(str(data["ip"]).strip(), {"enabled": True, "rules": rules}, _fw_known_ports())
    return jsonify({"ok": True, "verdicts": verdicts})

@app.route("/api/firewall/lists")
@login_required
def api_firewall_lists():
    """Catalogue des listes communautaires (retéléchargé s'il a plus de 10 min)."""
    cache, err = _lists_get(0 if request.args.get("fresh") == "1" else 600)
    return jsonify({"ok": True, "lists": sorted(cache["lists"].values(), key=lambda l: l["name"].lower()),
                    "fetched": cache.get("fetched") or None, "error": err,
                    "browse_url": f"https://github.com/{PANEL_GITHUB_REPO}/tree/{FW_LISTS_BRANCH}"})

@app.route("/api/firewall/lists/publish", methods=["POST"])
@login_required
def api_firewall_lists_publish():
    """Prépare la proposition d'une liste au catalogue : l'entrée telle que les
    panels l'accepteront, et le lien d'une issue GitHub pré-remplie. Rien n'est
    envoyé d'ici : l'utilisateur relit et soumet l'issue lui-même."""
    import unicodedata
    from urllib.parse import urlencode
    data = request.get_json() or {}
    name = str(data.get("name") or "").strip()[:60]
    if not name:
        return jsonify({"ok": False, "msg": "Donnez un nom à la liste"}), 400
    lid = unicodedata.normalize("NFKD", name).encode("ascii", "ignore").decode().lower()
    lid = re.sub(r"[^a-z0-9]+", "-", lid).strip("-")[:40].strip("-") or f"liste-{secrets.token_hex(3)}"
    skipped = []
    items = [src if data.get("notes", True) else {**src, "note": ""} for src in data.get("sources") or []]
    sources = _fw_list_sources(items, skipped)
    if not sources:
        return jsonify({"ok": False, "msg": "Aucune adresse publiable : il faut des adresses publiques, "
                                            "des réseaux d'au plus /8 (/16 en IPv6) ou des AS",
                        "skipped": skipped}), 400
    entry = {"id": lid, "name": name,
             "description": str(data.get("description") or "").strip()[:300],
             "author": str(data.get("author") or "").strip()[:40],
             "updated": datetime.now().strftime("%Y-%m-%d"),
             "sources": [(f"AS{s['asn']}" if s.get("asn") else s["cidr"]) + (f"  # {s['note']}" if s["note"] else "")
                         for s in sources]}
    text = json.dumps(entry, indent=2, ensure_ascii=False)
    existing = _lists_all()["lists"].get(lid)
    title = f"{'Mise à jour de la liste' if existing else 'Nouvelle liste'} de blocage : {name}"
    intro = ("Proposition pour le catalogue des listes de blocage communautaires du pare-feu "
             f"(branche `{FW_LISTS_BRANCH}`), préparée par FRP Manager {PANEL_VERSION}.\n\n")
    base = f"https://github.com/{PANEL_GITHUB_REPO}/issues/new?"
    url = base + urlencode({"title": title, "body": f"{intro}```json\n{text}\n```\n"})
    too_long = len(url) > 8000           # limite des liens GitHub : l'entrée est collée à la main
    if too_long:
        url = base + urlencode({"title": title,
                                "body": f"{intro}```json\n(collez ici l'entrée copiée depuis le panel)\n```\n"})
    return jsonify({"ok": True, "entry": entry, "json": text, "issue_url": url, "too_long": too_long,
                    "skipped": skipped, "update": bool(existing)})

def _fw_blocked_payload():
    """Adresses bloquées ces dernières 24 h (retenues par nftables), avec leur AS,
    et les compteurs de chaque règle."""
    counts, seen = _fw_table_state()
    public = []
    for e in seen:
        try:
            if ipaddress.ip_address(e["src"]).is_global:
                public.append(e["src"])
        except ValueError:
            pass
    asn = _ip_asn_lookup(list(dict.fromkeys(public))[:200])
    for e in seen:
        e["as"] = asn.get(e["src"])
    return {"ok": True, "entries": seen[:200], "total": len(seen), "counters": counts,
            "available": _fw_seen_ok}

@app.route("/api/firewall/blocked")
@login_required
def api_firewall_blocked():
    """Repli de /ws/firewall quand le WebSocket ne passe pas."""
    return jsonify({**_fw_blocked_payload(), "now": int(time.time())})

if sock:
    @sock.route("/ws/firewall")
    def ws_firewall(ws):
        """Connexions bloquées en temps réel. Les ajouts faits par le trafic dans
        les ensembles nft ne produisent aucun événement (nft monitor ne les voit
        pas) : la table est relue chaque seconde et l'état n'est envoyé que s'il
        a changé (nouvelle adresse, nouvelle tentative, AS trouvé, compteur)."""
        if not _ws_authenticated(ws):
            return
        last, last_sent = None, time.time()
        try:
            while ws.connected:
                try:
                    data = _fw_blocked_payload()
                    payload = json.dumps(data, sort_keys=True)
                except Exception:
                    data = payload = None
                if payload and payload != last:
                    # « now » hors comparaison : sinon chaque seconde serait un changement
                    ws.send(json.dumps({**data, "now": int(time.time())}))
                    last, last_sent = payload, time.time()
                elif time.time() - last_sent >= KEEPALIVE_SECONDS:
                    ws.send(WS_KEEPALIVE_STATE)
                    last_sent = time.time()
                time.sleep(FW_LIVE_SECONDS)
        except Exception:
            pass

if __name__ == "__main__":
    host = MGR_CFG.get("bind_host", os.environ.get("FRP_MANAGER_HOST", "0.0.0.0"))
    port = MGR_CFG.get("bind_port", int(os.environ.get("FRP_MANAGER_PORT", 8765)))
    ssl_ctx = get_ssl_context()
    proto = "https" if ssl_ctx else "http"
    print(f"[INFO] FRP Manager démarré sur {proto}://{host}:{port}")
    threading.Thread(target=migrate_away_from_mmproxy, daemon=True).start()
    threading.Thread(target=_fw_sync_loop, daemon=True).start()
    app.run(host=host, port=port, debug=False, ssl_context=ssl_ctx)
