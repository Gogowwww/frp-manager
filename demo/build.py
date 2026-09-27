#!/usr/bin/env python3
"""Démo statique de FRP Manager : la vraie interface (templates/), servie sans
serveur Python. demo.js remplace l'API, les WebSocket et les journaux par des
données fictives, gardées dans l'onglet (sessionStorage).

    python3 demo/build.py [--out dist-demo] [--version 0.0.51] [--bundle dossier] [--zip demo.zip]

Le dossier produit (--out) se sert tel quel par n'importe quel serveur web.
Pour un hébergement qui lance une application (cloudpod Webstrator, egg Python
Pterodactyl), --bundle et --zip produisent app.py (demo/serve.py) et le site
dans site/, démarrés par « python3 app.py ». C'est ce que publie
.forgejo/workflows/demo.yml dans le dépôt frp-manager-demo. Rien à installer :
bibliothèque standard uniquement."""

import argparse
import hashlib
import json
import re
import shutil
import subprocess
import zipfile
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
TEMPLATES = ROOT / "templates"
DEMO = Path(__file__).resolve().parent

# Adresses absolues du serveur Flask → pages statiques de la démo.
# Chaque motif doit être trouvé : si le code change, la construction échoue
# au lieu de produire une démo qui renvoie vers une page inexistante.
PATCHES = {
    "js/api.js": [("location.href = '/login';", "location.href = 'login.html';")],
    "js/main.js": [("location.href = '/login';", "location.href = 'login.html';")],
}
LOGIN_PATCHES = [("location.href = '/';", "location.href = 'index.html';")]


def git_version():
    try:
        out = subprocess.run(["git", "-C", str(ROOT), "describe", "--tags", "--abbrev=0"],
                             capture_output=True, text=True, check=True).stdout.strip()
        return out.lstrip("v") or "demo"
    except Exception:
        return "demo"


def patch(text, pairs, name):
    for old, new in pairs:
        if old not in text:
            raise SystemExit(f"[demo] motif introuvable dans {name} : {old!r}")
        text = text.replace(old, new)
    return text


def render(template, *, base, version, preload, icons, demo_tag):
    text = template.replace('{% include "partials/icons.html" %}', icons)
    text = re.sub(r"\{% for m in preload_modules %\}.*?\{% endfor %\}",
                  "".join(f'<link rel="modulepreload" href="{base}/{m}">\n' for m in preload),
                  text, flags=re.S)
    text = re.sub(r"\{\{ asset\('([^']+)'\) \}\}", lambda m: f"{base}/{m.group(1)}", text)
    # Pas d'état intégré : l'interface le demande à l'API (simulée)
    text = text.replace("{{ boot|tojson }}", "null")
    text = text.replace("{{ panel_version }}", version)
    # demo.js avant tout module : fetch et WebSocket sont remplacés avant le premier appel
    text = text.replace("</title>", "</title>\n" + demo_tag, 1)
    left = re.findall(r"\{\{.*?\}\}|\{%.*?%\}", text)
    if left:
        raise SystemExit(f"[demo] balises Jinja non traitées : {left}")
    return text


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--out", default=str(ROOT / "dist-demo"))
    ap.add_argument("--version", default=None)
    ap.add_argument("--bundle", default=None, help="dossier app.py + site/ à produire (contenu remplacé)")
    ap.add_argument("--zip", default=None, help="archive app.py + site/ à produire")
    args = ap.parse_args()
    out = Path(args.out)
    version = (args.version or git_version()).lstrip("v")

    demo_js = (DEMO / "demo.js").read_text(encoding="utf-8").replace("__PANEL_VERSION__", version)

    # Empreinte du contenu : les assets vont dans v/<empreinte>/, comme sur le
    # vrai serveur. Chaque version a ses propres adresses (imports des modules
    # compris) et peut rester en cache indéfiniment.
    digest = hashlib.sha256(version.encode() + b"|" + demo_js.encode())
    for f in sorted((TEMPLATES / "assets").rglob("*")):
        if f.is_file():
            digest.update(f.relative_to(TEMPLATES).as_posix().encode() + b"|" + f.read_bytes())
    base = f"v/{digest.hexdigest()[:10]}"
    # « ./ » : un import de module exige un chemin relatif explicite
    href = f"./{base}"

    # Contenu vidé, dossier gardé (il peut être servi pendant la construction)
    out.mkdir(parents=True, exist_ok=True)
    for child in out.iterdir():
        shutil.rmtree(child) if child.is_dir() else child.unlink()
    shutil.copytree(TEMPLATES / "assets", out / base)
    for rel, pairs in PATCHES.items():
        f = out / base / rel
        f.write_text(patch(f.read_text(encoding="utf-8"), pairs, rel), encoding="utf-8")
    (out / base / "demo.js").write_text(demo_js, encoding="utf-8")

    preload = sorted(p.relative_to(out / base).as_posix()
                     for p in (out / base / "js").rglob("*.js")) + ["locales/fr.js"]
    common = {
        "base": href, "version": version, "preload": preload,
        "icons": (TEMPLATES / "partials" / "icons.html").read_text(encoding="utf-8"),
        "demo_tag": f'<script src="{href}/demo.js"></script>',
    }
    (out / "index.html").write_text(
        render((TEMPLATES / "index.html").read_text(encoding="utf-8"), **common), encoding="utf-8")
    login = patch((TEMPLATES / "login.html").read_text(encoding="utf-8"), LOGIN_PATCHES, "login.html")
    (out / "login.html").write_text(render(login, **common), encoding="utf-8")

    # Hébergeurs Apache : pas de liste des dossiers, pages HTML toujours relues
    # (les assets, sous v/<empreinte>/, peuvent rester en cache indéfiniment).
    (out / ".htaccess").write_text(
        "Options -Indexes\n"
        "DirectoryIndex index.html\n"
        "<IfModule mod_headers.c>\n"
        '  <FilesMatch "\\.html$">\n'
        '    Header set Cache-Control "no-cache"\n'
        "  </FilesMatch>\n"
        "</IfModule>\n", encoding="utf-8")
    (out / "v" / ".htaccess").write_text(
        "<IfModule mod_headers.c>\n"
        '  Header set Cache-Control "public, max-age=31536000, immutable"\n'
        "</IfModule>\n", encoding="utf-8")
    (out / "version.json").write_text(json.dumps({"version": version, "assets": base}) + "\n", encoding="utf-8")
    n = sum(1 for f in out.rglob("*") if f.is_file())
    print(f"[demo] v{version} -> {out} ({n} fichiers, assets dans {base}/)")

    # Application : app.py sert site/ (nom lancé par défaut par les eggs Python)
    files = [(DEMO / "serve.py", "app.py")] + [
        (f, "site/" + f.relative_to(out).as_posix()) for f in sorted(out.rglob("*")) if f.is_file()]
    if args.bundle:
        bundle = Path(args.bundle)
        bundle.mkdir(parents=True, exist_ok=True)
        for child in bundle.iterdir():
            if child.name != ".git":
                shutil.rmtree(child) if child.is_dir() else child.unlink()
        for src, rel in files:
            (bundle / rel).parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(src, bundle / rel)
        print(f"[demo] application -> {bundle} (lancer : python3 app.py)")
    if args.zip:
        with zipfile.ZipFile(args.zip, "w", zipfile.ZIP_DEFLATED) as z:
            for src, rel in files:
                z.write(src, rel)
        print(f"[demo] archive -> {args.zip} (lancer : python3 app.py)")


if __name__ == "__main__":
    main()
