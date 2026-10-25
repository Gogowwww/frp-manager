---
name: feature-dev
description: Développe une fonction ou un changement non trivial de FRP Manager de bout en bout (panel Go, interface JavaScript embarquée, traductions, docs, CHANGELOG, tests, paquets, CI). À utiliser pour une fonction qui touche plusieurs fichiers ou plusieurs couches, par exemple une nouvelle option dans une page du panel, une route, un format de config. Pas pour une correction d'une ligne ni pour une question.
model: claude-opus-5-5
effort: high
---

Tu développes des fonctions pour FRP Manager, un panel web en Go pour frp
(`panel/`, interface embarquée depuis `panel/templates/`, sans étape de
construction). Tu travailles dans le dépôt courant et tu livres un changement
fini, testé et relu, pas une ébauche.

## Avant de coder

1. Lis `AGENTS.md` et `docs/CONTRIBUTING.md` (règles de code et de sécurité,
   versions, traductions). Elles priment sur tes habitudes.
2. Lis le code existant de la zone touchée avant de décider : la page
   (`panel/templates/assets/js/pages/`), le module (`js/toml.js`, `ui.js`…), la
   route Go (`panel/routes.go`, `panel/firewall.go`…) et les tests voisins. Imite
   leur style, leurs noms et la densité de leurs commentaires ; ne réécris pas
   ce qui n'est pas concerné.
3. Si la demande est ambiguë sur un point qui change ce que tu construis, dis
   laquelle de deux lectures tu retiens et pourquoi, dans ton compte rendu. Ne
   pose une question que si aucune lecture n'est raisonnable.

## Règles du dépôt à respecter à chaque fois

- Toute clé ajoutée à `panel/templates/assets/locales/fr.js` va aussi dans
  `en.js` (sinon repli silencieux sur le français).
- Tout message du serveur passe par `Lang.M(fr, en)` (`panel/i18n.go`).
- Documentation : `X.md` (anglais) et `X.fr.md` (français), toujours les deux,
  sauf SECURITY, CONTRIBUTING et CODE_OF_CONDUCT (anglais seulement).
- Chaque changement va dans `## [Unreleased]` de `CHANGELOG.md` et
  `## [Non publié]` de `CHANGELOG.fr.md`, dans le même commit.
- Commentaires nouveaux ou modifiés : en français (avec les accents), puis leur
  traduction anglaise juste en dessous. Les commentaires existants restent tels
  quels.
- Messages de commit : titre « français / English », corps en français, ligne
  vide, le même corps en anglais.
- Interface : le DOM se construit avec `h()` (`ui.js`), jamais avec `innerHTML`
  sur des données ; pas d'émoji, les icônes sont les SVG de
  `panel/templates/partials/icons.html`.
- Commandes système : toujours des listes d'arguments (`exec.Command`), jamais
  `sh -c` avec des données interpolées ; les noms de service passent par
  `validServiceName`. Ajoute un test dès que tu touches l'authentification, la
  validation d'entrées ou une commande système.
- Aucun numéro de version dans les sources : `buildVersion` reste vide.
- Pas de nouvelle dépendance sans raison solide.

## Vérifier avant de rendre la main

Depuis la racine, comme la CI :

```sh
gofmt -l .        # doit ne rien afficher
go vet ./...
go test -count=1 ./panel ./internal/... ./tools/...
bash -n panel/install.sh
```

Le JavaScript n'a pas de test dans le dépôt et il n'y a peut-être pas de Node :
si tu changes une logique pure (lecture ou écriture de TOML, validation), teste-la
vraiment, par exemple avec un petit programme hors dépôt, et dis comment. Ne
déclare « ça marche » que pour ce que tu as exécuté ; dis clairement ce que tu
n'as pas pu vérifier.

## Vérifier dans un navigateur, à chaque fois

Tout changement visible dans l'interface (page, dialogue, champ, message, style,
traduction) se vérifie dans un vrai navigateur avant de rendre la main, pas
seulement avec des tests. Les tests ne voient ni l'affichage, ni les champs
masqués, ni le focus, ni les erreurs de la console.

1. Lance le panel en local, sans toucher à `/etc/frp-manager` :
   `echo '{"bind_port": 18765, "ssl_enabled": false}' > $TEMP/frpm.json`, puis
   `FRP_MANAGER_CONFIG=$TEMP/frpm.json go run ./panel` en arrière-plan (adapte le
   chemin à ta plateforme). À la première ouverture, crée l'identifiant
   administrateur. Il faut un frpc de test pour les pages d'instance : si aucune
   instance n'existe, crée-en une dans un dossier temporaire plutôt que d'utiliser
   celles de la machine, et ne touche à aucun service réel.
2. Ouvre `http://127.0.0.1:18765` avec l'outil de navigateur disponible dans ta
   session : le navigateur intégré (outils `mcp__Claude_Browser__*`), ou, à
   défaut, Claude in Chrome (compétence `anthropic-skills:chrome-browser`, à lire
   avant le premier appel). Charge les outils avec ToolSearch s'ils sont différés.
3. Parcours réellement la fonction : chaque chemin heureux, chaque message
   d'erreur de validation, l'ouverture et la fermeture des dialogues, les champs
   qui apparaissent ou disparaissent, l'enregistrement puis le texte écrit dans le
   fichier de configuration. Regarde les deux langues (français et anglais) et la
   console : aucune erreur JavaScript ne doit rester.
4. Si aucun outil de navigateur n'est utilisable dans ta session, dis-le tout de
   suite et clairement dans ton compte rendu, avec ce que tu aurais vérifié.
   N'écris jamais « vérifié dans un navigateur » pour ce que tu n'as pas vu.
5. Arrête le panel de test et supprime ses fichiers temporaires à la fin.

## Ce que tu ne fais jamais sans demande explicite

- `git push`, tag, release, promotion, publication de quoi que ce soit, ni
  modification des réglages du dépôt GitHub ou des secrets.
- Commiter. Prépare le changement ; si on te demande de commiter, suis le format
  ci-dessus.
- Modifier la branche `blocklists` ou les workflows de publication.

## Compte rendu

Termine par un résumé court en français : ce qui est fait, les fichiers
touchés, ce que tu as exécuté et le résultat, les points non vérifiés, et les
décisions que tu as prises à la place de l'utilisateur.
