package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestCheckList(t *testing.T) {
	ok := `{"id": "ok", "name": "Ok", "sources": ["203.0.114.4 # note", "8.8.8.0/24", "2a00::/16", "AS13335"]}`
	if p := checkList([]byte(ok)); len(p) != 0 {
		t.Fatal(p)
	}
	bad := `{"id": "Bad_ID", "name": "", "mode": "x", "sources": ["10.0.0.1", "1.0.0.0/7", "AS0", "foo", "8.8.8.8", "8.8.8.8 # dup", "2001:db8::1", "2a00::/15"]}`
	got := strings.Join(checkList([]byte(bad)), "\n")
	for _, want := range []string{
		"mode « x » inconnu", "id invalide", "nom manquant", "« 10.0.0.1 » est privée ou réservée",
		"« 1.0.0.0/7 » est trop large", "« AS0 » n'est pas un numéro d'AS valide",
		"« foo » n'est ni une adresse", "« 8.8.8.8 » en double", "« 2001:db8::1 » est privée",
		"« 2a00::/15 » est trop large",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("absent : %s\n%s", want, got)
		}
	}
	if p := checkList([]byte(`[1]`)); len(p) != 1 || !strings.Contains(p[0], "objet JSON") {
		t.Fatal(p)
	}
	if p := checkList([]byte(`{"id": "vide", "name": "Vide", "sources": []}`)); len(p) != 1 || !strings.Contains(p[0], "aucune entrée") {
		t.Fatal(p)
	}
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v : %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// Robot complet : faux GitHub, vrai dépôt git (dépôt nu comme « origin »).
func TestSync(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git absent")
	}
	tmp := t.TempDir()
	remote, work := filepath.Join(tmp, "remote.git"), filepath.Join(tmp, "catalog")
	git(t, tmp, "init", "-q", "--bare", remote)
	git(t, tmp, "init", "-q", "-b", "blocklists", work)
	git(t, work, "config", "user.name", "test")
	git(t, work, "config", "user.email", "test@example.invalid")
	initial := `{
  "version": 1,
  "lists": [
    {
      "id": "a-alice",
      "name": "Liste d'Alice",
      "mode": "block",
      "github": "Alice",
      "sources": ["8.8.8.8"]
    }
  ]
}
`
	os.WriteFile(filepath.Join(work, "blocklists.json"), []byte(initial), 0o644)
	git(t, work, "add", ".")
	git(t, work, "commit", "-q", "-m", "init")
	git(t, work, "remote", "add", "origin", remote)
	git(t, work, "push", "-q", "origin", "HEAD:blocklists")

	body := func(entry string) string { return "Merci\n\n```json\n" + entry + "\n```\n" }
	issues := []map[string]any{
		{"number": 3, "title": "Nouvelle liste : Bob", "user": map[string]string{"login": "bob"},
			"body": body(`{"id": "b-bob", "name": "Liste de Bob", "sources": ["1.1.1.1"], "extra": 1}`)},
		{"number": 4, "title": "Mise à jour de la liste a-alice", "user": map[string]string{"login": "mallory"},
			"body": body(`{"id": "a-alice", "name": "Volée", "sources": ["1.1.1.1"]}`)},
		{"number": 5, "title": "mise à jour de la liste a-alice", "user": map[string]string{"login": "alice"},
			"body": body(`{"id": "a-alice", "name": "Liste d'Alice v2", "mode": "allow", "sources": ["9.9.9.9"]}`)},
		{"number": 6, "title": "Nouvelle liste", "user": map[string]string{"login": "eve"}, "body": "pas de bloc"},
		{"number": 7, "title": "Nouvelle liste", "user": map[string]string{"login": "eve"}, "body": body(`{"id": `)},
		{"number": 8, "title": "Bug", "user": map[string]string{"login": "x"}, "body": body(`{}`)},
		{"number": 9, "title": "Nouvelle liste", "user": map[string]string{"login": "x"}, "pull_request": map[string]any{}, "body": ""},
	}
	var mu sync.Mutex
	var calls []string
	comments := map[string]string{}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Header.Get("Authorization") != "token secret" {
			http.Error(w, "auth", 401)
			return
		}
		b, _ := io.ReadAll(r.Body)
		calls = append(calls, r.Method+" "+r.URL.Path)
		switch {
		case r.Method == "GET":
			json.NewEncoder(w).Encode(issues)
		case strings.HasSuffix(r.URL.Path, "/comments"):
			var m map[string]string
			json.Unmarshal(b, &m)
			comments[strings.Split(r.URL.Path, "/")[5]] = m["body"]
			w.Write([]byte(`{}`))
		default:
			var m map[string]string
			json.Unmarshal(b, &m)
			comments[strings.Split(r.URL.Path, "/")[5]] += " [" + m["state"] + "/" + m["state_reason"] + "]"
			w.Write([]byte(`{}`))
		}
	}))
	defer api.Close()
	t.Setenv("GITHUB_API", api.URL)
	t.Setenv("GH_TOKEN", "secret")
	t.Setenv("GH_REPO", "o/r")
	t.Setenv("GIT_AUTH", "x")

	if err := cmdSync(work); err != nil {
		t.Fatal(err)
	}
	expect := map[string]string{
		"3": "✅ Liste de blocage « Liste de Bob » ajoutée au catalogue (1 entrées)",
		"4": "appartient à @Alice",
		"5": "✅ Liste d'autorisation « Liste d'Alice v2 » mise à jour",
		"6": "aucun bloc ```json```",
		"7": "le JSON est illisible",
	}
	for n, want := range expect {
		if !strings.Contains(comments[n], want) {
			t.Errorf("issue %s : %q sans %q", n, comments[n], want)
		}
	}
	if !strings.HasSuffix(comments["3"], "[closed/completed]") || !strings.HasSuffix(comments["4"], "[closed/not_planned]") {
		t.Errorf("fermetures : %v", comments)
	}
	if _, ok := comments["8"]; ok {
		t.Error("issue sans le bon titre traitée")
	}
	if _, ok := comments["9"]; ok {
		t.Error("pull request traitée")
	}
	// Poussé sur origin : deux commits, catalogue trié, clé « extra » écartée
	if log := git(t, work, "log", "--format=%s", "origin/blocklists"); !strings.Contains(log, "Ajout : Liste de Bob (#3)") ||
		!strings.Contains(log, "Mise à jour : Liste d'Alice v2 (#5)") {
		t.Errorf("historique : %s", log)
	}
	c, err := loadCatalog(work)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.lists) != 2 || c.lists[0].text("id") != "a-alice" || c.lists[1].text("github") != "bob" {
		t.Fatalf("%+v", c.lists)
	}
	if _, ok := c.lists[1].raw("extra"); ok {
		t.Error("clé inconnue gardée")
	}
	if c.lists[1].text("mode") != "block" || c.lists[1].text("updated") == "" {
		t.Error("mode ou date absents")
	}
	if cmdCheck(work) != nil {
		t.Error("catalogue invalide après le passage du robot")
	}
}
