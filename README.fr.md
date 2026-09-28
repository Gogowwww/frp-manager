# FRP Manager

Interface web et tableau de bord auto-hébergés pour [frp](https://github.com/fatedier/frp) : pilotez **frps** et **frpc** depuis le navigateur, au lieu d'éditer des fichiers TOML, de redémarrer des services systemd et de lire les journaux en SSH.

[![Licence](https://img.shields.io/github/license/Gogowwww/frp-manager)](LICENSE)
[![Release](https://img.shields.io/github/v/release/Gogowwww/frp-manager)](https://github.com/Gogowwww/frp-manager/releases)
![Plateforme](https://img.shields.io/badge/platform-Linux-blue)
[![Docker](https://img.shields.io/badge/image-ghcr.io-2496ED?logo=docker&logoColor=white)](https://github.com/users/Gogowwww/packages/container/package/frp-manager)
[![Site](https://img.shields.io/badge/site-frp--manager.gogow.fr-0e9f6e)](https://frp-manager.gogow.fr/fr/)

[English](README.md) · **Français**

![Tableau de bord de FRP Manager](panel/home.png)

## Démarrage rapide

Avec Docker Compose, sur la machine qui fait (ou fera) tourner frps ou frpc :

```bash
curl -O https://raw.githubusercontent.com/Gogowwww/frp-manager/main/docker-compose.yml
docker compose up -d
```

Le panel n'écoute que sur `127.0.0.1:8765`. Depuis votre ordinateur, ouvrez un tunnel SSH avec `ssh -L 8765:127.0.0.1:8765 utilisateur@serveur`, allez sur `https://127.0.0.1:8765` et créez l'identifiant administrateur. Pour l'ouvrir à votre réseau, voir [Accéder au panel depuis un autre poste](docs/installation.fr.md#accéder-au-panel-depuis-un-autre-poste).

Sans Docker, le script d'installation met en place le panel, frp et leurs services systemd :

```bash
curl -LO https://github.com/Gogowwww/frp-manager/releases/latest/download/frp-manager.zip
unzip frp-manager.zip && cd frp-manager && sudo bash install.sh
```

Pas encore prêt à installer ? La [démo en ligne](https://frp-manager.gogow.fr/demo/) fait tourner la vraie interface avec des données fictives.

## Fonctionnalités

- **Tableau de bord** : détection automatique des services systemd frps/frpc, des binaires, des configurations et des conteneurs Docker ; démarrer, arrêter, redémarrer, démarrage automatique ; état en direct par WebSocket ; surnoms.
- **Ports (frpc)** : liste lisible (`vps.example.net:25565 → 127.0.0.1:25565`) et éditeur guidé pour `tcp`, `udp`, `http`, `https`, `stcp`, `xtcp` et les visiteurs, avec options PROXY protocol, chiffrement et compression. Les réglages que l'éditeur ne gère pas sont conservés tels quels.
- **Pare-feu (frps)** : règles « autoriser seulement » ou « bloquer » par adresse, réseau ou fournisseur entier (numéro d'AS), appliquées dans le noyau avec nftables avant le NAT de Docker ; connexions bloquées en temps réel ; listes communautaires facultatives.
- **Configuration** : réglages de frps et frpc par sections, chaque champ affichant sa clé TOML ; les configurations sont vérifiées avant d'être écrites.
- **Journaux** : journal systemd, fichier de log ou conteneur Docker, en direct (WebSocket, repli SSE), avec filtre.
- **Mises à jour** : installation de frp en un clic avec vérification SHA-256, envoi manuel d'une archive, mise à jour du panel en un clic.
- **Interface** : thèmes clair et sombre, ordinateur et mobile, français et anglais (anglais en aperçu : certains messages du serveur restent en français).

| Ports | Éditeur de port | Journaux en direct |
|:---:|:---:|:---:|
| ![Ports](panel/tunnels.png) | ![Éditeur de port](panel/tunnel-editor.png) | ![Journaux](panel/logs.png) |

## Pourquoi ce panel

frp est un binaire unique configuré par des fichiers TOML. Le piloter à la main fonctionne bien, mais la gestion au quotidien reste manuelle : éditer un fichier en SSH pour chaque nouveau port, redémarrer le bon service, vérifier quel port est déjà pris, retrouver le bon journal. FRP Manager garde ce modèle et ajoute une couche par-dessus :

| Tâche | frp à la main | FRP Manager |
|---|---|---|
| Ajouter ou modifier un tunnel | éditer `frpc.toml`, redémarrer frpc | formulaire guidé, TOML vérifié avant l'enregistrement, enregistrer et redémarrer en un clic |
| Voir ce qui est exposé | lire les configurations | liste des ports avec leur chemin, par client |
| Plusieurs instances frps/frpc | un service et un fichier par instance à suivre | détectées automatiquement, une carte chacune |
| Filtrer qui atteint les ports de frps | écrire des règles nftables ou iptables | règles par IP, réseau ou AS, testées avant application |
| Suivre les journaux | `journalctl -fu frpc` en SSH | journaux en direct dans le navigateur |
| Mettre à jour frp | télécharger, vérifier, remplacer les binaires, redémarrer | un clic, SHA-256 vérifiée, services redémarrés |

Le panel ne remplace pas frp et ne change pas son format de configuration : les fichiers restent du TOML que vous pouvez toujours éditer à la main, et retirer le panel laisse frp fonctionner comme avant.

## Sécurité

Le panel contrôle des ports exposés et tourne en root : il est verrouillé par défaut.

- **Configuration initiale obligatoire** : aucune page ni route de l'API ne répond tant qu'aucun compte administrateur n'existe (mot de passe de 12 caractères ou plus).
- **Mot de passe haché** en argon2id (scrypt en repli) ; les anciens hashs SHA-256 sont acceptés une fois puis rehachés automatiquement.
- **Écoute sur `127.0.0.1`** pour une nouvelle installation ; l'ouvrir au réseau est un choix explicite.
- **Protection contre la force brute** : tentatives limitées par IP, avec un verrouillage croissant.
- **Durcissement web** : jeton CSRF sur chaque modification, cookies HttpOnly / SameSite=Strict / Secure, Content-Security-Policy, X-Frame-Options, Referrer-Policy.
- **Durcissement système** : aucune commande shell construite à partir d'une saisie, noms de services et de conteneurs sur liste blanche, configurations confinées aux dossiers de frp, extraction d'archives sécurisée.
- **Chaîne d'approvisionnement** : archives frp et mises à jour du panel vérifiées par leur somme SHA-256 publiée ; miroirs tiers (ghproxy, ghfast, gh-proxy) désactivables et jamais utilisés sans somme de contrôle.

> **Avertissement Docker** : le conteneur utilise `pid: host`, `privileged: true` et `nsenter` pour piloter le systemd de l'hôte, ainsi que le socket Docker. **C'est un accès équivalent à root sur l'hôte** : qui contrôle le panel contrôle la machine. Lisez [docs/docker.fr.md](docs/docker.fr.md) avant de le déployer.

Modèle de menace et signalement des vulnérabilités : [SECURITY.fr.md](SECURITY.fr.md).

## Documentation

- [Site du projet](https://frp-manager.gogow.fr/fr/) : présentation, démo en ligne et toute la documentation
- [Installation, configuration et désinstallation](docs/installation.fr.md) : prérequis, images Docker, versions et pré-releases, clés de configuration, reverse proxy, mot de passe perdu, structure des fichiers
- [Docker : privilèges et risques](docs/docker.fr.md)
- [Politique de sécurité](SECURITY.fr.md)
- [Journal des modifications](CHANGELOG.fr.md)

## Contribuer

Signalements de bugs, idées, traductions et pull requests sont les bienvenus : voir [CONTRIBUTING.fr.md](CONTRIBUTING.fr.md) et le [code de conduite](CODE_OF_CONDUCT.fr.md). Des adresses à bloquer peuvent être partagées avec le bouton **Publier** du pare-feu ou sur la branche [`blocklists`](https://github.com/Gogowwww/frp-manager/tree/blocklists).

## Licence

[Apache 2.0](LICENSE).

## Remerciements

[fatedier/frp](https://github.com/fatedier/frp), le projet autour duquel ce panel est construit.

## Développement

FRP Manager est développé avec l'aide d'un assistant de programmation IA ([Claude](https://claude.ai)). Chaque nouvelle version sort d'abord en pré-release, pour être testée avant de devenir une release.

[![Star History Chart](https://api.star-history.com/chart?repos=Gogowwww/frp-manager&type=date&legend=bottom-right)](https://www.star-history.com/?repos=Gogowwww%2Ffrp-manager&type=date&legend=bottom-right)
