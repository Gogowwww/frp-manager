#!/usr/bin/env python3
"""FRP Manager — backend Flask multi-instances"""

import os, re, json, subprocess, threading, shutil, shlex, tarfile, tempfile, platform, time, secrets, hashlib, ssl
import socket as _socket, http.client as _http_client
from pathlib import Path
from datetime import datetime
from functools import wraps

from flask import Flask, render_template, request, jsonify, Response, session, redirect, url_for
import requests as req

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
        # network_mode "host" requis pour l'option IP réelle (go-mmproxy) :
        # le container partage alors le loopback de l'hôte
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
    inst = INSTANCES[iid]
    content_str = (request.get_json() or {}).get("content", "")

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
            return jsonify({"ok": True, "msg": f"Sauvegardé : {cfg_path}"})
        except Exception as e:
            return jsonify({"ok": False, "msg": f"Erreur écriture : {e}"}), 500

    # ── Instance systemd / binaire ────────────────────────────────────────────
    if not inst.get("config"):
        return jsonify({"ok": False, "msg": "Aucun fichier de config associé"}), 400
    cfg = Path(inst["config"])
    try:
        cfg.parent.mkdir(parents=True, exist_ok=True)
        cfg.write_text(content_str)
        return jsonify({"ok": True, "msg": f"Sauvegardé : {cfg}"})
    except Exception as e:
        return jsonify({"ok": False, "msg": f"Erreur écriture : {e}"}), 500

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
        container = inst["container_name"]
        # Vérifier que le container existe avant de streamer
        status, _ = _docker_api("GET", f"/containers/{container}/json")
        if status != 200:
            # Essayer avec l'id court
            s2, data2 = _docker_api("GET", f"/containers/json?all=true")
            if s2 == 200 and isinstance(data2, list):
                for c in data2:
                    names = [n.lstrip("/") for n in (c.get("Names") or [])]
                    if container in names:
                        container = (c.get("Id") or container)[:12]
                        break
        return Response(_docker_logs_stream_gen(container),
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
                        for item in ["app.py", "frp-autoupdate.py", "templates", "install.sh", "mmproxy-patch"]:
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

# ── go-mmproxy : IP réelle du client sans PROXY protocol côté service ────────
# Chaîne : client → frps → frpc (PROXY v2) → go-mmproxy (127.0.0.1:relais)
#          → service local, avec l'IP source usurpée = IP réelle du client.
# Le service voit du TCP brut avec la vraie IP, sans supporter le PROXY protocol.
MMPROXY_HOST_BIN     = "/usr/local/bin/go-mmproxy"              # chemin côté hôte (ExecStart)
MMPROXY_BUNDLED_BIN  = Path("/opt/frp-manager/bin/go-mmproxy")  # embarqué dans l'image Docker
MMPROXY_STATE_FILE   = MGR_CONF_DIR / "mmproxy.json"
MMPROXY_ALLOWED_FILE = MGR_CONF_DIR / "mmproxy-allowed.txt"
MMPROXY_ROUTES_UNIT  = "frp-mmproxy-routes"
MMPROXY_UNIT_PREFIX  = "frp-mmproxy"
MMPROXY_PORT_MIN     = 18000
MMPROXY_PORT_MAX     = 18999
SYSTEMD_UNIT_DIR     = "/etc/systemd/system"

_mmproxy_lock         = threading.Lock()
_mmproxy_install_lock = threading.Lock()

def mmproxy_bin_write_path():
    """Chemin d'écriture du binaire (vue container : /host/usr/local/bin en Docker)."""
    return FRP_BIN_DIR / "go-mmproxy"

def mmproxy_installed():
    p = mmproxy_bin_write_path()
    return p.exists() and os.access(p, os.X_OK)

def load_mmproxy_state():
    try:
        if MMPROXY_STATE_FILE.exists():
            d = json.loads(MMPROXY_STATE_FILE.read_text())
            if isinstance(d.get("instances"), dict):
                return d
    except Exception:
        pass
    return {"instances": {}}

def save_mmproxy_state(st):
    MGR_CONF_DIR.mkdir(parents=True, exist_ok=True)
    MMPROXY_STATE_FILE.write_text(json.dumps(st, indent=2))

def _mm_unit_name(iid, name, listen_port):
    slug = re.sub(r'[^A-Za-z0-9_.-]+', '-', f"{iid}-{name}").strip('-') or "tunnel"
    return f"{MMPROXY_UNIT_PREFIX}-{slug}-{listen_port}"

def _mm_routes_unit_content():
    # Règles de routage recommandées par go-mmproxy : les réponses du service
    # vers l'IP usurpée doivent rester sur loopback au lieu de partir vers Internet.
    return f"""[Unit]
Description=Regles de routage loopback pour go-mmproxy (frp-manager)
Documentation=https://github.com/path-network/go-mmproxy
After=network.target

[Service]
Type=oneshot
RemainAfterExit=yes
ExecStart=-/sbin/ip rule add from 127.0.0.1/8 iif lo table 123
ExecStart=-/sbin/ip route add local 0.0.0.0/0 dev lo table 123
ExecStart=-/sbin/ip -6 rule add from ::1/128 iif lo table 123
ExecStart=-/sbin/ip -6 route add local ::/0 dev lo table 123
ExecStop=-/sbin/ip rule del from 127.0.0.1/8 iif lo table 123
ExecStop=-/sbin/ip -6 rule del from ::1/128 iif lo table 123

[Install]
WantedBy=multi-user.target
"""

def _mm_tunnel_unit_content(iid, name, listen_port, target_ip, target_port, proto="tcp"):
    p = "udp" if proto == "udp" else "tcp"
    return f"""[Unit]
Description=go-mmproxy (IP reelle) - tunnel frp '{name}' ({iid}, {p})
Documentation=https://github.com/path-network/go-mmproxy
After=network.target {MMPROXY_ROUTES_UNIT}.service
Requires={MMPROXY_ROUTES_UNIT}.service

[Service]
Type=simple
ExecStart={MMPROXY_HOST_BIN} -l 127.0.0.1:{listen_port} -4 {target_ip}:{target_port} -6 [::1]:{target_port} -p {p} -allowed-subnets {MMPROXY_ALLOWED_FILE} -v 0
Restart=always
RestartSec=3
LimitNOFILE=1048576

[Install]
WantedBy=multi-user.target
"""

def _ensure_mm_allowed_file():
    """Seul frpc (loopback) a le droit d'envoyer des en-têtes PROXY au relais."""
    try:
        content = "127.0.0.1/32\n::1/128\n"
        if not MMPROXY_ALLOWED_FILE.exists() or MMPROXY_ALLOWED_FILE.read_text() != content:
            MGR_CONF_DIR.mkdir(parents=True, exist_ok=True)
            MMPROXY_ALLOWED_FILE.write_text(content)
        return True
    except Exception:
        return False

def _ensure_mm_routes_unit():
    path = f"{SYSTEMD_UNIT_DIR}/{MMPROXY_ROUTES_UNIT}.service"
    content = _mm_routes_unit_content()
    old = host_read_file(path)
    if (old or "").strip() != content.strip():
        if not host_write_file(path, content):
            return False, f"écriture impossible : {path}"
        run_cmd(["systemctl", "daemon-reload"])
    run_cmd(["systemctl", "enable", f"{MMPROXY_ROUTES_UNIT}.service"])
    ok, _, err = run_cmd(["systemctl", "start", f"{MMPROXY_ROUTES_UNIT}.service"])
    if not ok:
        return False, err or "démarrage échoué"
    return True, ""

def _mm_alloc_port(used):
    """Premier port libre de la plage relais (test de bind TCP + UDP sur loopback)."""
    for port in range(MMPROXY_PORT_MIN, MMPROXY_PORT_MAX + 1):
        if port in used:
            continue
        ok = True
        for fam in (_socket.SOCK_STREAM, _socket.SOCK_DGRAM):
            try:
                s = _socket.socket(_socket.AF_INET, fam)
                s.setsockopt(_socket.SOL_SOCKET, _socket.SO_REUSEADDR, 1)
                s.bind(("127.0.0.1", port))
                s.close()
            except OSError:
                ok = False
                break
        if ok:
            return port
    return None

def mmproxy_sync(iid, desired):
    """
    Aligne les unités systemd go-mmproxy d'une instance frpc sur `desired`
    ({name: {target_ip, target_port}}). Retourne (ok, ports, entries, messages).
    """
    with _mmproxy_lock:
        st        = load_mmproxy_state()
        instances = st.setdefault("instances", {})
        inst_state = dict(instances.get(iid, {}))
        msgs, changed_units = [], set()
        need_reload = False

        def _persist():
            if inst_state:
                instances[iid] = inst_state
            else:
                instances.pop(iid, None)
            save_mmproxy_state(st)

        # 1. Nettoyage : tunnels sortis du mode IP réelle
        for name in [n for n in inst_state if n not in desired]:
            entry = inst_state.pop(name)
            unit  = entry.get("unit") or _mm_unit_name(iid, name, entry.get("listen_port", 0))
            run_cmd(["systemctl", "disable", "--now", f"{unit}.service"])
            host_remove_file(f"{SYSTEMD_UNIT_DIR}/{unit}.service")
            need_reload = True
            msgs.append(f"relais « {name} » supprimé")

        # 2. Création / mise à jour
        ports = {}
        if desired:
            if not mmproxy_installed():
                _persist()
                return False, {}, inst_state, ["go-mmproxy n'est pas installé — utilisez le bouton « Installer » de l'onglet Ports"]
            if not _ensure_mm_allowed_file():
                _persist()
                return False, {}, inst_state, [f"écriture impossible : {MMPROXY_ALLOWED_FILE}"]
            rok, rerr = _ensure_mm_routes_unit()
            if not rok:
                _persist()
                return False, {}, inst_state, [f"règles de routage impossibles à activer : {rerr}"]

            used = {e.get("listen_port") for i2, entries in instances.items() if i2 != iid
                    for e in entries.values()}
            used |= {e.get("listen_port") for e in inst_state.values()}
            used.discard(None)

            for name, t in desired.items():
                prev = inst_state.get(name) or {}
                lp   = prev.get("listen_port")
                if not lp:
                    lp = _mm_alloc_port(used)
                    if not lp:
                        _persist()
                        return False, {}, inst_state, [f"aucun port libre dans la plage relais {MMPROXY_PORT_MIN}-{MMPROXY_PORT_MAX}"]
                    used.add(lp)
                unit = _mm_unit_name(iid, name, lp)
                if prev.get("unit") and prev["unit"] != unit:
                    run_cmd(["systemctl", "disable", "--now", prev["unit"] + ".service"])
                    host_remove_file(f"{SYSTEMD_UNIT_DIR}/{prev['unit']}.service")
                    need_reload = True
                proto   = "udp" if t.get("proto") == "udp" else "tcp"
                upath   = f"{SYSTEMD_UNIT_DIR}/{unit}.service"
                content = _mm_tunnel_unit_content(iid, name, lp, t["target_ip"], t["target_port"], proto)
                old     = host_read_file(upath)
                if (old or "").strip() != content.strip():
                    if not host_write_file(upath, content):
                        _persist()
                        return False, {}, inst_state, [f"écriture impossible : {upath}"]
                    changed_units.add(unit)
                    need_reload = True
                inst_state[name] = {"target_ip": t["target_ip"], "target_port": t["target_port"],
                                    "listen_port": lp, "proto": proto, "unit": unit}
                ports[name] = lp

        if need_reload:
            run_cmd(["systemctl", "daemon-reload"])

        for name in desired:
            unit = inst_state[name]["unit"]
            run_cmd(["systemctl", "enable", f"{unit}.service"])
            action = "restart" if unit in changed_units else "start"
            ok_, _, err_ = run_cmd(["systemctl", action, f"{unit}.service"])
            if not ok_:
                msgs.append(f"⚠ relais « {name} » : {err_ or 'démarrage échoué'}")

        _persist()

        # Plus aucun relais sur la machine → désactiver les règles de routage
        if not any(instances.values()):
            run_cmd(["systemctl", "disable", "--now", f"{MMPROXY_ROUTES_UNIT}.service"])

        if desired:
            msgs.append(f"{len(desired)} relais go-mmproxy synchronisé(s)")
        return True, ports, inst_state, msgs

def install_mmproxy():
    """
    Installe le binaire go-mmproxy, par ordre de préférence :
    1. binaire embarqué dans l'image Docker (/opt/frp-manager/bin)
    2. asset des releases GitHub (frp-manager puis upstream)
    3. compilation sur l'hôte via Go >= 1.21
    """
    logs = []
    dst  = mmproxy_bin_write_path()
    arch = get_arch()

    def done(msg, version):
        st = load_mmproxy_state()
        st["version"] = version
        save_mmproxy_state(st)
        logs.append(f"[OK] {msg}")
        return {"ok": True, "msg": msg, "log": logs}

    # 1. Binaire embarqué (image Docker)
    try:
        if MMPROXY_BUNDLED_BIN.exists():
            dst.parent.mkdir(parents=True, exist_ok=True)
            shutil.copy2(str(MMPROXY_BUNDLED_BIN), str(dst))
            dst.chmod(0o755)
            return done(f"go-mmproxy installé depuis l'image Docker → {MMPROXY_HOST_BIN}", "bundled")
    except Exception as e:
        logs.append(f"binaire embarqué : {e}")

    # 2. Assets de release GitHub
    for repo in (PANEL_GITHUB_REPO, "path-network/go-mmproxy"):
        try:
            r = req.get(f"https://api.github.com/repos/{repo}/releases/latest", timeout=12,
                        headers={"Accept": "application/vnd.github.v3+json"})
            r.raise_for_status()
            rel = r.json()
            asset = next((a for a in rel.get("assets", [])
                          if "go-mmproxy" in a.get("name", "").lower()
                          and re.search(rf'linux[_-]{arch}(\.|$)', a.get("name", "").lower())), None)
            if not asset:
                logs.append(f"{repo} : aucun binaire go-mmproxy linux/{arch} dans la release")
                continue
            aname = asset["name"].lower()
            with req.get(asset["browser_download_url"], stream=True, timeout=120) as dl:
                dl.raise_for_status()
                with tempfile.NamedTemporaryFile(delete=False) as tmp:
                    for chunk in dl.iter_content(65536):
                        tmp.write(chunk)
                    tmp_path = Path(tmp.name)
            try:
                data = None
                if aname.endswith((".tar.gz", ".tgz")):
                    with tarfile.open(tmp_path, "r:gz") as tf:
                        for m in tf.getmembers():
                            if m.isfile() and Path(m.name).name == "go-mmproxy":
                                data = tf.extractfile(m).read()
                                break
                else:
                    data = tmp_path.read_bytes()
                if not data:
                    raise RuntimeError("binaire absent de l'archive")
                dst.parent.mkdir(parents=True, exist_ok=True)
                dst.write_bytes(data)
                dst.chmod(0o755)
                return done(f"go-mmproxy téléchargé depuis {repo} ({rel.get('tag_name', '?')})",
                            rel.get("tag_name") or "release")
            finally:
                tmp_path.unlink(missing_ok=True)
        except Exception as e:
            logs.append(f"{repo} : {e}")

    # 3. Compilation sur l'hôte via le patch UDP (Go >= 1.21).
    # Uniquement hors Docker : en Docker, le script de patch est DANS le container
    # et `go` tourne sur l'hôte (via nsenter) — il ne verrait pas les fichiers.
    # En Docker, le binaire embarqué (stratégie 1) couvre déjà ce cas.
    build_sh = Path(__file__).resolve().parent / "mmproxy-patch" / "build.sh"
    if _IN_DOCKER:
        logs.append("compilation hôte ignorée en mode Docker (binaire embarqué attendu)")
    elif not build_sh.exists():
        logs.append(f"script de build absent : {build_sh}")
    else:
        ok, out, _ = run_host(["sh", "-c", "go version"], timeout=15)
        if ok and out:
            m = re.search(r'go(\d+)\.(\d+)', out)
            if m and (int(m.group(1)), int(m.group(2))) >= (1, 21):
                logs.append(f"compilation (patch UDP) via {out.strip()} …")
                bok, bout, berr = run_host(
                    ["sh", str(build_sh), str(dst)], timeout=420)
                if bok and mmproxy_installed():
                    return done("go-mmproxy (patch UDP) compilé et installé via Go", "go-build")
                logs.append(f"build.sh : {berr or bout or 'échec'}")
            else:
                logs.append(f"Go trop ancien ({out.strip()}) — 1.21+ requis")
        else:
            logs.append("Go absent de l'hôte")

    return {"ok": False,
            "msg": "Installation impossible (voir log). Manuellement : installez Go ≥ 1.21 puis "
                   "GOBIN=/usr/local/bin go install github.com/path-network/go-mmproxy@latest",
            "log": logs}

@app.route("/api/mmproxy/status")
@login_required
def api_mmproxy_status():
    iid       = request.args.get("iid", "")
    installed = mmproxy_installed()
    st        = load_mmproxy_state()
    routes_active = service_status(MMPROXY_ROUTES_UNIT)["running"] if installed else False
    entries = {}
    if iid:
        for name, e in st.get("instances", {}).get(iid, {}).items():
            unit = e.get("unit", "")
            entries[name] = {**e, "active": service_status(unit)["running"] if unit else False}
    return jsonify({"ok": True, "installed": installed, "version": st.get("version"),
                    "bin": MMPROXY_HOST_BIN, "routes_active": routes_active,
                    "in_docker": IN_DOCKER, "entries": entries,
                    "port_range": [MMPROXY_PORT_MIN, MMPROXY_PORT_MAX]})

@app.route("/api/mmproxy/install", methods=["POST"])
@login_required
def api_mmproxy_install():
    if not _mmproxy_install_lock.acquire(blocking=False):
        return jsonify({"ok": False, "msg": "Installation déjà en cours"})
    try:
        if mmproxy_installed():
            return jsonify({"ok": True, "msg": "go-mmproxy est déjà installé", "log": []})
        return jsonify(install_mmproxy())
    finally:
        _mmproxy_install_lock.release()

@app.route("/api/mmproxy/sync", methods=["POST"])
@login_required
def api_mmproxy_sync():
    data = request.get_json() or {}
    iid  = str(data.get("iid", ""))
    detect_frp(force=False)
    inst = INSTANCES.get(iid)
    if not inst or inst.get("type") != "frpc":
        return jsonify({"ok": False, "msg": f"Instance frpc inconnue : {iid}"}), 404

    desired, errs = {}, []
    for t in (data.get("tunnels") or []):
        name = str(t.get("name", "")).strip()
        if not name:
            errs.append("tunnel sans nom")
            continue
        try:
            port = int(t.get("target_port", 0))
        except (TypeError, ValueError):
            port = 0
        if not (1 <= port <= 65535):
            errs.append(f"« {name} » : port local invalide")
            continue
        ip = str(t.get("target_ip", "") or "127.0.0.1").strip()
        if ip in ("", "localhost"):
            ip = "127.0.0.1"
        if not re.match(r'^127(\.\d{1,3}){3}$', ip):
            errs.append(f"« {name} » : l'IP locale doit être en 127.0.0.0/8 (service sur la même machine que frpc)")
            continue
        proto = "udp" if str(t.get("proto", "tcp")).lower() == "udp" else "tcp"
        if name in desired:
            errs.append(f"nom de tunnel dupliqué : {name}")
            continue
        desired[name] = {"target_ip": ip, "target_port": port, "proto": proto}

    if errs:
        return jsonify({"ok": False, "msg": " · ".join(errs)}), 400
    # Container frpc : OK seulement en network_mode host (loopback partagé avec
    # l'hôte, sinon le container ne peut pas atteindre le relais sur 127.0.0.1)
    if desired and inst.get("source") == "docker" and inst.get("network_mode") != "host":
        return jsonify({"ok": False, "msg": "IP réelle (go-mmproxy) : le container frpc doit être en network_mode: host"}), 400

    ok, ports, entries, msgs = mmproxy_sync(iid, desired)
    return jsonify({"ok": ok, "ports": ports, "entries": entries,
                    "msg": " · ".join(msgs) if msgs else ("OK" if ok else "échec")}), (200 if ok else 500)

if __name__ == "__main__":
    host = MGR_CFG.get("bind_host", os.environ.get("FRP_MANAGER_HOST", "0.0.0.0"))
    port = MGR_CFG.get("bind_port", int(os.environ.get("FRP_MANAGER_PORT", 8765)))
    ssl_ctx = get_ssl_context()
    proto = "https" if ssl_ctx else "http"
    print(f"[INFO] FRP Manager démarré sur {proto}://{host}:{port}")
    app.run(host=host, port=port, debug=False, ssl_context=ssl_ctx)
