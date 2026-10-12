// Outils des tests : configuration dans un dossier temporaire (jamais celle de
// /etc/frp-manager), serveur de test, client avec cookies et jeton CSRF.

package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

const testPassword = "correct horse battery staple"

type testEnv struct {
	t   *testing.T
	dir string
	srv *httptest.Server
}

// setup : config neuve sans mot de passe, aucun verrouillage hérité, aucune
// commande réelle (systemctl, nft…), aucune instance.
func setup(t *testing.T) *testEnv {
	t.Helper()
	dir := t.TempDir()
	saved := struct {
		conf, confDir, certDir, cert, key, state, asn, lists string
		search                                               []string
		exec                                                 func([]string, time.Duration, *string) (bool, string, string)
		disc                                                 func() (map[string]*Instance, map[string]J)
		stat                                                 func(map[string]J) map[string]J
	}{mgrConfFile, mgrConfDir, sslCertDir, sslCertFile, sslKeyFile, frpStateFile, fwASNFile, fwListsFile,
		configSearchPaths, execCmd, discoverFn, readStatusesFn}
	t.Cleanup(func() {
		mgrConfFile, mgrConfDir, sslCertDir, sslCertFile, sslKeyFile = saved.conf, saved.confDir, saved.certDir, saved.cert, saved.key
		frpStateFile, fwASNFile, fwListsFile, configSearchPaths = saved.state, saved.asn, saved.lists, saved.search
		execCmd, discoverFn, readStatusesFn = saved.exec, saved.disc, saved.stat
	})
	mgrConfFile = filepath.Join(dir, "frp-manager.json")
	mgrConfDir = dir
	sslCertDir = filepath.Join(dir, "ssl")
	sslCertFile = filepath.Join(sslCertDir, "cert.pem")
	sslKeyFile = filepath.Join(sslCertDir, "key.pem")
	frpStateFile = filepath.Join(dir, "state.json")
	fwASNFile = filepath.Join(dir, "asn-cache.json")
	fwListsFile = filepath.Join(dir, "blocklists-cache.json")
	configSearchPaths = []string{filepath.Join(dir, "etc-frp")}
	execCmd = func(args []string, _ time.Duration, _ *string) (bool, string, string) {
		return false, "", args[0] + ": command not found"
	}
	setDiscovery(nil)
	setCfg(defaultManagerConfig())
	loginMu.Lock()
	loginFailures = map[string]*loginFailure{}
	loginMu.Unlock()
	listsMu.Lock()
	listsVal, listsFailAt, listsFailMsg = nil, 0, ""
	listsMu.Unlock()
	asnMu.Lock()
	asnVal = nil
	asnMu.Unlock()
	t.Setenv("LANG", "fr_FR.UTF-8") // messages hors requête : français, quel que soit le poste
	os.Unsetenv("LC_ALL")
	os.Unsetenv("LC_MESSAGES")
	initServer()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &testEnv{t: t, dir: dir, srv: srv}
}

// setDiscovery : instances factices (nil : aucune), toutes arrêtées.
func setDiscovery(insts map[string]*Instance) {
	discoverFn = func() (map[string]*Instance, map[string]J) {
		cache := map[string]J{}
		out := map[string]*Instance{}
		for iid, inst := range insts {
			out[iid] = inst
			cache[iid] = J{"id": iid, "type": inst.Type, "source": inst.Source, "service": inst.Service,
				"binary_found": true, "config_path": nullable(inst.Config), "config_exists": inst.Config != "" && fileExists(inst.Config)}
		}
		return out, cache
	}
	readStatusesFn = func(found map[string]J) map[string]J {
		out := map[string]J{}
		for iid := range found {
			out[iid] = stoppedStatus()
		}
		return out
	}
	invalidateCache(true)
}

// fakeInstances : frps et frpc (systemd), chacun avec sa config « # <id> ».
func (e *testEnv) fakeInstances() map[string]*Instance {
	insts := map[string]*Instance{}
	for _, iid := range []string{"frps", "frpc"} {
		conf := filepath.Join(e.dir, iid+".toml")
		os.WriteFile(conf, []byte("# "+iid+"\n"), 0o644)
		insts[iid] = &Instance{ID: iid, Type: iid, Source: "systemd", Service: iid, Config: conf}
	}
	setDiscovery(insts)
	return insts
}

func (e *testEnv) setPassword(password, username string, legacySHA256 bool) string {
	stored := hashPassword(password)
	if legacySHA256 {
		stored = sha256Hex(password)
	}
	if _, err := cfgUpdate(Cfg{"username": username, "password_hash": stored}); err != nil {
		e.t.Fatal(err)
	}
	return stored
}

func (e *testEnv) savedConfig() map[string]any {
	raw, err := os.ReadFile(mgrConfFile)
	if err != nil {
		e.t.Fatal(err)
	}
	var m map[string]any
	json.Unmarshal(raw, &m)
	return m
}

// ── Client ──────────────────────────────────────────────────────────────────

type client struct {
	e    *testEnv
	http *http.Client
	csrf string
	lang string
}

type response struct {
	code   int
	header http.Header
	body   []byte
}

func (r response) json() map[string]any {
	var m map[string]any
	json.Unmarshal(r.body, &m)
	return m
}

func (r response) text() string { return string(r.body) }

func (e *testEnv) client() *client {
	jar, _ := cookiejar.New(nil)
	return &client{e: e, http: &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}}
}

func (c *client) do(method, path string, body any, headers map[string]string) response {
	c.e.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, c.e.srv.URL+path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.lang != "" {
		req.Header.Set("X-Lang", c.lang)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		c.e.t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return response{resp.StatusCode, resp.Header, data}
}

func (c *client) get(path string) response { return c.do("GET", path, nil, nil) }

var csrfMetaRe = regexp.MustCompile(`<meta name="csrf-token" content="([^"]+)">`)

// token : jeton CSRF de la session (celui que la page pose dans <meta>).
func (c *client) token() string {
	if c.csrf != "" {
		return c.csrf
	}
	path := "/login"
	for i := 0; i < 4; i++ {
		r := c.get(path)
		if loc := r.header.Get("Location"); r.code == http.StatusFound && loc != "" {
			path = loc
			continue
		}
		if m := csrfMetaRe.FindStringSubmatch(r.text()); m != nil {
			c.csrf = m[1]
			return c.csrf
		}
		break
	}
	c.e.t.Fatal("jeton CSRF introuvable")
	return ""
}

func (c *client) send(method, path string, body any) response {
	if body == nil {
		body = map[string]any{}
	}
	return c.do(method, path, body, map[string]string{"X-CSRF-Token": c.token()})
}

func (c *client) post(path string, body any) response { return c.send("POST", path, body) }
func (c *client) put(path string, body any) response  { return c.send("PUT", path, body) }
func (c *client) del(path string) response            { return c.send("DELETE", path, nil) }

func (c *client) login(username, password string) response {
	return c.post("/api/login", J{"username": username, "password": password})
}

func expectCode(t *testing.T, r response, want int) {
	t.Helper()
	if r.code != want {
		t.Fatalf("code %d, attendu %d : %s", r.code, want, strings.TrimSpace(r.text()))
	}
}
