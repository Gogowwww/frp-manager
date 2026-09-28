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


# Langue du journal : français si la langue du système l'est, anglais sinon
_LOCALE = os.environ.get("LC_ALL") or os.environ.get("LC_MESSAGES") or os.environ.get("LANG") or ""
FR = _LOCALE.lower().startswith("fr")


def M(fr, en):
    return fr if FR else en


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
                    log(M(f"  ATTENTION : sommes de contrôle obtenues via le miroir tiers {url.split('/')[2]}",
                          f"  WARNING: checksums obtained through the third-party mirror {url.split('/')[2]}"))
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
                raise ValueError(M("archive refusée : trop d'entrées", "archive rejected: too many entries"))
            parts = PurePosixPath(m.name).parts
            if m.name.startswith(("/", "\\")) or ".." in parts or "\\" in m.name:
                raise ValueError(M(f"archive refusée : chemin suspect {m.name!r}", f"archive rejected: suspicious path {m.name!r}"))
            base = parts[-1] if parts else ""
            if m.issym() or m.islnk():
                if base in ("frps", "frpc"):
                    raise ValueError(M(f"archive refusée : {m.name} est un lien", f"archive rejected: {m.name} is a link"))
                continue
            if not m.isfile():
                continue
            total += m.size
            if total > ARCHIVE_MAX_UNPACKED:
                raise ValueError(M("archive refusée : trop volumineuse une fois décompressée",
                                   "archive rejected: too large once decompressed"))
            if base not in ("frps", "frpc") or len(parts) > 2 or base in found:
                continue
            src, out = tf.extractfile(m), Path(dest) / base
            with open(out, "wb") as f:
                shutil.copyfileobj(src, f, 65536)
            if out.stat().st_size != m.size:
                raise ValueError(M("archive refusée : taille incohérente", "archive rejected: inconsistent size"))
            with open(out, "rb") as f:
                if f.read(4) != b"\x7fELF":
                    raise ValueError(M(f"archive refusée : {m.name} n'est pas un exécutable Linux",
                                     f"archive rejected: {m.name} is not a Linux executable"))
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
                raise ValueError(M(f"tag inattendu : {tag!r}", f"unexpected tag: {tag!r}"))
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
    raise RuntimeError(M("Tous les endpoints de version sont inaccessibles", "All version endpoints are unreachable"))


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

    log(M(f"Téléchargement de {filename}…", f"Downloading {filename}…"))
    expected = fetch_checksums(tag).get(filename)
    if not expected:
        log(M(f"  ATTENTION : pas de somme de contrôle pour {filename}, intégrité non vérifiable",
              f"  WARNING: no checksum for {filename}, integrity cannot be verified"))
    tmp_path = None
    for url in release_urls(tag, filename):
        source = url.split('/')[2]
        third = not url.startswith("https://github.com/")
        if third and not expected:
            log(M(f"  {source} ignoré : miroir tiers sans somme de contrôle",
                  f"  {source} skipped: third-party mirror without a checksum"))
            continue
        log(M(f"  Tentative via {source}…", f"  Trying {source}…")
            + (M(" (miroir tiers)", " (third-party mirror)") if third else ""))
        try:
            digest, size, candidate = hashlib.sha256(), 0, None
            with requests.get(url, stream=True, timeout=120) as r:
                r.raise_for_status()
                with tempfile.NamedTemporaryFile(suffix=".tar.gz", delete=False) as tmp:
                    candidate = Path(tmp.name)
                    for chunk in r.iter_content(65536):
                        size += len(chunk)
                        if size > DOWNLOAD_MAX_BYTES:
                            raise ValueError(M("fichier trop volumineux", "file too large"))
                        digest.update(chunk)
                        tmp.write(chunk)
            if expected and not hmac.compare_digest(digest.hexdigest(), expected):
                candidate.unlink(missing_ok=True)
                log(M(f"  {source} : somme SHA-256 différente de celle publiée, fichier rejeté",
                      f"  {source}: SHA-256 differs from the published one, file rejected"))
                continue
            tmp_path = candidate
            log(M(f"  Téléchargé depuis {source}", f"  Downloaded from {source}")
                + (M(", SHA-256 vérifiée", ", SHA-256 verified") if expected else M(" (non vérifié)", " (not verified)")))
            break
        except Exception as e:
            if candidate:
                candidate.unlink(missing_ok=True)
            log(M(f"  Échec {source} : {e}", f"  {source} failed: {e}"))
            continue

    if not tmp_path:
        raise RuntimeError(M("Tous les miroirs de téléchargement ont échoué", "All download mirrors failed"))

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
        log(M(f"Arrêt des services : {', '.join(running_svcs)}", f"Stopping services: {', '.join(running_svcs)}"))
        for svc in running_svcs:
            _sp.run(["systemctl", "stop", svc])

    log(M("Extraction…", "Extracting…"))
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
            log(M(f"Redémarrage des services : {', '.join(running_svcs)}", f"Restarting services: {', '.join(running_svcs)}"))
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
        log(M(f"Échec de la notification Discord : {redact(e)}", f"Discord notification failed: {redact(e)}"))


def run_cmd(cmd):
    import subprocess
    r = subprocess.run(cmd, capture_output=True, text=True)
    return r.returncode == 0


def restart_services(services_to_restart):
    for svc in services_to_restart:
        ok = run_cmd(["systemctl", "is-active", "--quiet", svc])
        if ok:
            log(M(f"Redémarrage de {svc}…", f"Restarting {svc}…"))
            run_cmd(["systemctl", "restart", svc])


def main():
    log(M("Vérification des mises à jour de frp…", "frp auto-update check starting…"))
    try:
        latest_version, tag, assets = fetch_latest_release()
        log(M(f"Dernière version : {tag}", f"Latest release: {tag}"))
    except Exception as e:
        log(M(f"ATTENTION : informations de version introuvables (réseau ?) : {e}",
              f"WARNING: Could not fetch release info (network issue?): {e}"))
        log(M("Mise à jour ignorée : le service continue normalement.", "Skipping auto-update: service will continue normally."))
        # Sortie propre : ne pas faire échouer systemd
        sys.exit(0)

    state = load_state()
    installed = state.get("installed_version")

    if installed == latest_version:
        log(M(f"Déjà à jour (v{installed}). Rien à faire.", f"Already up to date (v{installed}). Nothing to do."))
        save_state({**state, "last_update_check": datetime.now().isoformat()})
        sys.exit(0)

    log(M(f"Mise à jour nécessaire : {installed or 'non installé'} → {latest_version}",
          f"Update needed: {installed or 'not installed'} → {latest_version}"))

    # Find which services are running before update
    import subprocess
    running_services = []
    for svc in SERVICES:
        r = subprocess.run(["systemctl", "is-active", "--quiet", svc])
        if r.returncode == 0:
            running_services.append(svc)

    try:
        install_version(latest_version, tag, assets)
        log(M(f"Mis à jour en v{latest_version}", f"Successfully updated to v{latest_version}"))
        restart_services(running_services)
        restarted = ", ".join(running_services) if running_services else M("aucun", "none")
        send_discord(M(
            f"✅ **frp mis à jour** sur `{platform.node()}`\n"
            f"• `{installed or 'non installé'}` → `v{latest_version}`\n"
            f"• Services redémarrés : {restarted}",
            f"✅ **frp updated** on `{platform.node()}`\n"
            f"• `{installed or 'not installed'}` → `v{latest_version}`\n"
            f"• Services restarted: {restarted}")
        )
    except Exception as e:
        log(M(f"ERREUR pendant la mise à jour : {e}", f"ERROR during update: {e}"))
        send_discord(M(
            f"❌ **Mise à jour de frp échouée** sur `{platform.node()}`\n"
            f"• Version visée : `v{latest_version}`\n"
            f"• Erreur : {redact(e)}",
            f"❌ **frp auto-update failed** on `{platform.node()}`\n"
            f"• Target version: `v{latest_version}`\n"
            f"• Error: {redact(e)}")
        )
        # Ne pas faire échouer le service systemd même si l'update rate
        sys.exit(0)


if __name__ == "__main__":
    main()
