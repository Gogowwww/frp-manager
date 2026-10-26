// Authentification : mots de passe, comptes et permissions (ACL), connexion,
// limitation des tentatives, configuration initiale.

package main

import (
	"bufio"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"log"
	"net/http"
	"os"
	"regexp"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/scrypt"
	"golang.org/x/term"
)

// ── Mots de passe ───────────────────────────────────────────────────────────
// argon2id, avec les réglages par défaut d'argon2-cffi (versions Python) : un
// hash écrit par l'une se vérifie avec l'autre. Formats lus en plus : scrypt
// (versions Python sans argon2-cffi) et l'ancien SHA-256 sans sel (≤ 0.0.50),
// rehachés en argon2id dès la première connexion réussie.

const (
	passwordMinLength = 12
	passwordMaxLength = 1024
	argonTime         = 3
	argonMemory       = 65536
	argonThreads      = 4
	argonKeyLen       = 32
	argonSaltLen      = 16
)

var sha256HexRe = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)

// Un calcul argon2 occupe 64 Mio : deux à la fois au plus (rafale de tentatives
// de connexion), et la mémoire rendue au système juste après, sans attendre le
// ramasse-miettes.
var argonSlots = make(chan struct{}, 2)

func argonKey(pw, salt []byte, t, m uint32, p uint8, n uint32, id bool) []byte {
	argonSlots <- struct{}{}
	var key []byte
	if id {
		key = argon2.IDKey(pw, salt, t, m, p, n)
	} else {
		key = argon2.Key(pw, salt, t, m, p, n)
	}
	<-argonSlots
	debug.FreeOSMemory()
	return key
}

func hashPassword(pw string) string {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		panic(err)
	}
	key := argonKey([]byte(pw), salt, argonTime, argonMemory, argonThreads, argonKeyLen, true)
	b64 := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, argonMemory, argonTime, argonThreads,
		b64.EncodeToString(salt), b64.EncodeToString(key))
}

// verifyPassword → (mot de passe correct, à rehacher). Comparaisons en temps constant.
func verifyPassword(pw, stored string) (bool, bool) {
	switch {
	case strings.HasPrefix(stored, "$argon2"):
		return verifyArgon2(pw, stored)
	case strings.HasPrefix(stored, "scrypt$"):
		parts := strings.Split(stored, "$")
		if len(parts) != 6 {
			return false, false
		}
		n, e1 := strconv.Atoi(parts[1])
		r, e2 := strconv.Atoi(parts[2])
		p, e3 := strconv.Atoi(parts[3])
		salt, e4 := base64.StdEncoding.DecodeString(parts[4])
		digest, e5 := base64.StdEncoding.DecodeString(parts[5])
		if e1 != nil || e2 != nil || e3 != nil || e4 != nil || e5 != nil || n < 2 || n > 1<<20 || r < 1 || r > 32 || p < 1 || p > 16 {
			return false, false
		}
		key, err := scrypt.Key([]byte(pw), salt, n, r, p, 32)
		if err != nil {
			return false, false
		}
		ok := subtle.ConstantTimeCompare(key, digest) == 1
		return ok, ok // argon2 toujours disponible ici : scrypt est rehaché
	case sha256HexRe.MatchString(stored):
		sum := sha256.Sum256([]byte(pw))
		ok := subtle.ConstantTimeCompare([]byte(hex.EncodeToString(sum[:])), []byte(strings.ToLower(stored))) == 1
		return ok, ok
	}
	return false, false
}

// verifyArgon2 : $argon2id$v=19$m=65536,t=3,p=4$<sel>$<hash> (base64 sans remplissage).
func verifyArgon2(pw, stored string) (bool, bool) {
	parts := strings.Split(stored, "$")
	if len(parts) != 6 || parts[1] != "argon2id" && parts[1] != "argon2i" {
		return false, false
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false, false
	}
	var m uint32
	var t uint32
	var p uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil || m == 0 || t == 0 || p == 0 || m > 4<<20 || t > 100 {
		return false, false
	}
	b64 := base64.RawStdEncoding
	salt, err1 := b64.DecodeString(parts[4])
	want, err2 := b64.DecodeString(parts[5])
	if err1 != nil || err2 != nil || len(want) == 0 || len(want) > 1024 {
		return false, false
	}
	got := argonKey([]byte(pw), salt, t, m, p, uint32(len(want)), parts[1] == "argon2id")
	if subtle.ConstantTimeCompare(got, want) != 1 {
		return false, false
	}
	rehash := parts[1] != "argon2id" || m != argonMemory || t != argonTime || p != argonThreads ||
		len(want) != argonKeyLen || len(salt) != argonSaltLen
	return true, rehash
}

func needsSetup() bool { return str(cfg(), "password_hash", "") == "" }

func (l Lang) passwordProblem(pw string) string {
	n := utf8.RuneCountInString(pw)
	if n < passwordMinLength {
		return l.M(fmt.Sprintf("Le mot de passe doit faire au moins %d caractères.", passwordMinLength),
			fmt.Sprintf("The password must be at least %d characters long.", passwordMinLength))
	}
	if n > passwordMaxLength {
		return l.M(fmt.Sprintf("Le mot de passe ne doit pas dépasser %d caractères.", passwordMaxLength),
			fmt.Sprintf("The password must not exceed %d characters.", passwordMaxLength))
	}
	return ""
}

// ── Utilisateurs et permissions (ACL) ───────────────────────────────────────
// Le compte créé à la configuration initiale (username / password_hash) est le
// « propriétaire » : toujours administrateur, jamais supprimé ni désactivé, seul à
// changer ses propres identifiants dans Réglages. Les autres comptes sont dans
// cfg["users"] (identifiant aléatoire → fiche) : locaux (mot de passe) ou
// OpenID Connect. Une fiche : username, source ("local" | "oidc"), enabled,
// role ("admin" | "operator" | "viewer" | "custom"), permissions (lues seulement
// pour « custom »), instances ("*" ou liste). Les droits sont relus à chaque
// requête : modifier ou désactiver un compte agit aussitôt.

type User = map[string]any

const (
	ownerID  = "owner"
	usersMax = 100
)

type permInfo struct{ scoped, sensitive bool }

// permission → (limitable à certaines instances, sensible). « Sensible » : revient
// à donner la main sur la machine (config frp, pare-feu, mise à jour du panel…).
var permissionOrder = []string{
	"instances.view", "instances.control", "instances.create", "instances.delete", "config.read", "config.write",
	"logs.view", "ports.view", "ports.manage", "firewall.view", "firewall.manage", "updates.view", "updates.install",
	"updates.panel", "webhooks.view", "webhooks.manage", "settings.manage", "users.manage",
}

var permissions = map[string]permInfo{
	"instances.view": {true, false}, "instances.control": {true, false}, "instances.delete": {true, true},
	"config.read": {true, true}, "config.write": {true, true}, "logs.view": {true, false},
	"ports.view": {false, false}, "ports.manage": {false, true}, "firewall.view": {false, false},
	"firewall.manage": {false, true}, "updates.view": {false, false}, "updates.install": {false, true},
	"updates.panel": {false, true}, "webhooks.view": {false, false}, "webhooks.manage": {false, true},
	"settings.manage": {false, true}, "users.manage": {false, true},
	// Créer une instance : nouvelle unité systemd et nouvelle config, sans
	// instance existante à laquelle la limiter
	// Creating an instance: new systemd unit and new config, with no existing
	// instance to limit it to
	"instances.create": {false, true},
}

// Accorder la clé accorde aussi les valeurs (écrire une config suppose de la lire)
var permissionImplies = map[string][]string{
	"instances.control": {"instances.view"}, "instances.delete": {"instances.view"}, "instances.create": {"instances.view"},
	"config.read": {"instances.view"}, "config.write": {"config.read"}, "logs.view": {"instances.view"},
	"ports.view": {"instances.view"}, "ports.manage": {"ports.view"}, "firewall.view": {"instances.view"},
	"firewall.manage": {"firewall.view"}, "updates.install": {"updates.view"}, "updates.panel": {"updates.view"},
	"webhooks.manage": {"webhooks.view"},
}

var roles = map[string][]string{
	"admin": {"*"},
	"operator": {"instances.control", "config.write", "logs.view", "ports.manage",
		"firewall.view", "updates.view", "webhooks.view"},
	"viewer": {"instances.view", "logs.view", "ports.view", "firewall.view", "updates.view"},
}

var roleOrder = []string{"admin", "operator", "viewer"}

func isUserRole(r string) bool { _, ok := roles[r]; return ok || r == "custom" }

type permSet map[string]bool

func (s permSet) sorted() []string {
	out := make([]string, 0, len(s))
	for p := range s {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

func (s permSet) subsetOf(o permSet) bool {
	for p := range s {
		if !o[p] {
			return false
		}
	}
	return true
}

// expandPermissions : permissions accordées + celles qu'elles impliquent ; "*" = toutes.
func expandPermissions(perms []string) permSet {
	out := permSet{}
	todo := append([]string(nil), perms...)
	for len(todo) > 0 {
		p := todo[len(todo)-1]
		todo = todo[:len(todo)-1]
		if p == "*" {
			for k := range permissions {
				out[k] = true
			}
		} else if _, ok := permissions[p]; ok && !out[p] {
			out[p] = true
			todo = append(todo, permissionImplies[p]...)
		}
	}
	return out
}

func stringList(v any) []string {
	var out []string
	switch x := v.(type) {
	case []any:
		for _, e := range x {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
	case []string:
		out = append(out, x...)
	}
	return out
}

// userPermissions : permissions effectives d'une fiche. Limité à des instances,
// un compte ne garde que les permissions qui portent sur des instances.
func userPermissions(u User) permSet {
	role := str(u, "role", "")
	if role == "admin" {
		return expandPermissions([]string{"*"})
	}
	var granted permSet
	if r, ok := roles[role]; ok {
		granted = expandPermissions(r)
	} else {
		granted = expandPermissions(stringList(u["permissions"]))
	}
	if userInstances(u) != nil {
		for p := range granted {
			if !permissions[p].scoped {
				delete(granted, p)
			}
		}
	}
	return granted
}

// userInstances : nil = toutes les instances ; sinon l'ensemble permis.
func userInstances(u User) map[string]bool {
	ins, ok := u["instances"]
	if !ok {
		ins = "*"
	}
	if str(u, "role", "") == "admin" || ins == "*" {
		return nil
	}
	out := map[string]bool{}
	if list, ok := ins.([]any); ok {
		for _, i := range list {
			out[pyStr(i)] = true
		}
	}
	return out
}

func userCan(u User, perm string) bool { return userPermissions(u)[perm] }

func instanceAllowed(u User, iid string) bool {
	allowed := userInstances(u)
	return allowed == nil || allowed[iid]
}

// isFullAdmin : toutes les permissions sur toutes les instances. Réservé aux
// réglages qui décident de qui peut se connecter (OpenID Connect).
func isFullAdmin(u User) bool {
	return userInstances(u) == nil && len(userPermissions(u)) == len(permissions)
}

func ownerUser() User {
	c := cfg()
	return User{"id": ownerID, "username": str(c, "username", "admin"), "source": "local",
		"password_hash": str(c, "password_hash", ""), "enabled": true,
		"role": "admin", "instances": "*", "owner": true}
}

// allUsers : identifiant → fiche, propriétaire compris.
func allUsers() map[string]User {
	c := cfg()
	users := map[string]User{}
	if str(c, "password_hash", "") != "" {
		users[ownerID] = ownerUser()
	}
	for uid, v := range mapv(c, "users") {
		u, ok := v.(map[string]any)
		if !ok || uid == ownerID {
			continue
		}
		rec := make(User, len(u)+2)
		for k, val := range u {
			rec[k] = val
		}
		rec["id"] = uid
		rec["source"] = str(u, "source", "local")
		users[uid] = rec
	}
	return users
}

func sameName(a, b string) bool { return strings.EqualFold(a, b) }

// findLocalUser : fiche locale de cet identifiant (casse exacte), ou nil.
func findLocalUser(username string) User {
	var found User
	for _, u := range allUsers() {
		if u["source"] == "local" && subtle.ConstantTimeCompare([]byte(username), []byte(str(u, "username", ""))) == 1 {
			found = u
		}
	}
	return found
}

var (
	dummyHashOnce sync.Once
	dummyHashVal  string
)

// dummyHash : hash d'un mot de passe que personne ne connaît, vérifié quand
// l'identifiant n'existe pas, pour que la réponse prenne le même temps.
func dummyHash() string {
	dummyHashOnce.Do(func() { dummyHashVal = hashPassword(tokenHex(16)) })
	return dummyHashVal
}

// usersUpdate applique mutate sur une copie des comptes puis enregistre.
func usersUpdate(mutate func(users map[string]any)) error {
	cfgMu.Lock()
	defer cfgMu.Unlock()
	return usersUpdateLocked(mutate)
}

func usersUpdateLocked(mutate func(users map[string]any)) error {
	users, _ := deepCopy(mapv(cfg(), "users")).(map[string]any)
	mutate(users)
	_, err := cfgUpdateLocked(Cfg{"users": users})
	return err
}

// sessionFingerprint : empreinte du secret du compte, gardée dans la session :
// changer le mot de passe (ou lier le compte à une autre identité OIDC)
// déconnecte les sessions ouvertes.
func sessionFingerprint(u User) string {
	var material string
	if u["source"] == "oidc" {
		o := mapv(u, "oidc")
		material = "oidc\x00" + str(o, "iss", "") + "\x00" + str(o, "sub", "")
	} else {
		material = str(u, "password_hash", "")
	}
	mac := hmac.New(sha256.New, secretKey())
	mac.Write([]byte(material))
	return hex.EncodeToString(mac.Sum(nil))[:16]
}

// currentUser : fiche du compte connecté (relue dans la configuration), ou nil.
func (c *Ctx) currentUser() User {
	if c.userLoaded {
		return c.user
	}
	c.userLoaded = true
	if c.sess.data["authenticated"] != true {
		return nil
	}
	uid := c.sess.get("uid")
	if uid == "" {
		uid = ownerID
	}
	u := allUsers()[uid]
	if u != nil && boolv(u, "enabled", true) &&
		subtle.ConstantTimeCompare([]byte(c.sess.get("pwfp")), []byte(sessionFingerprint(u))) == 1 {
		// Session ouverte avec une passkey : elle se ferme quand la passkey est supprimée
		if pk := c.sess.get("pk"); pk == "" || passkeyByID(u, pk) != nil {
			c.user = u
		}
	}
	return c.user
}

// startSession : nouvelle session après authentification (pas de fixation). Le
// jeton CSRF est gardé : la page déjà ouverte continue de fonctionner.
func (c *Ctx) startSession(uid string) {
	tok := c.sess.get("csrf")
	c.sess.clear()
	c.sess.permanent = true
	c.sess.set("authenticated", true)
	c.sess.set("uid", uid)
	c.sess.set("pwfp", sessionFingerprint(allUsers()[uid]))
	if tok == "" {
		tok = tokenURLSafe(32)
	}
	c.sess.set("csrf", tok)
	c.userLoaded = false
	c.user = nil
}

func (c *Ctx) unauthenticated() {
	if strings.HasPrefix(c.R.URL.Path, "/api/") {
		c.Fail(http.StatusUnauthorized, c.M("Non authentifié", "Not authenticated"))
		return
	}
	c.Redirect("/login")
}

func (c *Ctx) forbidden(msg string) {
	if msg == "" {
		msg = c.M("Permission refusée", "Permission denied")
	}
	c.Fail(http.StatusForbidden, msg)
}

// loginRequired : compte connecté.
func loginRequired(h func(*Ctx)) func(*Ctx) {
	return func(c *Ctx) {
		if c.currentUser() == nil {
			c.unauthenticated()
			return
		}
		h(c)
	}
}

// perm : compte connecté qui possède toutes ces permissions et, si la route
// porte sur une instance ({iid}), qui a le droit d'y toucher.
func perm(perms []string, h func(*Ctx)) func(*Ctx) {
	return func(c *Ctx) {
		u := c.currentUser()
		if u == nil {
			c.unauthenticated()
			return
		}
		have := userPermissions(u)
		var missing []string
		for _, p := range perms {
			if !have[p] {
				missing = append(missing, p)
			}
		}
		if len(missing) > 0 {
			list := strings.Join(missing, ", ")
			c.forbidden(c.M("Permission refusée : "+list, "Permission denied: "+list))
			return
		}
		if iid := c.Param("iid"); iid != "" && !instanceAllowed(u, iid) {
			c.forbidden(c.M("Accès refusé à cette instance", "Access to this instance is denied"))
			return
		}
		h(c)
	}
}

func P(perms ...string) []string { return perms }

// ── Limitation des tentatives de connexion ──────────────────────────────────
// Par adresse IP : 5 essais libres, puis verrouillage de 30 s, doublé à chaque
// nouvel échec (1 h au plus). Oublié 24 h après le dernier échec ou à la
// première connexion réussie. Derrière un reverse proxy, toutes les requêtes
// arrivent de son adresse : le verrouillage vaut alors pour tout le monde.

const (
	loginFreeAttempts = 5
	loginLockBase     = 30.0
	loginLockMax      = 3600.0
	loginForgetAfter  = 86400.0
)

type loginFailure struct {
	count       int
	until, last float64
}

var (
	loginFailures = map[string]*loginFailure{}
	loginMu       sync.Mutex
	nowFunc       = func() float64 { return float64(time.Now().UnixNano()) / 1e9 }
)

func loginRetryAfter(ip string) int {
	now := nowFunc()
	loginMu.Lock()
	defer loginMu.Unlock()
	e := loginFailures[ip]
	if e == nil {
		return 0
	}
	w := int(e.until - now + 0.999)
	if w < 0 {
		return 0
	}
	return w
}

func loginFailed(ip string) {
	now := nowFunc()
	loginMu.Lock()
	defer loginMu.Unlock()
	if len(loginFailures) > 10000 {
		for k, e := range loginFailures {
			if now-e.last > loginForgetAfter {
				delete(loginFailures, k)
			}
		}
	}
	e := loginFailures[ip]
	if e == nil || now-e.last > loginForgetAfter {
		e = &loginFailure{last: now}
		loginFailures[ip] = e
	}
	e.count++
	e.last = now
	if extra := e.count - loginFreeAttempts; extra >= 0 {
		if extra > 20 {
			extra = 20
		}
		lock := loginLockBase * float64(int(1)<<extra)
		if lock > loginLockMax {
			lock = loginLockMax
		}
		e.until = now + lock
	}
}

func loginFailureCount(ip string) int {
	loginMu.Lock()
	defer loginMu.Unlock()
	if e := loginFailures[ip]; e != nil {
		return e.count
	}
	return 0
}

func loginSucceeded(ip string) {
	loginMu.Lock()
	delete(loginFailures, ip)
	loginMu.Unlock()
}

// loginRejected : échec de connexion (mot de passe ou passkey) compté pour
// cette adresse, alerte au verrouillage, réponse msg ou « trop d'essais ».
func (c *Ctx) loginRejected(ip, msg string) {
	loginFailed(ip)
	wait := loginRetryAfter(ip)
	fails := loginFailureCount(ip)
	if wait > 0 && (fails == loginFreeAttempts || fails%10 == 0) {
		webhookEmit("login.locked", [2]string{"Verrouillage de " + ip, ip + " locked out"},
			[2]string{fmt.Sprintf("%d échecs de connexion consécutifs depuis %s : adresse verrouillée %d s.", fails, ip, wait),
				fmt.Sprintf("%d failed sign-ins in a row from %s: address locked for %d s.", fails, ip, wait)},
			ip, J{"ip": ip, "failures": fails, "locked_seconds": wait})
	}
	if wait > 0 {
		c.tooManyAttempts(wait)
		return
	}
	c.Fail(http.StatusUnauthorized, msg)
}

func (c *Ctx) tooManyAttempts(wait int) {
	c.W.Header().Set("Retry-After", strconv.Itoa(wait))
	c.Fail(http.StatusTooManyRequests, c.M(fmt.Sprintf("Trop de tentatives : réessayez dans %d s.", wait),
		fmt.Sprintf("Too many attempts: try again in %d s.", wait)))
}

// truncRunes : les n premiers caractères (comme s[:n] en Python).
func truncRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n])
}

func init() {
	route("GET /login", "login_page", func(c *Ctx) {
		if c.currentUser() != nil {
			c.Redirect("/")
			return
		}
		oc := oidcConfig()
		label := str(oc, "label", "")
		if label == "" {
			label = "SSO"
		}
		errCode := c.Query("error")
		if !oidcErrors[errCode] {
			errCode = ""
		}
		c.render("login.html", J{"oidc_enabled": oidcEnabled(), "oidc_label": label, "oidc_error": errCode,
			"passkeys": anyPasskey()})
	})

	route("POST /api/login", "api_login", func(c *Ctx) {
		ip := c.RemoteIP()
		if wait := loginRetryAfter(ip); wait > 0 {
			c.tooManyAttempts(wait)
			return
		}
		data := c.Body()
		user := truncRunes(pyStrDefault(data, "username"), 256)
		pw := truncRunes(pyStrDefault(data, "password"), passwordMaxLength)
		account := findLocalUser(user)
		if account != nil && !boolv(account, "enabled", true) {
			account = nil
		}
		// Mot de passe vérifié même si l'identifiant est faux : même durée de réponse
		stored := dummyHash()
		if account != nil && str(account, "password_hash", "") != "" {
			stored = str(account, "password_hash", "")
		}
		pwOK, rehash := verifyPassword(pw, stored)
		if !pwOK || account == nil {
			c.loginRejected(ip, c.M("Identifiants incorrects", "Wrong username or password"))
			return
		}
		loginSucceeded(ip)
		uid := str(account, "id", ownerID)
		if rehash {
			// Ancien hash (SHA-256 sans sel, scrypt, ou argon2 aux anciens réglages)
			if err := setPasswordHash(uid, hashPassword(pw)); err != nil {
				log.Printf("[WARN] Rehachage du mot de passe non enregistré : %v", err)
			} else {
				log.Printf("[INFO] Mot de passe rehaché avec argon2id")
			}
		}
		c.startSession(uid)
		recordLogin(uid)
		webhookEmit("login.success", [2]string{"Connexion de " + user, user + " signed in"},
			[2]string{"Connexion au panel depuis " + ip + ".", "Panel sign-in from " + ip + "."},
			ip, J{"ip": ip, "user": user})
		c.JSON(J{"ok": true})
	})

	route("POST /api/logout", "api_logout", func(c *Ctx) {
		c.sess.clear()
		c.JSON(J{"ok": true})
	})

	route("GET /setup", "setup_page", func(c *Ctx) {
		if !needsSetup() {
			c.Redirect("/")
			return
		}
		c.render("setup.html", nil)
	})

	// Crée l'identifiant et le mot de passe, une seule fois : dès qu'un mot de
	// passe existe, cette route refuse tout.
	route("POST /api/setup", "api_setup", func(c *Ctx) {
		data := c.Body()
		user := strings.TrimSpace(pyStrDefault(data, "username"))
		pw := pyStrDefault(data, "password")
		if !usernameRe.MatchString(user) || utf8.RuneCountInString(user) > 64 {
			c.Fail(http.StatusBadRequest, c.M("Identifiant invalide (1 à 64 caractères).", "Invalid username (1 to 64 characters)."))
			return
		}
		if p := c.Lang.passwordProblem(pw); p != "" {
			c.Fail(http.StatusBadRequest, p)
			return
		}
		setupMu.Lock()
		if !needsSetup() {
			setupMu.Unlock()
			c.Fail(http.StatusConflict, c.M("Le panel est déjà configuré.", "The panel is already set up."))
			return
		}
		// Statistiques anonymes : case de la configuration initiale, décochée par défaut
		// Anonymous statistics: initial setup checkbox, unchecked by default
		stats := truthy(data["anonymous_stats"])
		_, err := cfgUpdate(Cfg{"username": user, "password_hash": hashPassword(pw), "anonymous_stats": stats})
		setupMu.Unlock()
		if err == nil && stats {
			go sendStats()
		}
		if err != nil {
			c.Fail(http.StatusInternalServerError, err.Error())
			return
		}
		c.startSession(ownerID)
		c.JSON(J{"ok": true})
	})
}

var (
	setupMu    sync.Mutex
	usernameRe = regexp.MustCompile(`^[^\x00-\x1f\x7f]+$`)
)

// pyStrDefault : str(data.get(k, "")) en Python.
func pyStrDefault(m map[string]any, k string) string {
	v, ok := m[k]
	if !ok {
		return ""
	}
	return pyStr(v)
}

// setPasswordHash : celui du propriétaire (config) ou d'un compte.
func setPasswordHash(uid, newHash string) error {
	if uid == ownerID {
		_, err := cfgUpdate(Cfg{"password_hash": newHash})
		return err
	}
	return usersUpdate(func(users map[string]any) {
		if u, ok := users[uid].(map[string]any); ok {
			u["password_hash"] = newHash
		}
	})
}

func nowISO() string { return time.Now().Format("2006-01-02T15:04:05-07:00") }

// recordLogin : date de dernière connexion d'un compte (les comptes OIDC la notent à part).
func recordLogin(uid string) {
	if uid == ownerID {
		return
	}
	if err := usersUpdate(func(users map[string]any) {
		if u, ok := users[uid].(map[string]any); ok {
			u["last_login"] = nowISO()
		}
	}); err != nil {
		log.Printf("[WARN] Dernière connexion non enregistrée : %v", err)
	}
}

// resetPasswordCLI : frp-manager --reset-password, nouveaux identifiants depuis
// la console (mot de passe perdu, ou hash illisible).
func resetPasswordCLI() int {
	l := systemLang()
	current := str(cfg(), "username", "admin")
	fmt.Print(l.M("Identifiant ["+current+"] : ", "Username ["+current+"]: "))
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	user := strings.TrimSpace(line)
	if user == "" {
		user = current
	}
	if !usernameRe.MatchString(user) || utf8.RuneCountInString(user) > 64 {
		fmt.Fprintln(os.Stderr, l.M("Identifiant invalide (1 à 64 caractères).", "Invalid username (1 to 64 characters)."))
		return 1
	}
	readPw := func(prompt string) string {
		fmt.Print(prompt)
		b, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Println()
		if err != nil {
			l2, _ := bufio.NewReader(os.Stdin).ReadString('\n')
			return strings.TrimRight(l2, "\r\n")
		}
		return string(b)
	}
	pw := readPw(l.M("Nouveau mot de passe : ", "New password: "))
	if p := l.passwordProblem(pw); p != "" {
		fmt.Fprintln(os.Stderr, p)
		return 1
	}
	if readPw(l.M("Confirmer : ", "Confirm: ")) != pw {
		fmt.Fprintln(os.Stderr, l.M("Les deux mots de passe sont différents.", "The two passwords don't match."))
		return 1
	}
	c := copyCfg(cfg())
	c["username"], c["password_hash"] = user, hashPassword(pw)
	if err := saveManagerConfig(c); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Println(l.M("Identifiants enregistrés. Redémarrez frp-manager (systemctl restart frp-manager).",
		"Credentials saved. Restart frp-manager (systemctl restart frp-manager)."))
	return 0
}
