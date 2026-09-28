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
    "js/main.js": [
        ("location.href = '/login';", "location.href = 'login.html';"),
        # Titre de chaque page : « Pare-feu — FRP Manager · démo » (moteurs de recherche, onglets)
        ("document.title = `${page.title()} — FRP Manager`;",
         "document.title = `${page.title()} — FRP Manager · Live demo`;"),
    ],
}
LOGIN_PATCHES = [("location.href = '/';", "location.href = 'index.html';")]

# ── Référencement de la démo ────────────────────────────────────────────────
REPO_URL = "https://github.com/Gogowwww/frp-manager"
SEO_TITLE = "FRP Manager — live demo of the self-hosted web panel for frp (frps & frpc)"
SEO_DESC = ("Try FRP Manager in your browser: a free, open-source web GUI and dashboard for frp. "
            "Manage frps and frpc tunnels, open ports, an nftables firewall, live logs and updates "
            "without the command line. Sample data, nothing to install.")


def seo_head(site, version):
    """Balises <head> de la page d'accueil de la démo (partages, moteurs de recherche)."""
    ld = {
        "@context": "https://schema.org",
        "@type": "SoftwareApplication",
        "name": "FRP Manager",
        "description": SEO_DESC,
        "applicationCategory": "DeveloperApplication",
        "applicationSubCategory": "Network tunnel / reverse proxy management",
        "operatingSystem": "Linux",
        "softwareVersion": version,
        "url": site + "/",
        "image": site + "/og.png",
        "downloadUrl": REPO_URL + "/releases/latest",
        "codeRepository": REPO_URL,
        "license": "https://www.apache.org/licenses/LICENSE-2.0",
        "isAccessibleForFree": True,
        "offers": {"@type": "Offer", "price": "0", "priceCurrency": "EUR"},
        "keywords": "frp, frps, frpc, frp panel, frp dashboard, frp web ui, reverse proxy, tunnel, "
                    "port forwarding, self-hosted, homelab, nftables firewall",
        "sameAs": [REPO_URL],
    }
    esc = lambda s: s.replace("&", "&amp;").replace('"', "&quot;").replace("<", "&lt;")
    return "\n".join([
        f'<meta name="description" content="{esc(SEO_DESC)}">',
        '<meta name="robots" content="index, follow, max-image-preview:large">',
        f'<link rel="canonical" href="{site}/">',
        '<meta name="theme-color" content="#0e1319">',
        '<meta property="og:type" content="website">',
        '<meta property="og:site_name" content="FRP Manager">',
        f'<meta property="og:title" content="{esc(SEO_TITLE)}">',
        f'<meta property="og:description" content="{esc(SEO_DESC)}">',
        f'<meta property="og:url" content="{site}/">',
        f'<meta property="og:image" content="{site}/og.png">',
        '<meta property="og:image:width" content="1280">',
        '<meta property="og:image:height" content="640">',
        '<meta property="og:image:alt" content="FRP Manager, the self-hosted web panel for frp: firewall page of the live demo">',
        '<meta property="og:locale" content="en_US">',
        '<meta property="og:locale:alternate" content="fr_FR">',
        '<meta name="twitter:card" content="summary_large_image">',
        f'<meta name="twitter:title" content="{esc(SEO_TITLE)}">',
        f'<meta name="twitter:description" content="{esc(SEO_DESC)}">',
        f'<meta name="twitter:image" content="{site}/og.png">',
        f'<script type="application/ld+json">{json.dumps(ld, ensure_ascii=False)}</script>',
    ])


# Sans JavaScript (robots qui n'exécutent pas les scripts, aperçus de liens) :
# une vraie présentation au lieu de « nécessite JavaScript ».
NOSCRIPT = f"""<noscript><main style="max-width:760px;margin:0 auto;padding:40px 20px;font-family:system-ui,sans-serif;line-height:1.6">
<h1>FRP Manager — self-hosted web panel for frp</h1>
<p>FRP Manager is a free, open-source web interface (GUI and dashboard) for <a href="https://github.com/fatedier/frp">frp</a>, the fast reverse proxy.
It manages <strong>frps</strong> (the server) and <strong>frpc</strong> (the client) from a browser: start and stop services, open ports and tunnels
(TCP, UDP, HTTP, HTTPS, STCP, XTCP), edit the configuration, follow live logs, filter who can connect with an <strong>nftables firewall</strong>
and update frp in one click. Linux with systemd, or Docker.</p>
<p>This live demo runs the real interface with sample data. <strong>Enable JavaScript</strong> to try it.</p>
<p><a href="{REPO_URL}">Source code, documentation and installation on GitHub</a> ·
<a href="{REPO_URL}/releases/latest">Download the latest release</a></p>
</main></noscript>"""


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
    # Ni CSP ni session sur un site statique : pas de nonce, jeton CSRF factice
    text = text.replace(' nonce="{{ csp_nonce() }}"', "")
    text = text.replace("{{ csrf_token() }}", "demo")
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
    ap.add_argument("--site-url", default="https://demo-frp-manager.gogow.fr",
                    help="adresse publique de la démo (canonical, Open Graph, sitemap)")
    args = ap.parse_args()
    site = args.site_url.rstrip("/")
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
    index = patch((TEMPLATES / "index.html").read_text(encoding="utf-8"), [
        ('<html lang="fr">', '<html lang="en">'),       # langue servie par défaut aux visiteurs
        ("<title>FRP Manager</title>", f"<title>{SEO_TITLE}</title>\n{seo_head(site, version)}"),
        ('<noscript><p style="padding:24px">FRP Manager nécessite JavaScript.</p></noscript>', NOSCRIPT),
    ], "index.html")
    (out / "index.html").write_text(render(index, **common), encoding="utf-8")
    login = patch((TEMPLATES / "login.html").read_text(encoding="utf-8"), LOGIN_PATCHES + [
        ("<title>", '<meta name="robots" content="noindex, follow">\n<title>'),   # page sans intérêt en recherche
    ], "login.html")
    (out / "login.html").write_text(render(login, **common), encoding="utf-8")

    shutil.copyfile(DEMO / "og.png", out / "og.png")
    (out / "robots.txt").write_text(
        f"User-agent: *\nAllow: /\n\nSitemap: {site}/sitemap.xml\n", encoding="utf-8")
    (out / "sitemap.xml").write_text(
        '<?xml version="1.0" encoding="UTF-8"?>\n'
        '<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">\n'
        f"  <url><loc>{site}/</loc><changefreq>weekly</changefreq><priority>1.0</priority></url>\n"
        "</urlset>\n", encoding="utf-8")

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
