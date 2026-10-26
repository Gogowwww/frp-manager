// Routes principales : page, instances, configurations, journaux (WebSocket et
// SSE), réglages du panel, surnoms, ports.

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	toml "github.com/pelletier/go-toml/v2"
)

// filterInstances : seulement les instances permises au compte.
func filterInstances[V any](items map[string]V, u User) map[string]V {
	allowed := userInstances(u)
	if allowed == nil {
		return items
	}
	out := make(map[string]V)
	for k, v := range items {
		if allowed[k] {
			out[k] = v
		}
	}
	return out
}

func nicknames() map[string]any { return mapv(cfg(), "nicknames") }

func (c *Ctx) unknownInstance(iid string) {
	c.Fail(http.StatusNotFound, c.M("Instance inconnue : "+iid, "Unknown instance: "+iid))
}

// pyInt : int(v) de Python sur une valeur JSON → (entier, ok).
func pyInt(v any) (int, bool) {
	switch x := v.(type) {
	case int:
		return x, true
	case int64:
		return int(x), true
	case json.Number:
		if i, err := strconv.Atoi(x.String()); err == nil {
			return i, true
		}
		if f, err := x.Float64(); err == nil && !math.IsInf(f, 0) && !math.IsNaN(f) {
			return int(f), true
		}
	case float64:
		if !math.IsInf(x, 0) && !math.IsNaN(x) {
			return int(x), true
		}
	case bool:
		if x {
			return 1, true
		}
		return 0, true
	case string:
		if i, err := strconv.Atoi(strings.TrimSpace(x)); err == nil {
			return i, true
		}
	}
	return 0, false
}

func init() {
	route("GET /{$}", "index", loginRequired(func(c *Ctx) {
		// État initial fourni avec la page : l'interface s'affiche sans attendre
		// d'aller-retour vers l'API (détection en cache, pas une découverte forcée).
		u := c.currentUser()
		canView := userCan(u, "instances.view")
		insts, nicks := map[string]J{}, map[string]any{}
		if canView {
			insts = filterInstances(detectFrp(false), u)
			nicks = filterInstances(nicknames(), u)
		}
		boot := J{"instances": insts, "in_docker": inDocker, "standalone": standalone, "nicknames": nicks,
			"has_password": !needsSetup(), "me": meView(u)}
		c.render("index.html", J{"boot": boot})
	}))

	route("GET /api/detect", "api_detect", perm(P("instances.view"), func(c *Ctx) {
		c.JSON(J{"ok": true, "instances": filterInstances(detectFrp(true), c.currentUser()), "in_docker": inDocker, "standalone": standalone})
	}))

	route("GET /api/status", "api_status", perm(P("instances.view"), func(c *Ctx) {
		insts := filterInstances(detectFrp(false), c.currentUser())
		st := loadState()
		c.JSON(J{"ok": true, "instances": insts, "installed_version": frpInstalledVersion(), "last_update_check": st["last_update_check"]})
	}))

	route("POST /api/service/{iid}/{action}", "api_service_action", perm(P("instances.control"), func(c *Ctx) {
		iid, action := c.Param("iid"), c.Param("action")
		detectFrp(false)
		inst := getInstance(iid)
		if inst == nil {
			c.unknownInstance(iid)
			return
		}
		notePanelAction(iid)
		if inst.Source == "docker" {
			if action != "start" && action != "stop" && action != "restart" {
				c.Fail(http.StatusBadRequest, c.M(fmt.Sprintf("Action '%s' non supportée pour les containers Docker (start/stop/restart uniquement)", action),
					fmt.Sprintf("Action '%s' not supported for Docker containers (start/stop/restart only)", action)))
				return
			}
			st, _ := dockerAPI("POST", "/containers/"+inst.ContainerName+"/"+action, nil)
			ok := st == 200 || st == 204 || st == 304
			invalidateCache(false)
			msg := "OK"
			if !ok {
				msg = c.M(fmt.Sprintf("Erreur Docker (HTTP %d)", st), fmt.Sprintf("Docker error (HTTP %d)", st))
			}
			c.JSON(J{"ok": ok, "msg": msg})
			return
		}
		switch action {
		case "start", "stop", "restart", "reload", "enable", "disable":
		default:
			c.Fail(http.StatusBadRequest, c.M("Action invalide", "Invalid action"))
			return
		}
		ok, msg := c.Lang.serviceAction(inst.Service, action)
		invalidateCache(false)
		if msg == "" {
			msg = action + " OK"
			if !ok {
				msg = action + " FAILED"
			}
		}
		c.JSON(J{"ok": ok, "msg": msg})
	}))

	// Ce que la suppression d'un conteneur emporterait (affiché dans la confirmation).
	route("GET /api/instance/{iid}/delete-info", "api_instance_delete_info", perm(P("instances.delete"), func(c *Ctx) {
		iid := c.Param("iid")
		detectFrp(false)
		inst := getInstance(iid)
		if inst == nil {
			c.unknownInstance(iid)
			return
		}
		if inst.Source != "docker" {
			c.JSON(J{"ok": true, "image": nil, "config_path": nil})
			return
		}
		st, data := dockerAPI("GET", "/containers/"+inst.ContainerName+"/json", nil)
		ins, ok := data.(map[string]any)
		if st != 200 || !ok {
			c.Fail(http.StatusNotFound, c.M(fmt.Sprintf("Conteneur introuvable (HTTP %d)", st), fmt.Sprintf("Container not found (HTTP %d)", st)))
			return
		}
		image := str(mapv(ins, "Config"), "Image", "")
		if image == "" {
			image = inst.Image
		}
		c.JSON(J{"ok": true, "image": image, "config_path": nullable(dockerMountedConfig(ins, inst.Type))})
	}))

	route("DELETE /api/instance/{iid}", "api_instance_delete", perm(P("instances.delete"), deleteInstance))
	route("POST /api/instance/reinstall/{bin}", "api_instance_reinstall", perm(P("updates.install"), reinstallInstance))

	route("GET /api/config/{iid}", "api_config_get", perm(P("config.read"), func(c *Ctx) {
		detectFrp(false)
		inst := getInstance(c.Param("iid"))
		if inst == nil {
			c.Fail(http.StatusNotFound, c.M("Instance inconnue", "Unknown instance"))
			return
		}
		if inst.Source == "docker" {
			if content, ok := dockerFrpcConfig(inst.ContainerName); ok && content != "" {
				c.JSON(J{"ok": true, "content": content, "exists": true, "docker": true})
				return
			}
			c.JSON(J{"ok": true, "content": defaultConfigs[inst.Type], "exists": false, "docker": true,
				"msg": c.M("Config non trouvée — assurez-vous que le volume /etc/frp est monté.", "Config not found: make sure the /etc/frp volume is mounted.")})
			return
		}
		b, err := os.ReadFile(inst.Config)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				c.JSON(J{"ok": true, "content": defaultConfigs[inst.Type], "exists": false})
				return
			}
			c.Fail(http.StatusInternalServerError, err.Error())
			return
		}
		c.JSON(J{"ok": true, "content": validUTF8(string(b)), "exists": true})
	}))

	// État des proxys d'un frpc (adresse publique attribuée par frps), lu sur
	// son API d'administration : rien si [webServer] n'est pas configuré.
	route("GET /api/instance/{iid}/proxies", "api_instance_proxies", perm(P("config.read"), func(c *Ctx) {
		detectFrp(false)
		inst := getInstance(c.Param("iid"))
		if inst == nil || inst.Type != "frpc" {
			c.Fail(http.StatusNotFound, c.M("Instance inconnue", "Unknown instance"))
			return
		}
		var content string
		if inst.Source == "docker" {
			content, _ = dockerFrpcConfig(inst.ContainerName)
		} else if b, err := os.ReadFile(inst.Config); err == nil {
			content = string(b)
		}
		proxies, admin, err := frpcProxyStatus(content)
		res := J{"ok": true, "admin": admin, "reachable": admin && err == nil, "proxies": proxies}
		if err != nil {
			res["error"] = short(err.Error(), 160)
		}
		c.JSON(res)
	}))

	// Active, à la demande, l'API d'administration de frpc (127.0.0.1, mot de
	// passe tiré au hasard) pour lire les ports attribués par frps ; prise en
	// compte au prochain redémarrage de frpc.
	route("POST /api/instance/{iid}/proxies/admin", "api_instance_admin", perm(P("config.write"), func(c *Ctx) {
		detectFrp(false)
		inst := getInstance(c.Param("iid"))
		if inst == nil || inst.Type != "frpc" {
			c.Fail(http.StatusNotFound, c.M("Instance inconnue", "Unknown instance"))
			return
		}
		if inst.Source == "docker" {
			c.Fail(http.StatusBadRequest, c.M("Pas pour un conteneur : le panel ne joint pas son 127.0.0.1",
				"Not for a container: the panel cannot reach its 127.0.0.1"))
			return
		}
		b, err := os.ReadFile(inst.Config)
		if err != nil {
			c.Fail(http.StatusInternalServerError, err.Error())
			return
		}
		content, changed, err := enableFrpcAdmin(string(b), freeAdminPort(inst.Config))
		if err != nil {
			c.Fail(http.StatusBadRequest, c.M("Config illisible : ", "Unreadable config: ")+err.Error())
			return
		}
		if !changed {
			c.JSON(J{"ok": true, "changed": false})
			return
		}
		done, msg, status := c.Lang.writeInstanceConfig(inst, content)
		if done {
			invalidateCache(true)
		}
		c.JSON(J{"ok": done, "changed": done, "msg": msg}, status)
	}))

	route("POST /api/config/{iid}", "api_config_save", perm(P("config.write"), func(c *Ctx) {
		iid := c.Param("iid")
		detectFrp(false)
		inst := getInstance(iid)
		if inst == nil {
			c.Fail(http.StatusNotFound, c.M("Instance inconnue", "Unknown instance"))
			return
		}
		raw, ok := c.Body()["content"]
		content := ""
		if ok {
			s, isStr := raw.(string)
			if !isStr {
				c.Fail(http.StatusBadRequest, c.M("Contenu invalide", "Invalid content"))
				return
			}
			content = s
		}
		done, msg, status := c.Lang.writeInstanceConfig(inst, content)
		if done {
			invalidateCache(true) // serverAddr, fichier créé…
			webhookEmitInstance("config.saved", [2]string{"Configuration de " + iid + " modifiée", "Configuration of " + iid + " changed"},
				[2]string{"La configuration a été enregistrée depuis le panel.", "The configuration was saved from the panel."}, iid, iid)
		}
		c.JSON(J{"ok": done, "msg": msg}, status)
	}))

	route("GET /api/logs/{iid}", "api_logs", perm(P("logs.view"), func(c *Ctx) {
		detectFrp(false)
		inst := getInstance(c.Param("iid"))
		if inst == nil {
			c.JSON(J{"ok": false}, http.StatusNotFound)
			return
		}
		if inst.Source == "docker" {
			content := dockerLogsRaw(inst.ContainerName, 200)
			if content == "" {
				// Repli : l'id du conteneur
				if st, data := dockerAPI("GET", "/containers/"+inst.ContainerName+"/json", nil); st == 200 {
					if obj, ok := data.(map[string]any); ok {
						id := str(obj, "Id", "")
						if len(id) > 12 {
							id = id[:12]
						}
						content = dockerLogsRaw(id, 200)
					}
				}
			}
			if content == "" {
				content = c.M("(aucun log disponible)", "(no logs available)")
			}
			c.JSON(J{"ok": true, "content": content})
			return
		}
		if c.Query("source") == "file" {
			// runHost : en mode Docker, le fichier de log est sur l'hôte
			ok, out, e := runHost([]string{"tail", "-n200", inst.Log}, 15*time.Second, nil)
			if !ok {
				out = e
				if out == "" {
					out = c.M("Fichier illisible : "+inst.Log, "Unreadable file: "+inst.Log)
				}
			}
			c.JSON(J{"ok": true, "content": validUTF8(out)})
			return
		}
		if inst.Source == "standalone" {
			// Pas de journal systemd : la sortie du programme lancé par le panel
			// No systemd journal: the output of the program run by the panel
			c.JSON(J{"ok": true, "content": c.Lang.standaloneConsole(inst.ID, 200)})
			return
		}
		if !validServiceName(inst.Service) {
			c.Fail(http.StatusBadRequest, c.M("Nom de service refusé", "Service name rejected"))
			return
		}
		ok, out, e := runCmd([]string{"journalctl", "-u", inst.Service, "-n200", "--no-pager", "-o", "short-iso"})
		if !ok {
			out = e
		}
		c.JSON(J{"ok": true, "content": validUTF8(out)})
	}))

	// Journal en direct en SSE : repli quand le WebSocket n'est pas disponible.
	route("GET /api/logs/stream/{iid}", "api_logs_stream", perm(P("logs.view"), func(c *Ctx) {
		detectFrp(false)
		inst := getInstance(c.Param("iid"))
		if inst == nil {
			c.Fail(http.StatusNotFound, "Instance inconnue")
			return
		}
		source, history := liveLogParams(c)
		ctx, cancel := context.WithCancel(c.R.Context())
		defer cancel()
		lines := liveLogLines(ctx, c.Lang, inst, source, history)
		h := c.W.Header()
		h.Set("Content-Type", "text/event-stream; charset=utf-8")
		h.Set("Cache-Control", "no-cache")
		h.Set("X-Accel-Buffering", "no")
		c.W.WriteHeader(http.StatusOK)
		c.W.Flush()
		keep := time.NewTicker(keepaliveInterval)
		defer keep.Stop()
		for {
			select {
			case line, ok := <-lines:
				if !ok {
					return
				}
				if _, err := fmt.Fprintf(c.W, "data: %s\n\n", line); err != nil {
					return
				}
				c.W.Flush()
				keep.Reset(keepaliveInterval)
			case <-keep.C:
				// Commentaire SSE, ignoré par le navigateur
				if _, err := c.W.Write([]byte(": keepalive\n\n")); err != nil {
					return
				}
				c.W.Flush()
			case <-ctx.Done():
				return
			}
		}
	}))

	route("GET /ws/status", "ws_status", wsStatus)
	route("GET /ws/logs/{iid}", "ws_logs", wsLogs)

	route("GET /api/manager/config", "api_manager_config_get", perm(P("settings.manage"), func(c *Ctx) {
		safe := J{}
		for k, v := range cfg() {
			switch k {
			case "password_hash", "secret_key", "webhooks", "users", "oidc", "firewall", ownerPasskeysKey:
			default:
				safe[k] = v
			}
		}
		safe["is_owner"] = c.currentUser()["id"] == ownerID
		safe["has_password"] = !needsSetup()
		// Contenu exact des statistiques anonymes, montré avant de les activer
		// Exact content of the anonymous statistics, shown before turning them on
		safe["anonymous_stats_payload"] = statsPayload()
		c.JSON(J{"ok": true, "config": safe})
	}))

	route("POST /api/manager/config", "api_manager_config_set", perm(P("settings.manage"), func(c *Ctx) {
		data := c.Body()
		patch := Cfg{}
		_, hasUser := data["username"]
		if (hasUser || truthy(data["new_password"])) && c.currentUser()["id"] != ownerID {
			c.forbidden(c.M("Seul le propriétaire change ses identifiants ici.", "Only the owner changes their credentials here."))
			return
		}
		if hasUser {
			user := strings.TrimSpace(pyStr(data["username"]))
			if user == "" || !usernameRe.MatchString(user) || len([]rune(user)) > 64 {
				c.invalidUsername()
				return
			}
			if nameTaken(user, ownerID) {
				c.Fail(http.StatusConflict, c.M("Cet identifiant est déjà pris.", "This username is already taken."))
				return
			}
			patch["username"] = user
		}
		if v, ok := data["bind_host"]; ok {
			host := strings.TrimSpace(pyStr(v))
			if _, err := netip.ParseAddr(host); err != nil {
				c.Fail(http.StatusBadRequest, c.M("Adresse d'écoute invalide (ex. 127.0.0.1, 0.0.0.0 ou ::).", "Invalid listening address (e.g. 127.0.0.1, 0.0.0.0 or ::)."))
				return
			}
			patch["bind_host"] = host
		}
		badRange := func() {
			c.Fail(http.StatusBadRequest, c.M("Port (1 à 65535) ou durée de session (5 min à 30 jours) invalide.",
				"Invalid port (1 to 65535) or session length (5 min to 30 days)."))
		}
		if v, ok := data["bind_port"]; ok {
			p, ok := pyInt(v)
			if !ok || p < 1 || p > 65535 {
				badRange()
				return
			}
			patch["bind_port"] = float64(p)
		}
		if v, ok := data["session_timeout"]; ok {
			s, ok := pyInt(v)
			if !ok || s < 300 || s > 30*86400 {
				badRange()
				return
			}
			patch["session_timeout"] = float64(s)
		}
		if v, ok := data["ssl_enabled"]; ok {
			patch["ssl_enabled"] = truthy(v)
		}
		if v, ok := data["download_mirrors"]; ok {
			patch["download_mirrors"] = truthy(v)
		}
		if v, ok := data["anonymous_stats"]; ok {
			patch["anonymous_stats"] = truthy(v)
		}
		newPw := truthy(data["new_password"])
		if newPw {
			pw := pyStr(data["new_password"])
			if p := c.Lang.passwordProblem(pw); p != "" {
				c.Fail(http.StatusBadRequest, p)
				return
			}
			patch["password_hash"] = hashPassword(pw)
		}
		if _, err := cfgUpdate(patch); err != nil {
			c.Fail(http.StatusInternalServerError, err.Error())
			return
		}
		if newPw {
			c.startSession(ownerID) // cette session reste ouverte, les autres sont déconnectées
		}
		if truthy(patch["anonymous_stats"]) {
			// Premier signal tout de suite, sans attendre la boucle horaire
			// First ping right away, without waiting for the hourly loop
			go sendStats()
		}
		c.JSON(J{"ok": true, "msg": c.M("Sauvegardé. Redémarrez frp-manager pour appliquer bind_host/port.",
			"Saved. Restart frp-manager to apply bind_host/port.")})
	}))

	route("GET /api/nicknames", "api_nicknames_get", perm(P("instances.view"), func(c *Ctx) {
		c.JSON(J{"ok": true, "nicknames": filterInstances(nicknames(), c.currentUser())})
	}))

	route("POST /api/nickname/{iid}", "api_nickname_set", perm(P("instances.control"), func(c *Ctx) {
		iid := c.Param("iid")
		if !validInstanceID(iid) {
			c.Fail(http.StatusBadRequest, c.M("Instance invalide", "Invalid instance"))
			return
		}
		nick := truncRunes(strings.TrimSpace(pyStrDefault(c.Body(), "nickname")), 64)
		cfgMu.Lock()
		nicks := copyCfg(nicknames())
		if nick != "" {
			nicks[iid] = nick
		} else {
			delete(nicks, iid)
		}
		_, err := cfgUpdateLocked(Cfg{"nicknames": nicks})
		cfgMu.Unlock()
		if err != nil {
			c.Fail(http.StatusInternalServerError, err.Error())
			return
		}
		c.JSON(J{"ok": true, "msg": c.M("Surnom mis à jour", "Nickname updated")})
	}))

	route("GET /api/ports", "api_ports", perm(P("ports.view"), func(c *Ctx) {
		var ports []J
		insts := detectFrp(false)
		for _, iid := range sortedKeys(insts) {
			inst := insts[iid]
			typ, _ := inst["type"].(string)
			if inst["source"] != "docker" {
				cfgPath, _ := inst["config_path"].(string)
				if cfgPath == "" || inst["config_exists"] != true {
					continue
				}
				b, err := os.ReadFile(cfgPath)
				if err != nil {
					continue
				}
				for _, p := range extractPortsFromConfig(string(b), typ) {
					p["iid"], p["type"], p["service"] = iid, typ, inst["service"]
					ports = append(ports, p)
				}
			} else if typ == "frpc" {
				name, _ := inst["container_name"].(string)
				if name == "" {
					name = iid
				}
				if content, ok := dockerFrpcConfig(name); ok && content != "" {
					for _, p := range extractPortsFromConfig(content, "frpc") {
						p["iid"], p["type"], p["service"], p["source"] = iid, "frpc", name, "docker"
						ports = append(ports, p)
					}
				}
			}
		}
		ufwOK, allowed := ufwAllowedPorts()
		for _, p := range ports {
			port, proto := p["port"].(int), p["proto"].(string)
			p["ufw_allowed"] = allowed[fmt.Sprintf("%d/%s", port, proto)] || allowed[fmt.Sprintf("%d/any", port)]
		}
		if ports == nil {
			ports = []J{}
		}
		c.JSON(J{"ok": true, "ports": c.Lang.withPortLabels(ports), "ufw_available": ufwOK})
	}))

	route("POST /api/ports/open", "api_ports_open", perm(P("ports.manage"), func(c *Ctx) {
		data := c.Body()
		var raw any = json.Number("0")
		if v, ok := data["port"]; ok {
			raw = v
		}
		port, ok := pyInt(raw)
		if !ok || port < 1 || port > 65535 {
			c.Fail(http.StatusBadRequest, c.M("Port invalide", "Invalid port"))
			return
		}
		proto := "tcp"
		if v, ok := data["proto"].(string); ok {
			proto = strings.ToLower(v)
		}
		if proto != "tcp" && proto != "udp" {
			proto = "tcp"
		}
		done, out, e := runCmd([]string{"ufw", "allow", fmt.Sprintf("%d/%s", port, proto)})
		msg := strings.TrimSpace(out)
		if msg == "" {
			msg = strings.TrimSpace(e)
		}
		if msg == "" {
			msg = fmt.Sprintf("Port %d/%s ", port, proto) + map[bool]string{true: c.M("ouvert", "opened"), false: c.M("erreur", "error")}[done]
		}
		c.JSON(J{"ok": done, "msg": msg})
	}))
}

// frpInstalledVersion : state.json, sinon « frps --version » ou « frpc --version ».
func frpInstalledVersion() any {
	if standalone {
		// Version du programme réellement lancé : celle de l'image peut avoir
		// changé depuis la dernière mise à jour faite depuis le panel
		// Version of the program actually run: the image's one may have changed
		// since the last update made from the panel
		for _, name := range frpBinaries {
			if v := semverOf(readVersion(findBinary(name))); v != "" {
				return v
			}
		}
		return nil
	}
	st := loadState()
	if v := st["installed_version"]; truthy(v) {
		return v
	}
	re := regexp.MustCompile(`(\d+\.\d+\.\d+)`)
	for _, name := range frpBinaries {
		for _, d := range binarySearchPaths {
			b := filepath.Join(d, name)
			if !isExecutable(b) {
				continue
			}
			if ok, out, _ := runCmd([]string{b, "--version"}); ok && strings.TrimSpace(out) != "" {
				if m := re.FindString(out); m != "" {
					st["installed_version"] = m
					saveState(st)
					return m
				}
			}
		}
	}
	return nil
}

// ── Suppression d'une instance ──────────────────────────────────────────────

// Seules les unités créées par l'admin sont supprimées ; celles d'un paquet
// (/usr/lib, /lib) seraient réinstallées à la prochaine mise à jour.
var unitDir = "/etc/systemd/system/"

var configSuffixes = []string{".toml", ".ini", ".yaml", ".yml"}

func hasConfigSuffix(name string) bool {
	for _, s := range configSuffixes {
		if strings.HasSuffix(name, s) {
			return true
		}
	}
	return false
}

// forgetInstance oublie le surnom d'une instance supprimée ; une config
// conservée est écartée de la détection.
func forgetInstance(iid, keptConfig string) {
	cfgMu.Lock()
	defer cfgMu.Unlock()
	nicks := copyCfg(nicknames())
	_, changed := nicks[iid]
	delete(nicks, iid)
	dismissed := toAnyList(stringList(cfg()["dismissed_configs"]))
	if keptConfig != "" {
		found := false
		for _, d := range dismissed {
			if d == keptConfig {
				found = true
			}
		}
		if !found {
			dismissed = append(dismissed, keptConfig)
			changed = true
		}
	}
	if changed {
		cfgUpdateLocked(Cfg{"nicknames": nicks, "dismissed_configs": dismissed})
	}
}

type dockerMount struct{ src, dest string }

// dockerMountedConfig : chemin HÔTE du fichier de config monté dans un
// conteneur, ou "". Strict : il sert à supprimer le fichier.
func dockerMountedConfig(inspect map[string]any, binType string) string {
	var mounts []dockerMount
	for _, item := range listv(inspect, "Mounts") {
		m, _ := item.(map[string]any)
		if str(m, "Type", "") == "bind" && str(m, "Source", "") != "" && str(m, "Destination", "") != "" {
			mounts = append(mounts, dockerMount{str(m, "Source", ""), str(m, "Destination", "")})
		}
	}
	// 1. Le -c/--config du conteneur, traduit via ses montages
	args := append(stringList(mapv(inspect, "Config")["Cmd"]), stringList(inspect["Args"])...)
	sorted := append([]dockerMount(nil), mounts...)
	sort.SliceStable(sorted, func(i, j int) bool { return len(sorted[i].dest) > len(sorted[j].dest) })
	for i, arg := range args {
		target := ""
		if (arg == "-c" || arg == "--config") && i+1 < len(args) {
			target = args[i+1]
		} else if strings.HasPrefix(arg, "--config=") {
			target = strings.SplitN(arg, "=", 2)[1]
		}
		if target == "" {
			continue
		}
		for _, m := range sorted {
			dest := strings.TrimRight(m.dest, "/")
			if target == dest {
				return m.src
			}
			if strings.HasPrefix(target, dest+"/") {
				return strings.TrimRight(m.src, "/") + target[len(dest):]
			}
		}
	}
	// 2. Un fichier monté seul, au nom du type (frpc.toml, frps.ini…)
	for _, m := range mounts {
		name := strings.ToLower(path.Base(m.src))
		if strings.HasPrefix(name, binType) && hasConfigSuffix(name) {
			return m.src
		}
	}
	return ""
}

// configUsers : autres instances frp qui utilisent ce fichier de config.
func configUsers(p, exceptIID string) []string {
	var users []string
	insts := instancesSnapshot()
	for _, o := range sortedKeys(insts) {
		other := insts[o]
		if o == exceptIID {
			continue
		}
		if other.Source == "docker" {
			st, data := dockerAPI("GET", "/containers/"+other.ContainerName+"/json", nil)
			if ins, ok := data.(map[string]any); st == 200 && ok && dockerMountedConfig(ins, other.Type) == p {
				users = append(users, o)
			}
		} else if other.Config != "" && other.Config == p {
			users = append(users, o)
		}
	}
	return users
}

func (l Lang) deleteContainer(iid string, inst *Instance, deleteImage, deleteConfig bool) (bool, string, int) {
	container := inst.ContainerName
	st, data := dockerAPI("GET", "/containers/"+container+"/json", nil)
	ins, ok := data.(map[string]any)
	if st != 200 || !ok {
		return false, l.M(fmt.Sprintf("Conteneur introuvable (HTTP %d)", st), fmt.Sprintf("Container not found (HTTP %d)", st)), 404
	}
	imageID := str(ins, "Image", "")
	imageName := str(mapv(ins, "Config"), "Image", "")
	if imageName == "" {
		imageName = imageID
	}
	conf := ""
	if deleteConfig {
		conf = dockerMountedConfig(ins, inst.Type)
		if conf == "" {
			return false, l.M("Aucun fichier de configuration monté n'a été trouvé pour ce conteneur.",
				"No mounted configuration file was found for this container."), 400
		}
		if shared := configUsers(conf, iid); len(shared) > 0 {
			list := strings.Join(shared, ", ")
			return false, l.M(conf+" est aussi utilisé par "+list+" : rien n'a été supprimé.", conf+" is also used by "+list+": nothing was deleted."), 400
		}
	}
	status, resp := dockerAPI("DELETE", "/containers/"+container+"?force=true", nil)
	if status != 204 && status != 404 {
		detail := ""
		if obj, ok := resp.(map[string]any); ok {
			detail = str(obj, "message", "")
		} else if s, ok := resp.(string); ok {
			detail = s
		}
		return false, strings.TrimSpace(l.M(fmt.Sprintf("Erreur Docker (HTTP %d) : %s", status, detail),
			fmt.Sprintf("Docker error (HTTP %d): %s", status, detail))), 500
	}
	done := []string{l.M("conteneur "+container, "container "+container)}
	var notes []string
	if deleteImage && imageID != "" {
		status, _ := dockerAPI("DELETE", "/images/"+imageID, nil, 60*time.Second)
		switch {
		case status == 200:
			done = append(done, "image "+imageName)
		case status == 409:
			notes = append(notes, l.M("image "+imageName+" conservée : un autre conteneur l'utilise", "image "+imageName+" kept: another container uses it"))
		case status != 404:
			notes = append(notes, l.M(fmt.Sprintf("image %s non supprimée (HTTP %d)", imageName, status), fmt.Sprintf("image %s not deleted (HTTP %d)", imageName, status)))
		}
	}
	if conf != "" {
		if hostRemoveFile(conf) {
			done = append(done, conf)
		} else {
			notes = append(notes, l.M("impossible de supprimer "+conf, "could not delete "+conf))
		}
	}
	msg := l.M("Supprimé : "+strings.Join(done, ", "), "Deleted: "+strings.Join(done, ", "))
	if len(notes) > 0 {
		msg += " — " + strings.Join(notes, " ; ")
	}
	return true, msg, 200
}

func deleteInstance(c *Ctx) {
	iid := c.Param("iid")
	detectFrp(true)
	inst := getInstance(iid)
	if inst == nil {
		c.unknownInstance(iid)
		return
	}
	opts := c.Body()
	deleteConfig := truthy(opts["delete_config"])
	if inst.Source == "docker" {
		ok, msg, code := c.Lang.deleteContainer(iid, inst, truthy(opts["delete_image"]), deleteConfig)
		invalidateCache(true)
		if ok {
			forgetInstance(iid, "")
		}
		c.JSON(J{"ok": ok, "msg": msg}, code)
		return
	}
	if inst.Source == "standalone" {
		// Arrêté et retiré du démarrage automatique ; pas d'unité à supprimer
		// Stopped and removed from automatic start; no unit to delete
		standaloneForget(iid)
	}
	unit := inst.Service + ".service"
	_, fragment, _ := runCmd([]string{"systemctl", "show", unit, "--property=FragmentPath", "--value"})
	if fragment = strings.TrimSpace(fragment); fragment != "" {
		fragment = path.Clean(fragment)
	}
	if !validServiceName(inst.Service) {
		c.Fail(http.StatusBadRequest, c.M("Nom de service refusé", "Service name rejected"))
		return
	}
	if fragment != "" && !strings.HasPrefix(fragment, unitDir) {
		c.Fail(http.StatusBadRequest, c.M(fragment+" appartient à un paquet système : supprimez-le avec le gestionnaire de paquets.",
			fragment+" belongs to a system package: remove it with the package manager."))
		return
	}
	conf := inst.Config
	if conf != "" && deleteConfig {
		if shared := configUsers(conf, iid); len(shared) > 0 {
			list := strings.Join(shared, ", ")
			c.Fail(http.StatusBadRequest, c.M(conf+" est aussi utilisé par "+list+" : il n'a pas été supprimé.", conf+" is also used by "+list+": it was not deleted."))
			return
		}
	}
	var done []string
	if fragment != "" {
		runCmd([]string{"systemctl", "disable", "--now", unit}, 30*time.Second)
		if !hostRemoveFile(fragment) {
			invalidateCache(true)
			c.Fail(http.StatusInternalServerError, c.M("Service arrêté, mais impossible de supprimer "+fragment, "Service stopped, but "+fragment+" could not be deleted"))
			return
		}
		runHost([]string{"rm", "-rf", unitDir + unit + ".d"}, 15*time.Second, nil)
		runCmd([]string{"systemctl", "daemon-reload"})
		runCmd([]string{"systemctl", "reset-failed", unit})
		done = append(done, "service "+unit)
	}
	if deleteConfig && conf != "" && fileExists(conf) {
		if err := os.Remove(conf); err != nil {
			invalidateCache(true)
			c.Fail(http.StatusInternalServerError, c.M("Service supprimé, mais pas "+conf+" : "+err.Error(), "Service deleted, but not "+conf+": "+err.Error()))
			return
		}
		done = append(done, conf)
	}
	kept := ""
	if conf != "" && fileExists(conf) {
		kept = conf
	}
	if kept != "" && len(done) == 0 {
		done = append(done, c.M(iid+" retiré du tableau de bord ("+kept+" conservé)", iid+" removed from the dashboard ("+kept+" kept)"))
	}
	forgetInstance(iid, kept)
	invalidateCache(true)
	list := strings.Join(done, ", ")
	c.JSON(J{"ok": true, "msg": c.M("Supprimé : "+list, "Deleted: "+list)})
}

// ── État des proxys d'un frpc ───────────────────────────────────────────────

// frpcProxyStatus interroge l'API d'administration de frpc ([webServer] de sa
// config TOML) → (état par nom de proxy, API configurée, erreur). remote_addr
// est l'adresse publique, port attribué par frps compris (remotePort = 0).
func frpcProxyStatus(content string) (map[string]J, bool, error) {
	var conf map[string]any
	if toml.Unmarshal([]byte(frpTOML(content)), &conf) != nil {
		return nil, false, nil
	}
	ws := mapv(conf, "webServer")
	port := tomlInt(ws["port"])
	if port == 0 {
		return nil, false, nil
	}
	addr := str(ws, "addr", "")
	if addr == "" || addr == "0.0.0.0" || addr == "::" {
		addr = "127.0.0.1"
	}
	req, _ := http.NewRequest(http.MethodGet, "http://"+netJoin(addr, port)+"/api/status", nil)
	if user := str(ws, "user", ""); user != "" {
		req.SetBasicAuth(user, str(ws, "password", ""))
	}
	resp, err := (&http.Client{Timeout: 3 * time.Second}).Do(req)
	if err != nil {
		return nil, true, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, true, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var byType map[string][]map[string]any
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&byType); err != nil {
		return nil, true, err
	}
	out := map[string]J{}
	for _, list := range byType {
		for _, p := range list {
			if name := str(p, "name", ""); name != "" {
				out[name] = J{"status": str(p, "status", ""), "err": str(p, "err", ""), "remote_addr": str(p, "remote_addr", "")}
			}
		}
	}
	return out, true, nil
}

// enableFrpcAdmin ajoute à une config frpc un [webServer] local (clés à points,
// avant la première table pour rester au niveau racine). Rien à faire si un
// port est déjà configuré ; refus si [webServer] existe sans port.
func enableFrpcAdmin(content string, port int) (string, bool, error) {
	var conf map[string]any
	if err := toml.Unmarshal([]byte(frpTOML(content)), &conf); err != nil {
		return "", false, err
	}
	if _, ok := conf["webServer"]; ok {
		if tomlInt(mapv(conf, "webServer")["port"]) != 0 {
			return content, false, nil
		}
		return "", false, errors.New("webServer sans port")
	}
	block := fmt.Sprintf("# Tableau de bord local de frpc : FRP-Manager y lit les ports attribués par le serveur\n"+
		"webServer.addr = \"127.0.0.1\"\nwebServer.port = %d\nwebServer.user = \"frp-manager\"\nwebServer.password = %q\n\n",
		port, tokenHex(16))
	lines := strings.SplitAfter(content, "\n")
	for i, line := range lines {
		// Avant la première table, ou avant la ligne « {{- range … }} » qui la
		// précède : dans la boucle, le bloc serait répété
		// Before the first table, or before the "{{- range … }}" line in front of
		// it: inside the loop, the block would be repeated
		if s := strings.TrimSpace(line); strings.HasPrefix(s, "[") || strings.HasPrefix(s, "{{") {
			return strings.Join(lines[:i], "") + block + strings.Join(lines[i:], ""), true, nil
		}
	}
	if content != "" && !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	if content != "" && !strings.HasSuffix(content, "\n\n") {
		content += "\n"
	}
	return content + strings.TrimSuffix(block, "\n"), true, nil
}

// freeAdminPort : port libre sur 127.0.0.1 à partir de 7400 (port par défaut
// de frpc), hors ceux des autres configs frpc, même arrêtées.
func freeAdminPort(self string) int {
	taken := map[int]bool{}
	for _, inst := range instancesSnapshot() {
		if inst.Type != "frpc" || inst.Config == "" || inst.Config == self {
			continue
		}
		var conf map[string]any
		if b, err := os.ReadFile(inst.Config); err == nil && toml.Unmarshal([]byte(frpTOML(string(b))), &conf) == nil {
			taken[tomlInt(mapv(conf, "webServer")["port"])] = true
		}
	}
	for p := 7400; p < 7500; p++ {
		if taken[p] {
			continue
		}
		if ln, err := net.Listen("tcp", netJoin("127.0.0.1", p)); err == nil {
			ln.Close()
			return p
		}
	}
	return 7400
}

// ── Réinstallation d'un service supprimé ────────────────────────────────────
// Recrée frps.service ou frpc.service comme install.sh, sur /etc/frp/<bin>.toml :
// la config conservée à la suppression, sinon celle par défaut. Le programme
// est retéléchargé s'il manque. Le service est créé arrêté et non activé.

const frpUnitTemplate = `[Unit]
Description=frp %s (%s)
After=network.target

[Service]
Type=simple
Restart=on-failure
RestartSec=5s
ExecStart=%s -c %s
LimitNOFILE=1048576

[Install]
WantedBy=multi-user.target
`

// installMissingBinary : télécharge et installe le programme bin, dans la
// version de frp installée (la dernière si elle est inconnue).
var installMissingBinary = func(l Lang, bin, installed string) bool {
	version, tag := reinstallVersion(installed)
	if version == "" {
		version, tag, _ = fetchLatestVersion(l)
		if version == "" {
			updateLog.add(l.M("[ERROR] Toutes les sources inaccessibles.", "[ERROR] All sources unreachable."))
			return false
		}
	}
	updateLog.add(fmt.Sprintf("[INFO] Version: %s", tag))
	return downloadAndInstall(l, version, tag, []string{bin})
}

func reinstallInstance(c *Ctx) {
	bin := c.Param("bin")
	if bin != "frps" && bin != "frpc" {
		c.Fail(http.StatusBadRequest, c.M("Programme inconnu (frps ou frpc)", "Unknown program (frps or frpc)"))
		return
	}
	if inDocker && !standalone {
		c.Fail(http.StatusBadRequest, c.M("Sous Docker, les services frp sont des conteneurs.", "Under Docker, frp services are containers."))
		return
	}
	if serviceExists(bin) {
		c.Fail(http.StatusConflict, c.M("Le service "+bin+".service existe déjà.", "The "+bin+".service service already exists."))
		return
	}
	if !updateMu.TryLock() {
		c.JSON(J{"ok": false, "msg": c.M("Mise à jour déjà en cours", "An update is already running")})
		return
	}
	updateLog.reset()
	l := c.Lang
	installed := pyStr(loadState()["installed_version"])
	go func() {
		defer updateMu.Unlock()
		updateLog.finish(reinstallService(l, bin, installed))
	}()
	c.JSON(J{"ok": true})
}

// serviceExists : une unité bin.service est-elle connue de systemd ?
func serviceExists(bin string) bool {
	if standalone {
		return standaloneInstances()[bin] != nil
	}
	if fileExists(unitDir + bin + ".service") {
		return true
	}
	_, out, _ := runCmd([]string{"systemctl", "list-unit-files", bin + ".service", "--no-pager", "--plain", "--no-legend"})
	return strings.HasPrefix(strings.TrimSpace(out), bin+".service")
}

func reinstallService(l Lang, bin, installed string) bool {
	logf := updateLog.add
	unit := bin + ".service"
	program := findBinary(bin)
	if program == "" {
		logf(l.M("[INFO] Programme "+bin+" introuvable : téléchargement.", "[INFO] "+bin+" program not found: downloading it."))
		if !installMissingBinary(l, bin, installed) {
			return false
		}
		program = filepath.Join(frpBinDir, bin)
	}
	logf(l.M("[INFO] Programme : "+program, "[INFO] Program: "+program))
	conf := filepath.Join(frpConfDir, bin+".toml")
	if fileExists(conf) {
		logf(l.M("[INFO] Configuration conservée : "+conf, "[INFO] Kept configuration: "+conf))
	} else {
		os.MkdirAll(frpConfDir, 0o755)
		if err := os.WriteFile(conf, []byte(defaultConfigs[bin]), 0o600); err != nil {
			logf("[ERROR] " + err.Error())
			return false
		}
		logf(l.M("[INFO] Configuration par défaut créée : "+conf, "[INFO] Default configuration created: "+conf))
	}
	os.MkdirAll(frpLogDir, 0o755)
	if standalone {
		// Pas d'unité systemd : la config suffit, le panel lance le programme
		// No systemd unit: the config is enough, the panel runs the program
		undismissConfig(conf)
		invalidateCache(true)
		logf(l.M("[OK] "+bin+" recréé, arrêté : démarrez-le depuis sa carte.", "[OK] "+bin+" recreated, stopped: start it from its card."))
		return true
	}
	role := map[string]string{"frps": "server", "frpc": "client"}[bin]
	if err := os.WriteFile(unitDir+unit, []byte(fmt.Sprintf(frpUnitTemplate, role, bin, program, conf)), 0o644); err != nil {
		logf("[ERROR] " + err.Error())
		return false
	}
	runCmd([]string{"systemctl", "daemon-reload"})
	logf(l.M("[INFO] Service créé : "+unitDir+unit, "[INFO] Service created: "+unitDir+unit))
	undismissConfig(conf)
	invalidateCache(true)
	logf(l.M("[OK] "+unit+" réinstallé, arrêté : démarrez-le depuis sa carte.", "[OK] "+unit+" reinstalled, stopped: start it from its card."))
	return true
}

// undismissConfig : la config d'un service recréé revient dans la détection.
func undismissConfig(conf string) {
	cfgMu.Lock()
	defer cfgMu.Unlock()
	kept := []any{}
	changed := false
	for _, d := range stringList(cfg()["dismissed_configs"]) {
		if d == conf {
			changed = true
			continue
		}
		kept = append(kept, d)
	}
	if changed {
		cfgUpdateLocked(Cfg{"dismissed_configs": kept})
	}
}

// ── Écriture des configs : validation et confinement ────────────────────────
// Le panel n'écrit une config que dans les dossiers où il en cherche (/etc/frp
// en tête), après résolution des liens symboliques et des « .. ». Une config
// TOML doit se lire avant d'être écrite : un fichier cassé n'arrive jamais sur
// le disque.

const configMaxBytes = 1 << 20

// realPath : chemin réel, même si le fichier (ou ses derniers dossiers) n'existe pas.
func realPath(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		abs = p
	}
	if r, err := filepath.EvalSymlinks(abs); err == nil {
		return r
	}
	dir, base := filepath.Split(abs)
	dir = filepath.Clean(dir)
	if dir == abs || dir == "." || dir == "/" {
		return abs
	}
	return filepath.Join(realPath(dir), base)
}

func within(child, root string) bool {
	rel, err := filepath.Rel(root, child)
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// safeConfigPath : chemin réel d'une config autorisée en écriture.
func (l Lang) safeConfigPath(p string) (string, error) {
	real := realPath(p)
	if !hasConfigSuffix(filepath.Ext(real)) || filepath.Ext(real) == "" {
		list := strings.Join(configSuffixes, ", ")
		return "", errors.New(l.M(p+" : extension refusée (attendu "+list+")", p+": extension rejected (expected "+list+")"))
	}
	for _, root := range configSearchPaths {
		if within(real, realPath(root)) {
			return real, nil
		}
	}
	dirs := strings.Join(configSearchPaths, ", ")
	return "", errors.New(l.M(p+" est hors des dossiers de configuration autorisés ("+dirs+")",
		p+" is outside the allowed configuration directories ("+dirs+")"))
}

func (l Lang) validateConfigContent(p, content string) error {
	if len(content) > configMaxBytes {
		return errors.New(l.M("Configuration trop volumineuse (1 Mo au plus)", "Configuration too large (1 MB at most)"))
	}
	if strings.ContainsRune(content, 0) {
		return errors.New(l.M("Configuration invalide : caractère nul", "Invalid configuration: null character"))
	}
	if filepath.Ext(p) == ".toml" {
		// Modèle frp (plages de ports…) : vérifié sans être exécuté, puis neutralisé
		// Frp template (port ranges…): checked without being executed, then neutralized
		if err := checkFrpTemplate(content); err != nil {
			return errors.New(l.M("Modèle frp invalide, rien n'a été écrit : "+err.Error(), "Invalid frp template, nothing was written: "+err.Error()))
		}
		var v map[string]any
		if err := toml.Unmarshal([]byte(frpTOML(content)), &v); err != nil {
			return errors.New(l.M("TOML invalide, rien n'a été écrit : "+tomlErrorText(err), "Invalid TOML, nothing was written: "+tomlErrorText(err)))
		}
	}
	return nil
}

func tomlErrorText(err error) string {
	var de *toml.DecodeError
	if errors.As(err, &de) {
		row, col := de.Position()
		return fmt.Sprintf("%s (line %d, column %d)", de.Error(), row, col)
	}
	return err.Error()
}

// writeConfigFile → (ok, message, code HTTP).
func (l Lang) writeConfigFile(p, content string) (bool, string, int) {
	real, err := l.safeConfigPath(p)
	if err == nil {
		err = l.validateConfigContent(real, content)
	}
	if err != nil {
		return false, err.Error(), 400
	}
	if err := os.MkdirAll(filepath.Dir(real), 0o755); err != nil {
		return false, l.M("Erreur écriture : "+err.Error(), "Write error: "+err.Error()), 500
	}
	mode := os.FileMode(0o644)
	if st, err := os.Stat(real); err == nil {
		mode = st.Mode().Perm()
	}
	if err := os.WriteFile(real, []byte(content), mode); err != nil {
		return false, l.M("Erreur écriture : "+err.Error(), "Write error: "+err.Error()), 500
	}
	return true, l.M("Sauvegardé : "+real, "Saved: "+real), 200
}

// writeInstanceConfig : config TOML d'une instance (fichier monté pour un conteneur).
func (l Lang) writeInstanceConfig(inst *Instance, content string) (bool, string, int) {
	if inst.Source == "docker" {
		cfgPath := ""
		// Chercher via docker inspect → Mounts
		if st, data := dockerAPI("GET", "/containers/"+inst.ContainerName+"/json", nil); st == 200 {
			if ins, ok := data.(map[string]any); ok {
				for _, item := range listv(ins, "Mounts") {
					m, _ := item.(map[string]any)
					src := str(m, "Source", "")
					if src == "" {
						continue
					}
					ext := filepath.Ext(src)
					if (ext == ".toml" || ext == ".ini") && strings.Contains(strings.ToLower(filepath.Base(src)), "frpc") {
						cfgPath = src
						break
					}
					if st, err := os.Stat(src); err == nil && st.IsDir() {
						if m, _ := filepath.Glob(filepath.Join(src, "frpc*.toml")); len(m) > 0 {
							sort.Strings(m)
							cfgPath = m[0]
						}
					}
					if cfgPath != "" {
						break
					}
				}
			}
		}
		// Repli : les dossiers de configuration
		if cfgPath == "" {
			for _, d := range configSearchPaths {
				if m, _ := filepath.Glob(filepath.Join(d, "frpc*.toml")); len(m) > 0 {
					sort.Strings(m)
					cfgPath = m[0]
					break
				}
			}
		}
		if cfgPath == "" {
			cfgPath = filepath.Join(frpConfDir, "frpc.toml")
		}
		return l.writeConfigFile(cfgPath, content)
	}
	if inst.Config == "" {
		return false, l.M("Aucun fichier de config associé", "No configuration file associated"), 400
	}
	return l.writeConfigFile(inst.Config, content)
}

// dockerFrpcConfig : config TOML d'un conteneur frpc (montages, dossiers
// standards, puis -c du conteneur).
func dockerFrpcConfig(containerName string) (string, bool) {
	st, data := dockerAPI("GET", "/containers/"+containerName+"/json", nil)
	ins, ok := data.(map[string]any)
	if st != 200 || !ok {
		alt := strings.ReplaceAll(containerName, "docker_", "")
		st, data = dockerAPI("GET", "/containers/"+alt+"/json", nil)
		if ins, ok = data.(map[string]any); st != 200 || !ok {
			return "", false
		}
	}
	read := func(p string) (string, bool) {
		b, err := os.ReadFile(p)
		return validUTF8(string(b)), err == nil
	}
	for _, item := range listv(ins, "Mounts") {
		m, _ := item.(map[string]any)
		src := str(m, "Source", "")
		if src == "" {
			continue
		}
		ext := filepath.Ext(src)
		if (ext == ".toml" || ext == ".ini") && strings.Contains(strings.ToLower(filepath.Base(src)), "frpc") {
			if s, ok := read(src); ok {
				return s, true
			}
		}
		if st, err := os.Stat(src); err == nil && st.IsDir() {
			matches, _ := filepath.Glob(filepath.Join(src, "frpc*.toml"))
			sort.Strings(matches)
			for _, f := range matches {
				if s, ok := read(f); ok {
					return s, true
				}
			}
		}
	}
	for _, d := range configSearchPaths {
		matches, _ := filepath.Glob(filepath.Join(d, "frpc*.toml"))
		sort.Strings(matches)
		for _, f := range matches {
			if s, ok := read(f); ok {
				return s, true
			}
		}
	}
	for _, list := range [][]string{stringList(mapv(ins, "Config")["Cmd"]), stringList(ins["Args"])} {
		for i, arg := range list {
			if (arg == "-c" || arg == "--config") && i+1 < len(list) {
				if s, ok := read(list[i+1]); ok {
					return s, true
				}
			}
		}
	}
	return "", false
}

// readInstanceConfig : texte de la config d'une instance.
func readInstanceConfig(inst *Instance) (string, bool) {
	if inst.Source == "docker" {
		return dockerFrpcConfig(inst.ContainerName)
	}
	if inst.Config == "" {
		return "", false
	}
	b, err := os.ReadFile(inst.Config)
	return validUTF8(string(b)), err == nil
}

// ── Ports ───────────────────────────────────────────────────────────────────

var (
	nameLineRe     = regexp.MustCompile(`^name\s*=\s*["']?([^"']+)["']?`)
	typeLineRe     = regexp.MustCompile(`^type\s*=\s*["']?(\w+)["']?`)
	remotePortRe   = regexp.MustCompile(`^remotePort\s*=\s*(\d+)`)
	bindAddrLineRe = regexp.MustCompile(`^bindAddr\s*=\s*["']?([^"']+)["']?`)
	bindPortLineRe = regexp.MustCompile(`^bindPort\s*=\s*(\d+)`)
	portLineRe     = regexp.MustCompile(`^port\s*=\s*(\d+)`)
	frpsTopPorts   = []struct {
		re           *regexp.Regexp
		proto, label string
	}{
		{regexp.MustCompile(`(?mi)^bindPort\s*=\s*(\d+)`), "tcp", "Connexion frpc"},
		{regexp.MustCompile(`(?mi)^kcpBindPort\s*=\s*(\d+)`), "udp", "KCP"},
		{regexp.MustCompile(`(?mi)^quicBindPort\s*=\s*(\d+)`), "udp", "QUIC"},
		{regexp.MustCompile(`(?mi)^vhostHTTPPort\s*=\s*(\d+)`), "tcp", "vhost HTTP"},
		{regexp.MustCompile(`(?mi)^vhostHTTPSPort\s*=\s*(\d+)`), "tcp", "vhost HTTPS"},
	}
	serverPortRe = regexp.MustCompile(`(?mi)^serverPort\s*=\s*(\d+)`)
)

func atoi(s string) int { n, _ := strconv.Atoi(s); return n }

func splitLines(s string) []string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	lines := strings.Split(s, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// extractPortsFromConfig : numéros de port d'une config frp TOML.
func extractPortsFromConfig(content, binType string) []J {
	var ports []J
	switch binType {
	case "frps":
		for _, p := range frpsTopPorts {
			if m := p.re.FindStringSubmatch(content); m != nil {
				ports = append(ports, J{"port": atoi(m[1]), "proto": p.proto, "label": p.label})
			}
		}
	case "frpc":
		if m := serverPortRe.FindStringSubmatch(content); m != nil {
			ports = append(ports, J{"port": atoi(m[1]), "proto": "tcp", "label": "Connexion serveur"})
		}
		// remotePort de chaque [[proxies]] (ports exposés côté serveur frps)
		inProxy, proxyName, proxyType := false, "", "tcp"
		// Plage de ports (modèle de la page Ports) : un tunnel par paire de ports,
		// nommé comme frpc le fera (« nom-<port local> »)
		// Port range (Ports page template): one tunnel per pair of ports, named the
		// way frpc will name it ("name-<local port>")
		var pendingRange, proxyRange [][2]int
		for _, line := range splitLines(content) {
			s := strings.TrimSpace(line)
			if m := frpRangeLineRe.FindStringSubmatch(s); m != nil {
				pendingRange = nil
				first, err1 := frpPortRange(m[1])
				second, err2 := frpPortRange(m[2])
				if err1 == nil && err2 == nil && len(first) == len(second) {
					for i := range first {
						pendingRange = append(pendingRange, [2]int{first[i], second[i]})
					}
				}
				continue
			}
			if s == "[[proxies]]" {
				inProxy, proxyName, proxyType = true, "", "tcp"
				proxyRange, pendingRange = pendingRange, nil
				continue
			}
			if strings.HasPrefix(s, "[") && !strings.HasPrefix(s, "[[proxies]]") {
				inProxy = false
				continue
			}
			if inProxy {
				if m := nameLineRe.FindStringSubmatch(s); m != nil {
					proxyName = strings.TrimSpace(m[1])
				}
				if m := typeLineRe.FindStringSubmatch(s); m != nil {
					proxyType = strings.TrimSpace(m[1])
				}
				if frpSecondRe.MatchString(s) {
					proto := "tcp"
					if proxyType == "udp" {
						proto = "udp"
					}
					for _, pair := range proxyRange {
						label := frpFirstRe.ReplaceAllString(proxyName, strconv.Itoa(pair[0]))
						ports = append(ports, J{"port": pair[1], "proto": proto, "label": "Tunnel " + label + " (remotePort)"})
					}
				}
				if m := remotePortRe.FindStringSubmatch(s); m != nil {
					proto := "tcp"
					if proxyType == "udp" {
						proto = "udp"
					}
					label := proxyName
					if label == "" {
						label = proxyType
					}
					ports = append(ports, J{"port": atoi(m[1]), "proto": proto, "label": "Tunnel " + label + " (remotePort)"})
				}
			}
		}
		// bindPort de chaque [[visitors]] (écoute LOCALE côté frpc), seulement
		// si bindAddr n'est pas une adresse de bouclage.
		inVisitor, visName, visAddr, visPort := false, "", "127.0.0.1", 0
		flush := func() {
			if visPort != 0 && visAddr != "127.0.0.1" && visAddr != "::1" && visAddr != "localhost" {
				ports = append(ports, J{"port": visPort, "proto": "tcp", "label": strings.TrimSpace("Visiteur " + visName + " (bindPort)")})
			}
		}
		for _, line := range splitLines(content) {
			s := strings.TrimSpace(line)
			if s == "[[visitors]]" {
				flush()
				inVisitor, visName, visAddr, visPort = true, "", "127.0.0.1", 0
				continue
			}
			if strings.HasPrefix(s, "[") && s != "[[visitors]]" {
				flush()
				inVisitor, visPort = false, 0
				continue
			}
			if inVisitor {
				if m := nameLineRe.FindStringSubmatch(s); m != nil {
					visName = strings.TrimSpace(m[1])
				}
				if m := bindAddrLineRe.FindStringSubmatch(s); m != nil {
					visAddr = strings.TrimSpace(m[1])
				}
				if m := bindPortLineRe.FindStringSubmatch(s); m != nil {
					visPort = atoi(m[1])
				}
			}
		}
		flush()
	}
	// Port du webServer (section [webServer]) : frps et frpc
	inWS := false
	for _, line := range splitLines(content) {
		s := strings.TrimSpace(line)
		switch {
		case s == "[webServer]":
			inWS = true
		case strings.HasPrefix(s, "["):
			inWS = false
		case inWS:
			if m := portLineRe.FindStringSubmatch(s); m != nil {
				ports = append(ports, J{"port": atoi(m[1]), "proto": "tcp", "label": "Dashboard web"})
			}
		}
	}
	return ports
}

var ufwAllowRe = regexp.MustCompile(`(?i)^\s*(\d+)(?:/(\w+))?\s+ALLOW`)

// ufwAllowedPorts → (ufw disponible, ports autorisés « port/proto »).
func ufwAllowedPorts() (bool, map[string]bool) {
	_, out, e := runCmd([]string{"ufw", "status"})
	combined := strings.ToLower(out + e)
	for _, x := range []string{"not found", "command not found", "no such file", "executable file not found"} {
		if strings.Contains(combined, x) {
			return false, map[string]bool{}
		}
	}
	allowed := map[string]bool{}
	if strings.Contains(strings.ToLower(out), "inactive") {
		return true, allowed // UFW disponible mais inactif
	}
	for _, line := range strings.Split(out, "\n") {
		if m := ufwAllowRe.FindStringSubmatch(line); m != nil {
			proto := strings.ToLower(m[2])
			if proto == "" {
				proto = "tcp"
			}
			allowed[m[1]+"/"+proto] = true
			allowed[m[1]+"/any"] = true
		}
	}
	return true, allowed
}

// ── WebSocket ───────────────────────────────────────────────────────────────
// Un reverse proxy coupe une connexion qu'il juge inactive (nginx : 60 s par
// défaut) : sans nouvelle ligne ni changement d'état, on envoie un petit message
// applicatif, le ping WebSocket de bas niveau n'étant pas compté par tous.

const (
	keepaliveInterval = 20 * time.Second
	wsKeepaliveLogs   = "\x00" // ignoré par l'interface
	wsKeepaliveState  = `{"keepalive": true}`
	wsPingInterval    = 25 * time.Second
)

func liveLogParams(c *Ctx) (string, int) {
	history := 50
	if v := c.Query("history"); v != "" {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			history = max(0, min(200, n))
		}
	}
	source := c.Query("source")
	if source != "file" {
		source = "journal"
	}
	return source, history
}

// sameOrigin : handshake WebSocket venu d'une page du panel (pas de jeton CSRF
// possible sur une requête GET d'upgrade : on vérifie l'en-tête Origin, s'il est là).
func (c *Ctx) sameOrigin() bool {
	origin := c.R.Header.Get("Origin")
	if origin == "" {
		return true // client hors navigateur : il lui faut quand même la session
	}
	if i := strings.Index(origin, "://"); i >= 0 {
		origin = origin[i+3:]
	}
	netloc := strings.TrimRight(origin, "/")
	return netloc == c.R.Host || (netloc != "" && netloc == c.R.Header.Get("X-Forwarded-Host"))
}

// wsAccept ouvre la connexion puis vérifie le compte → (connexion, fonction qui
// renvoie la fiche à jour du compte ou nil s'il a perdu le droit). nil si refusé.
func (c *Ctx) wsAccept(permName, iid string) (*websocket.Conn, func() User) {
	conn, err := websocket.Accept(c.W, c.R, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return nil, nil
	}
	u := c.currentUser()
	if u == nil || !c.sameOrigin() || !userCan(u, permName) || (iid != "" && !instanceAllowed(u, iid)) {
		conn.Close(websocket.StatusPolicyViolation, c.M("Non authentifié", "Not authenticated"))
		return nil, nil
	}
	uid, fp := str(u, "id", ""), c.sess.get("pwfp")
	var mu sync.Mutex
	checkedAt, current := time.Now(), u
	return conn, func() User {
		mu.Lock()
		defer mu.Unlock()
		if time.Since(checkedAt) >= 5*time.Second {
			nu := allUsers()[uid]
			ok := nu != nil && boolv(nu, "enabled", true) && fp == sessionFingerprint(nu) &&
				userCan(nu, permName) && (iid == "" || instanceAllowed(nu, iid))
			checkedAt = time.Now()
			if ok {
				current = nu
			} else {
				current = nil
			}
		}
		return current
	}
}

// wsKeepPing : ping de bas niveau toutes les 25 s, comme les versions Python.
func wsKeepPing(ctx context.Context, conn *websocket.Conn) {
	t := time.NewTicker(wsPingInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			err := conn.Ping(pctx)
			cancel()
			if err != nil {
				return
			}
		}
	}
}

// État des instances poussé au navigateur : vérifié toutes les 2 s côté serveur
// (et aussitôt après une action), envoyé seulement s'il a changé.
func wsStatus(c *Ctx) {
	conn, allowed := c.wsAccept("instances.view", "")
	if conn == nil {
		return
	}
	defer conn.CloseNow()
	ctx := conn.CloseRead(c.R.Context())
	go wsKeepPing(ctx, conn)
	last, lastSent := "", time.Now()
	for {
		u := allowed()
		if u == nil {
			conn.Close(websocket.StatusPolicyViolation, c.M("Accès retiré", "Access revoked"))
			return
		}
		changed := statusChangedChan()
		data, err := json.Marshal(J{"instances": filterInstances(detectFrp(false), u), "in_docker": inDocker, "standalone": standalone})
		if err == nil && string(data) != last {
			if conn.Write(ctx, websocket.MessageText, data) != nil {
				return
			}
			last, lastSent = string(data), time.Now()
		} else if time.Since(lastSent) >= keepaliveInterval {
			if conn.Write(ctx, websocket.MessageText, []byte(wsKeepaliveState)) != nil {
				return
			}
			lastSent = time.Now()
		}
		select {
		case <-ctx.Done():
			return
		case <-changed:
		case <-time.After(2 * time.Second):
		}
	}
}

// Journal en direct par WebSocket : une ligne par message texte.
func wsLogs(c *Ctx) {
	iid := c.Param("iid")
	conn, allowed := c.wsAccept("logs.view", iid)
	if conn == nil {
		return
	}
	defer conn.CloseNow()
	detectFrp(false)
	inst := getInstance(iid)
	if inst == nil {
		conn.Close(websocket.StatusPolicyViolation, c.M("Instance inconnue", "Unknown instance"))
		return
	}
	ctx := conn.CloseRead(c.R.Context())
	go wsKeepPing(ctx, conn)
	source, history := liveLogParams(c)
	lines := liveLogLines(ctx, c.Lang, inst, source, history)
	lastSent := time.Now()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case line, ok := <-lines:
			if !ok {
				conn.Close(websocket.StatusNormalClosure, "")
				return
			}
			if conn.Write(ctx, websocket.MessageText, []byte(line)) != nil {
				return
			}
			lastSent = time.Now()
		case <-tick.C:
			// Attente avec délai : droits revérifiés, maintien de connexion
			if allowed() == nil {
				conn.Close(websocket.StatusPolicyViolation, c.M("Accès retiré", "Access revoked"))
				return
			}
			if time.Since(lastSent) >= keepaliveInterval {
				if conn.Write(ctx, websocket.MessageText, []byte(wsKeepaliveLogs)) != nil {
					return
				}
				lastSent = time.Now()
			}
		}
	}
}
