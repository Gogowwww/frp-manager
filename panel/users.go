// Gestion des comptes : /api/me, /api/users.

package main

import (
	"fmt"
	"log"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

var (
	uidRe   = regexp.MustCompile(`^[0-9a-f]{12}$`)
	emailRe = regexp.MustCompile(`^[^@\s]{1,64}@[^@\s]{1,255}$`)
)

// userView : fiche d'un compte telle que l'interface la reçoit (jamais de hash).
func userView(u User) J {
	role := str(u, "role", "viewer")
	perms := []string{}
	if role == "custom" {
		perms = stringList(u["permissions"])
		sort.Strings(perms)
		if perms == nil {
			perms = []string{}
		}
	}
	var instances any = "*"
	if ins := userInstances(u); ins != nil {
		list := make([]string, 0, len(ins))
		for i := range ins {
			list = append(list, i)
		}
		sort.Strings(list)
		instances = list
	}
	enabled := true
	if v, ok := u["enabled"]; ok {
		enabled = truthy(v)
	}
	return J{
		"id": u["id"], "username": u["username"], "display_name": str(u, "display_name", ""),
		"email": str(u, "email", ""), "source": u["source"], "enabled": enabled,
		"owner": u["id"] == ownerID, "role": role, "permissions": perms,
		"effective": userPermissions(u).sorted(), "instances": instances,
		"last_login": u["last_login"], "created": u["created"],
		"linked": truthy(mapv(u, "oidc")["sub"]), "passkeys": len(userPasskeys(u)),
	}
}

func meView(u User) J {
	v := userView(u)
	out := J{"permissions": v["effective"], "full_admin": isFullAdmin(u)}
	for _, k := range []string{"id", "username", "display_name", "source", "owner", "role", "instances"} {
		out[k] = v[k]
	}
	return out
}

// parseAccountFields : rôle, permissions et instances d'après la saisie raw,
// complétée par la fiche base. → (champs, message d'erreur).
func (c *Ctx) parseAccountFields(raw, base map[string]any) (User, string) {
	role := str(base, "role", "viewer")
	if v, ok := raw["role"]; ok {
		s, isStr := v.(string)
		if !isStr {
			return nil, c.M("Rôle inconnu.", "Unknown role.")
		}
		role = s
	}
	if !isUserRole(role) {
		return nil, c.M("Rôle inconnu.", "Unknown role.")
	}
	var permsRaw any
	if v, ok := raw["permissions"]; ok {
		permsRaw = v
	} else if permsRaw = base["permissions"]; !truthy(permsRaw) {
		permsRaw = []any{}
	}
	permList, ok := permsRaw.([]any)
	if !ok {
		return nil, c.M("Permission inconnue.", "Unknown permission.")
	}
	permSetIn := map[string]bool{}
	for _, p := range permList {
		s, isStr := p.(string)
		if _, known := permissions[s]; !isStr || !known {
			return nil, c.M("Permission inconnue.", "Unknown permission.")
		}
		permSetIn[s] = true
	}
	var ins any = "*"
	if v, ok := base["instances"]; ok {
		ins = v
	}
	if v, ok := raw["instances"]; ok {
		ins = v
	}
	if ins != "*" {
		list, ok := ins.([]any)
		if !ok || len(list) > 200 {
			return nil, c.M("Liste d'instances invalide.", "Invalid instance list.")
		}
		set := map[string]bool{}
		for _, i := range list {
			s, isStr := i.(string)
			if !isStr || !validInstanceID(s) {
				return nil, c.M("Liste d'instances invalide.", "Invalid instance list.")
			}
			set[s] = true
		}
		ins = toAnyList(sortedKeys(set))
	}
	if role == "admin" {
		ins = "*"
	}
	perms := []any{}
	if role == "custom" {
		perms = toAnyList(sortedKeys(permSetIn))
	}
	return User{"role": role, "permissions": perms, "instances": ins}, ""
}

func toAnyList(s []string) []any {
	out := make([]any, len(s))
	for i, v := range s {
		out[i] = v
	}
	return out
}

// grantProblem : un compte qui gère les utilisateurs ne peut ni accorder des
// droits qu'il n'a pas lui-même, ni modifier un compte plus puissant que lui.
func (c *Ctx) grantProblem(actor, fields, target User) string {
	if isFullAdmin(actor) {
		return ""
	}
	have := userPermissions(actor)
	if !userPermissions(fields).subsetOf(have) {
		return c.M("Vous ne pouvez pas accorder des permissions que vous n'avez pas vous-même.",
			"You can't grant permissions you don't have yourself.")
	}
	if target != nil && !userPermissions(target).subsetOf(have) {
		return c.M("Ce compte a plus de droits que le vôtre.", "This account has more rights than yours.")
	}
	return ""
}

func nameTaken(username, exceptUID string) bool {
	for uid, u := range allUsers() {
		if uid != exceptUID && sameName(username, str(u, "username", "")) {
			return true
		}
	}
	return false
}

func cleanUsername(raw any) string {
	if raw == nil {
		return ""
	}
	name := strings.TrimSpace(pyStr(raw))
	if usernameRe.MatchString(name) && utf8.RuneCountInString(name) <= 64 {
		return name
	}
	return ""
}

// displayNameMax : longueur maximale du nom affiché (comme celui venu d'OpenID Connect).
// displayNameMax: maximum length of the display name (like the one from OpenID Connect).
const displayNameMax = 64

// cleanDisplayName : nom affiché saisi par le compte ; "" l'efface.
// cleanDisplayName: display name typed by the account; "" clears it.
func cleanDisplayName(raw any) (string, bool) {
	name := strings.TrimSpace(pyStr(raw))
	if utf8.RuneCountInString(name) > displayNameMax {
		return "", false
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return "", false
		}
	}
	return name, true
}

func cleanEmail(raw any) string {
	if raw == nil {
		return ""
	}
	email := strings.ToLower(strings.TrimSpace(pyStr(raw)))
	if utf8.RuneCountInString(email) <= 254 && emailRe.MatchString(email) {
		return email
	}
	return ""
}

func (c *Ctx) invalidUsername() {
	c.Fail(http.StatusBadRequest, c.M("Identifiant invalide (1 à 64 caractères).", "Invalid username (1 to 64 characters)."))
}

// managedTarget : fiche du compte visé par une modification, ou nil (réponse envoyée).
func (c *Ctx) managedTarget(uid string) User {
	if uid == ownerID {
		c.Fail(http.StatusBadRequest, c.M("Le compte propriétaire se gère dans Réglages.", "The owner account is managed in Settings."))
		return nil
	}
	var target User
	if uidRe.MatchString(uid) {
		target = allUsers()[uid]
	}
	if target == nil {
		c.Fail(http.StatusNotFound, c.M("Compte inconnu.", "Unknown account."))
		return nil
	}
	if uid == str(c.currentUser(), "id", "") {
		c.Fail(http.StatusBadRequest, c.M("Vous ne pouvez pas modifier votre propre compte ici : demandez à un autre administrateur.",
			"You can't change your own account here: ask another administrator."))
		return nil
	}
	return target
}

func init() {
	route("GET /api/me", "api_me", loginRequired(func(c *Ctx) {
		c.JSON(J{"ok": true, "user": meView(c.currentUser())})
	}))

	// Un compte local change son propre mot de passe (l'ancien est redemandé).
	route("POST /api/me/password", "api_me_password", loginRequired(func(c *Ctx) {
		u := c.currentUser()
		if u["source"] != "local" {
			c.Fail(http.StatusBadRequest, c.M("Ce compte se connecte par OpenID Connect : pas de mot de passe ici.",
				"This account signs in with OpenID Connect: no password here."))
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
		next := pyStrDefault(data, "new_password")
		if p := c.Lang.passwordProblem(next); p != "" {
			c.Fail(http.StatusBadRequest, p)
			return
		}
		loginSucceeded(ip)
		if err := setPasswordHash(str(u, "id", ""), hashPassword(next)); err != nil {
			c.Fail(http.StatusInternalServerError, err.Error())
			return
		}
		c.startSession(str(u, "id", "")) // cette session reste ouverte, les autres sont déconnectées
		c.closeOtherSessions(str(u, "id", ""))
		c.JSON(J{"ok": true, "msg": c.M("Mot de passe modifié.", "Password changed.")})
	}))

	// Nom affiché : chaque compte règle le sien (barre latérale, listes).
	// Display name: each account sets its own (sidebar, lists).
	route("PUT /api/me", "api_me_update", loginRequired(func(c *Ctx) {
		u := c.currentUser()
		name, ok := cleanDisplayName(c.Body()["display_name"])
		if !ok {
			c.Fail(http.StatusBadRequest, c.M(fmt.Sprintf("Nom affiché invalide (%d caractères au plus, sans caractère de contrôle).", displayNameMax),
				fmt.Sprintf("Invalid display name (%d characters at most, no control character).", displayNameMax)))
			return
		}
		uid := str(u, "id", ownerID)
		var err error
		if uid == ownerID {
			_, err = cfgUpdate(Cfg{"display_name": name})
		} else {
			err = usersUpdate(func(users map[string]any) {
				if rec, ok := users[uid].(map[string]any); ok {
					rec["display_name"] = name
				}
			})
		}
		if err != nil {
			c.Fail(http.StatusInternalServerError, err.Error())
			return
		}
		c.userLoaded = false
		c.user = nil
		c.JSON(J{"ok": true, "user": meView(allUsers()[uid]), "msg": c.M("Nom affiché enregistré.", "Display name saved.")})
	}))

	// Sessions du compte : les appareils connectés, et la révocation de l'un d'eux.
	// The account's sessions: connected devices, and revocation of one of them.
	route("GET /api/me/sessions", "api_me_sessions", loginRequired(func(c *Ctx) {
		uid := str(c.currentUser(), "id", ownerID)
		c.JSON(J{"ok": true, "sessions": userSessions(uid, c.sess.get("sid"))})
	}))

	route("DELETE /api/me/sessions/{sid}", "api_me_session_revoke", loginRequired(func(c *Ctx) {
		uid := str(c.currentUser(), "id", ownerID)
		sid := c.Param("sid")
		if sid == c.sess.get("sid") {
			c.Fail(http.StatusBadRequest, c.M("C'est la session de cet appareil : utilisez « Se déconnecter ».",
				"This is this device's session: use \"Sign out\"."))
			return
		}
		if !sessionOpen(sid, uid) {
			c.Fail(http.StatusNotFound, c.M("Session inconnue ou déjà fermée.", "Unknown or already closed session."))
			return
		}
		if err := closeSession(sid); err != nil {
			c.Fail(http.StatusInternalServerError, err.Error())
			return
		}
		c.JSON(J{"ok": true, "msg": c.M("Session fermée.", "Session closed.")})
	}))

	route("DELETE /api/me/sessions", "api_me_sessions_close", loginRequired(func(c *Ctx) {
		uid := str(c.currentUser(), "id", ownerID)
		closed := c.closeOtherSessions(uid)
		msg := c.M("Aucune autre session ouverte.", "No other open session.")
		if closed == 1 {
			msg = c.M("1 autre session fermée.", "1 other session closed.")
		} else if closed > 1 {
			msg = c.M(fmt.Sprintf("%d autres sessions fermées.", closed), fmt.Sprintf("%d other sessions closed.", closed))
		}
		c.JSON(J{"ok": true, "count": closed, "msg": msg})
	}))

	route("GET /api/users", "api_users_list", perm(P("users.manage"), func(c *Ctx) {
		detectFrp(false)
		all := allUsers()
		list := make([]User, 0, len(all))
		for _, u := range all {
			list = append(list, u)
		}
		sort.Slice(list, func(i, j int) bool {
			oi, oj := list[i]["id"] == ownerID, list[j]["id"] == ownerID
			if oi != oj {
				return oi
			}
			return strings.ToLower(str(list[i], "username", "")) < strings.ToLower(str(list[j], "username", ""))
		})
		views := make([]J, len(list))
		for i, u := range list {
			views[i] = userView(u)
		}
		perms := make([]J, len(permissionOrder))
		for i, k := range permissionOrder {
			perms[i] = J{"id": k, "scoped": permissions[k].scoped, "sensitive": permissions[k].sensitive}
		}
		rolesOut := J{}
		for k, v := range roles {
			rolesOut[k] = expandPermissions(v).sorted()
		}
		c.JSON(J{
			"ok": true, "me": c.currentUser()["id"], "users": views, "permissions": perms,
			"implies": permissionImplies, "roles": rolesOut, "instances": sortedKeys(instancesSnapshot()),
			"oidc": oidcEnabled(), "can_configure_oidc": isFullAdmin(c.currentUser()),
		})
	}))

	route("POST /api/users", "api_users_create", perm(P("users.manage"), func(c *Ctx) {
		actor := c.currentUser()
		raw := c.Body()
		source := "local"
		if v, ok := raw["source"]; ok {
			source = pyStr(v)
		}
		if source != "local" && source != "oidc" {
			c.Fail(http.StatusBadRequest, c.M("Type de compte inconnu.", "Unknown account type."))
			return
		}
		username := cleanUsername(raw["username"])
		if username == "" {
			c.invalidUsername()
			return
		}
		fields, msg := c.parseAccountFields(raw, map[string]any{})
		if msg != "" {
			c.Fail(http.StatusBadRequest, msg)
			return
		}
		if p := c.grantProblem(actor, fields, nil); p != "" {
			c.forbidden(p)
			return
		}
		enabled := true
		if v, ok := raw["enabled"]; ok {
			enabled = truthy(v)
		}
		rec := map[string]any{"username": username, "source": source, "enabled": enabled, "created": nowISO()}
		for k, v := range fields {
			rec[k] = v
		}
		if source == "local" {
			pw := pyStrDefault(raw, "password")
			if p := c.Lang.passwordProblem(pw); p != "" {
				c.Fail(http.StatusBadRequest, p)
				return
			}
			rec["password_hash"] = hashPassword(pw)
		} else {
			email := cleanEmail(raw["email"])
			if email == "" {
				c.Fail(http.StatusBadRequest, c.M("Adresse e-mail requise : le compte est lié à la première connexion OpenID Connect dont l'e-mail vérifié est celui-ci.",
					"An email address is required: the account is linked on the first OpenID Connect sign-in whose verified email matches."))
				return
			}
			rec["email"] = email
		}
		uid := tokenHex(6)
		cfgMu.Lock()
		if nameTaken(username, "") {
			cfgMu.Unlock()
			c.Fail(http.StatusConflict, c.M("Cet identifiant est déjà pris.", "This username is already taken."))
			return
		}
		if source == "oidc" {
			for _, u := range allUsers() {
				if u["source"] == "oidc" && str(u, "email", "") == rec["email"] {
					cfgMu.Unlock()
					c.Fail(http.StatusConflict, c.M("Un compte OpenID Connect utilise déjà cet e-mail.", "An OpenID Connect account already uses this email."))
					return
				}
			}
		}
		if len(mapv(cfg(), "users")) >= usersMax {
			cfgMu.Unlock()
			c.Fail(http.StatusBadRequest, c.M(fmt.Sprintf("%d comptes au plus.", usersMax), fmt.Sprintf("%d accounts at most.", usersMax)))
			return
		}
		err := usersUpdateLocked(func(users map[string]any) { users[uid] = rec })
		cfgMu.Unlock()
		if err != nil {
			c.Fail(http.StatusInternalServerError, err.Error())
			return
		}
		c.JSON(J{"ok": true, "user": userView(allUsers()[uid]), "msg": c.M("Compte créé.", "Account created.")})
	}))

	route("PUT /api/users/{uid}", "api_users_update", perm(P("users.manage"), func(c *Ctx) {
		actor := c.currentUser()
		uid := c.Param("uid")
		target := c.managedTarget(uid)
		if target == nil {
			return
		}
		raw := c.Body()
		fields, msg := c.parseAccountFields(raw, target)
		if msg != "" {
			c.Fail(http.StatusBadRequest, msg)
			return
		}
		if p := c.grantProblem(actor, fields, target); p != "" {
			c.forbidden(p)
			return
		}
		patch := map[string]any{}
		for k, v := range fields {
			patch[k] = v
		}
		if v, ok := raw["enabled"]; ok {
			patch["enabled"] = truthy(v)
		}
		if v, ok := raw["username"]; ok {
			username := cleanUsername(v)
			if username == "" {
				c.invalidUsername()
				return
			}
			patch["username"] = username
		}
		if v, ok := raw["email"]; ok && target["source"] == "oidc" && !truthy(mapv(target, "oidc")["sub"]) {
			email := cleanEmail(v)
			if email == "" {
				c.Fail(http.StatusBadRequest, c.M("Adresse e-mail invalide.", "Invalid email address."))
				return
			}
			patch["email"] = email
		}
		if truthy(raw["password"]) {
			if target["source"] != "local" {
				c.Fail(http.StatusBadRequest, c.M("Pas de mot de passe sur un compte OpenID Connect.", "No password on an OpenID Connect account."))
				return
			}
			pw := pyStr(raw["password"])
			if p := c.Lang.passwordProblem(pw); p != "" {
				c.Fail(http.StatusBadRequest, p)
				return
			}
			patch["password_hash"] = hashPassword(pw)
		}
		// Appareil perdu : l'administrateur retire les passkeys du compte
		// (les sessions ouvertes avec l'une d'elles se ferment)
		if truthy(raw["remove_passkeys"]) {
			patch["passkeys"] = []any{}
		}
		cfgMu.Lock()
		if name, ok := patch["username"].(string); ok && nameTaken(name, uid) {
			cfgMu.Unlock()
			c.Fail(http.StatusConflict, c.M("Cet identifiant est déjà pris.", "This username is already taken."))
			return
		}
		err := usersUpdateLocked(func(users map[string]any) {
			if u, ok := users[uid].(map[string]any); ok {
				for k, v := range patch {
					u[k] = v
				}
			}
		})
		cfgMu.Unlock()
		if err != nil {
			c.Fail(http.StatusInternalServerError, err.Error())
			return
		}
		// Mot de passe changé ou compte désactivé : ses sessions se retirent du
		// registre (le changement de mot de passe les ferme déjà, voir
		// sessionFingerprint).
		// Password changed or account disabled: its sessions leave the registry
		// (a password change already closes them, see sessionFingerprint).
		if _, changed := patch["password_hash"]; changed || !boolv(patch, "enabled", true) {
			if _, err := closeUserSessions(uid, ""); err != nil {
				log.Printf("[WARN] %s", systemLang().M("Sessions non fermées : "+err.Error(), "Sessions not closed: "+err.Error()))
			}
		}
		c.JSON(J{"ok": true, "user": userView(allUsers()[uid]), "msg": c.M("Compte modifié.", "Account updated.")})
	}))

	route("DELETE /api/users/{uid}", "api_users_delete", perm(P("users.manage"), func(c *Ctx) {
		uid := c.Param("uid")
		target := c.managedTarget(uid)
		if target == nil {
			return
		}
		if p := c.grantProblem(c.currentUser(), target, target); p != "" {
			c.forbidden(p)
			return
		}
		if err := usersUpdate(func(users map[string]any) { delete(users, uid) }); err != nil {
			c.Fail(http.StatusInternalServerError, err.Error())
			return
		}
		// Le compte n'existe plus : ses sessions quittent le registre.
		// The account is gone: its sessions leave the registry.
		if _, err := closeUserSessions(uid, ""); err != nil {
			log.Printf("[WARN] %s", systemLang().M("Sessions non fermées : "+err.Error(), "Sessions not closed: "+err.Error()))
		}
		c.JSON(J{"ok": true, "msg": c.M("Compte supprimé.", "Account deleted.")})
	}))
}
