// Connexion unique OpenID Connect : fournisseur simulé (découverte, /token, userinfo).

package main

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeIdP struct {
	srv      *httptest.Server
	issuer   string
	mu       sync.Mutex
	claims   map[string]any
	userinfo map[string]any
	tokens   []url.Values
	basic    [][2]string
}

func jwtOf(claims map[string]any) string {
	enc := func(v any) string { b, _ := json.Marshal(v); return base64.RawURLEncoding.EncodeToString(b) }
	return enc(map[string]any{"alg": "RS256"}) + "." + enc(claims) + ".c2lnbmF0dXJl"
}

func newIdP(t *testing.T) *fakeIdP {
	idp := &fakeIdP{}
	mux := http.NewServeMux()
	mux.HandleFunc("/realms/test/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"issuer": idp.issuer, "authorization_endpoint": idp.issuer + "/auth",
			"token_endpoint": idp.issuer + "/token", "userinfo_endpoint": idp.issuer + "/userinfo",
			"token_endpoint_auth_methods_supported": []string{"client_secret_basic"}})
	})
	mux.HandleFunc("/realms/test/token", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		u, p, _ := r.BasicAuth()
		idp.mu.Lock()
		idp.tokens = append(idp.tokens, r.PostForm)
		idp.basic = append(idp.basic, [2]string{u, p})
		claims := idp.claims
		idp.mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{"access_token": "at", "id_token": jwtOf(claims)})
	})
	mux.HandleFunc("/realms/test/userinfo", func(w http.ResponseWriter, r *http.Request) {
		idp.mu.Lock()
		ui := idp.userinfo
		idp.mu.Unlock()
		if ui == nil || r.Header.Get("Authorization") != "Bearer at" {
			http.NotFound(w, r)
			return
		}
		json.NewEncoder(w).Encode(ui)
	})
	idp.srv = httptest.NewServer(mux)
	t.Cleanup(idp.srv.Close)
	idp.issuer = idp.srv.URL + "/realms/test"
	return idp
}

// oidcSetup : propriétaire « admin », connexion unique activée vers le fournisseur simulé.
func oidcSetup(t *testing.T, extra J) (*testEnv, *fakeIdP) {
	e := setup(t)
	e.setPassword(testPassword, "admin", false)
	idp := newIdP(t)
	oidcMetaMu.Lock()
	oidcMeta = map[string]oidcMetaEntry{}
	oidcMetaMu.Unlock()
	oc := map[string]any{"enabled": true, "issuer": idp.issuer, "client_id": "frpm", "client_secret": "s3cret"}
	for k, v := range extra {
		oc[k] = v
	}
	cfgUpdate(Cfg{"oidc": oc})
	return e, idp
}

func oidcStart(t *testing.T, c *client, idp *fakeIdP) url.Values {
	t.Helper()
	r := c.get("/auth/oidc/login")
	expectCode(t, r, 302)
	loc := r.header.Get("Location")
	if !strings.HasPrefix(loc, idp.issuer+"/auth?") {
		t.Fatalf("redirection : %s", loc)
	}
	u, _ := url.Parse(loc)
	return u.Query()
}

func oidcFinish(c *client, idp *fakeIdP, q url.Values, claims J) response {
	base := map[string]any{"iss": idp.issuer, "aud": "frpm", "sub": "u-1", "exp": float64(time.Now().Unix() + 300), "nonce": q.Get("nonce")}
	for k, v := range claims {
		base[k] = v
	}
	idp.mu.Lock()
	idp.claims = base
	idp.mu.Unlock()
	return c.get("/auth/oidc/callback?code=abc&state=" + url.QueryEscape(q.Get("state")))
}

func errorOf(r response) string {
	u, err := url.Parse(r.header.Get("Location"))
	if err != nil {
		return ""
	}
	return u.Query().Get("error")
}

func meOf(c *client) map[string]any {
	m, _ := c.get("/api/me").json()["user"].(map[string]any)
	return m
}

func TestOIDCAuthorizationRequest(t *testing.T) {
	e, idp := oidcSetup(t, nil)
	q := oidcStart(t, e.client(), idp)
	if q.Get("response_type") != "code" || q.Get("client_id") != "frpm" || q.Get("code_challenge_method") != "S256" ||
		strings.Fields(q.Get("scope"))[0] != "openid" || !strings.HasSuffix(q.Get("redirect_uri"), "/auth/oidc/callback") {
		t.Fatalf("%v", q)
	}
}

func TestOIDCUnknownAccountRefusedWithoutAutoCreate(t *testing.T) {
	e, idp := oidcSetup(t, nil)
	c := e.client()
	if errorOf(oidcFinish(c, idp, oidcStart(t, c, idp), nil)) != "no_account" {
		t.Fatal("compte créé")
	}
	expectCode(t, c.get("/api/me"), 401)
}

func TestOIDCAutoCreateAndSignIn(t *testing.T) {
	e, idp := oidcSetup(t, J{"auto_create": true})
	c := e.client()
	r := oidcFinish(c, idp, oidcStart(t, c, idp), J{"preferred_username": "admin", "email": "a@corp.io", "email_verified": true})
	if r.code != 200 || !strings.Contains(r.text(), "refresh") {
		t.Fatalf("%d %s", r.code, r.text())
	}
	me := meOf(c)
	if me["source"] != "oidc" || me["role"] != "viewer" || me["username"] != "admin-2" {
		t.Fatalf("%v", me) // « admin » est pris par le propriétaire
	}
	if idp.basic[0] != [2]string{"frpm", "s3cret"} || idp.tokens[0].Get("code_verifier") == "" {
		t.Fatal("échange du code")
	}
	c2 := e.client()
	expectCode(t, oidcFinish(c2, idp, oidcStart(t, c2, idp), nil), 200)
	if len(e.savedConfig()["users"].(map[string]any)) != 1 {
		t.Fatal("deuxième compte créé")
	}
}

func TestOIDCPreprovisionedAccountLinkedByVerifiedEmail(t *testing.T) {
	e, idp := oidcSetup(t, nil)
	owner := e.client()
	owner.login("admin", testPassword)
	expectCode(t, owner.post("/api/users", J{"username": "bob", "source": "oidc", "email": "bob@corp.io", "role": "operator"}), 200)
	c := e.client()
	if errorOf(oidcFinish(c, idp, oidcStart(t, c, idp), J{"email": "bob@corp.io", "email_verified": false})) != "no_account" {
		t.Fatal("e-mail non vérifié lié")
	}
	expectCode(t, oidcFinish(c, idp, oidcStart(t, c, idp), J{"email": "bob@corp.io", "email_verified": true}), 200)
	me := meOf(c)
	if me["username"] != "bob" || me["role"] != "operator" {
		t.Fatalf("%v", me)
	}
	for _, rec := range e.savedConfig()["users"].(map[string]any) {
		if !reflect.DeepEqual(rec.(map[string]any)["oidc"], map[string]any{"iss": idp.issuer, "sub": "u-1"}) {
			t.Fatalf("%v", rec)
		}
	}
}

func TestOIDCAllowedDomains(t *testing.T) {
	e, idp := oidcSetup(t, J{"auto_create": true, "allowed_domains": []any{"corp.io"}})
	c := e.client()
	if errorOf(oidcFinish(c, idp, oidcStart(t, c, idp), J{"email": "x@evil.io", "email_verified": true})) != "domain" {
		t.Fatal("domaine refusé accepté")
	}
	if errorOf(oidcFinish(c, idp, oidcStart(t, c, idp), J{"email": "x@corp.io"})) != "domain" {
		t.Fatal("e-mail non vérifié accepté")
	}
	expectCode(t, oidcFinish(c, idp, oidcStart(t, c, idp), J{"email": "x@corp.io", "email_verified": true}), 200)
}

func TestOIDCGroupRoles(t *testing.T) {
	e, idp := oidcSetup(t, J{"auto_create": true, "group_roles": map[string]any{"frp-admins": "admin", "frp-ops": "operator"}})
	c := e.client()
	oidcFinish(c, idp, oidcStart(t, c, idp), J{"groups": []any{"staff", "frp-ops"}})
	if meOf(c)["role"] != "operator" {
		t.Fatal("opérateur")
	}
	c2 := e.client()
	oidcFinish(c2, idp, oidcStart(t, c2, idp), J{"groups": []any{"frp-admins"}})
	if meOf(c2)["role"] != "admin" {
		t.Fatal("admin")
	}
	c3 := e.client()
	oidcFinish(c3, idp, oidcStart(t, c3, idp), J{"groups": []any{}})
	if meOf(c3)["role"] != "viewer" {
		t.Fatal("lecteur")
	}
}

func TestOIDCGroupsFromUserinfo(t *testing.T) {
	e, idp := oidcSetup(t, J{"auto_create": true, "group_roles": map[string]any{"frp-ops": "operator"}})
	idp.userinfo = map[string]any{"sub": "u-1", "groups": []any{"frp-ops"}}
	c := e.client()
	oidcFinish(c, idp, oidcStart(t, c, idp), nil)
	if meOf(c)["role"] != "operator" {
		t.Fatal("groupes de userinfo ignorés")
	}
}

func TestOIDCUserinfoForOtherSubjectIgnored(t *testing.T) {
	e, idp := oidcSetup(t, J{"auto_create": true, "group_roles": map[string]any{"frp-ops": "operator"}})
	idp.userinfo = map[string]any{"sub": "quelqu-un-d-autre", "groups": []any{"frp-ops"}}
	c := e.client()
	oidcFinish(c, idp, oidcStart(t, c, idp), nil)
	if meOf(c)["role"] != "viewer" {
		t.Fatal("userinfo d'un autre sujet pris en compte")
	}
}

func TestOIDCBadIDToken(t *testing.T) {
	for _, claims := range []J{{"nonce": "rejoue"}, {"aud": "autre-client"}, {"iss": "https://faux.example.com"}, {"exp": float64(1)}, {"sub": ""}} {
		e, idp := oidcSetup(t, J{"auto_create": true})
		c := e.client()
		if code := errorOf(oidcFinish(c, idp, oidcStart(t, c, idp), claims)); code != "claims" {
			t.Fatalf("%v : %q", claims, code)
		}
		expectCode(t, c.get("/api/me"), 401)
	}
}

func TestOIDCStateMismatchAndMissingCookie(t *testing.T) {
	e, idp := oidcSetup(t, nil)
	c := e.client()
	q := oidcStart(t, c, idp)
	if errorOf(c.get("/auth/oidc/callback?code=abc&state=autre")) != "state" {
		t.Fatal("state")
	}
	if errorOf(e.client().get("/auth/oidc/callback?code=abc&state="+q.Get("state"))) != "state" {
		t.Fatal("cookie absent")
	}
}

func TestOIDCProviderError(t *testing.T) {
	e, idp := oidcSetup(t, nil)
	c := e.client()
	oidcStart(t, c, idp)
	if errorOf(c.get("/auth/oidc/callback?error=access_denied&state=x")) != "denied" {
		t.Fatal("denied")
	}
}

func TestOIDCDisabledAccount(t *testing.T) {
	e, idp := oidcSetup(t, J{"auto_create": true})
	c := e.client()
	oidcFinish(c, idp, oidcStart(t, c, idp), nil)
	usersUpdate(func(users map[string]any) {
		for _, u := range users {
			u.(map[string]any)["enabled"] = false
		}
	})
	expectCode(t, c.get("/api/me"), 401)
	c2 := e.client()
	if errorOf(oidcFinish(c2, idp, oidcStart(t, c2, idp), nil)) != "disabled_user" {
		t.Fatal("compte désactivé accepté")
	}
}

func TestOIDCDisabledSSO(t *testing.T) {
	e, _ := oidcSetup(t, J{"enabled": false})
	c := e.client()
	if errorOf(c.get("/auth/oidc/login")) != "disabled" {
		t.Fatal("disabled")
	}
	if strings.Contains(c.get("/login").text(), "/auth/oidc/login") {
		t.Fatal("bouton affiché")
	}
}

func TestOIDCLoginPageShowsButtonAndError(t *testing.T) {
	e, _ := oidcSetup(t, J{"label": "Keycloak"})
	c := e.client()
	page := c.get("/login?error=no_account").text()
	if !strings.Contains(page, "/auth/oidc/login") || !strings.Contains(page, "Keycloak") || !strings.Contains(page, `data-oidc-error="no_account"`) {
		t.Fatal("page de connexion")
	}
	page = c.get("/login?error=%3Cscript%3E").text()
	if strings.Contains(strings.SplitN(page, "data-oidc-error", 2)[1][:30], "<script>") {
		t.Fatal("injection")
	}
}

func TestOIDCSettingsValidationAndSecret(t *testing.T) {
	e, idp := oidcSetup(t, nil)
	c := e.client()
	c.login("admin", testPassword)
	got := c.get("/api/oidc").json()
	oc := got["oidc"].(map[string]any)
	if oc["has_secret"] != true || oc["client_secret"] != nil || !strings.HasSuffix(got["redirect_uri"].(string), "/auth/oidc/callback") {
		t.Fatalf("%v", got)
	}
	expectCode(t, c.post("/api/oidc", J{"default_role": "admin"}), 400)
	expectCode(t, c.post("/api/oidc", J{"issuer": "http://idp.example.com"}), 400)
	expectCode(t, c.post("/api/oidc", J{"enabled": true, "client_id": ""}), 400)
	expectCode(t, c.post("/api/oidc", J{"group_roles": map[string]any{"g": "root"}}), 400)
	expectCode(t, c.post("/api/oidc", J{"label": "Entreprise", "allowed_domains": "corp.io, Example.org"}), 200)
	stored := e.savedConfig()["oidc"].(map[string]any)
	if stored["client_secret"] != "s3cret" || !reflect.DeepEqual(stored["allowed_domains"], []any{"corp.io", "example.org"}) {
		t.Fatalf("%v", stored)
	}
	if c.post("/api/oidc/test", J{"issuer": idp.issuer}).json()["ok"] != true {
		t.Fatal("test du fournisseur")
	}
}
