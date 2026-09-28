# Métadonnées GitHub à saisir à la main

Éléments à copier dans les réglages du dépôt
[Gogowwww/frp-manager](https://github.com/Gogowwww/frp-manager) : rien ici
n'est appliqué automatiquement.

## Description courte (About → Description)

Version anglaise, conseillée car le README affiché par défaut est en anglais :

> Self-hosted web UI and dashboard for frp: manage frps and frpc, tunnels, an nftables firewall, live logs and updates from your browser.

Version française :

> Interface web auto-hébergée pour frp : pilotez frps et frpc, tunnels, pare-feu nftables, journaux en direct et mises à jour depuis le navigateur.

Site web (About → Website) : `https://demo-frp-manager.gogow.fr`

## Topics (About → Topics)

```
frp frps frpc self-hosted homelab reverse-proxy tunneling web-ui docker flask systemd port-forwarding
```

## Autres réglages conseillés

- **Settings → Code security → Private vulnerability reporting** : activer,
  sinon le lien de signalement de SECURITY.md ne fonctionne pas.
- **Settings → General → Social preview** : téléverser une image 1280×640
  (par exemple `demo/og.png`, déjà utilisée par la démo).
- **Settings → Actions** : le workflow `.github/workflows/ci.yml` (tests, ruff,
  pip-audit) ne tourne que si GitHub Actions est disponible sur le compte.

## Texte de la release v0.0.51

Titre : `v0.0.51 — Sécurité : configuration initiale, argon2id, CSRF, téléchargements vérifiés`

```markdown
Version consacrée à la sécurité du panel. La mise à jour est transparente : votre mot de passe actuel continue de fonctionner et votre adresse d'écoute ne change pas.

## Nouveautés

- **Configuration initiale obligatoire** : un panel sans mot de passe n'affiche plus que la page de création de l'identifiant administrateur (12 caractères minimum). Plus aucun panel ouvert à tous.
- **Mot de passe haché en argon2id** (scrypt en repli). L'ancien format est rehaché automatiquement à votre prochaine connexion.
- **Protection contre la force brute** : 5 essais par adresse IP, puis verrouillage progressif.
- **Protection CSRF**, cookies HttpOnly / SameSite=Strict / Secure, en-têtes Content-Security-Policy, X-Frame-Options et Referrer-Policy.
- **Téléchargements vérifiés** : archives frp contrôlées par leur somme SHA-256 publiée, mise à jour du panel vérifiée par la somme jointe à la release. Les miroirs tiers (ghproxy, ghfast, gh-proxy) peuvent être désactivés dans Réglages → Accès réseau et ne sont jamais utilisés sans somme de contrôle.
- **Configurations vérifiées** : un TOML invalide n'est plus jamais écrit sur le disque.
- `python3 app.py --reset-password` pour redéfinir les identifiants depuis la console.
- SECURITY.md, guide de contribution, tests automatisés, README réorganisé.

## Changements à connaître

- Une **nouvelle installation écoute sur 127.0.0.1** : accédez-y par tunnel SSH (`ssh -L 8765:127.0.0.1:8765 utilisateur@serveur`) ou ouvrez-la au réseau dans Réglages / avec `FRP_MANAGER_HOST=0.0.0.0` en Docker. Les installations existantes ne changent pas.
- Le panel n'écrit plus de configuration frp en dehors de `/etc/frp`, `/usr/local/etc/frp`, `/opt/frp` et `/root/frp`.
- Docker : la documentation détaille désormais ce qu'impliquent `pid: host` et `privileged` (accès équivalent à root sur l'hôte) : voir docs/docker.md.

## Mettre à jour

- **Installation par script** : bouton « Mettre à jour » du panel, ou `sudo bash install.sh` depuis la nouvelle archive.
- **Docker** : `docker compose pull && docker compose up -d`.

Après la mise à jour, rechargez la page du panel et reconnectez-vous.

**Full Changelog**: https://github.com/Gogowwww/frp-manager/compare/v0.0.50...v0.0.51

---

## English

A security-focused release. Updating is seamless: your current password keeps working and your listening address does not change.

### New

- **Mandatory initial setup**: a panel without a password now only shows the administrator account creation page (12 characters minimum). No more panels open to everyone.
- **Password hashed with argon2id** (scrypt as a fallback). The old format is rehashed automatically at your next login.
- **Brute-force protection**: 5 attempts per IP address, then a growing lockout.
- **CSRF protection**, HttpOnly / SameSite=Strict / Secure cookies, Content-Security-Policy, X-Frame-Options and Referrer-Policy headers.
- **Verified downloads**: frp archives checked against their published SHA-256, panel update checked against the checksum attached to the release. Third-party mirrors (ghproxy, ghfast, gh-proxy) can be disabled in Settings → Network access and are never used without a checksum.
- **Validated configurations**: an invalid TOML file is never written to disk.
- `python3 app.py --reset-password` to reset the credentials from the console.
- SECURITY.md, contribution guide, automated tests, reorganized README.

### Good to know

- A **new install listens on 127.0.0.1**: reach it through an SSH tunnel (`ssh -L 8765:127.0.0.1:8765 user@server`) or open it to the network in Settings / with `FRP_MANAGER_HOST=0.0.0.0` on Docker. Existing installs do not change.
- The panel no longer writes frp configurations outside `/etc/frp`, `/usr/local/etc/frp`, `/opt/frp` and `/root/frp`.
- Docker: the documentation now explains what `pid: host` and `privileged` imply (root-equivalent access to the host): see docs/docker.md.

### How to update

- **Install script**: the panel's "Update" button, or `sudo bash install.sh` from the new archive.
- **Docker**: `docker compose pull && docker compose up -d`.

After updating, reload the panel page and log in again.
```
