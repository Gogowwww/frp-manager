#!/usr/bin/env python3
"""
frp-autoupdate.py
Called by cron and at startup by systemd to auto-update frp binaries.
Sends a Discord notification on success or failure (optional).
"""

import os
import sys
import json
import shutil
import tarfile
import tempfile
import platform
import requests
from pathlib import Path
from datetime import datetime

# ── Config ────────────────────────────────────────────────────────────────
FRP_BIN_DIR    = Path("/usr/local/bin")
FRP_CONF_DIR   = Path("/etc/frp")
FRP_LOG_DIR    = Path("/var/log/frp")
FRP_STATE_FILE = Path("/var/lib/frp-manager/state.json")
GITHUB_RELEASES = "https://api.github.com/repos/fatedier/frp/releases/latest"
VERSION_APIS = [
    "https://api.github.com/repos/fatedier/frp/releases/latest",
    "https://mirror.ghproxy.com/https://api.github.com/repos/fatedier/frp/releases/latest",
]
RELEASE_MIRRORS = [
    "https://github.com/fatedier/frp/releases/download/{tag}/{filename}",
    "https://mirror.ghproxy.com/https://github.com/fatedier/frp/releases/download/{tag}/{filename}",
    "https://ghfast.top/https://github.com/fatedier/frp/releases/download/{tag}/{filename}",
    "https://gh-proxy.com/https://github.com/fatedier/frp/releases/download/{tag}/{filename}",
]

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
            version = tag.lstrip("v")
            assets  = data.get("assets", [])
            return version, tag, assets
        except Exception:
            continue
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
    tmp_path = None
    for mirror_tpl in RELEASE_MIRRORS:
        url = mirror_tpl.format(tag=tag, filename=filename)
        source = url.split('/')[2]
        log(f"  Tentative via {source}…")
        try:
            with requests.get(url, stream=True, timeout=120) as r:
                r.raise_for_status()
                with tempfile.NamedTemporaryFile(suffix=".tar.gz", delete=False) as tmp:
                    for chunk in r.iter_content(65536):
                        tmp.write(chunk)
                    tmp_path = Path(tmp.name)
            log(f"  Téléchargé depuis {source}")
            break
        except Exception as e:
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
            with tarfile.open(tmp_path, "r:gz") as tf:
                tf.extractall(tmpdir)
            extracted_dir = next(Path(tmpdir).iterdir())
            for binary in ["frps", "frpc"]:
                src = extracted_dir / binary
                dst = FRP_BIN_DIR / binary
                if src.exists():
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

    log("Extracting…")
    with tempfile.TemporaryDirectory() as tmpdir:
        with tarfile.open(tmp_path, "r:gz") as tf:
            tf.extractall(tmpdir)
        extracted_dir = next(Path(tmpdir).iterdir())
        for binary in ["frps", "frpc"]:
            src = extracted_dir / binary
            dst = FRP_BIN_DIR / binary
            if src.exists():
                shutil.copy2(str(src), str(dst))
                dst.chmod(0o755)
                log(f"  → {dst}")
    tmp_path.unlink(missing_ok=True)

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
        log(f"Discord notification failed: {e}")


def run_cmd(cmd):
    import subprocess
    r = subprocess.run(cmd, capture_output=True, text=True)
    return r.returncode == 0


def restart_services(services_to_restart):
    import subprocess
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
            f"• Erreur: {e}"
        )
        # Ne pas faire échouer le service systemd même si l'update rate
        sys.exit(0)


if __name__ == "__main__":
    main()
