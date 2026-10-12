// OpenID Connect (connexion unique).
//
// Flux « authorization code » avec PKCE (S256), state et nonce. L'état du flux
// tient dans un cookie signé SameSite=Lax, à part : le cookie de session est
// Strict, donc absent du retour du fournisseur (navigation venue d'un autre site).
// Le jeton d'identité est reçu directement du point d'accès /token en HTTPS : la
// spécification (OIDC Core §3.1.3.7) permet alors de se fier à TLS plutôt qu'à la
// signature du jeton. On en contrôle quand même émetteur, audience, expiration
// et nonce.

package main

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	oidcFlowCookie = "frpm_oidc"
	oidcFlowMaxAge = 600
)

var oidcErrors = map[string]bool{"disabled": true, "state": true, "denied": true, "provider": true, "token": true,
	"claims": true, "no_account": true, "disabled_user": true, "domain": true}

func oidcDefaults() map[string]any {
	return map[string]any{
		"enabled": false, "issuer": "", "client_id": "", "client_secret": "",
		"scopes": "openid profile email", "label": "", "redirect_uri": "",
		"username_claim": "preferred_username",
		"auto_create":    false, "default_role": "viewer", "allowed_domains": []any{},
		"groups_claim": "groups", "group_roles": map[string]any{},
	}
}

var (
	oidcClaimRe  = regexp.MustCompile(`^[A-Za-z0-9_.:\-/]{1,128}$`)
	oidcScopesRe = regexp.MustCompile(`^[A-Za-z0-9_.:\-/]+(?: [A-Za-z0-9_.:\-/]+)*$`)
	domainRe     = regexp.MustCompile(`^(?:[a-z0-9-]{1,63}\.)+[a-z]{2,63}$`)
	controlRe    = regexp.MustCompile(`[\x00-\x1f\x7f]`)

	oidcMetaMu sync.Mutex
	oidcMeta   = map[string]oidcMetaEntry{} // émetteur → document de découverte
	httpClient = &http.Client{Timeout: 30 * time.Second}
)

type oidcMetaEntry struct {
	expires time.Time
	meta    map[string]any
}

// OidcError : code (une des oidcErrors, affichée sur la page de connexion) ; le
// détail ne va que dans le journal.
type OidcError struct{ Code, Detail string }

func (e *OidcError) Error() string { return e.Detail }

func oidcErr(code, detail string) *OidcError {
	if detail == "" {
		detail = code
	}
	return &OidcError{code, detail}
}

func oidcConfig() map[string]any {
	c := oidcDefaults()
	for k, v := range mapv(cfg(), "oidc") {
		c[k] = v
	}
	return c
}

func oidcEnabled() bool {
	c := oidcConfig()
	return truthy(c["enabled"]) && truthy(c["issuer"]) && truthy(c["client_id"])
}

// oidcURLOK : HTTPS, ou HTTP vers la machine locale (essais avec un fournisseur sur le poste).
func oidcURLOK(raw any) bool {
	s, ok := raw.(string)
	if !ok {
		return false
	}
	u, err := url.Parse(s)
	if err != nil || u.Hostname() == "" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	return u.Scheme == "https" || (u.Scheme == "http" && (host == "localhost" || host == "127.0.0.1" || host == "::1"))
}

func short(s string, n int) string { return truncRunes(s, n) }

func oidcDiscover(issuer string, fresh bool) (map[string]any, error) {
	issuer = strings.TrimRight(issuer, "/")
	oidcMetaMu.Lock()
	hit, ok := oidcMeta[issuer]
	oidcMetaMu.Unlock()
	if ok && hit.expires.After(time.Now()) && !fresh {
		return hit.meta, nil
	}
	req, _ := http.NewRequest(http.MethodGet, issuer+"/.well-known/openid-configuration", nil)
	req.Header.Set("Accept", "application/json")
	client := &http.Client{Timeout: 8 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, oidcErr("provider", "découverte impossible : "+short(err.Error(), 160))
	}
	defer resp.Body.Close()
	var meta map[string]any
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 400 {
		return nil, oidcErr("provider", fmt.Sprintf("découverte impossible : HTTP %d", resp.StatusCode))
	}
	if err := json.Unmarshal(body, &meta); err != nil || meta == nil {
		return nil, oidcErr("provider", "découverte impossible : réponse illisible")
	}
	if strings.TrimRight(str(meta, "issuer", ""), "/") != issuer {
		return nil, oidcErr("provider", "l'émetteur annoncé ne correspond pas à l'adresse configurée")
	}
	for _, key := range []string{"authorization_endpoint", "token_endpoint"} {
		if !oidcURLOK(meta[key]) {
			return nil, oidcErr("provider", key+" absent ou non sécurisé")
		}
	}
	if !oidcURLOK(meta["userinfo_endpoint"]) {
		delete(meta, "userinfo_endpoint")
	}
	oidcMetaMu.Lock()
	oidcMeta[issuer] = oidcMetaEntry{time.Now().Add(time.Hour), meta}
	oidcMetaMu.Unlock()
	return meta, nil
}

func (c *Ctx) oidcRedirectURI(oc map[string]any) string {
	if ru := str(oc, "redirect_uri", ""); ru != "" {
		return ru
	}
	return c.externalURL("/auth/oidc/callback")
}

func (c *Ctx) oidcClearCookie() {
	http.SetCookie(c.W, &http.Cookie{Name: oidcFlowCookie, Value: "", Path: "/auth/oidc", MaxAge: -1, Expires: time.Unix(0, 0)})
	c.W.Header().Set("Cache-Control", "no-store")
}

func (c *Ctx) oidcFail(code string) {
	c.oidcClearCookie()
	c.Redirect("/login?error=" + url.QueryEscape(code))
}

func b64url(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

type oidcFlow struct {
	S  string `json:"s"`
	N  string `json:"n"`
	CV string `json:"cv"`
	RU string `json:"ru"`
	T  int64  `json:"t"`
}

// jwtClaims : contenu (sans vérification de signature : voir plus haut) d'un jeton JWT.
func jwtClaims(token string) (map[string]any, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, oidcErr("claims", "jeton d'identité illisible")
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return nil, oidcErr("claims", "jeton d'identité illisible")
	}
	var claims map[string]any
	if json.Unmarshal(payload, &claims) != nil || claims == nil {
		return nil, oidcErr("claims", "jeton d'identité illisible")
	}
	return claims, nil
}

// oidcExchange échange le code contre les jetons et renvoie les revendications.
func oidcExchange(oc, meta map[string]any, code string, flow oidcFlow) (map[string]any, error) {
	clientID := str(oc, "client_id", "")
	form := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {flow.RU},
		"code_verifier": {flow.CV}, "client_id": {clientID}}
	secret := str(oc, "client_secret", "")
	basic := false
	if secret != "" {
		methods := stringList(meta["token_endpoint_auth_methods_supported"])
		if len(methods) == 0 {
			methods = []string{"client_secret_basic"}
		}
		has := func(m string) bool {
			for _, x := range methods {
				if x == m {
					return true
				}
			}
			return false
		}
		if has("client_secret_basic") || !has("client_secret_post") {
			basic = true
		} else {
			form.Set("client_secret", secret)
		}
	}
	req, _ := http.NewRequest(http.MethodPost, str(meta, "token_endpoint", ""), strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	if basic {
		req.SetBasicAuth(url.QueryEscape(clientID), url.QueryEscape(secret)) // RFC 6749 §2.3.1
	}
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return nil, oidcErr("token", "échange du code impossible : "+short(err.Error(), 160))
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var tok map[string]any
	if err := json.Unmarshal(body, &tok); err != nil {
		return nil, oidcErr("token", "échange du code impossible : réponse illisible")
	}
	idToken, isStr := tok["id_token"].(string)
	if resp.StatusCode != http.StatusOK || tok == nil || !isStr {
		e := ""
		if tok != nil && tok["error"] != nil {
			e = short(pyStr(tok["error"]), 80)
		}
		return nil, oidcErr("token", fmt.Sprintf("HTTP %d %s", resp.StatusCode, e))
	}
	claims, err := jwtClaims(idToken)
	if err != nil {
		return nil, err
	}
	if strings.TrimRight(str(claims, "iss", ""), "/") != strings.TrimRight(str(oc, "issuer", ""), "/") {
		return nil, oidcErr("claims", "émetteur du jeton inattendu")
	}
	var auds []any
	if list, ok := claims["aud"].([]any); ok {
		auds = list
	} else {
		auds = []any{claims["aud"]}
	}
	found := false
	for _, a := range auds {
		if a == clientID {
			found = true
		}
	}
	if !found || (len(auds) > 1 && claims["azp"] != clientID) {
		return nil, oidcErr("claims", "audience du jeton inattendue")
	}
	exp, ok := claims["exp"].(float64)
	if !ok || exp < float64(time.Now().Unix())-60 {
		return nil, oidcErr("claims", "jeton expiré")
	}
	if subtle.ConstantTimeCompare([]byte(str(claims, "nonce", "")), []byte(flow.N)) != 1 {
		return nil, oidcErr("claims", "nonce différent")
	}
	sub, ok := claims["sub"].(string)
	if !ok || len(sub) == 0 || len([]rune(sub)) > 255 {
		return nil, oidcErr("claims", "sujet absent")
	}
	// E-mail, groupes… : parfois seulement dans userinfo
	if ui, ok := meta["userinfo_endpoint"].(string); ok {
		if at, ok := tok["access_token"].(string); ok {
			req, _ := http.NewRequest(http.MethodGet, ui, nil)
			req.Header.Set("Authorization", "Bearer "+at)
			req.Header.Set("Accept", "application/json")
			if r2, err := (&http.Client{Timeout: 8 * time.Second}).Do(req); err == nil {
				b, _ := io.ReadAll(io.LimitReader(r2.Body, 1<<20))
				r2.Body.Close()
				var info map[string]any
				if r2.StatusCode == http.StatusOK && json.Unmarshal(b, &info) == nil && info != nil && info["sub"] == sub {
					merged := map[string]any{}
					for k, v := range info {
						merged[k] = v
					}
					for k, v := range claims {
						merged[k] = v
					}
					claims = merged
				}
			}
		}
	}
	return claims, nil
}

func oidcGroups(oc, claims map[string]any) []string {
	switch raw := claims[str(oc, "groups_claim", "groups")].(type) {
	case string:
		return []string{raw}
	case []any:
		return stringList(raw)
	}
	return nil
}

// oidcResolveUser : compte local correspondant à l'identité : déjà lié, préparé à
// l'avance (e-mail vérifié identique), ou créé si auto_create.
func oidcResolveUser(oc, claims map[string]any) (User, error) {
	iss, sub := strings.TrimRight(str(oc, "issuer", ""), "/"), claims["sub"].(string)
	email := strings.ToLower(strings.TrimSpace(str(claims, "email", "")))
	if claims["email"] == nil {
		email = ""
	}
	if !(cleanEmail(email) != "" && (claims["email_verified"] == true || claims["email_verified"] == "true")) {
		email = ""
	}
	defaultRole := str(oc, "default_role", "viewer")
	mapping, _ := oc["group_roles"].(map[string]any)
	roleByGroup := ""
	if len(mapping) > 0 {
		hits := map[string]bool{}
		for _, g := range oidcGroups(oc, claims) {
			if r, ok := mapping[g].(string); ok {
				hits[r] = true
			}
		}
		roleByGroup = defaultRole
		for _, r := range roleOrder {
			if hits[r] {
				roleByGroup = r
				break
			}
		}
	}
	cfgMu.Lock()
	defer cfgMu.Unlock()
	users, _ := deepCopy(mapv(cfg(), "users")).(map[string]any)
	uid := ""
	for _, k := range sortedKeys(users) {
		u, _ := users[k].(map[string]any)
		o := mapv(u, "oidc")
		if str(u, "source", "local") == "oidc" && o["sub"] == sub && o["iss"] == iss {
			uid = k
			break
		}
	}
	if uid == "" && email != "" {
		var pending []string
		for _, k := range sortedKeys(users) {
			u, _ := users[k].(map[string]any)
			if str(u, "source", "local") == "oidc" && !truthy(mapv(u, "oidc")["sub"]) && str(u, "email", "") == email {
				pending = append(pending, k)
			}
		}
		if len(pending) == 1 {
			uid = pending[0]
		}
	}
	shown := email
	if shown == "" {
		shown = "e-mail non vérifié"
	}
	if uid == "" {
		if !boolv(oc, "auto_create", false) {
			return nil, oidcErr("no_account", fmt.Sprintf("aucun compte pour %s (%s)", sub, shown))
		}
		var domains []string
		for _, d := range stringList(oc["allowed_domains"]) {
			domains = append(domains, strings.ToLower(d))
		}
		if len(domains) > 0 {
			at := strings.LastIndex(email, "@")
			ok := false
			for _, d := range domains {
				if email[at+1:] == d {
					ok = true
				}
			}
			if !ok {
				return nil, oidcErr("domain", fmt.Sprintf("domaine refusé (%s)", shown))
			}
		}
		if len(users) >= usersMax {
			return nil, oidcErr("no_account", "trop de comptes")
		}
		baseRaw := claims[str(oc, "username_claim", "preferred_username")]
		base := ""
		if truthy(baseRaw) {
			base = pyStr(baseRaw)
		} else if email != "" {
			base = email
		} else {
			base = sub
		}
		base = truncRunes(strings.TrimSpace(controlRe.ReplaceAllString(base, "")), 60)
		if base == "" {
			base = "user"
		}
		name, n := base, 2
		for nameTaken(name, "") {
			name = fmt.Sprintf("%s-%d", base, n)
			n++
		}
		uid = tokenHex(6)
		role := roleByGroup
		if role == "" {
			role = defaultRole
		}
		users[uid] = map[string]any{"username": name, "source": "oidc", "enabled": true, "created": nowISO(),
			"role": role, "permissions": []any{}, "instances": "*"}
	}
	u := users[uid].(map[string]any)
	if !boolv(u, "enabled", true) {
		return nil, oidcErr("disabled_user", fmt.Sprintf("compte %s désactivé", str(u, "username", "")))
	}
	u["oidc"] = map[string]any{"iss": iss, "sub": sub}
	if email != "" {
		u["email"] = email
	}
	if name, ok := claims["name"].(string); ok {
		u["display_name"] = truncRunes(name, 128)
	}
	if roleByGroup != "" && str(u, "role", "") != roleByGroup {
		u["role"] = roleByGroup
		u["permissions"] = []any{}
		if roleByGroup == "admin" {
			u["instances"] = "*"
		} else if _, ok := u["instances"]; !ok {
			u["instances"] = "*"
		}
	}
	u["last_login"] = nowISO()
	if _, err := cfgUpdateLocked(Cfg{"users": users}); err != nil {
		return nil, oidcErr("no_account", err.Error())
	}
	return allUsers()[uid], nil
}

// oidcPublic : réglages renvoyés à l'interface, jamais le secret client.
func oidcPublic(oc map[string]any) J {
	out := J{}
	for k, v := range oc {
		if k != "client_secret" {
			out[k] = v
		}
	}
	out["has_secret"] = truthy(oc["client_secret"])
	return out
}

// oidcClean → (réglages, message d'erreur).
func (c *Ctx) oidcClean(raw, old map[string]any) (map[string]any, string) {
	oc := copyCfg(old)
	if v, ok := raw["enabled"]; ok {
		oc["enabled"] = truthy(v)
	} else {
		oc["enabled"] = truthy(old["enabled"])
	}
	if v, ok := raw["issuer"]; ok {
		iss := strings.TrimRight(strings.TrimSpace(pyStr(v)), "/")
		oc["issuer"] = iss
		if iss != "" && (!oidcURLOK(iss) || len([]rune(iss)) > 512) {
			return nil, c.M("Adresse de l'émetteur invalide (https:// attendu).", "Invalid issuer address (https:// expected).")
		}
	}
	if v, ok := raw["client_id"]; ok {
		oc["client_id"] = truncRunes(strings.TrimSpace(pyStr(v)), 256)
	}
	if truthy(raw["client_secret"]) {
		oc["client_secret"] = truncRunes(strings.TrimSpace(pyStr(raw["client_secret"])), 1024)
	} else if truthy(raw["clear_secret"]) {
		oc["client_secret"] = ""
	}
	if v, ok := raw["scopes"]; ok {
		s := strings.Join(strings.Fields(pyStr(v)), " ")
		if s == "" {
			s = "openid profile email"
		}
		oc["scopes"] = s
		if !oidcScopesRe.MatchString(s) {
			return nil, c.M("Portées invalides.", "Invalid scopes.")
		}
	}
	if v, ok := raw["label"]; ok {
		oc["label"] = truncRunes(strings.TrimSpace(controlRe.ReplaceAllString(pyStr(v), "")), 40)
	}
	if v, ok := raw["redirect_uri"]; ok {
		ru := strings.TrimSpace(pyStr(v))
		oc["redirect_uri"] = ru
		if ru != "" && !oidcURLOK(ru) {
			return nil, c.M("Adresse de retour invalide (https:// attendu).", "Invalid redirect address (https:// expected).")
		}
	}
	for _, key := range []string{"username_claim", "groups_claim"} {
		if v, ok := raw[key]; ok {
			s := strings.TrimSpace(pyStr(v))
			oc[key] = s
			if !oidcClaimRe.MatchString(s) {
				return nil, c.M("Nom de revendication invalide.", "Invalid claim name.")
			}
		}
	}
	if v, ok := raw["auto_create"]; ok {
		oc["auto_create"] = truthy(v)
	}
	if v, ok := raw["default_role"]; ok {
		// Jamais admin par défaut : tout compte du fournisseur le deviendrait
		if v != "operator" && v != "viewer" {
			return nil, c.M("Rôle par défaut : opérateur ou lecteur.", "Default role: operator or viewer.")
		}
		oc["default_role"] = v
	}
	if v, ok := raw["allowed_domains"]; ok {
		var doms []any
		switch x := v.(type) {
		case string:
			for _, d := range strings.Fields(strings.ReplaceAll(x, ",", " ")) {
				doms = append(doms, strings.ToLower(strings.TrimSpace(d)))
			}
		case []any:
			doms = x
		default:
			return nil, c.M("Domaines autorisés invalides.", "Invalid allowed domains.")
		}
		if len(doms) > 20 {
			return nil, c.M("Domaines autorisés invalides.", "Invalid allowed domains.")
		}
		set := map[string]bool{}
		for _, d := range doms {
			s, ok := d.(string)
			if !ok || !domainRe.MatchString(s) {
				return nil, c.M("Domaines autorisés invalides.", "Invalid allowed domains.")
			}
			set[s] = true
		}
		oc["allowed_domains"] = toAnyList(sortedKeys(set))
	}
	if v, ok := raw["group_roles"]; ok {
		gr, isMap := v.(map[string]any)
		valid := isMap && len(gr) <= 50
		if valid {
			for k, r := range gr {
				rs, _ := r.(string)
				if _, known := roles[rs]; len(k) == 0 || len([]rune(k)) > 256 || !known {
					valid = false
				}
			}
		}
		if !valid {
			return nil, c.M("Correspondance groupes → rôles invalide.", "Invalid group → role mapping.")
		}
		oc["group_roles"] = gr
	}
	if truthy(oc["enabled"]) && !(truthy(oc["issuer"]) && truthy(oc["client_id"])) {
		return nil, c.M("L'émetteur et l'identifiant client sont requis pour activer la connexion.",
			"The issuer and client ID are required to enable sign-in.")
	}
	return oc, ""
}

func init() {
	route("GET /auth/oidc/login", "oidc_login", func(c *Ctx) {
		if !oidcEnabled() {
			c.oidcFail("disabled")
			return
		}
		oc := oidcConfig()
		meta, err := oidcDiscover(str(oc, "issuer", ""), false)
		if err != nil {
			log.Printf("[WARN] Connexion OIDC : %v", err)
			c.oidcFail("provider")
			return
		}
		flow := oidcFlow{S: tokenURLSafe(24), N: tokenURLSafe(24), CV: tokenURLSafe(48), RU: c.oidcRedirectURI(oc), T: time.Now().Unix()}
		scopes := str(oc, "scopes", "")
		if !oidcScopesRe.MatchString(scopes) {
			scopes = "openid profile email"
		}
		hasOpenID := false
		for _, s := range strings.Fields(scopes) {
			if s == "openid" {
				hasOpenID = true
			}
		}
		if !hasOpenID {
			scopes = "openid " + scopes
		}
		challenge := sha256.Sum256([]byte(flow.CV))
		params := url.Values{"response_type": {"code"}, "client_id": {str(oc, "client_id", "")}, "redirect_uri": {flow.RU},
			"scope": {scopes}, "state": {flow.S}, "nonce": {flow.N},
			"code_challenge": {b64url(challenge[:])}, "code_challenge_method": {"S256"}}
		base := str(meta, "authorization_endpoint", "")
		sep := "?"
		if strings.Contains(base, "?") {
			sep = "&"
		}
		payload, _ := json.Marshal(flow)
		http.SetCookie(c.W, &http.Cookie{Name: oidcFlowCookie, Value: signValue("frpm-oidc-flow", payload), MaxAge: oidcFlowMaxAge,
			HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: sessionCookieSecure, Path: "/auth/oidc"})
		c.W.Header().Set("Cache-Control", "no-store")
		c.Redirect(base + sep + params.Encode())
	})

	route("GET /auth/oidc/callback", "oidc_callback", func(c *Ctx) {
		var flow oidcFlow
		ck, err := c.R.Cookie(oidcFlowCookie)
		ok := err == nil
		if ok {
			payload, valid := unsignValue("frpm-oidc-flow", ck.Value)
			ok = valid && json.Unmarshal(payload, &flow) == nil && flow.S != "" && flow.N != "" && flow.CV != "" && flow.RU != "" &&
				time.Now().Unix()-flow.T <= oidcFlowMaxAge
		}
		if !ok {
			c.oidcFail("state")
			return
		}
		if !oidcEnabled() {
			c.oidcFail("disabled")
			return
		}
		if e := c.Query("error"); e != "" {
			log.Printf("[WARN] Connexion OIDC refusée par le fournisseur : %s", short(e, 80))
			c.oidcFail("denied")
			return
		}
		if subtle.ConstantTimeCompare([]byte(c.Query("state")), []byte(flow.S)) != 1 {
			c.oidcFail("state")
			return
		}
		code := c.Query("code")
		if code == "" || len(code) > 4096 {
			c.oidcFail("denied")
			return
		}
		oc := oidcConfig()
		var user User
		meta, err := oidcDiscover(str(oc, "issuer", ""), false)
		if err == nil {
			var claims map[string]any
			if claims, err = oidcExchange(oc, meta, code, flow); err == nil {
				user, err = oidcResolveUser(oc, claims)
			}
		}
		if err != nil {
			log.Printf("[WARN] Connexion OIDC refusée : %v", err)
			codeErr := "provider"
			if oe, isOE := err.(*OidcError); isOE {
				codeErr = oe.Code
			}
			c.oidcFail(codeErr)
			return
		}
		c.startSession(str(user, "id", ""))
		ip := c.RemoteIP()
		name := str(user, "username", "")
		webhookEmit("login.success", [2]string{"Connexion de " + name, name + " signed in"},
			[2]string{"Connexion au panel (OpenID Connect) depuis " + ip + ".", "Panel sign-in (OpenID Connect) from " + ip + "."},
			ip, J{"ip": ip, "user": name, "method": "oidc"})
		// Page intermédiaire plutôt qu'une redirection : le cookie de session est
		// SameSite=Strict, et le navigateur ne l'enverrait pas à une redirection
		// qui prolonge une navigation venue du fournisseur.
		c.oidcClearCookie()
		c.HTML(http.StatusOK, `<!doctype html><meta charset="utf-8"><meta http-equiv="refresh" content="0;url=/">`+
			`<title>FRP Manager</title><a href="/">FRP Manager</a>`)
	})

	fullAdmin := func(h func(*Ctx)) func(*Ctx) {
		return perm(P("users.manage"), func(c *Ctx) {
			if !isFullAdmin(c.currentUser()) {
				c.forbidden("")
				return
			}
			h(c)
		})
	}

	route("GET /api/oidc", "api_oidc_get", fullAdmin(func(c *Ctx) {
		c.JSON(J{"ok": true, "oidc": oidcPublic(oidcConfig()), "redirect_uri": c.externalURL("/auth/oidc/callback")})
	}))

	route("POST /api/oidc", "api_oidc_save", fullAdmin(func(c *Ctx) {
		oc, msg := c.oidcClean(c.Body(), oidcConfig())
		if msg != "" {
			c.Fail(http.StatusBadRequest, msg)
			return
		}
		if _, err := cfgUpdate(Cfg{"oidc": oc}); err != nil {
			c.Fail(http.StatusInternalServerError, err.Error())
			return
		}
		oidcMetaMu.Lock()
		oidcMeta = map[string]oidcMetaEntry{}
		oidcMetaMu.Unlock()
		c.JSON(J{"ok": true, "oidc": oidcPublic(oc), "msg": c.M("Connexion unique enregistrée.", "Single sign-on saved.")})
	}))

	// Interroge le document de découverte de l'émetteur saisi (sans rien enregistrer).
	route("POST /api/oidc/test", "api_oidc_test", fullAdmin(func(c *Ctx) {
		issuer := ""
		if v := c.Body()["issuer"]; truthy(v) {
			issuer = pyStr(v)
		} else {
			issuer = str(oidcConfig(), "issuer", "")
		}
		issuer = strings.TrimSpace(issuer)
		if !oidcURLOK(issuer) {
			c.Fail(http.StatusBadRequest, c.M("Adresse de l'émetteur invalide (https:// attendu).", "Invalid issuer address (https:// expected)."))
			return
		}
		meta, err := oidcDiscover(issuer, true)
		if err != nil {
			detail := err.Error()
			c.JSON(J{"ok": false, "msg": c.M("Échec : "+detail, "Failed: "+detail)})
			return
		}
		eps := J{}
		for _, k := range []string{"authorization_endpoint", "token_endpoint", "userinfo_endpoint"} {
			eps[k] = meta[k]
		}
		c.JSON(J{"ok": true, "msg": c.M("Fournisseur joignable.", "Provider reachable."), "endpoints": eps})
	}))
}
