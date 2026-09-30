#!/usr/bin/env python3
"""Serveur de la démo FRP Manager, pour un hébergement qui lance une seule
application (conteneur, « cloudpod »…). Bibliothèque standard uniquement :
aucune dépendance à installer.

    python3 app.py      (website/demo/serve.py, copié sous ce nom par build.py)

Sert le dossier site/ (placé à côté de ce fichier) sur le port $PORT, ou
$SERVER_PORT (Pterodactyl), 8080 par défaut ; toutes interfaces ($HOST pour
changer)."""

import gzip
import hashlib
import io
import os
import sys
from email.utils import formatdate
from functools import partial
from http.server import SimpleHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

SITE = Path(os.environ.get("DEMO_SITE") or Path(__file__).resolve().parent / "site")

# Fichiers texte servis compressés (gzip) à qui le demande : pages, feuilles de style, scripts,
# sitemap. Compressés une fois puis gardés en mémoire (le site pèse quelques centaines de Ko).
COMPRESSIBLE = {".html", ".css", ".js", ".json", ".svg", ".xml", ".txt"}
_gzipped = {}       # chemin → (mtime, corps compressé, ETag)


class Handler(SimpleHTTPRequestHandler):
    extensions_map = {**SimpleHTTPRequestHandler.extensions_map,
                      ".js": "text/javascript", ".css": "text/css", ".json": "application/json",
                      ".svg": "image/svg+xml", ".webp": "image/webp", ".html": "text/html; charset=utf-8"}

    def _compressed(self):
        """(chemin, type MIME, corps gzip, ETag) si la requête peut être servie compressée, sinon None."""
        if self.command not in ("GET", "HEAD") or "gzip" not in self.headers.get("Accept-Encoding", ""):
            return None
        path = Path(self.translate_path(self.path))
        if path.is_dir() and self.path.split("?", 1)[0].endswith("/"):
            path = path / "index.html"
        if path.suffix not in COMPRESSIBLE or not path.is_file():
            return None
        mtime = path.stat().st_mtime_ns
        hit = _gzipped.get(path)
        if not hit or hit[0] != mtime:
            raw = path.read_bytes()
            body = gzip.compress(raw, 9, mtime=0) if len(raw) > 512 else None
            hit = _gzipped[path] = (mtime, body, '"' + hashlib.sha256(raw).hexdigest()[:20] + '-gz"')
        return (path, self.guess_type(str(path)), hit[1], hit[2]) if hit[1] else None

    def send_head(self):
        found = self._compressed()
        if not found:
            return super().send_head()
        path, ctype, body, etag = found
        if self.headers.get("If-None-Match") == etag:
            self.send_response(304)
            self.send_header("ETag", etag)
            self.end_headers()
            return None
        self.send_response(200)
        self.send_header("Content-Type", ctype)
        self.send_header("Content-Encoding", "gzip")
        self.send_header("Vary", "Accept-Encoding")
        self.send_header("ETag", etag)
        self.send_header("Last-Modified", formatdate(path.stat().st_mtime, usegmt=True))
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        return io.BytesIO(body)

    def list_directory(self, path):
        self.send_error(404)          # pas de liste des dossiers
        return None

    def send_error(self, code, message=None, explain=None):
        """Page 404 du site (404.html) plutôt que la page d'erreur brute."""
        page = SITE / "404.html"
        if code != 404 or not page.is_file() or self.command not in ("GET", "HEAD"):
            return super().send_error(code, message, explain)
        body = page.read_bytes()
        self.send_response(404)
        self.send_header("Content-Type", "text/html; charset=utf-8")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        if self.command == "GET":
            self.wfile.write(body)

    def end_headers(self):
        # Assets sous v/<empreinte>/ (démo) ou assets/<empreinte>/ (site) : jamais modifiés, gardés en cache ;
        # les pages sont relues à chaque visite (nouvelle version de la démo).
        # Pas de chemin quand la requête est illisible (ex. HTTPS envoyé à ce
        # port HTTP) : la réponse d'erreur passe aussi par ici.
        path = getattr(self, "path", "").split("?", 1)[0]
        if path.startswith(("/v/", "/demo/v/", "/assets/")):
            self.send_header("Cache-Control", "public, max-age=31536000, immutable")
        else:
            self.send_header("Cache-Control", "no-cache")
        self.send_header("X-Content-Type-Options", "nosniff")
        self.send_header("Referrer-Policy", "same-origin")
        super().end_headers()

    def log_message(self, fmt, *args):
        pass                          # pas de journal par requête


def main():
    if not (SITE / "index.html").is_file():
        sys.exit(f"Démo introuvable : {SITE}/index.html")
    host = os.environ.get("HOST", "0.0.0.0")
    # SERVER_PORT : port attribué par un panel Pterodactyl
    port = int(os.environ.get("PORT") or os.environ.get("SERVER_PORT") or "8080")
    server = ThreadingHTTPServer((host, port), partial(Handler, directory=str(SITE)))
    server.daemon_threads = True
    print(f"Démo FRP Manager sur http://{host}:{port}", flush=True)
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        pass


if __name__ == "__main__":
    main()
