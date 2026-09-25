#!/usr/bin/env python3
"""Ajoute au catalogue les listes publiées par issue, sans relecture humaine.

Lancé toutes les 10 minutes par le workflow Forgejo blocklists.yml du dépôt
principal, dans un clone de cette branche. Pour chaque issue ouverte dont le
titre commence par « Nouvelle liste » ou « Mise à jour de la liste » :

  - l'entrée JSON de l'issue est vérifiée (mêmes règles que check.py) ;
  - une liste existante ne peut être remplacée que par le compte GitHub qui
    l'a publiée (champ « github ») ;
  - acceptée : ajoutée au catalogue, poussée, puis l'issue est fermée avec un
    commentaire ; refusée : l'issue est fermée avec la raison.

Si le push échoue, les issues acceptées restent ouvertes et sont retraitées
au passage suivant.

Variables : GH_TOKEN (commenter et fermer les issues), GH_REPO, GIT_AUTH
(en-tête d'authentification pour git push).
"""
import json, os, re, subprocess, sys, urllib.request
from datetime import date

from check import check_list, load, save

REPO = os.environ.get("GH_REPO", "Gogowwww/frp-manager")
TOKEN = os.environ["GH_TOKEN"]
TITLE = re.compile(r"^\s*(Nouvelle liste|Mise à jour de la liste)", re.I)
BLOCK = re.compile(r"```(?:json)?\s*\n(.*?)\n```", re.S)
KEYS = ("id", "name", "mode", "description", "author", "updated", "github", "sources")


def github(method, path, body=None):
    req = urllib.request.Request(
        f"https://api.github.com/repos/{REPO}{path}", method=method,
        data=json.dumps(body).encode() if body is not None else None,
        headers={"Authorization": f"token {TOKEN}", "Accept": "application/vnd.github+json",
                 "User-Agent": "frp-manager-blocklists"})
    with urllib.request.urlopen(req, timeout=30) as r:
        return json.loads(r.read() or "null")


def close(issue, text, accepted):
    github("POST", f"/issues/{issue['number']}/comments", {"body": text})
    github("PATCH", f"/issues/{issue['number']}",
           {"state": "closed", "state_reason": "completed" if accepted else "not_planned"})


def git(*args):
    subprocess.run(["git", *args], check=True)


def entry_of(issue):
    """→ (entrée, None) ou (None, raison du refus)."""
    m = BLOCK.search(issue.get("body") or "")
    if not m:
        return None, "aucun bloc ```json``` trouvé dans l'issue."
    try:
        entry = json.loads(m.group(1))
    except ValueError as e:
        return None, f"le JSON est illisible ({e}). Avez-vous bien collé l'entrée copiée depuis le panel ?"
    problems = check_list(entry)
    if problems:
        return None, "\n".join(f"- {p}" for p in problems)
    entry = {k: entry[k] for k in KEYS if k in entry}
    entry["mode"] = entry.get("mode", "block")
    entry["github"] = issue["user"]["login"]
    entry["updated"] = date.today().isoformat()
    return entry, None


def main():
    issues = [i for i in github("GET", "/issues?state=open&per_page=100")
              if "pull_request" not in i and TITLE.match(i.get("title") or "")]
    if not issues:
        return
    data = load()
    accepted = []
    for issue in sorted(issues, key=lambda i: i["number"]):
        entry, why = entry_of(issue)
        if entry:
            old = next((l for l in data["lists"] if l["id"] == entry["id"]), None)
            if old and old.get("github", "").lower() != entry["github"].lower():
                entry, why = None, (f"la liste « {entry['id']} » appartient à @{old.get('github') or '?'}. "
                                    "Seul son auteur peut la mettre à jour : choisissez un autre nom.")
        if not entry:
            print(f"#{issue['number']} refusée : {why}")
            close(issue, f"❌ Liste refusée :\n\n{why}\n\nCorrigez puis publiez-la à nouveau depuis le panel.", False)
            continue
        data["lists"] = [l for l in data["lists"] if l["id"] != entry["id"]] + [entry]
        save(data)
        git("add", "blocklists.json")
        if subprocess.run(["git", "diff", "--cached", "--quiet"]).returncode:
            git("commit", "-q", "-m", f"{'Mise à jour' if old else 'Ajout'} : {entry['name']} (#{issue['number']})")
        accepted.append((issue, entry, bool(old)))
        print(f"#{issue['number']} acceptée : {entry['id']}")
    if not accepted:
        return
    git("-c", f"http.https://github.com/.extraheader={os.environ['GIT_AUTH']}", "push", "-q", "origin", "HEAD:blocklists")
    for issue, entry, updated in accepted:
        kind = "d'autorisation" if entry["mode"] == "allow" else "de blocage"
        close(issue, f"✅ Liste {kind} « {entry['name']} » {'mise à jour' if updated else 'ajoutée au catalogue'} "
                     f"({len(entry['sources'])} entrées). Les panels la voient sous quelques minutes : "
                     "Pare-feu → Listes communautaires → Actualiser.", True)


if __name__ == "__main__":
    try:
        main()
    except Exception as e:
        sys.exit(f"Échec : {e}")
