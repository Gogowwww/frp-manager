// Certificats des sites : magasin de paires certificat/clé pour les tunnels
// HTTPS de frpc (plugin https2http / https2https), import PEM et suppression.
// Le panel ne sert jamais ces certificats lui-même : ils vivent sur la machine
// frpc et sont déclarés dans le tunnel.
// Site certificates: store of certificate/key pairs for frpc HTTPS tunnels
// (https2http / https2https plugin), PEM import and removal. The panel never
// serves these certificates itself: they live on the frpc machine and are
// declared in the tunnel.

package main

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
)

// maxSiteCertSize : certificat, chaîne et clé tiennent dans cette limite.
// maxSiteCertSize: certificate, chain and key fit within this limit.
const maxSiteCertSize = 64 << 10

var (
	// Dossier des certificats des sites, à côté de la configuration du panel.
	// Site certificate folder, next to the panel configuration.
	siteCertDir = filepath.Join(mgrConfDir, "certs")
	// Identifiant de certificat : nom de fichier sûr, sans chemin.
	// Certificate id: a safe file name, no path.
	siteCertIDRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
)

func init() {
	route("GET /api/manager/certs", "api_manager_certs_get", perm(P("config.read"), func(c *Ctx) {
		acme := acmeCfg()
		c.JSON(J{
			"ok":    true,
			"dir":   siteCertDir,
			"certs": siteCertList(),
			"proxy": httpsProxyCert(),
			"acme": J{"provider": acme.Provider, "configured": acme.Token != "",
				"staging": acme.Staging, "has_email": acme.Email != ""},
		})
	}))

	route("POST /api/manager/certs", "api_manager_certs_save", perm(P("settings.manage"), func(c *Ctx) {
		data := c.Body()
		certPEM := []byte(strings.TrimSpace(str(data, "cert", "")))
		keyPEM := []byte(strings.TrimSpace(str(data, "key", "")))
		cert, err := validateSiteCert(certPEM, keyPEM)
		if err != nil {
			c.Fail(http.StatusBadRequest, c.M("Certificat refusé : ", "Certificate rejected: ")+errIn(err, c.Lang))
			return
		}
		id, err := saveSiteCert(str(data, "name", ""), certPEM, keyPEM, cert)
		if err != nil {
			c.Fail(http.StatusInternalServerError, c.M("Certificat non enregistré : ", "Certificate not saved: ")+err.Error())
			return
		}
		info, _ := siteCertInfo(id)
		c.JSON(J{"ok": true, "id": id, "certificate": info,
			"msg": c.M("Certificat enregistré : il peut être choisi dans un tunnel HTTPS.",
				"Certificate saved: it can be picked in an HTTPS tunnel.")})
	}))

	route("DELETE /api/manager/certs/{id}", "api_manager_certs_delete", perm(P("settings.manage"), func(c *Ctx) {
		// Certificat choisi dans les réglages du reverse proxy HTTPS : le
		// supprimer laisserait les tunnels HTTPS sans certificat.
		// Certificate chosen in the HTTPS reverse proxy settings: deleting it
		// would leave HTTPS tunnels without a certificate.
		if c.Param("id") == str(cfg(), "https_proxy_cert_id", "") && boolv(cfg(), "https_proxy_cert", false) {
			c.Fail(http.StatusConflict, c.M("Certificat choisi dans Réglages → Proxy inverse HTTPS : choisissez-en un autre avant de supprimer.",
				"Certificate chosen in Settings → HTTPS reverse proxy: pick another one before deleting."))
			return
		}
		inUse, err := deleteSiteCert(c.Param("id"))
		if err != nil {
			if len(inUse) > 0 {
				c.Fail(http.StatusConflict, c.M("Certificat utilisé par : ", "Certificate used by: ")+strings.Join(inUse, ", "))
				return
			}
			c.Fail(http.StatusBadRequest, c.M("Certificat non supprimé : ", "Certificate not deleted: ")+errIn(err, c.Lang))
			return
		}
		c.JSON(J{"ok": true, "msg": c.M("Certificat supprimé.", "Certificate deleted.")})
	}))
}

// siteCertPaths : chemins de la paire d'un certificat.
// siteCertPaths: paths of a certificate pair.
func siteCertPaths(id string) (crt, key string) {
	return filepath.Join(siteCertDir, id+".crt"), filepath.Join(siteCertDir, id+".key")
}

func siteCertMetaPath(id string) string { return filepath.Join(siteCertDir, id+".json") }

// siteCertMeta : ce que le panel sait d'un certificat qu'il gère lui-même
// (obtenu par ACME), pour le renouveler.
// siteCertMeta: what the panel knows about a certificate it manages itself
// (obtained through ACME), to renew it.
type siteCertMeta struct {
	Domains  []string  `json:"domains,omitempty"`
	Provider string    `json:"provider,omitempty"`
	Staging  bool      `json:"staging,omitempty"`
	Issued   time.Time `json:"issued,omitempty"`
}

func siteCertMetaOf(id string) siteCertMeta {
	var m siteCertMeta
	if b, err := os.ReadFile(siteCertMetaPath(id)); err == nil {
		_ = json.Unmarshal(b, &m)
	}
	return m
}

func writeSiteCertMeta(id string, m siteCertMeta) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(siteCertMetaPath(id), b, 0o600)
}

// siteCertIDs : certificats présents (un .crt avec sa .key).
// siteCertIDs: certificates present (a .crt with its .key).
func siteCertIDs() []string {
	entries, err := os.ReadDir(siteCertDir)
	if err != nil {
		return nil
	}
	ids := []string{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".crt") {
			continue
		}
		id := strings.TrimSuffix(name, ".crt")
		if !siteCertIDRe.MatchString(id) {
			continue
		}
		if _, key := siteCertPaths(id); !fileExists(key) {
			continue
		}
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// siteCertList : état de chaque certificat, jamais la clé.
// siteCertList: state of each certificate, never the key.
func siteCertList() []J {
	out := []J{}
	for _, id := range siteCertIDs() {
		if info, err := siteCertInfo(id); err == nil {
			out = append(out, info)
		}
	}
	return out
}

// siteCertInfo : domaine, émetteur, validité et empreinte d'un certificat.
// siteCertInfo: domains, issuer, validity and fingerprint of a certificate.
func siteCertInfo(id string) (J, error) {
	if !siteCertIDRe.MatchString(id) {
		return nil, langErr("identifiant refusé", "id refused")
	}
	crtPath, keyPath := siteCertPaths(id)
	raw, err := os.ReadFile(crtPath)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(raw)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, langErr("certificat PEM illisible", "unreadable PEM certificate")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, err
	}
	names := append([]string{}, cert.DNSNames...)
	if cn := cert.Subject.CommonName; cn != "" && !slices.Contains(names, cn) {
		names = append(names, cn)
	}
	sort.Strings(names)
	sum := sha256.Sum256(cert.Raw)
	meta := siteCertMetaOf(id)
	return J{
		"id": id, "crt_path": crtPath, "key_path": keyPath,
		"domains": names, "subject": cert.Subject.String(), "issuer": cert.Issuer.String(),
		"not_before":      cert.NotBefore.UTC().Format(time.RFC3339),
		"not_after":       cert.NotAfter.UTC().Format(time.RFC3339),
		"expires_in_days": int(time.Until(cert.NotAfter).Hours() / 24),
		"expired":         time.Now().After(cert.NotAfter),
		"fingerprint":     fingerprintHex(sum[:]),
		"managed":         meta.Provider != "",
		"provider":        meta.Provider,
	}, nil
}

// validateSiteCert : la paire se charge, la clé correspond au certificat, et le
// certificat est bien celui d'un site. Un certificat expiré est accepté : la
// liste le signale, l'import reste possible.
// validateSiteCert: the pair loads, the key matches the certificate, and the
// certificate is a site certificate. An expired certificate is accepted: the
// list flags it, the import stays possible.
func validateSiteCert(certPEM, keyPEM []byte) (*x509.Certificate, error) {
	if len(certPEM) == 0 || len(keyPEM) == 0 {
		return nil, langErr("certificat et clé privée requis", "certificate and private key required")
	}
	if len(certPEM) > maxSiteCertSize || len(keyPEM) > maxSiteCertSize {
		return nil, langErr("fichier trop volumineux (64 Ko maximum)", "file too large (64 KB maximum)")
	}
	if _, err := tls.X509KeyPair(certPEM, keyPEM); err != nil {
		return nil, err
	}
	block, _ := pem.Decode(certPEM)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, langErr("certificat PEM illisible", "unreadable PEM certificate")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, err
	}
	if cert.BasicConstraintsValid && cert.IsCA {
		return nil, langErr("c'est un certificat d'autorité : fournissez le certificat du site",
			"this is a certificate authority: supply the site certificate")
	}
	return cert, nil
}

// siteCertID : identifiant lisible tiré d'un nom de domaine ou d'un nom donné.
// siteCertID: readable id derived from a domain name or a given name.
func siteCertID(name string) string {
	s := strings.ToLower(strings.TrimSpace(name))
	s = strings.TrimPrefix(s, "*.")
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	id := strings.Trim(b.String(), "-._")
	if len(id) > 63 {
		id = strings.Trim(id[:63], "-._")
	}
	if !siteCertIDRe.MatchString(id) {
		return ""
	}
	return id
}

// saveSiteCert : enregistre une paire importée sous un identifiant libre, tiré
// du nom donné, sinon du nom courant, sinon du premier domaine.
// saveSiteCert: stores an imported pair under a free id, taken from the given
// name, otherwise the common name, otherwise the first domain.
func saveSiteCert(name string, certPEM, keyPEM []byte, cert *x509.Certificate) (string, error) {
	id := siteCertID(name)
	if id == "" {
		id = siteCertID(cert.Subject.CommonName)
	}
	if id == "" && len(cert.DNSNames) > 0 {
		id = siteCertID(cert.DNSNames[0])
	}
	if id == "" {
		return "", langErr("nom de certificat introuvable : donnez un nom", "no certificate name found: supply a name")
	}
	base := id
	crtPath, _ := siteCertPaths(id)
	for n := 2; fileExists(crtPath); n++ {
		id = fmt.Sprintf("%s-%d", base, n)
		crtPath, _ = siteCertPaths(id)
	}
	if err := writeSiteCertPair(id, certPEM, keyPEM); err != nil {
		return "", err
	}
	return id, nil
}

// writeSiteCertPair : écrit la paire sous son identifiant, en remplaçant celle
// qui existait (renouvellement) ; clé lisible par root seulement.
// writeSiteCertPair: writes the pair under its id, replacing the existing one
// (renewal); key readable by root only.
func writeSiteCertPair(id string, certPEM, keyPEM []byte) error {
	if !siteCertIDRe.MatchString(id) {
		return langErr("identifiant refusé", "id refused")
	}
	if err := os.MkdirAll(siteCertDir, 0o700); err != nil {
		return err
	}
	// MkdirAll ne restreint pas un dossier existant.
	// MkdirAll does not restrict an existing directory.
	if err := os.Chmod(siteCertDir, 0o700); err != nil {
		return err
	}
	crtPath, keyPath := siteCertPaths(id)
	if err := writeFileAtomic(keyPath, keyPEM, 0o600); err != nil {
		return err
	}
	if err := writeFileAtomic(crtPath, certPEM, 0o644); err != nil {
		os.Remove(keyPath)
		return err
	}
	return nil
}

// deleteSiteCert : supprime un certificat, sauf s'il est déclaré dans la config
// d'un frpc (le tunnel ne démarrerait plus). Renvoie les instances qui l'utilisent.
// deleteSiteCert: removes a certificate, unless it is declared in an frpc config
// (the tunnel would not start). Returns the instances using it.
func deleteSiteCert(id string) ([]string, error) {
	if !siteCertIDRe.MatchString(id) {
		return nil, langErr("identifiant refusé", "id refused")
	}
	if inUse := siteCertUsage(id); len(inUse) > 0 {
		return inUse, langErr("certificat utilisé", "certificate in use")
	}
	crtPath, keyPath := siteCertPaths(id)
	if !fileExists(crtPath) && !fileExists(keyPath) {
		return nil, langErr("certificat introuvable", "certificate not found")
	}
	if err := os.Remove(crtPath); err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	if err := os.Remove(keyPath); err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	os.Remove(siteCertMetaPath(id))
	return nil, nil
}

// siteCertExists : une paire .crt/.key existe pour cet identifiant.
// siteCertExists: a .crt/.key pair exists for this id.
func siteCertExists(id string) bool {
	if !siteCertIDRe.MatchString(id) {
		return false
	}
	crt, key := siteCertPaths(id)
	return fileExists(crt) && fileExists(key)
}

// httpsProxyCert : réglage « ports HTTPS en reverse proxy avec certificats » :
// le certificat et le mode choisis une fois dans les réglages, repris par défaut
// par les tunnels HTTPS (plugin https2http / https2https de frpc).
// httpsProxyCert: "HTTPS ports as a reverse proxy with certificates" setting:
// the certificate and mode chosen once in the settings, reused by default by
// HTTPS tunnels (frpc https2http / https2https plugin).
func httpsProxyCert() J {
	mode := str(cfg(), "https_proxy_cert_mode", "https2http")
	if mode != "https2https" {
		mode = "https2http"
	}
	enabled := boolv(cfg(), "https_proxy_cert", false)
	id := str(cfg(), "https_proxy_cert_id", "")
	out := J{"enabled": enabled, "mode": mode, "cert_id": id, "ready": false}
	if siteCertExists(id) {
		if info, err := siteCertInfo(id); err == nil {
			out["crt_path"] = info["crt_path"]
			out["key_path"] = info["key_path"]
			out["domains"] = info["domains"]
			out["ready"] = enabled
		}
	}
	return out
}

// siteCertUsage : instances frpc dont la config cite ce certificat.
// siteCertUsage: frpc instances whose config mentions this certificate.
func siteCertUsage(id string) []string {
	crtPath, _ := siteCertPaths(id)
	out := []string{}
	for _, inst := range instancesSnapshot() {
		if inst.Type != "frpc" {
			continue
		}
		if content, ok := frpsConfigText(inst); ok && strings.Contains(content, crtPath) {
			out = append(out, inst.ID)
		}
	}
	sort.Strings(out)
	return out
}

// writeFileAtomic : fichier temporaire dans le même dossier puis renommage.
// writeFileAtomic: temporary file in the same folder, then rename.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, perm); err != nil {
		return err
	}
	if err := os.Chmod(tmp, perm); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// fingerprintHex : empreinte SHA-256 en majuscules séparées par « : ».
// fingerprintHex: SHA-256 fingerprint, uppercase and separated by ":".
func fingerprintHex(sum []byte) string {
	parts := make([]string, len(sum))
	for i, b := range sum {
		parts[i] = fmt.Sprintf("%02X", b)
	}
	return strings.Join(parts, ":")
}
