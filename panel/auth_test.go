// Connexion, configuration initiale, CSRF, limitation des tentatives, en-têtes.

package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"os"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// ── Hachage ─────────────────────────────────────────────────────────────────

func TestHashIsSaltedAndSlow(t *testing.T) {
	a, b := hashPassword(testPassword), hashPassword(testPassword)
	if a == b {
		t.Fatal("sel non aléatoire")
	}
	if !strings.HasPrefix(a, "$argon2id$v=19$m=65536,t=3,p=4$") {
		t.Fatalf("format inattendu : %s", a)
	}
	if ok, rehash := verifyPassword(testPassword, a); !ok || rehash {
		t.Fatal("vérification")
	}
	if ok, _ := verifyPassword("mauvais mot de passe", a); ok {
		t.Fatal("mauvais mot de passe accepté")
	}
}

// Hashs écrits par les versions Python du panel (argon2-cffi, et scrypt sans
// argon2-cffi) : lus tels quels, scrypt rehaché en argon2id.
func TestPythonHashesAccepted(t *testing.T) {
	argon := "$argon2id$v=19$m=65536,t=3,p=4$XZdaDs2iNP4Vx9MUHngsAQ$KRHM2vWw2kMTC66VROY3FJ7aZbHYHfkaPAOxfKoSGZ8"
	if ok, rehash := verifyPassword(testPassword, argon); !ok || rehash {
		t.Fatalf("argon2-cffi : (%v, %v)", ok, rehash)
	}
	scryptHash := "scrypt$32768$8$1$gvze8zGrR94i5glYa4X4Gw==$APp5Eju+DCveUIPNG/xC7TwP0INBfz6VOGrqMZK3hhI="
	if ok, rehash := verifyPassword(testPassword, scryptHash); !ok || !rehash {
		t.Fatalf("scrypt : (%v, %v)", ok, rehash)
	}
	for _, stored := range []string{argon, scryptHash} {
		if ok, _ := verifyPassword(testPassword+"x", stored); ok {
			t.Fatal("mauvais mot de passe accepté")
		}
	}
}

func TestLegacySHA256AcceptedAndFlagged(t *testing.T) {
	legacy := sha256Hex(testPassword)
	if ok, rehash := verifyPassword(testPassword, legacy); !ok || !rehash {
		t.Fatal("ancien hash")
	}
	if ok, _ := verifyPassword("autre", legacy); ok {
		t.Fatal("mauvais mot de passe accepté")
	}
}

func TestGarbageHashNeverMatches(t *testing.T) {
	for _, stored := range []string{"", "abc", "scrypt$1$2", "$argon2id$v=19$cassé", "$argon2id$v=19$m=1,t=1,p=1$$"} {
		if ok, _ := verifyPassword(testPassword, stored); ok {
			t.Fatalf("%q accepté", stored)
		}
	}
}

// ── Migration des anciens hashs ─────────────────────────────────────────────

func TestLoginMigratesLegacyHash(t *testing.T) {
	e := setup(t)
	legacy := e.setPassword(testPassword, "admin", true)
	c := e.client()
	expectCode(t, c.login("admin", testPassword), 200)
	stored := e.savedConfig()["password_hash"].(string)
	if stored == legacy || !strings.HasPrefix(stored, "$argon2id$") {
		t.Fatalf("hash non migré : %s", stored)
	}
	expectCode(t, e.client().login("admin", testPassword), 200)
}

func TestFailedLoginDoesNotTouchLegacyHash(t *testing.T) {
	e := setup(t)
	legacy := e.setPassword(testPassword, "admin", true)
	expectCode(t, e.client().login("admin", "mauvais"), 401)
	if e.savedConfig()["password_hash"] != legacy {
		t.Fatal("hash modifié")
	}
}

// ── Connexion ───────────────────────────────────────────────────────────────

func TestLoginSuccessOpensSession(t *testing.T) {
	e := setup(t)
	e.setPassword(testPassword, "admin", false)
	c := e.client()
	expectCode(t, c.get("/api/status"), 401)
	expectCode(t, c.login("admin", testPassword), 200)
	expectCode(t, c.get("/api/me"), 200)
}

func TestLoginRejectsWrongUsernameOrPassword(t *testing.T) {
	e := setup(t)
	e.setPassword(testPassword, "admin", false)
	c := e.client()
	expectCode(t, c.login("root", testPassword), 401)
	expectCode(t, c.login("admin", "nope"), 401)
}

func TestPasswordChangeLogsOutOtherSessions(t *testing.T) {
	e := setup(t)
	e.setPassword(testPassword, "admin", false)
	c, other := e.client(), e.client()
	c.login("admin", testPassword)
	other.login("admin", testPassword)
	expectCode(t, c.post("/api/manager/config", J{"new_password": "un tout nouveau mot de passe"}), 200)
	expectCode(t, c.get("/api/manager/config"), 200)     // session courante gardée
	expectCode(t, other.get("/api/manager/config"), 401) // l'autre est déconnectée
}

func TestCSRFTokenSurvivesPasswordChange(t *testing.T) {
	e := setup(t)
	e.setPassword(testPassword, "admin", false)
	c := e.client()
	c.login("admin", testPassword)
	before := c.token()
	expectCode(t, c.post("/api/manager/config", J{"new_password": "un tout nouveau mot de passe"}), 200)
	r := c.do("POST", "/api/nickname/frpc", J{"nickname": "Maison"}, map[string]string{"X-CSRF-Token": before})
	expectCode(t, r, 200)
}

func TestNewPasswordMustBeLongEnough(t *testing.T) {
	e := setup(t)
	e.setPassword(testPassword, "admin", false)
	c := e.client()
	c.login("admin", testPassword)
	expectCode(t, c.post("/api/manager/config", J{"new_password": "court"}), 400)
}

func TestLogout(t *testing.T) {
	e := setup(t)
	e.setPassword(testPassword, "admin", false)
	c := e.client()
	c.login("admin", testPassword)
	expectCode(t, c.post("/api/logout", nil), 200)
	expectCode(t, c.get("/api/me"), 401)
}

// ── Configuration initiale ──────────────────────────────────────────────────

func TestSetupRequiredWithoutPassword(t *testing.T) {
	e := setup(t)
	c := e.client()
	r := c.get("/")
	if r.code != 302 || !strings.HasSuffix(r.header.Get("Location"), "/setup") {
		t.Fatalf("/ : %d %s", r.code, r.header.Get("Location"))
	}
	expectCode(t, c.get("/login"), 302)
	expectCode(t, c.get("/api/status"), 401)
	expectCode(t, c.login("admin", ""), 401)
	expectCode(t, c.get("/setup"), 200)
	expectCode(t, c.get("/inconnu"), 302)
}

func TestSetupEnforcesMinLength(t *testing.T) {
	e := setup(t)
	expectCode(t, e.client().post("/api/setup", J{"username": "admin", "password": "trop court"}), 400)
	if !needsSetup() {
		t.Fatal("configuré")
	}
}

func TestSetupCreatesAccountOnce(t *testing.T) {
	e := setup(t)
	c := e.client()
	expectCode(t, c.post("/api/setup", J{"username": "moi", "password": testPassword}), 200)
	if needsSetup() || e.savedConfig()["username"] != "moi" {
		t.Fatal("compte non créé")
	}
	expectCode(t, c.get("/api/manager/config"), 200) // connecté d'office
	// Une seconde tentative (autre navigateur) est refusée et la page disparaît
	other := e.client()
	expectCode(t, other.post("/api/setup", J{"username": "pirate", "password": strings.Repeat("x", 20)}), 409)
	expectCode(t, other.get("/setup"), 302)
	if e.savedConfig()["username"] != "moi" {
		t.Fatal("compte remplacé")
	}
}

// ── CSRF ────────────────────────────────────────────────────────────────────

func TestPostWithoutCSRFTokenIsRejected(t *testing.T) {
	e := setup(t)
	e.setPassword(testPassword, "admin", false)
	c := e.client()
	c.token()
	body := J{"username": "admin", "password": testPassword}
	expectCode(t, c.do("POST", "/api/login", body, nil), 403)
	expectCode(t, c.do("POST", "/api/login", body, map[string]string{"X-CSRF-Token": "faux"}), 403)
}

func TestPagesEmbedCSRFToken(t *testing.T) {
	e := setup(t)
	e.setPassword(testPassword, "admin", false)
	c := e.client()
	tok := c.token()
	if !strings.Contains(c.get("/login").text(), `content="`+tok+`"`) {
		t.Fatal("jeton absent de la page")
	}
}

// ── Limitation des tentatives ───────────────────────────────────────────────

func TestLoginRateLimit(t *testing.T) {
	e := setup(t)
	e.setPassword(testPassword, "admin", false)
	c := e.client()
	for i := 1; i <= loginFreeAttempts; i++ {
		want := 401
		if i == loginFreeAttempts {
			want = 429
		}
		expectCode(t, c.login("admin", "mauvais"), want)
	}
	// Même le bon mot de passe est refusé pendant le verrouillage
	r := c.login("admin", testPassword)
	expectCode(t, r, 429)
	if n, _ := strconv.Atoi(r.header.Get("Retry-After")); n <= 0 {
		t.Fatal("Retry-After absent")
	}
}

func TestLockoutGrows(t *testing.T) {
	setup(t)
	saved := nowFunc
	defer func() { nowFunc = saved }()
	nowFunc = func() float64 { return 1_000_000 }
	ip := "203.0.113.9"
	for i := 0; i < loginFreeAttempts; i++ {
		loginFailed(ip)
	}
	first := loginRetryAfter(ip)
	loginFailed(ip)
	if loginRetryAfter(ip) <= first {
		t.Fatal("verrouillage constant")
	}
	for i := 0; i < 40; i++ {
		loginFailed(ip)
	}
	if loginRetryAfter(ip) > int(loginLockMax) {
		t.Fatal("verrouillage au-delà du maximum")
	}
}

// ── En-têtes et cookies ─────────────────────────────────────────────────────

func TestSecurityHeaders(t *testing.T) {
	e := setup(t)
	e.setPassword(testPassword, "admin", false)
	r := e.client().get("/login")
	csp := r.header.Get("Content-Security-Policy")
	if !strings.Contains(csp, "frame-ancestors 'none'") || !strings.Contains(csp, "'nonce-") || strings.Contains(csp, "unsafe-eval") {
		t.Fatalf("CSP : %s", csp)
	}
	for k, v := range map[string]string{"X-Content-Type-Options": "nosniff", "X-Frame-Options": "DENY", "Referrer-Policy": "no-referrer"} {
		if r.header.Get(k) != v {
			t.Fatalf("%s : %q", k, r.header.Get(k))
		}
	}
	if r := e.client().get("/api/me"); r.header.Get("Cache-Control") != "no-store" {
		t.Fatal("réponse de l'API mise en cache")
	}
}

func TestSessionCookieFlags(t *testing.T) {
	e := setup(t)
	e.setPassword(testPassword, "admin", false)
	saved := sessionCookieSecure
	defer func() { sessionCookieSecure = saved }()
	sessionCookieSecure = true
	ck := e.client().get("/login").header.Get("Set-Cookie")
	for _, want := range []string{"HttpOnly", "SameSite=Strict", "Secure"} {
		if !strings.Contains(ck, want) {
			t.Fatalf("cookie sans %s : %s", want, ck)
		}
	}
}

func TestTamperedSessionCookieRejected(t *testing.T) {
	e := setup(t)
	e.setPassword(testPassword, "admin", false)
	payload := `{"d":{"authenticated":true,"uid":"owner","pwfp":"x"},"e":9999999999,"p":true}`
	forged := base64.RawURLEncoding.EncodeToString([]byte(payload)) + ".AAAA"
	c := e.client()
	r := c.do("GET", "/api/me", nil, map[string]string{"Cookie": "session=" + forged})
	expectCode(t, r, 401)
}

func TestManagerConfigNeverExposesSecrets(t *testing.T) {
	e := setup(t)
	e.setPassword(testPassword, "admin", false)
	c := e.client()
	c.login("admin", testPassword)
	conf := c.get("/api/manager/config").json()["config"].(map[string]any)
	if _, ok := conf["password_hash"]; ok {
		t.Fatal("password_hash exposé")
	}
	if _, ok := conf["secret_key"]; ok {
		t.Fatal("secret_key exposée")
	}
}

func TestConfigFileIsPrivate(t *testing.T) {
	e := setup(t)
	e.setPassword(testPassword, "admin", false)
	if runtime.GOOS == "windows" {
		t.Skip("droits POSIX")
	}
	st, _ := os.Stat(mgrConfFile)
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("droits %o", st.Mode().Perm())
	}
}

// ── Adresse d'écoute : nouvelle installation et rétrocompatibilité ──────────

func TestNewInstallListensOnLocalhost(t *testing.T) {
	e := setup(t)
	t.Setenv("FRP_MANAGER_PORT", "9123")
	ensureManagerConfig()
	c := e.savedConfig()
	if c["bind_host"] != "127.0.0.1" || c["bind_port"] != float64(9123) || c["secret_key"] == "" {
		t.Fatalf("config : %v", c)
	}
}

func TestLegacyInstallKeepsListeningEverywhere(t *testing.T) {
	e := setup(t)
	os.MkdirAll(sslCertDir, 0o700)
	os.WriteFile(sslCertFile, []byte("x"), 0o600) // certificat d'un panel antérieur, pas de config
	ensureManagerConfig()
	if e.savedConfig()["bind_host"] != "0.0.0.0" {
		t.Fatal("adresse changée")
	}
}

func TestExistingConfigIsKept(t *testing.T) {
	e := setup(t)
	os.WriteFile(mgrConfFile, []byte(`{"bind_port": 8766, "password_hash": "abc", "cle_inconnue": [1, 2]}`), 0o600)
	setCfg(loadManagerConfig())
	ensureManagerConfig()
	c := e.savedConfig()
	if c["bind_host"] != "0.0.0.0" || c["bind_port"] != float64(8766) || c["password_hash"] != "abc" || c["cle_inconnue"] == nil {
		t.Fatalf("config : %v", c)
	}
}
