#!/usr/bin/env python3
"""Serveur de la démo FRP Manager, pour un hébergement qui lance une seule
application (conteneur, « cloudpod »…). Bibliothèque standard uniquement :
aucune dépendance à installer.

    python3 app.py      (website/demo/serve.py, copié sous ce nom par build.py)

Sert le dossier site/ (placé à côté de ce fichier) sur le port $PORT, ou
$SERVER_PORT (Pterodactyl), 8080 par défaut ; toutes interfaces ($HOST pour
changer)."""

import os
import sys
from functools import partial
from http.server import SimpleHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

SITE = Path(os.environ.get("DEMO_SITE") or Path(__file__).resolve().parent / "site")


class Handler(SimpleHTTPRequestHandler):
    extensions_map = {**SimpleHTTPRequestHandler.extensions_map,
                      ".js": "text/javascript", ".css": "text/css", ".json": "application/json",
                      ".svg": "image/svg+xml", ".webp": "image/webp", ".html": "text/html; charset=utf-8"}

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
