#!/usr/bin/env python3
"""Captures d'écran du site, prises dans la démo avec un navigateur sans fenêtre.

    python3 website/shots.py --url http://127.0.0.1:18231 [--browser chemin]

Le site doit être servi (python3 website/build.py puis demo/serve.py). Produit
website/img/<page>-<langue>.webp (panel en thème sombre, sans le bandeau de la
démo). À relancer quand l'interface change ; les images sont versionnées.
Demande Pillow (conversion en WebP) et Chrome, Chromium ou Edge.
"""

import argparse
import shutil
import subprocess
import tempfile
from pathlib import Path

from PIL import Image

HERE = Path(__file__).resolve().parent
PAGES = ["dashboard", "ports", "firewall", "logs", "config"]
# Captures des fonctionnalités : sans la barre latérale du panel (240 px CSS),
# qui se répète d'une image à l'autre et rendrait le contenu illisible en petit
CROP_SIDEBAR = {"ports", "firewall", "logs"}
SCALE = 1.5
BROWSERS = [
    r"C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe",
    r"C:\Program Files\Google\Chrome\Application\chrome.exe",
    "chromium", "chromium-browser", "google-chrome", "microsoft-edge",
]


def find_browser(given):
    for cand in ([given] if given else BROWSERS):
        if cand and (Path(cand).is_file() or shutil.which(cand)):
            return cand
    raise SystemExit("Aucun navigateur Chrome/Chromium/Edge trouvé (--browser)")


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--url", default="http://127.0.0.1:18231")
    ap.add_argument("--browser")
    ap.add_argument("--width", type=int, default=1280)
    ap.add_argument("--height", type=int, default=780)
    args = ap.parse_args()
    browser = find_browser(args.browser)
    for lang, accept in (("en", "en-US"), ("fr", "fr-FR")):
        # Un profil neuf par langue : la démo mémorise la langue dans localStorage
        with tempfile.TemporaryDirectory() as profile, tempfile.TemporaryDirectory() as tmp:
            for page in PAGES:
                png = Path(tmp) / f"{page}.png"
                subprocess.run([
                    browser, "--headless=new", "--disable-gpu", "--hide-scrollbars", "--no-first-run",
                    f"--user-data-dir={profile}", f"--lang={accept}", f"--accept-lang={accept}",
                    "--force-dark-mode", "--blink-settings=preferredColorScheme=0",
                    f"--window-size={args.width},{args.height}", f"--force-device-scale-factor={SCALE}",
                    "--virtual-time-budget=6000", f"--screenshot={png}",
                    f"{args.url}/demo/?shot#/{page}",
                ], check=True, capture_output=True)
                out = HERE / "img" / f"{page}-{lang}.webp"
                im = Image.open(png).convert("RGB")
                if page in CROP_SIDEBAR:
                    im = im.crop((int(240 * SCALE), 0, im.width, im.height))
                im.save(out, "WEBP", quality=86, method=6)
                print(f"{out.name}: {out.stat().st_size // 1024} Ko")


if __name__ == "__main__":
    main()
