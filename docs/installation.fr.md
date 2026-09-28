# Installation, configuration et désinstallation

[English](installation.md) · **Français**

- [Prérequis](#prérequis)
- [Installation par script](#installation-par-script)
- [Docker et Portainer](#docker-et-portainer)
- [Première ouverture](#première-ouverture)
- [Accéder au panel depuis un autre poste](#accéder-au-panel-depuis-un-autre-poste)
- [Versions et pré-releases](#versions-et-pré-releases)
- [Configuration du panel](#configuration-du-panel)
- [Mot de passe perdu](#mot-de-passe-perdu)
- [Structure des fichiers](#structure-des-fichiers)
- [Désinstallation](#désinstallation)
- [Historique : retrait de go-mmproxy](#historique--retrait-de-go-mmproxy)

## Prérequis

- Linux avec **systemd**
- **Python 3.8+** (installation par script) ou **Docker**
- Architectures : `amd64`, `arm64`, `arm`

Inutile d'installer frp avant : le script télécharge `frps`/`frpc` et crée
leurs services systemd. frp se met ensuite à jour depuis la page
**Mises à jour**, quand vous le décidez : rien n'est mis à jour automatiquement.

## Installation par script

```bash
curl -LO https://github.com/Gogowwww/frp-manager/releases/latest/download/frp-manager.zip
unzip frp-manager.zip && cd frp-manager
sudo bash install.sh
```

Le script installe le panel dans un environnement Python isolé
(`/opt/frp-manager/venv`), crée le service systemd `frp-manager` et le
démarre. Il prépare aussi frp :

- binaires `frps` / `frpc` dans `/usr/local/bin` (dernière version, somme
  SHA-256 vérifiée) ;
- configurations par défaut `/etc/frp/frps.toml` et `/etc/frp/frpc.toml`,
  seulement si elles n'existent pas ;
- services `frps.service` et `frpc.service`, créés mais ni activés ni
  démarrés : vous les configurez puis les lancez depuis le panel.

Relancer `install.sh` depuis une archive plus récente met le panel à jour en
conservant sa configuration. Le bouton **Mettre à jour** du panel fait la même
chose sans ligne de commande.

## Docker et Portainer

```bash
curl -O https://raw.githubusercontent.com/Gogowwww/frp-manager/main/docker-compose.yml
docker compose up -d
```

**Portainer** : *Stacks → Add stack → Repository*, URL
`https://github.com/Gogowwww/frp-manager`, compose path `docker-compose.yml`,
puis *Deploy the stack*.

| Image | Contenu |
|---|---|
| `ghcr.io/gogowwww/frp-manager:latest` | dernière release stable (recommandé) |
| `ghcr.io/gogowwww/frp-manager:X.Y.Z` | une version précise, pour la figer ou revenir en arrière |
| `ghcr.io/gogowwww/frp-manager:dev` | dernière pré-release, pour tester avant tout le monde |

Le conteneur pilote frp sur l'hôte avec `pid: host` et `nsenter`, ce qui lui
donne un accès équivalent à root sur l'hôte : lisez
[docker.fr.md](docker.fr.md) avant de le déployer.

## Première ouverture

Tant qu'aucun mot de passe n'existe, le panel n'affiche qu'une page de
**configuration initiale** : choisissez l'identifiant administrateur et un mot
de passe d'au moins 12 caractères. Aucune autre page ni aucune route de l'API
ne répond avant.

Le certificat HTTPS est auto-signé : le navigateur affiche un avertissement au
premier accès. Pour un certificat valide, placez le panel derrière un reverse
proxy (nginx, Caddy) avec Let's Encrypt.

## Accéder au panel depuis un autre poste

Une nouvelle installation n'écoute que sur **`127.0.0.1`** : le panel n'est
pas joignable depuis le réseau tant que vous ne l'avez pas décidé.

| Méthode | Comment |
|---|---|
| Tunnel SSH (recommandé) | `ssh -L 8765:127.0.0.1:8765 utilisateur@serveur`, puis `https://127.0.0.1:8765` |
| Reverse proxy local | nginx ou Caddy écoute sur le réseau et relaie vers `127.0.0.1:8765` (autorisez l'upgrade WebSocket sur `/ws/`) |
| Écoute sur le réseau | `bind_host` à `0.0.0.0` dans **Réglages → Accès réseau** (ou dans le fichier de config), ou `FRP_MANAGER_HOST=0.0.0.0` en Docker, puis redémarrage du panel |

Si vous ouvrez le panel au réseau, filtrez l'accès par IP (pare-feu, VPN).
Une installation antérieure à 0.0.51 garde son adresse d'écoute actuelle.

Exemple nginx (extrait) :

```nginx
location / {
    proxy_pass https://127.0.0.1:8765;
    proxy_set_header Host $host;
    proxy_http_version 1.1;
    proxy_set_header Upgrade $http_upgrade;
    proxy_set_header Connection "upgrade";
}
```

Derrière un reverse proxy TLS avec `ssl_enabled: false`, ajoutez
`"cookie_secure": true` au fichier de config pour que le cookie de session
reste marqué *Secure*.

## Versions et pré-releases

Chaque nouvelle version sort d'abord en **pré-release**, pour être testée
avant d'être proposée à tout le monde. Les numéros se suivent et une
pré-release validée devient la release définitive **avec le même numéro**,
sans être reconstruite.

| | Pré-release | Release |
|---|---|---|
| Page GitHub | marquée *Pre-release* | *Latest* |
| Image Docker | `:X.Y.Z` + `:dev` | `:X.Y.Z` + `:latest` |
| Proposée aux panels stables | non | oui |
| Proposée aux panels en pré-release (installation par script) | oui | oui |

- **Panel stable** : il ne voit que les releases.
- **Panel en pré-release, installation par script** : le bouton « Mettre à
  jour » propose la version publiée la plus récente, pré-release ou release.
- **Panel en pré-release sous Docker** : changez le tag de l'image (`:dev` ou
  `:latest`).

Quand la pré-release installée devient une release définitive, le panel
repasse de lui-même sur le canal stable.

## Configuration du panel

Tout se règle depuis la page **Réglages**. Le fichier sous-jacent est
`/etc/frp-manager/frp-manager.json`, lisible par root seulement :

| Clé | Description | Défaut |
|---|---|---|
| `bind_host` | Adresse d'écoute | `127.0.0.1` (nouvelle installation) |
| `bind_port` | Port du panel | `8765` |
| `username` | Identifiant de connexion | `admin` |
| `password_hash` | Mot de passe haché (argon2id ou scrypt, géré par l'interface) | créé à la première ouverture |
| `secret_key` | Clé de signature des sessions, tirée au hasard au premier démarrage | générée |
| `session_timeout` | Durée de session en secondes | `3600` |
| `ssl_enabled` | HTTPS avec certificat auto-signé | `true` |
| `cookie_secure` | Cookie *Secure* même sans HTTPS côté panel (reverse proxy TLS) | `false` |
| `download_mirrors` | Miroirs tiers en repli de github.com pour télécharger frp | `true` |
| `nicknames` | Surnoms des instances (gérés par l'interface) | `{}` |

`bind_host`, `bind_port` et `ssl_enabled` s'appliquent au prochain redémarrage
de `frp-manager`.

Variables d'environnement :

| Variable | Effet |
|---|---|
| `FRP_MANAGER_HOST` | Adresse d'écoute, prioritaire sur `bind_host` |
| `FRP_MANAGER_PORT` | Port d'une nouvelle installation (écrit dans `bind_port` au premier démarrage) |
| `FRP_MANAGER_NO_MIRRORS=1` | Interdit les miroirs de téléchargement tiers |
| `FRP_MANAGER_CONFIG` | Autre emplacement du fichier de configuration |

### Miroirs de téléchargement

Si github.com ne répond pas, frp peut être téléchargé via des miroirs tiers
(`mirror.ghproxy.com`, `ghfast.top`, `gh-proxy.com`). Ces services voient
passer le fichier et pourraient le modifier : c'est un risque pour la chaîne
d'approvisionnement. Le panel n'accepte donc une archive venue d'un miroir que
si sa somme SHA-256 correspond à celle publiée avec la release de frp, et le
signale dans le journal d'installation. Si github.com vous est accessible,
désactivez les miroirs (**Réglages → Accès réseau**).

## Mot de passe perdu

```bash
sudo /opt/frp-manager/venv/bin/python3 /opt/frp-manager/app.py --reset-password
sudo systemctl restart frp-manager
```

En Docker : `docker exec -it frp-manager python3 app.py --reset-password`, puis
`docker restart frp-manager`.

## Structure des fichiers

```
Dépôt
  app.py                   Serveur Flask + API
  install.sh               Installation par script
  Dockerfile, docker-compose.yml
  templates/
    index.html, login.html, setup.html
    partials/icons.html    Icônes SVG (embarquées, sans CDN)
    assets/
      css/app.css          Thèmes clair et sombre
      js/                  Application (modules ES, sans étape de build)
      locales/fr.js, en.js Textes de l'interface
  tests/                   Tests pytest

/opt/frp-manager/          Panel installé (script)
/etc/frp-manager/          Configuration du panel + certificats SSL
/etc/frp/                  Configurations frps/frpc (TOML)
/var/log/frp/              Journaux frp
/var/lib/frp-manager/      État (versions installées)
/etc/systemd/system/       frp-manager.service, frps.service, frpc.service
```

Le panel n'écrit une configuration frp que dans les dossiers où il en cherche :
`/etc/frp`, `/usr/local/etc/frp`, `/opt/frp`, `/root/frp`.

## Désinstallation

```bash
sudo systemctl disable --now frp-manager
sudo rm /etc/systemd/system/frp-manager.service
sudo rm -rf /opt/frp-manager /etc/frp-manager
sudo systemctl daemon-reload
```

Les configurations frp (`/etc/frp/`) et les binaires (`/usr/local/bin/`) sont
conservés.

## Historique : retrait de go-mmproxy

La version 0.0.26 a retiré l'option « IP réelle » (go-mmproxy). Si vous
l'utilisiez, le panel remet automatiquement les tunnels concernés sur leur
vrai service au premier démarrage, redémarre frpc, puis supprime les relais,
les règles de routage et le binaire `go-mmproxy`. Pour transmettre l'IP des
visiteurs, utilisez l'option **PROXY protocol** avec un service qui la prend en
charge (nginx, HAProxy…).
