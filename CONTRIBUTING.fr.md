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

Le panel est une application Flask sans étape de build : Python côté serveur
(`app.py`), modules ES natifs côté navigateur (`templates/assets/js/`).

```bash
git clone https://github.com/Gogowwww/frp-manager.git && cd frp-manager
python3 -m venv .venv && . .venv/bin/activate
pip install -r requirements.txt pytest ruff
```

Pour lancer le panel en local sans toucher à `/etc/frp-manager` :

```bash
echo '{"bind_port": 18765, "ssl_enabled": false}' > /tmp/frpm.json
FRP_MANAGER_CONFIG=/tmp/frpm.json python3 app.py
```

La [démo](demo/build.py) (`python3 demo/build.py`) produit une version
statique de l'interface avec des données fictives, pratique pour travailler
sur le front sans frp installé.

Le site du projet ([frp-manager.gogow.fr](https://frp-manager.gogow.fr)) se
construit avec `python3 website/build.py` : page d'accueil, documentation
rendue depuis `docs/`, `SECURITY.md`, `CHANGELOG.md`… et démo sous `/demo/`.
Servez le résultat avec `DEMO_SITE=dist-site python3 demo/serve.py`. Après un
changement visible de l'interface, `python3 website/shots.py` refait les
captures du site à partir de la démo (Pillow et Chrome ou Edge requis).

## Avant d'ouvrir une pull request

```bash
ruff check .
python3 -m pytest
```

- Une pull request = un sujet. Décrivez le problème résolu et comment vous
  l'avez testé (installation par script, Docker, ou les deux).
- Gardez le style du code existant : commentaires en français avec les
  accents, noms explicites, pas de dépendance nouvelle sans raison.
- Ajoutez un test quand vous touchez à l'authentification, à la validation
  des entrées ou aux commandes système.
- Toute commande système se passe en liste d'arguments (jamais `shell=True`
  ni `sh -c` avec des données interpolées) ; tout nom de service ou de
  conteneur passe par `valid_service_name` / `valid_container_name`.
- L'interface ne construit le DOM qu'avec `h()` (`templates/assets/js/ui.js`),
  jamais avec `innerHTML` sur des données. Pas d'émoji dans l'interface : les
  icônes sont les SVG embarqués de `templates/partials/icons.html`.
- Mettez à jour la section « Non publié » du journal des modifications, dans
  les deux langues : [CHANGELOG.fr.md](CHANGELOG.fr.md) et [CHANGELOG.md](CHANGELOG.md).

## Traduire le panel

Tous les textes de l'interface sont dans `templates/assets/locales/` :
`fr.js` (langue de référence) et `en.js`.

1. Copiez `fr.js` en `<code>.js` (`de.js`, `es.js`…) et traduisez les valeurs,
   sans toucher aux clés ni aux `{paramètres}`.
2. Déclarez la langue dans `LOCALES` de `templates/assets/js/i18n.js`.
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
licence du projet, [Apache 2.0](LICENSE).
