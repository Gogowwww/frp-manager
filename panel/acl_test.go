// Comptes supplémentaires et permissions (ACL).

package main

import (
	"reflect"
	"strings"
	"testing"
)

const otherPassword = "un autre mot de passe solide"

// owner : client connecté en propriétaire.
func (e *testEnv) owner() *client {
	e.setPassword(testPassword, "admin", false)
	c := e.client()
	expectCode(e.t, c.login("admin", testPassword), 200)
	return c
}

func createUser(c *client, fields J) response {
	body := J{"username": "alice", "password": otherPassword, "role": "viewer"}
	for k, v := range fields {
		body[k] = v
	}
	return c.post("/api/users", body)
}

func (e *testEnv) loginAs(username, password string) *client {
	e.t.Helper()
	c := e.client()
	r := c.login(username, password)
	expectCode(e.t, r, 200)
	return c
}

func userID(r response) string { return r.json()["user"].(map[string]any)["id"].(string) }

func set(perms ...string) permSet {
	s := permSet{}
	for _, p := range perms {
		s[p] = true
	}
	return s
}

// ── Permissions ─────────────────────────────────────────────────────────────

func TestImplicationsAndRoles(t *testing.T) {
	if !reflect.DeepEqual(expandPermissions([]string{"config.write"}), set("config.write", "config.read", "instances.view")) {
		t.Fatal("implications")
	}
	if len(expandPermissions([]string{"*"})) != len(permissions) {
		t.Fatal("*")
	}
	if userPermissions(User{"role": "operator"})["users.manage"] {
		t.Fatal("opérateur gestionnaire de comptes")
	}
	if len(userPermissions(User{"role": "custom", "permissions": []any{"nope"}})) != 0 {
		t.Fatal("permission inconnue accordée")
	}
}

func TestScopedAccountKeepsOnlyInstancePermissions(t *testing.T) {
	u := User{"role": "custom", "permissions": []any{"config.write", "firewall.manage"}, "instances": []any{"frpc"}}
	if !reflect.DeepEqual(userPermissions(u), set("config.write", "config.read", "instances.view")) {
		t.Fatalf("%v", userPermissions(u))
	}
	if userInstances(User{"role": "admin", "instances": []any{"frpc"}}) != nil {
		t.Fatal("admin limité")
	}
	if ins := userInstances(User{"role": "viewer", "instances": "bogus"}); ins == nil || len(ins) != 0 {
		t.Fatal("liste illisible : rien")
	}
}

func TestOwnerIsAdmin(t *testing.T) {
	e := setup(t)
	me := e.owner().get("/api/me").json()["user"].(map[string]any)
	if me["owner"] != true || me["role"] != "admin" || me["full_admin"] != true || !strings.Contains(strings.Join(toStrings(me["permissions"]), ","), "users.manage") {
		t.Fatalf("%v", me)
	}
}

func toStrings(v any) []string { return stringList(v) }

func TestFullAdminFlag(t *testing.T) {
	e := setup(t)
	e.fakeInstances()
	o := e.owner()
	createUser(o, J{"username": "adm", "role": "admin"})
	createUser(o, J{"username": "partiel", "role": "custom", "permissions": []any{"users.manage", "config.write"}, "instances": []any{"frpc"}})
	if e.loginAs("adm", otherPassword).get("/api/me").json()["user"].(map[string]any)["full_admin"] != true {
		t.Fatal("adm")
	}
	if e.loginAs("partiel", otherPassword).get("/api/me").json()["user"].(map[string]any)["full_admin"] != false {
		t.Fatal("partiel")
	}
}

// ── Création et connexion ───────────────────────────────────────────────────

func TestCreateAndLoginViewer(t *testing.T) {
	e := setup(t)
	e.fakeInstances()
	o := e.owner()
	r := createUser(o, nil)
	expectCode(t, r, 200)
	stored := e.savedConfig()["users"].(map[string]any)
	if len(stored) != 1 {
		t.Fatal("compte non enregistré")
	}
	for _, rec := range stored {
		m := rec.(map[string]any)
		if m["username"] != "alice" || !strings.HasPrefix(m["password_hash"].(string), "$argon2id") {
			t.Fatalf("%v", m)
		}
	}
	if _, ok := r.json()["user"].(map[string]any)["password_hash"]; ok {
		t.Fatal("hash renvoyé")
	}
	alice := e.loginAs("alice", otherPassword)
	expectCode(t, alice.get("/api/nicknames"), 200)
	expectCode(t, alice.post("/api/service/frpc/restart", nil), 403)
	expectCode(t, alice.get("/api/config/frpc"), 403)
	expectCode(t, alice.get("/api/manager/config"), 403)
	expectCode(t, alice.get("/api/users"), 403)
}

func TestUsernamesAreUniqueIgnoringCase(t *testing.T) {
	e := setup(t)
	o := e.owner()
	expectCode(t, createUser(o, J{"username": "ADMIN"}), 409)
	expectCode(t, createUser(o, nil), 200)
	expectCode(t, createUser(o, J{"username": "Alice"}), 409)
}

func TestWeakPasswordAndUnknownRoleRefused(t *testing.T) {
	e := setup(t)
	o := e.owner()
	expectCode(t, createUser(o, J{"password": "court"}), 400)
	expectCode(t, createUser(o, J{"role": "root"}), 400)
	expectCode(t, createUser(o, J{"role": "custom", "permissions": []any{"tout"}}), 400)
}

func TestWrongPasswordForOtherAccount(t *testing.T) {
	e := setup(t)
	createUser(e.owner(), nil)
	expectCode(t, e.client().login("alice", testPassword), 401)
}

// ── Limitation aux instances ────────────────────────────────────────────────

func TestInstanceScope(t *testing.T) {
	e := setup(t)
	e.fakeInstances()
	o := e.owner()
	cfgUpdate(Cfg{"nicknames": map[string]any{"frps": "serveur", "frpc": "client"}})
	createUser(o, J{"role": "custom", "permissions": []any{"config.read", "instances.control"}, "instances": []any{"frpc"}})
	alice := e.loginAs("alice", otherPassword)
	if !reflect.DeepEqual(alice.get("/api/nicknames").json()["nicknames"], map[string]any{"frpc": "client"}) {
		t.Fatal("surnoms")
	}
	if alice.get("/api/config/frpc").json()["content"] != "# frpc\n" {
		t.Fatal("config")
	}
	r := alice.get("/api/config/frps")
	if r.code != 403 || !strings.Contains(r.json()["msg"].(string), "instance") {
		t.Fatalf("%d %s", r.code, r.text())
	}
	expectCode(t, alice.post("/api/nickname/frps", J{"nickname": "x"}), 403)
	expectCode(t, alice.post("/api/nickname/frpc", J{"nickname": "x"}), 200)
}

// ── Effet immédiat des modifications ────────────────────────────────────────

func TestDisableLogsOutAtOnce(t *testing.T) {
	e := setup(t)
	o := e.owner()
	uid := userID(createUser(o, nil))
	alice := e.loginAs("alice", otherPassword)
	expectCode(t, alice.get("/api/me"), 200)
	expectCode(t, o.put("/api/users/"+uid, J{"enabled": false}), 200)
	expectCode(t, alice.get("/api/me"), 401)
	expectCode(t, e.client().login("alice", otherPassword), 401)
}

func TestRoleChangeAppliesToOpenSession(t *testing.T) {
	e := setup(t)
	e.fakeInstances()
	o := e.owner()
	uid := userID(createUser(o, nil))
	alice := e.loginAs("alice", otherPassword)
	expectCode(t, alice.get("/api/config/frpc"), 403)
	o.put("/api/users/"+uid, J{"role": "operator"})
	expectCode(t, alice.get("/api/config/frpc"), 200)
}

func TestAdminPasswordResetDropsSessions(t *testing.T) {
	e := setup(t)
	o := e.owner()
	uid := userID(createUser(o, nil))
	alice := e.loginAs("alice", otherPassword)
	expectCode(t, o.put("/api/users/"+uid, J{"password": "encore un mot de passe"}), 200)
	expectCode(t, alice.get("/api/me"), 401)
	e.loginAs("alice", "encore un mot de passe")
}

func TestDeleteAccount(t *testing.T) {
	e := setup(t)
	o := e.owner()
	uid := userID(createUser(o, nil))
	alice := e.loginAs("alice", otherPassword)
	expectCode(t, o.del("/api/users/"+uid), 200)
	expectCode(t, alice.get("/api/me"), 401)
	if len(e.savedConfig()["users"].(map[string]any)) != 0 {
		t.Fatal("compte gardé")
	}
}

// ── Garde-fous ──────────────────────────────────────────────────────────────

func TestOwnerAndSelfProtected(t *testing.T) {
	e := setup(t)
	o := e.owner()
	expectCode(t, o.put("/api/users/owner", J{"enabled": false}), 400)
	expectCode(t, o.del("/api/users/owner"), 400)
	uid := userID(createUser(o, J{"role": "admin"}))
	alice := e.loginAs("alice", otherPassword)
	expectCode(t, alice.put("/api/users/"+uid, J{"role": "viewer"}), 400)
	expectCode(t, alice.del("/api/users/"+uid), 400)
}

func TestUserManagerCannotEscalate(t *testing.T) {
	e := setup(t)
	o := e.owner()
	createUser(o, J{"role": "custom", "permissions": []any{"users.manage", "logs.view"}})
	bob := userID(createUser(o, J{"username": "bob", "role": "viewer"}))
	alice := e.loginAs("alice", otherPassword)
	expectCode(t, createUser(alice, J{"username": "carol", "role": "admin"}), 403)
	expectCode(t, createUser(alice, J{"username": "carol", "role": "custom", "permissions": []any{"settings.manage"}}), 403)
	expectCode(t, alice.put("/api/users/"+bob, J{"role": "operator"}), 403)
	expectCode(t, alice.del("/api/users/"+bob), 403)
	expectCode(t, createUser(alice, J{"username": "carol", "role": "custom", "permissions": []any{"logs.view"}}), 200)
	expectCode(t, alice.get("/api/oidc"), 403)
}

func TestSettingsManagerCannotTakeOverOwner(t *testing.T) {
	e := setup(t)
	createUser(e.owner(), J{"role": "custom", "permissions": []any{"settings.manage"}})
	alice := e.loginAs("alice", otherPassword)
	expectCode(t, alice.post("/api/manager/config", J{"new_password": "je prends la main ici"}), 403)
	expectCode(t, alice.post("/api/manager/config", J{"username": "moi"}), 403)
	expectCode(t, alice.post("/api/manager/config", J{"download_mirrors": false}), 200)
}

func TestManagerConfigHidesAccounts(t *testing.T) {
	e := setup(t)
	o := e.owner()
	createUser(o, nil)
	conf := o.get("/api/manager/config").json()["config"].(map[string]any)
	for _, k := range []string{"users", "oidc", "password_hash", "firewall", "webhooks"} {
		if _, ok := conf[k]; ok {
			t.Fatalf("%s exposé", k)
		}
	}
}

func TestOwnerRenameCannotCollide(t *testing.T) {
	e := setup(t)
	o := e.owner()
	createUser(o, nil)
	expectCode(t, o.post("/api/manager/config", J{"username": "alice"}), 409)
}

func TestManagerConfigValidation(t *testing.T) {
	e := setup(t)
	o := e.owner()
	expectCode(t, o.post("/api/manager/config", J{"bind_host": "pas-une-ip"}), 400)
	expectCode(t, o.post("/api/manager/config", J{"bind_port": 70000}), 400)
	expectCode(t, o.post("/api/manager/config", J{"session_timeout": 60}), 400)
	expectCode(t, o.post("/api/manager/config", J{"bind_host": "0.0.0.0", "bind_port": "8770", "session_timeout": 1800}), 200)
	c := e.savedConfig()
	if c["bind_host"] != "0.0.0.0" || c["bind_port"] != float64(8770) || c["session_timeout"] != float64(1800) {
		t.Fatalf("%v", c)
	}
}

// ── Mot de passe personnel ──────────────────────────────────────────────────

func TestChangeOwnPassword(t *testing.T) {
	e := setup(t)
	createUser(e.owner(), nil)
	alice := e.loginAs("alice", otherPassword)
	other := e.loginAs("alice", otherPassword)
	expectCode(t, alice.post("/api/me/password", J{"current_password": "faux", "new_password": strings.Repeat("x", 12)}), 403)
	expectCode(t, alice.post("/api/me/password", J{"current_password": otherPassword, "new_password": "nouveau mot de passe"}), 200)
	expectCode(t, alice.get("/api/me"), 200)
	expectCode(t, other.get("/api/me"), 401)
	e.loginAs("alice", "nouveau mot de passe")
}

func TestBootCarriesPermissions(t *testing.T) {
	e := setup(t)
	e.fakeInstances()
	createUser(e.owner(), J{"role": "custom", "permissions": []any{"logs.view"}, "instances": []any{"frps"}})
	alice := e.loginAs("alice", otherPassword)
	html := alice.get("/").text()
	boot := strings.SplitN(strings.SplitN(html, `id="boot"`, 2)[1], "</script>", 2)[0]
	if !strings.Contains(boot, `"permissions":["instances.view","logs.view"]`) || strings.Contains(boot, `"frpc"`) {
		t.Fatalf("boot : %s", boot)
	}
}

func TestUsersListShape(t *testing.T) {
	e := setup(t)
	e.fakeInstances()
	o := e.owner()
	createUser(o, nil)
	d := o.get("/api/users").json()
	users := d["users"].([]any)
	if len(users) != 2 || users[0].(map[string]any)["id"] != "owner" {
		t.Fatalf("%v", users)
	}
	if len(d["permissions"].([]any)) != len(permissions) || d["can_configure_oidc"] != true {
		t.Fatal("permissions")
	}
	if !reflect.DeepEqual(d["instances"], []any{"frpc", "frps"}) {
		t.Fatalf("instances %v", d["instances"])
	}
}
