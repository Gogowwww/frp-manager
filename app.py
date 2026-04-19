#!/usr/bin/env python3
"""FRP Manager — backend Flask multi-instances"""

import os, re, json, subprocess, threading, shutil, tarfile, tempfile, platform, time, secrets, hashlib, ssl
from pathlib import Path
from datetime import datetime
from functools import wraps

from flask import Flask, render_template, request, jsonify, Response, session, redirect, url_for
import requests as req

# ── Version du panel ─────────────────────────────────────────────────────────
PANEL_VERSION     = "0.0.2"
PANEL_GITHUB_REPO = "Gogowwww/frp-manager"
PANEL_GITHUB_API  = f"https://api.github.com/repos/{PANEL_GITHUB_REPO}/releases/latest"

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

# ── Helpers système ───────────────────────────────────────────────────────────
def run_cmd(cmd, timeout=15):
    try:
        r = subprocess.run(cmd, capture_output=True, text=True, timeout=timeout)
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
            pass
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
            binary = Path(inst["binary"])
            exists = binary.exists() and os.access(binary, os.X_OK)
            st = service_status(inst["service"]) if exists else {
                "active": "not-installed", "enabled": False, "running": False}
            cfg = Path(inst["config"]) if inst["config"] else None
            result[iid] = {
                "id": iid, "type": inst["type"],
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
    return render_template("index.html")

@app.route("/api/detect")
@login_required
def api_detect():
    return jsonify({"ok": True, "instances": detect_frp(force=True)})

@app.route("/api/status")
@login_required
def api_status():
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
    detect_frp(force=False)
    if iid not in INSTANCES:
        return jsonify({"ok": False, "msg": f"Instance inconnue : {iid}"}), 404
    if action not in ("start","stop","restart","reload","enable","disable"):
        return jsonify({"ok": False, "msg": "Action invalide"}), 400
    ok, msg = service_action(INSTANCES[iid]["service"], action)
    _invalidate_cache()
    return jsonify({"ok": ok, "msg": msg or f"{action} {'OK' if ok else 'FAILED'}"})

@app.route("/api/config/<iid>", methods=["GET"])
@login_required
def api_config_get(iid):
    detect_frp(force=False)
    if iid not in INSTANCES:
        return jsonify({"ok": False, "msg": "Instance inconnue"}), 404
    cfg = Path(INSTANCES[iid]["config"])
    if not cfg.exists():
        return jsonify({"ok": True, "content": DEFAULT_CONFIGS.get(INSTANCES[iid]["type"], ""), "exists": False})
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
    detect_frp(force=False)
    if iid not in INSTANCES:
        return jsonify({"ok": False}), 404
    if request.args.get("source") == "file":
        ok, out, _ = run_cmd(["tail", "-n200", str(INSTANCES[iid]["log"])])
        return jsonify({"ok": True, "content": out})
    ok, out, err = run_cmd(["journalctl", "-u", INSTANCES[iid]["service"],
                             "-n200", "--no-pager", "-o", "short-iso"])
    return jsonify({"ok": True, "content": out if ok else err})

@app.route("/api/logs/stream/<iid>")
@login_required
def api_logs_stream(iid):
    detect_frp(force=False)
    svc = INSTANCES.get(iid, {}).get("service", iid)
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
                    # Détecter le préfixe du zip (ex: frp-manager-1.0.0/)
                    prefix = ""
                    if members and "/" in members[0]:
                        prefix = members[0].split("/")[0] + "/"

                    with tempfile.TemporaryDirectory() as tmpdir:
                        zf.extractall(tmpdir)
                        src_dir = Path(tmpdir) / prefix if prefix else Path(tmpdir)

                        # Copier app.py, templates/, frp-autoupdate.py
                        for item in ["app.py", "frp-autoupdate.py", "templates"]:
                            src = src_dir / item
                            dst = install_dir / item
                            if src.is_dir():
                                if dst.exists():
                                    shutil.rmtree(dst)
                                shutil.copytree(str(src), str(dst))
                            elif src.is_file():
                                shutil.copy2(str(src), str(dst))
                            if src.exists():
                                _panel_log(f"[INFO] Mis à jour : {item}")
            except Exception as e:
                _panel_log(f"[ERROR] Extraction : {e}")
                return
            finally:
                try: tmp_path.unlink()
                except: pass

            _panel_log(f"[OK] Panel {tag} installé. Redémarrage dans 2s…")
            # Redémarrer le service après un court délai pour laisser la réponse partir
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

if __name__ == "__main__":
    host = MGR_CFG.get("bind_host", os.environ.get("FRP_MANAGER_HOST", "0.0.0.0"))
    port = MGR_CFG.get("bind_port", int(os.environ.get("FRP_MANAGER_PORT", 8765)))
    ssl_ctx = get_ssl_context()
    proto = "https" if ssl_ctx else "http"
    print(f"[INFO] FRP Manager démarré sur {proto}://{host}:{port}")
    app.run(host=host, port=port, debug=False, ssl_context=ssl_ctx)
