// Serveur HTTP : contexte de requête, routes, session, jeton CSRF, en-têtes de
// sécurité, compression, fichiers du site et pages HTML.

package main

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"io/fs"
	"mime"
	"net"
	"net/http"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"
)

//go:embed templates
var webFS embed.FS

// ── Contexte de requête ─────────────────────────────────────────────────────

type Ctx struct {
	W     *respWriter
	R     *http.Request
	Lang  Lang
	Route string

	sess       *session
	user       User
	userLoaded bool
	nonce      string
	body       map[string]any
}

func (c *Ctx) M(fr, en string) string { return c.Lang.M(fr, en) }

// Body : objet JSON de la requête ({} s'il est absent ou illisible), nombres
// gardés tels qu'écrits (json.Number).
func (c *Ctx) Body() map[string]any {
	if c.body != nil {
		return c.body
	}
	c.body = map[string]any{}
	if c.R.Body == nil {
		return c.body
	}
	dec := json.NewDecoder(io.LimitReader(c.R.Body, maxBodySize))
	dec.UseNumber()
	var v any
	if dec.Decode(&v) == nil {
		if m, ok := v.(map[string]any); ok {
			c.body = m
		}
	}
	return c.body
}

func (c *Ctx) Query(k string) string { return c.R.URL.Query().Get(k) }
func (c *Ctx) Param(k string) string { return c.R.PathValue(k) }

// RemoteIP : adresse du client (sans port), comme request.remote_addr.
func (c *Ctx) RemoteIP() string {
	host, _, err := net.SplitHostPort(c.R.RemoteAddr)
	if err != nil {
		if c.R.RemoteAddr == "" {
			return "?"
		}
		return c.R.RemoteAddr
	}
	return host
}

func (c *Ctx) acceptsGzip() bool {
	return strings.Contains(c.R.Header.Get("Accept-Encoding"), "gzip")
}

// send : réponse complète ; JSON, HTML et texte compressés au-delà de 1 Ko.
func (c *Ctx) send(status int, contentType string, body []byte) {
	h := c.W.Header()
	h.Set("Content-Type", contentType)
	mt, _, _ := mime.ParseMediaType(contentType)
	if status == http.StatusOK && len(body) >= 1024 && c.acceptsGzip() && h.Get("Content-Encoding") == "" &&
		(mt == "application/json" || mt == "text/html" || mt == "text/plain") && !strings.HasPrefix(c.R.URL.Path, "/ws/") {
		var buf bytes.Buffer
		zw, _ := gzip.NewWriterLevel(&buf, 5)
		zw.Write(body)
		zw.Close()
		body = buf.Bytes()
		h.Set("Content-Encoding", "gzip")
		h.Set("Vary", "Accept-Encoding")
	}
	c.W.WriteHeader(status)
	if c.R.Method != http.MethodHead {
		c.W.Write(body)
	}
}

// JSON : réponse JSON (status facultatif, 200 par défaut).
func (c *Ctx) JSON(v any, status ...int) {
	st := http.StatusOK
	if len(status) > 0 {
		st = status[0]
	}
	data, err := json.Marshal(v)
	if err != nil {
		data = []byte(`{"ok":false,"msg":"JSON"}`)
		st = http.StatusInternalServerError
	}
	data = append(data, '\n')
	c.send(st, "application/json", data)
}

// Fail : {"ok": false, "msg": msg} avec ce code HTTP.
func (c *Ctx) Fail(status int, msg string) { c.JSON(J{"ok": false, "msg": msg}, status) }

func (c *Ctx) Text(status int, s string) { c.send(status, "text/plain; charset=utf-8", []byte(s)) }
func (c *Ctx) HTML(status int, s string) { c.send(status, "text/html; charset=utf-8", []byte(s)) }

func (c *Ctx) Redirect(url string) {
	c.W.Header().Set("Location", url)
	c.W.WriteHeader(http.StatusFound)
}

// externalURL : adresse absolue d'un chemin du panel (url_for(..., _external=True)).
func (c *Ctx) externalURL(p string) string {
	scheme := "http"
	if c.R.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + c.R.Host + p
}

type J = map[string]any

const maxBodySize = 128 << 20 // archive frp envoyée à la main (~15 Mo) : large marge

// ── Réponse : en-têtes de sécurité et cookie de session posés au dernier moment

type respWriter struct {
	http.ResponseWriter
	c           *Ctx
	wroteHeader bool
	status      int
}

func (w *respWriter) WriteHeader(code int) {
	if w.wroteHeader {
		return
	}
	w.wroteHeader = true
	w.status = code
	w.c.beforeHeaders()
	w.ResponseWriter.WriteHeader(code)
}

func (w *respWriter) Write(b []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(b)
}

func (w *respWriter) Flush() {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *respWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if !w.wroteHeader {
		w.wroteHeader = true
		w.c.beforeHeaders()
	}
	if hj, ok := w.ResponseWriter.(http.Hijacker); ok {
		return hj.Hijack()
	}
	return nil, nil, errors.New("hijack non pris en charge")
}

func (w *respWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (c *Ctx) beforeHeaders() {
	h := c.W.Header()
	setDefault := func(k, v string) {
		if h.Get(k) == "" {
			h.Set(k, v)
		}
	}
	scriptSrc := "'self'"
	if c.nonce != "" {
		scriptSrc = "'self' 'nonce-" + c.nonce + "'"
	}
	host := c.R.Host
	setDefault("Content-Security-Policy", strings.Join([]string{
		"default-src 'self'",
		"script-src " + scriptSrc,
		"style-src 'self' 'unsafe-inline' https://fonts.googleapis.com",
		"font-src 'self' https://fonts.gstatic.com",
		"img-src 'self' data:",
		"connect-src 'self' wss://" + host + " ws://" + host,
		"object-src 'none'",
		"base-uri 'none'",
		"form-action 'self'",
		"frame-ancestors 'none'",
	}, "; "))
	setDefault("X-Content-Type-Options", "nosniff")
	setDefault("X-Frame-Options", "DENY")
	setDefault("Referrer-Policy", "no-referrer")
	if strings.HasPrefix(c.R.URL.Path, "/api/") {
		// Réponses de l'API (configs avec leurs tokens…) jamais gardées en cache
		setDefault("Cache-Control", "no-store")
	}
	if c.sess != nil {
		c.sess.save(c)
	}
}

// ── Routes ──────────────────────────────────────────────────────────────────

var mux = http.NewServeMux()

// Routes servies tant qu'aucun mot de passe n'existe
var setupRoutes = map[string]bool{"setup_page": true, "api_setup": true, "versioned_asset": true, "static": true}

// route enregistre un gestionnaire : pattern « GET /api/x/{iid} », name pour les
// contrôles (configuration initiale).
func route(pattern, name string, h func(*Ctx)) {
	mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
		serve(w, r, name, h)
	})
}

func serve(w http.ResponseWriter, r *http.Request, name string, h func(*Ctx)) {
	c := &Ctx{R: r, Lang: requestLang(r), Route: name}
	c.W = &respWriter{ResponseWriter: w, c: c}
	c.sess = loadSession(r)
	r.Body = http.MaxBytesReader(c.W, r.Body, maxBodySize)
	if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions && !c.csrfValid() {
		c.Fail(http.StatusForbidden, c.M("Jeton CSRF absent ou invalide : rechargez la page.",
			"Missing or invalid CSRF token: reload the page."))
		return
	}
	if needsSetup() && !setupRoutes[name] {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			c.JSON(J{"ok": false, "msg": c.M("Configuration initiale requise", "Initial setup required"), "setup": true}, http.StatusUnauthorized)
			return
		}
		c.Redirect("/setup")
		return
	}
	h(c)
}

func notFound(c *Ctx) {
	c.Text(http.StatusNotFound, c.M("Introuvable", "Not found"))
}

// ── Session : cookie signé (HMAC-SHA256 avec secret_key) ────────────────────
// Contenu : authenticated, uid, pwfp, csrf. Une session « permanente » (après
// connexion) expire après session_timeout d'inactivité : sa date d'expiration
// est repoussée à chaque requête.

const sessionCookie = "session"

var sessionCookieSecure bool

type session struct {
	data      map[string]any
	modified  bool
	permanent bool
	had       bool
}

func secretKey() []byte {
	if k := str(cfg(), "secret_key", ""); k != "" {
		return []byte(k)
	}
	return fallbackSecret
}

var fallbackSecret = []byte(tokenHex(32))

func signValue(purpose string, payload []byte) string {
	mac := hmac.New(sha256.New, secretKey())
	mac.Write([]byte(purpose + "\x00"))
	mac.Write(payload)
	return base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func unsignValue(purpose, value string) ([]byte, bool) {
	dot := strings.LastIndexByte(value, '.')
	if dot < 0 {
		return nil, false
	}
	payload, err1 := base64.RawURLEncoding.DecodeString(value[:dot])
	sig, err2 := base64.RawURLEncoding.DecodeString(value[dot+1:])
	if err1 != nil || err2 != nil {
		return nil, false
	}
	mac := hmac.New(sha256.New, secretKey())
	mac.Write([]byte(purpose + "\x00"))
	mac.Write(payload)
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return nil, false
	}
	return payload, true
}

func sessionLifetime() time.Duration {
	s := intv(cfg(), "session_timeout", 3600)
	if s <= 0 {
		s = 3600
	}
	return time.Duration(s) * time.Second
}

func loadSession(r *http.Request) *session {
	s := &session{data: map[string]any{}}
	ck, err := r.Cookie(sessionCookie)
	if err != nil {
		return s
	}
	s.had = true
	payload, ok := unsignValue("session", ck.Value)
	if !ok {
		return s
	}
	var stored struct {
		Data map[string]any `json:"d"`
		Exp  int64          `json:"e"`
		Perm bool           `json:"p"`
	}
	if json.Unmarshal(payload, &stored) != nil || stored.Data == nil {
		return s
	}
	if stored.Perm && time.Now().Unix() > stored.Exp {
		return s
	}
	s.data, s.permanent = stored.Data, stored.Perm
	return s
}

func (s *session) get(k string) string {
	if v, ok := s.data[k].(string); ok {
		return v
	}
	return ""
}

func (s *session) set(k string, v any) { s.data[k] = v; s.modified = true }

func (s *session) clear() { s.data = map[string]any{}; s.permanent = false; s.modified = true }

func (s *session) save(c *Ctx) {
	if len(s.data) == 0 {
		if s.had && s.modified {
			http.SetCookie(c.W.ResponseWriter, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1,
				HttpOnly: true, Secure: sessionCookieSecure, SameSite: http.SameSiteStrictMode})
		}
		return
	}
	if !s.modified && !s.permanent {
		return
	}
	exp := time.Now().Add(sessionLifetime())
	payload, _ := json.Marshal(map[string]any{"d": s.data, "e": exp.Unix(), "p": s.permanent})
	ck := &http.Cookie{Name: sessionCookie, Value: signValue("session", payload), Path: "/",
		HttpOnly: true, Secure: sessionCookieSecure, SameSite: http.SameSiteStrictMode}
	if s.permanent {
		ck.Expires = exp
	}
	http.SetCookie(c.W.ResponseWriter, ck)
}

func tokenURLSafe(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// csrfToken : jeton lié à la session, exigé sur toute requête qui modifie quelque
// chose (en-tête X-CSRF-Token, envoyé par api.js).
func (c *Ctx) csrfToken() string {
	tok := c.sess.get("csrf")
	if tok == "" {
		tok = tokenURLSafe(32)
		c.sess.set("csrf", tok)
	}
	return tok
}

func (c *Ctx) csrfValid() bool {
	sent := c.R.Header.Get("X-CSRF-Token")
	tok := c.sess.get("csrf")
	return tok != "" && sent != "" && subtle.ConstantTimeCompare([]byte(sent), []byte(tok)) == 1
}

func (c *Ctx) cspNonce() string {
	if c.nonce == "" {
		c.nonce = tokenURLSafe(16)
	}
	return c.nonce
}

// ── Fichiers du site (CSS, JS, traductions) ─────────────────────────────────
// Servis sous une adresse qui contient leur empreinte (/v/<empreinte>/js/main.js) :
// le navigateur les garde un an sans rien redemander, et toute modification
// change l'empreinte, donc l'adresse. Chaque fichier est compressé une seule
// fois, au démarrage. L'ancienne adresse /assets/… reste servie.

type asset struct {
	data, gz []byte
	mime     string
}

var (
	assets         map[string]asset
	assetsVersion  string
	preloadModules []string
	assetTypes     = map[string]string{".js": "text/javascript", ".css": "text/css", ".json": "application/json",
		".svg": "image/svg+xml", ".png": "image/png", ".woff2": "font/woff2"}
	compressible = map[string]bool{".js": true, ".css": true, ".json": true, ".svg": true}
)

func loadAssets() {
	assets = map[string]asset{}
	var rels []string
	fs.WalkDir(webFS, "templates/assets", func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if _, ok := assetTypes[path.Ext(p)]; ok {
				rels = append(rels, strings.TrimPrefix(p, "templates/assets/"))
			}
		}
		return nil
	})
	sort.Strings(rels)
	digest := sha256.New()
	for _, rel := range rels {
		data, _ := webFS.ReadFile("templates/assets/" + rel)
		digest.Write([]byte(rel + "\x00"))
		digest.Write(data)
		a := asset{data: data, mime: assetTypes[path.Ext(rel)]}
		if compressible[path.Ext(rel)] && len(data) > 512 {
			var buf bytes.Buffer
			zw, _ := gzip.NewWriterLevel(&buf, 9)
			zw.Write(data)
			zw.Close()
			if buf.Len() < len(data) {
				a.gz = buf.Bytes()
			}
		}
		assets[rel] = a
		if strings.HasPrefix(rel, "js/") && strings.HasSuffix(rel, ".js") {
			preloadModules = append(preloadModules, rel)
		}
	}
	preloadModules = append(preloadModules, "locales/fr.js")
	assetsVersion = hex.EncodeToString(digest.Sum(nil))[:12]
}

func assetURL(rel string) string { return "/v/" + assetsVersion + "/" + rel }

func serveAsset(c *Ctx, rel string, cacheControl string) {
	a, ok := assets[rel]
	if !ok {
		notFound(c)
		return
	}
	h := c.W.Header()
	h.Set("Content-Type", a.mime)
	body := a.data
	if a.gz != nil {
		h.Set("Vary", "Accept-Encoding")
		if c.acceptsGzip() {
			h.Set("Content-Encoding", "gzip")
			body = a.gz
		}
	}
	h.Set("Cache-Control", cacheControl)
	c.W.WriteHeader(http.StatusOK)
	if c.R.Method != http.MethodHead {
		c.W.Write(body)
	}
}

func init() {
	route("GET /v/{version}/{filename...}", "versioned_asset", func(c *Ctx) {
		// Une empreinte périmée (page ouverte avant une mise à jour) : pas de cache
		cc := "no-cache"
		if c.Param("version") == assetsVersion {
			cc = "public, max-age=31536000, immutable"
		}
		serveAsset(c, c.Param("filename"), cc)
	})
	route("GET /assets/{filename...}", "static", func(c *Ctx) {
		serveAsset(c, c.Param("filename"), "no-cache")
	})
}

// ── Pages HTML ──────────────────────────────────────────────────────────────
// Les gabarits (templates/*.html) gardent la syntaxe Jinja des versions Python,
// réduite à quelques formes : elles sont résolues ici, une fois au démarrage
// pour ce qui ne change pas (inclusions, adresses des fichiers), à chaque
// requête pour le reste (jeton CSRF, nonce, état initial).

var (
	pages        = map[string]string{}
	assetCallRe  = regexp.MustCompile(`\{\{ asset\('([^']+)'\) \}\}`)
	preloadRe    = regexp.MustCompile(`(?s)\{% for m in preload_modules %\}(.*?)\{% endfor %\}`)
	ifOidcRe     = regexp.MustCompile(`(?s)\{% if oidc_enabled %\}(.*?)\{% endif %\}`)
	leftoverTmpl = regexp.MustCompile(`\{\{.*?\}\}|\{%.*?%\}`)
	dynamicTmpl  = map[string]bool{"{{ csrf_token() }}": true, "{{ csp_nonce() }}": true, "{{ boot|tojson }}": true,
		"{{ panel_version }}": true, "{{ oidc_error }}": true, "{{ oidc_label }}": true,
		"{% if oidc_enabled %}": true, "{% endif %}": true}
)

func loadPages() {
	icons, _ := webFS.ReadFile("templates/partials/icons.html")
	for _, name := range []string{"index.html", "login.html", "setup.html"} {
		raw, err := webFS.ReadFile("templates/" + name)
		if err != nil {
			panic(err)
		}
		t := strings.ReplaceAll(string(raw), `{% include "partials/icons.html" %}`, string(icons))
		t = preloadRe.ReplaceAllStringFunc(t, func(block string) string {
			inner := preloadRe.FindStringSubmatch(block)[1]
			var b strings.Builder
			for _, m := range preloadModules {
				b.WriteString(strings.ReplaceAll(inner, "{{ asset(m) }}", assetURL(m)))
			}
			return b.String()
		})
		t = assetCallRe.ReplaceAllStringFunc(t, func(s string) string {
			return assetURL(assetCallRe.FindStringSubmatch(s)[1])
		})
		for _, left := range leftoverTmpl.FindAllString(t, -1) {
			if !dynamicTmpl[left] {
				panic(fmt.Sprintf("gabarit %s : forme non prise en charge %s", name, left))
			}
		}
		pages[name] = t
	}
}

// render : page HTML ; vars donne panel_version, boot, oidc_* au besoin.
func (c *Ctx) render(name string, vars map[string]any) {
	t := pages[name]
	if strings.Contains(t, "{% if oidc_enabled %}") {
		on, _ := vars["oidc_enabled"].(bool)
		t = ifOidcRe.ReplaceAllStringFunc(t, func(s string) string {
			if on {
				return ifOidcRe.FindStringSubmatch(s)[1]
			}
			return ""
		})
	}
	repl := []string{
		"{{ csrf_token() }}", html.EscapeString(c.csrfToken()),
		"{{ csp_nonce() }}", c.cspNonce(),
		"{{ panel_version }}", html.EscapeString(panelVersion),
	}
	if b, ok := vars["boot"]; ok {
		data, _ := json.Marshal(b) // < > & échappés (<…) : sûr dans un <script>
		data = bytes.ReplaceAll(data, []byte("'"), []byte(`'`))
		repl = append(repl, "{{ boot|tojson }}", string(data))
	}
	for _, k := range []string{"oidc_error", "oidc_label"} {
		if v, ok := vars[k].(string); ok {
			repl = append(repl, "{{ "+k+" }}", html.EscapeString(v))
		}
	}
	c.HTML(http.StatusOK, strings.NewReplacer(repl...).Replace(t))
}
