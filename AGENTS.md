# FRP Manager — consignes pour les agents

Panel web en Go pour frp (`panel/`, interface embarquée depuis
`panel/templates/`). Avant de modifier le code ou de préparer une release, lire
[docs/CONTRIBUTING.fr.md](docs/CONTRIBUTING.fr.md) : règles de code et de
sécurité, versions, traductions.

`packaging/python-bridge/app.py` est le seul Python du dépôt : une passerelle
pour les anciennes installations, pas le panel.

## Vérifications

Depuis la racine, comme la CI (`.forgejo/workflows/ci.yml`) :

```sh
gofmt -l .        # doit ne rien afficher, sinon la CI échoue
go vet ./...
go test -count=1 ./panel ./internal/... ./tools/...
bash -n panel/install.sh
```

La CI lance les tests du panel sous Linux (`docker buildx build --target test .`).

## Couplages

- Toute clé ajoutée à `panel/templates/assets/locales/fr.js` va aussi dans
  `en.js` (sinon repli silencieux sur le français).
- Tout message du serveur passe par la méthode `M(fr, en)` du type `Lang`
  (`panel/i18n.go`).
- Documentation : `X.md` (anglais) et `X.fr.md` (français), toujours les deux.
- Chaque changement poussé sur main va dans `## [Unreleased]` de
  `CHANGELOG.md` et `## [Non publié]` de `CHANGELOG.fr.md`, dans le même
  commit.
- README, `docs/`, CHANGELOG et `panel/templates/` alimentent le site
  frp-manager.gogow.fr (`demo.yml`) : un push qui les touche le republie.
- Ne pas écrire de numéro de version dans les sources : `buildVersion`
  (`panel/updates.go`) reste vide et est gravé à la compilation.

## Releases

- Ne jamais tagger, publier ou promouvoir une release sans demande explicite.
- Numéro : versionnage sémantique (CONTRIBUTING, « Versions et releases ») ;
  un bouton, une route ou un comportement visible nouveau = numéro du milieu.
- Pré-release : section datée dans les deux CHANGELOG, dont la première phrase
  devient le titre (relire avec `go run ./tools/releasenotes vX.Y.Z`), puis tag
  annoté `vX.Y.Z` poussé sur `origin` (Forgejo). La release définitive
  (`promote.yml`) est lancée par l'utilisateur.
- La branche `blocklists` est orpheline et ne vit que sur GitHub : ne jamais la
  fusionner dans main.
- Messages de commit en français (avec les accents) puis en anglais : titre
  « Ports : plus de port tiré au hasard / Ports: no more random port », corps
  en français, ligne vide, le même corps en anglais.
- Commentaires nouveaux ou modifiés : en français, puis leur traduction
  anglaise juste en dessous. Les commentaires existants restent tels quels.
