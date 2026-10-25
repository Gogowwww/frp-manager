// Récepteur des statistiques anonymes de FRP Manager, à héberger soi-même.
// Receiver of FRP Manager's anonymous statistics, to host yourself.
//
//	go run ./tools/statsd            (écoute sur :8080)
//
// Un panel dont l'administrateur l'a accepté envoie une fois par jour
// {"version", "arch", "install"} en POST sur /api/ping. Seuls des compteurs par
// jour sont gardés : ni adresse IP, ni identifiant, ni heure. L'adresse sert
// seulement, en mémoire et sous forme d'empreinte, à limiter les abus ; elle
// est oubliée au bout de 24 h.
// A panel whose administrator agreed sends {"version", "arch", "install"} with
// POST to /api/ping once a day. Only daily counters are kept: no IP address, no
// identifier, no time. The address is only used, in memory and hashed, to limit
// abuse; it is forgotten after 24 h.
//
// Les compteurs sont lus avec GET /stats.json et l'en-tête
// « Authorization: Bearer <STATS_TOKEN> » ; sans STATS_TOKEN, cette route
// n'existe pas (404). GET /healthz répond 200.
// Counters are read with GET /stats.json and the "Authorization: Bearer
// <STATS_TOKEN>" header; without STATS_TOKEN, that route does not exist (404).
// GET /healthz answers 200.
//
// « frp-manager-statsd -healthcheck » interroge /healthz du programme déjà lancé
// et sort en 0 s'il répond (contrôle de santé de l'image Docker, qui n'a pas
// de curl).
// "frp-manager-statsd -healthcheck" queries /healthz of the already running
// program and exits 0 if it answers (health check of the Docker image, which
// has no curl).
//
// Variables : LISTEN (défaut :8080), STATS_FILE (défaut stats.json),
// STATS_TOKEN, TRUST_PROXY=1 (lire l'adresse dans CF-Connecting-IP ou
// X-Forwarded-For, derrière un proxy ou un tunnel de confiance), OLD_HOST
// (défaut frp-manager.gogow.fr) et NEW_SITE (défaut https://frp-manager.org) :
// toute requête GET ou HEAD faite sur OLD_HOST est redirigée vers NEW_SITE (301),
// sauf /api/ping.
// Variables: LISTEN (default :8080), STATS_FILE (default stats.json),
// STATS_TOKEN, TRUST_PROXY=1 (read the address from CF-Connecting-IP or
// X-Forwarded-For, behind a trusted proxy or tunnel), OLD_HOST (default
// frp-manager.gogow.fr) and NEW_SITE (default https://frp-manager.org): any GET
// or HEAD request made on OLD_HOST is redirected to NEW_SITE (301), except
// /api/ping.

package main

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	keepDays  = 400  // jours gardés / days kept
	perIPDay  = 30   // signaux par adresse et par jour / pings per address per day
	maxPerDay = 5000 // signaux acceptés par jour, tous confondus / accepted pings per day, all together
)

var (
	versionRe = regexp.MustCompile(`^[0-9]{1,4}\.[0-9]{1,4}\.[0-9]{1,4}(-[0-9A-Za-z.]{1,20})?(\+[0-9A-Za-z.-]{1,60})?$`)
	archOK    = map[string]bool{"amd64": true, "arm64": true, "arm": true}
	installOK = map[string]bool{"script": true, "apt": true, "docker": true}
)

type store struct {
	file string

	mu    sync.Mutex
	days  map[string]map[string]int // jour → "version|arch|install" → signaux / day → key → pings
	ips   map[string]int            // empreinte de l'adresse → signaux (24 h) / address hash → pings (24 h)
	ipsAt time.Time
}

func newStore(file string) *store {
	s := &store{file: file, days: map[string]map[string]int{}, ips: map[string]int{}, ipsAt: time.Now()}
	if raw, err := os.ReadFile(file); err == nil {
		var saved struct {
			Days map[string]map[string]int `json:"days"`
		}
		if json.Unmarshal(raw, &saved) == nil && saved.Days != nil {
			s.days = saved.Days
		}
	}
	return s
}

// countedVersion : version telle que comptée ; les instantanés de développement
// (+dev.<n>.g<commit>) sont regroupés sous « X.Y.Z+dev ».
// countedVersion: version as counted; development snapshots (+dev.<n>.g<commit>)
// are grouped under "X.Y.Z+dev".
func countedVersion(v string) string {
	if base, build, ok := strings.Cut(v, "+"); ok {
		if strings.HasPrefix(build, "dev") {
			return base + "+dev"
		}
		return base
	}
	return v
}

// saveLocked : écriture atomique du fichier des compteurs (s.mu tenu).
// saveLocked: atomic write of the counters file (s.mu held).
func (s *store) saveLocked() error {
	data, err := json.Marshal(map[string]any{"days": s.days})
	if err != nil {
		return err
	}
	tmp := s.file + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.file)
}

type server struct {
	st         *store
	token      string
	trustProxy bool
	oldHost    string
	newSite    string
	now        func() time.Time
}

// clientIP : adresse du visiteur ; derrière un proxy de confiance, celle qu'il transmet.
// clientIP: visitor's address; behind a trusted proxy, the one it passes on.
func (s *server) clientIP(r *http.Request) string {
	if s.trustProxy {
		if ip := strings.TrimSpace(r.Header.Get("CF-Connecting-IP")); ip != "" {
			return ip
		}
		if first, _, _ := strings.Cut(r.Header.Get("X-Forwarded-For"), ","); strings.TrimSpace(first) != "" {
			return strings.TrimSpace(first)
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// requestHost : nom d'hôte demandé, sans port ni point final.
// requestHost: requested host name, without port or trailing dot.
func (s *server) requestHost(r *http.Request) string {
	host := r.Host
	if s.trustProxy {
		if h := r.Header.Get("X-Forwarded-Host"); h != "" {
			host = h
		}
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return strings.ToLower(strings.TrimSuffix(host, "."))
}

func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p := path.Clean("/" + r.URL.Path)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	switch {
	case p == "/api/ping":
		s.ping(w, r)
	case s.oldHost != "" && s.requestHost(r) == s.oldHost && (r.Method == http.MethodGet || r.Method == http.MethodHead):
		http.Redirect(w, r, s.newSite+r.URL.RequestURI(), http.StatusMovedPermanently)
	case p == "/healthz":
		w.Header().Set("Cache-Control", "no-store")
		fmt.Fprintln(w, "ok")
	case p == "/stats.json" && s.token != "":
		s.stats(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (s *server) ping(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "405 Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	var in struct{ Version, Arch, Install string }
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1025))
	if err != nil || len(raw) > 1024 || json.Unmarshal(raw, &in) != nil ||
		!versionRe.MatchString(in.Version) || !archOK[in.Arch] || !installOK[in.Install] {
		http.Error(w, "400 Bad Request", http.StatusBadRequest)
		return
	}
	sum := sha256.Sum256([]byte(s.clientIP(r)))
	ip := hex.EncodeToString(sum[:8])
	now := s.now().UTC()
	day := now.Format("2006-01-02")
	st := s.st
	st.mu.Lock()
	if now.Sub(st.ipsAt) > 24*time.Hour {
		st.ips, st.ipsAt = map[string]int{}, now
	}
	total := 0
	for _, n := range st.days[day] {
		total += n
	}
	if st.ips[ip] >= perIPDay || total >= maxPerDay {
		st.mu.Unlock()
		w.Header().Set("Retry-After", "3600")
		http.Error(w, "429 Too Many Requests", http.StatusTooManyRequests)
		return
	}
	st.ips[ip]++
	if st.days[day] == nil {
		st.days[day] = map[string]int{}
	}
	st.days[day][countedVersion(in.Version)+"|"+in.Arch+"|"+in.Install]++
	oldest := now.AddDate(0, 0, -keepDays).Format("2006-01-02")
	for d := range st.days {
		if d < oldest {
			delete(st.days, d)
		}
	}
	err = st.saveLocked()
	st.mu.Unlock()
	if err != nil {
		log.Printf("statistiques : %v", err)
	}
	w.WriteHeader(http.StatusNoContent)
}

// stats : les compteurs, avec le jeton en en-tête (comparaison à temps constant).
// stats: the counters, with the token in the header (constant-time comparison).
func (s *server) stats(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if subtle.ConstantTimeCompare([]byte(got), []byte(s.token)) != 1 {
		w.Header().Set("WWW-Authenticate", `Bearer realm="frp-manager-stats"`)
		http.Error(w, "401 Unauthorized", http.StatusUnauthorized)
		return
	}
	s.st.mu.Lock()
	data, err := json.Marshal(map[string]any{"days": s.st.days, "today": s.now().UTC().Format("2006-01-02")})
	s.st.mu.Unlock()
	if err != nil {
		http.Error(w, "500 Internal Server Error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(data)
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// healthcheck : GET /healthz sur l'adresse d'écoute, 0 si 200.
// healthcheck: GET /healthz on the listen address, 0 if 200.
func healthcheck(listen string) int {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return 1
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Get("http://" + net.JoinHostPort(host, port) + "/healthz")
	if err != nil {
		return 1
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}

func main() {
	addr := env("LISTEN", ":8080")
	if len(os.Args) > 1 && os.Args[1] == "-healthcheck" {
		os.Exit(healthcheck(addr))
	}
	file := env("STATS_FILE", "stats.json")
	s := &server{
		st:         newStore(file),
		token:      os.Getenv("STATS_TOKEN"),
		trustProxy: os.Getenv("TRUST_PROXY") == "1",
		oldHost:    strings.ToLower(env("OLD_HOST", "frp-manager.gogow.fr")),
		newSite:    strings.TrimRight(env("NEW_SITE", "https://frp-manager.org"), "/"),
		now:        time.Now,
	}
	fmt.Printf("Statistiques FRP Manager sur %s (compteurs : %s, redirection de %s vers %s)\n", addr, file, s.oldHost, s.newSite)
	srv := &http.Server{Addr: addr, Handler: s, ReadHeaderTimeout: 15 * time.Second,
		ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 120 * time.Second}
	log.Fatal(srv.ListenAndServe())
}
