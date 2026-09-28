# Docker : fonctionnement, privilèges et risques

[English](docker.md) · **Français**

## Ce que fait le conteneur

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

## Implications : accès équivalent à root sur l'hôte

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
   [SECURITY.fr.md](../SECURITY.fr.md).
3. N'exposez pas le panel sur Internet sans reverse proxy, VPN ou filtrage
   par IP.

## Ouvrir le panel au réseau

Une nouvelle installation n'écoute que sur `127.0.0.1`. Trois possibilités :

- **Tunnel SSH** (rien à ouvrir) :
  `ssh -L 8765:127.0.0.1:8765 utilisateur@serveur`, puis
  `https://127.0.0.1:8765` ;
- **Reverse proxy local** (nginx, Caddy) qui écoute sur le réseau et relaie
  vers `127.0.0.1:8765` ; autorisez l'upgrade WebSocket sur `/ws/` ;
- **Écoute sur le réseau** : décommentez `FRP_MANAGER_HOST=0.0.0.0` dans
  `docker-compose.yml` (ou mettez `bind_host` à `0.0.0.0` dans Réglages),
  redémarrez, créez l'identifiant sans attendre et filtrez l'accès.

Une installation existante (antérieure à 0.0.51) garde son adresse d'écoute.

## Réduire les privilèges : état des lieux

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
panel, journaux en direct, installation de frp (page Mises à jour),
application d'une règle de pare-feu, suppression d'une instance. Si vous
validez une configuration réduite qui fonctionne, une pull request est la
bienvenue.

**Alternative plus sûre** : l'installation classique (`install.sh`) sans
Docker, avec le panel sur `127.0.0.1` et un accès par tunnel SSH ou VPN.
