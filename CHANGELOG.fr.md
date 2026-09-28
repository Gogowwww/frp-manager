# Journal des modifications

[English](CHANGELOG.md) · **Français**

Toutes les évolutions notables de FRP Manager sont notées ici.

Le format suit [Keep a Changelog](https://keepachangelog.com/fr/1.1.0/) et les
numéros de version se suivent (`0.0.50`, `0.0.51`…) : chaque version sort
d'abord en pré-release, puis devient la release définitive avec le même
numéro. Les versions antérieures à 0.0.51 sont décrites dans les
[notes de release GitHub](https://github.com/Gogowwww/frp-manager/releases).

## [Non publié]

## [0.0.52] - 2026-09-28

Le panel devient entièrement bilingue et a désormais son propre site.

### Ajouté

- Site du projet, [frp-manager.gogow.fr](https://frp-manager.gogow.fr) : page
  d'accueil, documentation (générée depuis les fichiers du dépôt) et démo en
  ligne, en anglais et en français (`website/`).
- Versions anglaises de SECURITY, CONTRIBUTING, CODE_OF_CONDUCT et de ce
  journal (les versions françaises sont désormais les fichiers `.fr.md`).
- Les messages renvoyés par le serveur (erreurs, confirmations, journaux
  d'installation de frp et du panel, avis des journaux en direct, libellés des
  ports du pare-feu) suivent la langue de l'interface, français ou anglais.
  `install.sh` et `frp-autoupdate.py` parlent la langue du système (français si
  `LANG` est en français, anglais sinon).

### Modifié

- L'anglais devient une langue officielle : le panel suit la langue du
  navigateur (anglais pour toute autre langue que le français) et l'anglais
  n'est plus marqué comme aperçu dans les Réglages.
- La démo est servie sous `/demo/` du site. Son panel a désormais un mot de
  passe, comme toute installation depuis 0.0.51, et la connexion ramène sur
  `/demo/` au lieu de `/demo/index.html`.
- Captures d'écran du site prises dans la démo, en anglais et en français
  (`website/shots.py`).

## [0.0.51] - 2026-09-28

Pré-release puis release définitive le même jour.

Version consacrée à la sécurité. La mise à jour depuis le panel ou par
`install.sh` est transparente : l'ancien mot de passe continue de fonctionner.

### Sécurité

- Mot de passe haché en argon2id (argon2-cffi), avec scrypt en repli si le
  module manque. L'ancien hash SHA-256 sans sel reste accepté et est rehaché
  automatiquement à la première connexion réussie. Comparaisons en temps
  constant.
- Configuration initiale obligatoire : sans mot de passe, le panel n'affiche
  plus que la page de création de l'identifiant (12 caractères minimum) ;
  aucune autre page ni route de l'API ne répond.
- Limitation des tentatives de connexion par adresse IP : 5 essais, puis
  verrouillage de 30 s doublé à chaque échec (1 h au plus).
- Jeton CSRF exigé sur toute requête qui modifie quelque chose ; origine des
  WebSocket vérifiée.
- Cookies de session HttpOnly et SameSite=Strict, Secure en HTTPS
  (`cookie_secure` derrière un reverse proxy TLS). Changer le mot de passe
  déconnecte les autres sessions.
- En-têtes Content-Security-Policy (scripts à nonce), X-Content-Type-Options,
  X-Frame-Options et Referrer-Policy ; réponses de l'API jamais mises en cache.
- Noms de services systemd et de conteneurs Docker validés par liste blanche ;
  plus aucune commande `sh -c` construite avec des données.
- Configurations TOML vérifiées (tomllib/tomli) avant d'être écrites ; écriture
  confinée aux dossiers de configuration de frp, liens symboliques résolus.
- Archives frp extraites sans `extractall` : chemins absolus ou avec `..`,
  liens, fichiers non ELF et bombes de décompression refusés ; taille des
  téléchargements et des envois plafonnée.
- Archives frp vérifiées par la somme SHA-256 publiée avec chaque release de
  frp ; miroirs tiers (ghproxy, ghfast, gh-proxy) désactivables (Réglages ou
  `FRP_MANAGER_NO_MIRRORS=1`) et jamais utilisés sans somme de contrôle.
- Mise à jour du panel vérifiée par la somme SHA-256 jointe à la release
  (`frp-manager.zip.sha256`, publiée à partir de cette version) et archive
  contrôlée avant extraction.
- Fichier de configuration du panel écrit de façon atomique, lisible par
  root seulement ; URL du webhook Discord masquée dans les journaux de
  `frp-autoupdate.py`.

### Modifié

- Une nouvelle installation écoute sur `127.0.0.1` au lieu de `0.0.0.0`.
  Les installations existantes gardent leur adresse d'écoute.
  `FRP_MANAGER_HOST` passe avant `bind_host`, et `FRP_MANAGER_PORT` fixe le
  port d'une nouvelle installation.
- Le panel refuse désormais d'écrire une configuration frp en dehors de
  `/etc/frp`, `/usr/local/etc/frp`, `/opt/frp` et `/root/frp`, ou une
  configuration TOML invalide (code 400 avec le détail de l'erreur).
- Mot de passe de 12 caractères minimum pour tout nouveau mot de passe
  (les mots de passe existants plus courts restent valides).
- README restructuré (démarrage rapide, sécurité, comparaison) ; détails
  déplacés dans `docs/`.

### Ajouté

- `python3 app.py --reset-password` pour redéfinir les identifiants depuis la
  console.
- Réglage « Miroirs de téléchargement tiers » (Réglages → Accès réseau).
- SECURITY.md, CONTRIBUTING.md, CODE_OF_CONDUCT.md, modèles d'issues et de
  pull request.
- Tests pytest (connexion, migration du hash, validation TOML, noms de
  services, archives) et intégration continue : tests, ruff, pip-audit.

[Non publié]: https://github.com/Gogowwww/frp-manager/compare/v0.0.52...HEAD
[0.0.52]: https://github.com/Gogowwww/frp-manager/compare/v0.0.51...v0.0.52
[0.0.51]: https://github.com/Gogowwww/frp-manager/compare/v0.0.50...v0.0.51
