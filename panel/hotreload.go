// Rechargement à chaud : frpc sait relire sa config via son API d'administration
// ([webServer] de sa config TOML) — frps, non : un redémarrage reste nécessaire
// pour lui. On n'applique à chaud que ce que frpc recharge vraiment, c.-à-d. les
// proxies et les visitors ; les réglages de connexion demandent un redémarrage.
// Hot reload: frpc can reread its config through its admin API (the [webServer]
// section of its TOML config) — frps cannot: it still needs a restart. Only what
// frpc really reloads is applied live, i.e. proxies and visitors; connection
// settings need a restart.

package main

import (
	"fmt"
	"net/http"
	"reflect"
	"time"
)

// proxiesOnlyChange : les deux configs ne diffèrent que par les proxies et les
// visitors (seules sections que frpc relit). Une config illisible → false.
// proxiesOnlyChange: the two configs differ only by proxies and visitors (the
// only sections frpc rereads). An unreadable config → false.
func proxiesOnlyChange(before, after string) bool {
	a, err := parseFrpConfig(before)
	if err != nil {
		return false
	}
	b, err := parseFrpConfig(after)
	if err != nil {
		return false
	}
	delete(a, "proxies")
	delete(a, "visitors")
	delete(b, "proxies")
	delete(b, "visitors")
	return reflect.DeepEqual(a, b)
}

// frpcReload : POST /api/reload sur l'API d'administration de frpc. (false, "")
// si [webServer] n'a pas de port (pas d'API à joindre) ; (false, message) si
// l'appel échoue.
// frpcReload: POST /api/reload on the frpc admin API. (false, "") when
// [webServer] has no port (no API to reach); (false, message) when the call fails.
func frpcReload(content string) (bool, string) {
	conf, err := parseFrpConfig(content)
	if err != nil {
		return false, ""
	}
	ws := mapv(conf, "webServer")
	port := tomlInt(ws["port"])
	if port == 0 {
		return false, ""
	}
	addr := str(ws, "addr", "")
	if addr == "" || addr == "0.0.0.0" || addr == "::" {
		addr = "127.0.0.1"
	}
	req, err := http.NewRequest(http.MethodPost, "http://"+urlHostPort(addr, port)+"/api/reload", nil)
	if err != nil {
		return false, short(err.Error(), 120)
	}
	if user := str(ws, "user", ""); user != "" {
		req.SetBasicAuth(user, str(ws, "password", ""))
	}
	resp, err := (&http.Client{Timeout: 8 * time.Second}).Do(req)
	if err != nil {
		return false, short(err.Error(), 120)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return false, fmt.Sprintf("HTTP %d", resp.StatusCode)
	}
	return true, ""
}

// instanceRunning : l'instance tourne (état du cache de découverte).
// instanceRunning: the instance is running (discovery cache state).
func instanceRunning(iid string) bool {
	return truthy(mapv(detectFrp(false)[iid], "status")["running"])
}

// applyConfigLive : après écriture d'une config, tente de l'appliquer sans
// redémarrer. (applied, note) ; note vide si le rechargement a réussi ou si rien
// n'a été tenté (frps, conteneur, instance arrêtée, API d'administration
// absente).
// applyConfigLive: after a config is written, try to apply it without a restart.
// (applied, note); an empty note means the reload succeeded or nothing was
// attempted (frps, container, stopped instance, no admin API).
func (l Lang) applyConfigLive(inst *Instance, before, after string) (bool, string) {
	if inst == nil || inst.Type != "frpc" || inst.Source == "docker" || inst.Config == "" || !instanceRunning(inst.ID) {
		return false, ""
	}
	if before != after && !proxiesOnlyChange(before, after) {
		return false, l.M("Les réglages de connexion ne sont pas rechargés à chaud : redémarrez frpc pour les appliquer.",
			"Connection settings are not reloaded live: restart frpc to apply them.")
	}
	if ok, msg := frpcReload(after); ok {
		return true, ""
	} else if msg != "" {
		return false, l.M("Le rechargement à chaud a échoué ("+msg+") : redémarrez frpc.",
			"The live reload failed ("+msg+"): restart frpc.")
	}
	return false, ""
}
