// Tests du registre des sessions (sessions.go) et du nom affiché (/api/me).

package main

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// sessionsOf : sessions listées par le panel pour ce client.
func sessionsOf(t *testing.T, c *client) []map[string]any {
	t.Helper()
	r := c.get("/api/me/sessions")
	expectCode(t, r, 200)
	raw, _ := r.json()["sessions"].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, v := range raw {
		if rec, ok := v.(map[string]any); ok {
			out = append(out, rec)
		}
	}
	return out
}

func TestSessionsListedAndRevoked(t *testing.T) {
	e := setup(t)
	e.setPassword(testPassword, "admin", false)
	cur, other := e.client(), e.client()
	expectCode(t, cur.login("admin", testPassword), 200)
	expectCode(t, other.login("admin", testPassword), 200)

	list := sessionsOf(t, cur)
	if len(list) != 2 {
		t.Fatalf("%d session(s) listée(s), 2 attendues", len(list))
	}
	current, otherID := 0, ""
	for _, s := range list {
		if s["current"] == true {
			current++
			continue
		}
		otherID, _ = s["id"].(string)
	}
	if current != 1 {
		t.Fatalf("%d session(s) marquée(s) courante(s), 1 attendue", current)
	}
	if otherID == "" {
		t.Fatal("session de l'autre navigateur introuvable")
	}
	// Révocation d'un autre appareil : lui est déconnecté, pas nous
	expectCode(t, cur.del("/api/me/sessions/"+url.PathEscape(otherID)), 200)
	expectCode(t, other.get("/api/me"), 401)
	expectCode(t, cur.get("/api/me"), 200)
}

func TestSessionRevokesOthers(t *testing.T) {
	e := setup(t)
	e.setPassword(testPassword, "admin", false)
	cur, other := e.client(), e.client()
	expectCode(t, cur.login("admin", testPassword), 200)
	expectCode(t, other.login("admin", testPassword), 200)

	r := cur.del("/api/me/sessions")
	expectCode(t, r, 200)
	if n, _ := r.json()["count"].(float64); n != 1 {
		t.Fatalf("count = %v, 1 attendu", r.json()["count"])
	}
	expectCode(t, other.get("/api/me"), 401)
	expectCode(t, cur.get("/api/me"), 200)
	if list := sessionsOf(t, cur); len(list) != 1 {
		t.Fatalf("%d session(s) restante(s), 1 attendue", len(list))
	}
}

func TestRevokingCurrentSessionIsRefused(t *testing.T) {
	e := setup(t)
	e.setPassword(testPassword, "admin", false)
	c := e.client()
	expectCode(t, c.login("admin", testPassword), 200)
	list := sessionsOf(t, c)
	if len(list) != 1 || list[0]["current"] != true {
		t.Fatalf("session courante introuvable : %v", list)
	}
	sid, _ := list[0]["id"].(string)
	// « Se déconnecter » est le geste attendu : la route refuse cet identifiant
	expectCode(t, c.del("/api/me/sessions/"+url.PathEscape(sid)), 400)
	expectCode(t, c.get("/api/me"), 200)
}

// Cookie d'avant le registre (sans identifiant de session) : il est adopté à sa
// première requête, pour ne pas déconnecter les panels mis à jour.
func TestLegacySessionJoinsRegistry(t *testing.T) {
	e := setup(t)
	e.setPassword(testPassword, "admin", false)
	c := e.client()
	expectCode(t, c.login("admin", testPassword), 200)

	base, _ := url.Parse(e.srv.URL)
	cookies := c.http.Jar.Cookies(base)
	if len(cookies) != 1 {
		t.Fatalf("%d cookie(s) de session, 1 attendu", len(cookies))
	}
	payload, ok := unsignValue("session", cookies[0].Value)
	if !ok {
		t.Fatal("cookie de session illisible")
	}
	var stored map[string]any
	if err := json.Unmarshal(payload, &stored); err != nil {
		t.Fatal(err)
	}
	data, _ := stored["d"].(map[string]any)
	delete(data, "sid")
	raw, _ := json.Marshal(stored)
	c.http.Jar.SetCookies(base, []*http.Cookie{{Name: sessionCookie, Value: signValue("session", raw), Path: "/"}})

	expectCode(t, c.get("/api/me"), 200)
	// L'entrée d'origine (identifiant inconnu du cookie) reste jusqu'au nettoyage :
	// la liste doit au moins contenir la session adoptée, marquée courante.
	current := 0
	for _, s := range sessionsOf(t, c) {
		if s["current"] == true {
			current++
		}
	}
	if current != 1 {
		t.Fatalf("%d session(s) courante(s) après adoption, 1 attendue", current)
	}
}

func TestDisplayNameOwnAccount(t *testing.T) {
	e := setup(t)
	e.setPassword(testPassword, "admin", false)
	c := e.client()
	expectCode(t, c.login("admin", testPassword), 200)

	r := c.put("/api/me", J{"display_name": "  Gogo  "})
	expectCode(t, r, 200)
	if got, _ := r.json()["user"].(map[string]any)["display_name"].(string); got != "Gogo" {
		t.Fatalf("nom affiché = %q, « Gogo » attendu", got)
	}
	if got := e.savedConfig()["display_name"]; got != "Gogo" {
		t.Fatalf("config display_name = %v", got)
	}
	// Relu par /api/me (barre latérale)
	if got, _ := c.get("/api/me").json()["user"].(map[string]any)["display_name"].(string); got != "Gogo" {
		t.Fatalf("/api/me display_name = %q", got)
	}
	// 64 caractères au plus
	expectCode(t, c.put("/api/me", J{"display_name": strings.Repeat("a", displayNameMax+1)}), 400)
	// Vide : le nom disparaît, l'identifiant reprend la main
	expectCode(t, c.put("/api/me", J{"display_name": ""}), 200)
	if got := e.savedConfig()["display_name"]; got != "" {
		t.Fatalf("config display_name = %v après effacement", got)
	}
}

func TestDisplayNameOfAnotherAccount(t *testing.T) {
	e := setup(t)
	e.setPassword(testPassword, "admin", false)
	owner := e.client()
	expectCode(t, owner.login("admin", testPassword), 200)
	expectCode(t, owner.post("/api/users", J{"username": "gogo", "password": "un mot de passe assez long", "role": "viewer"}), 200)

	u := e.client()
	expectCode(t, u.login("gogo", "un mot de passe assez long"), 200)
	expectCode(t, u.put("/api/me", J{"display_name": "Gogo"}), 200)
	if got, _ := u.get("/api/me").json()["user"].(map[string]any)["display_name"].(string); got != "Gogo" {
		t.Fatalf("nom affiché = %q", got)
	}
	// Le propriétaire garde le sien (vide) : le registre est par compte
	if got := owner.get("/api/me").json()["user"].(map[string]any)["display_name"]; got != "" {
		t.Fatalf("nom affiché du propriétaire = %v", got)
	}
	// Le compte désactivé perd ses sessions
	users := owner.get("/api/users").json()["users"].([]any)
	uid := ""
	for _, v := range users {
		if rec, ok := v.(map[string]any); ok && rec["username"] == "gogo" {
			uid, _ = rec["id"].(string)
		}
	}
	if uid == "" {
		t.Fatal("compte gogo introuvable")
	}
	expectCode(t, owner.put("/api/users/"+url.PathEscape(uid), J{"enabled": false}), 200)
	expectCode(t, u.get("/api/me"), 401)
	if list := sessionsOf(t, owner); len(list) != 1 {
		t.Fatalf("sessions du propriétaire : %d, 1 attendue", len(list))
	}
}

func TestManagerConfigHidesSessions(t *testing.T) {
	e := setup(t)
	e.setPassword(testPassword, "admin", false)
	c := e.client()
	expectCode(t, c.login("admin", testPassword), 200)
	conf := c.get("/api/manager/config").json()["config"].(map[string]any)
	if _, leak := conf["sessions"]; leak {
		t.Fatal("les sessions ouvertes ne doivent pas sortir par /api/manager/config")
	}
}
