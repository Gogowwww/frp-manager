# Politique de sécurité

[English](SECURITY.md) · **Français**

FRP Manager pilote des services réseau exposés à Internet (frps, frpc), des
règles de pare-feu et, selon l'installation, le système hôte avec les droits
root. Une faille dans le panel peut donc avoir des conséquences sérieuses :
merci de la signaler de façon responsable.

## Signaler une vulnérabilité

- **Ne l'ouvrez pas en issue publique.**
- Utilisez le signalement privé de GitHub : onglet **Security** du dépôt, bouton
  **Report a vulnerability**
  ([lien direct](https://github.com/Gogowwww/frp-manager/security/advisories/new)).
- Indiquez la version du panel (en bas de la barre latérale), le mode
  d'installation (script ou Docker), les étapes pour reproduire et l'impact
  que vous envisagez.

Ce projet est maintenu bénévolement : comptez un accusé de réception sous
7 jours et, pour une faille confirmée, un correctif publié en pré-release dès
que possible. Le signalement est crédité dans les notes de version si vous le
souhaitez.

## Versions prises en charge

| Version | Correctifs de sécurité |
|---|---|
| Dernière release (`:latest`) | oui |
| Dernière pré-release (`:dev`) | oui |
| Versions antérieures | non : mettez à jour |

## Modèle de menace (résumé)

**Ce que le panel protège**

- L'accès à l'interface et à l'API : identifiant et mot de passe obligatoires
  (configuration initiale imposée au premier lancement), mot de passe haché en
  argon2id (scrypt en repli), limitation des tentatives par IP, sessions
  HttpOnly / SameSite=Strict / Secure en HTTPS, jeton CSRF sur toute requête
  qui modifie quelque chose.
- Le navigateur de l'administrateur : Content-Security-Policy stricte pour les
  scripts (nonce), X-Frame-Options, échappement systématique des données
  (autoescape Jinja2, DOM construit sans `innerHTML`).
- Le système : aucune commande passée à un shell avec des données saisies,
  noms de services et de conteneurs validés par liste blanche, configurations
  validées (TOML parsé) et confinées aux dossiers de configuration de frp,
  archives extraites sans `extractall` (chemins, liens, taille contrôlés).
- La chaîne d'approvisionnement : archives frp vérifiées par la somme SHA-256
  publiée avec chaque release de fatedier/frp ; miroirs tiers (ghproxy, ghfast,
  gh-proxy) désactivables et jamais utilisés sans somme de contrôle ; archive
  de mise à jour du panel vérifiée par sa somme SHA-256 publiée.

**Ce que le panel ne protège pas**

- **Un administrateur authentifié a, par conception, un pouvoir équivalent à
  root sur la machine** : il installe des binaires, écrit des unités systemd,
  modifie le pare-feu. Protéger le mot de passe revient à protéger la machine.
- **En Docker, `pid: host`, `privileged: true` et le socket Docker donnent au
  conteneur un accès équivalent à root sur l'hôte** (voir
  [docs/docker.fr.md](docker.fr.md)). L'isolation du conteneur ne limite pas ce
  que le panel peut faire.
- Le certificat HTTPS généré est auto-signé : il chiffre, mais n'authentifie
  pas le serveur. Pour un accès depuis Internet, placez le panel derrière un
  reverse proxy avec un certificat valide, ou n'y accédez que par VPN ou
  tunnel SSH.
- Derrière un reverse proxy, la limitation des tentatives voit l'adresse du
  proxy : filtrez aussi l'accès à ce niveau.
- Les tokens frp sont stockés en clair dans les fichiers TOML, comme frp
  l'exige ; l'éditeur de configuration les affiche aux administrateurs.

**Recommandations**

1. Laissez le panel sur `127.0.0.1` (valeur par défaut d'une nouvelle
   installation) et accédez-y par tunnel SSH, VPN ou reverse proxy.
2. Si vous l'ouvrez au réseau, créez l'identifiant immédiatement et filtrez
   l'accès par IP.
3. Utilisez un mot de passe long et unique, et un token frp long et unique
   par serveur.
4. Désactivez les miroirs tiers (Réglages → Accès réseau) si github.com vous
   est accessible.

## Base de données

Le panel n'utilise aucune base de données SQL : sa configuration est un
fichier JSON (`/etc/frp-manager/frp-manager.json`, lisible par root seulement)
et son état un autre (`/var/lib/frp-manager/state.json`). Il n'y a donc pas de
surface d'injection SQL.
