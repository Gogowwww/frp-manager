#!/usr/bin/env python3
"""FRP Manager — backend Flask multi-instances"""

import os, re, sys, posixpath, json, subprocess, threading, shutil, tarfile, tempfile, platform, time, secrets, hashlib, hmac, base64, ipaddress, ssl, queue, gzip
import socket as _socket, http.client as _http_client
from urllib.parse import urlsplit, urlencode, quote_plus
from pathlib import Path, PurePosixPath
from datetime import datetime, timedelta
from functools import wraps

from flask import Flask, render_template, request, jsonify, Response, session, redirect, url_for, g, has_request_context
import requests as req
from itsdangerous import URLSafeTimedSerializer, BadData

try:
    # Hachage du mot de passe : argon2id si argon2-cffi est installé, sinon
    # scrypt (bibliothèque standard). Une mise à jour du panel dont le pip a
    # échoué démarre donc quand même.
    from argon2 import PasswordHasher as _PasswordHasher
    from argon2.exceptions import VerificationError as _Argon2Mismatch, InvalidHashError as _Argon2Invalid
    _ARGON2 = _PasswordHasher()
except ImportError:
    _ARGON2 = None

try:
    import tomllib as _tomllib
except ImportError:          # Python < 3.11 : tomli (requirements.txt), s'il est installé
    try:
        import tomli as _tomllib
    except ImportError:
        _tomllib = None

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
    # 3. Version notée par la mise à jour du panel
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
# FRP_MANAGER_CONFIG : autre emplacement (tests, installations particulières)
MGR_CONF_FILE = Path(os.environ.get("FRP_MANAGER_CONFIG") or "/etc/frp-manager/frp-manager.json")
MGR_CONF_DIR  = MGR_CONF_FILE.parent

SSL_CERT_DIR  = MGR_CONF_DIR / "ssl"
SSL_CERT_FILE = SSL_CERT_DIR / "cert.pem"
SSL_KEY_FILE  = SSL_CERT_DIR / "key.pem"

def _default_manager_config():
    return {
        # Nouvelle installation : joignable depuis cette machine seulement.
        # Pour ouvrir au réseau : Réglages, bind_host, ou FRP_MANAGER_HOST=0.0.0.0.
        "bind_host":        "127.0.0.1",
        "bind_port":        8765,
        "username":         "admin",
        "password_hash":    "",
        "secret_key":       secrets.token_hex(32),
        "session_timeout":  3600,
        "ssl_enabled":      True,
        # Miroirs tiers (ghproxy, ghfast, gh-proxy) en repli de github.com pour
        # télécharger frp : pratique quand GitHub est filtré, mais c'est un
        # intermédiaire de plus dans la chaîne d'approvisionnement.
        "download_mirrors": True,
        "nicknames":        {},
        "webhooks":         [],
        # Comptes supplémentaires (le propriétaire reste username/password_hash)
        # et connexion unique OpenID Connect : voir « Utilisateurs et permissions »
        "users":            {},
        "oidc":             {},
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
    """Écriture atomique, lisible par root seulement (hash du mot de passe,
    clé de session)."""
    MGR_CONF_DIR.mkdir(parents=True, exist_ok=True)
    tmp = MGR_CONF_FILE.with_name(MGR_CONF_FILE.name + ".tmp")
    fd = os.open(tmp, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    with os.fdopen(fd, "w") as f:
        f.write(json.dumps(cfg, indent=2))
    os.chmod(tmp, 0o600)
    os.replace(tmp, MGR_CONF_FILE)

def ensure_manager_config():
    """Au démarrage : le fichier de config existe, avec sa clé de session, et
    n'est lisible que par root. Une installation antérieure sans fichier (panel
    jamais configuré) garde son ancienne adresse d'écoute, 0.0.0.0 : la passer
    à 127.0.0.1 couperait l'accès à distance à la mise à jour."""
    global MGR_CFG
    try:
        data = json.loads(MGR_CONF_FILE.read_text()) if MGR_CONF_FILE.exists() else None
    except Exception:
        print(f"[WARN] {MGR_CONF_FILE} illisible : laissé tel quel")
        return
    if data is not None and data.get("secret_key") and "bind_host" in data:
        try:
            os.chmod(MGR_CONF_FILE, 0o600)
        except OSError:
            pass
        return
    cfg = dict(MGR_CFG)
    if data is None:
        # Nouvelle installation : FRP_MANAGER_PORT (docker-compose.yml) donne le port
        try:
            cfg["bind_port"] = int(os.environ.get("FRP_MANAGER_PORT") or cfg["bind_port"])
        except ValueError:
            pass
        # Certificat déjà généré par un panel antérieur (install.sh écrit
        # state.json avant le premier démarrage : pas un indice fiable)
        if SSL_CERT_FILE.exists():
            cfg["bind_host"] = "0.0.0.0"
    elif "bind_host" not in data:
        cfg["bind_host"] = "0.0.0.0"
    save_manager_config(cfg)
    MGR_CFG = cfg

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
app.config.update(
    SESSION_COOKIE_HTTPONLY=True,
    SESSION_COOKIE_SAMESITE="Strict",
    # Secure dès que le panel sert du HTTPS ; derrière un reverse proxy TLS
    # avec ssl_enabled=false, cookie_secure=true dans frp-manager.json.
    SESSION_COOKIE_SECURE=bool(MGR_CFG.get("ssl_enabled", True) or MGR_CFG.get("cookie_secure")),
    PERMANENT_SESSION_LIFETIME=timedelta(seconds=int(MGR_CFG.get("session_timeout") or 3600)),
    # Archive frp envoyée à la main (~15 Mo) : large marge, mais pas illimité
    MAX_CONTENT_LENGTH=128 * 1024 * 1024,
)
# Les modules JS importés par main.js n'ont pas de ?v=version dans leur URL :
# max-age=0 force le navigateur à les revalider (304 si inchangés), sans quoi
# il pourrait garder d'anciens modules en cache après une mise à jour du panel.
app.config["SEND_FILE_MAX_AGE_DEFAULT"] = 0
# Ping toutes les 25 s : évite qu'un reverse proxy ou un tunnel frp ne coupe
# une connexion WebSocket restée silencieuse (journal sans nouvelle ligne).
app.config["SOCK_SERVER_OPTIONS"] = {"ping_interval": 25}
sock = Sock(app) if Sock else None

# ── Langue des messages ──────────────────────────────────────────────────────
# Les messages renvoyés à l'interface suivent sa langue : en-tête X-Lang posé
# par api.js (paramètre ?lang= pour les journaux en direct), sinon la langue
# du navigateur. Hors requête (journaux d'installation, lignes des journaux en
# direct) : la langue retenue par le thread, sinon celle du système (LANG).
_tls = threading.local()

def _lang():
    if has_request_context():
        v = request.headers.get("X-Lang") or request.args.get("lang") or ""
        if not v and request.accept_languages:
            # Navigateur dans une autre langue : anglais, comme l'interface
            v = request.accept_languages.best_match(["fr", "en"]) or "en"
    else:
        v = getattr(_tls, "lang", None)
        if not v:
            # Ligne de commande : français seulement si le système l'est (comme install.sh)
            env = os.environ.get("LC_ALL") or os.environ.get("LC_MESSAGES") or os.environ.get("LANG") or ""
            v = "fr" if env.lower().startswith("fr") else "en"
    return "en" if v.lower().startswith("en") else "fr"

def M(fr, en):
    """Le message dans la langue de l'interface (français par défaut)."""
    return en if _lang() == "en" else fr

def _in_lang(lang, fn):
    """fn lancé dans un thread, avec la langue de la requête qui l'a démarré."""
    def run(*args, **kwargs):
        _tls.lang = lang
        return fn(*args, **kwargs)
    return run

# Libellés des ports frp (détectés en français, parfois mis en cache pour tous
# les onglets) : traduits au moment de la réponse
_PORT_LABELS_EN = {
    "Connexion frpc": "frpc connections", "Connexion serveur": "Server connection",
    "Dashboard web": "Web dashboard", "Connexion des clients frpc": "frpc client connections",
    "Sites HTTP (vhost)": "HTTP sites (vhost)", "Sites HTTPS (vhost)": "HTTPS sites (vhost)",
    "Tableau de bord frps": "frps dashboard", "Port réservé aux clients": "Port reserved for clients",
    "Plage réservée aux clients": "Range reserved for clients", "Port ouvert par frps": "Port opened by frps",
}

def port_label(label):
    if _lang() != "en" or not label:
        return label
    if label in _PORT_LABELS_EN:
        return _PORT_LABELS_EN[label]
    m = re.fullmatch(r"Port « (.*) »", label)
    if m:
        return f"Port “{m.group(1)}”"
    return re.sub(r"^Visiteur ", "Visitor ", label)

def with_port_labels(items):
    return [{**i, "label": port_label(i.get("label"))} for i in items]

# ── En-têtes de sécurité, CSP et jeton CSRF ──────────────────────────────────
# Les scripts en ligne des pages portent un nonce tiré à chaque requête : la CSP
# refuse tout autre script en ligne. Les styles en ligne restent permis (l'interface
# en pose quelques-uns depuis le JS), les polices viennent de Google Fonts.
def csp_nonce():
    if not hasattr(g, "csp_nonce"):
        g.csp_nonce = secrets.token_urlsafe(16)
    return g.csp_nonce

def csrf_token():
    """Jeton lié à la session, exigé sur toute requête qui modifie quelque chose
    (en-tête X-CSRF-Token, envoyé par api.js)."""
    tok = session.get("csrf")
    if not tok:
        tok = session["csrf"] = secrets.token_urlsafe(32)
    return tok

app.jinja_env.globals.update(csp_nonce=csp_nonce, csrf_token=csrf_token)

def _csrf_valid():
    sent = request.headers.get("X-CSRF-Token") or ""
    tok = session.get("csrf") or ""
    return bool(tok and sent) and hmac.compare_digest(sent.encode(), tok.encode())

@app.after_request
def _security_headers(resp):
    script_src = f"'self' 'nonce-{g.csp_nonce}'" if hasattr(g, "csp_nonce") else "'self'"
    host = request.host
    resp.headers.setdefault("Content-Security-Policy", "; ".join([
        "default-src 'self'",
        f"script-src {script_src}",
        "style-src 'self' 'unsafe-inline' https://fonts.googleapis.com",
        "font-src 'self' https://fonts.gstatic.com",
        "img-src 'self' data:",
        f"connect-src 'self' wss://{host} ws://{host}",
        "object-src 'none'",
        "base-uri 'none'",
        "form-action 'self'",
        "frame-ancestors 'none'",
    ]))
    resp.headers.setdefault("X-Content-Type-Options", "nosniff")
    resp.headers.setdefault("X-Frame-Options", "DENY")
    resp.headers.setdefault("Referrer-Policy", "no-referrer")
    if request.path.startswith("/api/"):
        # Réponses de l'API (configs avec leurs tokens…) jamais gardées en cache
        resp.headers.setdefault("Cache-Control", "no-store")
    return resp

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
        return Response(M("Introuvable", "Not found"), status=404, mimetype="text/plain")
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
    # Le site lui-même (redirection de /releases/latest) : pas soumis au quota de l'API
    ("github.com", "https://github.com/fatedier/frp/releases/latest"),
]
FRP_RELEASE_DOWNLOAD = "https://github.com/fatedier/frp/releases/download/{tag}/{filename}"
# Miroirs TIERS, essayés seulement si github.com échoue et si download_mirrors
# est vrai (Réglages) : ils voient passer et pourraient modifier le fichier, d'où
# la vérification par la somme SHA-256 publiée avec chaque release de frp.
THIRD_PARTY_MIRRORS = [
    "https://mirror.ghproxy.com/" + FRP_RELEASE_DOWNLOAD,
    "https://ghfast.top/" + FRP_RELEASE_DOWNLOAD,
    "https://gh-proxy.com/" + FRP_RELEASE_DOWNLOAD,
]
FALLBACK_DOWNLOAD_MIRRORS = [FRP_RELEASE_DOWNLOAD] + THIRD_PARTY_MIRRORS
FRP_CHECKSUMS_FILE = "frp_sha256_checksums.txt"   # joint à chaque release de fatedier/frp

# Pas de configs par défaut créées automatiquement — l'utilisateur les crée lui-même
DEFAULT_CONFIGS = {
    "frps": 'bindAddr = "0.0.0.0"\nbindPort = 7000\n\nauth.method = "token"\nauth.token = "changeme"\n\nlog.to = "/var/log/frp/frps.log"\nlog.level = "info"\nlog.maxDays = 3\n',
    "frpc": 'serverAddr = ""\nserverPort = 7000\n\nauth.method = "token"\nauth.token = "changeme"\n\nlog.to = "/var/log/frp/frpc.log"\nlog.level = "info"\nlog.maxDays = 3\n',
}

# ── Auth ──────────────────────────────────────────────────────────────────────
PASSWORD_MIN_LENGTH = 12
PASSWORD_MAX_LENGTH = 1024
_SHA256_HEX = re.compile(r"[0-9a-fA-F]{64}")
_SCRYPT_N, _SCRYPT_R, _SCRYPT_P = 2 ** 15, 8, 1

def _b64(data):
    return base64.b64encode(data).decode()

def _scrypt(pw, salt, n=_SCRYPT_N, r=_SCRYPT_R, p=_SCRYPT_P):
    return hashlib.scrypt(pw.encode(), salt=salt, n=n, r=r, p=p, maxmem=128 * 1024 * 1024, dklen=32)

def hash_password(pw):
    """argon2id (argon2-cffi) ou, à défaut, scrypt ; sel aléatoire dans les deux cas."""
    if _ARGON2:
        return _ARGON2.hash(pw)
    salt = secrets.token_bytes(16)
    return f"scrypt${_SCRYPT_N}${_SCRYPT_R}${_SCRYPT_P}${_b64(salt)}${_b64(_scrypt(pw, salt))}"

def verify_password(pw, stored):
    """→ (mot de passe correct, à rehacher). Comparaisons en temps constant.
    Formats acceptés : argon2id, scrypt, et l'ancien SHA-256 sans sel des versions
    ≤ 0.0.50, rehaché dès la première connexion réussie."""
    stored = stored or ""
    if stored.startswith("$argon2"):
        if not _ARGON2:
            print("[ERROR] Mot de passe haché en argon2 mais argon2-cffi est absent : "
                  "installez-le (pip install argon2-cffi) ou réinitialisez le mot de passe "
                  "(python3 app.py --reset-password)")
            return False, False
        try:
            _ARGON2.verify(stored, pw)
        except (_Argon2Mismatch, _Argon2Invalid, ValueError):   # UnicodeError : hash corrompu
            return False, False
        return True, _ARGON2.check_needs_rehash(stored)
    if stored.startswith("scrypt$"):
        try:
            _, n, r, p, salt, digest = stored.split("$")
            params = (int(n), int(r), int(p))
            ok = hmac.compare_digest(_scrypt(pw, base64.b64decode(salt), *params), base64.b64decode(digest))
        except (ValueError, TypeError):
            return False, False
        return ok, ok and (_ARGON2 is not None or params != (_SCRYPT_N, _SCRYPT_R, _SCRYPT_P))
    if _SHA256_HEX.fullmatch(stored):
        ok = hmac.compare_digest(hashlib.sha256(pw.encode()).hexdigest(), stored.lower())
        return ok, ok
    return False, False

def needs_setup():
    """Aucun mot de passe : seule la page de configuration initiale répond."""
    return not MGR_CFG.get("password_hash")

# ── Utilisateurs et permissions (ACL) ────────────────────────────────────────
# Le compte créé à la configuration initiale (username / password_hash) est le
# « propriétaire » : toujours administrateur, jamais supprimé ni désactivé, seul à
# changer ses propres identifiants dans Réglages. Les autres comptes sont dans
# MGR_CFG["users"] (identifiant aléatoire → fiche) : locaux (mot de passe) ou
# OpenID Connect (voir plus bas).
#
# Une fiche : username, source ("local" | "oidc"), enabled, role ("admin" |
# "operator" | "viewer" | "custom"), permissions (liste, lue seulement pour le
# rôle « custom »), instances ("*" ou liste d'identifiants d'instances). Les
# droits sont relus dans la configuration à chaque requête : modifier ou
# désactiver un compte agit aussitôt, sans attendre la fin de sa session.
OWNER_ID  = "owner"
USERS_MAX = 100

# permission → (limitable à certaines instances, sensible). « Sensible » : revient
# à donner la main sur la machine (config frp, pare-feu, mise à jour du panel…).
PERMISSIONS = {
    "instances.view":    (True,  False),   # voir les instances et leur état
    "instances.control": (True,  False),   # démarrer, arrêter, redémarrer, surnom
    "instances.delete":  (True,  True),
    "config.read":       (True,  True),    # les configs contiennent les jetons frp
    "config.write":      (True,  True),
    "logs.view":         (True,  False),
    "ports.view":        (False, False),
    "ports.manage":      (False, True),
    "firewall.view":     (False, False),
    "firewall.manage":   (False, True),
    "updates.view":      (False, False),
    "updates.install":   (False, True),    # installer / envoyer une version de frp
    "updates.panel":     (False, True),    # mettre à jour le panel lui-même
    "webhooks.view":     (False, False),
    "webhooks.manage":   (False, True),
    "settings.manage":   (False, True),
    "users.manage":      (False, True),
}
# Accorder la clé accorde aussi les valeurs (écrire une config suppose de la lire)
_PERMISSION_IMPLIES = {
    "instances.control": ("instances.view",),
    "instances.delete":  ("instances.view",),
    "config.read":       ("instances.view",),
    "config.write":      ("config.read",),
    "logs.view":         ("instances.view",),
    "ports.view":        ("instances.view",),
    "ports.manage":      ("ports.view",),
    "firewall.view":     ("instances.view",),
    "firewall.manage":   ("firewall.view",),
    "updates.install":   ("updates.view",),
    "updates.panel":     ("updates.view",),
    "webhooks.manage":   ("webhooks.view",),
}
ROLES = {
    "admin":    ("*",),
    "operator": ("instances.control", "config.write", "logs.view", "ports.manage",
                 "firewall.view", "updates.view", "webhooks.view"),
    "viewer":   ("instances.view", "logs.view", "ports.view", "firewall.view", "updates.view"),
}
USER_ROLES = (*ROLES, "custom")

def expand_permissions(perms):
    """Permissions accordées + celles qu'elles impliquent ; "*" = toutes."""
    out, todo = set(), list(perms or ())
    while todo:
        p = todo.pop()
        if p == "*":
            out.update(PERMISSIONS)
        elif p in PERMISSIONS and p not in out:
            out.add(p)
            todo.extend(_PERMISSION_IMPLIES.get(p, ()))
    return frozenset(out)

def user_permissions(user):
    """Permissions effectives d'une fiche. Limité à des instances, un compte ne
    garde que les permissions qui portent sur des instances."""
    role = user.get("role")
    if role == "admin":
        return expand_permissions(["*"])
    granted = expand_permissions(ROLES[role] if role in ROLES else user.get("permissions"))
    if user_instances(user) is not None:
        granted = frozenset(p for p in granted if PERMISSIONS[p][0])
    return granted

def user_instances(user):
    """None = toutes les instances ; sinon l'ensemble des identifiants permis."""
    ins = user.get("instances", "*")
    if user.get("role") == "admin" or ins == "*":
        return None
    return frozenset(str(i) for i in ins) if isinstance(ins, list) else frozenset()

def user_can(user, perm):
    return perm in user_permissions(user)

def instance_allowed(user, iid):
    allowed = user_instances(user)
    return allowed is None or iid in allowed

def filter_instances(instances, user):
    allowed = user_instances(user)
    return instances if allowed is None else {k: v for k, v in instances.items() if k in allowed}

def is_full_admin(user):
    """Administrateur : toutes les permissions sur toutes les instances. Réservé
    aux réglages qui décident de qui peut se connecter (OpenID Connect)."""
    return user_instances(user) is None and user_permissions(user) == frozenset(PERMISSIONS)

def _owner_user():
    return {"id": OWNER_ID, "username": MGR_CFG.get("username", "admin"), "source": "local",
            "password_hash": MGR_CFG.get("password_hash") or "", "enabled": True,
            "role": "admin", "instances": "*", "owner": True}

def all_users():
    """identifiant → fiche, propriétaire compris."""
    users = {OWNER_ID: _owner_user()} if MGR_CFG.get("password_hash") else {}
    for uid, u in (MGR_CFG.get("users") or {}).items():
        if isinstance(u, dict) and uid != OWNER_ID:
            users[uid] = {**u, "id": uid, "source": u.get("source", "local")}
    return users

def _same_name(a, b):
    return a.casefold() == b.casefold()

def find_local_user(username):
    """Fiche locale de cet identifiant (casse exacte), ou None."""
    found = None
    for u in all_users().values():
        if u["source"] == "local" and hmac.compare_digest(username.encode(), str(u["username"]).encode()):
            found = u
    return found

_dummy_hash_value = None

def _dummy_hash():
    """Hash d'un mot de passe que personne ne connaît : vérifié quand l'identifiant
    n'existe pas, pour que la réponse prenne le même temps."""
    global _dummy_hash_value
    if _dummy_hash_value is None:
        _dummy_hash_value = hash_password(secrets.token_hex(16))
    return _dummy_hash_value

def _cfg_update(**patch):
    global MGR_CFG
    with _cfg_lock:
        cfg = {**MGR_CFG, **patch}
        save_manager_config(cfg)
        MGR_CFG = cfg
    return cfg

def _users_update(mutate):
    """Applique mutate(dict_des_comptes) sur une copie puis enregistre."""
    with _cfg_lock:
        users = {k: dict(v) for k, v in (MGR_CFG.get("users") or {}).items()}
        result = mutate(users)
        _cfg_update(users=users)
    return result

_cfg_lock = threading.RLock()

def _session_fingerprint(user):
    """Empreinte du secret du compte, gardée dans la session : changer le mot de
    passe (ou lier le compte à une autre identité OIDC) déconnecte les sessions
    ouvertes."""
    if user["source"] == "oidc":
        o = user.get("oidc") or {}
        material = f"oidc\0{o.get('iss', '')}\0{o.get('sub', '')}"
    else:
        material = user.get("password_hash") or ""
    return hmac.new(app.secret_key.encode(), material.encode(), hashlib.sha256).hexdigest()[:16]

def current_user():
    """Fiche du compte connecté (relue dans la configuration), ou None."""
    if "_user" in g.__dict__:
        return g._user
    user = None
    if session.get("authenticated"):
        u = all_users().get(session.get("uid") or OWNER_ID)    # sans « uid » : session d'avant les comptes
        if u and u.get("enabled", True) and hmac.compare_digest(
                str(session.get("pwfp", "")).encode(), _session_fingerprint(u).encode()):
            user = u
    g._user = user
    return user

def _session_valid():
    return current_user() is not None

def _start_session(uid=OWNER_ID):
    """Nouvelle session après authentification (pas de fixation de session). Le
    jeton CSRF est gardé : la page déjà ouverte (Réglages, après un changement
    de mot de passe) continue de fonctionner."""
    tok = session.get("csrf")
    session.clear()
    session.permanent = True
    session["authenticated"] = True
    session["uid"] = uid
    session["pwfp"] = _session_fingerprint(all_users()[uid])
    session["csrf"] = tok or secrets.token_urlsafe(32)
    g.__dict__.pop("_user", None)

def _unauthenticated():
    if request.path.startswith("/api/"):
        return jsonify({"ok": False, "msg": M("Non authentifié", "Not authenticated")}), 401
    return redirect(url_for("login_page"))

def _forbidden(msg=None):
    return jsonify({"ok": False, "msg": msg or M("Permission refusée", "Permission denied")}), 403

def login_required(f):
    @wraps(f)
    def decorated(*args, **kwargs):
        if not current_user():
            return _unauthenticated()
        return f(*args, **kwargs)
    return decorated

def permission_required(*perms):
    """Compte connecté qui possède toutes ces permissions et, si la route porte
    sur une instance (<iid>), qui a le droit d'y toucher."""
    def decorator(f):
        @wraps(f)
        def decorated(*args, **kwargs):
            user = current_user()
            if not user:
                return _unauthenticated()
            have = user_permissions(user)
            missing = [p for p in perms if p not in have]
            if missing:
                return _forbidden(M(f"Permission refusée : {', '.join(missing)}",
                                    f"Permission denied: {', '.join(missing)}"))
            iid = kwargs.get("iid")
            if iid is not None and not instance_allowed(user, iid):
                return _forbidden(M("Accès refusé à cette instance", "Access to this instance is denied"))
            return f(*args, **kwargs)
        return decorated
    return decorator

# Seules routes servies tant qu'aucun mot de passe n'existe
_SETUP_ENDPOINTS = {"setup_page", "api_setup", "versioned_asset", "static"}

@app.before_request
def _security_gate():
    if request.method not in ("GET", "HEAD", "OPTIONS") and not _csrf_valid():
        return jsonify({"ok": False, "msg": M("Jeton CSRF absent ou invalide : rechargez la page.",
                                              "Missing or invalid CSRF token: reload the page.")}), 403
    if needs_setup() and request.endpoint not in _SETUP_ENDPOINTS:
        if request.path.startswith("/api/"):
            return jsonify({"ok": False, "msg": M("Configuration initiale requise", "Initial setup required"), "setup": True}), 401
        return redirect(url_for("setup_page"))
    return None

# ── Limitation des tentatives de connexion ───────────────────────────────────
# Par adresse IP : 5 essais libres, puis verrouillage de 30 s, doublé à chaque
# nouvel échec (1 h au plus). Oublié 24 h après le dernier échec ou à la
# première connexion réussie. Derrière un reverse proxy, toutes les requêtes
# arrivent de son adresse : le verrouillage vaut alors pour tout le monde.
LOGIN_FREE_ATTEMPTS = 5
LOGIN_LOCK_BASE     = 30
LOGIN_LOCK_MAX      = 3600
LOGIN_FORGET_AFTER  = 86400
_login_failures     = {}         # ip → {"count", "until", "last"}
_login_lock         = threading.Lock()

def _login_retry_after(ip, now=None):
    now = now or time.time()
    with _login_lock:
        e = _login_failures.get(ip)
        return max(0, int(e["until"] - now + 0.999)) if e else 0

def _login_failed(ip, now=None):
    now = now or time.time()
    with _login_lock:
        if len(_login_failures) > 10000:
            for k in [k for k, e in _login_failures.items() if now - e["last"] > LOGIN_FORGET_AFTER]:
                del _login_failures[k]
        e = _login_failures.get(ip)
        if not e or now - e["last"] > LOGIN_FORGET_AFTER:
            e = _login_failures[ip] = {"count": 0, "until": 0, "last": now}
        e["count"] += 1
        e["last"] = now
        extra = e["count"] - LOGIN_FREE_ATTEMPTS
        if extra >= 0:
            e["until"] = now + min(LOGIN_LOCK_MAX, LOGIN_LOCK_BASE * 2 ** min(extra, 20))

def _login_succeeded(ip):
    with _login_lock:
        _login_failures.pop(ip, None)

def _too_many_attempts(wait):
    resp = jsonify({"ok": False, "msg": M(f"Trop de tentatives : réessayez dans {wait} s.",
                                         f"Too many attempts: try again in {wait} s.")})
    resp.status_code = 429
    resp.headers["Retry-After"] = str(wait)
    return resp

@app.route("/login", methods=["GET"])
def login_page():
    if _session_valid():
        return redirect(url_for("index"))
    oidc = oidc_config()
    return render_template("login.html", oidc_enabled=oidc_enabled(),
                           oidc_label=oidc["label"] or "SSO",
                           oidc_error=request.args.get("error", "") if request.args.get("error", "") in OIDC_ERRORS else "")

@app.route("/api/login", methods=["POST"])
def api_login():
    ip = request.remote_addr or "?"
    wait = _login_retry_after(ip)
    if wait:
        return _too_many_attempts(wait)
    data = request.get_json(silent=True) or {}
    user = str(data.get("username", ""))[:256]
    pw   = str(data.get("password", ""))[:PASSWORD_MAX_LENGTH]
    account = find_local_user(user)
    account = account if account and account.get("enabled", True) else None
    # Mot de passe vérifié même si l'identifiant est faux : même durée de réponse
    pw_ok, rehash = verify_password(pw, (account or {}).get("password_hash") or _dummy_hash())
    if not (pw_ok and account):
        _login_failed(ip)
        wait = _login_retry_after(ip)
        fails = _login_failures.get(ip, {}).get("count", 0)
        if wait and (fails == LOGIN_FREE_ATTEMPTS or fails % 10 == 0):
            webhook_emit("login.locked", (f"Verrouillage de {ip}", f"{ip} locked out"),
                         (f"{fails} échecs de connexion consécutifs depuis {ip} : adresse verrouillée {wait} s.",
                          f"{fails} failed sign-ins in a row from {ip}: address locked for {wait} s."),
                         subject=ip, data={"ip": ip, "failures": fails, "locked_seconds": wait})
        if wait:
            return _too_many_attempts(wait)
        return jsonify({"ok": False, "msg": M("Identifiants incorrects", "Wrong username or password")}), 401
    _login_succeeded(ip)
    if rehash:
        # Ancien hash (SHA-256 sans sel, ou scrypt alors qu'argon2 est disponible)
        try:
            _set_password_hash(account["id"], hash_password(pw))
            print("[INFO] Mot de passe rehaché avec " + ("argon2id" if _ARGON2 else "scrypt"))
        except OSError as e:
            print(f"[WARN] Rehachage du mot de passe non enregistré : {e}")
        account = all_users()[account["id"]]
    _start_session(account["id"])
    _record_login(account["id"])
    webhook_emit("login.success", (f"Connexion de {user}", f"{user} signed in"),
                 (f"Connexion au panel depuis {ip}.", f"Panel sign-in from {ip}."),
                 subject=ip, data={"ip": ip, "user": user})
    return jsonify({"ok": True})

def _set_password_hash(uid, new_hash):
    """Enregistre un nouveau hash : celui du propriétaire (config) ou d'un compte."""
    if uid == OWNER_ID:
        _cfg_update(password_hash=new_hash)
    else:
        def mutate(users):
            if uid in users:
                users[uid]["password_hash"] = new_hash
        _users_update(mutate)

def _record_login(uid):
    """Date de dernière connexion d'un compte (les comptes OIDC la notent à part)."""
    if uid == OWNER_ID:
        return
    try:
        def mutate(users):
            if uid in users:
                users[uid]["last_login"] = datetime.now().astimezone().isoformat(timespec="seconds")
        _users_update(mutate)
    except OSError as e:
        print(f"[WARN] Dernière connexion non enregistrée : {e}")

@app.route("/api/logout", methods=["POST"])
def api_logout():
    session.clear()
    return jsonify({"ok": True})

# ── Configuration initiale ───────────────────────────────────────────────────
_setup_lock  = threading.Lock()
_USERNAME_RE = re.compile(r"[^\x00-\x1f\x7f]{1,64}")

def password_problem(pw):
    """Message si le mot de passe est refusé, sinon None."""
    if len(pw) < PASSWORD_MIN_LENGTH:
        return M(f"Le mot de passe doit faire au moins {PASSWORD_MIN_LENGTH} caractères.",
                 f"The password must be at least {PASSWORD_MIN_LENGTH} characters long.")
    if len(pw) > PASSWORD_MAX_LENGTH:
        return M(f"Le mot de passe ne doit pas dépasser {PASSWORD_MAX_LENGTH} caractères.",
                 f"The password must not exceed {PASSWORD_MAX_LENGTH} characters.")
    return None

@app.route("/setup", methods=["GET"])
def setup_page():
    if not needs_setup():
        return redirect(url_for("index"))
    return render_template("setup.html")

@app.route("/api/setup", methods=["POST"])
def api_setup():
    """Crée l'identifiant et le mot de passe, une seule fois : dès qu'un mot de
    passe existe, cette route refuse tout."""
    global MGR_CFG
    data = request.get_json(silent=True) or {}
    user = str(data.get("username", "")).strip()
    pw = str(data.get("password", ""))
    if not _USERNAME_RE.fullmatch(user):
        return jsonify({"ok": False, "msg": M("Identifiant invalide (1 à 64 caractères).",
                                              "Invalid username (1 to 64 characters).")}), 400
    problem = password_problem(pw)
    if problem:
        return jsonify({"ok": False, "msg": problem}), 400
    with _setup_lock:
        if not needs_setup():
            return jsonify({"ok": False, "msg": M("Le panel est déjà configuré.", "The panel is already set up.")}), 409
        cfg = {**MGR_CFG, "username": user, "password_hash": hash_password(pw)}
        save_manager_config(cfg)
        MGR_CFG = cfg
    _start_session()
    return jsonify({"ok": True})

def reset_password_cli():
    """python3 app.py --reset-password : nouveaux identifiants depuis la console
    (mot de passe perdu, ou hash illisible)."""
    import getpass
    current = MGR_CFG.get("username", "admin")
    user = input(M(f"Identifiant [{current}] : ", f"Username [{current}]: ")).strip() or current
    if not _USERNAME_RE.fullmatch(user):
        sys.exit(M("Identifiant invalide (1 à 64 caractères).", "Invalid username (1 to 64 characters)."))
    pw = getpass.getpass(M("Nouveau mot de passe : ", "New password: "))
    problem = password_problem(pw)
    if problem:
        sys.exit(problem)
    if getpass.getpass(M("Confirmer : ", "Confirm: ")) != pw:
        sys.exit(M("Les deux mots de passe sont différents.", "The two passwords don't match."))
    save_manager_config({**MGR_CFG, "username": user, "password_hash": hash_password(pw)})
    print(M("Identifiants enregistrés. Redémarrez frp-manager (systemctl restart frp-manager).",
            "Credentials saved. Restart frp-manager (systemctl restart frp-manager)."))

# ── Gestion des comptes ──────────────────────────────────────────────────────
_UID_RE   = re.compile(r"[0-9a-f]{12}")
_EMAIL_RE = re.compile(r"[^@\s]{1,64}@[^@\s]{1,255}")

def _now_iso():
    return datetime.now().astimezone().isoformat(timespec="seconds")

def _user_view(u):
    """Fiche d'un compte telle que l'interface la reçoit (jamais de hash)."""
    role = u.get("role", "viewer")
    ins = user_instances(u)
    return {
        "id": u["id"], "username": u["username"], "display_name": u.get("display_name", ""),
        "email": u.get("email", ""), "source": u["source"], "enabled": u.get("enabled", True),
        "owner": u["id"] == OWNER_ID, "role": role,
        "permissions": sorted(u.get("permissions") or []) if role == "custom" else [],
        "effective": sorted(user_permissions(u)),
        "instances": "*" if ins is None else sorted(ins),
        "last_login": u.get("last_login"), "created": u.get("created"),
        "linked": bool((u.get("oidc") or {}).get("sub")),
    }

def _me_view(u):
    v = _user_view(u)
    return {**{k: v[k] for k in ("id", "username", "display_name", "source", "owner", "role", "instances")},
            "permissions": v["effective"]}

def _parse_account_fields(raw, base):
    """Rôle, permissions et instances d'après la saisie `raw`, complétée par la
    fiche `base`. → (champs, message d'erreur)."""
    role = raw.get("role", base.get("role", "viewer"))
    if role not in USER_ROLES:
        return None, M("Rôle inconnu.", "Unknown role.")
    perms = raw.get("permissions", base.get("permissions") or [])
    if not isinstance(perms, list) or not all(isinstance(p, str) and p in PERMISSIONS for p in perms):
        return None, M("Permission inconnue.", "Unknown permission.")
    ins = raw.get("instances", base.get("instances", "*"))
    if ins != "*":
        if not (isinstance(ins, list) and len(ins) <= 200 and all(valid_instance_id(i) for i in ins)):
            return None, M("Liste d'instances invalide.", "Invalid instance list.")
        ins = sorted(set(ins))
    if role == "admin":
        ins = "*"
    return {"role": role, "permissions": sorted(set(perms)) if role == "custom" else [], "instances": ins}, None

def _grant_problem(actor, fields, target=None):
    """Un compte qui gère les utilisateurs ne peut ni accorder des droits qu'il n'a
    pas lui-même, ni modifier un compte plus puissant que lui (sinon
    « gérer les utilisateurs » reviendrait à être administrateur)."""
    if is_full_admin(actor):
        return None
    have = user_permissions(actor)
    if not user_permissions(fields) <= have:
        return M("Vous ne pouvez pas accorder des permissions que vous n'avez pas vous-même.",
                 "You can't grant permissions you don't have yourself.")
    if target is not None and not user_permissions(target) <= have:
        return M("Ce compte a plus de droits que le vôtre.", "This account has more rights than yours.")
    return None

def _name_taken(username, except_uid=None):
    return any(_same_name(username, u["username"]) for uid, u in all_users().items() if uid != except_uid)

def _clean_username(raw):
    name = str(raw or "").strip()
    return name if _USERNAME_RE.fullmatch(name) else None

def _clean_email(raw):
    email = str(raw or "").strip().lower()
    return email if len(email) <= 254 and _EMAIL_RE.fullmatch(email) else None

@app.route("/api/me")
@login_required
def api_me():
    return jsonify({"ok": True, "user": _me_view(current_user())})

@app.route("/api/me/password", methods=["POST"])
@login_required
def api_me_password():
    """Un compte local change son propre mot de passe (l'ancien est redemandé)."""
    user = current_user()
    if user["source"] != "local":
        return jsonify({"ok": False, "msg": M("Ce compte se connecte par OpenID Connect : pas de mot de passe ici.",
                                              "This account signs in with OpenID Connect: no password here.")}), 400
    ip = request.remote_addr or "?"
    wait = _login_retry_after(ip)
    if wait:
        return _too_many_attempts(wait)
    data = request.get_json(silent=True) or {}
    ok, _ = verify_password(str(data.get("current_password", ""))[:PASSWORD_MAX_LENGTH], user.get("password_hash"))
    if not ok:
        _login_failed(ip)
        return jsonify({"ok": False, "msg": M("Mot de passe actuel incorrect.", "Wrong current password.")}), 403
    new = str(data.get("new_password", ""))
    problem = password_problem(new)
    if problem:
        return jsonify({"ok": False, "msg": problem}), 400
    _login_succeeded(ip)
    _set_password_hash(user["id"], hash_password(new))
    _start_session(user["id"])       # cette session reste ouverte, les autres sont déconnectées
    return jsonify({"ok": True, "msg": M("Mot de passe modifié.", "Password changed.")})

@app.route("/api/users", methods=["GET"])
@permission_required("users.manage")
def api_users_list():
    detect_frp(force=False)
    users = sorted(all_users().values(), key=lambda u: (u["id"] != OWNER_ID, u["username"].casefold()))
    return jsonify({
        "ok": True, "me": current_user()["id"], "users": [_user_view(u) for u in users],
        "permissions": [{"id": k, "scoped": v[0], "sensitive": v[1]} for k, v in PERMISSIONS.items()],
        "implies": {k: list(v) for k, v in _PERMISSION_IMPLIES.items()},
        "roles": {k: sorted(expand_permissions(v)) for k, v in ROLES.items()},
        "instances": sorted(INSTANCES), "oidc": oidc_enabled(),
        "can_configure_oidc": is_full_admin(current_user()),
    })

@app.route("/api/users", methods=["POST"])
@permission_required("users.manage")
def api_users_create():
    actor = current_user()
    raw = request.get_json(silent=True) or {}
    source = raw.get("source", "local")
    if source not in ("local", "oidc"):
        return jsonify({"ok": False, "msg": M("Type de compte inconnu.", "Unknown account type.")}), 400
    username = _clean_username(raw.get("username"))
    if not username:
        return jsonify({"ok": False, "msg": M("Identifiant invalide (1 à 64 caractères).",
                                              "Invalid username (1 to 64 characters).")}), 400
    fields, err = _parse_account_fields(raw, {})
    if err:
        return jsonify({"ok": False, "msg": err}), 400
    problem = _grant_problem(actor, fields)
    if problem:
        return _forbidden(problem)
    rec = {"username": username, "source": source, "enabled": bool(raw.get("enabled", True)),
           "created": _now_iso(), **fields}
    if source == "local":
        pw = str(raw.get("password", ""))
        problem = password_problem(pw)
        if problem:
            return jsonify({"ok": False, "msg": problem}), 400
        rec["password_hash"] = hash_password(pw)
    else:
        email = _clean_email(raw.get("email"))
        if not email:
            return jsonify({"ok": False, "msg": M("Adresse e-mail requise : le compte est lié à la première connexion "
                                                  "OpenID Connect dont l'e-mail vérifié est celui-ci.",
                                                  "An email address is required: the account is linked on the first "
                                                  "OpenID Connect sign-in whose verified email matches.")}), 400
        rec["email"] = email
    uid = secrets.token_hex(6)
    with _cfg_lock:
        if _name_taken(username):
            return jsonify({"ok": False, "msg": M("Cet identifiant est déjà pris.", "This username is already taken.")}), 409
        if source == "oidc" and any(u.get("email") == rec["email"] and u["source"] == "oidc" for u in all_users().values()):
            return jsonify({"ok": False, "msg": M("Un compte OpenID Connect utilise déjà cet e-mail.",
                                                  "An OpenID Connect account already uses this email.")}), 409
        if len(MGR_CFG.get("users") or {}) >= USERS_MAX:
            return jsonify({"ok": False, "msg": M(f"{USERS_MAX} comptes au plus.", f"{USERS_MAX} accounts at most.")}), 400
        _users_update(lambda users: users.__setitem__(uid, rec))
    return jsonify({"ok": True, "user": _user_view(all_users()[uid]),
                    "msg": M("Compte créé.", "Account created.")})

def _managed_target(uid):
    """(fiche, None) du compte visé par une modification, ou (None, réponse d'erreur)."""
    if uid == OWNER_ID:
        return None, (jsonify({"ok": False, "msg": M("Le compte propriétaire se gère dans Réglages.",
                                                      "The owner account is managed in Settings.")}), 400)
    target = all_users().get(uid) if _UID_RE.fullmatch(uid) else None
    if not target:
        return None, (jsonify({"ok": False, "msg": M("Compte inconnu.", "Unknown account.")}), 404)
    if uid == current_user()["id"]:
        return None, (jsonify({"ok": False, "msg": M("Vous ne pouvez pas modifier votre propre compte ici : "
                                                      "demandez à un autre administrateur.",
                                                      "You can't change your own account here: ask another administrator.")}), 400)
    return target, None

@app.route("/api/users/<uid>", methods=["PUT"])
@permission_required("users.manage")
def api_users_update(uid):
    actor = current_user()
    target, err = _managed_target(uid)
    if err:
        return err
    raw = request.get_json(silent=True) or {}
    fields, msg = _parse_account_fields(raw, target)
    if msg:
        return jsonify({"ok": False, "msg": msg}), 400
    problem = _grant_problem(actor, fields, target)
    if problem:
        return _forbidden(problem)
    patch = dict(fields)
    if "enabled" in raw:
        patch["enabled"] = bool(raw["enabled"])
    if "username" in raw:
        username = _clean_username(raw["username"])
        if not username:
            return jsonify({"ok": False, "msg": M("Identifiant invalide (1 à 64 caractères).",
                                                  "Invalid username (1 to 64 characters).")}), 400
        patch["username"] = username
    if "email" in raw and target["source"] == "oidc" and not (target.get("oidc") or {}).get("sub"):
        email = _clean_email(raw["email"])
        if not email:
            return jsonify({"ok": False, "msg": M("Adresse e-mail invalide.", "Invalid email address.")}), 400
        patch["email"] = email
    if raw.get("password"):
        if target["source"] != "local":
            return jsonify({"ok": False, "msg": M("Pas de mot de passe sur un compte OpenID Connect.",
                                                  "No password on an OpenID Connect account.")}), 400
        problem = password_problem(str(raw["password"]))
        if problem:
            return jsonify({"ok": False, "msg": problem}), 400
        patch["password_hash"] = hash_password(str(raw["password"]))
    with _cfg_lock:
        if "username" in patch and _name_taken(patch["username"], except_uid=uid):
            return jsonify({"ok": False, "msg": M("Cet identifiant est déjà pris.", "This username is already taken.")}), 409
        _users_update(lambda users: users[uid].update(patch))
    return jsonify({"ok": True, "user": _user_view(all_users()[uid]), "msg": M("Compte modifié.", "Account updated.")})

@app.route("/api/users/<uid>", methods=["DELETE"])
@permission_required("users.manage")
def api_users_delete(uid):
    target, err = _managed_target(uid)
    if err:
        return err
    problem = _grant_problem(current_user(), target, target)
    if problem:
        return _forbidden(problem)
    _users_update(lambda users: users.pop(uid, None))
    return jsonify({"ok": True, "msg": M("Compte supprimé.", "Account deleted.")})

# ── OpenID Connect (connexion unique) ────────────────────────────────────────
# Flux « authorization code » avec PKCE (S256), state et nonce. L'état du flux
# tient dans un cookie signé SameSite=Lax, à part : le cookie de session est
# Strict, donc absent du retour du fournisseur (navigation venue d'un autre site).
# Le jeton d'identité est reçu directement du point d'accès /token en HTTPS : la
# spécification (OIDC Core §3.1.3.7) permet alors de se fier à TLS plutôt qu'à la
# signature du jeton. On en contrôle quand même émetteur, audience, expiration
# et nonce.
OIDC_FLOW_COOKIE  = "frpm_oidc"
OIDC_FLOW_MAX_AGE = 600
OIDC_ERRORS = ("disabled", "state", "denied", "provider", "token", "claims",
               "no_account", "disabled_user", "domain")
OIDC_DEFAULTS = {
    "enabled": False, "issuer": "", "client_id": "", "client_secret": "",
    "scopes": "openid profile email", "label": "", "redirect_uri": "",
    "username_claim": "preferred_username",
    "auto_create": False, "default_role": "viewer", "allowed_domains": [],
    "groups_claim": "groups", "group_roles": {},
}
_OIDC_CLAIM_RE = re.compile(r"[A-Za-z0-9_.:\-/]{1,128}")
_OIDC_SCOPES_RE = re.compile(r"[A-Za-z0-9_.:\-/]+(?: [A-Za-z0-9_.:\-/]+)*")
_DOMAIN_RE = re.compile(r"(?:[a-z0-9-]{1,63}\.)+[a-z]{2,63}")
_oidc_meta = {}                 # émetteur → (expire, document de découverte)

class OidcError(Exception):
    """code : une des OIDC_ERRORS (affichée sur la page de connexion) ; le détail
    ne va que dans le journal."""
    def __init__(self, code, detail=""):
        super().__init__(detail or code)
        self.code, self.detail = code, detail or code

def oidc_config():
    return {**OIDC_DEFAULTS, **(MGR_CFG.get("oidc") or {})}

def oidc_enabled():
    c = oidc_config()
    return bool(c["enabled"] and c["issuer"] and c["client_id"])

def _oidc_url_ok(url):
    """HTTPS, ou HTTP vers la machine locale (essais avec un fournisseur sur le poste)."""
    try:
        u = urlsplit(str(url))
        return bool(u.hostname) and (u.scheme == "https" or (u.scheme == "http" and u.hostname in ("localhost", "127.0.0.1", "::1")))
    except ValueError:
        return False

def oidc_discover(issuer, fresh=False):
    issuer = issuer.rstrip("/")
    hit = _oidc_meta.get(issuer)
    if hit and hit[0] > time.time() and not fresh:
        return hit[1]
    try:
        r = req.get(issuer + "/.well-known/openid-configuration", timeout=8, headers={"Accept": "application/json"})
        r.raise_for_status()
        meta = r.json()
    except (req.RequestException, ValueError) as e:
        raise OidcError("provider", f"découverte impossible : {str(e)[:160]}")
    if not isinstance(meta, dict) or str(meta.get("issuer", "")).rstrip("/") != issuer:
        raise OidcError("provider", "l'émetteur annoncé ne correspond pas à l'adresse configurée")
    for key in ("authorization_endpoint", "token_endpoint"):
        if not _oidc_url_ok(meta.get(key)):
            raise OidcError("provider", f"{key} absent ou non sécurisé")
    if not _oidc_url_ok(meta.get("userinfo_endpoint")):
        meta.pop("userinfo_endpoint", None)
    _oidc_meta[issuer] = (time.time() + 3600, meta)
    return meta

def oidc_redirect_uri(cfg=None):
    return (cfg or oidc_config()).get("redirect_uri") or url_for("oidc_callback", _external=True)

def _oidc_flow_serializer():
    return URLSafeTimedSerializer(app.secret_key, salt="frpm-oidc-flow")

def _oidc_clear_cookie(resp):
    resp.delete_cookie(OIDC_FLOW_COOKIE, path="/auth/oidc")
    resp.headers["Cache-Control"] = "no-store"
    return resp

def _oidc_fail(code):
    return _oidc_clear_cookie(redirect(url_for("login_page", error=code)))

def _b64url(data):
    return base64.urlsafe_b64encode(data).rstrip(b"=").decode()

@app.route("/auth/oidc/login")
def oidc_login():
    if not oidc_enabled():
        return _oidc_fail("disabled")
    cfg = oidc_config()
    try:
        meta = oidc_discover(cfg["issuer"])
    except OidcError as e:
        print(f"[WARN] Connexion OIDC : {e.detail}")
        return _oidc_fail("provider")
    flow = {"s": secrets.token_urlsafe(24), "n": secrets.token_urlsafe(24),
            "cv": secrets.token_urlsafe(48), "ru": oidc_redirect_uri(cfg)}
    scopes = cfg["scopes"] if _OIDC_SCOPES_RE.fullmatch(cfg["scopes"] or "") else OIDC_DEFAULTS["scopes"]
    if "openid" not in scopes.split():
        scopes = "openid " + scopes
    params = {"response_type": "code", "client_id": cfg["client_id"], "redirect_uri": flow["ru"],
              "scope": scopes, "state": flow["s"], "nonce": flow["n"],
              "code_challenge": _b64url(hashlib.sha256(flow["cv"].encode()).digest()),
              "code_challenge_method": "S256"}
    base = meta["authorization_endpoint"]
    resp = redirect(base + ("&" if "?" in base else "?") + urlencode(params))
    resp.set_cookie(OIDC_FLOW_COOKIE, _oidc_flow_serializer().dumps(flow), max_age=OIDC_FLOW_MAX_AGE,
                    httponly=True, samesite="Lax", secure=bool(app.config["SESSION_COOKIE_SECURE"]),
                    path="/auth/oidc")
    resp.headers["Cache-Control"] = "no-store"
    return resp

def _jwt_claims(token):
    """Contenu (sans vérification de signature : voir plus haut) d'un jeton JWT."""
    try:
        head, payload, _sig = token.split(".")
        claims = json.loads(base64.urlsafe_b64decode(payload + "=" * (-len(payload) % 4)))
    except (ValueError, TypeError):
        raise OidcError("claims", "jeton d'identité illisible")
    if not isinstance(claims, dict):
        raise OidcError("claims", "jeton d'identité illisible")
    return claims

def oidc_exchange(cfg, meta, code, flow):
    """Échange le code contre les jetons et renvoie les revendications de l'utilisateur."""
    data = {"grant_type": "authorization_code", "code": code, "redirect_uri": flow["ru"],
            "code_verifier": flow["cv"], "client_id": cfg["client_id"]}
    auth, secret = None, cfg["client_secret"]
    if secret:
        methods = meta.get("token_endpoint_auth_methods_supported") or ["client_secret_basic"]
        if "client_secret_basic" in methods or "client_secret_post" not in methods:
            auth = (quote_plus(cfg["client_id"]), quote_plus(secret))    # RFC 6749 §2.3.1
        else:
            data["client_secret"] = secret
    try:
        r = req.post(meta["token_endpoint"], data=data, auth=auth, timeout=10, headers={"Accept": "application/json"})
        tok = r.json()
    except (req.RequestException, ValueError) as e:
        raise OidcError("token", f"échange du code impossible : {str(e)[:160]}")
    if r.status_code != 200 or not isinstance(tok, dict) or not isinstance(tok.get("id_token"), str):
        raise OidcError("token", f"HTTP {r.status_code} {str(tok.get('error') if isinstance(tok, dict) else '')[:80]}")
    claims = _jwt_claims(tok["id_token"])
    if str(claims.get("iss", "")).rstrip("/") != cfg["issuer"].rstrip("/"):
        raise OidcError("claims", "émetteur du jeton inattendu")
    aud = claims.get("aud")
    auds = aud if isinstance(aud, list) else [aud]
    if cfg["client_id"] not in auds or (len(auds) > 1 and claims.get("azp") != cfg["client_id"]):
        raise OidcError("claims", "audience du jeton inattendue")
    exp = claims.get("exp")
    if not isinstance(exp, (int, float)) or exp < time.time() - 60:
        raise OidcError("claims", "jeton expiré")
    if not hmac.compare_digest(str(claims.get("nonce", "")).encode(), flow["n"].encode()):
        raise OidcError("claims", "nonce différent")
    sub = claims.get("sub")
    if not isinstance(sub, str) or not 0 < len(sub) <= 255:
        raise OidcError("claims", "sujet absent")
    # E-mail, groupes… : parfois seulement dans userinfo
    if meta.get("userinfo_endpoint") and isinstance(tok.get("access_token"), str):
        try:
            ui = req.get(meta["userinfo_endpoint"], timeout=8,
                         headers={"Authorization": "Bearer " + tok["access_token"], "Accept": "application/json"})
            info = ui.json() if ui.status_code == 200 else None
        except (req.RequestException, ValueError):
            info = None
        if isinstance(info, dict) and info.get("sub") == sub:
            claims = {**info, **claims}
    return claims

def _oidc_groups(cfg, claims):
    raw = claims.get(cfg["groups_claim"])
    raw = [raw] if isinstance(raw, str) else raw if isinstance(raw, list) else []
    return [g_ for g_ in raw if isinstance(g_, str)]

def oidc_resolve_user(cfg, claims):
    """Compte local correspondant à l'identité : déjà lié, préparé à l'avance (e-mail
    vérifié identique), ou créé si auto_create. → fiche ; OidcError sinon."""
    iss, sub = cfg["issuer"].rstrip("/"), claims["sub"]
    email = str(claims.get("email") or "").strip().lower()
    email = email if _clean_email(email) and claims.get("email_verified") in (True, "true") else ""
    mapping = cfg["group_roles"] if isinstance(cfg["group_roles"], dict) else {}
    role_by_group = None
    if mapping:
        hits = {mapping[g_] for g_ in _oidc_groups(cfg, claims) if g_ in mapping}
        role_by_group = next((r for r in ("admin", "operator", "viewer") if r in hits), cfg["default_role"])
    with _cfg_lock:
        users = {k: dict(v) for k, v in (MGR_CFG.get("users") or {}).items()}
        uid = next((k for k, u in users.items() if u.get("source") == "oidc"
                    and (u.get("oidc") or {}).get("sub") == sub and (u.get("oidc") or {}).get("iss") == iss), None)
        if uid is None and email:
            pending = [k for k, u in users.items() if u.get("source") == "oidc" and not (u.get("oidc") or {}).get("sub")
                       and u.get("email") == email]
            uid = pending[0] if len(pending) == 1 else None
        if uid is None:
            if not cfg["auto_create"]:
                raise OidcError("no_account", f"aucun compte pour {sub} ({email or 'e-mail non vérifié'})")
            domains = [d.lower() for d in cfg["allowed_domains"]]
            if domains and email.rpartition("@")[2] not in domains:
                raise OidcError("domain", f"domaine refusé ({email or 'e-mail non vérifié'})")
            if len(users) >= USERS_MAX:
                raise OidcError("no_account", "trop de comptes")
            base = re.sub(r"[\x00-\x1f\x7f]", "", str(claims.get(cfg["username_claim"]) or email or sub)).strip()[:60] or "user"
            name, n = base, 2
            while _name_taken(name):
                name, n = f"{base}-{n}", n + 1
            uid = secrets.token_hex(6)
            users[uid] = {"username": name, "source": "oidc", "enabled": True, "created": _now_iso(),
                          "role": role_by_group or cfg["default_role"], "permissions": [], "instances": "*"}
        u = users[uid]
        if not u.get("enabled", True):
            raise OidcError("disabled_user", f"compte {u.get('username')} désactivé")
        u["oidc"] = {"iss": iss, "sub": sub}
        if email:
            u["email"] = email
        if isinstance(claims.get("name"), str):
            u["display_name"] = claims["name"][:128]
        if role_by_group and u.get("role") != role_by_group:
            u.update(role=role_by_group, permissions=[], instances="*" if role_by_group == "admin" else u.get("instances", "*"))
        u["last_login"] = _now_iso()
        _cfg_update(users=users)
    return all_users()[uid]

@app.route("/auth/oidc/callback")
def oidc_callback():
    try:
        flow = _oidc_flow_serializer().loads(request.cookies.get(OIDC_FLOW_COOKIE, ""), max_age=OIDC_FLOW_MAX_AGE)
        if not isinstance(flow, dict) or not all(isinstance(flow.get(k), str) for k in ("s", "n", "cv", "ru")):
            raise BadData("flux invalide")
    except BadData:
        return _oidc_fail("state")
    if not oidc_enabled():
        return _oidc_fail("disabled")
    if request.args.get("error"):
        print(f"[WARN] Connexion OIDC refusée par le fournisseur : {request.args['error'][:80]}")
        return _oidc_fail("denied")
    if not hmac.compare_digest(request.args.get("state", "").encode(), flow["s"].encode()):
        return _oidc_fail("state")
    code = request.args.get("code", "")
    if not code or len(code) > 4096:
        return _oidc_fail("denied")
    cfg = oidc_config()
    try:
        claims = oidc_exchange(cfg, oidc_discover(cfg["issuer"]), code, flow)
        user = oidc_resolve_user(cfg, claims)
    except OidcError as e:
        print(f"[WARN] Connexion OIDC refusée : {e.detail}")
        return _oidc_fail(e.code)
    _start_session(user["id"])
    ip = request.remote_addr or "?"
    webhook_emit("login.success", (f"Connexion de {user['username']}", f"{user['username']} signed in"),
                 (f"Connexion au panel (OpenID Connect) depuis {ip}.", f"Panel sign-in (OpenID Connect) from {ip}."),
                 subject=ip, data={"ip": ip, "user": user["username"], "method": "oidc"})
    # Page intermédiaire plutôt qu'une redirection : le cookie de session est
    # SameSite=Strict, et le navigateur ne l'enverrait pas à une redirection
    # qui prolonge une navigation venue du fournisseur.
    resp = Response('<!doctype html><meta charset="utf-8"><meta http-equiv="refresh" content="0;url=/">'
                    '<title>FRP Manager</title><a href="/">FRP Manager</a>', mimetype="text/html")
    return _oidc_clear_cookie(resp)

def _oidc_public(cfg):
    """Réglages renvoyés à l'interface : jamais le secret client."""
    return {**{k: v for k, v in cfg.items() if k != "client_secret"},
            "has_secret": bool(cfg["client_secret"])}

def _oidc_clean(raw, old):
    """→ (réglages, message d'erreur)."""
    def err(fr, en):
        return None, M(fr, en)
    cfg = dict(old)
    cfg["enabled"] = bool(raw.get("enabled", old["enabled"]))
    if "issuer" in raw:
        cfg["issuer"] = str(raw["issuer"]).strip().rstrip("/")
        if cfg["issuer"] and (not _oidc_url_ok(cfg["issuer"]) or len(cfg["issuer"]) > 512):
            return err("Adresse de l'émetteur invalide (https:// attendu).", "Invalid issuer address (https:// expected).")
    if "client_id" in raw:
        cfg["client_id"] = str(raw["client_id"]).strip()[:256]
    if raw.get("client_secret"):
        cfg["client_secret"] = str(raw["client_secret"]).strip()[:1024]
    elif raw.get("clear_secret"):
        cfg["client_secret"] = ""
    if "scopes" in raw:
        cfg["scopes"] = " ".join(str(raw["scopes"]).split()) or OIDC_DEFAULTS["scopes"]
        if not _OIDC_SCOPES_RE.fullmatch(cfg["scopes"]):
            return err("Portées invalides.", "Invalid scopes.")
    if "label" in raw:
        cfg["label"] = re.sub(r"[\x00-\x1f\x7f]", "", str(raw["label"])).strip()[:40]
    if "redirect_uri" in raw:
        cfg["redirect_uri"] = str(raw["redirect_uri"]).strip()
        if cfg["redirect_uri"] and not _oidc_url_ok(cfg["redirect_uri"]):
            return err("Adresse de retour invalide (https:// attendu).", "Invalid redirect address (https:// expected).")
    for key in ("username_claim", "groups_claim"):
        if key in raw:
            cfg[key] = str(raw[key]).strip()
            if not _OIDC_CLAIM_RE.fullmatch(cfg[key]):
                return err("Nom de revendication invalide.", "Invalid claim name.")
    if "auto_create" in raw:
        cfg["auto_create"] = bool(raw["auto_create"])
    if "default_role" in raw:
        # Jamais admin par défaut : tout compte du fournisseur le deviendrait
        if raw["default_role"] not in ("operator", "viewer"):
            return err("Rôle par défaut : opérateur ou lecteur.", "Default role: operator or viewer.")
        cfg["default_role"] = raw["default_role"]
    if "allowed_domains" in raw:
        doms = raw["allowed_domains"]
        doms = [d.strip().lower() for d in doms.replace(",", " ").split()] if isinstance(doms, str) else doms
        if not (isinstance(doms, list) and len(doms) <= 20 and all(isinstance(d, str) and _DOMAIN_RE.fullmatch(d) for d in doms)):
            return err("Domaines autorisés invalides.", "Invalid allowed domains.")
        cfg["allowed_domains"] = sorted(set(doms))
    if "group_roles" in raw:
        gr = raw["group_roles"]
        if not (isinstance(gr, dict) and len(gr) <= 50 and all(
                isinstance(k, str) and 0 < len(k) <= 256 and v in ROLES for k, v in gr.items())):
            return err("Correspondance groupes → rôles invalide.", "Invalid group → role mapping.")
        cfg["group_roles"] = gr
    if cfg["enabled"] and not (cfg["issuer"] and cfg["client_id"]):
        return err("L'émetteur et l'identifiant client sont requis pour activer la connexion.",
                   "The issuer and client ID are required to enable sign-in.")
    return cfg, None

@app.route("/api/oidc", methods=["GET"])
@permission_required("users.manage")
def api_oidc_get():
    if not is_full_admin(current_user()):
        return _forbidden()
    return jsonify({"ok": True, "oidc": _oidc_public(oidc_config()),
                    "redirect_uri": url_for("oidc_callback", _external=True)})

@app.route("/api/oidc", methods=["POST"])
@permission_required("users.manage")
def api_oidc_save():
    if not is_full_admin(current_user()):
        return _forbidden()
    cfg, err = _oidc_clean(request.get_json(silent=True) or {}, oidc_config())
    if err:
        return jsonify({"ok": False, "msg": err}), 400
    _cfg_update(oidc=cfg)
    _oidc_meta.clear()
    return jsonify({"ok": True, "oidc": _oidc_public(cfg), "msg": M("Connexion unique enregistrée.", "Single sign-on saved.")})

@app.route("/api/oidc/test", methods=["POST"])
@permission_required("users.manage")
def api_oidc_test():
    """Interroge le document de découverte de l'émetteur saisi (sans rien enregistrer)."""
    if not is_full_admin(current_user()):
        return _forbidden()
    issuer = str((request.get_json(silent=True) or {}).get("issuer") or oidc_config()["issuer"]).strip()
    if not _oidc_url_ok(issuer):
        return jsonify({"ok": False, "msg": M("Adresse de l'émetteur invalide (https:// attendu).",
                                              "Invalid issuer address (https:// expected).")}), 400
    try:
        meta = oidc_discover(issuer, fresh=True)
    except OidcError as e:
        return jsonify({"ok": False, "msg": M(f"Échec : {e.detail}", f"Failed: {e.detail}")})
    return jsonify({"ok": True, "msg": M("Fournisseur joignable.", "Provider reachable."),
                    "endpoints": {k: meta.get(k) for k in ("authorization_endpoint", "token_endpoint", "userinfo_endpoint")}})

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

def mirrors_allowed():
    """Miroirs tiers autorisés ? (Réglages ; FRP_MANAGER_NO_MIRRORS=1 les interdit)"""
    if os.environ.get("FRP_MANAGER_NO_MIRRORS", "") in ("1", "true", "yes"):
        return False
    return MGR_CFG.get("download_mirrors", True) is not False

def build_download_mirrors(tag, filename):
    tpls = FALLBACK_DOWNLOAD_MIRRORS if mirrors_allowed() else [FRP_RELEASE_DOWNLOAD]
    return [tpl.format(tag=tag, filename=filename) for tpl in tpls]

def is_third_party(url):
    return not url.startswith("https://github.com/")

# ── Docker ──────────────────────────────────────────────────────────────────
# Détection Docker : /.dockerenv est créé par Docker dans chaque container
_IN_DOCKER = Path("/.dockerenv").exists()

# ── Validation des noms ───────────────────────────────────────────────────────
# Tout nom qui finit dans une commande (systemctl, journalctl) ou dans une
# adresse de l'API Docker passe par ces listes blanches. Les commandes sont
# toujours des listes d'arguments, jamais une chaîne passée à un shell.
_SERVICE_NAME_RE   = re.compile(r"frp[sc][A-Za-z0-9_.@-]{0,60}")
_CONTAINER_NAME_RE = re.compile(r"[A-Za-z0-9][A-Za-z0-9_.-]{0,127}")
_INSTANCE_ID_RE    = re.compile(r"(?:docker_)?[A-Za-z0-9][A-Za-z0-9_.@-]{0,127}")

def valid_service_name(name):
    """Unité systemd frps*/frpc* (sans .service) : frps, frpc2, frpc-maison…"""
    return isinstance(name, str) and bool(_SERVICE_NAME_RE.fullmatch(name))

def valid_container_name(name):
    """Nom (ou identifiant) de conteneur Docker, selon les règles de Docker."""
    return isinstance(name, str) and bool(_CONTAINER_NAME_RE.fullmatch(name))

def valid_instance_id(iid):
    return isinstance(iid, str) and bool(_INSTANCE_ID_RE.fullmatch(iid))

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
    ok, _, _ = run_host(["tee", "--", str(path)], input_text=content)
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
    if not valid_service_name(name):
        return False, M(f"Nom de service refusé : {name!r}", f"Service name rejected: {name!r}")
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
        except ValueError: data = raw.decode(errors="replace")
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
        self.lang = _lang()       # lines() tourne dans un autre thread, hors requête
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
        _tls.lang = self.lang
        try:
            if self.inst.get("source") == "docker":
                yield from self._docker_lines(_resolve_container(self.inst["container_name"]))
            elif self.source == "file":
                path = self.inst.get("log")
                if not path:
                    yield M("[frp-manager] aucun fichier de log connu pour cette instance",
                          "[frp-manager] no known log file for this instance")
                    return
                # -F : suit le fichier même après une rotation des logs
                yield from self._command_lines(["tail", f"-n{self.history}", "-F", str(path)])
            else:
                if not valid_service_name(self.inst.get("service")):
                    yield M("[frp-manager] nom de service refusé", "[frp-manager] service name rejected")
                    return
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
            yield M("[frp-manager] socket Docker indisponible", "[frp-manager] Docker socket unavailable")
            return
        self._conn = _UnixHTTPConn(str(_DOCKER_SOCK))
        self._conn.request("GET",
            f"/v1.41/containers/{container}/logs?stdout=1&stderr=1&follow=1&tail={self.history}")
        resp = self._conn.getresponse()
        if resp.status != 200:
            yield M(f"[frp-manager] logs Docker indisponibles (HTTP {resp.status})",
                    f"[frp-manager] Docker logs unavailable (HTTP {resp.status})")
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
        if not valid_container_name(name):
            continue
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

# ── GitHub : l'API, et le site en repli ───────────────────────────────────────
# Sans compte, l'API n'accepte que 60 appels par heure et par adresse IP (partagée
# par toutes les machines d'un même réseau). Une fois ce quota épuisé, on ne la
# sollicite plus avant sa remise à zéro et on lit les mêmes informations sur le
# site : redirection de /releases/latest et flux releases.atom.
_gh_api_blocked_until = 0

def gh_api(path, timeout=10):
    """GET sur l'API GitHub (chemin ou URL complète) → JSON."""
    global _gh_api_blocked_until
    if time.time() < _gh_api_blocked_until:
        raise RuntimeError(M("quota de l'API GitHub épuisé, repli sur github.com",
                             "GitHub API quota exhausted, falling back to github.com"))
    url = path if path.startswith("http") else f"https://api.github.com/{path}"
    r = req.get(url, timeout=timeout, headers={"Accept": "application/vnd.github.v3+json"})
    if r.status_code in (403, 429) and r.headers.get("X-RateLimit-Remaining") == "0":
        try:
            _gh_api_blocked_until = int(r.headers.get("X-RateLimit-Reset"))
        except (TypeError, ValueError):
            _gh_api_blocked_until = time.time() + 3600
    r.raise_for_status()
    return r.json()

def gh_web_latest_tag(repo, timeout=10):
    """Tag de la release « latest » d'un dépôt, lu sur le site (sans l'API)."""
    r = req.head(f"https://github.com/{repo}/releases/latest", timeout=timeout, allow_redirects=False)
    m = re.search(r"/releases/tag/([^/?#]+)$", r.headers.get("Location", ""))
    if not m:
        raise RuntimeError(M(f"github.com : pas de release « latest » pour {repo} (HTTP {r.status_code})",
                             f"github.com: no \"latest\" release for {repo} (HTTP {r.status_code})"))
    return m.group(1)

def gh_web_tags(repo, timeout=10):
    """Tags des releases publiées (pré-releases comprises), du flux releases.atom."""
    r = req.get(f"https://github.com/{repo}/releases.atom", timeout=timeout)
    r.raise_for_status()
    return list(dict.fromkeys(re.findall(r"/releases/tag/([^\"'<>\s]+)", r.text)))

def fetch_version_source(url, timeout=12):
    """Tag publié par une source de version : API (JSON) ou page /releases/latest du site."""
    m = re.match(r"https://github\.com/([^/]+/[^/]+)/releases/latest$", url)
    if m:
        return gh_web_latest_tag(m.group(1), timeout)
    data = gh_api(url, timeout)
    return data.get("tag_name") or data.get("tag")

def fetch_latest_version():
    for name, url in build_version_sources():
        try:
            tag = fetch_version_source(url)
            # Le tag finit dans des adresses de téléchargement : format strict
            if tag and re.fullmatch(r"v\d+\.\d+\.\d+", tag):
                return tag.lstrip("v"), tag, name
        except Exception:
            continue
    return None, None, M("toutes les sources inaccessibles", "all sources unreachable")

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
    try:
        if not include_prereleases:
            return gh_api(PANEL_GITHUB_API)
        releases = gh_api(f"repos/{PANEL_GITHUB_REPO}/releases?per_page=30")
        candidates = [rel for rel in releases
                      if not rel.get("draft") and _version_key(rel.get("tag_name", ""))]
        return max(candidates, key=lambda rel: _version_key(rel["tag_name"]), default=None)
    except Exception:
        # API indisponible (quota) : même choix, d'après le site
        latest = gh_web_latest_tag(PANEL_GITHUB_REPO)
        tag = latest
        if include_prereleases:
            tags = [t for t in gh_web_tags(PANEL_GITHUB_REPO) if _version_key(t)] + [latest]
            tag = max(tags, key=_version_key)
        return _panel_web_release(tag, latest)

def _panel_web_release(tag, latest):
    """Release du panel reconstituée sans l'API : l'archive publiée par la CI porte
    toujours le nom frp-manager.zip ; une version plus récente que « latest » est
    une pré-release."""
    return {
        "tag_name": tag,
        "html_url": f"https://github.com/{PANEL_GITHUB_REPO}/releases/tag/{tag}",
        "prerelease": version_newer(tag, latest),
        "assets": [{"name": name,
                    "browser_download_url": f"https://github.com/{PANEL_GITHUB_REPO}/releases/download/{tag}/{name}"}
                   for name in ("frp-manager.zip", PANEL_CHECKSUM_ASSET)],
    }

# Somme SHA-256 de frp-manager.zip, jointe aux releases depuis 0.0.51
PANEL_CHECKSUM_ASSET = "frp-manager.zip.sha256"
PANEL_ZIP_MAX_BYTES    = 50 * 1024 * 1024
PANEL_ZIP_MAX_UNPACKED = 100 * 1024 * 1024
PANEL_ZIP_MAX_MEMBERS  = 2000

def check_panel_zip(zf):
    """ValueError si l'archive du panel est suspecte : chemins absolus ou avec
    « .. », liens symboliques, trop d'entrées ou trop gros une fois décompressé."""
    infos = zf.infolist()
    if len(infos) > PANEL_ZIP_MAX_MEMBERS:
        raise ValueError(M("archive refusée : trop d'entrées", "archive rejected: too many entries"))
    total = 0
    for info in infos:
        name = info.filename.replace("\\", "/")
        if name.startswith("/") or re.match(r"^[A-Za-z]:", name) or ".." in PurePosixPath(name).parts:
            raise ValueError(M(f"archive refusée : chemin suspect « {info.filename} »",
                             f"archive rejected: suspicious path “{info.filename}”"))
        if (info.external_attr >> 16) & 0o170000 == 0o120000:
            raise ValueError(M(f"archive refusée : lien symbolique « {info.filename} »",
                             f"archive rejected: symbolic link “{info.filename}”"))
        total += info.file_size
        if total > PANEL_ZIP_MAX_UNPACKED:
            raise ValueError(M("archive refusée : trop volumineuse une fois décompressée",
                                   "archive rejected: too large once decompressed"))

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

DOWNLOAD_MAX_BYTES = 100 * 1024 * 1024       # archive frp : ~15 Mo

def parse_checksums(text):
    """Fichier au format sha256sum → {nom de fichier: empreinte hexadécimale}."""
    sums = {}
    for line in (text or "").splitlines():
        m = re.fullmatch(r"\s*([0-9a-fA-F]{64})\s+\*?(\S+)\s*", line)
        if m:
            sums[m.group(2)] = m.group(1).lower()
    return sums

def fetch_frp_checksums(tag, log_fn):
    """Sommes SHA-256 de la release → (dict, vient de github.com). github.com
    d'abord ; un miroir tiers seulement s'ils sont autorisés (vérification alors
    affaiblie : le même intermédiaire pourrait fournir archive et somme)."""
    for url in build_download_mirrors(tag, FRP_CHECKSUMS_FILE):
        try:
            r = req.get(url, timeout=30)
            r.raise_for_status()
            sums = parse_checksums(r.text[:1_000_000])
            if sums:
                if is_third_party(url):
                    log_fn(M(f"[WARN] Sommes de contrôle obtenues via le miroir tiers {url.split('/')[2]} : "
                             "vérification moins fiable",
                             f"[WARN] Checksums obtained through the third-party mirror {url.split('/')[2]}: "
                             "weaker verification"))
                return sums, not is_third_party(url)
        except Exception:
            continue
    return {}, False

def _download_to_temp(url, suffix, max_bytes=DOWNLOAD_MAX_BYTES):
    """Télécharge vers un fichier temporaire → (chemin, sha256). Taille plafonnée."""
    h, size = hashlib.sha256(), 0
    with req.get(url, stream=True, timeout=120, allow_redirects=True) as r:
        r.raise_for_status()
        with tempfile.NamedTemporaryFile(suffix=suffix, delete=False) as tmp:
            try:
                for chunk in r.iter_content(65536):
                    size += len(chunk)
                    if size > max_bytes:
                        raise ValueError(M(f"fichier plus gros que {max_bytes // 1048576} Mo",
                                           f"file larger than {max_bytes // 1048576} MB"))
                    h.update(chunk)
                    tmp.write(chunk)
            except Exception:
                tmp.close()
                Path(tmp.name).unlink(missing_ok=True)
                raise
    return Path(tmp.name), h.hexdigest()

def download_archive(version, tag, log_fn):
    """Archive frp téléchargée ET vérifiée (SHA-256), ou (None, nom)."""
    arch     = get_arch()
    filename = f"frp_{version}_linux_{arch}.tar.gz"
    sums, sums_trusted = fetch_frp_checksums(tag, log_fn)
    expected = sums.get(filename)
    if not expected:
        log_fn(M(f"[WARN] Pas de somme de contrôle publiée pour {filename} : intégrité non vérifiable",
                 f"[WARN] No published checksum for {filename}: integrity cannot be verified"))
    if not mirrors_allowed():
        log_fn(M("[INFO] Miroirs tiers désactivés : github.com uniquement",
                 "[INFO] Third-party mirrors disabled: github.com only"))
    for url in build_download_mirrors(tag, filename):
        source = url.split("/")[2]
        third = is_third_party(url)
        if third and not expected:
            log_fn(M(f"[WARN] {source} ignoré : un miroir tiers n'est utilisé qu'avec une somme de contrôle",
                     f"[WARN] {source} skipped: a third-party mirror is only used with a checksum"))
            continue
        log_fn(M(f"[INFO] Tentative : {source} …", f"[INFO] Trying {source}…")
               + (M(" (miroir tiers)", " (third-party mirror)") if third else ""))
        try:
            path, digest = _download_to_temp(url, ".tar.gz")
        except Exception as e:
            log_fn(f"[WARN] {source} : {e}")
            continue
        if expected and not hmac.compare_digest(digest, expected):
            path.unlink(missing_ok=True)
            log_fn(M(f"[ERROR] {source} : somme SHA-256 différente de celle publiée, fichier rejeté",
                     f"[ERROR] {source}: SHA-256 differs from the published one, file rejected"))
            continue
        if expected:
            log_fn(M(f"[OK] Téléchargé depuis {source}, SHA-256 vérifiée",
                     f"[OK] Downloaded from {source}, SHA-256 verified")
                   + ("" if sums_trusted else M(" (somme obtenue via un miroir tiers)",
                                                " (checksum obtained through a third-party mirror)")))
        else:
            log_fn(M(f"[OK] Téléchargé depuis {source} (non vérifié)", f"[OK] Downloaded from {source} (not verified)"))
        return path, filename
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

ARCHIVE_MAX_MEMBERS  = 1000
ARCHIVE_MAX_UNPACKED = 300 * 1024 * 1024
FRP_BINARIES         = ("frps", "frpc")

def extract_frp_binaries(archive, dest):
    """Extrait frps/frpc d'une archive .tar.gz dans dest, sans jamais utiliser
    extractall : chemins absolus ou avec « .. », liens et fichiers spéciaux sont
    refusés, et la taille décompressée plafonnée (bombe de décompression).
    → {nom: chemin extrait}. ValueError si l'archive est suspecte."""
    dest = Path(dest)
    found, total, count = {}, 0, 0
    with tarfile.open(archive, "r:gz") as tf:
        for m in tf:
            count += 1
            if count > ARCHIVE_MAX_MEMBERS:
                raise ValueError(M("archive refusée : trop d'entrées", "archive rejected: too many entries"))
            parts = PurePosixPath(m.name).parts
            if m.name.startswith(("/", "\\")) or ".." in parts or "\\" in m.name:
                raise ValueError(M(f"archive refusée : chemin suspect « {m.name} »",
                                 f"archive rejected: suspicious path “{m.name}”"))
            base = parts[-1] if parts else ""
            if m.issym() or m.islnk():
                if base in FRP_BINARIES:
                    raise ValueError(M(f"archive refusée : {m.name} est un lien", f"archive rejected: {m.name} is a link"))
                continue
            if not m.isfile():
                continue                          # dossiers, périphériques, FIFO : ignorés
            total += m.size
            if total > ARCHIVE_MAX_UNPACKED:
                raise ValueError(M("archive refusée : trop volumineuse une fois décompressée",
                                   "archive rejected: too large once decompressed"))
            if base not in FRP_BINARIES or len(parts) > 2 or base in found:
                continue
            src = tf.extractfile(m)
            out = dest / base
            with open(out, "wb") as f:
                written = 0
                while True:
                    chunk = src.read(65536)
                    if not chunk:
                        break
                    written += len(chunk)
                    if written > m.size:
                        raise ValueError(M("archive refusée : taille incohérente", "archive rejected: inconsistent size"))
                    f.write(chunk)
            with open(out, "rb") as f:
                if f.read(4) != b"\x7fELF":
                    raise ValueError(M(f"archive refusée : {m.name} n'est pas un exécutable Linux",
                                     f"archive rejected: {m.name} is not a Linux executable"))
            found[base] = out
    return found

def install_from_archive(tmp_path, version, log_fn):
    log_fn(M("[INFO] Arrêt des services frp …", "[INFO] Stopping frp services…"))
    running = _stop_running_frp_services()
    if running:
        log_fn(M(f"[INFO] Stoppés : {', '.join(running)}", f"[INFO] Stopped: {', '.join(running)}"))
    try:
        log_fn(M("[INFO] Extraction …", "[INFO] Extracting…"))
        with tempfile.TemporaryDirectory() as tmpdir:
            extracted = extract_frp_binaries(tmp_path, tmpdir)
            installed = []
            for b in FRP_BINARIES:
                src = extracted.get(b)
                dst = FRP_BIN_DIR / b
                if src:
                    shutil.copy2(str(src), str(dst))
                    dst.chmod(0o755)
                    log_fn(f"[INFO] {b} → {dst}")
                    installed.append(b)
        if not installed:
            log_fn(M("[ERROR] Aucun binaire trouvé dans l'archive.", "[ERROR] No binary found in the archive."))
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
        log_fn(M(f"[OK] frp {version} installé.", f"[OK] frp {version} installed."))
        webhook_emit("update.frp_installed", (f"frp {version} installé", f"frp {version} installed"),
                     (f"frp {version} a été installé depuis le panel.", f"frp {version} was installed from the panel."),
                     subject=version, data={"version": version})
        return True
    except Exception as e:
        log_fn(f"[ERROR] {e}")
        return False
    finally:
        if running:
            log_fn(M(f"[INFO] Redémarrage : {', '.join(running)} …", f"[INFO] Restarting: {', '.join(running)}…"))
            for svc in running:
                r_ok, _, err = run_cmd(["systemctl", "start", svc])
                log_fn(f"[{'OK' if r_ok else 'WARN'}] {svc}{'' if r_ok else ' : ' + err}")

# ── Routes ────────────────────────────────────────────────────────────────────
@app.route("/")
@login_required
def index():
    # État initial fourni avec la page : l'interface s'affiche sans attendre
    # d'aller-retour vers l'API (détection en cache, pas une découverte forcée).
    user = current_user()
    can_view = user_can(user, "instances.view")
    boot = {"instances": filter_instances(detect_frp(force=False), user) if can_view else {},
            "in_docker": IN_DOCKER,
            "nicknames": filter_instances(MGR_CFG.get("nicknames", {}), user) if can_view else {},
            "has_password": bool(MGR_CFG.get("password_hash")),
            "me": _me_view(user)}
    return render_template("index.html", panel_version=PANEL_VERSION, boot=boot)

@app.route("/api/detect")
@permission_required("instances.view")
def api_detect():
    return jsonify({"ok": True, "instances": filter_instances(detect_frp(force=True), current_user()), "in_docker": IN_DOCKER})

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
@permission_required("instances.view")
def api_status():
    instances = filter_instances(detect_frp(force=False), current_user())
    state     = load_state()
    return jsonify({
        "ok": True, "instances": instances,
        "installed_version": _get_frp_installed_version(),
        "last_update_check": state.get("last_update_check"),
    })

@app.route("/api/service/<iid>/<action>", methods=["POST"])
@permission_required("instances.control")
def api_service_action(iid, action):
    detect_frp(force=False)
    if iid not in INSTANCES:
        return jsonify({"ok": False, "msg": M(f"Instance inconnue : {iid}", f"Unknown instance: {iid}")}), 404
    inst = INSTANCES[iid]
    _wh_panel_action[iid] = time.time()
    # ── Container Docker ──────────────────────────────────────────────────────
    if inst.get("source") == "docker":
        if action not in ("start", "stop", "restart"):
            return jsonify({"ok": False,
                "msg": M(f"Action '{action}' non supportée pour les containers Docker (start/stop/restart uniquement)",
                         f"Action '{action}' not supported for Docker containers (start/stop/restart only)")}), 400
        container = inst["container_name"]
        status, _ = _docker_api("POST", f"/containers/{container}/{action}")
        ok = status in (200, 204, 304)
        _invalidate_cache()
        return jsonify({"ok": ok, "msg": "OK" if ok else M(f"Erreur Docker (HTTP {status})", f"Docker error (HTTP {status})")})
    # ── Instance systemd ──────────────────────────────────────────────────────
    if action not in ("start","stop","restart","reload","enable","disable"):
        return jsonify({"ok": False, "msg": M("Action invalide", "Invalid action")}), 400
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
@permission_required("instances.delete")
def api_instance_delete_info(iid):
    """Ce que la suppression d'un container emporterait (affiché dans la confirmation)."""
    detect_frp(force=False)
    inst = INSTANCES.get(iid)
    if not inst:
        return jsonify({"ok": False, "msg": M(f"Instance inconnue : {iid}", f"Unknown instance: {iid}")}), 404
    if inst.get("source") != "docker":
        return jsonify({"ok": True, "image": None, "config_path": None})
    st, ins = _docker_api("GET", f"/containers/{inst['container_name']}/json")
    if st != 200 or not isinstance(ins, dict):
        return jsonify({"ok": False, "msg": M(f"Conteneur introuvable (HTTP {st})", f"Container not found (HTTP {st})")}), 404
    return jsonify({"ok": True,
                    "image": (ins.get("Config") or {}).get("Image") or inst.get("image"),
                    "config_path": _docker_mounted_config(ins, inst["type"])})

def _delete_container(iid, inst, delete_image, delete_config):
    container = inst["container_name"]
    st, ins = _docker_api("GET", f"/containers/{container}/json")
    if st != 200 or not isinstance(ins, dict):
        return False, M(f"Conteneur introuvable (HTTP {st})", f"Container not found (HTTP {st})"), 404
    image_id = ins.get("Image")
    image_name = (ins.get("Config") or {}).get("Image") or image_id
    cfg = _docker_mounted_config(ins, inst["type"]) if delete_config else None
    if delete_config and not cfg:
        return False, M("Aucun fichier de configuration monté n'a été trouvé pour ce conteneur.",
                         "No mounted configuration file was found for this container."), 400
    if cfg:
        shared = _config_users(cfg, iid)
        if shared:
            return False, M(f"{cfg} est aussi utilisé par {', '.join(shared)} : rien n'a été supprimé.",
                             f"{cfg} is also used by {', '.join(shared)}: nothing was deleted."), 400

    status, data = _docker_api("DELETE", f"/containers/{container}?force=true")
    if status not in (204, 404):
        detail = data.get("message") if isinstance(data, dict) else data
        return False, M(f"Erreur Docker (HTTP {status}) : {detail or ''}", f"Docker error (HTTP {status}): {detail or ''}").strip(), 500
    done, notes = [M(f"conteneur {container}", f"container {container}")], []

    if delete_image and image_id:
        status, data = _docker_api("DELETE", f"/images/{image_id}", timeout=60)
        if status == 200:
            done.append(f"image {image_name}")
        elif status == 409:
            notes.append(M(f"image {image_name} conservée : un autre conteneur l'utilise",
                            f"image {image_name} kept: another container uses it"))
        elif status != 404:
            notes.append(M(f"image {image_name} non supprimée (HTTP {status})", f"image {image_name} not deleted (HTTP {status})"))
    if cfg:
        if host_remove_file(cfg):
            done.append(cfg)
        else:
            notes.append(M(f"impossible de supprimer {cfg}", f"could not delete {cfg}"))
    msg = M(f"Supprimé : {', '.join(done)}", f"Deleted: {', '.join(done)}")
    if notes:
        msg += " — " + " ; ".join(notes)
    return True, msg, 200

@app.route("/api/instance/<iid>", methods=["DELETE"])
@permission_required("instances.delete")
def api_instance_delete(iid):
    detect_frp(force=True)
    if iid not in INSTANCES:
        return jsonify({"ok": False, "msg": M(f"Instance inconnue : {iid}", f"Unknown instance: {iid}")}), 404
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
    fragment = posixpath.normpath(fragment.strip()) if fragment.strip() else ""
    if not valid_service_name(inst["service"]):
        return jsonify({"ok": False, "msg": M("Nom de service refusé", "Service name rejected")}), 400
    if fragment and not fragment.startswith(_UNIT_DIR):
        return jsonify({"ok": False,
            "msg": M(f"{fragment} appartient à un paquet système : supprimez-le avec le gestionnaire de paquets.",
                     f"{fragment} belongs to a system package: remove it with the package manager.")}), 400

    cfg = Path(inst["config"]) if inst.get("config") else None
    if cfg and delete_config:
        shared = _config_users(str(cfg), iid)
        if shared:
            return jsonify({"ok": False,
                "msg": M(f"{cfg} est aussi utilisé par {', '.join(shared)} : il n'a pas été supprimé.",
                         f"{cfg} is also used by {', '.join(shared)}: it was not deleted.")}), 400
    done = []
    if fragment:
        run_cmd(["systemctl", "disable", "--now", unit], timeout=30)
        if not host_remove_file(fragment):
            _invalidate_cache(discovery=True)
            return jsonify({"ok": False, "msg": M(f"Service arrêté, mais impossible de supprimer {fragment}",
                                                  f"Service stopped, but {fragment} could not be deleted")}), 500
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
            return jsonify({"ok": False, "msg": M(f"Service supprimé, mais pas {cfg} : {e}",
                                                  f"Service deleted, but not {cfg}: {e}")}), 500
    kept = str(cfg) if cfg and cfg.exists() else None
    if kept and not done:
        done.append(M(f"{iid} retiré du tableau de bord ({kept} conservé)", f"{iid} removed from the dashboard ({kept} kept)"))
    _forget_instance(iid, kept_config=kept)
    _invalidate_cache(discovery=True)
    return jsonify({"ok": True, "msg": M(f"Supprimé : {', '.join(done)}", f"Deleted: {', '.join(done)}")})

@app.route("/api/config/<iid>", methods=["GET"])
@permission_required("config.read")
def api_config_get(iid):
    detect_frp(force=False)
    if iid not in INSTANCES:
        return jsonify({"ok": False, "msg": M("Instance inconnue", "Unknown instance")}), 404
    inst = INSTANCES[iid]
    # ── Container Docker : lire la config depuis les volumes montés ───────────
    if inst.get("source") == "docker":
        cfg_content = _get_docker_frpc_config(inst["container_name"])
        if cfg_content:
            return jsonify({"ok": True, "content": cfg_content, "exists": True, "docker": True})
        # Pas de config trouvée → retourner un template vide
        return jsonify({"ok": True, "content": DEFAULT_CONFIGS.get(inst["type"], ""),
                        "exists": False, "docker": True,
                        "msg": M("Config non trouvée — assurez-vous que le volume /etc/frp est monté.",
                                  "Config not found: make sure the /etc/frp volume is mounted.")})
    # ── Instance systemd / binaire ────────────────────────────────────────────
    cfg = Path(inst["config"])
    if not cfg.exists():
        return jsonify({"ok": True, "content": DEFAULT_CONFIGS.get(inst["type"], ""), "exists": False})
    return jsonify({"ok": True, "content": cfg.read_text(), "exists": True})

@app.route("/api/config/<iid>", methods=["POST"])
@permission_required("config.write")
def api_config_save(iid):
    detect_frp(force=False)
    if iid not in INSTANCES:
        return jsonify({"ok": False, "msg": M("Instance inconnue", "Unknown instance")}), 404
    content = (request.get_json(silent=True) or {}).get("content", "")
    if not isinstance(content, str):
        return jsonify({"ok": False, "msg": M("Contenu invalide", "Invalid content")}), 400
    ok, msg, status = write_instance_config(INSTANCES[iid], content)
    if ok:
        _invalidate_cache(discovery=True)      # serverAddr, fichier créé…
        webhook_emit("config.saved", (f"Configuration de {iid} modifiée", f"Configuration of {iid} changed"),
                     ("La configuration a été enregistrée depuis le panel.", "The configuration was saved from the panel."),
                     subject=iid, instance=iid)
    return jsonify({"ok": ok, "msg": msg}), status

# ── Écriture des configs : validation et confinement ─────────────────────────
# Le panel n'écrit une config que dans les dossiers où il en cherche
# (CONFIG_SEARCH_PATHS, /etc/frp en tête), après résolution des liens
# symboliques et des « .. ». Une config TOML doit se lire avant d'être écrite :
# un fichier cassé n'arrive jamais sur le disque.
CONFIG_MAX_BYTES = 1024 * 1024

def safe_config_path(path):
    """Chemin réel d'une config autorisée en écriture, ou ValueError."""
    real = Path(os.path.realpath(str(path)))
    if real.suffix not in _CONFIG_SUFFIXES:
        raise ValueError(M(f"{path} : extension refusée (attendu {', '.join(_CONFIG_SUFFIXES)})",
                           f"{path}: extension rejected (expected {', '.join(_CONFIG_SUFFIXES)})"))
    for root in CONFIG_SEARCH_PATHS:
        root_real = Path(os.path.realpath(str(root)))
        if real.parent == root_real or root_real in real.parents:
            return real
    dirs = ", ".join(str(r) for r in CONFIG_SEARCH_PATHS)
    raise ValueError(M(f"{path} est hors des dossiers de configuration autorisés ({dirs})",
                       f"{path} is outside the allowed configuration directories ({dirs})"))

def validate_config_content(path, content):
    """ValueError si le contenu ne peut pas être écrit tel quel."""
    if len(content.encode("utf-8", "surrogatepass")) > CONFIG_MAX_BYTES:
        raise ValueError(M("Configuration trop volumineuse (1 Mo au plus)", "Configuration too large (1 MB at most)"))
    if "\x00" in content:
        raise ValueError(M("Configuration invalide : caractère nul", "Invalid configuration: null character"))
    if Path(path).suffix == ".toml":
        if _tomllib is None:
            print("[WARN] tomllib/tomli indisponible : configuration TOML écrite sans vérification")
            return
        try:
            _tomllib.loads(content)
        except _tomllib.TOMLDecodeError as e:
            raise ValueError(M(f"TOML invalide, rien n'a été écrit : {e}", f"Invalid TOML, nothing was written: {e}"))

def _write_config_file(path, content):
    """→ (ok, message, code HTTP)."""
    try:
        real = safe_config_path(path)
        validate_config_content(real, content)
    except ValueError as e:
        return False, str(e), 400
    try:
        real.parent.mkdir(parents=True, exist_ok=True)
        real.write_text(content)
        return True, M(f"Sauvegardé : {real}", f"Saved: {real}"), 200
    except Exception as e:
        return False, M(f"Erreur écriture : {e}", f"Write error: {e}"), 500

def write_instance_config(inst, content_str):
    """Écrit la config TOML d'une instance (fichier monté pour un container).
    → (ok, message, code HTTP)"""
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
        return _write_config_file(cfg_path, content_str)

    # ── Instance systemd / binaire ────────────────────────────────────────────
    if not inst.get("config"):
        return False, M("Aucun fichier de config associé", "No configuration file associated"), 400
    return _write_config_file(inst["config"], content_str)

@app.route("/api/logs/<iid>")
@permission_required("logs.view")
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
        return jsonify({"ok": True, "content": content or M("(aucun log disponible)", "(no logs available)")})
    # ── Instance systemd ──────────────────────────────────────────────────────
    if request.args.get("source") == "file":
        # run_host : en mode Docker, le fichier de log est sur l'hôte, pas dans le container
        ok, out, err = run_host(["tail", "-n200", str(inst["log"])])
        return jsonify({"ok": True, "content": out if ok else (err or M(f"Fichier illisible : {inst['log']}",
                                                                         f"Unreadable file: {inst['log']}"))})
    if not valid_service_name(inst["service"]):
        return jsonify({"ok": False, "msg": M("Nom de service refusé", "Service name rejected")}), 400
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
    threading.Thread(target=_in_lang(_lang(), run), daemon=True).start()
    return q

@app.route("/api/logs/stream/<iid>")
@permission_required("logs.view")
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

def _same_origin():
    """Handshake WebSocket venu d'une page du panel (pas de jeton CSRF possible
    sur une requête GET d'upgrade : on vérifie l'en-tête Origin, s'il est là)."""
    origin = request.headers.get("Origin")
    if not origin:
        return True          # client hors navigateur : il lui faut quand même la session
    netloc = origin.split("://", 1)[-1].rstrip("/")
    return netloc in {request.host, request.headers.get("X-Forwarded-Host", "")}

def _ws_authenticated(ws, perm, iid=None):
    """→ fonction qui renvoie la fiche à jour du compte (None s'il a perdu le
    droit entre-temps), ou None si la connexion est refusée d'emblée."""
    user = current_user()
    if not user or not _same_origin() or not user_can(user, perm) or (iid is not None and not instance_allowed(user, iid)):
        ws.close(reason=1008, message=M("Non authentifié", "Not authenticated"))
        return None
    uid, fp = user["id"], session.get("pwfp", "")
    checked = {"at": time.time(), "user": user}

    def still_allowed():
        if time.time() - checked["at"] >= 5:
            u = all_users().get(uid)
            ok = (u and u.get("enabled", True) and hmac.compare_digest(fp.encode(), _session_fingerprint(u).encode())
                  and user_can(u, perm) and (iid is None or instance_allowed(u, iid)))
            checked.update(at=time.time(), user=u if ok else None)
        return checked["user"]
    return still_allowed

if sock:
    @sock.route("/ws/status")
    def ws_status(ws):
        """État des instances poussé au navigateur : vérifié toutes les 2 s côté
        serveur (et aussitôt après une action), envoyé seulement s'il a changé.
        Remplace l'appel périodique de /api/status par l'interface."""
        allowed = _ws_authenticated(ws, "instances.view")
        if not allowed:
            return
        last, last_sent = None, time.time()
        try:
            while ws.connected:
                user = allowed()
                if not user:
                    ws.close(reason=1008, message=M("Accès retiré", "Access revoked"))
                    break
                try:
                    payload = json.dumps({"instances": filter_instances(detect_frp(force=False), user), "in_docker": IN_DOCKER},
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
        allowed = _ws_authenticated(ws, "logs.view", iid)
        if not allowed:
            return
        detect_frp(force=False)
        inst = INSTANCES.get(iid)
        if not inst:
            ws.close(reason=1008, message=M("Instance inconnue", "Unknown instance"))
            return
        live = _live_log_from_request(inst)
        lines = _pump(live)
        last_sent = time.time()
        try:
            # Attente avec délai : on s'aperçoit de la fermeture de l'onglet
            # même quand le journal reste silencieux.
            while ws.connected:
                if not allowed():
                    ws.close(reason=1008, message=M("Accès retiré", "Access revoked"))
                    break
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
@permission_required("settings.manage")
def api_manager_config_get():
    safe = {k: v for k, v in MGR_CFG.items() if k not in ("password_hash", "secret_key", "webhooks", "users", "oidc", "firewall")}
    safe["is_owner"] = current_user()["id"] == OWNER_ID
    safe["has_password"] = bool(MGR_CFG.get("password_hash"))
    return jsonify({"ok": True, "config": safe})

@app.route("/api/manager/config", methods=["POST"])
@permission_required("settings.manage")
def api_manager_config_set():
    global MGR_CFG
    data = request.get_json(silent=True) or {}
    cfg  = dict(MGR_CFG)
    if ("username" in data or data.get("new_password")) and current_user()["id"] != OWNER_ID:
        return _forbidden(M("Seul le propriétaire change ses identifiants ici.",
                            "Only the owner changes their credentials here."))
    if "username" in data:
        user = str(data["username"]).strip()
        if not _USERNAME_RE.fullmatch(user):
            return jsonify({"ok": False, "msg": M("Identifiant invalide (1 à 64 caractères).",
                                              "Invalid username (1 to 64 characters).")}), 400
        if _name_taken(user, except_uid=OWNER_ID):
            return jsonify({"ok": False, "msg": M("Cet identifiant est déjà pris.", "This username is already taken.")}), 409
        cfg["username"] = user
    if "bind_host" in data:
        host = str(data["bind_host"]).strip()
        try:
            ipaddress.ip_address(host)
        except ValueError:
            return jsonify({"ok": False, "msg": M("Adresse d'écoute invalide (ex. 127.0.0.1, 0.0.0.0 ou ::).",
                                                  "Invalid listening address (e.g. 127.0.0.1, 0.0.0.0 or ::).")}), 400
        cfg["bind_host"] = host
    try:
        if "bind_port" in data:
            cfg["bind_port"] = int(data["bind_port"])
            if not 1 <= cfg["bind_port"] <= 65535:
                raise ValueError
        if "session_timeout" in data:
            cfg["session_timeout"] = int(data["session_timeout"])
            if not 300 <= cfg["session_timeout"] <= 30 * 86400:
                raise ValueError
    except (TypeError, ValueError):
        return jsonify({"ok": False, "msg": M("Port (1 à 65535) ou durée de session (5 min à 30 jours) invalide.",
                                              "Invalid port (1 to 65535) or session length (5 min to 30 days).")}), 400
    if "ssl_enabled" in data: cfg["ssl_enabled"] = bool(data["ssl_enabled"])
    if "download_mirrors" in data: cfg["download_mirrors"] = bool(data["download_mirrors"])
    if data.get("new_password"):
        problem = password_problem(str(data["new_password"]))
        if problem:
            return jsonify({"ok": False, "msg": problem}), 400
        cfg["password_hash"] = hash_password(str(data["new_password"]))
    save_manager_config(cfg)
    MGR_CFG = cfg
    app.permanent_session_lifetime = timedelta(seconds=int(cfg.get("session_timeout") or 3600))
    if data.get("new_password"):
        _start_session()      # cette session reste ouverte, les autres sont déconnectées
    return jsonify({"ok": True, "msg": M("Sauvegardé. Redémarrez frp-manager pour appliquer bind_host/port.",
                                         "Saved. Restart frp-manager to apply bind_host/port.")})

@app.route("/api/nicknames", methods=["GET"])
@permission_required("instances.view")
def api_nicknames_get():
    return jsonify({"ok": True, "nicknames": filter_instances(MGR_CFG.get("nicknames", {}), current_user())})

@app.route("/api/nickname/<iid>", methods=["POST"])
@permission_required("instances.control")
def api_nickname_set(iid):
    global MGR_CFG
    if not valid_instance_id(iid):
        return jsonify({"ok": False, "msg": M("Instance invalide", "Invalid instance")}), 400
    data = request.get_json(silent=True) or {}
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
    return jsonify({"ok": True, "msg": M("Surnom mis à jour", "Nickname updated")})

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

def github_release_is_prerelease(version, fresh=False):
    """Statut de la release GitHub v<version> : True/False, ou None si inconnu
    (pas de release, GitHub injoignable). Mis en cache 10 minutes, sauf fresh
    (bouton « Vérifier ») : une promotion récente est vue tout de suite."""
    now = time.time()
    cached = _release_status_cache.get(version)
    if cached and not fresh and now - cached[0] < 600:
        return cached[1]
    status = None
    try:
        status = bool(gh_api(f"repos/{PANEL_GITHUB_REPO}/releases/tags/v{version}", timeout=8).get("prerelease"))
    except req.HTTPError as e:
        if e.response is not None and e.response.status_code == 404:
            status = None       # pas de release pour cette version
        else:
            status = _web_prerelease_status(version)
    except Exception:
        status = _web_prerelease_status(version)
    _release_status_cache[version] = (now, status)
    return status

def _web_prerelease_status(version):
    """Statut déduit du site quand l'API ne répond pas : ce qui suit la release
    « latest » est en pré-release. Une version antérieure passe pour définitive,
    sans conséquence : les deux canaux lui proposent au moins « latest »."""
    try:
        latest = gh_web_latest_tag(PANEL_GITHUB_REPO, timeout=8)
    except Exception:
        return None
    return version_newer(version, latest)

def panel_is_prerelease(fresh=False):
    """Pré-release si le numéro l'indique (dev-<sha>, X.Y.Z-suffixe) ou si la release
    GitHub de cette version est marquée pré-release. Après promotion en release
    définitive, les mises à jour redeviennent possibles sans rien réinstaller."""
    return is_prerelease_version(PANEL_VERSION) or github_release_is_prerelease(PANEL_VERSION, fresh) is True

@app.route("/api/panel/version")
@permission_required("updates.view")
def api_panel_version():
    """Retourne la version actuelle du panel et vérifie si une mise à jour est dispo.
    Canal stable : releases uniquement. Pré-release en installation classique :
    pré-releases et releases plus récentes. Pré-release sous Docker : rien
    (la mise à jour passe par l'image)."""
    repo_configured = "VOTRE_USER" not in PANEL_GITHUB_REPO
    fresh = request.args.get("fresh") == "1"
    prerelease = panel_is_prerelease(fresh)
    latest_ver, release_url = _github_cached(("panel", prerelease),
                                             lambda: fetch_panel_latest(include_prereleases=prerelease),
                                             fresh=fresh)
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
@permission_required("updates.panel")
def api_panel_update():
    """Télécharge la dernière release du panel et relance frp-manager."""
    if "VOTRE_USER" in PANEL_GITHUB_REPO:
        return jsonify({"ok": False, "msg": M("Repo GitHub du panel non configuré.", "The panel's GitHub repository is not configured.")})
    prerelease = panel_is_prerelease()
    if prerelease and IN_DOCKER:
        return jsonify({"ok": False, "msg": M("Pré-release sous Docker : mettez à jour l'image du conteneur.",
                                              "Pre-release under Docker: update the container image.")})
    if not panel_update_lock.acquire(blocking=False):
        return jsonify({"ok": False, "msg": M("Mise à jour du panel déjà en cours.", "A panel update is already running.")})

    global panel_update_log
    panel_update_log = []

    def run():
        try:
            _panel_log(M("[INFO] Récupération des infos de release…", "[INFO] Fetching release information…"))
            try:
                # Une installation en pré-release suit aussi les pré-releases suivantes
                data = fetch_panel_release(include_prereleases=prerelease)
                if not data:
                    _panel_log(M("[ERROR] Aucune release trouvée.", "[ERROR] No release found."))
                    return
                tag = data.get("tag_name", "")
                assets = data.get("assets", [])
            except Exception as e:
                _panel_log(M(f"[ERROR] GitHub inaccessible : {e}", f"[ERROR] GitHub unreachable: {e}"))
                return
            if not version_newer(tag, PANEL_VERSION):
                _panel_log(M(f"[ERROR] {tag} n'est pas plus récente que v{PANEL_VERSION} : rien à faire.",
                             f"[ERROR] {tag} is not newer than v{PANEL_VERSION}: nothing to do."))
                return

            # Chercher l'asset zip (frp-manager.zip ou frp-manager-vX.X.X.zip)
            zip_url = sum_url = None
            for a in assets:
                if a["name"].endswith(".zip") and "frp-manager" in a["name"] and not zip_url:
                    zip_url = a["browser_download_url"]
                elif a["name"] == PANEL_CHECKSUM_ASSET:
                    sum_url = a["browser_download_url"]
            # Fallback : source code zip
            if not zip_url:
                zip_url = data.get("zipball_url")

            if not zip_url:
                _panel_log(M("[ERROR] Aucun asset .zip trouvé dans la release.", "[ERROR] No .zip asset found in the release."))
                return

            expected = None
            if sum_url:
                try:
                    r = req.get(sum_url, timeout=30)
                    r.raise_for_status()
                    m = re.match(r"\s*([0-9a-fA-F]{64})\b", r.text)
                    expected = m.group(1).lower() if m else None
                except Exception:
                    expected = None
            if not expected:
                _panel_log(M("[WARN] Pas de somme SHA-256 publiée pour cette release : intégrité non vérifiée",
                             "[WARN] No SHA-256 published for this release: integrity not verified"))

            _panel_log(M(f"[INFO] Téléchargement de {tag}…", f"[INFO] Downloading {tag}…"))
            try:
                tmp_path, digest = _download_to_temp(zip_url, ".zip", PANEL_ZIP_MAX_BYTES)
            except Exception as e:
                _panel_log(M(f"[ERROR] Téléchargement échoué : {e}", f"[ERROR] Download failed: {e}"))
                return
            if expected:
                if not hmac.compare_digest(digest, expected):
                    tmp_path.unlink(missing_ok=True)
                    _panel_log(M("[ERROR] Somme SHA-256 différente de celle publiée : mise à jour annulée",
                                 "[ERROR] SHA-256 differs from the published one: update cancelled"))
                    return
                _panel_log(M("[OK] Somme SHA-256 vérifiée", "[OK] SHA-256 verified"))

            _panel_log(M("[INFO] Extraction…", "[INFO] Extracting…"))
            install_dir = Path("/opt/frp-manager")
            try:
                import zipfile
                with zipfile.ZipFile(tmp_path, "r") as zf:
                    check_panel_zip(zf)
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
                        _panel_log(M("[INFO] Source zip : ", "[INFO] Zip source: ") + f"{src_dir} — "
                                   + M("contenu : ", "contents: ")
                                   + f"{[p.name for p in src_dir.iterdir()] if src_dir.exists() else '?'}")

                        # Copier app.py, templates/, install.sh
                        for item in ["app.py", "templates", "install.sh"]:
                            src = src_dir / item
                            dst = install_dir / item
                            if not src.exists():
                                _panel_log(M(f"[WARN] Absent du zip : {item}", f"[WARN] Missing from the zip: {item}"))
                                continue
                            if src.is_dir():
                                if dst.exists():
                                    shutil.rmtree(dst)
                                shutil.copytree(str(src), str(dst))
                                n = sum(1 for _ in dst.rglob("*") if _.is_file())
                                _panel_log(M(f"[INFO] Mis à jour : {item}/ ({n} fichiers)", f"[INFO] Updated: {item}/ ({n} files)"))
                            elif src.is_file():
                                shutil.copy2(str(src), str(dst))
                                _panel_log(M(f"[INFO] Mis à jour : {item}", f"[INFO] Updated: {item}"))

                        # Dépendances Python de la nouvelle version, dans le même
                        # environnement que le panel (venv de l'installation classique)
                        reqs = src_dir / "requirements.txt"
                        if reqs.exists():
                            _panel_log(M("[INFO] Installation des dépendances Python…", "[INFO] Installing Python dependencies…"))
                            try:
                                rc = subprocess.run([sys.executable, "-m", "pip", "install", "-q", "-r", str(reqs)],
                                                    capture_output=True, text=True, timeout=300)
                                if rc.returncode == 0:
                                    _panel_log(M("[INFO] Dépendances à jour", "[INFO] Dependencies up to date"))
                                else:
                                    _panel_log(f"[WARN] pip : {(rc.stderr or rc.stdout).strip()[-300:]} — "
                                               + M("le panel démarrera quand même", "the panel will start anyway"))
                            except Exception as e:
                                _panel_log(M(f"[WARN] pip indisponible ({e}) — le panel démarrera quand même",
                                             f"[WARN] pip unavailable ({e}): the panel will start anyway"))
            except Exception as e:
                _panel_log(M(f"[ERROR] Extraction : {e}", f"[ERROR] Extraction: {e}"))
                return
            finally:
                try: tmp_path.unlink()
                except OSError: pass

            # Sauvegarder la version installée dans state.json
            try:
                p = Path("/var/lib/frp-manager/state.json")
                p.parent.mkdir(parents=True, exist_ok=True)
                state = json.loads(p.read_text()) if p.exists() else {}
                state["panel_version"] = tag.lstrip("v")
                p.write_text(json.dumps(state, indent=2))
                _panel_log(M(f"[INFO] Version {tag} sauvegardée dans state.json", f"[INFO] Version {tag} saved in state.json"))
            except Exception as e:
                _panel_log(M(f"[WARN] Impossible de sauvegarder la version : {e}", f"[WARN] Could not save the version: {e}"))

            _panel_log(M(f"[OK] Panel {tag} installé. Redémarrage dans 2s…", f"[OK] Panel {tag} installed. Restarting in 2 s…"))
            def restart():
                time.sleep(2)
                _panel_log(M("[INFO] Redémarrage de frp-manager…", "[INFO] Restarting frp-manager…"))
                subprocess.Popen(["systemctl", "restart", "frp-manager"])
            threading.Thread(target=_in_lang(_lang(), restart), daemon=True).start()

        finally:
            panel_update_lock.release()

    threading.Thread(target=_in_lang(_lang(), run), daemon=True).start()
    return jsonify({"ok": True})

@app.route("/api/panel/update/log")
@permission_required("updates.view")
def api_panel_update_log():
    return jsonify({"ok": True, "lines": panel_update_log})

@app.route("/api/connectivity")
@permission_required("updates.view")
def api_connectivity():
    results = {}
    sources = build_version_sources()
    def test(name, url):
        try:
            results[name] = {"ok": True, "version": fetch_version_source(url, timeout=8) or "?"}
        except Exception as e:
            results[name] = {"ok": False, "error": str(e)[:120]}
    threads = [threading.Thread(target=test, args=(n,u)) for n,u in sources]
    for t in threads: t.start()
    for t in threads: t.join()
    return jsonify({"ok": True, "sources": results})

@app.route("/api/update/check")
@permission_required("updates.view")
def api_update_check():
    version, tag, source = _github_cached("frp", fetch_latest_version, fresh=request.args.get("fresh") == "1")
    if not version:
        return jsonify({"ok": False, "msg": M("Toutes les sources inaccessibles.", "All sources unreachable.")})
    installed = load_state().get("installed_version")
    state = load_state()
    state["last_update_check"] = datetime.now().isoformat()
    save_state(state)
    return jsonify({"ok": True, "latest": version, "tag": tag, "installed": installed,
                    "source": source, "update_available": installed != version if installed else True})

@app.route("/api/update/install", methods=["POST"])
@permission_required("updates.install")
def api_update_install():
    if not update_lock.acquire(blocking=False):
        return jsonify({"ok": False, "msg": M("Mise à jour déjà en cours", "An update is already running")})
    global update_log_buf
    update_log_buf = []
    def run():
        try:
            version, tag, source = fetch_latest_version()
            if not version:
                _log(M("[ERROR] Toutes les sources inaccessibles. Utilisez l'upload manuel.",
                        "[ERROR] All sources unreachable. Use the manual upload."))
                return
            _log(f"[INFO] Version: {tag} ({source})")
            tmp, _ = download_archive(version, tag, _log)
            if not tmp:
                _log(M("[ERROR] Tous les miroirs ont échoué. Utilisez l'upload manuel.",
                        "[ERROR] All mirrors failed. Use the manual upload."))
                return
            try:
                install_from_archive(tmp, version, _log)
            finally:
                try: tmp.unlink()
                except OSError: pass
        finally:
            update_lock.release()
    threading.Thread(target=_in_lang(_lang(), run), daemon=True).start()
    return jsonify({"ok": True})

@app.route("/api/update/upload", methods=["POST"])
@permission_required("updates.install")
def api_update_upload():
    if not update_lock.acquire(blocking=False):
        return jsonify({"ok": False, "msg": M("Mise à jour déjà en cours", "An update is already running")})
    global update_log_buf
    update_log_buf = []
    if "file" not in request.files:
        update_lock.release()
        return jsonify({"ok": False, "msg": M("Aucun fichier reçu", "No file received")})
    f       = request.files["file"]
    version = request.form.get("version", "").strip().lstrip("v")
    if not re.fullmatch(r"[0-9A-Za-z._-]{1,32}", version):
        version = "manual"
    with tempfile.NamedTemporaryFile(suffix=".tar.gz", delete=False) as tmp:
        f.save(tmp.name)
        tmp_path = Path(tmp.name)
    def run():
        try:
            install_from_archive(tmp_path, version, _log)
        finally:
            try: tmp_path.unlink()
            except OSError: pass
            update_lock.release()
    threading.Thread(target=_in_lang(_lang(), run), daemon=True).start()
    return jsonify({"ok": True})

@app.route("/api/update/log")
@permission_required("updates.view")
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
@permission_required("ports.view")
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
    return jsonify({"ok": True, "ports": with_port_labels(ports), "ufw_available": ufw_ok})

@app.route("/api/ports/open", methods=["POST"])
@permission_required("ports.manage")
def api_ports_open():
    data = request.get_json() or {}
    try:
        port = int(data.get("port", 0))
    except (ValueError, TypeError):
        return jsonify({"ok": False, "msg": M("Port invalide", "Invalid port")}), 400
    if not (1 <= port <= 65535):
        return jsonify({"ok": False, "msg": M("Port invalide", "Invalid port")}), 400
    proto = data.get("proto", "tcp").lower()
    if proto not in ("tcp", "udp"):
        proto = "tcp"
    ok, out, err = run_cmd(["ufw", "allow", f"{port}/{proto}"])
    msg = (out or err or "").strip()
    return jsonify({"ok": ok, "msg": msg or f"Port {port}/{proto} " + (M("ouvert", "opened") if ok else M("erreur", "error"))})

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

# ── Retrait de frp-autoupdate.py (0.0.53) ────────────────────────────────────
# frp ne se met plus à jour tout seul : uniquement depuis la page Mises à jour.
# Une installation antérieure garde sa tâche cron, le script et la ligne
# ExecStartPost du service (la mise à jour du panel ne touche qu'aux fichiers) :
# on les retire au démarrage.
LEGACY_AUTOUPDATE_CRON   = Path("/etc/cron.d/frp-autoupdate")
LEGACY_AUTOUPDATE_SCRIPT = Path(app.root_path) / "frp-autoupdate.py"
PANEL_UNIT_FILE          = Path("/etc/systemd/system/frp-manager.service")

def remove_frp_autoupdate():
    if IN_DOCKER:
        return
    for f in (LEGACY_AUTOUPDATE_CRON, LEGACY_AUTOUPDATE_SCRIPT):
        try:
            if f.exists():
                f.unlink()
                print(f"[INFO] Mise à jour automatique de frp retirée : {f} supprimé")
        except OSError as e:
            print(f"[WARN] {f} non supprimé : {e}")
    try:
        text = PANEL_UNIT_FILE.read_text()
    except OSError:
        return
    if "frp-autoupdate.py" in text:
        kept = "".join(line for line in text.splitlines(keepends=True) if "frp-autoupdate.py" not in line)
        try:
            PANEL_UNIT_FILE.write_text(kept)
            run_cmd(["systemctl", "daemon-reload"])
            print(f"[INFO] {PANEL_UNIT_FILE} : lancement de frp-autoupdate.py retiré")
        except OSError as e:
            print(f"[WARN] {PANEL_UNIT_FILE} non modifié : {e}")

def install_frp_cli():
    """python3 app.py --install-frp : installe la dernière version de frp si
    frps/frpc manquent (utilisé par install.sh). → code de sortie."""
    if all((FRP_BIN_DIR / b).exists() for b in FRP_BINARIES):
        print(M("frps et frpc sont déjà installés.", "frps and frpc are already installed."))
        return 0
    version, tag, source = fetch_latest_version()
    if not version:
        print(M("[ERROR] Dernière version de frp introuvable (réseau ?).", "[ERROR] Could not find the latest frp version (network?)."))
        return 1
    tmp, _ = download_archive(version, tag, print)
    if not tmp:
        return 1
    try:
        return 0 if install_from_archive(tmp, version, print) else 1
    finally:
        tmp.unlink(missing_ok=True)

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
            ok, msg, _ = write_instance_config(inst, new_text)
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
# release. On y publie une liste par une issue pré-remplie depuis le panel ;
# le workflow Forgejo blocklists.yml la vérifie et l'ajoute tout seul (seul
# l'auteur d'une liste peut la mettre à jour). Une liste bloque (« block ») ou
# autorise seulement (« allow ») : c'est le mode de la règle créée en s'y
# abonnant. Une règle abonnée (champ « list ») filtre ses propres adresses plus
# celles de la liste. Le catalogue est gardé sur disque : sans GitHub, la
# dernière version sert.
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
            raise ValueError(M(f"« {text} » n'est pas un numéro d'AS valide", f"“{text}” is not a valid AS number"))
        return {"asn": asn, "note": note}
    try:
        net = ipaddress.ip_network(text, strict=False)
    except ValueError:
        raise ValueError(M(f"« {text} » n'est ni une adresse IP, ni un réseau (203.0.113.0/24), ni un AS (AS16276)",
                           f"“{text}” is neither an IP address, a network (203.0.113.0/24) nor an AS (AS16276)"))
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
            skip("…", M(f"liste limitée à {FW_LIST_MAX_SOURCES} entrées", f"list limited to {FW_LIST_MAX_SOURCES} entries"))
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
                skip(text, M(f"{FW_LIST_MAX_ASNS} AS au plus par liste", f"{FW_LIST_MAX_ASNS} AS at most per list"))
                continue
            asns += 1
        else:
            net = ipaddress.ip_network(src["cidr"])
            if not net.is_global:
                skip(text, M("adresse privée ou réservée", "private or reserved address"))
                continue
            if net.prefixlen < (8 if net.version == 4 else 16):
                skip(text, M("réseau trop large", "network too wide"))
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
            "mode": "allow" if raw.get("mode") == "allow" else "block",
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
    names = ", ".join(missing)
    return (M(f"Liste communautaire introuvable dans le catalogue : {names}",
              f"Community list not found in the catalog: {names}")
            + (M(f" (catalogue injoignable : {err})", f" (catalog unreachable: {err})") if err
               else M(" — retirée ? supprimez la règle qui s'y abonne",
                      ": removed? Delete the rule that subscribes to it")))

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
    # Ports réellement ouverts : sans tableau de bord frps ni allowPorts, ceux
    # des clients ne sont connus que par là.
    for port, proto in _frps_listening_ports():
        if not any(k["start"] <= port <= k["end"] and k["proto"] in (proto, "any") for k in known):
            add(port, port, proto, "Port ouvert par frps", "listen")
    known.sort(key=lambda k: (k["start"], k["end"]))
    return known

def _frps_listening_ports():
    """{(port, proto)} sur lesquels frps écoute : sockets du processus frps de
    l'hôte (installation classique ou conteneur en réseau « host »), et ports
    publiés par les conteneurs frps en réseau bridge."""
    ports = set()
    ok, out, _ = run_host(["ss", "-H", "-lntup"])
    for line in out.splitlines() if ok else []:
        cols = line.split()
        if len(cols) < 6 or '(("frps"' not in line:
            continue
        addr, _, port = cols[4].rpartition(":")
        if addr.strip("[]") in ("127.0.0.1", "::1", "localhost") or addr.startswith("127."):
            continue                       # joignable seulement depuis la machine
        if port.isdigit():
            ports.add((int(port), "udp" if cols[0] == "udp" else "tcp"))
    for inst in _frps_instances().values():
        if inst.get("source") != "docker":
            continue
        st, ins = _docker_api("GET", f"/containers/{inst['container_name']}/json")
        if st != 200 or not isinstance(ins, dict):
            continue
        for spec, binds in ((ins.get("NetworkSettings") or {}).get("Ports") or {}).items():
            proto = "udp" if spec.endswith("/udp") else "tcp"
            for b in binds or []:
                if str(b.get("HostPort", "")).isdigit() and not str(b.get("HostIp", "")).startswith(("127.", "::1")):
                    ports.add((int(b["HostPort"]), proto))
    return ports

def _fw_parse_ports(spec):
    """'7000, 25565, 30000-30010' → [(7000, 7000), …]. ValueError si invalide."""
    ranges = []
    for part in re.split(r"[,\s]+", str(spec).strip()):
        if not part:
            continue
        m = re.fullmatch(r"(\d{1,5})(?:-(\d{1,5}))?", part)
        if not m:
            raise ValueError(M(f"« {part} » n'est pas un port ni une plage (ex. 30000-30010)",
                               f"“{part}” is neither a port nor a range (e.g. 30000-30010)"))
        a, b = int(m.group(1)), int(m.group(2) or m.group(1))
        if not (1 <= a <= b <= 65535):
            raise ValueError(M(f"« {part} » : les ports vont de 1 à 65535, dans l'ordre",
                               f"“{part}”: ports go from 1 to 65535, in order"))
        ranges.append((a, b))
    if not ranges:
        raise ValueError(M("Indiquez au moins un port", "Enter at least one port"))
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

class RuleError(ValueError):
    """Règle refusée : message à montrer tel quel."""

def _fw_normalize_rule(raw, index):
    """Valide une règle venue de l'interface. → règle propre, ou ValueError."""
    where = M(f"Règle {index + 1}", f"Rule {index + 1}")
    name = str(raw.get("name") or "").strip()[:60]
    if name:
        where = M(f"Règle « {name} »", f"Rule “{name}”")
    mode = raw.get("mode")
    if mode not in FW_MODES:
        raise RuleError(M(f"{where} : action inconnue", f"{where}: unknown action"))
    ports = str(raw.get("ports") or "").strip()
    if ports != "*":
        try:
            ranges = _fw_parse_ports(ports)
        except ValueError as e:
            raise RuleError(M(f"{where} : {e}", f"{where}: {e}"))
        panel = _fw_panel_port()
        if ranges == [(panel, panel)]:
            raise RuleError(M(f"{where} : le port du panel ({panel}) n'est jamais filtré ici",
                              f"{where}: the panel port ({panel}) is never filtered here"))
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
            raise RuleError(M(f"{where} : {e}", f"{where}: {e}"))
    lst = str(raw.get("list") or "").strip()
    if lst and not FW_LIST_ID.fullmatch(lst):
        raise RuleError(M(f"{where} : liste communautaire « {lst} » invalide", f"{where}: invalid community list “{lst}”"))
    if mode == "block" and not sources and not lst:
        raise RuleError(M(f"{where} : indiquez au moins une adresse à bloquer", f"{where}: enter at least one address to block"))
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
@permission_required("firewall.view")
def api_firewall_get():
    detect_frp(force=False)
    has_frps = bool(_frps_instances())
    cfg = _fw_cfg()
    ok, out, err = run_host(["nft", "--version"]) if has_frps else (False, "", "")
    known = _fw_known_ports() if has_frps and ok else []
    client = _client_ip()
    return jsonify({
        "ok": True, "has_frps": has_frps,
        "available": ok, "nft": out.strip() if ok else (err or M("nft introuvable", "nft not found")).strip(),
        "enabled": cfg["enabled"], "rules": cfg["rules"], "ports": with_port_labels(known),
        "active": ok and _fw_table_present(),
        "counters": _fw_counters() if ok else {},
        "asns": _asn_info(_fw_rule_asns(cfg["rules"])),
        "lists": _lists_info(cfg["rules"]),
        "client_ip": client,
        "client_verdicts": with_port_labels(_fw_verdicts(client, cfg, known)) if client and known and cfg["enabled"] else [],
        "panel_port": _fw_panel_port(), "in_docker": IN_DOCKER,
    })

# Gestionnaires de paquets reconnus → commandes d'installation de nftables
_NFT_INSTALLERS = [
    ("apt-get", [["env", "DEBIAN_FRONTEND=noninteractive", "apt-get", "update", "-q"],
                 ["env", "DEBIAN_FRONTEND=noninteractive", "apt-get", "install", "-y", "-q", "nftables"]]),
    ("dnf",     [["dnf", "install", "-y", "nftables"]]),
    ("yum",     [["yum", "install", "-y", "nftables"]]),
    ("apk",     [["apk", "add", "--no-cache", "nftables"]]),
    ("pacman",  [["pacman", "-Sy", "--noconfirm", "--needed", "nftables"]]),
    ("zypper",  [["zypper", "--non-interactive", "install", "nftables"]]),
]

@app.route("/api/firewall/install", methods=["POST"])
@permission_required("firewall.manage")
def api_firewall_install():
    """Installe nftables sur la machine (l'hôte en Docker) avec son gestionnaire de paquets."""
    if run_host(["nft", "--version"])[0]:
        return jsonify({"ok": True, "msg": M("nftables est déjà installé.", "nftables is already installed.")})
    for manager, steps in _NFT_INSTALLERS:
        if not run_host(["sh", "-c", 'command -v "$1"', "sh", manager])[0]:
            continue
        for cmd in steps:
            ok, out, err = run_host(cmd, timeout=600)
            if not ok:
                unknown = M("erreur inconnue", "unknown error")
                detail = (err or out or unknown).strip().splitlines()[-1:]
                detail = detail[0] if detail else unknown
                return jsonify({"ok": False, "msg": M(f"Installation de nftables échouée ({manager}) : {detail}",
                                                      f"nftables installation failed ({manager}): {detail}")}), 500
        ok, out, err = run_host(["nft", "--version"])
        if not ok:
            return jsonify({"ok": False, "msg": M(f"nftables installé, mais nft ne répond pas : {err or out}",
                                                  f"nftables installed, but nft does not respond: {err or out}")}), 500
        return jsonify({"ok": True, "msg": M(f"nftables installé ({out.strip()}).", f"nftables installed ({out.strip()}).")})
    return jsonify({"ok": False, "msg": M("Aucun gestionnaire de paquets reconnu (apt, dnf, yum, apk, pacman, zypper) : "
                                          "installez nftables à la main.",
                                          "No known package manager (apt, dnf, yum, apk, pacman, zypper): "
                                          "install nftables by hand.")}), 400

@app.route("/api/firewall", methods=["POST"])
@permission_required("firewall.manage")
def api_firewall_save():
    global MGR_CFG
    detect_frp(force=False)
    if not _frps_instances():
        return jsonify({"ok": False, "msg": M("Le pare-feu ne filtre que les ports d'un frps : aucun frps sur cette machine.",
                                              "The firewall only filters the ports of an frps: no frps on this machine.")}), 400
    data = request.get_json() or {}
    try:
        rules = [_fw_normalize_rule(r, i) for i, r in enumerate(data.get("rules") or [])]
    except ValueError as e:
        return jsonify({"ok": False, "msg": str(e)}), 400
    err = _fw_ensure_lists(rules)
    if err:
        return jsonify({"ok": False, "msg": err + M(". Rien n'a changé.", ". Nothing was changed.")}), 502
    for asn in _fw_rule_asns(rules):
        try:
            _asn_get(asn)
        except Exception as e:
            return jsonify({"ok": False, "msg": M(f"Impossible de récupérer les préfixes de AS{asn} "
                                                  f"auprès de RIPEstat, rien n'a changé : {e}",
                                                  f"Could not fetch the prefixes of AS{asn} "
                                                  f"from RIPEstat, nothing was changed: {e}")}), 502
    new = {"enabled": bool(data.get("enabled")), "rules": rules}
    ok, msg = _fw_apply(new)
    if not ok:
        return jsonify({"ok": False, "msg": M(f"nftables a refusé les règles, rien n'a changé : {msg or 'erreur inconnue'}",
                                              f"nftables rejected the rules, nothing was changed: {msg or 'unknown error'}")}), 500
    MGR_CFG = {**MGR_CFG, "firewall": new}
    save_manager_config(MGR_CFG)
    webhook_emit("firewall.saved", ("Pare-feu modifié", "Firewall changed"),
                 (f"{len(rules)} règle(s), pare-feu {'activé' if new['enabled'] else 'désactivé'}.",
                  f"{len(rules)} rule(s), firewall {'enabled' if new['enabled'] else 'disabled'}."),
                 data={"rules": len(rules), "enabled": new["enabled"]})
    return jsonify({"ok": True, "rules": rules,
                    "msg": M("Pare-feu appliqué", "Firewall applied") if new["enabled"]
                           else M("Pare-feu désactivé : plus aucun filtrage", "Firewall disabled: no more filtering")})

@app.route("/api/firewall/test", methods=["POST"])
@permission_required("firewall.view")
def api_firewall_test():
    """Ce que les règles (brouillon compris) feraient d'une adresse."""
    detect_frp(force=False)
    data = request.get_json() or {}
    try:
        ipaddress.ip_address(str(data.get("ip", "")).strip())
        rules = [_fw_normalize_rule(r, i) for i, r in enumerate(data.get("rules") or [])]
    except ValueError as e:
        return jsonify({"ok": False, "msg": str(e) if isinstance(e, RuleError) else M("Adresse IP invalide", "Invalid IP address")}), 400
    err = _fw_ensure_lists(rules)
    if err:
        return jsonify({"ok": False, "msg": err}), 502
    for asn in _fw_rule_asns(rules):
        try:
            _asn_get(asn)
        except Exception as e:
            return jsonify({"ok": False, "msg": M(f"Impossible de récupérer les préfixes de AS{asn} : {e}",
                                                  f"Could not fetch the prefixes of AS{asn}: {e}")}), 502
    verdicts = _fw_verdicts(str(data["ip"]).strip(), {"enabled": True, "rules": rules}, _fw_known_ports())
    return jsonify({"ok": True, "verdicts": with_port_labels(verdicts)})

@app.route("/api/firewall/lists")
@permission_required("firewall.view")
def api_firewall_lists():
    """Catalogue des listes communautaires (retéléchargé s'il a plus de 10 min)."""
    cache, err = _lists_get(0 if request.args.get("fresh") == "1" else 600)
    return jsonify({"ok": True, "lists": sorted(cache["lists"].values(), key=lambda lst: lst["name"].lower()),
                    "fetched": cache.get("fetched") or None, "error": err,
                    "browse_url": f"https://github.com/{PANEL_GITHUB_REPO}/tree/{FW_LISTS_BRANCH}"})

@app.route("/api/firewall/lists/publish", methods=["POST"])
@permission_required("firewall.manage")
def api_firewall_lists_publish():
    """Prépare la proposition d'une liste au catalogue : l'entrée telle que les
    panels l'accepteront, et le lien d'une issue GitHub pré-remplie. Rien n'est
    envoyé d'ici : l'utilisateur relit et soumet l'issue lui-même, puis le
    workflow Forgejo blocklists.yml l'ajoute au catalogue."""
    import unicodedata
    from urllib.parse import urlencode
    data = request.get_json() or {}
    name = str(data.get("name") or "").strip()[:60]
    if not name:
        return jsonify({"ok": False, "msg": M("Donnez un nom à la liste", "Give the list a name")}), 400
    lid = unicodedata.normalize("NFKD", name).encode("ascii", "ignore").decode().lower()
    lid = re.sub(r"[^a-z0-9]+", "-", lid).strip("-")[:40].strip("-") or f"liste-{secrets.token_hex(3)}"
    skipped = []
    items = [src if data.get("notes", True) else {**src, "note": ""} for src in data.get("sources") or []]
    sources = _fw_list_sources(items, skipped)
    if not sources:
        return jsonify({"ok": False, "msg": M("Aucune adresse publiable : il faut des adresses publiques, "
                                              "des réseaux d'au plus /8 (/16 en IPv6) ou des AS",
                                              "Nothing to publish: public addresses, networks of /8 at most "
                                              "(/16 for IPv6) or AS numbers are required"),
                        "skipped": skipped}), 400
    mode = "allow" if data.get("mode") == "allow" else "block"
    entry = {"id": lid, "name": name, "mode": mode,
             "description": str(data.get("description") or "").strip()[:300],
             "author": str(data.get("author") or "").strip()[:40],
             "updated": datetime.now().strftime("%Y-%m-%d"),
             "sources": [(f"AS{s['asn']}" if s.get("asn") else s["cidr"]) + (f"  # {s['note']}" if s["note"] else "")
                         for s in sources]}
    text = json.dumps(entry, indent=2, ensure_ascii=False)
    existing = _lists_all()["lists"].get(lid)
    kind = "d'autorisation" if mode == "allow" else "de blocage"
    title = f"{'Mise à jour de la liste' if existing else 'Nouvelle liste'} {kind} : {name}"
    intro = (f"Liste pour le catalogue communautaire du pare-feu (branche `{FW_LISTS_BRANCH}`), "
             f"préparée par FRP Manager {PANEL_VERSION}. Elle est vérifiée puis ajoutée "
             "automatiquement dans les minutes qui suivent.\n\n")
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
@permission_required("firewall.view")
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

# ── Notifications webhook ────────────────────────────────────────────────────
# Chaque webhook a son adresse, son format (Discord, Slack, Telegram, ntfy,
# Gotify, JSON signé, texte ou modèle libre), ses événements et sa langue.
# Les envois passent par une file et un thread : une requête du panel n'attend
# jamais un service de notification lent ou en panne.

# id → (niveau, français, anglais)
WEBHOOK_EVENTS = {
    "instance.down":        ("error",   "Instance arrêtée",                          "Instance stopped"),
    "instance.up":          ("success", "Instance démarrée",                         "Instance started"),
    "login.success":        ("info",    "Connexion au panel",                        "Panel sign-in"),
    "login.locked":         ("warning", "Adresse verrouillée (échecs de connexion)", "Address locked out (failed sign-ins)"),
    "config.saved":         ("info",    "Configuration modifiée",                    "Configuration changed"),
    "firewall.saved":       ("info",    "Règles du pare-feu modifiées",              "Firewall rules changed"),
    "update.panel":         ("info",    "Mise à jour du panel disponible",           "Panel update available"),
    "update.frp":           ("info",    "Nouvelle version de frp disponible",        "New frp version available"),
    "update.frp_installed": ("success", "frp mis à jour",                            "frp updated"),
    "panel.started":        ("info",    "Panel démarré",                             "Panel started"),
}
WEBHOOK_FORMATS = ("generic", "discord", "slack", "telegram", "ntfy", "gotify", "text")
WEBHOOK_MAX          = 20
WEBHOOK_HISTORY_MAX  = 50
WEBHOOK_RETRY_DELAYS = (0, 3, 10)          # 3 essais
WEBHOOK_TIMEOUT      = 10
WEBHOOK_MONITOR_SECONDS = 15
WEBHOOK_UPDATE_CHECK_SECONDS = 6 * 3600
_WEBHOOK_COLORS = {"info": 0x3B82F6, "success": 0x22C55E, "warning": 0xF59E0B, "error": 0xEF4444}
_WEBHOOK_TEMPLATE_KEYS = ("event", "title", "message", "level", "host", "time", "instance", "subject", "version")
_HEADER_NAME_RE = re.compile(r"[A-Za-z0-9!#$%&'*+.^_`|~-]{1,64}")

_webhook_queue   = queue.Queue(maxsize=200)
_webhook_history = []                       # plus récent en dernier
_webhook_lock    = threading.Lock()
_webhook_recent  = {}                       # (webhook, événement, sujet) → dernier envoi
_wh_panel_action = {}                       # instance → moment de la dernière action du panel

class WebhookError(ValueError):
    pass

def _webhooks_active():
    return [w for w in MGR_CFG.get("webhooks") or [] if w.get("enabled", True)]

def webhook_emit(event, title, text, subject="", instance=None, data=None, level=None):
    """Met un événement en file ; title et text sont des couples (français, anglais).
    Ne fait rien (et ne coûte rien) tant qu'aucun webhook n'est actif."""
    if event not in WEBHOOK_EVENTS or not _webhooks_active():
        return
    item = {"event": event, "title": title, "text": text, "subject": subject or "",
            "instance": instance, "data": data or {}, "level": level or WEBHOOK_EVENTS[event][0],
            "time": datetime.now().astimezone().isoformat(timespec="seconds")}
    try:
        _webhook_queue.put_nowait(item)
    except queue.Full:
        print("[WARN] Webhooks : file pleine, notification abandonnée")

def _webhook_render_template(template, values):
    """Remplace {{clé}} par la valeur échappée pour une chaîne JSON : le modèle
    s'écrit '{"content": "{{message}}"}'."""
    def sub(m):
        return json.dumps(str(values.get(m.group(1), "")), ensure_ascii=False)[1:-1]
    return re.sub(r"\{\{\s*(\w+)\s*\}\}", sub, template)

def _webhook_headers_parse(text):
    headers = {}
    for line in str(text or "").splitlines():
        line = line.strip()
        if not line:
            continue
        name, sep, value = line.partition(":")
        name, value = name.strip(), value.strip()
        if not sep or not _HEADER_NAME_RE.fullmatch(name) or any(c in value for c in "\r\n\0"):
            raise WebhookError(M(f"En-tête invalide : « {line[:60]} » (attendu : Nom: valeur).",
                                 f"Invalid header: “{line[:60]}” (expected: Name: value)."))
        headers[name] = value
    if len(headers) > 10:
        raise WebhookError(M("10 en-têtes au plus.", "10 headers at most."))
    return headers

def _webhook_values(item, lang):
    i = 0 if lang == "fr" else 1
    return {"event": item["event"], "title": item["title"][i], "message": item["text"][i],
            "level": item["level"], "host": _socket.gethostname(), "time": item["time"],
            "instance": item.get("instance") or "", "subject": item.get("subject") or "",
            "version": PANEL_VERSION}

def _webhook_request(hook, item):
    """(url, corps en octets, en-têtes) de la notification pour ce webhook."""
    v = _webhook_values(item, hook.get("lang", "fr"))
    fmt = hook.get("format", "generic")
    url = hook["url"]
    headers = {"Content-Type": "application/json", "User-Agent": f"FRP-Manager/{PANEL_VERSION}",
               "X-FRPManager-Event": item["event"]}
    if fmt == "discord":
        body = {"username": "FRP Manager", "embeds": [{
            "title": v["title"][:256], "description": v["message"][:4000],
            "color": _WEBHOOK_COLORS.get(item["level"], _WEBHOOK_COLORS["info"]),
            "timestamp": datetime.now().astimezone().isoformat(), "footer": {"text": v["host"]}}]}
    elif fmt == "slack":
        hexcolor = "#%06x" % _WEBHOOK_COLORS.get(item["level"], _WEBHOOK_COLORS["info"])
        body = {"attachments": [{"fallback": v["title"], "color": hexcolor, "title": v["title"],
                                 "text": v["message"], "footer": v["host"], "ts": int(time.time())}]}
    elif fmt == "telegram":
        esc = lambda s: s.replace("&", "&amp;").replace("<", "&lt;").replace(">", "&gt;")
        if not url.rstrip("/").endswith("/sendMessage"):
            url = url.rstrip("/") + "/sendMessage"
        body = {"chat_id": hook.get("chat_id", ""), "parse_mode": "HTML", "disable_web_page_preview": True,
                "text": f"<b>{esc(v['title'])}</b>\n{esc(v['message'])}\n<i>{esc(v['host'])}</i>"}
    elif fmt == "ntfy":
        # Publication en JSON à la racine du serveur : accents et emoji passent (pas d'en-têtes)
        p = urlsplit(url)
        url = f"{p.scheme}://{p.netloc}/"
        prio = {"info": 3, "success": 3, "warning": 4, "error": 5}[item["level"]]
        tag = {"info": "information_source", "success": "white_check_mark",
               "warning": "warning", "error": "rotating_light"}[item["level"]]
        body = {"topic": p.path.strip("/"), "title": v["title"], "message": f"{v['message']}\n{v['host']}",
                "priority": prio, "tags": [tag]}
    elif fmt == "gotify":
        prio = {"info": 4, "success": 4, "warning": 6, "error": 8}[item["level"]]
        body = {"title": v["title"], "message": f"{v['message']}\n{v['host']}", "priority": prio}
    elif fmt == "text":
        headers["Content-Type"] = "text/plain; charset=utf-8"
        body = f"{v['title']}\n{v['message']}\n{v['host']}"
    elif hook.get("template"):
        body = _webhook_render_template(hook["template"], v)
    else:
        body = {"event": item["event"], "level": item["level"], "title": v["title"], "message": v["message"],
                "host": v["host"], "time": item["time"], "panel_version": PANEL_VERSION,
                "instance": item.get("instance"), "data": item.get("data") or {}}
    raw = body.encode() if isinstance(body, str) else json.dumps(body, ensure_ascii=False).encode()
    if fmt in ("generic", "text") and hook.get("secret"):
        sig = hmac.new(hook["secret"].encode(), raw, hashlib.sha256).hexdigest()
        headers["X-FRPManager-Signature"] = f"sha256={sig}"
    try:
        headers.update(_webhook_headers_parse(hook.get("headers")))
    except WebhookError:
        pass                                  # refusés à l'enregistrement
    return url, raw, headers

def _webhook_send(hook, item):
    """Un envoi avec ses nouveaux essais → (ok, HTTP ou 0, message court)."""
    url, raw, headers = _webhook_request(hook, item)
    status, err = 0, ""
    for delay in WEBHOOK_RETRY_DELAYS:
        if delay:
            time.sleep(delay)
        try:
            r = req.post(url, data=raw, headers=headers, timeout=WEBHOOK_TIMEOUT, allow_redirects=False)
            status, err = r.status_code, ""
            if 200 <= status < 300:
                return True, status, "OK"
            err = f"HTTP {status}"
            if status < 500 and status != 429:
                break                         # refus définitif : inutile de réessayer
        except req.RequestException as e:
            status, err = 0, type(e).__name__
    return False, status, err

def _webhook_record(hook, item, ok, status, msg):
    entry = {"time": item["time"], "webhook": hook.get("id"), "name": hook.get("name", ""),
             "event": item["event"], "ok": ok, "status": status, "msg": msg}
    with _webhook_lock:
        _webhook_history.append(entry)
        del _webhook_history[:-WEBHOOK_HISTORY_MAX]
    if not ok:
        print(f"[WARN] Webhook « {hook.get('name')} » : {msg}")

def _webhook_matches(hook, item):
    events = hook.get("events") or []
    if "*" not in events and item["event"] not in events:
        return False
    only = hook.get("instances") or []
    if only and item.get("instance") and item["instance"] not in only:
        return False
    cooldown = int(hook.get("cooldown") or 0)
    if cooldown:
        key, now = (hook.get("id"), item["event"], item.get("subject")), time.time()
        with _webhook_lock:
            if now - _webhook_recent.get(key, 0) < cooldown:
                return False
            _webhook_recent[key] = now
            if len(_webhook_recent) > 2000:
                for k in [k for k, t in _webhook_recent.items() if now - t > 86400]:
                    del _webhook_recent[k]
    return True

def _webhook_worker():
    while True:
        item = _webhook_queue.get()
        try:
            for hook in _webhooks_active():
                if _webhook_matches(hook, item):
                    _webhook_record(hook, item, *_webhook_send(hook, item))
        except Exception as e:
            print(f"[WARN] Webhooks : {e}")

def _webhook_state_note(key, value):
    """Retient la dernière version annoncée : une mise à jour n'est notifiée qu'une fois."""
    state = load_state()
    if state.get(key) == value:
        return False
    state[key] = value
    try:
        save_state(state)
    except OSError:
        pass
    return True

def _webhook_wants(event):
    return any("*" in (w.get("events") or []) or event in (w.get("events") or []) for w in _webhooks_active())

def _webhook_check_updates():
    if _webhook_wants("update.panel") and "VOTRE_USER" not in PANEL_GITHUB_REPO:
        pre = panel_is_prerelease()
        ver, url = _github_cached(("panel", pre), lambda: fetch_panel_latest(include_prereleases=pre))
        if ver and not (pre and IN_DOCKER) and version_newer(ver, PANEL_VERSION) \
                and _webhook_state_note("notified_panel", ver):
            webhook_emit("update.panel", (f"Panel {ver} disponible", f"Panel {ver} available"),
                         (f"Version installée : {PANEL_VERSION}. {url}", f"Installed version: {PANEL_VERSION}. {url}"),
                         subject=ver, data={"current": PANEL_VERSION, "latest": ver, "url": url})
    if _webhook_wants("update.frp"):
        ver = _github_cached("frp", fetch_latest_version)[0]
        installed = load_state().get("installed_version")
        if ver and installed and version_newer(ver, installed) and _webhook_state_note("notified_frp", ver):
            webhook_emit("update.frp", (f"frp {ver} disponible", f"frp {ver} available"),
                         (f"Version installée : {installed}.", f"Installed version: {installed}."),
                         subject=ver, data={"current": installed, "latest": ver})

def _webhook_instance_events(instances, seen, pending):
    """Compare l'état des instances au précédent ; un changement doit tenir deux
    relevés d'affilée avant d'être annoncé (redémarrage rapide, appel raté)."""
    for iid, inst in instances.items():
        if not inst.get("binary_found", True):
            seen.pop(iid, None)
            pending.pop(iid, None)
            continue
        running = bool(inst["status"].get("running"))
        if iid not in seen:
            seen[iid] = running
        elif running == seen[iid]:
            pending.pop(iid, None)
        elif pending.get(iid) != running:
            pending[iid] = running            # premier relevé différent : à confirmer
        else:
            seen[iid] = running
            pending.pop(iid, None)
            name = (MGR_CFG.get("nicknames") or {}).get(iid) or inst.get("service") or iid
            by_panel = time.time() - _wh_panel_action.get(iid, 0) < 120
            src = (" (action lancée depuis le panel)", " (action started from the panel)") if by_panel else ("", "")
            if running:
                webhook_emit("instance.up", (f"{name} est démarrée", f"{name} is running"),
                             ("L'instance tourne." + src[0], "The instance is running." + src[1]),
                             subject=iid, instance=iid, data={"by_panel": by_panel})
            else:
                webhook_emit("instance.down", (f"{name} est arrêtée", f"{name} is stopped"),
                             ("L'instance ne tourne plus." + src[0], "The instance is no longer running." + src[1]),
                             subject=iid, instance=iid, data={"by_panel": by_panel})

def _webhook_monitor_loop():
    time.sleep(10)
    seen, pending, last_updates = {}, {}, 0
    while True:
        try:
            if _webhooks_active():
                _webhook_instance_events(detect_frp(force=False), seen, pending)
                if time.time() - last_updates >= WEBHOOK_UPDATE_CHECK_SECONDS:
                    last_updates = time.time()
                    _webhook_check_updates()
            else:
                seen.clear()
                pending.clear()
        except Exception as e:
            print(f"[WARN] Webhooks : {e}")
        time.sleep(WEBHOOK_MONITOR_SECONDS)

def _webhook_public(w):
    """Un webhook tel que l'interface le voit : l'adresse (souvent porteuse d'un
    jeton), le secret et les en-têtes ne sortent jamais du serveur."""
    p = urlsplit(w.get("url", ""))
    return {"id": w["id"], "name": w.get("name", ""), "enabled": w.get("enabled", True),
            "format": w.get("format", "generic"), "url_hint": f"{p.scheme}://{p.netloc}/…" if p.netloc else "",
            "has_secret": bool(w.get("secret")), "has_headers": bool(w.get("headers")),
            "chat_id": w.get("chat_id", ""), "events": w.get("events") or [], "instances": w.get("instances") or [],
            "lang": w.get("lang", "fr"), "cooldown": int(w.get("cooldown") or 0), "template": w.get("template", "")}

def _webhook_normalize(raw, old=None):
    """Webhook validé ; l'adresse, le secret et les en-têtes laissés vides
    reprennent ceux de l'enregistrement précédent."""
    old = old or {}
    if not isinstance(raw, dict):
        raise WebhookError(M("Webhook invalide.", "Invalid webhook."))
    name = str(raw.get("name") or "").strip()[:64]
    if not name:
        raise WebhookError(M("Donnez un nom au webhook.", "Give the webhook a name."))
    fmt = raw.get("format") or "generic"
    if fmt not in WEBHOOK_FORMATS:
        raise WebhookError(M("Format inconnu.", "Unknown format."))
    url = str(raw.get("url") or "").strip() or old.get("url", "")
    p = urlsplit(url)
    if p.scheme not in ("http", "https") or not p.hostname or len(url) > 2048 or any(c in url for c in " \r\n\0"):
        raise WebhookError(M(f"« {name} » : adresse invalide (http:// ou https:// attendu).",
                             f"“{name}”: invalid address (http:// or https:// expected)."))
    if fmt == "ntfy" and not p.path.strip("/"):
        raise WebhookError(M(f"« {name} » : ajoutez le sujet ntfy à l'adresse (https://ntfy.sh/mon-sujet).",
                             f"“{name}”: add the ntfy topic to the address (https://ntfy.sh/my-topic)."))
    chat_id = str(raw.get("chat_id") or "").strip()[:64]
    if fmt == "telegram" and not chat_id:
        raise WebhookError(M(f"« {name} » : identifiant de conversation Telegram requis.",
                             f"“{name}”: Telegram chat ID required."))
    events = raw.get("events")
    if not isinstance(events, list) or not events or any(e != "*" and e not in WEBHOOK_EVENTS for e in events):
        raise WebhookError(M(f"« {name} » : choisissez au moins un événement.", f"“{name}”: pick at least one event."))
    instances = [str(i) for i in (raw.get("instances") or []) if valid_instance_id(str(i))][:50]
    lang = "en" if raw.get("lang") == "en" else "fr"
    try:
        cooldown = int(raw.get("cooldown") or 0)
        if not 0 <= cooldown <= 86400:
            raise ValueError
    except (TypeError, ValueError):
        raise WebhookError(M("Délai entre deux envois : 0 à 86 400 secondes.",
                             "Delay between two sends: 0 to 86,400 seconds."))
    secret = "" if raw.get("secret_clear") else str(raw.get("secret") or "") or old.get("secret", "")
    headers = "" if raw.get("headers_clear") else str(raw.get("headers") or "").strip() or old.get("headers", "")
    _webhook_headers_parse(headers)
    template = str(raw.get("template") or "").strip()[:4000] if fmt == "generic" else ""
    if template:
        try:
            json.loads(_webhook_render_template(template, {k: "x" for k in _WEBHOOK_TEMPLATE_KEYS}))
        except ValueError:
            raise WebhookError(M(f"« {name} » : le modèle n'est pas un JSON valide une fois rempli.",
                                 f"“{name}”: the template is not valid JSON once filled in."))
    return {"id": old.get("id") or secrets.token_hex(4), "name": name, "enabled": bool(raw.get("enabled", True)),
            "format": fmt, "url": url, "secret": secret, "headers": headers, "chat_id": chat_id,
            "events": events, "instances": instances, "lang": lang, "cooldown": cooldown, "template": template}

@app.route("/api/webhooks", methods=["GET"])
@permission_required("webhooks.view")
def api_webhooks_get():
    with _webhook_lock:
        history = list(reversed(_webhook_history))
    return jsonify({"ok": True, "webhooks": [_webhook_public(w) for w in MGR_CFG.get("webhooks") or []],
                    "history": history,
                    "events": [{"id": k, "level": v[0]} for k, v in WEBHOOK_EVENTS.items()],
                    "formats": list(WEBHOOK_FORMATS)})

@app.route("/api/webhooks", methods=["POST"])
@permission_required("webhooks.manage")
def api_webhooks_save():
    global MGR_CFG
    raws = (request.get_json(silent=True) or {}).get("webhooks")
    if not isinstance(raws, list) or len(raws) > WEBHOOK_MAX:
        return jsonify({"ok": False, "msg": M(f"{WEBHOOK_MAX} webhooks au plus.", f"{WEBHOOK_MAX} webhooks at most.")}), 400
    known = {w["id"]: w for w in MGR_CFG.get("webhooks") or []}
    try:
        hooks = [_webhook_normalize(r, known.get(str(r.get("id"))) if isinstance(r, dict) else None) for r in raws]
    except WebhookError as e:
        return jsonify({"ok": False, "msg": str(e)}), 400
    MGR_CFG = {**MGR_CFG, "webhooks": hooks}
    save_manager_config(MGR_CFG)
    return jsonify({"ok": True, "webhooks": [_webhook_public(w) for w in hooks],
                    "msg": M("Webhooks enregistrés", "Webhooks saved")})

@app.route("/api/webhooks/test", methods=["POST"])
@permission_required("webhooks.manage")
def api_webhooks_test():
    """Envoie une notification d'essai au webhook décrit (enregistré ou en cours de saisie)."""
    raw = (request.get_json(silent=True) or {}).get("webhook")
    raw = raw if isinstance(raw, dict) else {}
    old = next((w for w in MGR_CFG.get("webhooks") or [] if w["id"] == str(raw.get("id"))), None)
    try:
        hook = _webhook_normalize({**raw, "events": ["*"]}, old)
    except WebhookError as e:
        return jsonify({"ok": False, "msg": str(e)}), 400
    item = {"event": "test", "level": "info",
            "title": ("Notification d'essai", "Test notification"),
            "text": (f"Ce webhook fonctionne. FRP Manager {PANEL_VERSION}.",
                     f"This webhook works. FRP Manager {PANEL_VERSION}."),
            "subject": "test", "instance": None, "data": {"test": True},
            "time": datetime.now().astimezone().isoformat(timespec="seconds")}
    ok, status, msg = _webhook_send(hook, item)
    _webhook_record(hook, item, ok, status, msg)
    return jsonify({"ok": ok, "status": status,
                    "msg": M("Notification envoyée", "Notification sent") if ok
                           else M(f"Échec de l'envoi : {msg}", f"Sending failed: {msg}")})

if __name__ == "__main__":
    if "--reset-password" in sys.argv[1:]:
        reset_password_cli()
        sys.exit(0)
    if "--install-frp" in sys.argv[1:]:
        sys.exit(install_frp_cli())
    ensure_manager_config()
    remove_frp_autoupdate()
    app.secret_key = MGR_CFG["secret_key"]
    # FRP_MANAGER_HOST (Docker : docker-compose.yml) passe avant bind_host
    host = os.environ.get("FRP_MANAGER_HOST") or MGR_CFG.get("bind_host") or "127.0.0.1"
    port = int(MGR_CFG.get("bind_port") or 8765)
    ssl_ctx = get_ssl_context()
    app.config["SESSION_COOKIE_SECURE"] = bool(ssl_ctx or MGR_CFG.get("cookie_secure"))
    proto = "https" if ssl_ctx else "http"
    print(f"[INFO] FRP Manager démarré sur {proto}://{host}:{port}")
    if needs_setup():
        print("[INFO] Aucun mot de passe : ouvrez le panel pour créer l'identifiant administrateur.")
        if host not in ("127.0.0.1", "::1", "localhost"):
            print(f"[WARN] Le panel écoute sur {host} sans mot de passe : la première personne "
                  "qui l'ouvre choisit les identifiants. Configurez-le sans attendre.")
    if not mirrors_allowed():
        print("[INFO] Miroirs de téléchargement tiers désactivés")
    threading.Thread(target=migrate_away_from_mmproxy, daemon=True).start()
    threading.Thread(target=_fw_sync_loop, daemon=True).start()
    threading.Thread(target=_webhook_worker, daemon=True).start()
    threading.Thread(target=_webhook_monitor_loop, daemon=True).start()
    webhook_emit("panel.started", ("Panel démarré", "Panel started"),
                 (f"FRP Manager {PANEL_VERSION} écoute sur {host}:{port}.", f"FRP Manager {PANEL_VERSION} is listening on {host}:{port}."))
    app.run(host=host, port=port, debug=False, ssl_context=ssl_ctx)
