#!/usr/bin/env python3
"""Audit SEO du site construit par website/build.py.

    python3 website/build.py --out dist-site
    python3 website/seo_check.py dist-site [--site-url https://frp-manager.gogow.fr]

Vérifie, pour chaque page indexable : titre et description (longueur, unicité), un seul h1,
canonical, hreflang réciproques, Open Graph, images (alt, dimensions), données structurées
(JSON valide), liens internes, présence dans le sitemap. Code de sortie 1 s'il y a une
erreur ; les avertissements (longueurs limites) n'échouent pas. Bibliothèque standard seulement."""

import argparse
import json
import re
import sys
from html.parser import HTMLParser
from pathlib import Path
from urllib.parse import urlsplit
import xml.etree.ElementTree as ET

TITLE_MAX, DESC_MIN, DESC_MAX = 62, 70, 165
SITEMAP_NS = {"s": "http://www.sitemaps.org/schemas/sitemap/0.9", "x": "http://www.w3.org/1999/xhtml"}


class Page(HTMLParser):
    def __init__(self):
        super().__init__(convert_charrefs=True)
        self.title, self.h1, self.meta, self.links, self.imgs, self.anchors, self.ids = "", [], {}, [], [], [], set()
        self.jsonld, self.lang, self._in, self._buf = [], "", None, ""

    def handle_starttag(self, tag, attrs):
        a = dict(attrs)
        if a.get("id"):
            self.ids.add(a["id"])
        if tag == "html":
            self.lang = a.get("lang", "")
        elif tag == "meta":
            key = a.get("name") or a.get("property")
            if key:
                self.meta.setdefault(key, []).append(a.get("content", ""))
        elif tag == "link":
            self.links.append(a)
        elif tag == "img":
            self.imgs.append(a)
        elif tag == "a" and a.get("href"):
            self.anchors.append(a["href"])
        elif tag == "script" and a.get("type") == "application/ld+json":
            self._in, self._buf = "ld", ""
        elif tag in ("title", "h1"):
            self._in, self._buf = tag, ""

    def handle_data(self, data):
        if self._in:
            self._buf += data

    def handle_endtag(self, tag):
        if self._in == "ld" and tag == "script":
            self.jsonld.append(self._buf)
        elif self._in == tag == "title":
            self.title = self._buf.strip()
        elif self._in == tag == "h1":
            self.h1.append(re.sub(r"\s+", " ", self._buf).strip())
        else:
            return
        self._in = None


def url_to_file(root, path):
    p = root / path.lstrip("/")
    return p / "index.html" if path.endswith("/") or p.is_dir() else p


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("site_dir")
    ap.add_argument("--site-url", default="https://frp-manager.gogow.fr")
    args = ap.parse_args()
    root, site = Path(args.site_dir), args.site_url.rstrip("/")
    errors, warnings = [], []
    err = lambda page, msg: errors.append(f"{page}: {msg}")
    warn = lambda page, msg: warnings.append(f"{page}: {msg}")

    tree = ET.parse(root / "sitemap.xml").getroot()
    sitemap = {}
    for u in tree.findall("s:url", SITEMAP_NS):
        loc = u.findtext("s:loc", namespaces=SITEMAP_NS)
        alts = {a.get("hreflang"): a.get("href") for a in u.findall("x:link", SITEMAP_NS)}
        sitemap[loc] = {"alts": alts, "lastmod": u.findtext("s:lastmod", namespaces=SITEMAP_NS)}
    robots = (root / "robots.txt").read_text(encoding="utf-8")
    if f"Sitemap: {site}/sitemap.xml" not in robots:
        err("robots.txt", "ligne Sitemap absente")

    pages, titles, descs = {}, {}, {}
    for f in sorted(root.rglob("*.html")):
        rel = "/" + f.relative_to(root).as_posix()
        # Les pages de la démo (interface complète) ont leur propre contrôle plus bas
        page = Page()
        page.feed(f.read_text(encoding="utf-8"))
        pages[rel] = page

    for rel, page in pages.items():
        path = rel[: -len("index.html")] if rel.endswith("/index.html") else rel
        robots_meta = " ".join(page.meta.get("robots", []))
        noindex = "noindex" in robots_meta
        canon = next((link["href"] for link in page.links if link.get("rel") == "canonical"), None)
        if rel.startswith(("/demo/v/", "/assets/")) or rel in ("/demo/login.html",)                 or Path(rel).name.startswith("google") or (noindex and rel != "/404.html"):
            continue
        if not page.lang:
            err(rel, "attribut lang absent de <html>")
        if noindex:
            if f"{site}{path}" in sitemap:
                err(rel, "noindex mais présente dans le sitemap")
            continue
        if not page.title:
            err(rel, "titre absent")
        elif len(page.title) > TITLE_MAX:
            warn(rel, f"titre de {len(page.title)} caractères (> {TITLE_MAX}) : {page.title}")
        if page.title:
            titles.setdefault(page.title, []).append(rel)
        desc = (page.meta.get("description") or [""])[0]
        if not desc:
            err(rel, "meta description absente")
        else:
            if not DESC_MIN <= len(desc) <= DESC_MAX:
                warn(rel, f"description de {len(desc)} caractères (attendu {DESC_MIN}-{DESC_MAX})")
            descs.setdefault(desc, []).append(rel)
        if rel.startswith("/demo/"):
            if len(page.h1) > 1:
                err(rel, f"{len(page.h1)} balises h1")
        elif len(page.h1) != 1:
            err(rel, f"{len(page.h1)} balises h1 (une attendue)")
        if canon != f"{site}{path}":
            err(rel, f"canonical {canon!r} au lieu de {site}{path}")
        if f"{site}{path}" not in sitemap:
            err(rel, "absente du sitemap")
        for key in ("og:title", "og:description", "og:url", "og:image", "twitter:card"):
            if not page.meta.get(key):
                err(rel, f"balise {key} absente")
        if page.meta.get("og:url", [""])[0] != f"{site}{path}":
            err(rel, "og:url différente du canonical")
        alts = {link.get("hreflang"): link["href"] for link in page.links if link.get("rel") == "alternate" and link.get("hreflang")}
        if not rel.startswith("/demo/"):
            for lang in ("en", "fr", "x-default"):
                if lang not in alts:
                    err(rel, f"hreflang {lang} absent")
            for lang, href in alts.items():
                other = urlsplit(href).path
                back = pages.get(other + "index.html" if other.endswith("/") else other)
                if back is None:
                    err(rel, f"hreflang {lang} vers une page introuvable : {href}")
                    continue
                back_alts = {link.get("hreflang"): link["href"] for link in back.links if link.get("rel") == "alternate"}
                if lang != "x-default" and back_alts.get(page.lang) != f"{site}{path}":
                    err(rel, f"hreflang non réciproque avec {other}")
        for img in page.imgs:
            if "alt" not in img:
                err(rel, f"image sans alt : {img.get('src')}")
            if not (img.get("width") and img.get("height")):
                warn(rel, f"image sans dimensions (décalage de mise en page) : {img.get('src')}")
        for block in page.jsonld:
            try:
                data = json.loads(block)
            except ValueError as e:
                err(rel, f"JSON-LD invalide : {e}")
                continue
            nodes = data if isinstance(data, list) else data.get("@graph", [data]) if isinstance(data, dict) else []
            for node in nodes:
                if not isinstance(node, dict) or "@type" not in node:
                    err(rel, "JSON-LD sans @type")
        for href in page.anchors:
            if href.startswith(("#", "mailto:", "javascript:")):
                continue
            parts = urlsplit(href)
            if parts.scheme in ("http", "https"):
                if parts.netloc != urlsplit(site).netloc:
                    continue
                target = parts.path or "/"
            else:
                target = parts.path
                if not target.startswith("/"):
                    target = path.rsplit("/", 1)[0] + "/" + target
            file = url_to_file(root, target)
            if not file.exists():
                err(rel, f"lien interne cassé : {href}")
            elif parts.fragment and file.suffix == ".html":
                tgt = pages.get("/" + file.relative_to(root).as_posix())
                if tgt and parts.fragment not in tgt.ids:
                    err(rel, f"ancre introuvable : {href}")

    for what, seen in (("titre", titles), ("description", descs)):
        for text, where in seen.items():
            if len(where) > 1:
                err(", ".join(where[:3]), f"{what} en double : {text[:70]}")
    for loc, info in sitemap.items():
        p = urlsplit(loc).path
        if not url_to_file(root, p).exists():
            err("sitemap.xml", f"URL sans fichier : {loc}")
        if not info["lastmod"]:
            warn("sitemap.xml", f"lastmod absent : {loc}")
    for w in warnings:
        print("WARN ", w)
    for e in errors:
        print("ERROR", e)
    print(f"[seo] {len(pages)} pages HTML, {len(sitemap)} URL dans le sitemap : "
          f"{len(errors)} erreur(s), {len(warnings)} avertissement(s)")
    return 1 if errors else 0


if __name__ == "__main__":
    sys.exit(main())
