// Passkeys (WebAuthn) : un compte local enregistre une ou plusieurs passkeys
// (Réglages → Mon compte, mot de passe redemandé), puis se connecte sans mot de
// passe. Les passkeys sont « découvrables » : la page de connexion ne demande
// pas l'identifiant, l'appareil propose ses passkeys pour ce site.
//
// Vérification faite ici, sans bibliothèque : attestation « none » (on ne
// vérifie pas le modèle de l'authentificateur, seulement la clé qu'il crée),
// vérification de l'utilisateur exigée (empreinte, visage, code PIN de
// l'appareil), clés ES256, EdDSA et RS256. Chaque passkey est liée au nom de
// domaine sur lequel elle a été créée (rp_id) : le navigateur ne la propose
// que sur ce domaine, et une signature faite pour un autre est refusée.
//
// Stockage : liste « passkeys » de la fiche du compte ; pour le propriétaire,
// clé owner_passkeys de la configuration (sa fiche n'est pas dans users).

package main

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	passkeysMax          = 20
	passkeyNameMax       = 64
	passkeyChallengeTTL  = 5 * time.Minute
	passkeyTimeoutMillis = 300000
	ownerPasskeysKey     = "owner_passkeys"
)

// ── CBOR (RFC 8949), juste ce qu'il faut pour WebAuthn ──────────────────────
// Entiers, chaînes d'octets et de texte, tableaux, tables, booléens et null, de
// longueur définie (les authentificateurs encodent en CBOR canonique).

type cborDecoder struct {
	b     []byte
	pos   int
	depth int
}

var errCBOR = errors.New("CBOR invalide")

// cborDecode : première valeur de b → (valeur, octets lus). Entiers en int64,
// tables en map[any]any (clés int64 ou string).
func cborDecode(b []byte) (any, int, error) {
	d := &cborDecoder{b: b}
	v, err := d.value()
	return v, d.pos, err
}

func (d *cborDecoder) uint(info byte) (uint64, error) {
	var n int
	switch {
	case info < 24:
		return uint64(info), nil
	case info == 24:
		n = 1
	case info == 25:
		n = 2
	case info == 26:
		n = 4
	case info == 27:
		n = 8
	default:
		return 0, errCBOR // longueur indéfinie ou valeur réservée
	}
	if len(d.b)-d.pos < n {
		return 0, errCBOR
	}
	var v uint64
	for _, c := range d.b[d.pos : d.pos+n] {
		v = v<<8 | uint64(c)
	}
	d.pos += n
	return v, nil
}

func (d *cborDecoder) value() (any, error) {
	if d.pos >= len(d.b) {
		return nil, errCBOR
	}
	d.depth++
	defer func() { d.depth-- }()
	if d.depth > 16 {
		return nil, errCBOR
	}
	head := d.b[d.pos]
	d.pos++
	major, info := head>>5, head&0x1f
	if major == 7 {
		switch info {
		case 20:
			return false, nil
		case 21:
			return true, nil
		case 22, 23:
			return nil, nil
		}
		return nil, errCBOR // flottants : jamais dans WebAuthn
	}
	n, err := d.uint(info)
	if err != nil {
		return nil, err
	}
	switch major {
	case 0:
		if n > 1<<63-1 {
			return nil, errCBOR
		}
		return int64(n), nil
	case 1:
		if n > 1<<63-1 {
			return nil, errCBOR
		}
		return -1 - int64(n), nil
	case 2, 3:
		if n > uint64(len(d.b)-d.pos) {
			return nil, errCBOR
		}
		s := d.b[d.pos : d.pos+int(n)]
		d.pos += int(n)
		if major == 3 {
			if !utf8.Valid(s) {
				return nil, errCBOR
			}
			return string(s), nil
		}
		return bytes.Clone(s), nil
	case 4:
		if n > uint64(len(d.b)-d.pos) { // au moins un octet par élément
			return nil, errCBOR
		}
		list := make([]any, 0, n)
		for i := uint64(0); i < n; i++ {
			v, err := d.value()
			if err != nil {
				return nil, err
			}
			list = append(list, v)
		}
		return list, nil
	case 5:
		if n > uint64(len(d.b)-d.pos)/2 {
			return nil, errCBOR
		}
		m := make(map[any]any, n)
		for i := uint64(0); i < n; i++ {
			k, err := d.value()
			if err != nil {
				return nil, err
			}
			switch k.(type) {
			case int64, string:
			default:
				return nil, errCBOR
			}
			if _, dup := m[k]; dup {
				return nil, errCBOR
			}
			v, err := d.value()
			if err != nil {
				return nil, err
			}
			m[k] = v
		}
		return m, nil
	case 6: // étiquette : la valeur étiquetée seule
		return d.value()
	}
	return nil, errCBOR
}

// ── Clés COSE (RFC 9053) ────────────────────────────────────────────────────

const (
	coseES256 = -7
	coseEdDSA = -8
	coseRS256 = -257
)

type coseKey struct {
	alg int64
	pub crypto.PublicKey
}

func parseCOSEKey(raw []byte) (*coseKey, error) {
	v, n, err := cborDecode(raw)
	if err != nil {
		return nil, err
	}
	m, ok := v.(map[any]any)
	if !ok || n != len(raw) {
		return nil, errCBOR
	}
	i64 := func(k int64) int64 { x, _ := m[k].(int64); return x }
	bs := func(k int64) []byte { x, _ := m[k].([]byte); return x }
	bad := errors.New("clé publique non prise en charge")
	alg := i64(3)
	switch alg {
	case coseES256:
		x, y := bs(-2), bs(-3)
		if i64(1) != 2 || i64(-1) != 1 || len(x) != 32 || len(y) != 32 {
			return nil, bad
		}
		pub, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), append(append([]byte{4}, x...), y...))
		if err != nil {
			return nil, bad
		}
		return &coseKey{alg, pub}, nil
	case coseEdDSA:
		x := bs(-2)
		if i64(1) != 1 || i64(-1) != 6 || len(x) != ed25519.PublicKeySize {
			return nil, bad
		}
		return &coseKey{alg, ed25519.PublicKey(bytes.Clone(x))}, nil
	case coseRS256:
		nb, eb := bs(-1), bs(-2)
		if i64(1) != 3 || len(nb) < 256 || len(nb) > 512 || len(eb) == 0 || len(eb) > 4 {
			return nil, bad
		}
		e := new(big.Int).SetBytes(eb)
		pub := &rsa.PublicKey{N: new(big.Int).SetBytes(nb), E: int(e.Int64())}
		if pub.N.BitLen() < 2048 || pub.E < 3 || pub.E%2 == 0 {
			return nil, bad
		}
		return &coseKey{alg, pub}, nil
	}
	return nil, bad
}

func (k *coseKey) verify(data, sig []byte) bool {
	switch pub := k.pub.(type) {
	case *ecdsa.PublicKey:
		h := sha256.Sum256(data)
		return ecdsa.VerifyASN1(pub, h[:], sig)
	case ed25519.PublicKey:
		return ed25519.Verify(pub, data, sig)
	case *rsa.PublicKey:
		h := sha256.Sum256(data)
		return rsa.VerifyPKCS1v15(pub, crypto.SHA256, h[:], sig) == nil
	}
	return false
}

// ── Données de l'authentificateur ───────────────────────────────────────────

const (
	flagUP = 1 << 0 // présence de l'utilisateur
	flagUV = 1 << 2 // utilisateur vérifié (biométrie, code PIN…)
	flagBE = 1 << 3 // passkey synchronisable
	flagBS = 1 << 4 // passkey synchronisée (sauvegardée)
	flagAT = 1 << 6 // clé créée jointe
	flagED = 1 << 7 // extensions jointes
)

type authData struct {
	rpIDHash  []byte
	flags     byte
	signCount uint32
	aaguid    []byte
	credID    []byte
	credKey   []byte
}

func parseAuthData(b []byte) (*authData, error) {
	bad := errors.New("données d'authentificateur invalides")
	if len(b) < 37 {
		return nil, bad
	}
	ad := &authData{rpIDHash: b[:32], flags: b[32], signCount: binary.BigEndian.Uint32(b[33:37])}
	rest := b[37:]
	if ad.flags&flagAT != 0 {
		if len(rest) < 18 {
			return nil, bad
		}
		ad.aaguid = rest[:16]
		l := int(binary.BigEndian.Uint16(rest[16:18]))
		rest = rest[18:]
		if l == 0 || l > 1023 || len(rest) < l {
			return nil, bad
		}
		ad.credID, rest = rest[:l], rest[l:]
		_, n, err := cborDecode(rest)
		if err != nil {
			return nil, bad
		}
		ad.credKey, rest = rest[:n], rest[n:]
	}
	if ad.flags&flagED != 0 {
		v, n, err := cborDecode(rest)
		if _, isMap := v.(map[any]any); err != nil || !isMap {
			return nil, bad
		}
		rest = rest[n:]
	}
	if len(rest) != 0 {
		return nil, bad
	}
	return ad, nil
}

// ── Données du client (navigateur) ──────────────────────────────────────────

type clientData struct {
	Type        string `json:"type"`
	Challenge   string `json:"challenge"`
	Origin      string `json:"origin"`
	CrossOrigin bool   `json:"crossOrigin"`
}

// passkeyOriginHost : nom de domaine d'une origine où WebAuthn est permis (https,
// ou http sur localhost), en minuscules ; "" sinon.
func passkeyOriginHost(origin string) string {
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" || u.Path != "" || u.User != nil {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	switch {
	case u.Scheme == "https":
	case u.Scheme == "http" && (host == "localhost" || strings.HasSuffix(host, ".localhost")):
	default:
		return ""
	}
	if host == "" || net.ParseIP(host) != nil {
		return "" // WebAuthn refuse les adresses IP
	}
	return host
}

func parseClientData(raw []byte, typ, challenge string) (*clientData, error) {
	var cd clientData
	if err := json.Unmarshal(raw, &cd); err != nil {
		return nil, errors.New("clientDataJSON illisible")
	}
	if cd.Type != typ {
		return nil, errors.New("type de cérémonie inattendu")
	}
	if challenge == "" || subtle.ConstantTimeCompare([]byte(cd.Challenge), []byte(challenge)) != 1 {
		return nil, errors.New("défi inconnu ou expiré")
	}
	if cd.CrossOrigin {
		return nil, errors.New("demande venue d'un cadre d'un autre site")
	}
	return &cd, nil
}

// ── Stockage ────────────────────────────────────────────────────────────────

// userPasskeys : passkeys enregistrées d'un compte (fiches de la configuration,
// à ne pas modifier).
func userPasskeys(u User) []map[string]any {
	var raw any
	if u["id"] == ownerID {
		raw = cfg()[ownerPasskeysKey]
	} else {
		raw = u["passkeys"]
	}
	list, _ := raw.([]any)
	out := make([]map[string]any, 0, len(list))
	for _, v := range list {
		if pk, ok := v.(map[string]any); ok && str(pk, "id", "") != "" {
			out = append(out, pk)
		}
	}
	return out
}

func passkeyByID(u User, id string) map[string]any {
	for _, pk := range userPasskeys(u) {
		if subtle.ConstantTimeCompare([]byte(str(pk, "id", "")), []byte(id)) == 1 {
			return pk
		}
	}
	return nil
}

// passkeysUpdate applique mutate sur une copie des passkeys du compte uid puis
// enregistre ; mutate renvoie la nouvelle liste.
func passkeysUpdate(uid string, mutate func([]any) []any) error {
	cfgMu.Lock()
	defer cfgMu.Unlock()
	return passkeysUpdateLocked(uid, mutate)
}

func passkeysUpdateLocked(uid string, mutate func([]any) []any) error {
	if uid == ownerID {
		list, _ := deepCopy(cfg()[ownerPasskeysKey]).([]any)
		_, err := cfgUpdateLocked(Cfg{ownerPasskeysKey: mutate(list)})
		return err
	}
	return usersUpdateLocked(func(users map[string]any) {
		if u, ok := users[uid].(map[string]any); ok {
			list, _ := u["passkeys"].([]any)
			u["passkeys"] = mutate(list)
		}
	})
}

// anyPasskey : au moins une passkey sur un compte qui peut s'en servir (la page
// de connexion ne propose le bouton que dans ce cas).
func anyPasskey() bool {
	for _, u := range allUsers() {
		if u["source"] == "local" && boolv(u, "enabled", true) && len(userPasskeys(u)) > 0 {
			return true
		}
	}
	return false
}

func passkeyView(pk map[string]any) J {
	return J{"id": str(pk, "id", ""), "name": str(pk, "name", ""), "created": pk["created"],
		"last_used": pk["last_used"], "synced": boolv(pk, "backup_state", false), "rp_id": str(pk, "rp_id", "")}
}

// Fournisseurs de passkeys les plus courants, d'après l'AAGUID de
// l'authentificateur : nom proposé par défaut (modifiable).
var passkeyProviders = map[string]string{
	"ea9b8d664d011d213ce4b6b48cb575d4": "Google Password Manager",
	"adce000235bcc60a648b0b25f1f05503": "Chrome (macOS)",
	"fbfc3007154e4ecc8c0b6e020557d7bd": "iCloud Keychain",
	"dd4ec289e01d41c9bb8970fa845d4bf2": "iCloud Keychain",
	"08987058cadc4b81b6e130de50dcbe96": "Windows Hello",
	"9ddd1817af5a4672a2b93e3dd95000a9": "Windows Hello",
	"6028b017b1d44c02b4b3afcdafc96bb2": "Windows Hello",
	"bada5566a7aa401fbd9645619a55120d": "1Password",
	"d548826e79b4db40a3d811116f7e8349": "Bitwarden",
	"531126d6e717415c93203d9aa6981239": "Dashlane",
	"fdb141b25d84443e8a354698c205a502": "KeePassXC",
	"50726f746f6e5061737350726f746f6e": "Proton Pass",
	"53414d53554e47000000000000000000": "Samsung Pass",
}

// cleanPasskeyName : nom saisi, sans caractère de contrôle ; "" s'il est vide.
func cleanPasskeyName(raw any) (string, bool) {
	if raw == nil {
		return "", true
	}
	name := strings.TrimSpace(pyStr(raw))
	if utf8.RuneCountInString(name) > passkeyNameMax || strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return "", false
	}
	return name, true
}

// ── Défis (gardés dans la session) ──────────────────────────────────────────

// passkeyChallenge : nouveau défi pour la cérémonie purpose (« register:<uid> »
// ou « login »), qui remplace le précédent.
func (c *Ctx) passkeyChallenge(purpose string) string {
	chal := tokenURLSafe(32)
	c.sess.set("pk_chal", chal)
	c.sess.set("pk_for", purpose)
	c.sess.set("pk_exp", strconv.FormatInt(time.Now().Add(passkeyChallengeTTL).Unix(), 10))
	return chal
}

// takePasskeyChallenge : défi en cours pour purpose (usage unique), ou "".
func (c *Ctx) takePasskeyChallenge(purpose string) string {
	chal, forWhat, exp := c.sess.get("pk_chal"), c.sess.get("pk_for"), c.sess.get("pk_exp")
	for _, k := range []string{"pk_chal", "pk_for", "pk_exp"} {
		c.sess.del(k)
	}
	until, _ := strconv.ParseInt(exp, 10, 64)
	if chal == "" || forWhat != purpose || time.Now().Unix() > until {
		return ""
	}
	return chal
}

// ── Cérémonies ──────────────────────────────────────────────────────────────

func b64field(m map[string]any, k string, max int) ([]byte, bool) {
	s, ok := m[k].(string)
	if !ok || len(s) > max*4/3+4 {
		return nil, false
	}
	b, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(s, "="))
	return b, err == nil
}

// passkeyRegistration : vérifie la réponse de navigator.credentials.create() →
// fiche de la passkey à enregistrer.
func (c *Ctx) passkeyRegistration(cred map[string]any, challenge string) (map[string]any, error) {
	resp, _ := cred["response"].(map[string]any)
	cdRaw, ok1 := b64field(resp, "clientDataJSON", 4096)
	attRaw, ok2 := b64field(resp, "attestationObject", 16384)
	if !ok1 || !ok2 {
		return nil, errors.New("réponse incomplète")
	}
	cd, err := parseClientData(cdRaw, "webauthn.create", challenge)
	if err != nil {
		return nil, err
	}
	rpID := passkeyOriginHost(cd.Origin)
	if rpID == "" {
		return nil, errors.New("origine refusée : " + cd.Origin)
	}
	if !c.sameHost(cd.Origin) {
		return nil, errors.New("origine différente de l'adresse du panel : " + cd.Origin)
	}
	v, n, err := cborDecode(attRaw)
	att, isMap := v.(map[any]any)
	if err != nil || !isMap || n != len(attRaw) {
		return nil, errors.New("attestationObject illisible")
	}
	adRaw, _ := att["authData"].([]byte)
	ad, err := parseAuthData(adRaw)
	if err != nil {
		return nil, err
	}
	want := sha256.Sum256([]byte(rpID))
	if subtle.ConstantTimeCompare(ad.rpIDHash, want[:]) != 1 {
		return nil, errors.New("passkey créée pour un autre domaine")
	}
	if ad.flags&flagUP == 0 || ad.flags&flagUV == 0 {
		return nil, errors.New("utilisateur non vérifié par l'appareil")
	}
	if ad.flags&flagAT == 0 {
		return nil, errors.New("aucune clé dans la réponse")
	}
	key, err := parseCOSEKey(ad.credKey)
	if err != nil {
		return nil, err
	}
	credID := base64.RawURLEncoding.EncodeToString(ad.credID)
	if id, _ := cred["id"].(string); id != credID {
		return nil, errors.New("identifiant de passkey incohérent")
	}
	var transports []any
	if list, ok := resp["transports"].([]any); ok && len(list) <= 8 {
		for _, t := range list {
			if s, ok := t.(string); ok && len(s) <= 32 {
				transports = append(transports, s)
			}
		}
	}
	return map[string]any{
		"id": credID, "public_key": base64.RawURLEncoding.EncodeToString(ad.credKey), "alg": float64(key.alg),
		"rp_id": rpID, "sign_count": float64(ad.signCount), "aaguid": hex.EncodeToString(ad.aaguid),
		"backup_eligible": ad.flags&flagBE != 0, "backup_state": ad.flags&flagBS != 0,
		"transports": transports, "created": nowISO(),
	}, nil
}

// sameHost : l'origine (schéma://hôte[:port]) désigne l'adresse par laquelle on
// joint le panel (Host, ou X-Forwarded-Host derrière un reverse proxy).
func (c *Ctx) sameHost(origin string) bool {
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false
	}
	host := strings.ToLower(u.Host)
	if strings.EqualFold(host, c.R.Host) {
		return true
	}
	fwd := strings.TrimSpace(strings.Split(c.R.Header.Get("X-Forwarded-Host"), ",")[0])
	return fwd != "" && strings.EqualFold(host, fwd)
}

// passkeyAssertion : vérifie la réponse de navigator.credentials.get() →
// (compte, passkey, nouveaux drapeaux et compteur).
func passkeyAssertion(cred map[string]any, challenge string) (User, map[string]any, *authData, error) {
	resp, _ := cred["response"].(map[string]any)
	cdRaw, ok1 := b64field(resp, "clientDataJSON", 4096)
	adRaw, ok2 := b64field(resp, "authenticatorData", 4096)
	sig, ok3 := b64field(resp, "signature", 1024)
	rawID, ok4 := b64field(cred, "rawId", 1023)
	if !ok1 || !ok2 || !ok3 || !ok4 || len(rawID) == 0 {
		return nil, nil, nil, errors.New("réponse incomplète")
	}
	cd, err := parseClientData(cdRaw, "webauthn.get", challenge)
	if err != nil {
		return nil, nil, nil, err
	}
	credID := base64.RawURLEncoding.EncodeToString(rawID)
	var user User
	var pk map[string]any
	for _, u := range allUsers() {
		if p := passkeyByID(u, credID); p != nil {
			user, pk = u, p
			break
		}
	}
	if pk == nil {
		return nil, nil, nil, errors.New("passkey inconnue")
	}
	if user["source"] != "local" || !boolv(user, "enabled", true) {
		return nil, nil, nil, errors.New("compte désactivé")
	}
	if uh, ok := resp["userHandle"].(string); ok && uh != "" {
		got, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(uh, "="))
		if err != nil || subtle.ConstantTimeCompare(got, []byte(str(user, "id", ""))) != 1 {
			return nil, nil, nil, errors.New("passkey d'un autre compte")
		}
	}
	rpID := str(pk, "rp_id", "")
	if host := passkeyOriginHost(cd.Origin); host == "" || host != rpID {
		return nil, nil, nil, errors.New("origine refusée : " + cd.Origin)
	}
	ad, err := parseAuthData(adRaw)
	if err != nil {
		return nil, nil, nil, err
	}
	want := sha256.Sum256([]byte(rpID))
	if subtle.ConstantTimeCompare(ad.rpIDHash, want[:]) != 1 {
		return nil, nil, nil, errors.New("signature faite pour un autre domaine")
	}
	if ad.flags&flagUP == 0 || ad.flags&flagUV == 0 {
		return nil, nil, nil, errors.New("utilisateur non vérifié par l'appareil")
	}
	pub, err := base64.RawURLEncoding.DecodeString(str(pk, "public_key", ""))
	if err != nil {
		return nil, nil, nil, err
	}
	key, err := parseCOSEKey(pub)
	if err != nil {
		return nil, nil, nil, err
	}
	cdHash := sha256.Sum256(cdRaw)
	if !key.verify(append(bytes.Clone(adRaw), cdHash[:]...), sig) {
		return nil, nil, nil, errors.New("signature invalide")
	}
	// Compteur : s'il est tenu, il doit avancer (sinon, authentificateur cloné)
	stored := uint32(num(pk, "sign_count", 0))
	if (stored != 0 || ad.signCount != 0) && ad.signCount <= stored {
		return nil, nil, nil, fmt.Errorf("compteur de signatures en arrière (%d ≤ %d) : passkey peut-être copiée", ad.signCount, stored)
	}
	return user, pk, ad, nil
}

// ── Routes ──────────────────────────────────────────────────────────────────

func (c *Ctx) passkeysLocalOnly(u User) bool {
	if u["source"] != "local" {
		c.Fail(http.StatusBadRequest, c.M("Ce compte se connecte par OpenID Connect : les passkeys se gèrent chez le fournisseur.",
			"This account signs in with OpenID Connect: passkeys are managed by the provider."))
		return false
	}
	return true
}

func passkeyList(u User) []J {
	list := userPasskeys(u)
	out := make([]J, len(list))
	for i, pk := range list {
		out[i] = passkeyView(pk)
	}
	return out
}

func init() {
	route("GET /api/me/passkeys", "api_me_passkeys", loginRequired(func(c *Ctx) {
		u := c.currentUser()
		c.JSON(J{"ok": true, "passkeys": passkeyList(u), "available": u["source"] == "local", "max": passkeysMax})
	}))

	// Options de création : le mot de passe du compte est redemandé (une session
	// volée ne doit pas pouvoir ajouter une passkey et garder l'accès).
	route("POST /api/me/passkeys/options", "api_me_passkeys_options", loginRequired(func(c *Ctx) {
		u := c.currentUser()
		if !c.passkeysLocalOnly(u) {
			return
		}
		ip := c.RemoteIP()
		if wait := loginRetryAfter(ip); wait > 0 {
			c.tooManyAttempts(wait)
			return
		}
		data := c.Body()
		if ok, _ := verifyPassword(truncRunes(pyStrDefault(data, "current_password"), passwordMaxLength), str(u, "password_hash", "")); !ok {
			loginFailed(ip)
			c.Fail(http.StatusForbidden, c.M("Mot de passe actuel incorrect.", "Wrong current password."))
			return
		}
		loginSucceeded(ip)
		existing := userPasskeys(u)
		if len(existing) >= passkeysMax {
			c.Fail(http.StatusBadRequest, c.M(fmt.Sprintf("%d passkeys au plus par compte.", passkeysMax),
				fmt.Sprintf("%d passkeys at most per account.", passkeysMax)))
			return
		}
		uid := str(u, "id", "")
		exclude := make([]J, len(existing))
		for i, pk := range existing {
			exclude[i] = J{"type": "public-key", "id": str(pk, "id", "")}
		}
		display := str(u, "display_name", "")
		if display == "" {
			display = str(u, "username", "")
		}
		c.JSON(J{"ok": true, "publicKey": J{
			"challenge": c.passkeyChallenge("register:" + uid),
			"rp":        J{"name": "FRP Manager"},
			"user":      J{"id": base64.RawURLEncoding.EncodeToString([]byte(uid)), "name": str(u, "username", ""), "displayName": display},
			"pubKeyCredParams": []J{{"type": "public-key", "alg": coseEdDSA}, {"type": "public-key", "alg": coseES256},
				{"type": "public-key", "alg": coseRS256}},
			"timeout":            passkeyTimeoutMillis,
			"attestation":        "none",
			"excludeCredentials": exclude,
			"authenticatorSelection": J{"residentKey": "required", "requireResidentKey": true,
				"userVerification": "required"},
		}})
	}))

	route("POST /api/me/passkeys", "api_me_passkeys_add", loginRequired(func(c *Ctx) {
		u := c.currentUser()
		if !c.passkeysLocalOnly(u) {
			return
		}
		uid := str(u, "id", "")
		data := c.Body()
		name, ok := cleanPasskeyName(data["name"])
		if !ok {
			c.Fail(http.StatusBadRequest, c.M(fmt.Sprintf("Nom invalide (%d caractères au plus).", passkeyNameMax),
				fmt.Sprintf("Invalid name (%d characters at most).", passkeyNameMax)))
			return
		}
		challenge := c.takePasskeyChallenge("register:" + uid)
		cred, _ := data["credential"].(map[string]any)
		if challenge == "" || cred == nil {
			c.Fail(http.StatusBadRequest, c.M("Demande expirée : recommencez.", "Request expired: try again."))
			return
		}
		pk, err := c.passkeyRegistration(cred, challenge)
		if err != nil {
			log.Printf("[WARN] Passkey refusée (%s) : %v", str(u, "username", ""), err)
			c.Fail(http.StatusBadRequest, c.M("Passkey refusée : ", "Passkey rejected: ")+err.Error())
			return
		}
		if name == "" {
			name = passkeyProviders[str(pk, "aaguid", "")]
		}
		if name == "" {
			name = "Passkey"
		}
		pk["name"] = name
		cfgMu.Lock()
		var problem string
		for _, other := range allUsers() {
			if passkeyByID(other, str(pk, "id", "")) != nil {
				problem = c.M("Cette passkey est déjà enregistrée.", "This passkey is already registered.")
			}
		}
		if problem == "" && len(userPasskeys(allUsers()[uid])) >= passkeysMax {
			problem = c.M(fmt.Sprintf("%d passkeys au plus par compte.", passkeysMax), fmt.Sprintf("%d passkeys at most per account.", passkeysMax))
		}
		var saveErr error
		if problem == "" {
			saveErr = passkeysUpdateLocked(uid, func(list []any) []any { return append(list, pk) })
		}
		cfgMu.Unlock()
		if problem != "" {
			c.Fail(http.StatusConflict, problem)
			return
		}
		if saveErr != nil {
			c.Fail(http.StatusInternalServerError, saveErr.Error())
			return
		}
		log.Printf("[INFO] Passkey « %s » ajoutée au compte %s", name, str(u, "username", ""))
		c.JSON(J{"ok": true, "passkey": passkeyView(pk), "msg": c.M("Passkey ajoutée.", "Passkey added.")})
	}))

	route("PUT /api/me/passkeys/{pid}", "api_me_passkeys_rename", loginRequired(func(c *Ctx) {
		u := c.currentUser()
		pid := c.Param("pid")
		if passkeyByID(u, pid) == nil {
			c.Fail(http.StatusNotFound, c.M("Passkey inconnue.", "Unknown passkey."))
			return
		}
		name, ok := cleanPasskeyName(c.Body()["name"])
		if !ok || name == "" {
			c.Fail(http.StatusBadRequest, c.M(fmt.Sprintf("Nom invalide (1 à %d caractères).", passkeyNameMax),
				fmt.Sprintf("Invalid name (1 to %d characters).", passkeyNameMax)))
			return
		}
		err := passkeysUpdate(str(u, "id", ""), func(list []any) []any {
			for _, v := range list {
				if pk, ok := v.(map[string]any); ok && pk["id"] == pid {
					pk["name"] = name
				}
			}
			return list
		})
		if err != nil {
			c.Fail(http.StatusInternalServerError, err.Error())
			return
		}
		c.JSON(J{"ok": true, "msg": c.M("Passkey renommée.", "Passkey renamed.")})
	}))

	// Suppression : les sessions ouvertes avec cette passkey se ferment.
	route("DELETE /api/me/passkeys/{pid}", "api_me_passkeys_delete", loginRequired(func(c *Ctx) {
		u := c.currentUser()
		pid := c.Param("pid")
		if passkeyByID(u, pid) == nil {
			c.Fail(http.StatusNotFound, c.M("Passkey inconnue.", "Unknown passkey."))
			return
		}
		err := passkeysUpdate(str(u, "id", ""), func(list []any) []any {
			out := make([]any, 0, len(list))
			for _, v := range list {
				if pk, ok := v.(map[string]any); !ok || pk["id"] != pid {
					out = append(out, v)
				}
			}
			return out
		})
		if err != nil {
			c.Fail(http.StatusInternalServerError, err.Error())
			return
		}
		c.JSON(J{"ok": true, "msg": c.M("Passkey supprimée.", "Passkey deleted.")})
	}))

	// Connexion : options sans liste de passkeys (l'appareil propose les siennes
	// pour ce domaine, sans que l'identifiant soit demandé).
	route("POST /api/login/passkey/options", "api_login_passkey_options", func(c *Ctx) {
		if wait := loginRetryAfter(c.RemoteIP()); wait > 0 {
			c.tooManyAttempts(wait)
			return
		}
		c.JSON(J{"ok": true, "publicKey": J{
			"challenge": c.passkeyChallenge("login"), "timeout": passkeyTimeoutMillis,
			"userVerification": "required", "allowCredentials": []J{},
		}})
	})

	route("POST /api/login/passkey", "api_login_passkey", func(c *Ctx) {
		ip := c.RemoteIP()
		if wait := loginRetryAfter(ip); wait > 0 {
			c.tooManyAttempts(wait)
			return
		}
		challenge := c.takePasskeyChallenge("login")
		cred, _ := c.Body()["credential"].(map[string]any)
		if challenge == "" || cred == nil {
			c.Fail(http.StatusBadRequest, c.M("Demande expirée : recommencez.", "Request expired: try again."))
			return
		}
		user, pk, ad, err := passkeyAssertion(cred, challenge)
		if err != nil {
			log.Printf("[WARN] Connexion par passkey refusée depuis %s : %v", ip, err)
			c.loginRejected(ip, c.M("Passkey refusée.", "Passkey rejected."))
			return
		}
		loginSucceeded(ip)
		uid, pid := str(user, "id", ""), str(pk, "id", "")
		if err := passkeysUpdate(uid, func(list []any) []any {
			for _, v := range list {
				if p, ok := v.(map[string]any); ok && p["id"] == pid {
					p["sign_count"] = float64(ad.signCount)
					p["backup_state"] = ad.flags&flagBS != 0
					p["last_used"] = nowISO()
				}
			}
			return list
		}); err != nil {
			log.Printf("[WARN] Passkey : compteur non enregistré : %v", err)
		}
		c.startSession(uid)
		c.sess.set("pk", pid)
		recordLogin(uid)
		name := str(user, "username", "")
		webhookEmit("login.success", [2]string{"Connexion de " + name, name + " signed in"},
			[2]string{"Connexion au panel (passkey) depuis " + ip + ".", "Panel sign-in (passkey) from " + ip + "."},
			ip, J{"ip": ip, "user": name, "method": "passkey"})
		c.JSON(J{"ok": true})
	})
}
