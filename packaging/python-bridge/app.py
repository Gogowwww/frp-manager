#!/usr/bin/env python3
"""FRP Manager : passage d'une installation Python (jusqu'à 0.0.57) au programme Go.

L'archive des versions Go contient ce fichier à la place de l'ancien app.py. La
mise à jour d'un panel Python copie app.py dans /opt/frp-manager puis relance le
service : c'est alors ce fichier qui démarre. Il télécharge le programme Go de la
même version (somme SHA-256 vérifiée), l'installe, fait pointer le service
systemd dessus et redémarre le panel. La configuration (/etc/frp-manager) ne
change pas : comptes, mots de passe, webhooks, pare-feu et réglages sont gardés.

Bibliothèque standard uniquement : tourne avec le Python de l'ancienne installation.
"""

import hashlib
import io
import json
import os
import platform
import re
import subprocess
import sys
import time
import urllib.request
import zipfile

VERSION = "__VERSION__"           # gravée par le workflow de release
REPO = "Gogowwww/frp-manager"
INSTALL_DIR = os.path.dirname(os.path.abspath(__file__))
BINARY = os.path.join(INSTALL_DIR, "frp-manager")
UNIT = "/etc/systemd/system/frp-manager.service"
STATE = "/var/lib/frp-manager/state.json"
ARCHES = {"x86_64": "amd64", "amd64": "amd64", "aarch64": "arm64", "arm64": "arm64",
          "armv7l": "armv7", "armv8l": "armv7"}
MAX_BYTES = 100 * 1024 * 1024


def log(msg):
    print("[frp-manager] " + msg, flush=True)


def fetch(url):
    req = urllib.request.Request(url, headers={"User-Agent": "frp-manager-bridge"})
    with urllib.request.urlopen(req, timeout=120) as r:
        data = r.read(MAX_BYTES + 1)
    if len(data) > MAX_BYTES:
        raise ValueError("fichier trop volumineux")
    return data


def install_binary():
    arch = ARCHES.get(platform.machine().lower())
    if not arch:
        raise RuntimeError("architecture non prise en charge : " + platform.machine())
    base = "https://github.com/%s/releases/download/v%s/" % (REPO, VERSION)
    archive = fetch(base + "frp-manager.zip")
    m = re.match(r"\s*([0-9a-fA-F]{64})\b", fetch(base + "frp-manager.zip.sha256").decode("ascii", "replace"))
    if not m or hashlib.sha256(archive).hexdigest() != m.group(1).lower():
        raise RuntimeError("somme SHA-256 de frp-manager.zip différente de celle publiée")
    name = "bin/frp-manager-linux-" + arch
    with zipfile.ZipFile(io.BytesIO(archive)) as zf:
        members = [n for n in zf.namelist() if n.replace("\\", "/").endswith("/" + name) or n == name]
        if not members:
            raise RuntimeError(name + " absent de l'archive")
        data = zf.read(members[0])
        script = next((n for n in zf.namelist() if n.replace("\\", "/").endswith("/install.sh")), None)
        install_sh = zf.read(script) if script else None
    if not data.startswith(b"\x7fELF"):
        raise RuntimeError(name + " n'est pas un exécutable Linux")
    tmp = BINARY + ".new"
    with open(tmp, "wb") as f:
        f.write(data)
    os.chmod(tmp, 0o755)
    out = subprocess.run([tmp, "--version"], stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=30)
    if out.returncode != 0:
        raise RuntimeError("le programme ne démarre pas : " + out.stdout.decode(errors="replace")[-200:])
    os.replace(tmp, BINARY)
    if install_sh:
        with open(os.path.join(INSTALL_DIR, "install.sh"), "wb") as f:
            f.write(install_sh)
        os.chmod(os.path.join(INSTALL_DIR, "install.sh"), 0o755)
    log("programme Go installé : %s (%s)" % (BINARY, arch))


def update_unit():
    with open(UNIT) as f:
        text = f.read()
    lines = []
    for line in text.splitlines():
        if line.startswith("ExecStart="):
            line = "ExecStart=" + BINARY
        elif line.startswith("ExecStartPost=") and "frp-autoupdate" in line:
            continue
        lines.append(line)
    with open(UNIT, "w") as f:
        f.write("\n".join(lines) + "\n")
    subprocess.run(["systemctl", "daemon-reload"], timeout=60)
    log("service frp-manager : ExecStart=" + BINARY)


def note_version():
    try:
        state = {}
        if os.path.exists(STATE):
            with open(STATE) as f:
                state = json.load(f)
        state["panel_version"] = VERSION
        with open(STATE, "w") as f:
            json.dump(state, f, indent=2)
    except (OSError, ValueError):
        pass


def main():
    if os.geteuid() != 0:
        sys.exit("[frp-manager] à lancer en root (service systemd)")
    delay = 15
    while True:
        try:
            install_binary()
            update_unit()
            note_version()
            break
        except Exception as e:  # réseau coupé, GitHub injoignable… : on réessaie
            log("passage au programme Go impossible pour l'instant (%s) : nouvel essai dans %d s" % (e, delay))
            time.sleep(delay)
            delay = min(delay * 2, 600)
    log("redémarrage du panel sur le programme Go")
    subprocess.Popen(["systemctl", "--no-block", "restart", "frp-manager"])
    time.sleep(30)


if __name__ == "__main__":
    main()
