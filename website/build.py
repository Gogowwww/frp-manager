#!/usr/bin/env python3
"""Site vitrine de FRP Manager : page d'accueil, documentation et démo en ligne.

    python3 website/build.py [--out dist-site] [--version 0.0.51]
                             [--site-url https://frp-manager.gogow.fr]
                             [--bundle dossier] [--zip site.zip]

Produit un site statique bilingue (anglais à la racine, français sous /fr/) :
  /                  page d'accueil
  /docs/<page>/      documentation, rendue depuis les fichiers Markdown du dépôt
                     (docs/, SECURITY.md, CHANGELOG.md…) : toujours à jour
  /demo/             la démo (vraie interface, données fictives, demo/build.py)
Bibliothèque standard uniquement. --bundle et --zip produisent app.py
(demo/serve.py) + site/, comme la démo seule auparavant : c'est ce que publie
.forgejo/workflows/demo.yml dans le dépôt frp-manager-demo.
"""

import argparse
import html
import json
import re
import shutil
import subprocess
import sys
import zipfile
from pathlib import Path

HERE = Path(__file__).resolve().parent
ROOT = HERE.parent
sys.path.insert(0, str(HERE))
import markdown  # noqa: E402

REPO = "Gogowwww/frp-manager"
REPO_URL = f"https://github.com/{REPO}"
LANGS = ("en", "fr")

# ── Documentation : pages et fichiers sources ────────────────────────────────
# Une page sans version anglaise affiche la version française, avec un avertissement.
DOCS = [
    ("installation", {"en": "docs/installation.md", "fr": "docs/installation.fr.md"}),
    ("docker", {"en": "docs/docker.md", "fr": "docs/docker.fr.md"}),
    ("security", {"en": "SECURITY.md", "fr": "SECURITY.fr.md"}),
    ("contributing", {"en": "CONTRIBUTING.md", "fr": "CONTRIBUTING.fr.md"}),
    ("code-of-conduct", {"en": "CODE_OF_CONDUCT.md", "fr": "CODE_OF_CONDUCT.fr.md"}),
    ("changelog", {"en": "CHANGELOG.md", "fr": "CHANGELOG.fr.md"}),
]
# Fichier du dépôt → (langue, page) ; None comme langue = langue de la page qui fait le lien
DOC_FILES = {
    "installation.md": ("en", "installation"), "installation.fr.md": ("fr", "installation"),
    "docker.md": ("en", "docker"), "docker.fr.md": ("fr", "docker"),
    "SECURITY.md": ("en", "security"), "SECURITY.fr.md": ("fr", "security"),
    "CONTRIBUTING.md": ("en", "contributing"), "CONTRIBUTING.fr.md": ("fr", "contributing"),
    "CODE_OF_CONDUCT.md": ("en", "code-of-conduct"), "CODE_OF_CONDUCT.fr.md": ("fr", "code-of-conduct"),
    "CHANGELOG.md": ("en", "changelog"), "CHANGELOG.fr.md": ("fr", "changelog"),
    "README.md": ("en", ""), "README.fr.md": ("fr", ""),
}

T = {
    "en": {
        "html_lang": "en", "locale": "en_US",
        "nav_features": "Features", "nav_docs": "Documentation", "nav_demo": "Live demo",
        "nav_source": "Source code", "menu": "Menu", "theme": "Switch theme",
        "other_lang": "Français", "other_lang_code": "fr",
        "skip": "Skip to content",
        "footer_about": "Self-hosted web panel for frp, released under the Apache 2.0 license.",
        "footer_ai": "Developed with the help of an AI coding assistant (Claude). "
                     "Every version ships as a pre-release first, to be tested before it is promoted.",
        "footer_project": "Project", "footer_docs": "Documentation",
        "releases": "Releases", "issues": "Issues", "license": "License",
        "docs_title": "Documentation", "on_this_page": "On this page",
        "edit": "Edit this page on GitHub", "only_fr": "This page is only available in French for now.",
        "docs_intro": "Install, configure and run FRP Manager. These pages are generated from the "
                      "Markdown files of the repository, so they always match the latest version.",
        "copy": "Copy", "copied": "Copied",
        "notfound_title": "Page not found",
        "notfound_text": "This address does not match any page of the site. It may have moved when the "
                         "documentation was reorganized.",
        "notfound_home": "Back to the home page", "notfound_docs": "Browse the documentation",
        "doc_names": {"installation": "Installation and configuration", "docker": "Docker: privileges and risks",
                      "security": "Security policy", "contributing": "Contributing",
                      "code-of-conduct": "Code of conduct", "changelog": "Changelog"},
        "doc_desc": {"installation": "Requirements, install script, Docker images, first launch, network access, "
                                     "configuration keys, lost password, uninstall.",
                     "docker": "What pid: host and nsenter imply, opening the panel to the network, "
                               "reducing privileges.",
                     "security": "Reporting a vulnerability, supported versions, threat model.",
                     "contributing": "Development setup, tests, conventions, translations.",
                     "code-of-conduct": "How we treat each other in the project.",
                     "changelog": "What changed in each version."},
    },
    "fr": {
        "html_lang": "fr", "locale": "fr_FR",
        "nav_features": "Fonctionnalités", "nav_docs": "Documentation", "nav_demo": "Démo en ligne",
        "nav_source": "Code source", "menu": "Menu", "theme": "Changer de thème",
        "other_lang": "English", "other_lang_code": "en",
        "skip": "Aller au contenu",
        "footer_about": "Panel web auto-hébergé pour frp, publié sous licence Apache 2.0.",
        "footer_ai": "Développé avec l'aide d'un assistant de programmation IA (Claude). "
                     "Chaque version sort d'abord en pré-release, pour être testée avant de devenir une release.",
        "footer_project": "Projet", "footer_docs": "Documentation",
        "releases": "Versions", "issues": "Issues", "license": "Licence",
        "docs_title": "Documentation", "on_this_page": "Sur cette page",
        "edit": "Modifier cette page sur GitHub", "only_fr": "",
        "docs_intro": "Installer, configurer et faire tourner FRP Manager. Ces pages sont générées à partir "
                      "des fichiers Markdown du dépôt : elles correspondent toujours à la dernière version.",
        "copy": "Copier", "copied": "Copié",
        "notfound_title": "Page introuvable",
        "notfound_text": "Cette adresse ne correspond à aucune page du site. Elle a peut-être changé lors de "
                         "la réorganisation de la documentation.",
        "notfound_home": "Retour à l'accueil", "notfound_docs": "Parcourir la documentation",
        "doc_names": {"installation": "Installation et configuration", "docker": "Docker : privilèges et risques",
                      "security": "Politique de sécurité", "contributing": "Contribuer",
                      "code-of-conduct": "Code de conduite", "changelog": "Journal des modifications"},
        "doc_desc": {"installation": "Prérequis, script d'installation, images Docker, première ouverture, "
                                     "accès réseau, clés de configuration, mot de passe perdu, désinstallation.",
                     "docker": "Ce qu'impliquent pid: host et nsenter, ouvrir le panel au réseau, "
                               "réduire les privilèges.",
                     "security": "Signaler une vulnérabilité, versions prises en charge, modèle de menace.",
                     "contributing": "Environnement de développement, tests, conventions, traductions.",
                     "code-of-conduct": "Comment nous nous comportons dans le projet.",
                     "changelog": "Ce qui a changé à chaque version."},
    },
}


CURRENT = ' aria-current="page"'


def prefix(lang):
    return "" if lang == "en" else "/fr"


def doc_url(lang, slug):
    return f"{prefix(lang)}/docs/{slug}/" if slug else f"{prefix(lang)}/"


def icon(name, cls="i"):
    return f'<svg class="{cls}" aria-hidden="true"><use href="#i-{name}"/></svg>'


def esc(text):
    return html.escape(str(text), quote=True)


# ── Contenu de la page d'accueil ─────────────────────────────────────────────
HOME = {
    "en": {
        "title": "FRP Manager: a self-hosted web panel for frp (frps and frpc)",
        "desc": "FRP Manager is a free, open-source web interface for frp. Manage frps and frpc, "
                "open ports and tunnels, filter access with an nftables firewall, follow live logs and "
                "update frp from your browser.",
        "h1": "Run frp from your browser",
        "lead": "FRP Manager is a self-hosted web panel for <strong>frps</strong> and <strong>frpc</strong>. "
                "Open ports, filter who reaches them, read the logs and update frp, without editing TOML "
                "files over SSH.",
        "cta_demo": "Try the live demo", "cta_install": "Install",
        "meta": "Free and open source, Apache 2.0. Linux with systemd, or Docker.",
        "route_caption": "Each port is shown as the path it opens.",
        "route": [("globe", "Visitor", "anywhere on the Internet"),
                  ("server", "vps.example.net:25565", "frps, on your public server"),
                  ("laptop", "127.0.0.1:25565", "frpc, next to your Minecraft server")],
        "route_links": ["connects to", "frp tunnel"],
        "shot_alt": "FRP Manager dashboard: an frps server, an frpc client and an frpc Docker container, all running",
        "features_title": "Everything frp needs, in one place",
        "features_lead": "The panel finds your frp services on its own, systemd units and Docker containers "
                         "alike, and keeps your configuration files as plain TOML.",
        "rows": [
            {"img": "ports", "alt": "List of ports opened by frpc, each with its public and local address",
             "title": "Ports you can read",
             "text": "Every tunnel is listed as the path it opens, <code>vps.example.net:25565 → 127.0.0.1:25565</code>. "
                     "The guided editor covers <code>tcp</code>, <code>udp</code>, <code>http</code>, <code>https</code>, "
                     "<code>stcp</code>, <code>xtcp</code> and visitors, with PROXY protocol, encryption and compression. "
                     "Settings it does not handle are kept as they are.",
             "points": ["Changes pile up as a draft: save, or save and restart frpc",
                        "Each field shows its TOML key",
                        "A warning before stopping the frpc you are connected through"]},
            {"img": "firewall", "alt": "Firewall page with block and allow-only rules and the ports opened by frps",
             "title": "A firewall for frps ports",
             "text": "Decide who can connect to the ports frps opens: allow only, or block, by address, network or "
                     "whole provider (AS number, prefixes fetched from RIPEstat). Rules are applied in the kernel "
                     "with nftables, before Docker's NAT, and keep working if the panel stops.",
             "points": ["Test an address before applying, with a warning if you would lock yourself out",
                        "Blocked connections in real time, with their provider",
                        "Community lists you can subscribe to, or publish"]},
            {"img": "logs", "alt": "Live frpc logs with errors and warnings highlighted",
             "title": "Live logs",
             "text": "Follow the systemd journal, a log file or a Docker container as it is written, over "
                     "WebSocket with an automatic fallback. Errors and warnings stand out, and a filter narrows "
                     "the lines down.",
             "points": ["Works through a reverse proxy",
                        "Containers with or without a TTY"]},
        ],
        "more": [
            ("dashboard", "Automatic detection", "frps and frpc services, binaries, configs and Docker containers, one card each."),
            ("config", "Configuration forms", "frps and frpc settings in sections; TOML is checked before it is written."),
            ("download", "One-click updates", "frp downloaded and checked against its published SHA-256; the panel updates itself."),
            ("box", "Docker ready", "Run the panel in a container and drive frp on the host, or manage frp containers."),
            ("monitor", "Light and dark", "Readable on desktop and mobile, in English and French."),
            ("shield", "Locked by default", "Mandatory setup, argon2id passwords, CSRF protection, localhost only."),
        ],
        "demo_title": "Try it before installing",
        "demo_lead": "The live demo runs the real interface with sample data. Everything can be clicked; "
                     "nothing is actually changed, and it resets when you close the tab.",
        "demo_launch": "Launch the demo here", "demo_full": "Open the demo in full screen",
        "start_title": "Get started",
        "start_lead": "On the machine that runs frps or frpc. The panel installs frp for you if it is missing.",
        "tab_docker": "Docker Compose", "tab_script": "Install script",
        "steps_docker": [
            ("Download the compose file and start the container",
             "curl -O https://raw.githubusercontent.com/Gogowwww/frp-manager/main/docker-compose.yml\ndocker compose up -d"),
        ],
        "steps_script": [
            ("Download the latest release and run the installer as root",
             "curl -LO https://github.com/Gogowwww/frp-manager/releases/latest/download/frp-manager.zip\n"
             "unzip frp-manager.zip && cd frp-manager && sudo bash install.sh"),
        ],
        "steps_common": [
            ("Reach the panel through an SSH tunnel: it only listens on 127.0.0.1",
             "ssh -L 8765:127.0.0.1:8765 user@server"),
            ("Open https://127.0.0.1:8765 and create the administrator account", None),
        ],
        "start_more": "Opening the panel to your network, reverse proxy, configuration keys: "
                      "<a href=\"/docs/installation/\">installation guide</a>.",
        "why_title": "Why a panel for frp",
        "why_lead": "frp is a single binary configured by TOML files. Running it by hand works well; managing it "
                    "day to day is where the time goes. FRP Manager keeps the same files and adds a layer on top.",
        "why_head": ("Task", "frp by hand", "With FRP Manager"),
        "why_rows": [
            ("Add or change a tunnel", "Edit <code>frpc.toml</code>, restart frpc", "Guided form, TOML checked, save and restart in one click"),
            ("See what is exposed", "Read the configs", "List of ports with their path, per client"),
            ("Several frps or frpc", "One unit and one file per instance to track", "Detected automatically, one card each"),
            ("Filter who reaches frps", "Write nftables or iptables rules", "Rules by IP, network or AS, tested before applying"),
            ("Follow the logs", "<code>journalctl -fu frpc</code> over SSH", "Live logs in the browser"),
            ("Update frp", "Download, check, replace the binaries, restart", "One click, SHA-256 checked, services restarted"),
        ],
        "why_note": "Removing the panel leaves frp running as before: your configuration stays plain TOML.",
        "sec_title": "Built for a machine that faces the Internet",
        "sec_lead": "The panel controls exposed ports and runs as root, so it starts locked down.",
        "sec_points": [
            ("No open panel", "Until an administrator account exists, the initial setup page is the only thing it serves."),
            ("Strong passwords", "Hashed with argon2id, 12 characters minimum, attempts limited per IP."),
            ("Localhost first", "A new install listens on 127.0.0.1; opening it to the network is your call."),
            ("Hardened web layer", "CSRF tokens, strict cookies, Content-Security-Policy, no clickjacking."),
            ("Checked downloads", "frp archives and panel updates are verified against their published SHA-256."),
            ("No shell injection", "Commands are argument lists, names are allow-listed, archives extracted safely."),
        ],
        "sec_docker": "<strong>Using Docker?</strong> The container uses <code>pid: host</code> and "
                      "<code>nsenter</code> to drive systemd on the host: that is root-equivalent access to the "
                      "host. <a href=\"/docs/docker/\">What it implies</a>.",
        "sec_more": "Threat model and vulnerability reporting: <a href=\"/docs/security/\">security policy</a>.",
        "faq_title": "Questions",
        "faq": [
            ("Does it replace frp?",
             "No. FRP Manager drives the official frps and frpc binaries and reads and writes their usual TOML "
             "files. You can keep editing them by hand, and frp keeps running if you remove the panel."),
            ("Do I need to install frp first?",
             "No. The install script downloads frps and frpc, creates their systemd services and leaves them "
             "stopped until you configure them. frp is then updated from the Updates page."),
            ("Does it work with frp running in Docker?",
             "Yes. The panel detects frps and frpc containers through the Docker socket and can start, stop and "
             "restart them, read their logs and list their ports."),
            ("How do I reach the panel from another computer?",
             "A new install only listens on 127.0.0.1. Use an SSH tunnel, a local reverse proxy (nginx, Caddy), "
             "or set the listening address to 0.0.0.0 in Settings and filter access by IP."),
            ("I lost my password.",
             "Run <code>python3 app.py --reset-password</code> in the install directory (or through "
             "<code>docker exec</code>), then restart the panel."),
            ("Which languages are available?",
             "English and French, messages from the server included. The panel follows your browser's "
             "language; you can change it in Settings, Preferences."),
        ],
        "final_title": "Take back your ports",
        "final_text": "Open the demo, or install it on the machine that runs frp.",
    },
    "fr": {
        "title": "FRP Manager : panel web auto-hébergé pour frp (frps et frpc)",
        "desc": "FRP Manager est une interface web libre et gratuite pour frp. Pilotez frps et frpc, ouvrez "
                "des ports et des tunnels, filtrez l'accès avec un pare-feu nftables, suivez les journaux en "
                "direct et mettez frp à jour depuis le navigateur.",
        "h1": "Pilotez frp depuis votre navigateur",
        "lead": "FRP Manager est un panel web auto-hébergé pour <strong>frps</strong> et <strong>frpc</strong>. "
                "Ouvrez des ports, filtrez qui les atteint, lisez les journaux et mettez frp à jour, sans éditer "
                "de fichiers TOML en SSH.",
        "cta_demo": "Essayer la démo", "cta_install": "Installer",
        "meta": "Libre et gratuit, Apache 2.0. Linux avec systemd, ou Docker.",
        "route_caption": "Chaque port est affiché comme le chemin qu'il ouvre.",
        "route": [("globe", "Visiteur", "n'importe où sur Internet"),
                  ("server", "vps.example.net:25565", "frps, sur votre serveur public"),
                  ("laptop", "127.0.0.1:25565", "frpc, à côté de votre serveur Minecraft")],
        "route_links": ["se connecte à", "tunnel frp"],
        "shot_alt": "Tableau de bord de FRP Manager : un serveur frps, un client frpc et un conteneur frpc, tous en marche",
        "features_title": "Tout ce dont frp a besoin, au même endroit",
        "features_lead": "Le panel trouve seul vos services frp, unités systemd comme conteneurs Docker, et "
                         "garde vos fichiers de configuration en TOML.",
        "rows": [
            {"img": "ports", "alt": "Liste des ports ouverts par frpc, chacun avec son adresse publique et locale",
             "title": "Des ports lisibles",
             "text": "Chaque tunnel est affiché comme le chemin qu'il ouvre, <code>vps.example.net:25565 → 127.0.0.1:25565</code>. "
                     "L'éditeur guidé couvre <code>tcp</code>, <code>udp</code>, <code>http</code>, <code>https</code>, "
                     "<code>stcp</code>, <code>xtcp</code> et les visiteurs, avec PROXY protocol, chiffrement et "
                     "compression. Les réglages qu'il ne gère pas sont conservés tels quels.",
             "points": ["Les modifications s'accumulent en brouillon : enregistrer, ou enregistrer et redémarrer frpc",
                        "Chaque champ affiche sa clé TOML",
                        "Un avertissement avant d'arrêter le frpc par lequel vous êtes connecté"]},
            {"img": "firewall", "alt": "Page pare-feu avec des règles de blocage et d'autorisation et les ports ouverts par frps",
             "title": "Un pare-feu pour les ports de frps",
             "text": "Décidez qui peut se connecter aux ports qu'ouvre frps : autoriser seulement, ou bloquer, par "
                     "adresse, réseau ou fournisseur entier (numéro d'AS, préfixes récupérés auprès de RIPEstat). "
                     "Les règles sont appliquées dans le noyau avec nftables, avant le NAT de Docker, et continuent "
                     "de filtrer si le panel s'arrête.",
             "points": ["Tester une adresse avant d'appliquer, avec une alerte si vous alliez vous bloquer",
                        "Connexions bloquées en temps réel, avec leur fournisseur",
                        "Listes communautaires auxquelles s'abonner, ou à publier"]},
            {"img": "logs", "alt": "Journaux frpc en direct avec erreurs et avertissements mis en évidence",
             "title": "Journaux en direct",
             "text": "Suivez le journal systemd, un fichier de log ou un conteneur Docker au fil de l'eau, par "
                     "WebSocket avec repli automatique. Erreurs et avertissements ressortent, un filtre réduit les "
                     "lignes affichées.",
             "points": ["Fonctionne derrière un reverse proxy",
                        "Conteneurs avec ou sans TTY"]},
        ],
        "more": [
            ("dashboard", "Détection automatique", "Services frps et frpc, binaires, configurations et conteneurs Docker, une carte chacun."),
            ("config", "Formulaires de configuration", "Réglages de frps et frpc par sections ; le TOML est vérifié avant d'être écrit."),
            ("download", "Mises à jour en un clic", "frp téléchargé et vérifié par sa somme SHA-256 publiée ; le panel se met à jour seul."),
            ("box", "Prêt pour Docker", "Faites tourner le panel en conteneur pour piloter frp sur l'hôte, ou gérez des conteneurs frp."),
            ("monitor", "Clair ou sombre", "Lisible sur ordinateur et mobile, en français et en anglais."),
            ("shield", "Verrouillé par défaut", "Configuration initiale imposée, mots de passe argon2id, CSRF, écoute locale."),
        ],
        "demo_title": "Essayez avant d'installer",
        "demo_lead": "La démo en ligne fait tourner la vraie interface avec des données fictives. Tout est "
                     "cliquable, rien n'est réellement modifié, et tout revient à zéro à la fermeture de l'onglet.",
        "demo_launch": "Lancer la démo ici", "demo_full": "Ouvrir la démo en plein écran",
        "start_title": "Démarrer",
        "start_lead": "Sur la machine qui fait tourner frps ou frpc. Le panel installe frp s'il manque.",
        "tab_docker": "Docker Compose", "tab_script": "Script d'installation",
        "steps_docker": [
            ("Télécharger le fichier compose et lancer le conteneur",
             "curl -O https://raw.githubusercontent.com/Gogowwww/frp-manager/main/docker-compose.yml\ndocker compose up -d"),
        ],
        "steps_script": [
            ("Télécharger la dernière version et lancer l'installation en root",
             "curl -LO https://github.com/Gogowwww/frp-manager/releases/latest/download/frp-manager.zip\n"
             "unzip frp-manager.zip && cd frp-manager && sudo bash install.sh"),
        ],
        "steps_common": [
            ("Atteindre le panel par un tunnel SSH : il n'écoute que sur 127.0.0.1",
             "ssh -L 8765:127.0.0.1:8765 utilisateur@serveur"),
            ("Ouvrir https://127.0.0.1:8765 et créer le compte administrateur", None),
        ],
        "start_more": "Ouvrir le panel à votre réseau, reverse proxy, clés de configuration : "
                      "<a href=\"/fr/docs/installation/\">guide d'installation</a>.",
        "why_title": "Pourquoi un panel pour frp",
        "why_lead": "frp est un binaire unique configuré par des fichiers TOML. Le faire tourner à la main "
                    "fonctionne bien ; c'est la gestion au quotidien qui prend du temps. FRP Manager garde les "
                    "mêmes fichiers et ajoute une couche par-dessus.",
        "why_head": ("Tâche", "frp à la main", "Avec FRP Manager"),
        "why_rows": [
            ("Ajouter ou modifier un tunnel", "Éditer <code>frpc.toml</code>, redémarrer frpc", "Formulaire guidé, TOML vérifié, enregistrer et redémarrer en un clic"),
            ("Voir ce qui est exposé", "Lire les configurations", "Liste des ports avec leur chemin, par client"),
            ("Plusieurs frps ou frpc", "Un service et un fichier par instance à suivre", "Détectés automatiquement, une carte chacun"),
            ("Filtrer qui atteint frps", "Écrire des règles nftables ou iptables", "Règles par IP, réseau ou AS, testées avant application"),
            ("Suivre les journaux", "<code>journalctl -fu frpc</code> en SSH", "Journaux en direct dans le navigateur"),
            ("Mettre à jour frp", "Télécharger, vérifier, remplacer les binaires, redémarrer", "Un clic, SHA-256 vérifiée, services redémarrés"),
        ],
        "why_note": "Retirer le panel laisse frp tourner comme avant : votre configuration reste du TOML.",
        "sec_title": "Conçu pour une machine exposée à Internet",
        "sec_lead": "Le panel contrôle des ports exposés et tourne en root : il démarre verrouillé.",
        "sec_points": [
            ("Pas de panel ouvert", "Tant qu'aucun compte administrateur n'existe, il ne sert que la page de configuration initiale."),
            ("Mots de passe solides", "Hachés en argon2id, 12 caractères minimum, tentatives limitées par IP."),
            ("Local d'abord", "Une nouvelle installation écoute sur 127.0.0.1 ; l'ouvrir au réseau est votre choix."),
            ("Couche web durcie", "Jetons CSRF, cookies stricts, Content-Security-Policy, pas de clickjacking."),
            ("Téléchargements vérifiés", "Archives frp et mises à jour du panel contrôlées par leur somme SHA-256 publiée."),
            ("Pas d'injection shell", "Commandes en listes d'arguments, noms sur liste blanche, archives extraites sans risque."),
        ],
        "sec_docker": "<strong>Sous Docker ?</strong> Le conteneur utilise <code>pid: host</code> et "
                      "<code>nsenter</code> pour piloter le systemd de l'hôte : c'est un accès équivalent à root "
                      "sur l'hôte. <a href=\"/fr/docs/docker/\">Ce que cela implique</a>.",
        "sec_more": "Modèle de menace et signalement des vulnérabilités : "
                    "<a href=\"/fr/docs/security/\">politique de sécurité</a>.",
        "faq_title": "Questions",
        "faq": [
            ("Est-ce que ça remplace frp ?",
             "Non. FRP Manager pilote les binaires officiels frps et frpc, et lit et écrit leurs fichiers TOML "
             "habituels. Vous pouvez continuer à les éditer à la main, et frp continue de tourner si vous "
             "retirez le panel."),
            ("Faut-il installer frp avant ?",
             "Non. Le script d'installation télécharge frps et frpc, crée leurs services systemd et les laisse "
             "arrêtés jusqu'à ce que vous les configuriez. frp se met ensuite à jour depuis la page Mises à jour."),
            ("Est-ce que ça marche avec frp sous Docker ?",
             "Oui. Le panel détecte les conteneurs frps et frpc par le socket Docker : il peut les démarrer, "
             "les arrêter, les redémarrer, lire leurs journaux et lister leurs ports."),
            ("Comment accéder au panel depuis un autre ordinateur ?",
             "Une nouvelle installation n'écoute que sur 127.0.0.1. Utilisez un tunnel SSH, un reverse proxy "
             "local (nginx, Caddy), ou passez l'adresse d'écoute à 0.0.0.0 dans Réglages et filtrez l'accès par IP."),
            ("J'ai perdu mon mot de passe.",
             "Lancez <code>python3 app.py --reset-password</code> dans le dossier d'installation (ou par "
             "<code>docker exec</code>), puis redémarrez le panel."),
            ("Quelles langues sont disponibles ?",
             "Français et anglais, messages du serveur compris. Le panel suit la langue du navigateur ; "
             "vous pouvez la changer dans Réglages, Préférences."),
        ],
        "final_title": "Reprenez la main sur vos ports",
        "final_text": "Ouvrez la démo, ou installez le panel sur la machine qui fait tourner frp.",
    },
}


# ── Gabarit commun ───────────────────────────────────────────────────────────
def shell(lang, path, alt_path, title, desc, body, ctx, *, section="", noindex=False):
    t = T[lang]
    other = t["other_lang_code"]
    site, version = ctx["site"], ctx["version"]
    canonical = site + path
    nav = [
        (f"{prefix(lang)}/#features", t["nav_features"], "features"),
        (f"{prefix(lang)}/docs/", t["nav_docs"], "docs"),
        ("/demo/", t["nav_demo"], "demo"),
    ]
    nav_html = "".join(
        f'<a href="{href}"{CURRENT if key == section else ""}>{label}</a>'
        for href, label, key in nav)
    footer_docs = "".join(f'<li><a href="{doc_url(lang, slug)}">{esc(t["doc_names"][slug])}</a></li>'
                          for slug, _ in DOCS)
    robots = '<meta name="robots" content="noindex">' if noindex else ""
    alternates = "" if noindex else (
        f'<link rel="alternate" hreflang="{lang}" href="{site}{path}">\n'
        f'<link rel="alternate" hreflang="{other}" href="{site}{alt_path}">\n'
        f'<link rel="alternate" hreflang="x-default" href="{site}{alt_path if lang == "fr" else path}">')
    return f"""<!DOCTYPE html>
<html lang="{t['html_lang']}">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="color-scheme" content="light dark">
<title>{esc(title)}</title>
<meta name="description" content="{esc(desc)}">
{robots}
<link rel="canonical" href="{canonical}">
{alternates}
<meta property="og:type" content="website">
<meta property="og:site_name" content="FRP Manager">
<meta property="og:title" content="{esc(title)}">
<meta property="og:description" content="{esc(desc)}">
<meta property="og:url" content="{canonical}">
<meta property="og:image" content="{site}/og.png">
<meta property="og:image:width" content="1280">
<meta property="og:image:height" content="640">
<meta property="og:locale" content="{t['locale']}">
<meta name="twitter:card" content="summary_large_image">
<meta name="theme-color" content="#0e9f6e">
<link rel="icon" type="image/svg+xml" href="/assets/{ctx['asset_v']}/favicon.svg">
<script>try{{var th=localStorage.getItem('frpm.site.theme');if(th==='light'||th==='dark')document.documentElement.dataset.theme=th}}catch(e){{}}</script>
<link rel="preconnect" href="https://fonts.googleapis.com">
<link rel="preconnect" href="https://fonts.gstatic.com" crossorigin>
<link rel="stylesheet" href="https://fonts.googleapis.com/css2?family=IBM+Plex+Mono:wght@400;500&family=IBM+Plex+Sans:ital,wght@0,400;0,500;0,600;0,700;1,400&display=swap">
<link rel="stylesheet" href="/assets/{ctx['asset_v']}/site.css">
<script src="/assets/{ctx['asset_v']}/site.js" defer></script>
</head>
<body class="page-{section or 'other'}" data-copy="{esc(t['copy'])}" data-copied="{esc(t['copied'])}">
{ctx['icons']}
<a class="skip" href="#main">{t['skip']}</a>
<header class="top">
  <div class="top-in">
    <a class="brand" href="{prefix(lang)}/">{icon('logo', 'brand-mark')}<span>FRP Manager</span></a>
    <button class="nav-toggle" type="button" aria-expanded="false" aria-controls="nav">{icon('menu')}<span class="sr">{t['menu']}</span></button>
    <nav class="nav" id="nav">
      {nav_html}
      <a href="{REPO_URL}" rel="noopener">{t['nav_source']}</a>
      <span class="nav-tools">
        <a class="lang" href="{alt_path}" hreflang="{other}" lang="{other}">{t['other_lang']}</a>
        <button class="theme" type="button" title="{t['theme']}" aria-label="{t['theme']}">{icon('sun', 'i i-light')}{icon('moon', 'i i-dark')}</button>
      </span>
    </nav>
  </div>
</header>
<main id="main">
{body}
</main>
<footer class="foot">
  <div class="foot-in">
    <div class="foot-about">
      <a class="brand" href="{prefix(lang)}/">{icon('logo', 'brand-mark')}<span>FRP Manager</span></a>
      <p>{t['footer_about']}</p>
      <p class="foot-ai">{t['footer_ai']}</p>
      <p class="foot-version">v{esc(version)}</p>
    </div>
    <div>
      <h2>{t['footer_project']}</h2>
      <ul>
        <li><a href="/demo/">{t['nav_demo']}</a></li>
        <li><a href="{REPO_URL}" rel="noopener">{t['nav_source']}</a></li>
        <li><a href="{REPO_URL}/releases" rel="noopener">{t['releases']}</a></li>
        <li><a href="{REPO_URL}/issues" rel="noopener">{t['issues']}</a></li>
        <li><a href="{REPO_URL}/blob/main/LICENSE" rel="noopener">{t['license']}</a></li>
      </ul>
    </div>
    <div>
      <h2>{t['footer_docs']}</h2>
      <ul>{footer_docs}</ul>
    </div>
  </div>
</footer>
</body>
</html>
"""


# ── Page d'accueil ───────────────────────────────────────────────────────────
def code_block(cmd):
    return f'<div class="code"><pre><code>{esc(cmd)}</code></pre></div>'


def home_body(lang, ctx):
    c = HOME[lang]
    img = f"/assets/{ctx['asset_v']}/img"
    route = []
    for n, (ico, addr, label) in enumerate(c["route"]):
        if n:
            route.append(f'<li class="route-link" aria-hidden="true"><span>{esc(c["route_links"][n - 1])}</span></li>')
        mono = " mono" if n else ""
        route.append(f'<li class="route-node"><span class="route-ico">{icon(ico)}</span>'
                     f'<span><span class="route-addr{mono}">{esc(addr)}</span>'
                     f'<span class="route-label">{esc(label)}</span></span></li>')
    rows = []
    for n, row in enumerate(c["rows"]):
        points = "".join(f"<li>{icon('check-circle')}<span>{p}</span></li>" for p in row["points"])
        rows.append(f"""<article class="feature{' feature-flip' if n % 2 else ''}">
  <div class="feature-text">
    <h3>{row['title']}</h3>
    <p>{row['text']}</p>
    <ul class="checks">{points}</ul>
  </div>
  <figure class="shot"><img src="{img}/{row['img']}-{lang}.webp" alt="{esc(row['alt'])}" loading="lazy" width="1560" height="1170"></figure>
</article>""")
    more = "".join(f'<div class="more-item">{icon(ico)}<h3>{esc(title)}</h3><p>{esc(text)}</p></div>'
                   for ico, title, text in c["more"])

    def steps(items):
        return "".join(f"<li><p>{esc(text)}</p>{code_block(cmd) if cmd else ''}</li>" for text, cmd in items)
    why_head = "".join(f"<th>{h}</th>" for h in c["why_head"])
    why_rows = "".join(f"<tr><th scope=\"row\">{a}</th><td>{b}</td><td>{d}</td></tr>" for a, b, d in c["why_rows"])
    sec = "".join(f"<div><h3>{esc(a)}</h3><p>{esc(b)}</p></div>" for a, b in c["sec_points"])
    faq = "".join(f"<details><summary>{esc(q)}</summary><p>{a}</p></details>" for q, a in c["faq"])
    return f"""<section class="hero">
  <div class="wrap hero-in">
    <div class="hero-text">
      <p class="hero-version"><a href="{REPO_URL}/releases" rel="noopener">v{esc(ctx['version'])}</a></p>
      <h1>{c['h1']}</h1>
      <p class="lead">{c['lead']}</p>
      <div class="actions">
        <a class="btn btn-primary" href="/demo/">{icon('play')}{c['cta_demo']}</a>
        <a class="btn" href="#start">{icon('download')}{c['cta_install']}</a>
      </div>
      <p class="hero-meta">{c['meta']}</p>
    </div>
    <figure class="route">
      <ol>{''.join(route)}</ol>
      <figcaption>{c['route_caption']}</figcaption>
    </figure>
  </div>
  <div class="wrap">
    <figure class="window hero-shot">
      <div class="window-bar" aria-hidden="true"><span></span><span></span><span></span></div>
      <img src="{img}/dashboard-{lang}.webp" alt="{esc(c['shot_alt'])}" width="1920" height="1170" fetchpriority="high">
    </figure>
  </div>
</section>

<section class="section" id="features">
  <div class="wrap">
    <header class="section-head">
      <h2>{c['features_title']}</h2>
      <p>{c['features_lead']}</p>
    </header>
    {''.join(rows)}
    <div class="more">{more}</div>
  </div>
</section>

<section class="section section-demo" id="demo">
  <div class="wrap">
    <header class="section-head">
      <h2>{c['demo_title']}</h2>
      <p>{c['demo_lead']}</p>
    </header>
    <div class="window demo-frame" data-src="/demo/">
      <div class="window-bar" aria-hidden="true"><span></span><span></span><span></span></div>
      <button class="demo-launch" type="button">
        <img src="{img}/config-{lang}.webp" alt="" loading="lazy" width="1920" height="1170">
        <span class="btn btn-primary">{icon('play')}{c['demo_launch']}</span>
      </button>
    </div>
    <p class="demo-full"><a href="/demo/">{icon('external')}{c['demo_full']}</a></p>
  </div>
</section>

<section class="section" id="start">
  <div class="wrap narrow">
    <header class="section-head">
      <h2>{c['start_title']}</h2>
      <p>{c['start_lead']}</p>
    </header>
    <div class="tabs" data-tabs>
      <div class="tab-list" role="tablist">
        <button role="tab" type="button" aria-selected="true" aria-controls="tab-docker" id="tab-docker-btn">{c['tab_docker']}</button>
        <button role="tab" type="button" aria-selected="false" aria-controls="tab-script" id="tab-script-btn" tabindex="-1">{c['tab_script']}</button>
      </div>
      <div class="tab-panel" role="tabpanel" id="tab-docker" aria-labelledby="tab-docker-btn">
        <ol class="steps">{steps(c['steps_docker'] + c['steps_common'])}</ol>
      </div>
      <div class="tab-panel" role="tabpanel" id="tab-script" aria-labelledby="tab-script-btn" hidden>
        <ol class="steps">{steps(c['steps_script'] + c['steps_common'])}</ol>
      </div>
    </div>
    <p class="note">{c['start_more']}</p>
  </div>
</section>

<section class="section" id="why">
  <div class="wrap narrow">
    <header class="section-head">
      <h2>{c['why_title']}</h2>
      <p>{c['why_lead']}</p>
    </header>
    <div class="table compare"><table><thead><tr>{why_head}</tr></thead><tbody>{why_rows}</tbody></table></div>
    <p class="note">{c['why_note']}</p>
  </div>
</section>

<section class="section section-security" id="security">
  <div class="wrap">
    <header class="section-head">
      {icon('shield', 'i section-ico')}
      <h2>{c['sec_title']}</h2>
      <p>{c['sec_lead']}</p>
    </header>
    <div class="sec-grid">{sec}</div>
    <p class="callout">{icon('alert')}<span>{c['sec_docker']}</span></p>
    <p class="note">{c['sec_more']}</p>
  </div>
</section>

<section class="section" id="faq">
  <div class="wrap narrow">
    <header class="section-head"><h2>{c['faq_title']}</h2></header>
    <div class="faq">{faq}</div>
  </div>
</section>

<section class="final">
  <div class="wrap final-in">
    <div>
      <h2>{c['final_title']}</h2>
      <p>{c['final_text']}</p>
    </div>
    <div class="actions">
      <a class="btn btn-primary" href="/demo/">{icon('play')}{c['cta_demo']}</a>
      <a class="btn" href="{doc_url(lang, 'installation')}">{icon('download')}{c['cta_install']}</a>
    </div>
  </div>
</section>"""


# ── Documentation ────────────────────────────────────────────────────────────
def strip_lang_switch(text):
    """Retire la ligne « English · Français » en tête des pages : le site a la sienne."""
    lines = text.split("\n")
    head = [n for n, line in enumerate(lines[:6]) if "English" in line and "Français" in line]
    for n in reversed(head):
        del lines[n]
    return "\n".join(lines)


def make_link_fn(lang, src_path):
    base = Path(src_path).parent

    def fn(url):
        if re.match(r"^(https?:|mailto:|#)", url):
            return url
        path, _, frag = url.partition("#")
        frag = f"#{frag}" if frag else ""
        name = path.split("/")[-1]
        if name in DOC_FILES:
            target_lang, slug = DOC_FILES[name]
            return doc_url(target_lang or lang, slug) + frag
        resolved = (base / path).as_posix()
        parts = []
        for p in resolved.split("/"):
            if p == "..":
                if parts:
                    parts.pop()
            elif p not in ("", "."):
                parts.append(p)
        return f"{REPO_URL}/blob/main/{'/'.join(parts)}{frag}"
    return fn


def doc_page(lang, slug, sources, ctx):
    t = T[lang]
    src = sources.get(lang) or sources["fr"]
    content_lang = lang if lang in sources else "fr"
    text = strip_lang_switch((ROOT / src).read_text(encoding="utf-8"))
    body, headings = markdown.render(text, make_link_fn(lang, src))
    title = next((h for lvl, h, _ in headings if lvl == 1), t["doc_names"][slug])
    title = re.sub(r"[`*]", "", title)
    toc = "".join(f'<li class="toc-{lvl}"><a href="#{esc(a)}">{markdown.Renderer().inline(h)}</a></li>'
                  for lvl, h, a in headings if lvl in (2, 3))
    side = "".join(
        f'<li><a href="{doc_url(lang, s)}"{CURRENT if s == slug else ""}>'
        f'{esc(t["doc_names"][s])}</a></li>' for s, _ in DOCS)
    notice = (f'<p class="callout callout-info">{icon("info")}<span>{t["only_fr"]}</span></p>'
              if content_lang != lang else "")
    names = [s for s, _ in DOCS]
    i = names.index(slug)
    pager = []
    if i > 0:
        pager.append(f'<a class="prev" href="{doc_url(lang, names[i - 1])}">{esc(t["doc_names"][names[i - 1]])}</a>')
    if i + 1 < len(names):
        pager.append(f'<a class="next" href="{doc_url(lang, names[i + 1])}">{esc(t["doc_names"][names[i + 1]])}</a>')
    html_body = f"""<div class="wrap docs">
  <aside class="docs-side">
    <p class="docs-side-title"><a href="{prefix(lang)}/docs/">{t['docs_title']}</a></p>
    <ul>{side}</ul>
  </aside>
  <article class="prose" lang="{content_lang}">
    {notice}
    {body}
    <nav class="pager">{''.join(pager)}</nav>
    <p class="edit"><a href="{REPO_URL}/blob/main/{src}" rel="noopener">{icon('edit')}{t['edit']}</a></p>
  </article>
  <aside class="docs-toc">
    {f'<p class="docs-side-title">{t["on_this_page"]}</p><ul>{toc}</ul>' if toc else ''}
  </aside>
</div>"""
    alt = doc_url(T[lang]["other_lang_code"], slug)
    desc = t["doc_desc"][slug]
    return shell(lang, doc_url(lang, slug), alt, f"{title} — FRP Manager", desc, html_body, ctx, section="docs")


def docs_index(lang, ctx):
    t = T[lang]
    items = "".join(f'<li><a href="{doc_url(lang, s)}"><strong>{esc(t["doc_names"][s])}</strong>'
                    f'<span>{esc(t["doc_desc"][s])}</span></a></li>' for s, _ in DOCS)
    body = f"""<div class="wrap narrow docs-index">
  <h1>{t['docs_title']}</h1>
  <p class="lead">{t['docs_intro']}</p>
  <ul class="doc-list">{items}</ul>
</div>"""
    return shell(lang, f"{prefix(lang)}/docs/", f"{prefix(T[lang]['other_lang_code'])}/docs/",
                 f"{t['docs_title']} — FRP Manager", t["docs_intro"], body, ctx, section="docs")


def not_found(ctx):
    t = T["en"]
    tf = T["fr"]
    body = f"""<div class="wrap narrow notfound">
  <p class="notfound-code">404</p>
  <h1>{t['notfound_title']}</h1>
  <p>{t['notfound_text']}</p>
  <div class="actions"><a class="btn btn-primary" href="/">{t['notfound_home']}</a><a class="btn" href="/docs/">{t['notfound_docs']}</a></div>
  <div lang="fr" class="notfound-fr">
    <h2>{tf['notfound_title']}</h2>
    <p>{tf['notfound_text']}</p>
    <p><a href="/fr/">{tf['notfound_home']}</a> · <a href="/fr/docs/">{tf['notfound_docs']}</a></p>
  </div>
</div>"""
    return shell("en", "/404.html", "/fr/", f"{t['notfound_title']} — FRP Manager", t["notfound_text"],
                 body, ctx, noindex=True)


FAVICON = """<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 26 26"><path d="M13 2L23 7.5V18.5L13 24L3 18.5V7.5L13 2Z" stroke="#10b981" stroke-width="1.5" stroke-linejoin="round" fill="#101418"/><path d="M7.5 11h11M16 8.5l2.5 2.5L16 13.5" stroke="#10b981" stroke-width="1.4" stroke-linecap="round" stroke-linejoin="round" fill="none"/><path d="M18.5 15H7.5M10 12.5L7.5 15l2.5 2.5" stroke="#10b981" stroke-width="1.4" stroke-linecap="round" stroke-linejoin="round" fill="none"/></svg>
"""


# ── Construction ─────────────────────────────────────────────────────────────
def git_version():
    try:
        out = subprocess.run(["git", "-C", str(ROOT), "describe", "--tags", "--abbrev=0"],
                             capture_output=True, text=True, check=True).stdout.strip()
        return out.lstrip("v") or "dev"
    except Exception:
        return "dev"


def write(out, rel, text):
    p = out / rel
    p.parent.mkdir(parents=True, exist_ok=True)
    p.write_text(text, encoding="utf-8")


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--out", default=str(ROOT / "dist-site"))
    ap.add_argument("--version", default=None)
    ap.add_argument("--site-url", default="https://frp-manager.gogow.fr")
    ap.add_argument("--bundle", default=None, help="dossier app.py + site/ à produire (contenu remplacé)")
    ap.add_argument("--zip", default=None, help="archive app.py + site/ à produire")
    args = ap.parse_args()
    site = args.site_url.rstrip("/")
    version = (args.version or git_version()).lstrip("v")
    out = Path(args.out)

    out.mkdir(parents=True, exist_ok=True)
    for child in out.iterdir():
        shutil.rmtree(child) if child.is_dir() else child.unlink()

    # La démo d'abord, dans /demo/ (elle vide son propre dossier)
    subprocess.run([sys.executable, str(ROOT / "demo" / "build.py"), "--out", str(out / "demo"),
                    "--version", version, "--site-url", f"{site}/demo", "--embedded"], check=True)

    # Fichiers statiques sous /assets/<empreinte>/ : gardés en cache indéfiniment
    import hashlib
    assets = {"site.css": (HERE / "site.css").read_bytes(), "site.js": (HERE / "site.js").read_bytes(),
              "favicon.svg": FAVICON.encode()}
    # Captures prises dans la démo par website/shots.py (une par page et par langue)
    images = {p.name: p.read_bytes() for p in sorted((HERE / "img").glob("*.webp"))}
    digest = hashlib.sha256()
    for name, data in sorted(assets.items()) + sorted(images.items()):
        digest.update(name.encode() + b"|" + data)
    asset_v = digest.hexdigest()[:10]
    adir = out / "assets" / asset_v
    (adir / "img").mkdir(parents=True)
    for name, data in assets.items():
        (adir / name).write_bytes(data)
    for name, data in images.items():
        (adir / "img" / name).write_bytes(data)
    shutil.copyfile(ROOT / "demo" / "og.png", out / "og.png")

    ctx = {"site": site, "version": version, "asset_v": asset_v,
           "icons": (ROOT / "templates" / "partials" / "icons.html").read_text(encoding="utf-8")}
    pages = []
    for lang in LANGS:
        other = T[lang]["other_lang_code"]
        c = HOME[lang]
        write(out, f"{prefix(lang).lstrip('/')}/index.html".lstrip("/"),
              shell(lang, f"{prefix(lang)}/", f"{prefix(other)}/", c["title"], c["desc"],
                    home_body(lang, ctx), ctx, section="home"))
        pages.append(f"{prefix(lang)}/")
        write(out, f"{prefix(lang).lstrip('/')}/docs/index.html".lstrip("/"), docs_index(lang, ctx))
        pages.append(f"{prefix(lang)}/docs/")
        for slug, sources in DOCS:
            write(out, doc_url(lang, slug).lstrip("/") + "index.html", doc_page(lang, slug, sources, ctx))
            pages.append(doc_url(lang, slug))
    write(out, "404.html", not_found(ctx))

    write(out, "robots.txt", f"User-agent: *\nAllow: /\n\nSitemap: {site}/sitemap.xml\n")
    urls = "".join(f"  <url><loc>{site}{p}</loc></url>\n" for p in pages + ["/demo/"])
    write(out, "sitemap.xml", '<?xml version="1.0" encoding="UTF-8"?>\n'
          '<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">\n' + urls + "</urlset>\n")
    # Hébergeurs Apache : pages relues, assets en cache, page 404 du site
    write(out, ".htaccess", "Options -Indexes\nDirectoryIndex index.html\nErrorDocument 404 /404.html\n"
          "<IfModule mod_headers.c>\n  <FilesMatch \"\\.html$\">\n    Header set Cache-Control \"no-cache\"\n"
          "  </FilesMatch>\n</IfModule>\n")
    write(out, "assets/.htaccess", "<IfModule mod_headers.c>\n"
          "  Header set Cache-Control \"public, max-age=31536000, immutable\"\n</IfModule>\n")
    write(out, "version.json", json.dumps({"version": version, "assets": asset_v}) + "\n")
    n = sum(1 for f in out.rglob("*") if f.is_file())
    print(f"[site] v{version} -> {out} ({len(pages)} pages, {n} fichiers)")

    files = [(ROOT / "demo" / "serve.py", "app.py")] + [
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
        print(f"[site] application -> {bundle} (lancer : python3 app.py)")
    if args.zip:
        with zipfile.ZipFile(args.zip, "w", zipfile.ZIP_DEFLATED) as z:
            for src, rel in files:
                z.write(src, rel)
        print(f"[site] archive -> {args.zip} (lancer : python3 app.py)")


if __name__ == "__main__":
    main()
