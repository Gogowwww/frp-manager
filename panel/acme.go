// Certificats Let's Encrypt (ACME DNS-01) : obtention pour les sites exposés
// par frpc et renouvellement automatique. La validation passe par un
// enregistrement TXT temporaire chez Cloudflare : frps ne termine pas le TLS
// des sites, il n'y a donc ni HTTP-01 ni TLS-ALPN-01 possible côté serveur.
// Let's Encrypt certificates (ACME DNS-01): issuance for sites exposed by frpc
// and automatic renewal. Validation goes through a temporary TXT record at
// Cloudflare: frps does not terminate site TLS, so neither HTTP-01 nor
// TLS-ALPN-01 is possible on the server side.

package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/acme"
)

// Réglages ACME gardés dans la configuration du panel (root seulement).
// ACME settings kept in the panel configuration (root only).
type acmeSettings struct {
	Provider string
	Token    string
	Email    string
	Staging  bool
}

const (
	acmeDirectoryProd    = "https://acme-v02.api.letsencrypt.org/directory"
	acmeDirectoryStaging = "https://acme-staging-v02.api.letsencrypt.org/directory"
)

var (
	// Remplaçables par les tests (aucun appel réseau réel).
	// Replaced by tests (no real network call).
	cfAPIBase = "https://api.cloudflare.com/client/v4"
	dohURL    = "https://cloudflare-dns.com/dns-query"
)

func acmeCfg() acmeSettings {
	m := mapv(cfg(), "acme")
	return acmeSettings{
		Provider: str(m, "provider", ""),
		Token:    str(m, "token", ""),
		Email:    str(m, "email", ""),
		Staging:  boolv(m, "staging", false),
	}
}

func saveACMECfg(s acmeSettings) error {
	patch := J{"acme": J{"provider": s.Provider, "token": s.Token, "email": s.Email, "staging": s.Staging}}
	_, err := cfgUpdate(patch)
	return err
}

// acmeDirectory : adresse de l'API ACME, production ou bac à sable.
// acmeDirectory: ACME API address, production or staging.
func acmeDirectory(staging bool) string {
	if staging {
		return acmeDirectoryStaging
	}
	return acmeDirectoryProd
}

// acmeClient : client ACME avec le compte créé ou retrouvé, et l'adresse de
// contact configurée quand elle existe.
// acmeClient: ACME client with the account created or found, and the configured
// contact address when it exists.
func acmeClient(ctx context.Context, staging bool, email string) (*acme.Client, error) {
	accountKey, err := acmeAccountKey()
	if err != nil {
		return nil, err
	}
	client := &acme.Client{Key: accountKey, DirectoryURL: acmeDirectory(staging)}
	acct := &acme.Account{}
	if email != "" {
		acct.Contact = []string{"mailto:" + email}
	}
	if _, err := client.Register(ctx, acct, acme.AcceptTOS); err != nil && !errors.Is(err, acme.ErrAccountAlreadyExists) {
		return nil, err
	}
	return client, nil
}

func init() {
	route("POST /api/manager/certs/acme", "api_manager_certs_acme", perm(P("settings.manage"), func(c *Ctx) {
		domains, err := acmeDomains(c.Body())
		if err != nil {
			c.Fail(http.StatusBadRequest, errIn(err, c.Lang))
			return
		}
		token := strings.TrimSpace(str(c.Body(), "token", ""))
		if token == "" {
			c.Fail(http.StatusBadRequest, c.M("Jeton API Cloudflare requis.", "Cloudflare API token required."))
			return
		}
		s := acmeSettings{Provider: "cloudflare", Token: token, Email: strings.TrimSpace(str(c.Body(), "email", "")),
			Staging: truthy(c.Body()["staging"])}
		ctx, cancel := context.WithTimeout(c.R.Context(), 5*time.Minute)
		defer cancel()
		id, err := issueSiteCert(ctx, "", domains, s)
		if err != nil {
			c.Fail(http.StatusBadGateway, c.M("Certificat non obtenu : ", "Certificate not issued: ")+err.Error())
			return
		}
		// Le jeton n'est gardé qu'une fois qu'il a servi : pas de secret inutile
		// en configuration s'il est faux.
		// The token is only kept once it worked: no useless secret in the
		// configuration when it is wrong.
		if err := saveACMECfg(s); err != nil {
			log.Printf("[WARN] %s", systemLang().M(fmt.Sprintf("Réglages ACME non enregistrés : %v", err),
				fmt.Sprintf("ACME settings not saved: %v", err)))
		}
		info, _ := siteCertInfo(id)
		c.JSON(J{"ok": true, "id": id, "certificate": info,
			"msg": c.M("Certificat obtenu et enregistré.", "Certificate issued and saved.")})
	}))

	// Sans jeton Cloudflare : le panel affiche les enregistrements TXT et
	// l'administrateur les crée chez son fournisseur DNS.
	// Without a Cloudflare token: the panel shows the TXT records and the
	// administrator creates them at their DNS provider.
	route("POST /api/manager/certs/acme/manual", "api_manager_certs_acme_manual", perm(P("settings.manage"), func(c *Ctx) {
		domains, err := acmeDomains(c.Body())
		if err != nil {
			c.Fail(http.StatusBadRequest, errIn(err, c.Lang))
			return
		}
		ctx, cancel := context.WithTimeout(c.R.Context(), time.Minute)
		defer cancel()
		id, records, err := startManualOrder(ctx, domains, truthy(c.Body()["staging"]))
		if err != nil {
			c.Fail(http.StatusBadGateway, c.M("Demande ACME impossible : ", "ACME request failed: ")+err.Error())
			return
		}
		c.JSON(J{"ok": true, "id": id, "domains": domains, "records": records,
			"msg": c.M("Créez ces enregistrements TXT, puis lancez la validation.",
				"Create these TXT records, then run the validation.")})
	}))

	route("POST /api/manager/certs/acme/manual/{id}", "api_manager_certs_acme_manual_finish", perm(P("settings.manage"), func(c *Ctx) {
		ctx, cancel := context.WithTimeout(c.R.Context(), 10*time.Minute)
		defer cancel()
		id, err := finishManualOrder(ctx, c.Param("id"))
		if err != nil {
			c.Fail(http.StatusBadGateway, c.M("Certificat non obtenu : ", "Certificate not issued: ")+errIn(err, c.Lang))
			return
		}
		info, _ := siteCertInfo(id)
		c.JSON(J{"ok": true, "id": id, "certificate": info,
			"msg": c.M("Certificat obtenu et enregistré.", "Certificate issued and saved.")})
	}))
}

// acmeDomains : domaines demandés (« a.example.com, *.example.com » ou liste).
// acmeDomains: requested domains ("a.example.com, *.example.com" or a list).
func acmeDomains(body map[string]any) ([]string, error) {
	var raw []string
	switch v := body["domains"].(type) {
	case string:
		raw = strings.Split(v, ",")
	case []any:
		for _, item := range v {
			raw = append(raw, pyStr(item))
		}
	}
	out := []string{}
	seen := map[string]bool{}
	for _, d := range raw {
		d = strings.ToLower(strings.TrimSpace(d))
		if d == "" || seen[d] {
			continue
		}
		if !validSiteDomain(d) {
			return nil, langErr("domaine refusé : "+d, "domain refused: "+d)
		}
		seen[d] = true
		out = append(out, d)
	}
	if len(out) == 0 {
		return nil, langErr("indiquez au moins un domaine", "give at least one domain")
	}
	return out, nil
}

// validSiteDomain : nom d'hôte, joker « *. » accepté.
// validSiteDomain: host name, "*." wildcard accepted.
func validSiteDomain(d string) bool {
	d = strings.TrimPrefix(d, "*.")
	if d == "" || len(d) > 253 || !strings.Contains(d, ".") {
		return false
	}
	for _, label := range strings.Split(d, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, r := range label {
			if !(r == '-' || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')) {
				return false
			}
		}
	}
	return true
}

// acmeAccountKey : clé du compte ACME, créée au premier usage et gardée à côté
// des certificats.
// acmeAccountKey: ACME account key, created on first use and kept next to the
// certificates.
func acmeAccountKey() (*ecdsa.PrivateKey, error) {
	path := filepath.Join(siteCertDir, "acme-account.key")
	if b, err := os.ReadFile(path); err == nil {
		if block, _ := pem.Decode(b); block != nil {
			if key, err := x509.ParseECPrivateKey(block.Bytes); err == nil {
				return key, nil
			}
		}
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(siteCertDir, 0o700); err != nil {
		return nil, err
	}
	if err := writeFileAtomic(path, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}), 0o600); err != nil {
		return nil, err
	}
	return key, nil
}

// issueSiteCert : obtient un certificat pour ces domaines. id vide : le nom du
// premier domaine, avec un suffixe si un certificat de ce nom existe déjà ; id
// donné : renouvellement, la paire est remplacée.
// issueSiteCert: obtains a certificate for these domains. Empty id: the first
// domain name, with a suffix when a certificate of that name exists; given id:
// renewal, the pair is replaced.
func issueSiteCert(ctx context.Context, id string, domains []string, s acmeSettings) (string, error) {
	client, err := acmeClient(ctx, s.Staging, s.Email)
	if err != nil {
		return "", err
	}
	ids := make([]acme.AuthzID, len(domains))
	for i, d := range domains {
		ids[i] = acme.AuthzID{Type: "dns", Value: d}
	}
	order, err := client.AuthorizeOrder(ctx, ids)
	if err != nil {
		return "", err
	}

	cf := cfClient{token: s.Token, base: cfAPIBase}
	// Enregistrements TXT créés, retirés au retour même en cas d'échec.
	// Created TXT records, removed on return even on failure.
	type record struct{ zone, id string }
	var created []record
	defer func() {
		for _, rec := range created {
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
			_ = cf.deleteTXT(cleanup, rec.zone, rec.id)
			cancel()
		}
	}()

	for _, authzURL := range order.AuthzURLs {
		authz, err := client.GetAuthorization(ctx, authzURL)
		if err != nil {
			return "", err
		}
		if authz.Status == acme.StatusValid {
			continue
		}
		chal := challengeOfType(authz, "dns-01")
		if chal == nil {
			return "", fmt.Errorf("validation dns-01 indisponible pour %s", authz.Identifier.Value)
		}
		value, err := client.DNS01ChallengeRecord(chal.Token)
		if err != nil {
			return "", err
		}
		name := txtRecordName(authz.Identifier.Value)
		zone, err := cf.zoneFor(ctx, name)
		if err != nil {
			return "", err
		}
		recID, err := cf.createTXT(ctx, zone, name, value)
		if err != nil {
			return "", err
		}
		created = append(created, record{zone: zone, id: recID})
		if err := waitTXT(ctx, name, value, 2*time.Minute); err != nil {
			return "", err
		}
		if _, err := client.Accept(ctx, chal); err != nil {
			return "", err
		}
	}
	for _, authzURL := range order.AuthzURLs {
		if _, err := client.WaitAuthorization(ctx, authzURL); err != nil {
			return "", err
		}
	}

	chain, keyPEM, err := orderCertificate(ctx, client, order, domains)
	if err != nil {
		return "", err
	}
	return saveIssuedCert(id, domains, chain, keyPEM, siteCertMeta{Domains: domains, Provider: "cloudflare",
		Staging: s.Staging, Issued: time.Now().UTC()})
}

// orderCertificate : clé du site, CSR et certificat final d'un ordre validé.
// orderCertificate: site key, CSR and final certificate of a validated order.
func orderCertificate(ctx context.Context, client *acme.Client, order *acme.Order, domains []string) (certPEM, keyPEM []byte, err error) {
	siteKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	csr, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{DNSNames: domains}, siteKey)
	if err != nil {
		return nil, nil, err
	}
	ders, _, err := client.CreateOrderCert(ctx, order.FinalizeURL, csr, true)
	if err != nil {
		return nil, nil, err
	}
	var chain bytes.Buffer
	for _, der := range ders {
		if err := pem.Encode(&chain, &pem.Block{Type: "CERTIFICATE", Bytes: der}); err != nil {
			return nil, nil, err
		}
	}
	keyDER, err := x509.MarshalECPrivateKey(siteKey)
	if err != nil {
		return nil, nil, err
	}
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	if _, err := validateSiteCert(chain.Bytes(), keyPEM); err != nil {
		return nil, nil, err
	}
	return chain.Bytes(), keyPEM, nil
}

// saveIssuedCert : écrit la paire et ses métadonnées sous un identifiant libre.
// saveIssuedCert: writes the pair and its metadata under a free id.
func saveIssuedCert(id string, domains []string, certPEM, keyPEM []byte, meta siteCertMeta) (string, error) {
	if id == "" {
		id = siteCertID(strings.TrimPrefix(domains[0], "*."))
		if id == "" {
			id = "site"
		}
		base := id
		crtPath, _ := siteCertPaths(id)
		for n := 2; fileExists(crtPath); n++ {
			id = fmt.Sprintf("%s-%d", base, n)
			crtPath, _ = siteCertPaths(id)
		}
	}
	if err := writeSiteCertPair(id, certPEM, keyPEM); err != nil {
		return "", err
	}
	if err := writeSiteCertMeta(id, meta); err != nil {
		return "", err
	}
	return id, nil
}

// ── Validation DNS-01 manuelle ──────────────────────────────────────────────
// Sans jeton d'API, l'administrateur crée lui-même les enregistrements TXT chez
// son fournisseur DNS : le panel les affiche, attend leur propagation, puis
// valide l'ordre.
// Manual DNS-01 validation: without an API token, the administrator creates the
// TXT records at their DNS provider themselves: the panel shows them, waits for
// propagation, then validates the order.

type manualAuthz struct {
	url     string
	txtName string
	txt     string
}

type manualOrder struct {
	client  *acme.Client
	order   *acme.Order
	domains []string
	staging bool
	authz   []manualAuthz
	created time.Time
}

var manualOrders = struct {
	sync.Mutex
	m map[string]*manualOrder
}{m: map[string]*manualOrder{}}

// manualOrderTTL : délai laissé pour créer les enregistrements et valider.
// manualOrderTTL: time left to create the records and validate.
const manualOrderTTL = 30 * time.Minute

// startManualOrder : prépare un ordre DNS-01 et renvoie les enregistrements TXT
// à créer. Rien n'est écrit chez le fournisseur DNS.
// startManualOrder: prepares a DNS-01 order and returns the TXT records to
// create. Nothing is written at the DNS provider.
func startManualOrder(ctx context.Context, domains []string, staging bool) (string, []J, error) {
	client, err := acmeClient(ctx, staging, acmeCfg().Email)
	if err != nil {
		return "", nil, err
	}
	ids := make([]acme.AuthzID, len(domains))
	for i, d := range domains {
		ids[i] = acme.AuthzID{Type: "dns", Value: d}
	}
	order, err := client.AuthorizeOrder(ctx, ids)
	if err != nil {
		return "", nil, err
	}
	records := []J{}
	authzList := []manualAuthz{}
	for _, authzURL := range order.AuthzURLs {
		authz, err := client.GetAuthorization(ctx, authzURL)
		if err != nil {
			return "", nil, err
		}
		if authz.Status == acme.StatusValid {
			continue
		}
		chal := challengeOfType(authz, "dns-01")
		if chal == nil {
			return "", nil, fmt.Errorf("validation dns-01 indisponible pour %s", authz.Identifier.Value)
		}
		value, err := client.DNS01ChallengeRecord(chal.Token)
		if err != nil {
			return "", nil, err
		}
		name := txtRecordName(authz.Identifier.Value)
		records = append(records, J{"domain": authz.Identifier.Value, "name": name, "value": value})
		authzList = append(authzList, manualAuthz{url: authzURL, txtName: name, txt: value})
	}
	id := tokenHex(16)
	manualOrders.Lock()
	for key, o := range manualOrders.m {
		if time.Since(o.created) > manualOrderTTL {
			delete(manualOrders.m, key)
		}
	}
	manualOrders.m[id] = &manualOrder{client: client, order: order, domains: domains,
		staging: staging, authz: authzList, created: time.Now()}
	manualOrders.Unlock()
	return id, records, nil
}

// finishManualOrder : vérifie la propagation, valide l'ordre et enregistre le
// certificat. L'ordre reste disponible en cas d'échec de propagation, pour
// réessayer sans recréer les enregistrements.
// finishManualOrder: checks propagation, validates the order and saves the
// certificate. The order stays available when propagation failed, to retry
// without creating the records again.
func finishManualOrder(ctx context.Context, id string) (string, error) {
	manualOrders.Lock()
	o, ok := manualOrders.m[id]
	manualOrders.Unlock()
	if !ok || time.Since(o.created) > manualOrderTTL {
		return "", langErr("demande expirée : recommencez", "request expired: start again")
	}
	for _, a := range o.authz {
		if err := waitTXT(ctx, a.txtName, a.txt, 2*time.Minute); err != nil {
			return "", err
		}
	}
	for _, a := range o.authz {
		authz, err := o.client.GetAuthorization(ctx, a.url)
		if err != nil {
			return "", err
		}
		switch authz.Status {
		case acme.StatusValid:
			continue
		case acme.StatusInvalid:
			manualOrders.Lock()
			delete(manualOrders.m, id)
			manualOrders.Unlock()
			return "", langErr("validation refusée par Let’s Encrypt : recommencez la demande",
				"validation refused by Let’s Encrypt: start the request again")
		}
		chal := challengeOfType(authz, "dns-01")
		if chal == nil {
			return "", fmt.Errorf("validation dns-01 indisponible pour %s", authz.Identifier.Value)
		}
		if _, err := o.client.Accept(ctx, chal); err != nil {
			return "", err
		}
	}
	for _, a := range o.authz {
		if _, err := o.client.WaitAuthorization(ctx, a.url); err != nil {
			return "", err
		}
	}
	chain, keyPEM, err := orderCertificate(ctx, o.client, o.order, o.domains)
	if err != nil {
		return "", err
	}
	certID, err := saveIssuedCert("", o.domains, chain, keyPEM, siteCertMeta{Domains: o.domains,
		Provider: "manual", Staging: o.staging, Issued: time.Now().UTC()})
	if err != nil {
		return "", err
	}
	manualOrders.Lock()
	delete(manualOrders.m, id)
	manualOrders.Unlock()
	return certID, nil
}

// challengeOfType : la validation ACME du type demandé.
// challengeOfType: the ACME challenge of the requested type.
func challengeOfType(authz *acme.Authorization, kind string) *acme.Challenge {
	for _, ch := range authz.Challenges {
		if ch.Type == kind {
			return ch
		}
	}
	return nil
}

// txtRecordName : nom de l'enregistrement TXT d'un domaine, joker compris.
// txtRecordName: TXT record name of a domain, wildcard included.
func txtRecordName(domain string) string {
	return "_acme-challenge." + strings.TrimPrefix(strings.TrimSuffix(domain, "."), "*.")
}

// ── Cloudflare (DNS-01) ────────────────────────────────────────────────────

type cfClient struct {
	token string
	base  string
}

type cfEnvelope struct {
	Success bool `json:"success"`
	Errors  []struct {
		Message string `json:"message"`
	} `json:"errors"`
	Result json.RawMessage `json:"result"`
}

func (c cfClient) call(ctx context.Context, method, path string, body any) (json.RawMessage, error) {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rdr)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var env cfEnvelope
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&env); err != nil {
		return nil, fmt.Errorf("cloudflare: HTTP %d", resp.StatusCode)
	}
	if !env.Success {
		msg := fmt.Sprintf("HTTP %d", resp.StatusCode)
		if len(env.Errors) > 0 && env.Errors[0].Message != "" {
			msg = env.Errors[0].Message
		}
		return nil, fmt.Errorf("cloudflare: %s", msg)
	}
	return env.Result, nil
}

// zoneFor : zone Cloudflare qui héberge ce nom, essayée du suffixe le plus
// court au plus long.
// zoneFor: Cloudflare zone hosting this name, tried from the shortest to the
// longest suffix.
func (c cfClient) zoneFor(ctx context.Context, name string) (string, error) {
	for _, zone := range zoneCandidates(name) {
		raw, err := c.call(ctx, "GET", "/zones?name="+url.QueryEscape(zone), nil)
		if err != nil {
			return "", err
		}
		var zones []struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(raw, &zones) == nil && len(zones) > 0 && zones[0].ID != "" {
			return zones[0].ID, nil
		}
	}
	return "", fmt.Errorf("zone Cloudflare introuvable pour %s", name)
}

// zoneCandidates : suffixes possibles d'un nom (« a.b.example.com » →
// example.com, b.example.com, a.b.example.com).
// zoneCandidates: possible suffixes of a name ("a.b.example.com" →
// example.com, b.example.com, a.b.example.com).
func zoneCandidates(name string) []string {
	labels := strings.Split(strings.TrimSuffix(name, "."), ".")
	out := []string{}
	for i := len(labels) - 2; i >= 0; i-- {
		out = append(out, strings.Join(labels[i:], "."))
	}
	return out
}

func (c cfClient) createTXT(ctx context.Context, zoneID, name, value string) (string, error) {
	raw, err := c.call(ctx, "POST", "/zones/"+zoneID+"/dns_records", map[string]any{
		"type": "TXT", "name": name, "content": value, "ttl": 60, "comment": "frp-manager ACME",
	})
	if err != nil {
		return "", err
	}
	var rec struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &rec); err != nil || rec.ID == "" {
		return "", fmt.Errorf("cloudflare: enregistrement TXT sans identifiant")
	}
	return rec.ID, nil
}

func (c cfClient) deleteTXT(ctx context.Context, zoneID, recordID string) error {
	_, err := c.call(ctx, "DELETE", "/zones/"+zoneID+"/dns_records/"+recordID, nil)
	return err
}

// waitTXT : attend que l'enregistrement TXT soit visible par un résolveur public.
// waitTXT: waits until the TXT record is visible from a public resolver.
func waitTXT(ctx context.Context, name, value string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		if txtPresent(ctx, name, value) {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("enregistrement TXT %s non propagé", name)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(3 * time.Second):
		}
	}
}

// txtPresent : la valeur est-elle dans les réponses TXT de ce nom (DNS over HTTPS) ?
// txtPresent: is the value among the TXT answers for this name (DNS over HTTPS)?
func txtPresent(ctx context.Context, name, value string) bool {
	req, err := http.NewRequestWithContext(ctx, "GET", dohURL+"?name="+url.QueryEscape(name)+"&type=TXT", nil)
	if err != nil {
		return false
	}
	req.Header.Set("Accept", "application/dns-json")
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	var data struct {
		Answer []struct {
			Data string `json:"data"`
		} `json:"Answer"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&data); err != nil {
		return false
	}
	for _, a := range data.Answer {
		if strings.Contains(a.Data, value) {
			return true
		}
	}
	return false
}

// siteCertRenewLoop : renouvelle les certificats Cloudflare 30 jours avant
// leur expiration, tant que le jeton reste configuré. Ceux obtenus par
// validation manuelle (provider « manual ») se renouvellent à la main.
// siteCertRenewLoop: renews Cloudflare certificates 30 days before expiry, as
// long as the token stays configured. Ones obtained through manual validation
// ("manual" provider) are renewed by hand.
func siteCertRenewLoop() {
	l := systemLang()
	time.Sleep(5 * time.Minute)
	for {
		s := acmeCfg()
		if s.Provider != "cloudflare" || s.Token == "" {
			time.Sleep(12 * time.Hour)
			continue
		}
		for _, id := range siteCertIDs() {
			meta := siteCertMetaOf(id)
			if meta.Provider != "cloudflare" || len(meta.Domains) == 0 {
				continue
			}
			info, err := siteCertInfo(id)
			if err != nil {
				continue
			}
			days, _ := info["expires_in_days"].(int)
			if days > 30 {
				continue
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			_, err = issueSiteCert(ctx, id, meta.Domains, s)
			cancel()
			if err != nil {
				log.Printf("[WARN] %s", l.M(fmt.Sprintf("Renouvellement du certificat %s impossible : %v", id, err),
					fmt.Sprintf("Could not renew certificate %s: %v", id, err)))
				continue
			}
			log.Printf("[INFO] %s", l.M("Certificat renouvelé : "+id, "Certificate renewed: "+id))
		}
		time.Sleep(12 * time.Hour)
	}
}
