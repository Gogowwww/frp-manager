# FRP Manager

> Interface web de gestion pour [frp](https://github.com/fatedier/frp) — multi-instances, configuration interactive, logs en direct, auto-update.

![License](https://img.shields.io/github/license/Gogowwww/frp-manager)
![Version](https://img.shields.io/github/v/release/Gogowwww/frp-manager)
![Platform](https://img.shields.io/badge/platform-Linux-blue)

---

## Présentation

FRP Manager est un panel web auto-hébergé pour gérer vos instances **frps** (serveur) et **frpc** (client) sans toucher à la ligne de commande. Il détecte automatiquement vos services systemd existants et s'adapte à votre configuration, qu'il s'agisse d'une instance unique ou de plusieurs serveurs frp tournant en parallèle.

**Pourquoi FRP Manager ?**

frp est un outil puissant mais sa gestion reste entièrement manuelle : édition de fichiers TOML, redémarrages systemd, surveillance des logs via SSH. FRP Manager centralise tout ça dans une interface claire, accessible depuis n'importe quel navigateur.

---

## Fonctionnalités

### Vue d'ensemble
- **Détection automatique** de tous les services frps/frpc existants sur la machine (scan des units systemd, binaires, configs)
- **Cartes d'état** par instance : statut actif/inactif, version, chemin du binaire et de la config, unit systemd
- **Contrôle des services** : Start, Stop, Restart, Reload, Enable, Disable — directement depuis l'interface
- **Raccourcis** vers la config et les logs de chaque instance

### Configuration
- **Formulaire interactif** avec tous les paramètres frps et frpc organisés par importance :
  - Section principale : ports essentiels, authentification (token masqué avec bouton révéler)
  - Options avancées (accordéon) : TLS, ports optionnels (KCP, QUIC, vhost), performance, logs
  - Gestion des **tunnels (proxies)** pour frpc : ajout dynamique avec support tcp, udp, http, https, stcp, xtcp, Proxy Protocol v1/v2
- **Mode TOML brut** pour les utilisateurs avancés
- **Sauvegarde + Reload** en un clic

### Logs
- Lecture via `journalctl` ou fichier log
- **Streaming live** (SSE) avec coloration syntaxique (erreurs, warnings, succès)

### Mise à jour de frp
- Vérification automatique de la dernière version sur GitHub
- Téléchargement avec **miroirs de fallback** (ghproxy, ghfast, gh-proxy) si GitHub est inaccessible
- **Upload manuel** d'une archive `.tar.gz` si tous les accès réseau sont bloqués
- Arrêt/redémarrage automatique des services pendant la mise à jour (résout le "Text file busy")
- Test de connectivité des sources

### Mise à jour du panel lui-même
- Vérification de la dernière release depuis ce repo GitHub
- **Auto-update en un clic** : télécharge le zip, remplace les fichiers, redémarre frp-manager automatiquement

### Paramètres
- Configuration du panel : IP/port d'écoute, timeout de session
- **Accès sécurisé** : authentification par identifiant + mot de passe (hashé SHA-256)
- Version du panel avec statut de mise à jour

---

## Prérequis

- Linux avec **systemd**
- **Python 3.8+**
- **frp** installé (frps et/ou frpc) — [télécharger ici](https://github.com/fatedier/frp/releases)
- Architectures supportées : `amd64`, `arm64`, `arm`

---

## Installation

```bash
# 1. Télécharger la dernière release
wget https://github.com/Gogowwww/frp-manager/releases/latest/download/frp-manager.zip
unzip frp-manager.zip && cd frp-manager

# 2. Lancer l'installation (root requis)
sudo bash install.sh
```

Le script installe automatiquement les dépendances Python dans un virtualenv isolé, crée le service systemd `frp-manager`, et démarre le panel.

L'interface est ensuite accessible sur :
```
http://VOTRE_IP:8765
```

> **Note** : Le port et l'IP d'écoute sont configurables dans l'onglet **Paramètres** de l'interface, ou directement dans `/etc/frp-manager/frp-manager.json`.

---

## Structure des fichiers

```
/opt/frp-manager/          # Code du panel
  app.py                   # Serveur Flask
  frp-autoupdate.py        # Script d'auto-update frp (cron)
  venv/                    # Environnement Python isolé
  templates/
    index.html             # Interface web
    login.html             # Page de connexion

/etc/frp-manager/
  frp-manager.json         # Configuration du panel

/etc/frp/                  # Configurations frp
  frps.toml
  frps2.toml               # Instances supplémentaires
  frpc.toml

/var/log/frp/              # Logs frp
/var/lib/frp-manager/      # État persistant (versions installées)

/etc/systemd/system/
  frp-manager.service      # Service du panel

/etc/cron.d/
  frp-autoupdate           # Vérification auto des mises à jour frp (03h00)
```

---

## Configuration du panel

Le fichier de configuration se trouve dans `/etc/frp-manager/frp-manager.json` :

```json
{
  "bind_host": "0.0.0.0",
  "bind_port": 8765,
  "username": "admin",
  "password_hash": "",
  "session_timeout": 3600
}
```

| Clé | Description | Défaut |
|---|---|---|
| `bind_host` | IP d'écoute du panel | `0.0.0.0` |
| `bind_port` | Port du panel | `8765` |
| `username` | Identifiant de connexion | `admin` |
| `password_hash` | SHA-256 du mot de passe (géré via l'UI) | `""` (pas de mot de passe) |
| `session_timeout` | Durée de session en secondes | `3600` |

> Un redémarrage de `frp-manager` est nécessaire pour appliquer les changements de `bind_host` et `bind_port`.

---

## Mise à jour de frp

Depuis l'onglet **Mise à jour** :

1. Cliquez sur **🔍 Vérifier** pour contrôler la dernière version disponible
2. Cliquez sur **⬆ Installer** pour mettre à jour automatiquement

Si GitHub est inaccessible depuis votre serveur, utilisez la section **Installation manuelle** : téléchargez l'archive sur votre machine locale et uploadez-la directement dans l'interface.

---

## Mise à jour du panel

Depuis l'onglet **Paramètres → Version du panel** :

1. Cliquez sur **🔍 Vérifier**
2. Si une mise à jour est disponible, cliquez sur **⬆ Mettre à jour le panel**

Le panel télécharge automatiquement la release, remplace les fichiers et redémarre le service. La page se recharge seule une fois le redémarrage terminé.

---

## Sécurité

Il est **fortement recommandé** de configurer un mot de passe depuis l'onglet **Paramètres → Accès sécurisé** avant d'exposer le panel sur internet.

Pour une sécurité maximale :
- Placez le panel derrière un reverse proxy (nginx, NPM) avec HTTPS
- Restreignez l'accès par IP si possible
- Utilisez un token frp fort et unique pour chaque instance

---

## Désinstallation

```bash
sudo systemctl stop frp-manager
sudo systemctl disable frp-manager
sudo rm /etc/systemd/system/frp-manager.service
sudo rm -rf /opt/frp-manager /etc/frp-manager /etc/cron.d/frp-autoupdate
sudo systemctl daemon-reload
```

Les configurations frp dans `/etc/frp/` et les binaires dans `/usr/local/bin/` ne sont **pas supprimés** par cette procédure.

---

## Contribuer

Les contributions sont les bienvenues ! Pour proposer une amélioration :

1. Forkez le repo
2. Créez une branche : `git checkout -b feature/ma-feature`
3. Commitez vos changements
4. Ouvrez une Pull Request

Pour signaler un bug ou proposer une fonctionnalité, ouvrez une [issue](https://github.com/Gogowwww/frp-manager/issues).

---

## Licence

MIT — voir [LICENSE](LICENSE)

---

## Remerciements

- [fatedier/frp](https://github.com/fatedier/frp) — le projet frp sans lequel rien de tout ça n'aurait de sens
- Communauté open source pour les retours et contributions
