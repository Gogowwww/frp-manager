// Registre des sessions : une entrée par session ouverte, gardée dans la
// configuration (cfg["sessions"]) avec son compte, son adresse et son navigateur.
// Le cookie ne porte que l'identifiant : révoquer une session, c'est retirer son
// entrée, ce qui la ferme à sa requête suivante.
// Sessions registry: one entry per open session, kept in the configuration
// (cfg["sessions"]) with its account, address and browser. The cookie only
// carries the identifier: revoking a session removes its entry, which closes it
// on its next request.

package main

import (
	"log"
	"sort"
	"sync"
	"time"
)

const (
	// sessionsMax : entrées gardées par compte, les plus récentes d'abord.
	// sessionsMax: entries kept per account, most recent first.
	sessionsMax = 20
	// sessionSeenGap : intervalle minimal entre deux notes d'activité en mémoire.
	// sessionSeenGap: minimum interval between two in-memory activity marks.
	sessionSeenGap = 5 * time.Minute
	// sessionPruneMin : plancher de la marge de nettoyage (voir sessionPruneAfter).
	// sessionPruneMin: floor of the cleanup margin (see sessionPruneAfter).
	sessionPruneMin = 24 * time.Hour
)

// newSessionID : identifiant aléatoire de session (18 caractères hexadécimaux).
// newSessionID: random session identifier (18 hexadecimal characters).
func newSessionID() string { return tokenHex(9) }

// sessionPruneAfter : au-delà de cette inactivité, une entrée est retirée. Elle
// vaut toujours au moins deux fois la durée de session : un cookie encore
// valable n'est jamais révoqué par le nettoyage.
// sessionPruneAfter: past this inactivity an entry is dropped. It is always at
// least twice the session lifetime: a cookie that is still valid is never
// revoked by the cleanup.
func sessionPruneAfter() time.Duration {
	if d := 2 * sessionLifetime(); d > sessionPruneMin {
		return d
	}
	return sessionPruneMin
}

// sessionsUpdate : mutation du registre, enregistrée comme le reste de la
// configuration (même verrou : deux écritures ne se perdent pas).
// sessionsUpdate: registry mutation, saved like the rest of the configuration
// (same lock: two writes do not get lost).
func sessionsUpdate(mutate func(sessions map[string]any)) error {
	cfgMu.Lock()
	defer cfgMu.Unlock()
	sessions, _ := deepCopy(mapv(cfg(), "sessions")).(map[string]any)
	if sessions == nil {
		sessions = map[string]any{}
	}
	mutate(sessions)
	_, err := cfgUpdateLocked(Cfg{"sessions": sessions})
	return err
}

// pruneSessions : retire les entrées sans activité depuis trop longtemps et
// celles qui dépassent le plafond d'un compte.
// pruneSessions: drops entries without activity for too long and those beyond an
// account's cap.
func pruneSessions(sessions map[string]any) {
	limit := float64(time.Now().Add(-sessionPruneAfter()).Unix())
	type entry struct {
		sid, uid string
		seen     float64
	}
	list := make([]entry, 0, len(sessions))
	for sid, v := range sessions {
		rec, ok := v.(map[string]any)
		if !ok {
			delete(sessions, sid)
			continue
		}
		seen := num(rec, "seen", 0)
		if seen < limit {
			delete(sessions, sid)
			continue
		}
		list = append(list, entry{sid, str(rec, "uid", ""), seen})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].seen > list[j].seen })
	kept := map[string]int{}
	for _, e := range list {
		kept[e.uid]++
		if kept[e.uid] > sessionsMax {
			delete(sessions, e.sid)
		}
	}
}

// registerSession : entre une session dans le registre après authentification.
// registerSession: adds a session to the registry after signing in.
func registerSession(uid, sid, ip, ua string) error {
	now := time.Now()
	return sessionsUpdate(func(sessions map[string]any) {
		sessions[sid] = map[string]any{"uid": uid, "ip": truncRunes(ip, 64), "ua": truncRunes(ua, 200),
			"created": nowISO(), "seen": float64(now.Unix())}
		pruneSessions(sessions)
	})
}

// sessionOpen : la session est-elle encore ouverte pour ce compte ?
// sessionOpen: is the session still open for this account?
func sessionOpen(sid, uid string) bool {
	sessions, ok := cfg()["sessions"].(map[string]any)
	if !ok || sid == "" {
		return false
	}
	rec, ok := sessions[sid].(map[string]any)
	return ok && str(rec, "uid", "") == uid
}

var (
	touchMu   sync.Mutex
	touchLast = map[string]time.Time{}
)

// touchSession : note l'activité d'une session. En mémoire seulement : le
// fichier de configuration n'est pas réécrit à chaque requête.
// touchSession: marks a session's activity. In memory only: the configuration
// file is not rewritten on every request.
func touchSession(sid string) {
	if sid == "" {
		return
	}
	now := time.Now()
	touchMu.Lock()
	if t, ok := touchLast[sid]; ok && now.Sub(t) < sessionSeenGap {
		touchMu.Unlock()
		return
	}
	if len(touchLast) > 512 {
		touchLast = map[string]time.Time{}
	}
	touchLast[sid] = now
	touchMu.Unlock()

	cfgMu.Lock()
	defer cfgMu.Unlock()
	sessions, _ := deepCopy(mapv(cfg(), "sessions")).(map[string]any)
	rec, ok := sessions[sid].(map[string]any)
	if !ok {
		return
	}
	rec["seen"] = float64(now.Unix())
	next := copyCfg(cfg())
	next["sessions"] = sessions
	setCfg(next)
}

// closeSession : retire une session du registre.
// closeSession: removes a session from the registry.
func closeSession(sid string) error {
	return sessionsUpdate(func(sessions map[string]any) { delete(sessions, sid) })
}

// closeUserSessions : retire les sessions d'un compte, sauf except (celle de
// l'appareil courant). Renvoie le nombre fermé.
// closeUserSessions: removes an account's sessions except `except` (the current
// device's). Returns how many were closed.
func closeUserSessions(uid, except string) (int, error) {
	closed := 0
	err := sessionsUpdate(func(sessions map[string]any) {
		for sid, v := range sessions {
			rec, ok := v.(map[string]any)
			if !ok {
				delete(sessions, sid)
				continue
			}
			if sid != except && str(rec, "uid", "") == uid {
				delete(sessions, sid)
				closed++
			}
		}
	})
	return closed, err
}

// userSessions : sessions d'un compte, la plus récemment active d'abord.
// userSessions: an account's sessions, most recently active first.
func userSessions(uid, current string) []J {
	sessions := mapv(cfg(), "sessions")
	type entry struct {
		sid  string
		seen float64
	}
	list := make([]entry, 0, len(sessions))
	for sid, v := range sessions {
		rec, ok := v.(map[string]any)
		if !ok || str(rec, "uid", "") != uid {
			continue
		}
		list = append(list, entry{sid, num(rec, "seen", 0)})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].seen > list[j].seen })
	out := make([]J, 0, len(list))
	for _, e := range list {
		rec, _ := sessions[e.sid].(map[string]any)
		out = append(out, J{"id": e.sid, "current": e.sid == current, "ip": str(rec, "ip", ""),
			"ua": str(rec, "ua", ""), "created": str(rec, "created", ""), "last_seen": int64(e.seen)})
	}
	return out
}

// closeOtherSessions : ferme les autres sessions du compte connecté, celle de
// cet appareil restant ouverte. Échec d'écriture : seulement journalisé, la
// session courante reste utilisable.
// closeOtherSessions: closes the signed-in account's other sessions, leaving
// this device's open. A write failure is only logged: the current session stays
// usable.
func (c *Ctx) closeOtherSessions(uid string) int {
	closed, err := closeUserSessions(uid, c.sess.get("sid"))
	if err != nil {
		log.Printf("[WARN] %s", systemLang().M("Sessions non enregistrées : "+err.Error(), "Sessions not saved: "+err.Error()))
	}
	return closed
}
