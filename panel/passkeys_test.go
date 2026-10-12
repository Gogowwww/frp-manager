// Passkeys : authentificateur simulé (ES256, EdDSA, RS256), enregistrement,
// connexion, compteur, domaine, suppression, comptes OIDC, décodeur CBOR.

package main

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"math/big"
	"net/http"
	"strings"
	"testing"
)

// ── Encodeur CBOR minimal (côté authentificateur) ───────────────────────────

type cborPair struct {
	k, v any
}

func cborHead(major byte, n uint64) []byte {
	switch {
	case n < 24:
		return []byte{major<<5 | byte(n)}
	case n < 1<<8:
		return []byte{major<<5 | 24, byte(n)}
	case n < 1<<16:
		return []byte{major<<5 | 25, byte(n >> 8), byte(n)}
	default:
		b := []byte{major<<5 | 26, 0, 0, 0, 0}
		binary.BigEndian.PutUint32(b[1:], uint32(n))
		return b
	}
}

func cborEnc(v any) []byte {
	switch x := v.(type) {
	case int:
		if x < 0 {
			return cborHead(1, uint64(-1-x))
		}
		return cborHead(0, uint64(x))
	case []byte:
		return append(cborHead(2, uint64(len(x))), x...)
	case string:
		return append(cborHead(3, uint64(len(x))), x...)
	case []cborPair:
		out := cborHead(5, uint64(len(x)))
		for _, p := range x {
			out = append(out, cborEnc(p.k)...)
			out = append(out, cborEnc(p.v)...)
		}
		return out
	}
	panic("type CBOR non géré")
}

// ── Authentificateur simulé ─────────────────────────────────────────────────

type fakeAuthenticator struct {
	alg     int
	signer  crypto.Signer
	credID  []byte
	count   uint32
	counter bool // tient un compteur de signatures
	flags   byte
	rpID    string
}

func newAuthenticator(t *testing.T, alg int) *fakeAuthenticator {
	a := &fakeAuthenticator{alg: alg, credID: make([]byte, 16), flags: flagUP | flagUV, rpID: "panel.example"}
	rand.Read(a.credID)
	var err error
	switch alg {
	case coseES256:
		a.signer, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	case coseEdDSA:
		_, a.signer, err = ed25519.GenerateKey(rand.Reader)
	case coseRS256:
		a.signer, err = rsa.GenerateKey(rand.Reader, 2048)
	}
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func (a *fakeAuthenticator) coseKey() []byte {
	switch pub := a.signer.Public().(type) {
	case *ecdsa.PublicKey:
		raw, _ := pub.Bytes()
		return cborEnc([]cborPair{{1, 2}, {3, coseES256}, {-1, 1}, {-2, raw[1:33]}, {-3, raw[33:]}})
	case ed25519.PublicKey:
		return cborEnc([]cborPair{{1, 1}, {3, coseEdDSA}, {-1, 6}, {-2, []byte(pub)}})
	case *rsa.PublicKey:
		return cborEnc([]cborPair{{1, 3}, {3, coseRS256}, {-1, pub.N.Bytes()}, {-2, big.NewInt(int64(pub.E)).Bytes()}})
	}
	panic("clé")
}

func (a *fakeAuthenticator) authData(flags byte, attested bool) []byte {
	h := sha256.Sum256([]byte(a.rpID))
	out := append([]byte{}, h[:]...)
	out = append(out, flags)
	out = binary.BigEndian.AppendUint32(out, a.count)
	if attested {
		out = append(out, make([]byte, 16)...) // AAGUID nul
		out = binary.BigEndian.AppendUint16(out, uint16(len(a.credID)))
		out = append(out, a.credID...)
		out = append(out, a.coseKey()...)
	}
	return out
}

func clientJSON(typ, challenge, origin string) []byte {
	b, _ := json.Marshal(map[string]any{"type": typ, "challenge": challenge, "origin": origin, "crossOrigin": false})
	return b
}

var b64 = base64.RawURLEncoding.EncodeToString

// create : réponse de navigator.credentials.create() pour ces options.
func (a *fakeAuthenticator) create(opts map[string]any, origin string) J {
	pk := opts["publicKey"].(map[string]any)
	att := cborEnc([]cborPair{{"fmt", "none"}, {"attStmt", []cborPair{}}, {"authData", a.authData(a.flags|flagAT, true)}})
	return J{"id": b64(a.credID), "rawId": b64(a.credID), "type": "public-key", "response": J{
		"clientDataJSON":    b64(clientJSON("webauthn.create", pk["challenge"].(string), origin)),
		"attestationObject": b64(att), "transports": []string{"internal"},
	}}
}

// get : réponse de navigator.credentials.get(), userHandle = identifiant du compte.
func (a *fakeAuthenticator) get(opts map[string]any, origin, userHandle string) J {
	pk := opts["publicKey"].(map[string]any)
	if a.counter {
		a.count++
	}
	ad := a.authData(a.flags, false)
	cd := clientJSON("webauthn.get", pk["challenge"].(string), origin)
	cdHash := sha256.Sum256(cd)
	msg := append(append([]byte{}, ad...), cdHash[:]...)
	var sig []byte
	var err error
	if _, ed := a.signer.(ed25519.PrivateKey); ed {
		sig, err = a.signer.Sign(rand.Reader, msg, crypto.Hash(0))
	} else {
		h := sha256.Sum256(msg)
		sig, err = a.signer.Sign(rand.Reader, h[:], crypto.SHA256)
	}
	if err != nil {
		panic(err)
	}
	return J{"id": b64(a.credID), "rawId": b64(a.credID), "type": "public-key", "response": J{
		"clientDataJSON": b64(cd), "authenticatorData": b64(ad), "signature": b64(sig), "userHandle": b64([]byte(userHandle)),
	}}
}

const testOrigin = "https://panel.example"

// postVia : requête comme si le panel était joint en https://panel.example
// derrière un reverse proxy.
func (c *client) postVia(path string, body any) response {
	return c.do("POST", path, body, map[string]string{"X-CSRF-Token": c.token(), "X-Forwarded-Host": "panel.example"})
}

// registerPasskey : le compte connecté sur cl ajoute la passkey de a.
func registerPasskey(t *testing.T, cl *client, a *fakeAuthenticator, password string) response {
	t.Helper()
	r := cl.postVia("/api/me/passkeys/options", J{"current_password": password})
	expectCode(t, r, http.StatusOK)
	return cl.postVia("/api/me/passkeys", J{"name": "", "credential": a.create(r.json(), testOrigin)})
}

func passkeyLogin(t *testing.T, cl *client, a *fakeAuthenticator, uid string) response {
	t.Helper()
	r := cl.postVia("/api/login/passkey/options", nil)
	expectCode(t, r, http.StatusOK)
	return cl.postVia("/api/login/passkey", J{"credential": a.get(r.json(), testOrigin, uid)})
}

func ownerWithPasskey(t *testing.T, alg int) (*testEnv, *fakeAuthenticator) {
	e := setup(t)
	e.setPassword(testPassword, "admin", false)
	cl := e.client()
	expectCode(t, cl.login("admin", testPassword), http.StatusOK)
	a := newAuthenticator(t, alg)
	r := registerPasskey(t, cl, a, testPassword)
	expectCode(t, r, http.StatusOK)
	return e, a
}

// ── Tests ───────────────────────────────────────────────────────────────────

func TestPasskeyRegisterAndLogin(t *testing.T) {
	for _, alg := range []int{coseES256, coseEdDSA, coseRS256} {
		e, a := ownerWithPasskey(t, alg)
		saved := e.savedConfig()[ownerPasskeysKey].([]any)
		if len(saved) != 1 || saved[0].(map[string]any)["rp_id"] != "panel.example" || saved[0].(map[string]any)["name"] != "Passkey" {
			t.Fatalf("alg %d : passkey enregistrée %v", alg, saved)
		}
		cl := e.client()
		if !strings.Contains(cl.get("/login").text(), `id="login-passkey"`) {
			t.Fatal("bouton passkey absent de la page de connexion")
		}
		expectCode(t, passkeyLogin(t, cl, a, ownerID), http.StatusOK)
		me := cl.get("/api/me").json()
		if me["user"].(map[string]any)["id"] != ownerID {
			t.Fatalf("alg %d : %v", alg, me)
		}
		// La clé publique ne sort pas par la configuration
		if _, leak := cl.get("/api/manager/config").json()["config"].(map[string]any)[ownerPasskeysKey]; leak {
			t.Fatal("owner_passkeys dans /api/manager/config")
		}
	}
}

func TestPasskeyNoButtonWithoutPasskey(t *testing.T) {
	e := setup(t)
	e.setPassword(testPassword, "admin", false)
	if strings.Contains(e.client().get("/login").text(), `id="login-passkey"`) {
		t.Fatal("bouton passkey sans passkey enregistrée")
	}
}

func TestPasskeyRegistrationNeedsPassword(t *testing.T) {
	e := setup(t)
	e.setPassword(testPassword, "admin", false)
	cl := e.client()
	expectCode(t, cl.login("admin", testPassword), http.StatusOK)
	expectCode(t, cl.postVia("/api/me/passkeys/options", J{"current_password": "mauvais"}), http.StatusForbidden)
	// Sans options (donc sans mot de passe) : pas de défi, refus
	a := newAuthenticator(t, coseES256)
	fake := J{"publicKey": J{"challenge": b64([]byte("devine"))}}
	expectCode(t, cl.postVia("/api/me/passkeys", J{"credential": a.create(fake, testOrigin)}), http.StatusBadRequest)
	if n := len(userPasskeys(ownerUser())); n != 0 {
		t.Fatalf("%d passkeys", n)
	}
}

func TestPasskeyRegistrationChecks(t *testing.T) {
	e := setup(t)
	e.setPassword(testPassword, "admin", false)
	cl := e.client()
	expectCode(t, cl.login("admin", testPassword), http.StatusOK)
	try := func(a *fakeAuthenticator, origin string, via bool) response {
		r := cl.postVia("/api/me/passkeys/options", J{"current_password": testPassword})
		body := J{"credential": a.create(r.json(), origin)}
		if via {
			return cl.postVia("/api/me/passkeys", body)
		}
		return cl.post("/api/me/passkeys", body)
	}
	noUV := newAuthenticator(t, coseES256)
	noUV.flags = flagUP
	expectCode(t, try(noUV, testOrigin, true), http.StatusBadRequest)
	otherRP := newAuthenticator(t, coseES256)
	otherRP.rpID = "evil.example"
	expectCode(t, try(otherRP, testOrigin, true), http.StatusBadRequest)
	// Origine en http (hors localhost) ou différente de l'adresse du panel
	expectCode(t, try(newAuthenticator(t, coseES256), "http://panel.example", true), http.StatusBadRequest)
	expectCode(t, try(newAuthenticator(t, coseES256), testOrigin, false), http.StatusBadRequest)
	// Défi à usage unique
	a := newAuthenticator(t, coseES256)
	r := cl.postVia("/api/me/passkeys/options", J{"current_password": testPassword})
	body := J{"name": "Clé USB", "credential": a.create(r.json(), testOrigin)}
	expectCode(t, cl.postVia("/api/me/passkeys", body), http.StatusOK)
	expectCode(t, cl.postVia("/api/me/passkeys", body), http.StatusBadRequest)
	list := cl.get("/api/me/passkeys").json()["passkeys"].([]any)
	if len(list) != 1 || list[0].(map[string]any)["name"] != "Clé USB" {
		t.Fatalf("liste : %v", list)
	}
	// Même passkey une seconde fois : refusée
	expectCode(t, registerPasskey(t, cl, a, testPassword), http.StatusConflict)
}

func TestPasskeyLoginChecks(t *testing.T) {
	e, a := ownerWithPasskey(t, coseES256)
	a.counter = true
	cl := e.client()
	expectCode(t, passkeyLogin(t, cl, a, ownerID), http.StatusOK)

	// Compteur qui recule (passkey copiée) : refusé
	a.count -= 2
	expectCode(t, passkeyLogin(t, e.client(), a, ownerID), http.StatusUnauthorized)
	a.count += 5
	expectCode(t, passkeyLogin(t, e.client(), a, ownerID), http.StatusOK)

	// Autre origine, autre compte, signature fausse, sans vérification
	other := e.client()
	r := other.postVia("/api/login/passkey/options", nil)
	expectCode(t, other.postVia("/api/login/passkey", J{"credential": a.get(r.json(), "https://evil.example", ownerID)}), http.StatusUnauthorized)
	r = other.postVia("/api/login/passkey/options", nil)
	expectCode(t, other.postVia("/api/login/passkey", J{"credential": a.get(r.json(), testOrigin, "0123456789ab")}), http.StatusUnauthorized)
	r = other.postVia("/api/login/passkey/options", nil)
	cred := a.get(r.json(), testOrigin, ownerID)
	cred["response"].(J)["signature"] = b64([]byte("pas une signature"))
	expectCode(t, other.postVia("/api/login/passkey", J{"credential": cred}), http.StatusUnauthorized)
	a.flags = flagUP
	expectCode(t, passkeyLogin(t, other, a, ownerID), http.StatusUnauthorized)
	a.flags = flagUP | flagUV
	// Défi rejoué : la même réponse ne sert qu'une fois
	r = other.postVia("/api/login/passkey/options", nil)
	cred = a.get(r.json(), testOrigin, ownerID)
	expectCode(t, other.postVia("/api/login/passkey", J{"credential": cred}), http.StatusOK)
	expectCode(t, other.postVia("/api/login/passkey", J{"credential": cred}), http.StatusBadRequest)
	// Passkey inconnue
	stranger := newAuthenticator(t, coseES256)
	expectCode(t, passkeyLogin(t, e.client(), stranger, ownerID), http.StatusUnauthorized)
}

func TestPasskeyFailuresLockOut(t *testing.T) {
	e, _ := ownerWithPasskey(t, coseES256)
	stranger := newAuthenticator(t, coseES256)
	cl := e.client()
	for i := 0; i < loginFreeAttempts; i++ {
		passkeyLogin(t, cl, stranger, ownerID)
	}
	expectCode(t, cl.postVia("/api/login/passkey/options", nil), http.StatusTooManyRequests)
}

func TestPasskeyDeleteEndsItsSessions(t *testing.T) {
	e, a := ownerWithPasskey(t, coseEdDSA)
	pkSession := e.client()
	expectCode(t, passkeyLogin(t, pkSession, a, ownerID), http.StatusOK)
	pwSession := e.client()
	expectCode(t, pwSession.login("admin", testPassword), http.StatusOK)

	id := b64(a.credID)
	expectCode(t, pwSession.put("/api/me/passkeys/"+id, J{"name": "Téléphone"}), http.StatusOK)
	expectCode(t, pwSession.put("/api/me/passkeys/"+id, J{"name": "  "}), http.StatusBadRequest)
	if userPasskeys(ownerUser())[0]["name"] != "Téléphone" {
		t.Fatal("nom non enregistré")
	}
	expectCode(t, pwSession.del("/api/me/passkeys/"+id), http.StatusOK)
	expectCode(t, pkSession.get("/api/me"), http.StatusUnauthorized)
	expectCode(t, pwSession.get("/api/me"), http.StatusOK)
	expectCode(t, passkeyLogin(t, e.client(), a, ownerID), http.StatusUnauthorized)
}

func TestPasskeyAccountsAndAdmin(t *testing.T) {
	e := setup(t)
	e.setPassword(testPassword, "admin", false)
	admin := e.client()
	expectCode(t, admin.login("admin", testPassword), http.StatusOK)
	r := admin.post("/api/users", J{"username": "alice", "password": testPassword, "role": "viewer"})
	expectCode(t, r, http.StatusOK)
	uid := r.json()["user"].(map[string]any)["id"].(string)

	alice := e.client()
	expectCode(t, alice.login("alice", testPassword), http.StatusOK)
	a := newAuthenticator(t, coseES256)
	expectCode(t, registerPasskey(t, alice, a, testPassword), http.StatusOK)
	viaKey := e.client()
	expectCode(t, passkeyLogin(t, viaKey, a, uid), http.StatusOK)
	if viaKey.get("/api/me").json()["user"].(map[string]any)["username"] != "alice" {
		t.Fatal("mauvais compte")
	}
	// L'administrateur voit le nombre de passkeys et peut les retirer
	var view map[string]any
	for _, u := range admin.get("/api/users").json()["users"].([]any) {
		if u.(map[string]any)["id"] == uid {
			view = u.(map[string]any)
		}
	}
	if view["passkeys"] != float64(1) {
		t.Fatalf("vue : %v", view)
	}
	// Compte désactivé : passkey refusée
	expectCode(t, admin.put("/api/users/"+uid, J{"enabled": false}), http.StatusOK)
	expectCode(t, passkeyLogin(t, e.client(), a, uid), http.StatusUnauthorized)
	expectCode(t, admin.put("/api/users/"+uid, J{"enabled": true, "remove_passkeys": true}), http.StatusOK)
	expectCode(t, passkeyLogin(t, e.client(), a, uid), http.StatusUnauthorized)
	if n := len(userPasskeys(allUsers()[uid])); n != 0 {
		t.Fatalf("%d passkeys après retrait", n)
	}
}

func TestPasskeyOIDCAccountRefused(t *testing.T) {
	e, idp := oidcSetup(t, J{"auto_create": true})
	cl := e.client()
	expectCode(t, oidcFinish(cl, idp, oidcStart(t, cl, idp), J{"preferred_username": "bob", "email": "bob@corp.io", "email_verified": true}), 200)
	expectCode(t, cl.postVia("/api/me/passkeys/options", J{"current_password": "x"}), http.StatusBadRequest)
	if cl.get("/api/me/passkeys").json()["available"] != false {
		t.Fatal("passkeys proposées à un compte OIDC")
	}
}

func TestPasskeyOriginHost(t *testing.T) {
	cases := map[string]string{
		"https://panel.example":      "panel.example",
		"https://Panel.Example:8443": "panel.example",
		"http://localhost:8765":      "localhost",
		"http://panel.example":       "",
		"https://192.168.1.2":        "",
		"https://[::1]":              "",
		"https://panel.example/x":    "",
		"panel.example":              "",
	}
	for in, want := range cases {
		if got := passkeyOriginHost(in); got != want {
			t.Errorf("%q → %q, attendu %q", in, got, want)
		}
	}
}

func TestCBORDecoderRobust(t *testing.T) {
	v, n, err := cborDecode(cborEnc([]cborPair{{1, 2}, {-1, []byte{1, 2}}, {"a", "b"}}))
	m, ok := v.(map[any]any)
	if err != nil || !ok || n != 11 || m[int64(1)] != int64(2) || m["a"] != "b" {
		t.Fatalf("%v %d %v", v, n, err)
	}
	// Données tronquées, longueurs démesurées, imbrication profonde, aléa : erreur, jamais de panique
	bad := [][]byte{{}, {0x5a, 0xff, 0xff, 0xff, 0xff}, {0x9b, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff},
		{0xbf}, {0xa1, 0x01}, {0xfb, 0, 0, 0, 0, 0, 0, 0, 0}, []byte(strings.Repeat("\x81", 64) + "\x00")}
	for _, b := range bad {
		if _, _, err := cborDecode(b); err == nil {
			t.Errorf("% x accepté", b)
		}
	}
	buf := make([]byte, 64)
	for i := 0; i < 20000; i++ {
		rand.Read(buf)
		cborDecode(buf)
		parseAuthData(buf)
		parseCOSEKey(buf)
	}
}
