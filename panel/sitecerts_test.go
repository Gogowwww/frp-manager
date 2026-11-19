package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// pairForTests : paire auto-signée pour des noms donnés (ou une autorité).
// pairForTests: self-signed pair for the given names (or a certificate authority).
func pairForTests(t *testing.T, names []string, ca bool) ([]byte, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: names[0]},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(48 * time.Hour),
		DNSNames:              names,
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: ca,
		IsCA:                  ca,
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
}

// withSiteCertDir : dossier de certificats temporaire pour un test.
// withSiteCertDir: temporary certificate folder for a test.
func withSiteCertDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	saved := siteCertDir
	siteCertDir = dir
	t.Cleanup(func() { siteCertDir = saved })
	return dir
}

func TestSaveListDeleteSiteCert(t *testing.T) {
	dir := withSiteCertDir(t)
	certPEM, keyPEM := pairForTests(t, []string{"site.example.com", "www.site.example.com"}, false)
	cert, err := validateSiteCert(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("paire refusée : %v", err)
	}
	id, err := saveSiteCert("", certPEM, keyPEM, cert)
	if err != nil {
		t.Fatalf("enregistrement : %v", err)
	}
	if id != "site.example.com" {
		t.Fatalf("identifiant = %q", id)
	}
	// Clé privée lisible par root seulement (les droits POSIX ne s'appliquent
	// pas sous Windows, où le module tourne aussi pour les tests)
	// Private key readable by root only (POSIX permissions do not apply on
	// Windows, where the module also runs for tests)
	info, err := os.Stat(filepath.Join(dir, id+".key"))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); runtime.GOOS != "windows" && perm != 0o600 {
		t.Fatalf("droits de la clé = %o", perm)
	}

	list := siteCertList()
	if len(list) != 1 {
		t.Fatalf("liste = %d certificats", len(list))
	}
	entry := list[0]
	domains, _ := entry["domains"].([]string)
	if len(domains) != 2 || domains[0] != "site.example.com" {
		t.Fatalf("domaines = %v", entry["domains"])
	}
	if entry["expired"] != false {
		t.Fatalf("certificat marqué expiré : %v", entry["expired"])
	}
	if s, _ := entry["fingerprint"].(string); len(s) < 32 {
		t.Fatalf("empreinte = %q", s)
	}
	if s, _ := entry["crt_path"].(string); s != filepath.Join(dir, id+".crt") {
		t.Fatalf("chemin = %q", s)
	}

	// Deuxième import du même nom : identifiant suffixé, rien d'écrasé
	// Second import of the same name: suffixed id, nothing overwritten
	again, err := saveSiteCert("", certPEM, keyPEM, cert)
	if err != nil || again == id {
		t.Fatalf("second import = %q, %v", again, err)
	}
	if len(siteCertList()) != 2 {
		t.Fatalf("liste après second import = %d", len(siteCertList()))
	}

	if _, err := deleteSiteCert(id); err != nil {
		t.Fatalf("suppression : %v", err)
	}
	if fileExists(filepath.Join(dir, id+".crt")) || fileExists(filepath.Join(dir, id+".key")) {
		t.Fatal("fichiers restants après suppression")
	}
	if _, err := deleteSiteCert(id); err == nil {
		t.Fatal("suppression d'un certificat absent acceptée")
	}
}

func TestValidateSiteCert(t *testing.T) {
	certPEM, keyPEM := pairForTests(t, []string{"a.example.com"}, false)
	otherCert, otherKey := pairForTests(t, []string{"b.example.com"}, false)

	if _, err := validateSiteCert(nil, keyPEM); err == nil {
		t.Fatal("paire vide acceptée")
	}
	if _, err := validateSiteCert(certPEM, otherKey); err == nil {
		t.Fatal("clé d'un autre certificat acceptée")
	}
	caPEM, caKey := pairForTests(t, []string{"ca.example.com"}, true)
	if _, err := validateSiteCert(caPEM, caKey); err == nil {
		t.Fatal("certificat d'autorité accepté")
	}
	if _, err := validateSiteCert(otherCert, otherKey); err != nil {
		t.Fatalf("paire valide refusée : %v", err)
	}
	if _, err := validateSiteCert(certPEM, append(keyPEM, make([]byte, maxSiteCertSize)...)); err == nil {
		t.Fatal("clé trop volumineuse acceptée")
	}
}

func TestSiteCertID(t *testing.T) {
	cases := map[string]string{
		"*.example.com":        "example.com",
		"Site.Example.COM":     "site.example.com",
		"my cert / name":       "my-cert---name",
		"":                     "",
		"../etc/passwd":        "etc-passwd",
		"---":                  "",
		"a.example.com (prod)": "a.example.com--prod",
	}
	for in, want := range cases {
		if got := siteCertID(in); got != want {
			t.Errorf("siteCertID(%q) = %q, attendu %q", in, got, want)
		}
	}
}

func TestDeleteSiteCertRefusesBadID(t *testing.T) {
	withSiteCertDir(t)
	if _, err := deleteSiteCert("../../etc/passwd"); err == nil {
		t.Fatal("identifiant avec chemin accepté")
	}
	if _, err := deleteSiteCert(""); err == nil {
		t.Fatal("identifiant vide accepté")
	}
}

func TestSiteCertUsageReadsFrpcConfigs(t *testing.T) {
	dir := withSiteCertDir(t)
	certPEM, keyPEM := pairForTests(t, []string{"used.example.com"}, false)
	cert, _ := validateSiteCert(certPEM, keyPEM)
	id, err := saveSiteCert("", certPEM, keyPEM, cert)
	if err != nil {
		t.Fatal(err)
	}
	crtPath, _ := siteCertPaths(id)
	conf := filepath.Join(t.TempDir(), "frpc.toml")
	content := "serverAddr = \"1.2.3.4\"\n\n[[proxies]]\nname = \"site\"\ntype = \"https\"\ncustomDomains = [\"used.example.com\"]\n\n[proxies.plugin]\ntype = \"https2http\"\nlocalAddr = \"127.0.0.1:8080\"\ncrtPath = \"" + crtPath + "\"\nkeyPath = \"" + siteCertPaths2(id) + "\"\n"
	if err := os.WriteFile(conf, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	instancesMu.Lock()
	saved := instancesMap
	instancesMap = map[string]*Instance{"frpc": {ID: "frpc", Type: "frpc", Config: conf}}
	instancesMu.Unlock()
	t.Cleanup(func() {
		instancesMu.Lock()
		instancesMap = saved
		instancesMu.Unlock()
	})

	used := siteCertUsage(id)
	if len(used) != 1 || used[0] != "frpc" {
		t.Fatalf("utilisations = %v", used)
	}
	if _, err := deleteSiteCert(id); err == nil {
		t.Fatal("suppression d'un certificat utilisé acceptée")
	}
	if !fileExists(filepath.Join(dir, id+".crt")) {
		t.Fatal("certificat supprimé malgré l'utilisation")
	}
}

func siteCertPaths2(id string) string {
	_, key := siteCertPaths(id)
	return key
}
