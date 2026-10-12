# Journal des modifications

[English](CHANGELOG.md) · **Français**

Toutes les évolutions notables de FRP Manager sont notées ici.

Le format suit [Keep a Changelog](https://keepachangelog.com/fr/1.1.0/) et les
numéros de version se suivent (`0.0.50`, `0.0.51`…) : chaque version sort
d'abord en pré-release, puis devient la release définitive avec le même
numéro. Les versions antérieures à 0.0.51 sont décrites dans les
[notes de release GitHub](https://github.com/Gogowwww/frp-manager/releases).

## [Non publié]

## [0.0.58] - 2026-10-12

Pré-release : première version du panel en Go.

### Modifié

- **Le panel est réécrit en Go** : un seul programme statique, sans Python ni
  dépendance à installer, pour amd64, arm64 et armv7 ; environ 18 Mo de
  mémoire au repos, et une image Docker bien plus légère (Alpine).
  L'interface, l'API, la configuration (`/etc/frp-manager/frp-manager.json`),
  les comptes et les mots de passe restent les mêmes ; seule la session est à
  rouvrir une fois. Les mots de passe hachés par les versions Python (argon2id,
  scrypt, ancien SHA-256) sont acceptés, puis rehachés en argon2id si besoin.
- **Passage automatique depuis une version Python** : la mise à jour depuis la
  0.0.57 ou avant (bouton **Mettre à jour**) installe le programme Go de la
  machine, somme SHA-256 vérifiée, et fait pointer le service dessus.
  `install.sh` fait de même et retire l'environnement Python devenu inutile.
- La mise à jour du panel exige désormais la somme SHA-256 publiée avec la
  release : le programme qui tourne n'est jamais remplacé sans elle.
- Les calculs argon2 (connexion, changement de mot de passe) sont limités à
  deux à la fois : une rafale de tentatives ne peut plus épuiser la mémoire.
- **Configuration** rejoint les Réglages, dans un groupe **frp** : le choix de
  l'instance passe dans l'en-tête de la section, et quitter la section avec des
  modifications non enregistrées demande toujours confirmation. L'ancienne
  adresse (`#/config?iid=…`) mène à la section.
- Le site du projet (accueil, documentation, démo en ligne) est lui aussi
  construit et servi par un programme Go (`go run ./website build|serve`), avec
  les mêmes pages ; ses captures d'écran passent en WebP sans perte. La page
  d'accueil gagne un onglet **apt** à côté de Docker et du script d'installation.

### Ajouté

- **Dépôt APT** : une fois le dépôt ajouté (clé et ligne `deb`, voir la
  documentation d'installation), `sudo apt install frp-manager` installe le
  panel et `apt upgrade` le met à jour. Le paquet prépare frp comme
  `install.sh` (services frps/frpc, configurations par défaut avec un port frps
  aléatoire, téléchargement de frp). Distribution `stable` pour les releases,
  `testing` pour les pré-releases ; amd64, arm64 et armhf. Les `.deb` sont aussi
  joints à chaque release.
- **Port de frps aléatoire** : à l'installation, `install.sh` propose un port
  d'écoute tiré au hasard entre 10000 et 32767 pour `frps.toml` (Entrée pour
  l'accepter, `7000` ou un autre numéro sinon), libre sur la machine, au lieu
  du 7000 que visent les robots ; `FRPS_BIND_PORT` répond sans question, et le
  port est rappelé en fin d'installation. Dans Réglages → Configuration, un
  bouton **Aléatoire** à côté du port d'écoute de frps fait le même tirage
  (installations Docker comprises).

### Corrigé

- Les connexions bloquées par le pare-feu s'affichent de nouveau en direct :
  depuis la 0.0.56, le WebSocket `/ws/firewall` se fermait aussitôt ouvert, et
  l'interface se rabattait sur une relecture périodique.

## [0.0.57] - 2026-10-12

Pré-release.

### Modifié

- **Tous les réglages au même endroit** : Notifications, Mises à jour,
  Utilisateurs et Connexion unique quittent le menu principal pour devenir des
  sections de **Réglages**, rangées en trois groupes (Personnel : Mon compte,
  Apparence ; Accès et sécurité : Accès au panel, Utilisateurs, Connexion
  unique ; Système : Notifications, Mises à jour). Un menu à gauche, une rangée
  défilante sur mobile ; chaque compte ne voit que les sections permises. Les
  anciennes adresses (`#/notifications`, `#/updates`, `#/users`) mènent à la
  bonne section, et la pastille de mise à jour passe sur Réglages.
- **Mon compte** affiche le profil (identifiant, rôle, instances) au-dessus du
  mot de passe.

## [0.0.56] - 2026-10-11

Pré-release.

### Ajouté

- **Utilisateurs et permissions (ACL)** : une page **Utilisateurs** crée d'autres
  comptes que le propriétaire (le compte de la configuration initiale, toujours
  administrateur). Chaque compte a un rôle — administrateur, opérateur, lecteur —
  ou des permissions choisies parmi 17 (voir les instances, les piloter, les
  supprimer, lire ou modifier les configurations, journaux, ports, pare-feu,
  mises à jour, mise à jour du panel, notifications, réglages, comptes), et peut
  être limité à certaines instances. Chaque route de l'API et chaque WebSocket
  les vérifie ; modifier, désactiver ou supprimer un compte agit aussitôt,
  sessions ouvertes comprises. Un compte qui gère les utilisateurs ne peut pas
  accorder des permissions qu'il n'a pas. L'interface masque ce que le compte ne
  peut pas faire ; configuration, ports et pare-feu passent en lecture seule
  sans la permission de modifier.
- **Connexion unique OpenID Connect** : un bouton « Se connecter avec … » sur la
  page de connexion (Keycloak, Authentik, Authelia, Zitadel, Google, Microsoft
  Entra…). Flux « authorization code » avec PKCE, `state` et `nonce`. Les comptes
  sont préparés par e-mail (liés à la première connexion dont l'e-mail est
  vérifié par le fournisseur) ou créés à la première connexion, éventuellement
  limités à certains domaines ; les rôles peuvent suivre les groupes du
  fournisseur. La connexion par mot de passe reste toujours possible.
- **Mon compte** dans Réglages : chaque compte local change son propre mot de passe.

### Modifié

- Les réglages ne s'affichent qu'aux comptes autorisés à les changer ; seul le
  propriétaire y change son identifiant et son mot de passe.

## [0.0.55] - 2026-09-30

Pré-release puis release définitive le même jour.

### Ajouté

- Notifications par webhook : une page **Notifications** envoie des alertes vers
  Discord, Slack / Mattermost, Telegram, ntfy, Gotify, une adresse JSON (modèle
  et signature HMAC-SHA256 facultatifs) ou en texte brut. Événements : instance
  arrêtée ou démarrée, adresse verrouillée, connexion, configuration ou pare-feu
  modifié, mise à jour du panel ou de frp disponible ou installée, panel
  démarré. Choix des événements par webhook, filtre d'instances, langue des
  messages, délai entre deux envois, en-têtes supplémentaires, bouton d'essai et
  historique des derniers envois. Les envois passent par une file et sont
  retentés ; adresses, secrets et en-têtes ne reviennent jamais dans le
  navigateur.
- Site : données structurées (SoftwareApplication, TechArticle, BreadcrumbList),
  fil d'Ariane, `hreflang` et `lastmod` dans le sitemap, balises Open Graph et
  Twitter complètes, `llms.txt`, polices non bloquantes, image principale
  préchargée, compression gzip par le serveur du site, et
  `website/seo_check.py`, un audit lancé avant chaque publication du site.

### Modifié

- Titres et descriptions du site ramenés aux longueurs des résultats de
  recherche ; les deux pages d'index de la documentation ont maintenant des
  titres distincts.

## [0.0.54] - 2026-09-29

Pré-release le 2026-09-29, release définitive le 2026-09-30.

### Ajouté

- Images Docker pour `arm64` et `armv7` en plus d'`amd64` (Raspberry Pi,
  serveurs ARM) : construites avec buildx et QEMU par les workflows de release
  et de dev ; la promotion recopie toute la liste multi-architecture vers
  `:latest`.

### Modifié

- Dépôt rangé : le code du panel (`app.py`, `install.sh`, `requirements.txt`,
  `templates/`) est dans `panel/`, la politique de sécurité, le guide de
  contribution et le code de conduite dans `docs/`, la démo dans
  `website/demo/`, la configuration de ruff dans `pyproject.toml`. L'archive de
  release garde le même contenu : installation et mise à jour fonctionnent comme
  avant. Les anciens scripts `build-dev.sh` et `release-docker.sh` sont retirés
  (remplacés par les workflows Forgejo).

## [0.0.53] - 2026-09-28

Pré-release puis release définitive le même jour.

frp ne se met plus à jour tout seul : vous le mettez à jour depuis le panel, quand vous le décidez.

### Retiré

- La mise à jour automatique de frp est retirée (`frp-autoupdate.py` : tâche
  cron quotidienne, vérification à chaque démarrage du panel, notification
  Discord). frp ne se met plus à jour que depuis la page Mises à jour du panel.
  `install.sh` installe frp avec `app.py --install-frp` (même téléchargement
  vérifié que le panel), et une installation existante supprime la tâche cron,
  le script et sa ligne dans le service systemd à son prochain démarrage.

### Corrigé

- En ligne de commande (`app.py --reset-password`, `--install-frp`), les
  messages ne sont en français que si la langue du système l'est, comme
  `install.sh`.

## [0.0.52] - 2026-09-28

Pré-release puis release définitive le même jour.

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

[Non publié]: https://github.com/Gogowwww/frp-manager/compare/v0.0.58...HEAD
[0.0.58]: https://github.com/Gogowwww/frp-manager/compare/v0.0.57...v0.0.58
[0.0.57]: https://github.com/Gogowwww/frp-manager/compare/v0.0.56...v0.0.57
[0.0.56]: https://github.com/Gogowwww/frp-manager/compare/v0.0.55...v0.0.56
[0.0.55]: https://github.com/Gogowwww/frp-manager/compare/v0.0.54...v0.0.55
[0.0.54]: https://github.com/Gogowwww/frp-manager/compare/v0.0.53...v0.0.54
[0.0.53]: https://github.com/Gogowwww/frp-manager/compare/v0.0.52...v0.0.53
[0.0.52]: https://github.com/Gogowwww/frp-manager/compare/v0.0.51...v0.0.52
[0.0.51]: https://github.com/Gogowwww/frp-manager/compare/v0.0.50...v0.0.51
