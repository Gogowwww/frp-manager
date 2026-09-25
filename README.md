# 🌍 Listes communautaires du pare-feu — FRP Manager

Cette branche ne contient que le **catalogue des listes communautaires** du pare-feu de
[FRP Manager](https://github.com/Gogowwww/frp-manager). Chaque panel le lit ici :

```
https://raw.githubusercontent.com/Gogowwww/frp-manager/blocklists/blocklists.json
```

Une liste est soit **à bloquer** (`"mode": "block"`, par défaut), soit **à autoriser seulement**
(`"mode": "allow"`) : c'est le type de la règle créée en s'y abonnant.

## S'abonner

Dans le panel : **Pare-feu → Listes communautaires → S'abonner**, puis **Appliquer**.
Une règle est créée sur tous les ports frp ; ses adresses suivent la liste
(catalogue relu toutes les 6 heures, préfixes des AS chaque jour). Si GitHub est
injoignable, la dernière version connue continue de filtrer.

## Publier une liste

Dans le panel : **Pare-feu → Listes communautaires → Publier une liste** (ou le bouton
flèche d'une règle). Le panel ouvre une issue pré-remplie ; envoyez-la.

**C'est automatique** : toutes les 10 minutes, un robot lit les issues dont le titre
commence par « Nouvelle liste » ou « Mise à jour de la liste », vérifie l'entrée
(règles ci-dessous), l'ajoute au catalogue et ferme l'issue avec un commentaire.
Refusée, l'issue est fermée avec la raison.

Une liste ne peut être mise à jour que par le **compte GitHub qui l'a publiée**
(champ `github`, rempli par le robot). Même nom qu'une liste de quelqu'un d'autre :
refusée, choisissez-en un autre.

## Format

```json
{
  "version": 1,
  "lists": [
    {
      "id": "scanners-de-ports",
      "name": "Scanners de ports",
      "mode": "block",
      "description": "Ce que contient la liste, et d'où viennent les adresses.",
      "author": "pseudo",
      "updated": "2026-09-25",
      "github": "compte-github",
      "sources": [
        "203.0.113.4  # note facultative",
        "198.51.100.0/24",
        "2001:db8::/32",
        "AS64500  # tout un opérateur"
      ]
    }
  ]
}
```

| Champ | Règle |
|---|---|
| `id` | minuscules, chiffres et tirets, 40 caractères au plus, unique |
| `name` / `description` / `author` | 60 / 300 / 40 caractères au plus |
| `mode` | `block` ou `allow` |
| `sources` | IP, réseau (IPv4 ou IPv6) ou numéro d'AS ; note après un `#` |

Refusé (et ignoré par les panels s'il passait quand même) : adresses privées ou
réservées, réseaux plus larges qu'un `/8` (`/16` en IPv6), plus de 50 AS ou de
20 000 entrées par liste.

## Fichiers

- `blocklists.json` — le catalogue
- `check.py` — vérifie le catalogue (`python check.py`) ou ajoute une entrée à la main (`python check.py add entree.json`)
- `sync_issues.py` — le robot, lancé par le workflow Forgejo `blocklists.yml` du dépôt principal
