# Journal des modifications

[English](CHANGELOG.md) · **Français**

Toutes les évolutions notables de FRP Manager sont notées ici.

Le format suit [Keep a Changelog](https://keepachangelog.com/fr/1.1.0/) et,
depuis 0.1.0, les numéros suivent le
[versionnage sémantique](https://semver.org/lang/fr/) : chaque version sort
d'abord en pré-release, puis devient la release définitive avec le même
numéro. Les versions antérieures à 0.0.51 sont décrites dans les
[notes de release GitHub](https://github.com/Gogowwww/frp-manager/releases).

## [Non publié]

### Ajouté

- **Worker Cloudflare des statistiques anonymes** (`tools/stats-worker`) :
  reçoit `/api/ping` sur frp-manager.org avec les mêmes contrôles et limites que
  le serveur du site (compteurs par jour dans une base D1, adresses salées,
  hachées et gardées pour le jour même seulement), redirige l'ancienne adresse du
  site vers frp-manager.org et sert l'espace privé `/prive/`, protégé par mot de
  passe (HTTP Basic, vérifié contre le secret `PRIVE_SHA256` ; 429 après 10 mots
  de passe refusés en 10 minutes) : un tableau de bord des envois des panels
  (aujourd'hui, 7 et 30 jours, total, graphique des 30 jours, répartition par
  version, architecture et méthode d'installation) et des téléchargements des
  releases GitHub, avec les mêmes données dans `/prive/stats.json`. Une page
  statique, sans script ni ressource externe. Un workflow le déploie à la demande
  et lance ses tests.

### Modifié

- **Guide d'installation** : les commandes apt utilisent `apt.frp-manager.org`.

### Ajouté

- **Les machines passent seules sur le nouveau dépôt apt** : le paquet embarque
  la clé de `apt.frp-manager.org`, et son étape de post-installation réécrit la
  source (`frp-manager.list` ou `.sources`) et remplace la clé des machines
  encore abonnées à l'ancien dépôt. Celles qui sont déjà sur le nouveau ne sont
  pas touchées.
- **Workflow `apt-sync.yml`** : remplit à la demande les distributions
  `testing` et `stable` du nouveau dépôt à partir des releases GitHub.

### Modifié

- **Test apt depuis apt.frp-manager.org** : la CI installe le paquet `dev` du
  nouveau dépôt (Debian 12 et Ubuntu 24.04), attend la version exacte qu'elle
  vient de publier, démarre le panel puis le désinstalle.

### Ajouté

- **Dépôt apt sur apt.frp-manager.org** (en plus de celui du Forgejo, pour
  l'instant) : `packaging/deb/publish-pages.sh` construit un dépôt statique
  signé (`dev`, `testing`, `stable`) dans le dépôt `Gogowwww/frp-manager-apt`,
  servi par GitHub Pages et mis à jour par la CI, la pré-release et la
  promotion.

### Ajouté

- **Chiffres publics du dépôt** : `tools/ghstats` et un workflow quotidien
  relèvent les téléchargements de chaque release, les étoiles et les forks sur
  la branche `stats` (`stats.json`, `history.json`), à partir de la seule API
  publique de GitHub.

### Modifié

- **Nouvelle adresse du projet** : le site, la démo en ligne et la
  documentation passent sur [frp-manager.org](https://frp-manager.org).
  L'ancienne adresse continue de répondre, y compris pour les statistiques
  anonymes des panels déjà installés. Le site est aussi publié sur GitHub
  Pages (`demo.yml`, branche `gh-pages`).

## [0.4.1] - 2026-10-25

Toute l'intégration continue tourne maintenant sur GitHub Actions.

Pré-release.

### Modifié

- **Toute la CI sur GitHub Actions** : tests, image `dev`, pré-release,
  promotion, listes communautaires et site tournent maintenant depuis
  `.github/workflows/` au lieu du Forgejo (`.forgejo/workflows/` est supprimé,
  ainsi que le workflow « Publier sur GitHub »). Le dépôt apt et le dépôt privé
  du site restent sur le Forgejo : l'adresse des machines installées ne change
  pas.

## [0.4.0] - 2026-10-24

Le panel peut envoyer des statistiques anonymes, si son administrateur les active.

Pré-release.

### Ajouté

- **Statistiques anonymes, sur accord** : une case dans la configuration
  initiale et un interrupteur dans Réglages → Accès au panel, désactivés par
  défaut. Une fois activées, le panel envoie au site du projet, une fois par
  jour, sa version, son architecture et sa méthode d'installation
  (`script`, `apt` ou `docker`), et rien d'autre : ni identifiant, ni
  adresse. Réglages montre le contenu exact envoyé. Cela aide à savoir quelles
  versions sont encore utilisées.
- **Scan Plumber** : les workflows GitHub et les réglages du dépôt sont
  vérifiés par [Plumber](https://getplumber.io) à chaque pull request et à
  chaque avance du main public ; son score s'affiche en badge dans le README.

### Modifié

- **Dépôt GitHub protégé** : `main` et `blocklists` ne peuvent plus être
  supprimées ni recevoir de push forcé, et les tags `v*` ne peuvent plus être
  supprimés ni déplacés. Le signalement privé des failles, vers lequel renvoie
  SECURITY.md, est maintenant activé. CONTRIBUTING décrit ces règles.
- **`frp-manager.zip` plus léger** (environ 1 Mo de moins) : l'archive de
  chaque release ne contient plus que le programme, `install.sh`, `app.py`,
  `docker-compose.yml`, `LICENSE` et les `README.md` et `CHANGELOG.md`
  anglais. Le dossier `docs/` (captures du README) et les versions
  françaises n'y sont plus : la mise à jour n'utilise que le programme, et la
  documentation est sur le site.

### Retiré

- **Wiki GitHub** : il ne faisait que répéter la documentation du site
  (frp-manager.gogow.fr), qui a la recherche, la démo en ligne et les deux
  langues. Le générateur `tools/wiki` et ses jobs dans `github.yml` et
  `promote.yml` disparaissent, et l'étiquette `documentation` de l'image
  Docker pointe maintenant vers le site.
- **Versions françaises de SECURITY, CONTRIBUTING et CODE_OF_CONDUCT** : ces
  trois documents n'existent plus qu'en anglais. Le README français et le site
  renvoient vers la version anglaise.
- **Moins de fichiers de documentation** : la page Docker (privilèges et
  risques) devient la section « Docker et Portainer » du guide d'installation,
  le code de conduite se résume au Contributor Covenant en quelques lignes, et
  la section d'historique go-mmproxy ainsi que la note interne sur les
  réglages GitHub disparaissent.
- **`pyproject.toml`** : la configuration de ruff pour la passerelle Python,
  que la CI ne lançait pas. La passerelle reste vérifiée avec
  `python3 -m py_compile`, comme dans la CI.

## [0.3.0] - 2026-10-24

Le port public d'un nouveau port est de nouveau choisi par le serveur frps.

Pré-release.

### Retiré

- **Port public tiré par le panel** (0.1.1) : un nouveau port n'a plus de
  port public tiré au hasard, et les boutons « Aléatoire » de l'éditeur et
  « Tirer un port au hasard » de la page Ports disparaissent. Laissé vide, le
  port public est choisi par le serveur frps ; « Afficher le port attribué »
  reste là pour le voir. Un numéro saisi à la main reste possible, et les
  ports déjà enregistrés gardent le leur.

## [0.2.1] - 2026-10-20

« Afficher le port attribué » explique ce qu'il change avant d'agir.

Pré-release.

### Modifié

- **« Afficher le port attribué » prévient avant d'agir** : une fenêtre
  explique ce que le panel ajoute à `frpc.toml` (tableau de bord local sur
  `127.0.0.1`, utilisateur `frp-manager`, mot de passe tiré au hasard), que
  frpc va redémarrer et comment le retirer, avant de le faire. La politique de
  sécurité et le README le décrivent aussi.

## [0.2.0] - 2026-10-19

Le port attribué par le serveur s'affiche en un clic dans Ports.

Pré-release.

### Ajouté

- **Afficher le port attribué par le serveur** : pour un port laissé sans
  numéro, un bouton « Afficher le port attribué » de la page Ports active le
  tableau de bord local de frpc et redémarre frpc ; la ligne montre ensuite
  le port choisi par frps, avec le badge « Attribué ». Ce tableau de bord
  n'écoute que sur `127.0.0.1` (injoignable depuis le réseau), sur un port
  libre à partir de 7400, avec un mot de passe tiré au hasard, dans la config
  frpc lisible par root seul. Rien ne s'active sans ce clic. Pas pour un frpc
  en conteneur, que le panel ne joint pas en local.

## [0.1.1] - 2026-10-19

Le panel tire lui-même le port public d'un nouveau port.

Pré-release.

### Modifié

- **Port public tiré par le panel** : un nouveau port TCP ou UDP reçoit un
  port public tiré au hasard (10000–32767), avec un bouton « Aléatoire » pour
  en tirer un autre. Le port est ainsi connu et ne change plus à chaque
  démarrage de frpc, sans rien activer de plus. Le laisser vide reste
  possible (le serveur en choisit un). Pour les ports déjà ouverts sans
  numéro, un bouton « Tirer un port au hasard » leur en donne un.

## [0.1.0] - 2026-10-19

Le port attribué par le serveur s'affiche dans Ports.

Pré-release, puis release définitive le même jour. Première version en
versionnage sémantique ; elle reprend aussi la pré-release 0.0.60.

### Ajouté

- **Port attribué par le serveur** : un port ouvert sans numéro (`remotePort`
  absent ou à 0), dont frps choisit le port, affiche dans Ports l'adresse
  publique réellement attribuée, avec un badge « Attribué ». Le panel la lit
  sur le tableau de bord local de frpc (`[webServer]`) ; s'il n'est pas
  activé, un message explique comment l'activer. Un port en erreur côté frpc
  (port déjà pris…) porte un badge « Erreur » avec le message de frpc.

### Corrigé

- Un port ouvert avec `remotePort = 0` s'affichait « serveur:0 » au lieu de
  « port attribué par le serveur ».

## [0.0.60] - 2026-10-19

Un service frps ou frpc supprimé se réinstalle depuis le tableau de bord.

Pré-release, reprise dans la release 0.1.0.

### Ajouté

- **Réinstaller un service frps ou frpc supprimé** : quand le tableau de bord
  n'a plus de service frps (ou frpc), un bouton « Réinstaller frps » recrée
  `frps.service` comme à l'installation, sur `/etc/frp/frps.toml` : la
  configuration conservée à la suppression, sinon une configuration par
  défaut. Le programme est retéléchargé (et vérifié) s'il manque. Le service
  est créé arrêté, à démarrer depuis sa carte ; le journal s'affiche dans une
  fenêtre.

### Corrigé

- Le suivi d'une installation de frp pouvait s'arrêter avant la fin (sur une
  ligne « [OK] » intermédiaire) : le serveur indique maintenant lui-même quand
  la tâche est terminée et si elle a réussi.

## [0.0.59] - 2026-10-12

Connexion par passkey, et la documentation dans le wiki GitHub.

Pré-release, puis release définitive le même jour.

### Ajouté

- **Passkeys** : tout compte qui se connecte par mot de passe peut ajouter des
  passkeys (Réglages → Mon compte, mot de passe redemandé), puis se connecter
  sans mot de passe, par le bouton « Se connecter avec une passkey » ou les
  suggestions du champ identifiant. Les passkeys se renomment et se suppriment
  (les sessions ouvertes avec une passkey supprimée sont fermées) ; un
  administrateur voit combien chaque compte en a et peut les retirer (appareil
  perdu). Exige un nom de domaine avec un certificat HTTPS valide, ou
  `localhost`.
- **Wiki GitHub** : la documentation (README, installation, Docker, sécurité,
  contribution, journal des modifications), en anglais et en français, est
  aussi publiée dans le wiki du dépôt, comme sur le site ; elle est régénérée
  à chaque publication du main public.

### Modifié

- **Captures du README** refaites sur l'interface actuelle, en anglais dans le
  README anglais et en français dans le README français, avec les réglages.
- **Site du projet et démo** : leur générateur a quitté le dépôt public ; ils
  restent construits automatiquement depuis `docs/` et l'interface du panel.

## [0.0.58] - 2026-10-12

Le panel passe en Go : un seul programme, plus léger, qui s'installe aussi par apt.

Pré-release, puis release définitive le même jour.

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

Tous les réglages au même endroit.

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

Plusieurs comptes avec leurs propres droits, et connexion unique OpenID Connect.

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

[Non publié]: https://github.com/Gogowwww/frp-manager/compare/v0.4.1...HEAD
[0.4.1]: https://github.com/Gogowwww/frp-manager/compare/v0.4.0...v0.4.1
[0.4.0]: https://github.com/Gogowwww/frp-manager/compare/v0.3.0...v0.4.0
[0.3.0]: https://github.com/Gogowwww/frp-manager/compare/v0.2.1...v0.3.0
[0.2.1]: https://github.com/Gogowwww/frp-manager/compare/v0.2.0...v0.2.1
[0.2.0]: https://github.com/Gogowwww/frp-manager/compare/v0.1.1...v0.2.0
[0.1.1]: https://github.com/Gogowwww/frp-manager/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/Gogowwww/frp-manager/compare/v0.0.60...v0.1.0
[0.0.60]: https://github.com/Gogowwww/frp-manager/compare/v0.0.59...v0.0.60
[0.0.59]: https://github.com/Gogowwww/frp-manager/compare/v0.0.58...v0.0.59
[0.0.58]: https://github.com/Gogowwww/frp-manager/compare/v0.0.57...v0.0.58
[0.0.57]: https://github.com/Gogowwww/frp-manager/compare/v0.0.56...v0.0.57
[0.0.56]: https://github.com/Gogowwww/frp-manager/compare/v0.0.55...v0.0.56
[0.0.55]: https://github.com/Gogowwww/frp-manager/compare/v0.0.54...v0.0.55
[0.0.54]: https://github.com/Gogowwww/frp-manager/compare/v0.0.53...v0.0.54
[0.0.53]: https://github.com/Gogowwww/frp-manager/compare/v0.0.52...v0.0.53
[0.0.52]: https://github.com/Gogowwww/frp-manager/compare/v0.0.51...v0.0.52
[0.0.51]: https://github.com/Gogowwww/frp-manager/compare/v0.0.50...v0.0.51
