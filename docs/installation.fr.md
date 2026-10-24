# Installation, configuration et désinstallation

[English](installation.md) · **Français**

- [Prérequis](#prérequis)
- [Installation par script](#installation-par-script)
- [Installation avec apt](#installation-avec-apt)
- [Docker et Portainer](#docker-et-portainer)
- [Depuis une version Python (0.0.57 et avant)](#depuis-une-version-python-0057-et-avant)
- [Première ouverture](#première-ouverture)
- [Accéder au panel depuis un autre poste](#accéder-au-panel-depuis-un-autre-poste)
- [Versions et pré-releases](#versions-et-pré-releases)
- [Configuration du panel](#configuration-du-panel)
- [Mot de passe perdu](#mot-de-passe-perdu)
- [Structure des fichiers](#structure-des-fichiers)
- [Désinstallation](#désinstallation)

## Prérequis

- Linux avec **systemd**
- Rien d'autre : le panel est un seul programme, sans dépendance (installation
  par script ou paquet `.deb`), ou **Docker**
- Architectures : `amd64`, `arm64`, `armv7`

Inutile d'installer frp avant : le script télécharge `frps`/`frpc` et crée
leurs services systemd. frp se met ensuite à jour depuis **Réglages → Mises à
jour**, quand vous le décidez : rien n'est mis à jour automatiquement.

## Installation par script

```bash
curl -LO https://github.com/Gogowwww/frp-manager/releases/latest/download/frp-manager.zip
unzip frp-manager.zip && cd frp-manager
sudo bash install.sh
```

Le script installe le programme `frp-manager` de votre architecture dans
`/opt/frp-manager`, crée le service systemd `frp-manager` et le démarre. Il
prépare aussi frp :

- binaires `frps` / `frpc` dans `/usr/local/bin` (dernière version, somme
  SHA-256 vérifiée) ;
- configurations par défaut `/etc/frp/frps.toml` et `/etc/frp/frpc.toml`,
  seulement si elles n'existent pas. Pour `frps.toml`, le script propose un
  port d'écoute tiré au hasard entre 10000 et 32767 (Entrée pour l'accepter,
  `7000` pour le port standard, ou un autre numéro) : les robots qui balaient
  Internet visent le port 7000. Sans terminal, le port aléatoire est pris ;
  `FRPS_BIND_PORT=7000 sudo -E bash install.sh` (ou `random`, ou un autre
  numéro) répond sans poser la question. Le port choisi est rappelé à la fin :
  ouvrez-le dans le pare-feu et reportez-le dans `serverPort` des clients ;
- services `frps.service` et `frpc.service`, créés mais ni activés ni
  démarrés : vous les configurez puis les lancez depuis le panel.

Relancer `install.sh` depuis une archive plus récente met le panel à jour en
conservant sa configuration. Le bouton **Mettre à jour** du panel fait la même
chose sans ligne de commande.

## Installation avec apt

FRP Manager a son dépôt APT (Debian, Ubuntu, Raspberry Pi OS ; `amd64`, `arm64`,
`armhf`). On l'ajoute une fois, puis le panel s'installe et se met à jour comme
n'importe quel paquet :

```bash
sudo install -d -m 0755 /etc/apt/keyrings
sudo curl -fsSL https://forgejo.gogow.fr/api/packages/administrator/debian/repository.key -o /etc/apt/keyrings/frp-manager.asc
echo "deb [signed-by=/etc/apt/keyrings/frp-manager.asc] https://forgejo.gogow.fr/api/packages/administrator/debian stable main" \
  | sudo tee /etc/apt/sources.list.d/frp-manager.list
sudo apt update
sudo apt install frp-manager
```

Les mises à jour arrivent ensuite avec `sudo apt update && sudo apt upgrade` (le
bouton **Mettre à jour** du panel le rappelle). Pour suivre les pré-releases,
remplacez `stable` par `testing` dans `/etc/apt/sources.list.d/frp-manager.list`
(`dev` : un paquet par commit de développement, pour les essais seulement).

Le paquet installe `/usr/bin/frp-manager` et le service `frp-manager`, puis
fait comme `install.sh` : services `frps` et `frpc` (créés, pas démarrés),
configurations par défaut avec un port frps aléatoire
(`sudo FRPS_BIND_PORT=7000 apt install frp-manager` pour le port standard),
téléchargement de frp, démarrage du panel. Une installation faite avec
`install.sh` passe sous le contrôle du paquet, sa configuration comprise.

`sudo apt remove frp-manager` arrête et retire le panel en gardant sa
configuration ; `sudo apt purge frp-manager` l'efface aussi. frp, ses
configurations et ses services restent en place. Pour retirer le dépôt :
`sudo rm /etc/apt/sources.list.d/frp-manager.list /etc/apt/keyrings/frp-manager.asc`.

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

Les images sont publiées pour `amd64`, `arm64` (Raspberry Pi 4/5, serveurs ARM)
et `armv7` à partir de la version 0.0.54 ; Docker choisit automatiquement la
bonne. Les versions antérieures n'existent qu'en `amd64`.

### Ce que fait le conteneur

Le panel tourne dans le conteneur mais pilote frp **sur l'hôte** : services
systemd `frps`/`frpc`, journaux, binaires dans `/usr/local/bin`, règles
nftables du pare-feu. Il n'embarque pas systemd ; il exécute les commandes
dans l'hôte avec `nsenter` :

```
nsenter -t 1 -m -u -i -n -p -- systemctl restart frpc
```

`-t 1` vise le processus 1 **de l'hôte**, visible grâce à `pid: host` ; les
options `-m -u -i -n -p` font entrer la commande dans ses espaces de noms
(montages, nom d'hôte, IPC, réseau, processus). La commande s'exécute alors
sur le système de fichiers et avec les services de l'hôte, avec les
capacités du conteneur.

### Implications : accès équivalent à root sur l'hôte

Avec la configuration fournie (`pid: host`, `privileged: true`,
`network_mode: host`, socket Docker monté), **le conteneur n'isole rien** :

- `privileged: true` accorde toutes les capacités Linux, l'accès aux
  périphériques et désactive les profils seccomp et AppArmor par défaut ;
- `pid: host` + `nsenter -t 1` permettent de lancer n'importe quelle commande
  comme root dans l'hôte ;
- le socket Docker (`/var/run/docker.sock`) permet à lui seul de créer un
  conteneur qui monte `/` de l'hôte : c'est aussi un accès root.

Conséquences pratiques :

1. Toute personne connectée au panel contrôle la machine hôte. C'est déjà le
   cas en installation classique (le service tourne en root) : Docker ne
   réduit pas ce risque.
2. Une faille du panel exploitable à distance donnerait un accès root à
   l'hôte. D'où la configuration initiale obligatoire, l'écoute sur
   `127.0.0.1` par défaut et les protections décrites dans
   [SECURITY.md](SECURITY.md) (en anglais).
3. N'exposez pas le panel sur Internet sans reverse proxy, VPN ou filtrage
   par IP : voir
   [Accéder au panel depuis un autre poste](#accéder-au-panel-depuis-un-autre-poste).

### Réduire les privilèges : état des lieux

La demande est légitime : remplacer `privileged: true` par
`cap_drop: [ALL]` plus quelques capacités, `read_only: true` et
`no-new-privileges`. **Cette réduction n'est pas encore validée** : elle n'a
pas pu être testée sur un hôte Docker réel, et la configuration fournie reste
donc `privileged: true`. Voici l'analyse, pour qui voudrait l'essayer.

Ce dont le panel a besoin, fonctionnalité par fonctionnalité :

| Fonctionnalité | Mécanisme | Capacités probablement nécessaires |
|---|---|---|
| Entrer dans les espaces de noms de l'hôte | `nsenter -t 1` (setns) | `SYS_ADMIN`, `SYS_PTRACE`, `SYS_CHROOT` |
| Services frp, journaux | `systemctl`, `journalctl` via nsenter | aucune de plus (socket systemd, root) |
| Pare-feu | `nft` via nsenter | `NET_ADMIN` |
| Écriture des binaires, configs, unités | fichiers de l'hôte | `DAC_OVERRIDE`, `CHOWN`, `FOWNER` |
| Bouton « Installer nftables » | gestionnaire de paquets de l'hôte | `SETUID`, `SETGID`, `CHOWN`, `FOWNER`, `DAC_OVERRIDE`, `KILL`… |

Limites de l'exercice :

- **`SYS_ADMIN` suffit déjà à sortir du conteneur** (c'est la capacité qui
  permet `setns` vers l'hôte). Le gain réel d'un `cap_drop` est donc faible
  tant que le panel doit piloter l'hôte : il bloque surtout des attaques
  opportunistes, pas un attaquant qui contrôle le panel.
- Le profil seccomp par défaut de Docker n'autorise `setns` qu'avec
  `SYS_ADMIN` ; AppArmor (`docker-default`) peut aussi bloquer certaines
  opérations. Il faudra peut-être `security_opt: [apparmor:unconfined]`.
- `read_only: true` demande un `tmpfs` pour `/tmp` (téléchargements et
  extractions temporaires) ; les dossiers écrits sont déjà des volumes.
- Le socket Docker reste un accès root : retirez-le si vous n'avez pas de
  conteneurs frp à piloter.

Configuration candidate, **non vérifiée**, à tester sur une machine de test :

```yaml
    # à la place de privileged: true
    cap_drop: [ALL]
    cap_add: [SYS_ADMIN, SYS_PTRACE, SYS_CHROOT, NET_ADMIN, DAC_OVERRIDE, CHOWN, FOWNER, SETUID, SETGID, KILL]
    security_opt:
      - no-new-privileges:true
    read_only: true
    tmpfs:
      - /tmp
```

Points à vérifier après l'essai : démarrage/arrêt d'un service frp depuis le
panel, journaux en direct, installation de frp (Réglages → Mises à jour),
application d'une règle de pare-feu, suppression d'une instance. Si vous
validez une configuration réduite qui fonctionne, une pull request est la
bienvenue.

**Alternative plus sûre** : l'installation classique (`install.sh`) sans
Docker, avec le panel sur `127.0.0.1` et un accès par tunnel SSH ou VPN.

## Depuis une version Python (0.0.57 et avant)

Jusqu'à la 0.0.57, le panel était une application Python ; depuis la 0.0.58,
c'est un programme Go unique. Rien à faire de particulier : le bouton
**Mettre à jour** de l'ancien panel installe la nouvelle version comme
d'habitude. Au redémarrage, celle-ci télécharge le programme Go de votre
architecture (somme SHA-256 vérifiée), l'installe dans `/opt/frp-manager`,
fait pointer le service `frp-manager` dessus et relance le panel.

La configuration ne change pas : comptes, mots de passe, réglages, webhooks,
pare-feu et connexion unique sont gardés ; il faut seulement se reconnecter
une fois. Relancer `install.sh` depuis la nouvelle archive fait la même chose
et retire en plus l'environnement Python devenu inutile
(`/opt/frp-manager/venv`). En Docker, `docker compose pull && docker compose up -d`
suffit.

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

## Passkeys

Chaque compte qui se connecte par mot de passe peut ajouter des **passkeys**
(Réglages → Mon compte → Passkeys ; le mot de passe est redemandé) : l'appareil
vous connecte ensuite tout seul (empreinte, visage, code PIN, clé de sécurité),
sans saisir d'identifiant ni de mot de passe. Le mot de passe reste valable.

- Les navigateurs n'autorisent les passkeys que sur un **nom de domaine avec un
  certificat HTTPS valide**, par exemple derrière un reverse proxy avec
  Let's Encrypt (`https://panel.exemple.fr`), ou sur `http://localhost`. Le
  certificat auto-signé du panel et l'accès par adresse IP
  (`https://192.0.2.10:8765`) sont refusés : la carte Passkeys l'indique.
- Une passkey est liée au domaine sur lequel elle a été créée : créée sur
  `panel.exemple.fr`, elle ne sert que là.
- Derrière un reverse proxy, transmettez l'en-tête `Host` d'origine
  (`proxy_set_header Host $host;`, comme ci-dessus) ou `X-Forwarded-Host`.
- Appareil perdu : supprimez la passkey dans Réglages → Mon compte après vous
  être connecté avec votre mot de passe, ou demandez à un administrateur de
  retirer les passkeys du compte (Utilisateurs → compte → *Retirer ses
  passkeys*). Les sessions ouvertes avec une passkey supprimée sont fermées.

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

## Notifications par webhook

**Réglages → Notifications** envoie un message à l'adresse de votre choix quand quelque chose se passe sur la
machine. Chaque webhook a sa destination, son format, ses événements et sa langue : les pannes vers Discord,
les mises à jour vers ntfy, les événements de sécurité vers votre propre service.

| Format | Adresse à saisir |
| --- | --- |
| Discord | l'adresse du webhook d'un salon (Paramètres, Intégrations) ; envoyé sous forme de carte colorée |
| Slack / Mattermost | l'adresse d'un Incoming Webhook |
| Telegram | `https://api.telegram.org/bot<jeton>` et l'identifiant de conversation |
| ntfy | l'adresse du sujet, `https://ntfy.sh/mon-sujet` ou votre serveur ; la priorité suit la gravité |
| Gotify | `https://gotify.example.org/message?token=…` |
| JSON (générique) | n'importe quelle adresse ; corps `{event, level, title, message, host, time, panel_version, instance, data}`, ou votre propre modèle |
| Texte brut | n'importe quelle adresse ; titre, message et nom de la machine en texte |

Événements : instance arrêtée ou démarrée (contrôlée toutes les 15 secondes et confirmée par un second
relevé : un redémarrage rapide ne déclenche pas d'alerte), adresse verrouillée après des échecs de
connexion, connexion, configuration ou pare-feu modifié, mise à jour du panel ou de frp disponible
(vérifiée toutes les 6 heures, annoncée une seule fois par version), frp mis à jour, panel démarré.
Réglages facultatifs : limiter à certaines instances, un délai entre deux envois du même événement, la
langue des messages, des en-têtes HTTP supplémentaires et un secret de signature.

Le format générique accepte un modèle JSON avec les variables `{{event}}`, `{{title}}`, `{{message}}`,
`{{level}}`, `{{host}}`, `{{time}}`, `{{instance}}`, `{{subject}}` et `{{version}}`, échappées pour le JSON :
`{"content": "{{title}} : {{message}}"}`. Avec un secret de signature, chaque requête porte
`X-FRPManager-Signature: sha256=<HMAC-SHA256 du corps>` :

```go
mac := hmac.New(sha256.New, []byte(secret)) // crypto/hmac, crypto/sha256
mac.Write(body)
expected := "sha256=" + hex.EncodeToString(mac.Sum(nil))
ok := hmac.Equal([]byte(expected), []byte(r.Header.Get("X-FRPManager-Signature")))
```

Les envois passent par une file et sont retentés trois fois en cas d'erreur réseau, de 5xx ou de 429 ; les
derniers sont listés sur la page. L'adresse, le secret et les en-têtes sont stockés dans le fichier de
configuration (lisible par root seulement) et ne reviennent jamais dans le navigateur : laissez un champ
vide en modifiant pour garder la valeur enregistrée. Les redirections ne sont pas suivies.

## Mot de passe perdu

```bash
sudo /opt/frp-manager/frp-manager --reset-password     # paquet .deb : sudo frp-manager --reset-password
sudo systemctl restart frp-manager
```

En Docker : `docker exec -it frp-manager frp-manager --reset-password`, puis
`docker restart frp-manager`.

Les passkeys du propriétaire sont gardées : elles permettent toujours de se
connecter.

## Structure des fichiers

```
Dépôt
  panel/                   Le panel : programme Go (go build ./panel)
    *.go                   Serveur HTTP, API, pare-feu, mises à jour… ; *_test.go : tests
    install.sh             Installation par script
    templates/             Interface, intégrée au programme à la compilation
      index.html, login.html, setup.html
      partials/icons.html  Icônes SVG (embarquées, sans CDN)
      assets/
        css/app.css        Thèmes clair et sombre
        js/                Application (modules ES, sans étape de build)
        locales/fr.js, en.js  Textes de l'interface
  docs/                    Documentation, politique de sécurité, guide de contribution
                           (aussi sur le site) ; img/ : captures
  internal/                Code partagé (règle des adresses publiques)
  tools/                   Outils du dépôt : blocklists (listes communautaires du
                           pare-feu), releasenotes (notes des releases)
  packaging/               Paquet .deb, passage depuis une version Python
  Dockerfile, docker-compose.yml, go.mod

/opt/frp-manager/          Panel installé (script) ; /usr/bin/frp-manager (paquet .deb)
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

Avec le paquet `.deb` : `sudo apt purge frp-manager`.

Les configurations frp (`/etc/frp/`) et les binaires (`/usr/local/bin/`) sont
conservés.
