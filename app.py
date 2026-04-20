#!/usr/bin/env python3
"""FRP Manager — backend Flask multi-instances"""

import os, re, json, subprocess, threading, shutil, tarfile, tempfile, platform, time, secrets, hashlib, ssl
import socket as _socket, http.client as _http_client
from pathlib import Path
from datetime import datetime
from functools import wraps

from flask import Flask, render_template, request, jsonify, Response, session, redirect, url_for
import requests as req

# ── Version du panel ─────────────────────────────────────────────────────────
_PANEL_VERSION_FALLBACK = "0.0.8"   # Version hardcodée — écrasée par state.json
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
app = Flask(__name__)
app.secret_key = MGR_CFG.get("secret_key") or secrets.token_hex(32)

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

# ── Docker / Demo ─────────────────────────────────────────────────────────────
# Détection Docker : /.dockerenv est créé par Docker dans chaque container
_IN_DOCKER = Path("/.dockerenv").exists()
# Mode démo : DEMO_MODE=true → fausses instances, aucune action réelle
DEMO_MODE   = os.environ.get("DEMO_MODE", "").lower() in ("1", "true", "yes")

_DEMO_INSTANCES = {
    "frps": {
        "id": "frps", "type": "frps",
        "binary_path": "/usr/local/bin/frps", "binary_found": True,
        "version": "0.61.0",
        "config_path": "/etc/frp/frps.toml", "config_exists": True,
        "service": "frps",
        "status": {"active": "active", "enabled": True, "running": True},
        "log_path": "/var/log/frp/frps.log",
    },
    "frpc": {
        "id": "frpc", "type": "frpc",
        "binary_path": "/usr/local/bin/frpc", "binary_found": True,
        "version": "0.61.0",
        "config_path": "/etc/frp/frpc.toml", "config_exists": True,
        "service": "frpc",
        "status": {"active": "inactive", "enabled": False, "running": False},
        "log_path": "/var/log/frp/frpc.log",
    },
}
_DEMO_CONFIGS = {
    "frps": 'bindAddr = "0.0.0.0"\nbindPort = 7000\n\n[auth]\nmethod = "token"\ntoken = "demo-secret-token"\n\n[log]\nto = "/var/log/frp/frps.log"\nlevel = "info"\nmaxDays = 3\n',
    "frpc": 'serverAddr = "demo.example.com"\nserverPort = 7000\n\n[auth]\nmethod = "token"\ntoken = "demo-secret-token"\n\n[log]\nto = "/var/log/frp/frpc.log"\nlevel = "info"\nmaxDays = 3\n\n[[proxies]]\nname = "ssh"\ntype = "tcp"\nlocalIP = "127.0.0.1"\nlocalPort = 22\nremotePort = 6022\n\n[[proxies]]\nname = "web"\ntype = "http"\nlocalIP = "127.0.0.1"\nlocalPort = 80\ncustomDomains = ["web.demo.example.com"]\n',
}
_DEMO_LOG = """\
2025-04-19 10:01:12.441 [I] [root.go:215] frps started successfully
2025-04-19 10:01:12.442 [I] [service.go:200] frps tcp listener on 0.0.0.0:7000
2025-04-19 10:02:33.118 [I] [control.go:446] [demo-client] new proxy [ssh] success
2025-04-19 10:02:33.119 [I] [control.go:446] [demo-client] new proxy [web] success
2025-04-19 10:15:00.000 [I] [proxy.go:112] [ssh] get a user connection [203.0.113.42:54321]
2025-04-19 10:30:00.000 [W] [control.go:311] [demo-client] heartbeat timeout — reconnecting
2025-04-19 10:30:02.500 [I] [control.go:446] [demo-client] new proxy [ssh] success
2025-04-19 10:30:02.501 [I] [control.go:446] [demo-client] new proxy [web] success
"""

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
        # Stream multiplexé Docker : header 8 octets (type[1] + padding[3] + size[4]) + payload
        out, i = [], 0
        while i + 8 <= len(raw):
            size = int.from_bytes(raw[i+4:i+8], "big")
            i   += 8
            out.append(raw[i:i+size].decode("utf-8", errors="replace"))
            i   += size
        return "".join(out)
    except Exception:
        return ""

def _docker_logs_stream_gen(container_name):
    """Générateur SSE qui streame les logs d'un container Docker (follow mode)."""
    if not _DOCKER_SOCK.exists():
        return
    sock = None
    try:
        sock = _socket.socket(_socket.AF_UNIX, _socket.SOCK_STREAM)
        sock.connect(str(_DOCKER_SOCK))
        sock.settimeout(5)
        req = (
            f"GET /v1.41/containers/{container_name}/logs"
            f"?stdout=1&stderr=1&follow=1&tail=50 HTTP/1.1\r\n"
            f"Host: localhost\r\nConnection: close\r\n\r\n"
        )
        sock.sendall(req.encode())
        # Sauter les headers HTTP
        hbuf = b""
        while b"\r\n\r\n" not in hbuf:
            try:
                chunk = sock.recv(1)
                if not chunk: return
                hbuf += chunk
            except _socket.timeout:
                continue
        buf = b""
        while True:
            try:
                chunk = sock.recv(4096)
                if not chunk: break
                buf += chunk
            except _socket.timeout:
                continue
            # Traiter les frames complètes
            while len(buf) >= 8:
                size = int.from_bytes(buf[4:8], "big")
                if len(buf) < 8 + size: break
                payload = buf[8:8+size].decode("utf-8", errors="replace")
                buf = buf[8+size:]
                for line in payload.splitlines():
                    yield f"data: {line}\n\n"
    except GeneratorExit:
        pass
    except Exception:
        pass
    finally:
        if sock:
            try: sock.close()
            except: pass

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
        iid = f"docker_{name}"
        instances[iid] = {
            "type":           bin_type,
            "source":         "docker",
            "container_name": name,
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

def _invalidate_cache():
    global _detect_cache_time
    _detect_cache_time = 0

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

def fetch_panel_latest():
    """Vérifie si une nouvelle version du panel est disponible sur le repo GitHub."""
    if "VOTRE_USER" in PANEL_GITHUB_REPO:
        return None, None   # Repo pas encore configuré
    try:
        r = req.get(PANEL_GITHUB_API, timeout=10,
                    headers={"Accept": "application/vnd.github.v3+json"})
        r.raise_for_status()
        data = r.json()
        tag  = data.get("tag_name", "")
        ver  = tag.lstrip("v")
        url  = data.get("html_url", f"https://github.com/{PANEL_GITHUB_REPO}/releases")
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
    if DEMO_MODE:
        return jsonify({"ok": True, "instances": _DEMO_INSTANCES})
    return jsonify({"ok": True, "instances": detect_frp(force=True)})

@app.route("/api/status")
@login_required
def api_status():
    if DEMO_MODE:
        return jsonify({"ok": True, "instances": _DEMO_INSTANCES, "installed_version": "0.61.0"})
    instances = detect_frp(force=False)
    state     = load_state()
    return jsonify({
        "ok": True, "instances": instances,
        "installed_version": state.get("installed_version"),
        "last_update_check": state.get("last_update_check"),
    })

@app.route("/api/service/<iid>/<action>", methods=["POST"])
@login_required
def api_service_action(iid, action):
    if DEMO_MODE:
        return jsonify({"ok": True, "msg": "⚠️ Mode démo — actions désactivées"})
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
    if DEMO_MODE:
        t = "frps" if iid.startswith("frps") else "frpc"
        return jsonify({"ok": True, "content": _DEMO_CONFIGS.get(t, ""), "exists": True})
    detect_frp(force=False)
    if iid not in INSTANCES:
        return jsonify({"ok": False, "msg": "Instance inconnue"}), 404
    inst = INSTANCES[iid]
    if inst.get("source") == "docker":
        return jsonify({"ok": False, "docker": True,
            "msg": f"Container Docker « {inst['container_name']} » — modifiez la config via le fichier monté dans le container (volume /etc/frp)."})
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
    content = request.get_json().get("content", "")
    cfg     = Path(INSTANCES[iid]["config"])
    cfg.parent.mkdir(parents=True, exist_ok=True)
    cfg.write_text(content)
    return jsonify({"ok": True, "msg": f"Sauvegardé : {cfg}"})

@app.route("/api/logs/<iid>")
@login_required
def api_logs(iid):
    if DEMO_MODE:
        return jsonify({"ok": True, "content": _DEMO_LOG})
    detect_frp(force=False)
    if iid not in INSTANCES:
        return jsonify({"ok": False}), 404
    inst = INSTANCES[iid]
    # ── Container Docker ──────────────────────────────────────────────────────
    if inst.get("source") == "docker":
        content = _docker_logs_raw(inst["container_name"], tail=200)
        return jsonify({"ok": True, "content": content})
    # ── Instance systemd ──────────────────────────────────────────────────────
    if request.args.get("source") == "file":
        ok, out, _ = run_cmd(["tail", "-n200", str(inst["log"])])
        return jsonify({"ok": True, "content": out})
    ok, out, err = run_cmd(["journalctl", "-u", inst["service"],
                             "-n200", "--no-pager", "-o", "short-iso"])
    return jsonify({"ok": True, "content": out if ok else err})

@app.route("/api/logs/stream/<iid>")
@login_required
def api_logs_stream(iid):
    detect_frp(force=False)
    inst = INSTANCES.get(iid, {})
    # ── Container Docker ──────────────────────────────────────────────────────
    if inst.get("source") == "docker":
        return Response(_docker_logs_stream_gen(inst["container_name"]),
                        mimetype="text/event-stream",
                        headers={"Cache-Control":"no-cache","X-Accel-Buffering":"no"})
    # ── Instance systemd ──────────────────────────────────────────────────────
    svc = inst.get("service", iid)
    def generate():
        proc = subprocess.Popen(
            ["journalctl", "-u", svc, "-f", "-n50", "--no-pager", "-o", "short-iso"],
            stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True)
        try:
            for line in proc.stdout:
                yield f"data: {line.rstrip()}\n\n"
        finally:
            proc.terminate()
    return Response(generate(), mimetype="text/event-stream",
                    headers={"Cache-Control":"no-cache","X-Accel-Buffering":"no"})

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

@app.route("/api/panel/version")
@login_required
def api_panel_version():
    """Retourne la version actuelle du panel et vérifie si une mise à jour est dispo."""
    latest_ver, release_url = fetch_panel_latest()
    repo_configured = "VOTRE_USER" not in PANEL_GITHUB_REPO
    update_available = False
    if latest_ver and repo_configured:
        try:
            from packaging.version import Version
            update_available = Version(latest_ver) > Version(PANEL_VERSION)
        except Exception:
            update_available = latest_ver != PANEL_VERSION
    return jsonify({
        "ok":               True,
        "current":          PANEL_VERSION,
        "latest":           latest_ver,
        "release_url":      release_url,
        "update_available": update_available,
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
    if not panel_update_lock.acquire(blocking=False):
        return jsonify({"ok": False, "msg": "Mise à jour du panel déjà en cours."})

    global panel_update_log
    panel_update_log = []

    def run():
        try:
            _panel_log("[INFO] Récupération des infos de release…")
            try:
                r = req.get(PANEL_GITHUB_API, timeout=12,
                            headers={"Accept": "application/vnd.github.v3+json"})
                r.raise_for_status()
                data = r.json()
                tag = data.get("tag_name", "")
                assets = data.get("assets", [])
            except Exception as e:
                _panel_log(f"[ERROR] GitHub inaccessible : {e}")
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
    Trouve et lit la config TOML d'un container frpc via docker inspect.
    Cherche dans les montages (Mounts) un fichier frpc*.toml.
    Retourne le contenu du fichier ou None.
    """
    status, data = _docker_api("GET", f"/containers/{container_name}/json")
    if status != 200 or not isinstance(data, dict):
        return None
    mounts = data.get("Mounts", [])
    for mount in mounts:
        src = mount.get("Source", "")
        dst = mount.get("Destination", "")
        # Chercher un fichier toml monté (Source sur l hote)
        for candidate in [src, dst]:
            p = Path(candidate)
            if p.suffix in (".toml", ".ini") and "frpc" in p.name.lower():
                try:
                    return p.read_text()
                except Exception:
                    pass
            # Si c est un dossier, chercher frpc*.toml dedans
            if p.is_dir():
                for f in p.glob("frpc*.toml"):
                    try:
                        return f.read_text()
                    except Exception:
                        pass
    # Fallback : chercher dans les variables d env du container (-c /path/to/frpc.toml)
    env_vars = data.get("Config", {}).get("Env", [])
    cmd = data.get("Config", {}).get("Cmd") or []
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

if __name__ == "__main__":
    host = MGR_CFG.get("bind_host", os.environ.get("FRP_MANAGER_HOST", "0.0.0.0"))
    port = MGR_CFG.get("bind_port", int(os.environ.get("FRP_MANAGER_PORT", 8765)))
    ssl_ctx = get_ssl_context()
    proto = "https" if ssl_ctx else "http"
    print(f"[INFO] FRP Manager démarré sur {proto}://{host}:{port}")
    app.run(host=host, port=port, debug=False, ssl_context=ssl_ctx)
