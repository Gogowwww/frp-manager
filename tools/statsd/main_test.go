package main

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newTestServer(t *testing.T, token string) (*server, string) {
	t.Helper()
	file := filepath.Join(t.TempDir(), "stats.json")
	return &server{
		st: newStore(file), token: token,
		oldHost: "frp-manager.gogow.fr", newSite: "https://frp-manager.org",
		now: func() time.Time { return time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC) },
	}, file
}

func do(s *server, method, target, body, host, remote string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, target, strings.NewReader(body))
	if host != "" {
		r.Host = host
	}
	if remote != "" {
		r.RemoteAddr = remote + ":1234"
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}

const ok = `{"version":"0.4.1","arch":"amd64","install":"apt"}`

func TestPing(t *testing.T) {
	s, file := newTestServer(t, "")
	if w := do(s, "POST", "/api/ping", ok, "", "203.0.113.9"); w.Code != http.StatusNoContent {
		t.Fatalf("envoi valide : %d", w.Code)
	}
	dev := `{"version":"0.4.1+dev.22.g7352f82","arch":"arm64","install":"docker"}`
	do(s, "POST", "/api/ping", dev, "", "203.0.113.10")
	do(s, "POST", "/api/ping", strings.Replace(dev, "dev.22.g7352f82", "dev.23.gabcdef0", 1), "", "203.0.113.11")
	day := s.st.days["2026-10-25"]
	if day["0.4.1|amd64|apt"] != 1 || day["0.4.1+dev|arm64|docker"] != 2 {
		t.Fatalf("compteurs (dev regroupés sous X.Y.Z+dev) : %v", day)
	}
	for _, bad := range []string{
		`{"version":"x","arch":"amd64","install":"apt"}`,
		`{"version":"0.4.1","arch":"mips","install":"apt"}`,
		`{"version":"0.4.1","arch":"amd64","install":"snap"}`,
		`pas du json`,
		`{"version":"0.4.1","arch":"amd64","install":"apt","pad":"` + strings.Repeat("a", 2000) + `"}`,
	} {
		if w := do(s, "POST", "/api/ping", bad, "", "203.0.113.9"); w.Code != http.StatusBadRequest {
			t.Fatalf("%.40q devrait être refusé : %d", bad, w.Code)
		}
	}
	if w := do(s, "GET", "/api/ping", "", "", ""); w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET : %d", w.Code)
	}
	// Persistance : un nouveau processus relit le fichier
	if again := newStore(file); again.days["2026-10-25"]["0.4.1|amd64|apt"] != 1 {
		t.Fatalf("compteurs non relus : %v", again.days)
	}
}

func TestPerIPLimit(t *testing.T) {
	s, _ := newTestServer(t, "")
	for i := 0; i < perIPDay; i++ {
		if w := do(s, "POST", "/api/ping", ok, "", "198.51.100.1"); w.Code != http.StatusNoContent {
			t.Fatalf("envoi %d : %d", i, w.Code)
		}
	}
	if w := do(s, "POST", "/api/ping", ok, "", "198.51.100.1"); w.Code != http.StatusTooManyRequests {
		t.Fatalf("au-delà de la limite par adresse : %d", w.Code)
	}
	if w := do(s, "POST", "/api/ping", ok, "", "198.51.100.2"); w.Code != http.StatusNoContent {
		t.Fatalf("une autre adresse reste acceptée : %d", w.Code)
	}
}

func TestTrustProxy(t *testing.T) {
	s, _ := newTestServer(t, "")
	send := func(forwarded string) int {
		r := httptest.NewRequest("POST", "/api/ping", strings.NewReader(ok))
		r.RemoteAddr = "10.0.0.1:1"
		r.Header.Set("X-Forwarded-For", forwarded)
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		return w.Code
	}
	// Sans TRUST_PROXY, X-Forwarded-For est ignoré : tout vient de 10.0.0.1
	for i := 0; i < perIPDay; i++ {
		send("203.0.113." + string(rune('1'+i%9)))
	}
	if send("203.0.113.99") != http.StatusTooManyRequests {
		t.Fatal("X-Forwarded-For ne doit pas être cru sans TRUST_PROXY")
	}
	s2, _ := newTestServer(t, "")
	s2.trustProxy = true
	r := httptest.NewRequest("POST", "/api/ping", strings.NewReader(ok))
	r.RemoteAddr = "10.0.0.1:1"
	r.Header.Set("X-Forwarded-For", "203.0.113.5, 10.0.0.1")
	w := httptest.NewRecorder()
	s2.ServeHTTP(w, r)
	if w.Code != http.StatusNoContent || len(s2.st.ips) != 1 {
		t.Fatalf("TRUST_PROXY : %d", w.Code)
	}
}

func TestStatsToken(t *testing.T) {
	closed, _ := newTestServer(t, "")
	if w := do(closed, "GET", "/stats.json", "", "", ""); w.Code != http.StatusNotFound {
		t.Fatalf("sans jeton configuré, la route n'existe pas : %d", w.Code)
	}
	s, _ := newTestServer(t, "secret")
	do(s, "POST", "/api/ping", ok, "", "203.0.113.9")
	if w := do(s, "GET", "/stats.json", "", "", ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("sans en-tête : %d", w.Code)
	}
	r := httptest.NewRequest("GET", "/stats.json", nil)
	r.Header.Set("Authorization", "Bearer faux")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("mauvais jeton : %d", w.Code)
	}
	r.Header.Set("Authorization", "Bearer secret")
	w = httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"0.4.1|amd64|apt":1`) {
		t.Fatalf("bon jeton : %d %s", w.Code, w.Body.String())
	}
}

func TestHealthcheck(t *testing.T) {
	s, _ := newTestServer(t, "")
	srv := httptest.NewServer(s)
	defer srv.Close()
	if code := healthcheck(strings.TrimPrefix(srv.URL, "http://")); code != 0 {
		t.Fatalf("serveur en marche : %d", code)
	}
	srv.Close()
	if code := healthcheck(strings.TrimPrefix(srv.URL, "http://")); code == 0 {
		t.Fatal("serveur arrêté : le contrôle doit échouer")
	}
	if code := healthcheck("pas-une-adresse"); code == 0 {
		t.Fatal("adresse invalide : le contrôle doit échouer")
	}
}

func TestOldHostRedirect(t *testing.T) {
	s, _ := newTestServer(t, "")
	if w := do(s, "GET", "/docs/install/?a=1", "", "FRP-Manager.gogow.fr:443", ""); w.Code != http.StatusMovedPermanently ||
		w.Header().Get("Location") != "https://frp-manager.org/docs/install/?a=1" {
		t.Fatalf("redirection : %d %q", w.Code, w.Header().Get("Location"))
	}
	if w := do(s, "POST", "/api/ping", ok, "frp-manager.gogow.fr", "203.0.113.9"); w.Code != http.StatusNoContent {
		t.Fatalf("/api/ping sur l'ancien nom d'hôte ne doit pas être redirigé : %d", w.Code)
	}
	if w := do(s, "GET", "/healthz", "", "api.frp-manager.org", ""); w.Code != http.StatusOK {
		t.Fatalf("santé sur un autre nom d'hôte : %d", w.Code)
	}
	if w := do(s, "GET", "/", "", "api.frp-manager.org", ""); w.Code != http.StatusNotFound {
		t.Fatalf("autre nom d'hôte, autre chemin : %d", w.Code)
	}
}
