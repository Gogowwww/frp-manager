package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBodyLimit(t *testing.T) {
	// Une requête ordinaire garde la petite limite, l'envoi d'archive la grande.
	// An ordinary request keeps the small limit, the archive upload the large one.
	if got := maxBodyFor("api_login"); got != maxJSONSize {
		t.Fatalf("api_login: limite %d, attendu %d", got, maxJSONSize)
	}
	if got := maxBodyFor("api_update_upload"); got != maxBodySize {
		t.Fatalf("api_update_upload: limite %d, attendu %d", got, maxBodySize)
	}

	// 2 Mio dans un champ : tronqué à 1 Mio, le JSON est illisible, corps vide.
	// 2 MiB in one field: truncated at 1 MiB, the JSON is unreadable, empty body.
	big := `{"username":"` + strings.Repeat("a", 2<<20) + `"}`
	c := &Ctx{R: httptest.NewRequest("POST", "/api/login", strings.NewReader(big)), bodyLimit: maxJSONSize}
	if body := c.Body(); len(body) != 0 {
		t.Fatalf("corps de %d champ(s) décodé malgré la limite", len(body))
	}

	// Le même corps passe sous la limite de l'envoi d'archive.
	// The same body fits under the archive upload limit.
	c = &Ctx{R: httptest.NewRequest("POST", "/api/update/upload", strings.NewReader(big)),
		bodyLimit: maxBodyFor("api_update_upload")}
	if body := c.Body(); body["username"] == nil {
		t.Fatal("corps de l'envoi d'archive non décodé")
	}
}

func TestBodyTooLarge(t *testing.T) {
	// Une requête dont le corps annoncé dépasse la limite de la route est
	// refusée, au lieu d'être tronquée puis ignorée avec un « Enregistré ».
	// A request whose announced body exceeds the route limit is refused,
	// instead of being truncated then ignored with a "Saved".
	e := setup(t)
	r := e.client().post("/api/login", J{"username": strings.Repeat("a", int(maxJSONSize))})
	if r.code != http.StatusRequestEntityTooLarge {
		t.Fatalf("code %d, attendu 413", r.code)
	}
	// Juste sous la limite : la route répond normalement (mauvais mot de passe).
	// Just under the limit: the route answers normally (wrong password).
	r = e.client().post("/api/login", J{"username": "admin", "password": "nope"})
	if r.code == http.StatusRequestEntityTooLarge {
		t.Fatal("petit corps refusé")
	}
}
