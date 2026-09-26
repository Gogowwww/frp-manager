# 🌐 FRP Manager

> 🚀 Panel web auto-hébergé pour piloter [frp](https://github.com/fatedier/frp) (**frps** & **frpc**) sans ligne de commande : services, ports ouverts, configuration, journaux et mises à jour, dans une interface claire, en clair ou en sombre, sur ordinateur comme sur mobile.

![License](https://img.shields.io/github/license/Gogowwww/frp-manager)
![Version](https://img.shields.io/github/v/release/Gogowwww/frp-manager)
![Platform](https://img.shields.io/badge/platform-Linux-blue)
![Docker](https://img.shields.io/badge/Docker-ready-2496ED?logo=docker&logoColor=white)
[![Stars](https://img.shields.io/github/stars/Gogowwww/frp-manager?style=flat&color=yellow)](https://github.com/Gogowwww/frp-manager/stargazers)
[![Built with Claude](https://img.shields.io/badge/Vibecoded%20with-Claude-D97706?logo=anthropic&logoColor=white)](https://claude.ai)

🇬🇧 **[English version below](#-english)**

---

## 📑 Sommaire

- [🖥️ Aperçu](#️-aperçu)
- [📖 Présentation](#-présentation)
- [✨ Fonctionnalités](#-fonctionnalités)
- [📋 Prérequis](#-prérequis)
- [⚙️ Installation](#️-installation)
- [🏷️ Versions et pré-releases](#️-versions-et-pré-releases)
- [🔧 Configuration du panel](#-configuration-du-panel)
- [🗂️ Structure des fichiers](#️-structure-des-fichiers)
- [🌍 Traduire le panel](#-traduire-le-panel)
- [🔒 Sécurité](#-sécurité)
- [🗑️ Désinstallation](#️-désinstallation)
- [🤝 Contribuer](#-contribuer)
- [📄 Licence](#-licence)
- [🇬🇧 English](#-english)

---

## 🖥️ Aperçu

| Tableau de bord | Ports |
|:---:|:---:|
| ![Tableau de bord](panel/home.png) | ![Ports](panel/tunnels.png) |
| **Ouverture d'un port** | **Journaux en direct** |
| ![Ouverture d'un port](panel/tunnel-editor.png) | ![Journaux](panel/logs.png) |

<details>
<summary>⚙️ Réglages</summary>

![Réglages](panel/settings.png)

</details>

---

## 📖 Présentation

**FRP Manager** gère vos instances **frps** (le serveur, sur la machine publique) et **frpc** (le client, qui expose vos services locaux à travers lui) depuis un navigateur.

Il détecte tout seul vos services systemd et vos conteneurs Docker frp, démarre en **HTTPS** avec un certificat auto-signé généré au premier lancement, et fonctionne aussi bien avec une seule instance qu'avec plusieurs serveurs frp en parallèle.

> 💡 **Pourquoi FRP Manager ?**
> frp est puissant, mais sa gestion reste manuelle : fichiers TOML, redémarrages systemd, logs via SSH. FRP Manager réunit tout ça dans une interface pensée pour être comprise sans connaître frp par cœur, tout en montrant la clé TOML de chaque réglage pour les habitués.

---

## ✨ Fonctionnalités

### 📊 Tableau de bord
- 🔍 **Détection automatique** des services frps/frpc (units systemd, binaires, configs) et des conteneurs Docker frp
- 🪪 **Une carte par instance** : état en clair (*En marche*, *Arrêté*, *Introuvable*), version, fichier de config, service
- ▶️ **Démarrer / Arrêter / Redémarrer** en un clic, **démarrage automatique** en interrupteur, rechargement de la config
- 🛑 **Confirmation avant d'arrêter** un frpc : si vous accédez au panel à travers l'un de ses ports, vous êtes prévenu
- ✏️ **Surnoms** d'instances (*« Serveur maison »*, *« Accès bureau »*…)
- 🔔 Alertes utiles en haut de page : panel sans mot de passe, mise à jour disponible
- ⚡ **État en direct** : poussé par WebSocket dès qu'un service change (repli sur une vérification toutes les 12 s si le WebSocket ne passe pas)

### 🔌 Ports (frpc)
- 🗺️ **Liste lisible** : chaque port montre son chemin `vps.exemple.net:25565 → 127.0.0.1:25565`
- 📝 **Éditeur guidé** : `tcp`, `udp`, `http`, `https`, `stcp`, `xtcp`, avec une explication pour chaque type ; port public, domaines ou clé secrète selon le type ; options PROXY protocol v1/v2, chiffrement, compression
- 🔐 **Visiteurs** (`[[visitors]]`) : accès local à un port privé STCP/XTCP partagé par une autre machine
- 💾 **Brouillon puis enregistrement** : les modifications s'accumulent, une barre propose *Enregistrer* ou *Enregistrer et redémarrer frpc* ; avertissement si vous quittez sans enregistrer
- 🧷 **Rien n'est perdu** : les réglages que l'interface ne gère pas (`subdomain`, `plugin`, `[proxies.healthCheck]`…) sont conservés tels quels

### 🛡️ Pare-feu (frps)
- 🚦 **Qui peut se connecter** aux ports qu'ouvre frps : règles *Autoriser seulement* (liste blanche) ou *Bloquer* (liste noire), sur des ports choisis ou sur **tous les ports frp**
- 🏢 **Par adresse, réseau ou opérateur entier** : `203.0.113.4`, `198.51.100.0/24` ou un numéro d'AS (`AS16276`), dont les préfixes sont récupérés auprès de RIPEstat et rafraîchis chaque jour
- 🔎 **Ports détectés tout seuls** : `bindPort`, ports vhost, KCP/QUIC, `allowPorts`, et les ports ouverts par les clients (via le tableau de bord de frps, s'il est activé) ; la liste est suivie automatiquement
- 🧱 **Filtrage par le noyau** (nftables, table dédiée `inet frp_manager`), avant le NAT de Docker : un frps en conteneur est filtré aussi, et **le filtrage continue si le panel s'arrête**
- 🙅 Le pare-feu **ne fait que bloquer** : il n'ouvre jamais un port et ne touche ni à ufw ni aux règles de Docker ; le port du panel et la boucle locale ne sont jamais filtrés
- 🧪 **Tester une adresse** avant d'appliquer, **alerte** si vos règles bloqueraient votre propre IP
- 📡 **Connexions bloquées en temps réel** (WebSocket, repli automatique) : adresse, opérateur (AS), port, règle, compteur par règle ; un clic bloque l'adresse ou tout son AS
- 🌍 **Listes communautaires** (à bloquer ou à autoriser seulement) : abonnez-vous en un clic à une liste du catalogue (règle tenue à jour toute seule), ou **publiez** la vôtre : le panel prépare une issue GitHub, vous l'envoyez, elle est vérifiée et ajoutée automatiquement en quelques minutes ; le catalogue vit sur la branche [`blocklists`](https://github.com/Gogowwww/frp-manager/tree/blocklists)

> ℹ️ Nécessite `nftables` sur la machine (`apt install nftables`). La page n'apparaît que si un frps tourne sur la machine.

### ⚙️ Configuration (frps / frpc)
- 🧩 **Formulaire par sections** : l'essentiel visible (connexion, authentification, tableau de bord frp), le reste replié dans *Réglages avancés* (TLS, ports KCP/QUIC/vhost, limites, journalisation)
- 🏷️ Chaque champ affiche sa **clé TOML** et une aide courte
- 🛡️ Les ports d'un frpc sont **préservés** quand vous enregistrez sa configuration

### 📜 Journaux
- 📂 Source **journal systemd**, **fichier de log** ou **conteneur Docker** (avec ou sans `tty`)
- 📡 **Flux en direct par WebSocket** (`wss://` sur le panel en HTTPS) sur la source choisie (journal systemd ou fichier), avec repli automatique sur SSE si le WebSocket ne passe pas ; coloration des erreurs et avertissements, **filtre** de lignes

> 🔀 Derrière un reverse proxy, autorisez la mise à niveau WebSocket sur `/ws/` (nginx : `proxy_http_version 1.1;` + en-têtes `Upgrade` et `Connection`). Sans ça, le panel retombe simplement sur SSE.

### ⬆️ Mises à jour
- **frp** : vérification de la dernière version, installation en un clic avec **miroirs de secours** (ghproxy, ghfast, gh-proxy), **upload manuel** d'une archive si GitHub est inaccessible, test d'accès aux sources
- **Panel** : mise à jour en un clic (installation classique) ou commande `docker pull` à copier (Docker) ; une **pré-release ne propose jamais de mise à jour** (voir [Versions et pré-releases](#️-versions-et-pré-releases))

### 🎛️ Réglages
- 🔐 Identifiant et mot de passe du panel
- 🌐 Adresse et port d'écoute, durée de session
- 🎨 **Thème** système / clair / sombre, **langue** : français, anglais en aperçu (sortie officielle prévue en v0.0.50)

### 🐳 Docker
- Le panel peut tourner **dans un conteneur** et piloter frp **sur l'hôte** (via `nsenter`, sans systemd dans l'image)
- Il détecte et contrôle aussi les **conteneurs frpc/frps** d'autres stacks (démarrer, arrêter, redémarrer, journaux, ports)

---

## 📋 Prérequis

- 🐧 Linux avec **systemd**
- 🐍 **Python 3.8+** (installation classique) ou **Docker**
- 🏗️ Architectures : `amd64`, `arm64`, `arm`

> ℹ️ **Pas besoin d'installer frp avant** : `install.sh` télécharge `frps`/`frpc` et crée leurs services systemd. frp se met ensuite à jour depuis la page **Mises à jour**.

---

## ⚙️ Installation

### 🥇 Méthode 1 — Script d'installation (recommandée)

```bash
# 1. Télécharger la dernière release
curl -LO https://github.com/Gogowwww/frp-manager/releases/latest/download/frp-manager.zip
unzip frp-manager.zip && cd frp-manager

# 2. Installer (root requis)
sudo bash install.sh
```

Le script installe le panel dans un environnement Python isolé, crée le service systemd `frp-manager` et le démarre. Il **prépare aussi frp** :

- ⬇️ binaires `frps` / `frpc` dans `/usr/local/bin` (dernière version, avec miroirs de secours) ;
- 📄 configs par défaut `/etc/frp/frps.toml` et `/etc/frp/frpc.toml`, **uniquement si elles n'existent pas** ;
- 🧱 services `frps.service` et `frpc.service`, créés mais **ni activés ni démarrés** : vous les configurez puis les lancez depuis le panel.

### 🐳 Méthode 2 — Docker / Portainer

**Docker Compose :**
```bash
git clone https://github.com/Gogowwww/frp-manager.git && cd frp-manager
docker compose up -d
```

**Portainer :** *Stacks → Add stack → Repository*, URL `https://github.com/Gogowwww/frp-manager`, compose path `docker-compose.yml`, puis *Deploy the stack*.

| Image | Contenu |
|---|---|
| `ghcr.io/gogowwww/frp-manager:latest` | dernière release stable (**recommandé**) |
| `ghcr.io/gogowwww/frp-manager:X.Y.Z` | une version précise, pour la figer ou revenir en arrière |
| `ghcr.io/gogowwww/frp-manager:dev` | dernière pré-release, pour tester avant tout le monde |

> 🔧 Le conteneur utilise `pid: host` et `nsenter` pour atteindre le systemd de l'hôte, et le socket Docker pour les conteneurs frp. Les binaires frp sont lus et écrits dans `/usr/local/bin` de l'hôte via le montage `/host/usr/local/bin`.

L'interface est ensuite accessible sur :
```
https://VOTRE_IP:8765
```

> ⚠️ Le certificat est auto-signé : le navigateur affiche un avertissement au premier accès, c'est normal. Pour un certificat valide, placez le panel derrière un reverse proxy (nginx, Caddy).

---

## 🏷️ Versions et pré-releases

Chaque nouvelle version sort d'abord en **pré-release**, pour être testée avant d'être proposée à tout le monde. Les numéros se suivent (`0.0.25`, `0.0.26`…) et une pré-release validée devient la release définitive **avec le même numéro**, sans être reconstruite.

| | Pré-release | Release |
|---|---|---|
| Page GitHub | marquée *Pre-release* | *Latest* |
| Image Docker | `:X.Y.Z` + `:dev` | `:X.Y.Z` + `:latest` |
| Proposée aux panels stables | ❌ | ✅ |
| Proposée aux panels en pré-release (installation classique) | ✅ | ✅ |

- **Panel stable** : il ne voit que les releases, jamais les pré-releases.
- **Panel en pré-release, installation classique** : il suit le canal des pré-releases et le bouton « Mettre à jour » propose la version publiée la plus récente, pré-release ou release.
- **Panel en pré-release sous Docker** : aucune mise à jour proposée dans l'interface ; changez le tag de l'image (`:dev` pour suivre les pré-releases, `:latest` pour revenir aux releases).

Quand la pré-release installée devient une release définitive, le panel repasse de lui-même sur le canal stable.

> ℹ️ **0.0.26 : l'option « IP réelle » (go-mmproxy) est retirée.** Si vous l'utilisiez, le panel remet automatiquement les tunnels concernés sur leur vrai service au premier démarrage, redémarre frpc, puis supprime les relais, les règles de routage et le binaire `go-mmproxy`. Pour transmettre l'IP des visiteurs, utilisez l'option **PROXY protocol** avec un service qui la prend en charge (nginx, HAProxy…).

---

## 🔧 Configuration du panel

Tout se règle depuis la page **Réglages**. Le fichier sous-jacent est `/etc/frp-manager/frp-manager.json` :

```json
{
  "bind_host": "0.0.0.0",
  "bind_port": 8765,
  "username": "admin",
  "password_hash": "",
  "session_timeout": 3600,
  "ssl_enabled": true,
  "nicknames": {}
}
```

| 🔑 Clé | 📝 Description | 🎯 Défaut |
|---|---|---|
| `bind_host` | Adresse d'écoute (`127.0.0.1` derrière un reverse proxy) | `0.0.0.0` |
| `bind_port` | Port du panel | `8765` |
| `username` | Identifiant de connexion | `admin` |
| `password_hash` | Mot de passe haché (géré par l'interface) | `""` *(aucun)* |
| `session_timeout` | Durée de session en secondes | `3600` |
| `ssl_enabled` | HTTPS avec certificat auto-signé | `true` |
| `nicknames` | Surnoms des instances (gérés par l'interface) | `{}` |

> 🔁 `bind_host`, `bind_port` et `ssl_enabled` s'appliquent au prochain redémarrage de `frp-manager`.

---

## 🗂️ Structure des fichiers

```
📦 Dépôt
  app.py                   # Serveur Flask + API
  frp-autoupdate.py        # Mise à jour automatique de frp (cron)
  install.sh               # Installation classique
  Dockerfile, docker-compose.yml
  templates/
    index.html, login.html # Pages (coquille)
    partials/icons.html    # Icônes SVG
    assets/
      css/app.css          # Design system (thèmes clair/sombre)
      js/                  # Application (modules ES, sans étape de build)
        pages/             # Tableau de bord, Ports, Configuration, Journaux…
      locales/fr.js, en.js # Textes de l'interface (français, anglais)

📁 /opt/frp-manager/        # Panel installé (méthode script)
📁 /etc/frp-manager/        # Configuration du panel + certificats SSL
📁 /etc/frp/                # Configurations frps/frpc (TOML)
📁 /var/log/frp/            # Journaux frp
📁 /var/lib/frp-manager/    # État (versions installées)
📁 /etc/systemd/system/     # frp-manager.service, frps.service, frpc.service
📁 /etc/cron.d/             # frp-autoupdate (vérification quotidienne, 03h00)
```

---

## 🌍 Traduire le panel

Tous les textes de l'interface sont dans `templates/assets/locales/` : `fr.js` (langue de référence) et `en.js` (anglais). Pour ajouter une langue :

1. Copier `fr.js` en `<code>.js` (`de.js`, `es.js`…) et traduire les valeurs, sans toucher aux clés ni aux `{paramètres}` ;
2. Déclarer la langue dans `LOCALES` de `templates/assets/js/i18n.js` ;
3. Elle apparaît dans **Réglages → Préférences → Langue**, et le navigateur la choisit automatiquement si elle correspond à sa langue.

Une langue encore en préparation va aussi dans `PREVIEW_LOCALES` : elle est proposée dans les Réglages avec la mention *aperçu*, mais jamais choisie d'après le navigateur. C'est le cas de l'anglais jusqu'à sa sortie officielle en **v0.0.50**.

> Les messages renvoyés par le serveur restent pour l'instant en français.

---

## 🔒 Sécurité

- 🔑 **Définissez un mot de passe** dès l'installation (**Réglages → Sécurité**) : le tableau de bord vous le rappelle tant qu'il n'y en a pas
- 🧱 **Limitez l'accès par IP** (pare-feu) ou écoutez sur `127.0.0.1` derrière un reverse proxy
- 📜 Pour un **certificat valide**, utilisez un reverse proxy (nginx, Caddy) avec Let's Encrypt
- 🎲 Utilisez un **jeton frp long et unique** par serveur

---

## 🗑️ Désinstallation

```bash
sudo systemctl disable --now frp-manager
sudo rm /etc/systemd/system/frp-manager.service
sudo rm -rf /opt/frp-manager /etc/frp-manager /etc/cron.d/frp-autoupdate
sudo systemctl daemon-reload
```

> ⚠️ Les configurations frp (`/etc/frp/`) et les binaires (`/usr/local/bin/`) sont conservés.

---

## 🤝 Contribuer

Les contributions sont les bienvenues :

1. 🍴 Forkez le dépôt
2. 🌿 Créez une branche : `git checkout -b feature/ma-feature`
3. 💾 Commitez vos changements
4. 📬 Ouvrez une Pull Request

> 🐛 Bug ou idée ? Ouvrez une [issue](https://github.com/Gogowwww/frp-manager/issues).

> 🌍 Une liste d'adresses à bloquer à partager ? Bouton **Publier** sur une règle *Bloquer* du pare-feu, ou directement une Pull Request sur la branche [`blocklists`](https://github.com/Gogowwww/frp-manager/tree/blocklists).

---

## 📄 Licence

**Apache 2.0** — voir [LICENSE](LICENSE)

---

## 🙏 Remerciements

- [fatedier/frp](https://github.com/fatedier/frp) — le projet frp sans lequel rien de tout ça n'aurait de sens
- 💛 La communauté open source pour les retours et contributions

---

## ✨ Vibecoding

<div align="center">

*Ce projet a été entièrement conçu, développé et itéré en **vibecoding** avec l'IA.*

[![Built with Claude](https://img.shields.io/badge/Vibecoded%20with-Claude-D97706?style=for-the-badge&logo=anthropic&logoColor=white)](https://claude.ai)

> *« Vibe, iterate, ship. »*

</div>

---

## ⭐ Star History

[![Star History Chart](https://api.star-history.com/chart?repos=Gogowwww/frp-manager&type=date&legend=bottom-right)](https://www.star-history.com/?repos=Gogowwww%2Ffrp-manager&type=date&legend=bottom-right)

---

## 🇬🇧 English

> 🚀 A self-hosted web panel to run [frp](https://github.com/fatedier/frp) (**frps** & **frpc**) without the command line: services, open ports, configuration, logs and updates, in a clean interface, light or dark, on desktop and mobile.

> 🌍 **The panel's interface in English** is available as a preview in **Settings → Preferences → Language** and will be officially released in **v0.0.50**. Until then, some messages sent by the server stay in French.

### 📖 Overview

**FRP Manager** manages your **frps** instances (the server, on the public machine) and **frpc** instances (the client, which exposes your local services through it) from a browser.

It detects your frp systemd services and Docker containers on its own, starts over **HTTPS** with a self-signed certificate generated on first launch, and works just as well with a single instance as with several frp servers side by side.

> 💡 **Why FRP Manager?**
> frp is powerful, but managing it stays manual: TOML files, systemd restarts, logs over SSH. FRP Manager brings all of that together in an interface designed to be understood without knowing frp by heart, while still showing the TOML key of every setting for power users.

### ✨ Features

#### 📊 Dashboard
- 🔍 **Automatic detection** of frps/frpc services (systemd units, binaries, configs) and frp Docker containers
- 🪪 **One card per instance**: plain status (*Running*, *Stopped*, *Not found*), version, config file, service
- ▶️ **Start / Stop / Restart** in one click, **start on boot** as a toggle, config reload
- 🛑 **Confirmation before stopping** an frpc: if you reach the panel through one of its ports, you are warned
- ✏️ **Nicknames** for instances (*"Home server"*, *"Office access"*…)
- 🔔 Useful alerts at the top of the page: panel without a password, update available
- ⚡ **Live status**: pushed over WebSocket as soon as a service changes (falls back to a check every 12 s if WebSocket doesn't get through)

#### 🔌 Ports (frpc)
- 🗺️ **Readable list**: each port shows its path `vps.example.net:25565 → 127.0.0.1:25565`
- 📝 **Guided editor**: `tcp`, `udp`, `http`, `https`, `stcp`, `xtcp`, with an explanation for each type; public port, domains or secret key depending on the type; PROXY protocol v1/v2, encryption and compression options
- 🔐 **Visitors** (`[[visitors]]`): local access to a private STCP/XTCP port shared by another machine
- 💾 **Draft, then save**: changes pile up and a bar offers *Save* or *Save and restart frpc*; you are warned if you leave without saving
- 🧷 **Nothing is lost**: settings the interface doesn't handle (`subdomain`, `plugin`, `[proxies.healthCheck]`…) are kept as they are

#### 🛡️ Firewall (frps)
- 🚦 **Who can connect** to the ports opened by frps: *Allow only* (allowlist) or *Block* (blocklist) rules, on selected ports or on **all frp ports**
- 🏢 **By address, network or whole provider**: `203.0.113.4`, `198.51.100.0/24` or an AS number (`AS16276`), whose prefixes are fetched from RIPEstat and refreshed daily
- 🔎 **Ports detected automatically**: `bindPort`, vhost ports, KCP/QUIC, `allowPorts`, and the ports opened by clients (through the frps dashboard, if enabled); the list is followed automatically
- 🧱 **Kernel-level filtering** (nftables, dedicated `inet frp_manager` table), before Docker's NAT: an frps in a container is filtered too, and **filtering keeps working if the panel stops**
- 🙅 The firewall **only blocks**: it never opens a port and touches neither ufw nor Docker's rules; the panel port and loopback are never filtered
- 🧪 **Test an address** before applying, with an **alert** if your rules would block your own IP
- 📡 **Blocked connections in real time** (WebSocket, automatic fallback): address, provider (AS), port, rule, per-rule counter; one click blocks the address or its whole AS
- 🌍 **Community lists** (to block or to allow only): subscribe in one click to a list from the catalog (the rule keeps itself up to date), or **publish** your own: the panel prepares a GitHub issue, you submit it, it is checked and added automatically within minutes; the catalog lives on the [`blocklists`](https://github.com/Gogowwww/frp-manager/tree/blocklists) branch

> ℹ️ Requires `nftables` on the machine (`apt install nftables`). The page only appears if an frps runs on the machine.

#### ⚙️ Configuration (frps / frpc)
- 🧩 **Form in sections**: the essentials in view (connection, authentication, frp dashboard), the rest folded into *Advanced settings* (TLS, KCP/QUIC/vhost ports, limits, logging)
- 🏷️ Each field shows its **TOML key** and a short hint
- 🛡️ An frpc's ports are **preserved** when you save its configuration

#### 📜 Logs
- 📂 Source: **systemd journal**, **log file** or **Docker container** (with or without `tty`)
- 📡 **Live stream over WebSocket** (`wss://` when the panel uses HTTPS) on the chosen source, with automatic fallback to SSE if WebSocket doesn't get through; errors and warnings highlighted, line **filter**

> 🔀 Behind a reverse proxy, allow the WebSocket upgrade on `/ws/` (nginx: `proxy_http_version 1.1;` + `Upgrade` and `Connection` headers). Without it, the panel simply falls back to SSE.

#### ⬆️ Updates
- **frp**: checks for the latest version, one-click install with **fallback mirrors** (ghproxy, ghfast, gh-proxy), **manual upload** of an archive if GitHub can't be reached, source access test
- **Panel**: one-click update (standard install) or a `docker pull` command to copy (Docker); a **pre-release never offers an update** (see [Versions and pre-releases](#️-versions-and-pre-releases))

#### 🎛️ Settings
- 🔐 Panel username and password
- 🌐 Listening address and port, session length
- 🎨 **Theme** system / light / dark, **language**: French, English as a preview (official release planned for v0.0.50)

#### 🐳 Docker
- The panel can run **in a container** and drive frp **on the host** (through `nsenter`, no systemd in the image)
- It also detects and controls **frpc/frps containers** from other stacks (start, stop, restart, logs, ports)

### 📋 Requirements

- 🐧 Linux with **systemd**
- 🐍 **Python 3.8+** (standard install) or **Docker**
- 🏗️ Architectures: `amd64`, `arm64`, `arm`

> ℹ️ **No need to install frp first**: `install.sh` downloads `frps`/`frpc` and creates their systemd services. frp is then updated from the **Updates** page.

### ⚙️ Installation

#### 🥇 Method 1 — Install script (recommended)

```bash
# 1. Download the latest release
curl -LO https://github.com/Gogowwww/frp-manager/releases/latest/download/frp-manager.zip
unzip frp-manager.zip && cd frp-manager

# 2. Install (root required)
sudo bash install.sh
```

The script installs the panel in an isolated Python environment, creates the `frp-manager` systemd service and starts it. It **also prepares frp**:

- ⬇️ `frps` / `frpc` binaries in `/usr/local/bin` (latest version, with fallback mirrors);
- 📄 default configs `/etc/frp/frps.toml` and `/etc/frp/frpc.toml`, **only if they don't exist yet**;
- 🧱 `frps.service` and `frpc.service` services, created but **neither enabled nor started**: you configure them, then start them from the panel.

#### 🐳 Method 2 — Docker / Portainer

**Docker Compose:**
```bash
git clone https://github.com/Gogowwww/frp-manager.git && cd frp-manager
docker compose up -d
```

**Portainer:** *Stacks → Add stack → Repository*, URL `https://github.com/Gogowwww/frp-manager`, compose path `docker-compose.yml`, then *Deploy the stack*.

| Image | Contents |
|---|---|
| `ghcr.io/gogowwww/frp-manager:latest` | latest stable release (**recommended**) |
| `ghcr.io/gogowwww/frp-manager:X.Y.Z` | a specific version, to pin it or roll back |
| `ghcr.io/gogowwww/frp-manager:dev` | latest pre-release, to test before everyone else |

> 🔧 The container uses `pid: host` and `nsenter` to reach the host's systemd, and the Docker socket for frp containers. frp binaries are read and written in the host's `/usr/local/bin` through the `/host/usr/local/bin` mount.

The interface is then available at:
```
https://YOUR_IP:8765
```

> ⚠️ The certificate is self-signed: your browser shows a warning on first access, that's expected. For a valid certificate, put the panel behind a reverse proxy (nginx, Caddy).

### 🏷️ Versions and pre-releases

Every new version first ships as a **pre-release**, to be tested before it is offered to everyone. Numbers follow each other (`0.0.25`, `0.0.26`…) and a validated pre-release becomes the final release **with the same number**, without being rebuilt.

| | Pre-release | Release |
|---|---|---|
| GitHub page | marked *Pre-release* | *Latest* |
| Docker image | `:X.Y.Z` + `:dev` | `:X.Y.Z` + `:latest` |
| Offered to stable panels | ❌ | ✅ |
| Offered to pre-release panels (standard install) | ✅ | ✅ |

- **Stable panel**: it only sees releases, never pre-releases.
- **Pre-release panel, standard install**: it follows the pre-release channel and the "Update" button offers the most recently published version, pre-release or release.
- **Pre-release panel under Docker**: no update is offered in the interface; change the image tag (`:dev` to follow pre-releases, `:latest` to go back to releases).

When the installed pre-release becomes a final release, the panel switches back to the stable channel on its own.

### 🔧 Panel configuration

Everything is set from the **Settings** page. The underlying file is `/etc/frp-manager/frp-manager.json`:

```json
{
  "bind_host": "0.0.0.0",
  "bind_port": 8765,
  "username": "admin",
  "password_hash": "",
  "session_timeout": 3600,
  "ssl_enabled": true,
  "nicknames": {}
}
```

| 🔑 Key | 📝 Description | 🎯 Default |
|---|---|---|
| `bind_host` | Listening address (`127.0.0.1` behind a reverse proxy) | `0.0.0.0` |
| `bind_port` | Panel port | `8765` |
| `username` | Login username | `admin` |
| `password_hash` | Hashed password (managed by the interface) | `""` *(none)* |
| `session_timeout` | Session length in seconds | `3600` |
| `ssl_enabled` | HTTPS with a self-signed certificate | `true` |
| `nicknames` | Instance nicknames (managed by the interface) | `{}` |

> 🔁 `bind_host`, `bind_port` and `ssl_enabled` apply the next time `frp-manager` restarts.

### 🌍 Translating the panel

All interface texts live in `templates/assets/locales/`: `fr.js` (reference language) and `en.js` (English). To add a language:

1. Copy `fr.js` to `<code>.js` (`de.js`, `es.js`…) and translate the values, without touching the keys or the `{parameters}`;
2. Declare the language in `LOCALES` in `templates/assets/js/i18n.js`;
3. It shows up in **Settings → Preferences → Language**, and the browser picks it automatically when it matches its language.

A language still in progress also goes into `PREVIEW_LOCALES`: it is offered in Settings marked *preview*, but never picked from the browser language. That's the case for English until its official release in **v0.0.50**.

> Messages sent by the server are still in French for now.

### 🔒 Security

- 🔑 **Set a password** right after installing (**Settings → Security**): the dashboard reminds you as long as there is none
- 🧱 **Restrict access by IP** (firewall) or listen on `127.0.0.1` behind a reverse proxy
- 📜 For a **valid certificate**, use a reverse proxy (nginx, Caddy) with Let's Encrypt
- 🎲 Use a **long, unique frp token** per server

### 🗑️ Uninstall

```bash
sudo systemctl disable --now frp-manager
sudo rm /etc/systemd/system/frp-manager.service
sudo rm -rf /opt/frp-manager /etc/frp-manager /etc/cron.d/frp-autoupdate
sudo systemctl daemon-reload
```

> ⚠️ frp configurations (`/etc/frp/`) and binaries (`/usr/local/bin/`) are kept.

### 🤝 Contributing

Contributions are welcome:

1. 🍴 Fork the repository
2. 🌿 Create a branch: `git checkout -b feature/my-feature`
3. 💾 Commit your changes
4. 📬 Open a Pull Request

> 🐛 Bug or idea? Open an [issue](https://github.com/Gogowwww/frp-manager/issues).

> 🌍 A list of addresses to block that you'd like to share? Use the **Publish** button on a *Block* firewall rule, or open a Pull Request on the [`blocklists`](https://github.com/Gogowwww/frp-manager/tree/blocklists) branch.

### 📄 License

**Apache 2.0** — see [LICENSE](LICENSE)

*This project was entirely designed, built and iterated through **vibecoding** with AI — [fatedier/frp](https://github.com/fatedier/frp) makes it all possible.*
