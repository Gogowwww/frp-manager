// FRP Manager : panel web de gestion de frps et frpc.

package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"log"
	"math/big"
	"net"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"
)

var serverOnce sync.Once

// initServer : fichiers du site, pages, et la route de repli (une seule fois).
func initServer() {
	serverOnce.Do(func() {
		loadAssets()
		loadPages()
		// Toute autre adresse : mêmes contrôles (configuration initiale, CSRF), puis 404
		route("/", "", notFound)
	})
}

// generateSelfSignedCert : certificat auto-signé (10 ans) pour le HTTPS du panel.
func generateSelfSignedCert() error {
	if err := os.MkdirAll(sslCertDir, 0o700); err != nil {
		return err
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return err
	}
	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	tpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "frp-manager", Organization: []string{"FRP Manager"}},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().AddDate(10, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		return err
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	if err := os.WriteFile(sslKeyFile, keyPEM, 0o600); err != nil {
		return err
	}
	return os.WriteFile(sslCertFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644)
}

// tlsConfig : certificat du panel, ou nil (HTTP) si ssl_enabled est faux ou
// si le certificat ne peut être ni lu ni créé.
func tlsConfig() *tls.Config {
	if !boolv(cfg(), "ssl_enabled", true) {
		return nil
	}
	if !fileExists(sslCertFile) || !fileExists(sslKeyFile) {
		if err := generateSelfSignedCert(); err != nil {
			log.Printf("[WARN] Impossible de générer le certificat SSL — démarrage en HTTP (%v)", err)
			return nil
		}
	}
	cert, err := tls.LoadX509KeyPair(sslCertFile, sslKeyFile)
	if err != nil {
		log.Printf("[WARN] Certificat SSL invalide (%v) — démarrage en HTTP", err)
		return nil
	}
	return &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
}

func main() {
	log.SetFlags(0)
	setCfg(loadManagerConfig())
	for _, arg := range os.Args[1:] {
		switch arg {
		case "--reset-password":
			os.Exit(resetPasswordCLI())
		case "--install-frp":
			os.Exit(installFrpCLI())
		case "--version":
			fmt.Println(panelVersion)
			return
		}
	}
	ensureManagerConfig()
	initServer()

	// FRP_MANAGER_HOST (Docker : docker-compose.yml) passe avant bind_host
	host := os.Getenv("FRP_MANAGER_HOST")
	if host == "" {
		host = str(cfg(), "bind_host", "127.0.0.1")
	}
	port := intv(cfg(), "bind_port", 8765)
	if port <= 0 {
		port = 8765
	}
	tc := tlsConfig()
	sessionCookieSecure = tc != nil || boolv(cfg(), "cookie_secure", false)
	proto := "http"
	if tc != nil {
		proto = "https"
	}
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	log.Printf("[INFO] FRP Manager %s démarré sur %s://%s", panelVersion, proto, addr)
	if needsSetup() {
		log.Printf("[INFO] Aucun mot de passe : ouvrez le panel pour créer l'identifiant administrateur.")
		if host != "127.0.0.1" && host != "::1" && host != "localhost" {
			log.Printf("[WARN] Le panel écoute sur %s sans mot de passe : la première personne qui l'ouvre choisit les identifiants. Configurez-le sans attendre.", host)
		}
	}
	if !mirrorsAllowed() {
		log.Printf("[INFO] Miroirs de téléchargement tiers désactivés")
	}
	go fwSyncLoop()
	go webhookWorker()
	go webhookMonitorLoop()
	go statsLoop()
	webhookEmit("panel.started", [2]string{"Panel démarré", "Panel started"},
		[2]string{fmt.Sprintf("FRP Manager %s écoute sur %s.", panelVersion, addr), fmt.Sprintf("FRP Manager %s is listening on %s.", panelVersion, addr)},
		"", nil)

	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		TLSConfig:         tc,
		ReadHeaderTimeout: 15 * time.Second,
		IdleTimeout:       120 * time.Second,
		ErrorLog:          log.New(discardTLSNoise{}, "", 0),
	}
	var err error
	if tc != nil {
		err = srv.ListenAndServeTLS("", "")
	} else {
		err = srv.ListenAndServe()
	}
	log.Fatalf("[ERROR] %v", err)
}

// discardTLSNoise : les poignées de main TLS refusées (certificat auto-signé
// pas encore accepté par le navigateur) ne remplissent pas le journal.
type discardTLSNoise struct{}

func (discardTLSNoise) Write(p []byte) (int, error) {
	s := string(p)
	if len(s) > 0 && !contains(s, "TLS handshake error") {
		os.Stderr.Write(p)
	}
	return len(p), nil
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
