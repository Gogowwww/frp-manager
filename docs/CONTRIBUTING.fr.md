# Contribuer à FRP Manager

[English](CONTRIBUTING.md) · **Français**

Merci de votre intérêt ! Signalements de bugs, idées, traductions et pull
requests sont les bienvenus, en français comme en anglais. En participant, vous acceptez le
[code de conduite](CODE_OF_CONDUCT.fr.md).

## Signaler un bug ou proposer une idée

- Cherchez d'abord dans les [issues](https://github.com/Gogowwww/frp-manager/issues)
  existantes.
- Utilisez le modèle « Bug » ou « Fonctionnalité » : version du panel, mode
  d'installation (script ou Docker), version de frp, étapes pour reproduire.
- Retirez tokens, adresses IP publiques et noms de domaine de ce que vous
  collez (configurations TOML, journaux).
- **Une faille de sécurité ne se signale pas en issue publique** : voir
  [SECURITY.fr.md](SECURITY.fr.md).

## Préparer l'environnement

Le panel est un programme Go (`panel/*.go`, Go 1.26 ou plus récent) qui
intègre son interface à la compilation : modules ES natifs côté navigateur
(`panel/templates/assets/js/`), sans étape de build.

```bash
git clone https://github.com/Gogowwww/frp-manager.git && cd frp-manager
go build -o frp-manager ./panel
```

Pour lancer le panel en local sans toucher à `/etc/frp-manager` (Windows et
macOS conviennent pour l'interface ; systemd, nftables et Docker demandent Linux) :

```bash
echo '{"bind_port": 18765, "ssl_enabled": false}' > /tmp/frpm.json
FRP_MANAGER_CONFIG=/tmp/frpm.json go run ./panel
```

La documentation s'écrit dans ce dépôt : `README.md`, `docs/` et les
CHANGELOG, en anglais et en français. Elle est publiée telle quelle sur le
[site du projet](https://frp-manager.gogow.fr/docs/) et sur le
[wiki GitHub](https://github.com/Gogowwww/frp-manager/wiki) (`tools/wiki`,
`go run ./tools/wiki -out wiki` pour voir les pages produites) : on ne modifie
ni le site ni le wiki directement. Le site et la démo en ligne sont construits
à part, à partir de ce dépôt.

## Avant d'ouvrir une pull request

```bash
go vet ./... && go test ./... && gofmt -l .
ruff check .        # passerelle Python (packaging/python-bridge)
```

- Une pull request = un sujet. Décrivez le problème résolu et comment vous
  l'avez testé (installation par script, Docker, ou les deux).
- Gardez le style du code existant : commentaires en français avec les
  accents, noms explicites, pas de dépendance nouvelle sans raison.
- Ajoutez un test quand vous touchez à l'authentification, à la validation
  des entrées ou aux commandes système.
- Toute commande système se passe en liste d'arguments (`exec.Command`,
  jamais `sh -c` avec des données interpolées) ; tout nom de service ou de
  conteneur passe par `validServiceName` / `validContainerName`.
- L'interface ne construit le DOM qu'avec `h()` (`panel/templates/assets/js/ui.js`),
  jamais avec `innerHTML` sur des données. Pas d'émoji dans l'interface : les
  icônes sont les SVG embarqués de `panel/templates/partials/icons.html`.
- Mettez à jour la section « Non publié » du journal des modifications, dans
  les deux langues : [CHANGELOG.fr.md](../CHANGELOG.fr.md) et [CHANGELOG.md](../CHANGELOG.md).

## Versions et releases

Le développement se fait sur le Forgejo du projet, où tourne toute
l'intégration continue ; GitHub ne sert qu'à publier. Son seul workflow GitHub
Actions est le scan [Plumber](https://getplumber.io)
(`.github/workflows/plumber.yml`) : Plumber ne lit que `.github/workflows` et
ne publie le score du README que depuis GitHub.

- **Numéros** : jusqu'à la 0.0.60, les versions se suivent (`0.0.59`,
  `0.0.60`). Ensuite, versionnage sémantique à partir de `0.1.0` : le chiffre
  du milieu pour une nouvelle fonctionnalité (`0.2.0`), le dernier pour des
  correctifs seuls (`0.1.1`), le premier pour un changement qui casse la
  configuration, l'API ou l'installation (`1.0.0`).
- **Pré-release** : un tag `vX.Y.Z` annoté sur le main Forgejo (après la
  section datée des deux CHANGELOG) lance `release.yml` : programme, image
  `:X.Y.Z`, paquets dans la distribution apt `testing`, pré-release GitHub.
  Ses notes (français puis anglais) sont tirées des deux CHANGELOG par
  `tools/releasenotes` : la section datée commence par une phrase de résumé,
  qui devient le titre et l'introduction (`go run ./tools/releasenotes vX.Y.Z`
  pour les relire avant de taguer).
- **Release définitive** : `promote.yml` (Actions → « Promouvoir une
  release », tag `vX.Y.Z`) rend définitive la même pré-release, sans la
  reconstruire : ce qui a été essayé est exactement ce qui est publié (image
  `:latest`, distribution apt `stable`, main public avancé).
- **Entre deux releases** : `github.yml` (Actions → « Publier sur GitHub »)
  publie le main Forgejo sur le main public (documentation, traductions,
  CHANGELOG…), en avance rapide et seulement si la CI du commit est passée.
- **Wiki GitHub** : `promote.yml` et `github.yml` le régénèrent depuis le
  commit qu'ils viennent de publier ; il suit donc toujours le main public.

## Traduire le panel

Tous les textes de l'interface sont dans `panel/templates/assets/locales/` :
`fr.js` (langue de référence) et `en.js`.

1. Copiez `fr.js` en `<code>.js` (`de.js`, `es.js`…) et traduisez les valeurs,
   sans toucher aux clés ni aux `{paramètres}`.
2. Déclarez la langue dans `LOCALES` de `panel/templates/assets/js/i18n.js`.
3. Tant qu'elle est incomplète, ajoutez-la aussi à `PREVIEW_LOCALES` : elle est
   proposée dans les Réglages avec la mention *aperçu*, sans être choisie
   d'après la langue du navigateur.

Toute clé ajoutée à `fr.js` doit l'être aussi dans `en.js` (sinon l'interface
retombe silencieusement sur le français). La documentation existe en deux
langues : `X.md` en anglais, `X.fr.md` en français (README, SECURITY,
CONTRIBUTING, CODE_OF_CONDUCT, CHANGELOG et les pages de `docs/`). Modifiez
les deux.

## Listes de blocage communautaires

Une liste d'adresses à partager ? Bouton **Publier** sur une règle *Bloquer*
du pare-feu, ou pull request sur la branche
[`blocklists`](https://github.com/Gogowwww/frp-manager/tree/blocklists).

## Licence

En contribuant, vous acceptez que votre contribution soit publiée sous la
licence du projet, [Apache 2.0](../LICENSE).
