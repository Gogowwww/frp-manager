#!/usr/bin/env python3
"""Catalogue des listes de blocage communautaires de FRP Manager.

  python check.py                  vérifie blocklists.json (mêmes règles que le panel)
  python check.py add entree.json  ajoute ou remplace une liste (JSON d'une issue)

Le panel écarte de lui-même les entrées refusées ci-dessous ; ce script les
signale avant la fusion, pour que le catalogue reste propre.
"""
import ipaddress, json, re, sys
from datetime import date
from pathlib import Path

CATALOG = Path(__file__).with_name("blocklists.json")
LIST_ID = re.compile(r"[a-z0-9][a-z0-9-]{0,39}")
MAX_SOURCES, MAX_ASNS = 20000, 50


def check_list(lst):
    """→ liste des problèmes (vide si la liste est acceptable)."""
    if not isinstance(lst, dict):
        return ["l'entrée doit être un objet JSON { \"id\": …, \"name\": …, \"sources\": [...] }"]
    where = f"liste « {lst.get('id', '?')} »"
    errors = []
    if lst.get("mode", "block") not in ("block", "allow"):
        errors.append(f"{where} : mode « {lst.get('mode')} » inconnu (block ou allow)")
    if len(str(lst.get("author") or "")) > 40:
        errors.append(f"{where} : auteur trop long (40 caractères au plus)")
    if not LIST_ID.fullmatch(str(lst.get("id") or "")):
        errors.append(f"{where} : id invalide (minuscules, chiffres, tirets, 40 caractères au plus)")
    if not str(lst.get("name") or "").strip():
        errors.append(f"{where} : nom manquant")
    if len(str(lst.get("name") or "")) > 60 or len(str(lst.get("description") or "")) > 300:
        errors.append(f"{where} : nom (60) ou description (300) trop long")
    sources = lst.get("sources")
    if not isinstance(sources, list) or not sources:
        return errors + [f"{where} : aucune entrée"]
    if len(sources) > MAX_SOURCES:
        errors.append(f"{where} : {len(sources)} entrées, {MAX_SOURCES} au plus")
    asns, seen = 0, set()
    for item in sources:
        text = str(item).partition("#")[0].strip()
        if text in seen:
            errors.append(f"{where} : « {text} » en double")
        seen.add(text)
        m = re.fullmatch(r"(?i)AS\s*(\d{1,10})", text)
        if m:
            asns += 1
            if not 1 <= int(m.group(1)) <= 4294967295:
                errors.append(f"{where} : « {text} » n'est pas un numéro d'AS valide")
            continue
        try:
            net = ipaddress.ip_network(text, strict=False)
        except ValueError:
            errors.append(f"{where} : « {text} » n'est ni une adresse, ni un réseau, ni un AS")
            continue
        if not net.is_global:
            errors.append(f"{where} : « {text} » est privée ou réservée")
        elif net.prefixlen < (8 if net.version == 4 else 16):
            errors.append(f"{where} : « {text} » est trop large (/8 au plus, /16 en IPv6)")
    if asns > MAX_ASNS:
        errors.append(f"{where} : {asns} AS, {MAX_ASNS} au plus")
    return errors


def load():
    data = json.loads(CATALOG.read_text(encoding="utf-8"))
    if data.get("version") != 1 or not isinstance(data.get("lists"), list):
        sys.exit("blocklists.json : attendu {\"version\": 1, \"lists\": [...]}")
    return data


def save(data):
    data["lists"].sort(key=lambda l: l["id"])
    CATALOG.write_text(json.dumps(data, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")


def main():
    data = load()
    if len(sys.argv) == 3 and sys.argv[1] == "add":
        entry = json.loads(Path(sys.argv[2]).read_text(encoding="utf-8"))
        problems = check_list(entry)
        if problems:
            sys.exit("\n".join(problems))
        entry["updated"] = date.today().isoformat()
        replaced = any(l["id"] == entry["id"] for l in data["lists"])
        data["lists"] = [l for l in data["lists"] if l["id"] != entry["id"]] + [entry]
        save(data)
        print(f"{'Remplacée' if replaced else 'Ajoutée'} : {entry['id']}")
        return
    if len(sys.argv) != 1:
        sys.exit(__doc__)
    problems = [p for lst in data["lists"] for p in check_list(lst)]
    ids = [l.get("id") for l in data["lists"]]
    problems += [f"id « {i} » utilisé plusieurs fois" for i in sorted({i for i in ids if ids.count(i) > 1})]
    if problems:
        sys.exit("\n".join(problems))
    print(f"{len(data['lists'])} liste(s), tout est en ordre")


if __name__ == "__main__":
    main()
