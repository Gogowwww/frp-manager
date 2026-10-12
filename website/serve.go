// Serveur du site et de la démo, pour un hébergement qui lance une seule
// application (conteneur, « cloudpod », egg Pterodactyl…). Sert le dossier site/
// sur le port $PORT, ou $SERVER_PORT (Pterodactyl), 8080 par défaut ; toutes
// interfaces ($HOST pour changer).

package main

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"mime"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Fichiers texte servis compressés (gzip) à qui le demande, compressés une fois
// puis gardés en mémoire (le site pèse quelques centaines de Ko).
var compressible = map[string]bool{".html": true, ".css": true, ".js": true, ".json": true, ".svg": true, ".xml": true, ".txt": true}

var mimeTypes = map[string]string{".js": "text/javascript", ".css": "text/css", ".json": "application/json",
	".svg": "image/svg+xml", ".webp": "image/webp", ".html": "text/html; charset=utf-8", ".png": "image/png",
	".txt": "text/plain; charset=utf-8", ".xml": "application/xml", ".ico": "image/x-icon", ".woff2": "font/woff2"}

type gzEntry struct {
	mtime time.Time
	body  []byte
	etag  string
}

type siteServer struct {
	dir string
	mu  sync.Mutex
	gz  map[string]gzEntry
}

func contentType(name string) string {
	ext := strings.ToLower(filepath.Ext(name))
	if t, ok := mimeTypes[ext]; ok {
		return t
	}
	if t := mime.TypeByExtension(ext); t != "" {
		return t
	}
	return "application/octet-stream"
}

func (s *siteServer) notFound(w http.ResponseWriter, r *http.Request) {
	body, err := os.ReadFile(filepath.Join(s.dir, "404.html"))
	if err != nil || (r.Method != http.MethodGet && r.Method != http.MethodHead) {
		http.Error(w, "404 Not Found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusNotFound)
	if r.Method == http.MethodGet {
		w.Write(body)
	}
}

func (s *siteServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p := path.Clean("/" + r.URL.Path)
	// Assets sous v/<empreinte>/ (démo) ou assets/<empreinte>/ (site) : jamais
	// modifiés, gardés en cache ; les pages sont relues à chaque visite.
	if strings.HasPrefix(p, "/v/") || strings.HasPrefix(p, "/demo/v/") || strings.HasPrefix(p, "/assets/") {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		w.Header().Set("Cache-Control", "no-cache")
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "same-origin")
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "405 Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	file := filepath.Join(s.dir, filepath.FromSlash(strings.TrimPrefix(p, "/")))
	st, err := os.Stat(file)
	if err == nil && st.IsDir() {
		if !strings.HasSuffix(r.URL.Path, "/") {
			target := r.URL.Path + "/"
			if r.URL.RawQuery != "" {
				target += "?" + r.URL.RawQuery
			}
			http.Redirect(w, r, target, http.StatusMovedPermanently)
			return
		}
		file = filepath.Join(file, "index.html")
		st, err = os.Stat(file)
	}
	if err != nil || st.IsDir() || strings.Contains(p, "/.") { // pas de liste des dossiers ni de fichiers cachés
		s.notFound(w, r)
		return
	}
	w.Header().Set("Content-Type", contentType(file))
	if compressible[strings.ToLower(filepath.Ext(file))] && strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
		if e, ok := s.compressed(file, st.ModTime()); ok {
			h := w.Header()
			h.Set("Content-Encoding", "gzip")
			h.Set("Vary", "Accept-Encoding")
			h.Set("ETag", e.etag)
			h.Set("Last-Modified", st.ModTime().UTC().Format(http.TimeFormat))
			if r.Header.Get("If-None-Match") == e.etag {
				w.WriteHeader(http.StatusNotModified)
				return
			}
			h.Set("Content-Length", fmt.Sprint(len(e.body)))
			w.WriteHeader(http.StatusOK)
			if r.Method == http.MethodGet {
				w.Write(e.body)
			}
			return
		}
	}
	f, err := os.Open(file)
	if err != nil {
		s.notFound(w, r)
		return
	}
	defer f.Close()
	http.ServeContent(w, r, "", st.ModTime(), f)
}

// compressed : corps gzip du fichier (au-delà de 512 octets), mis en cache.
func (s *siteServer) compressed(file string, mtime time.Time) (gzEntry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.gz[file]; ok && e.mtime.Equal(mtime) {
		return e, e.body != nil
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		return gzEntry{}, false
	}
	e := gzEntry{mtime: mtime}
	if len(raw) > 512 {
		var buf bytes.Buffer
		zw, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
		zw.Write(raw)
		zw.Close()
		sum := sha256.Sum256(raw)
		e.body, e.etag = buf.Bytes(), `"`+hex.EncodeToString(sum[:])[:20]+`-gz"`
	}
	s.gz[file] = e
	return e, e.body != nil
}

func serveSite(dir string) error {
	if _, err := os.Stat(filepath.Join(dir, "index.html")); err != nil {
		return fmt.Errorf("site introuvable : %s/index.html", dir)
	}
	host := os.Getenv("HOST")
	if host == "" {
		host = "0.0.0.0"
	}
	port := os.Getenv("PORT")
	if port == "" {
		port = os.Getenv("SERVER_PORT") // port attribué par un panel Pterodactyl
	}
	if port == "" {
		port = "8080"
	}
	addr := net.JoinHostPort(host, port)
	fmt.Printf("Site FRP Manager sur http://%s\n", addr)
	srv := &http.Server{Addr: addr, Handler: &siteServer{dir: dir, gz: map[string]gzEntry{}},
		ReadHeaderTimeout: 15 * time.Second, IdleTimeout: 120 * time.Second, ErrorLog: log.New(os.Stderr, "", 0)}
	return srv.ListenAndServe()
}
