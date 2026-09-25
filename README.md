# 🌍 Listes de blocage communautaires — FRP Manager

Cette branche ne contient que le **catalogue des listes de blocage** du pare-feu de
[FRP Manager](https://github.com/Gogowwww/frp-manager). Chaque panel le lit ici :

```
https://raw.githubusercontent.com/Gogowwww/frp-manager/blocklists/blocklists.json
```

## S'abonner

Dans le panel : **Pare-feu → Listes communautaires → S'abonner**, puis **Appliquer**.
Une règle *Bloquer* est créée sur tous les ports frp ; ses adresses suivent la liste
(catalogue relu toutes les 6 heures, préfixes des AS chaque jour). Si GitHub est
injoignable, la dernière version connue continue de filtrer.

## Publier une liste

- **Depuis le panel** : bouton **Publier** (flèche vers le haut) sur une règle *Bloquer*.
  Le panel écarte ce qui n'est pas publiable et ouvre une issue pré-remplie ; relisez-la et envoyez-la.
- **Ou directement** : une Pull Request sur cette branche qui modifie `blocklists.json`.

Rien n'entre au catalogue sans relecture.

## Format

```json
{
  "version": 1,
  "lists": [
    {
      "id": "scanners-de-ports",
      "name": "Scanners de ports",
      "description": "Ce que bloque la liste, et d'où viennent les adresses.",
      "author": "pseudo",
      "updated": "2026-09-25",
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
| `name` / `description` | 60 / 300 caractères au plus |
| `sources` | IP, réseau (IPv4 ou IPv6) ou numéro d'AS ; note après un `#` |

Refusé (et ignoré par les panels s'il passait quand même) : adresses privées ou
réservées, réseaux plus larges qu'un `/8` (`/16` en IPv6), plus de 50 AS ou de
20 000 entrées par liste.

## Relecture (mainteneurs)

```bash
# Entrée copiée depuis l'issue dans entree.json :
python check.py add entree.json   # ajoute ou remplace la liste, date du jour
python check.py                   # vérifie tout le catalogue (aussi lancé par la CI)
```

Avant d'accepter : les adresses sont-elles vraiment abusives, la source est-elle
indiquée, un réseau ou un AS entier est-il justifié ? Bloquer un grand hébergeur
coupe aussi ses clients légitimes.
