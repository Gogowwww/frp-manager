#!/usr/bin/env python3
"""
frp-autoupdate.py
Called by cron and at startup by systemd to auto-update frp binaries.
Sends a Discord notification on success or failure (optional).
"""

import os
import re
import sys
import json
import hmac
import shutil
import hashlib
import tarfile
import tempfile
import platform
import requests
from pathlib import Path, PurePosixPath
from datetime import datetime

# ── Config ────────────────────────────────────────────────────────────────
FRP_BIN_DIR    = Path("/usr/local/bin")
FRP_CONF_DIR   = Path("/etc/frp")
FRP_LOG_DIR    = Path("/var/log/frp")
FRP_STATE_FILE = Path("/var/lib/frp-manager/state.json")
MGR_CONF_FILE  = Path(os.environ.get("FRP_MANAGER_CONFIG") or "/etc/frp-manager/frp-manager.json")
GITHUB_RELEASES = "https://api.github.com/repos/fatedier/frp/releases/latest"
VERSION_APIS = [
    "https://api.github.com/repos/fatedier/frp/releases/latest",
]
RELEASE_DOWNLOAD = "https://github.com/fatedier/frp/releases/download/{tag}/{filename}"
# Miroirs TIERS (voir app.py) : seulement si download_mirrors est vrai dans
# frp-manager.json, et jamais sans somme SHA-256 pour vérifier l'archive.
THIRD_PARTY_MIRRORS = [
    "https://mirror.ghproxy.com/" + RELEASE_DOWNLOAD,
    "https://ghfast.top/" + RELEASE_DOWNLOAD,
    "https://gh-proxy.com/" + RELEASE_DOWNLOAD,
]
CHECKSUMS_FILE = "frp_sha256_checksums.txt"
TAG_RE = re.compile(r"v\d+\.\d+\.\d+")
DOWNLOAD_MAX_BYTES = 100 * 1024 * 1024
ARCHIVE_MAX_MEMBERS = 1000
ARCHIVE_MAX_UNPACKED = 300 * 1024 * 1024

# Optional: set DISCORD_WEBHOOK env var or hardcode here
DISCORD_WEBHOOK = os.environ.get("FRP_DISCORD_WEBHOOK", "")

SERVICES = ["frps", "frpc"]


def log(msg):
    ts = datetime.now().strftime("%Y-%m-%d %H:%M:%S")
    print(f"[{ts}] {msg}", flush=True)


def load_state():
    try:
        if FRP_STATE_FILE.exists():
            return json.loads(FRP_STATE_FILE.read_text())
    except Exception:
        pass
    return {"installed_version": None}


def save_state(state):
    FRP_STATE_FILE.parent.mkdir(parents=True, exist_ok=True)
    FRP_STATE_FILE.write_text(json.dumps(state, indent=2))


def mirrors_allowed():
    if os.environ.get("FRP_MANAGER_NO_MIRRORS", "") in ("1", "true", "yes"):
        return False
    try:
        return json.loads(MGR_CONF_FILE.read_text()).get("download_mirrors", True) is not False
    except Exception:
        return True


def release_urls(tag, filename):
    tpls = [RELEASE_DOWNLOAD] + (THIRD_PARTY_MIRRORS if mirrors_allowed() else [])
    return [t.format(tag=tag, filename=filename) for t in tpls]


def redact(text):
    """Pas d'URL de webhook (elle contient un jeton) dans les journaux."""
    text = str(text)
    if DISCORD_WEBHOOK:
        text = text.replace(DISCORD_WEBHOOK, "<webhook masqué>")
    return re.sub(r"(/api/webhooks/\d+/)[\w-]+", r"\1<masqué>", text)


def fetch_checksums(tag):
    for url in release_urls(tag, CHECKSUMS_FILE):
        try:
            r = requests.get(url, timeout=30)
            r.raise_for_status()
            sums = {}
            for line in r.text[:1_000_000].splitlines():
                m = re.fullmatch(r"\s*([0-9a-fA-F]{64})\s+\*?(\S+)\s*", line)
                if m:
                    sums[m.group(2)] = m.group(1).lower()
            if sums:
                if not url.startswith("https://github.com/"):
                    log(f"  ATTENTION : sommes de contrôle obtenues via le miroir tiers {url.split('/')[2]}")
                return sums
        except Exception:
            continue
    return {}


def extract_binaries(archive, dest):
    """frps/frpc seulement, sans extractall : chemins suspects, liens et bombes
    de décompression refusés (même logique que extract_frp_binaries d'app.py)."""
    found, total, count = {}, 0, 0
    with tarfile.open(archive, "r:gz") as tf:
        for m in tf:
            count += 1
            if count > ARCHIVE_MAX_MEMBERS:
                raise ValueError("archive refusée : trop d'entrées")
            parts = PurePosixPath(m.name).parts
            if m.name.startswith(("/", "\\")) or ".." in parts or "\\" in m.name:
                raise ValueError(f"archive refusée : chemin suspect {m.name!r}")
            base = parts[-1] if parts else ""
            if m.issym() or m.islnk():
                if base in ("frps", "frpc"):
                    raise ValueError(f"archive refusée : {m.name} est un lien")
                continue
            if not m.isfile():
                continue
            total += m.size
            if total > ARCHIVE_MAX_UNPACKED:
                raise ValueError("archive refusée : trop volumineuse une fois décompressée")
            if base not in ("frps", "frpc") or len(parts) > 2 or base in found:
                continue
            src, out = tf.extractfile(m), Path(dest) / base
            with open(out, "wb") as f:
                shutil.copyfileobj(src, f, 65536)
            if out.stat().st_size != m.size:
                raise ValueError("archive refusée : taille incohérente")
            with open(out, "rb") as f:
                if f.read(4) != b"\x7fELF":
                    raise ValueError(f"archive refusée : {m.name} n'est pas un exécutable Linux")
            found[base] = out
    return found


def get_arch():
    machine = platform.machine().lower()
    arch_map = {"x86_64": "amd64", "aarch64": "arm64", "armv7l": "arm"}
    return arch_map.get(machine, "amd64")


def fetch_latest_release():
    for url in VERSION_APIS:
        try:
            r = requests.get(url, timeout=30,
                             headers={"Accept": "application/vnd.github.v3+json"})
            r.raise_for_status()
            data = r.json()
            tag     = data["tag_name"]
            if not TAG_RE.fullmatch(tag):
                raise ValueError(f"tag inattendu : {tag!r}")
            version = tag.lstrip("v")
            assets  = data.get("assets", [])
            return version, tag, assets
        except Exception:
            continue
    # Quota de l'API épuisé (60 appels/h sans compte) : le site indique la même
    # version par la redirection de /releases/latest
    try:
        r = requests.head("https://github.com/fatedier/frp/releases/latest", timeout=30, allow_redirects=False)
        tag = r.headers.get("Location", "").rstrip("/").rsplit("/releases/tag/", 1)[1]
        if TAG_RE.fullmatch(tag):
            return tag.lstrip("v"), tag, []
    except Exception:
        pass
    raise RuntimeError("Tous les endpoints de version sont inaccessibles")


def find_asset_url(assets, version):
    arch     = get_arch()
    filename = f"frp_{version}_linux_{arch}.tar.gz"
    for a in assets:
        if a["name"] == filename:
            return a["browser_download_url"], filename
    return None, filename


def install_version(version, tag, assets):
    arch     = get_arch()
    filename = f"frp_{version}_linux_{arch}.tar.gz"

    log(f"Téléchargement de {filename}…")
    expected = fetch_checksums(tag).get(filename)
    if not expected:
        log(f"  ATTENTION : pas de somme de contrôle pour {filename}, intégrité non vérifiable")
    tmp_path = None
    for url in release_urls(tag, filename):
        source = url.split('/')[2]
        third = not url.startswith("https://github.com/")
        if third and not expected:
            log(f"  {source} ignoré : miroir tiers sans somme de contrôle")
            continue
        log(f"  Tentative via {source}…" + (" (miroir tiers)" if third else ""))
        try:
            digest, size, candidate = hashlib.sha256(), 0, None
            with requests.get(url, stream=True, timeout=120) as r:
                r.raise_for_status()
                with tempfile.NamedTemporaryFile(suffix=".tar.gz", delete=False) as tmp:
                    candidate = Path(tmp.name)
                    for chunk in r.iter_content(65536):
                        size += len(chunk)
                        if size > DOWNLOAD_MAX_BYTES:
                            raise ValueError("fichier trop volumineux")
                        digest.update(chunk)
                        tmp.write(chunk)
            if expected and not hmac.compare_digest(digest.hexdigest(), expected):
                candidate.unlink(missing_ok=True)
                log(f"  {source} : somme SHA-256 différente de celle publiée, fichier rejeté")
                continue
            tmp_path = candidate
            log(f"  Téléchargé depuis {source}" + (", SHA-256 vérifiée" if expected else " (non vérifié)"))
            break
        except Exception as e:
            if candidate:
                candidate.unlink(missing_ok=True)
            log(f"  Échec {source}: {e}")
            continue

    if not tmp_path:
        raise RuntimeError("Tous les miroirs de téléchargement ont échoué")

    # Détecter les services actifs avant de remplacer les binaires
    import subprocess as _sp
    running_svcs = []
    res = _sp.run(["systemctl", "list-units", "--type=service", "--state=active",
                   "--no-pager", "--plain", "--no-legend"],
                  capture_output=True, text=True)
    for line in res.stdout.splitlines():
        parts = line.split()
        if not parts:
            continue
        unit = parts[0].strip("●▶ ")
        if not unit.endswith(".service"):
            continue
        name = unit[:-8]
        prop = _sp.run(["systemctl", "show", unit, "--property=ExecStart", "--value"],
                       capture_output=True, text=True).stdout
        if any(b in prop for b in ("/frps", "/frpc")):
            running_svcs.append(name)

    if running_svcs:
        log(f"Arrêt des services : {', '.join(running_svcs)}")
        for svc in running_svcs:
            _sp.run(["systemctl", "stop", svc])

    log("Extraction…")
    try:
        with tempfile.TemporaryDirectory() as tmpdir:
            extracted = extract_binaries(tmp_path, tmpdir)
            for binary in ["frps", "frpc"]:
                src = extracted.get(binary)
                dst = FRP_BIN_DIR / binary
                if src:
                    shutil.copy2(str(src), str(dst))
                    dst.chmod(0o755)
                    log(f"  → {dst}")
    finally:
        tmp_path.unlink(missing_ok=True)
        if running_svcs:
            log(f"Redémarrage des services : {', '.join(running_svcs)}")
            for svc in running_svcs:
                r = _sp.run(["systemctl", "start", svc])
                log(f"  {svc} : {'OK' if r.returncode == 0 else 'WARN'}")

    FRP_CONF_DIR.mkdir(parents=True, exist_ok=True)
    FRP_LOG_DIR.mkdir(parents=True, exist_ok=True)

    state = load_state()
    state["installed_version"] = version
    state["last_update_check"] = datetime.now().isoformat()
    state["last_update_result"] = f"Auto-updated to {version}"
    save_state(state)


def send_discord(msg):
    if not DISCORD_WEBHOOK:
        return
    try:
        requests.post(DISCORD_WEBHOOK, json={"content": msg}, timeout=10)
    except Exception as e:
        log(f"Discord notification failed: {redact(e)}")


def run_cmd(cmd):
    import subprocess
    r = subprocess.run(cmd, capture_output=True, text=True)
    return r.returncode == 0


def restart_services(services_to_restart):
    for svc in services_to_restart:
        ok = run_cmd(["systemctl", "is-active", "--quiet", svc])
        if ok:
            log(f"Restarting {svc}…")
            run_cmd(["systemctl", "restart", svc])


def main():
    log("frp auto-update check starting…")
    try:
        latest_version, tag, assets = fetch_latest_release()
        log(f"Latest release: {tag}")
    except Exception as e:
        log(f"WARNING: Could not fetch release info (network issue?): {e}")
        log("Skipping auto-update — service will continue normally.")
        # Sortie propre : ne pas faire échouer systemd
        sys.exit(0)

    state = load_state()
    installed = state.get("installed_version")

    if installed == latest_version:
        log(f"Already up to date (v{installed}). Nothing to do.")
        save_state({**state, "last_update_check": datetime.now().isoformat()})
        sys.exit(0)

    log(f"Update needed: {installed or 'not installed'} → {latest_version}")

    # Find which services are running before update
    import subprocess
    running_services = []
    for svc in SERVICES:
        r = subprocess.run(["systemctl", "is-active", "--quiet", svc])
        if r.returncode == 0:
            running_services.append(svc)

    try:
        install_version(latest_version, tag, assets)
        log(f"Successfully updated to v{latest_version}")
        restart_services(running_services)
        send_discord(
            f"✅ **frp mis à jour** sur `{platform.node()}`\n"
            f"• `{installed or 'non installé'}` → `v{latest_version}`\n"
            f"• Services redémarrés: {', '.join(running_services) if running_services else 'aucun'}"
        )
    except Exception as e:
        log(f"ERROR during update: {e}")
        send_discord(
            f"❌ **frp auto-update échoué** sur `{platform.node()}`\n"
            f"• Version cible: `v{latest_version}`\n"
            f"• Erreur: {redact(e)}"
        )
        # Ne pas faire échouer le service systemd même si l'update rate
        sys.exit(0)


if __name__ == "__main__":
    main()
