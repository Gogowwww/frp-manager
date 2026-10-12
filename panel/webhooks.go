// Notifications webhook.
//
// Chaque webhook a son adresse, son format (Discord, Slack, Telegram, ntfy,
// Gotify, JSON signé, texte ou modèle libre), ses événements et sa langue. Les
// envois passent par une file et une goroutine : une requête du panel n'attend
// jamais un service de notification lent ou en panne.

package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
)

type webhookEvent struct{ level, fr, en string }

// id → (niveau, français, anglais)
var webhookEventOrder = []string{"instance.down", "instance.up", "login.success", "login.locked", "config.saved",
	"firewall.saved", "update.panel", "update.frp", "update.frp_installed", "panel.started"}

var webhookEvents = map[string]webhookEvent{
	"instance.down":        {"error", "Instance arrêtée", "Instance stopped"},
	"instance.up":          {"success", "Instance démarrée", "Instance started"},
	"login.success":        {"info", "Connexion au panel", "Panel sign-in"},
	"login.locked":         {"warning", "Adresse verrouillée (échecs de connexion)", "Address locked out (failed sign-ins)"},
	"config.saved":         {"info", "Configuration modifiée", "Configuration changed"},
	"firewall.saved":       {"info", "Règles du pare-feu modifiées", "Firewall rules changed"},
	"update.panel":         {"info", "Mise à jour du panel disponible", "Panel update available"},
	"update.frp":           {"info", "Nouvelle version de frp disponible", "New frp version available"},
	"update.frp_installed": {"success", "frp mis à jour", "frp updated"},
	"panel.started":        {"info", "Panel démarré", "Panel started"},
}

var webhookFormats = []string{"generic", "discord", "slack", "telegram", "ntfy", "gotify", "text"}

const (
	webhookMax         = 20
	webhookHistoryMax  = 50
	webhookTimeout     = 10 * time.Second
	webhookMonitorTick = 15 * time.Second
	webhookUpdateEvery = 6 * time.Hour
)

var (
	webhookRetryDelays  = []time.Duration{0, 3 * time.Second, 10 * time.Second} // 3 essais
	webhookColors       = map[string]int{"info": 0x3B82F6, "success": 0x22C55E, "warning": 0xF59E0B, "error": 0xEF4444}
	webhookTemplateKeys = []string{"event", "title", "message", "level", "host", "time", "instance", "subject", "version"}
	headerNameRe        = regexp.MustCompile("^[A-Za-z0-9!#$%&'*+.^_`|~-]{1,64}$")
	templateKeyRe       = regexp.MustCompile(`\{\{\s*(\w+)\s*\}\}`)
	webhookQueue        = make(chan webhookItem, 200)
	webhookMu           sync.Mutex
	webhookHistory      []J                    // plus récent en dernier
	webhookRecent       = map[string]float64{} // (webhook, événement, sujet) → dernier envoi
	panelActionMu       sync.Mutex
	panelActions        = map[string]float64{} // instance → dernière action lancée depuis le panel
)

type webhookItem struct {
	event, subject, instance, level, time string
	title, text                           [2]string
	data                                  J
}

// notePanelAction : l'instance vient d'être pilotée depuis le panel.
func notePanelAction(iid string) {
	panelActionMu.Lock()
	panelActions[iid] = nowFunc()
	panelActionMu.Unlock()
}

func webhooksActive() []map[string]any {
	var out []map[string]any
	for _, w := range listv(cfg(), "webhooks") {
		if m, ok := w.(map[string]any); ok && boolv(m, "enabled", true) {
			out = append(out, m)
		}
	}
	return out
}

// webhookEmit met un événement en file ; title et text sont des couples
// (français, anglais). Ne coûte rien tant qu'aucun webhook n'est actif.
func webhookEmit(event string, title, text [2]string, subject string, data J) {
	webhookEmitFull(event, title, text, subject, "", data)
}

func webhookEmitInstance(event string, title, text [2]string, subject, instance string) {
	webhookEmitFull(event, title, text, subject, instance, nil)
}

func webhookEmitFull(event string, title, text [2]string, subject, instance string, data J) {
	ev, known := webhookEvents[event]
	if !known || len(webhooksActive()) == 0 {
		return
	}
	if data == nil {
		data = J{}
	}
	item := webhookItem{event: event, title: title, text: text, subject: subject, instance: instance,
		data: data, level: ev.level, time: nowISO()}
	select {
	case webhookQueue <- item:
	default:
		log.Printf("[WARN] Webhooks : file pleine, notification abandonnée")
	}
}

// renderTemplate remplace {{clé}} par la valeur échappée pour une chaîne JSON :
// le modèle s'écrit '{"content": "{{message}}"}'.
func renderTemplate(template string, values map[string]string) string {
	return templateKeyRe.ReplaceAllStringFunc(template, func(m string) string {
		key := templateKeyRe.FindStringSubmatch(m)[1]
		return jsonStringBody(values[key])
	})
}

func jsonMarshalNoEscape(v any) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.Encode(v)
	return bytes.TrimRight(buf.Bytes(), "\n")
}

func jsonStringBody(s string) string {
	b := jsonMarshalNoEscape(s)
	return string(b[1 : len(b)-1])
}

func (l Lang) parseWebhookHeaders(text string) (map[string]string, error) {
	headers := map[string]string{}
	var names []string
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		name, value, found := strings.Cut(line, ":")
		name, value = strings.TrimSpace(name), strings.TrimSpace(value)
		if !found || !headerNameRe.MatchString(name) || strings.ContainsAny(value, "\r\n\x00") {
			short := truncRunes(line, 60)
			return nil, errors.New(l.M("En-tête invalide : « "+short+" » (attendu : Nom: valeur).", "Invalid header: “"+short+"” (expected: Name: value)."))
		}
		if _, dup := headers[name]; !dup {
			names = append(names, name)
		}
		headers[name] = value
	}
	if len(headers) > 10 {
		return nil, errors.New(l.M("10 en-têtes au plus.", "10 headers at most."))
	}
	return headers, nil
}

func hostname() string {
	h, _ := os.Hostname()
	return h
}

func webhookValues(item webhookItem, lang string) map[string]string {
	i := 1
	if lang == "fr" {
		i = 0
	}
	return map[string]string{"event": item.event, "title": item.title[i], "message": item.text[i],
		"level": item.level, "host": hostname(), "time": item.time, "instance": item.instance,
		"subject": item.subject, "version": panelVersion}
}

// webhookRequest → (adresse, corps, en-têtes) de la notification pour ce webhook.
func webhookRequest(hook map[string]any, item webhookItem) (string, []byte, map[string]string) {
	v := webhookValues(item, str(hook, "lang", "fr"))
	format := str(hook, "format", "generic")
	target := str(hook, "url", "")
	headers := map[string]string{"Content-Type": "application/json", "User-Agent": "FRP-Manager/" + panelVersion,
		"X-FRPManager-Event": item.event}
	color, ok := webhookColors[item.level]
	if !ok {
		color = webhookColors["info"]
	}
	var body any
	switch format {
	case "discord":
		body = J{"username": "FRP Manager", "embeds": []J{{
			"title": truncRunes(v["title"], 256), "description": truncRunes(v["message"], 4000),
			"color": color, "timestamp": time.Now().Format("2006-01-02T15:04:05.000000-07:00"), "footer": J{"text": v["host"]}}}}
	case "slack":
		body = J{"attachments": []J{{"fallback": v["title"], "color": fmt.Sprintf("#%06x", color), "title": v["title"],
			"text": v["message"], "footer": v["host"], "ts": time.Now().Unix()}}}
	case "telegram":
		esc := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace
		if !strings.HasSuffix(strings.TrimRight(target, "/"), "/sendMessage") {
			target = strings.TrimRight(target, "/") + "/sendMessage"
		}
		body = J{"chat_id": str(hook, "chat_id", ""), "parse_mode": "HTML", "disable_web_page_preview": true,
			"text": "<b>" + esc(v["title"]) + "</b>\n" + esc(v["message"]) + "\n<i>" + esc(v["host"]) + "</i>"}
	case "ntfy":
		// Publication en JSON à la racine du serveur : accents et emoji passent
		u, _ := url.Parse(target)
		target = u.Scheme + "://" + u.Host + "/"
		prio := map[string]int{"info": 3, "success": 3, "warning": 4, "error": 5}[item.level]
		tag := map[string]string{"info": "information_source", "success": "white_check_mark",
			"warning": "warning", "error": "rotating_light"}[item.level]
		body = J{"topic": strings.Trim(u.Path, "/"), "title": v["title"], "message": v["message"] + "\n" + v["host"],
			"priority": prio, "tags": []string{tag}}
	case "gotify":
		prio := map[string]int{"info": 4, "success": 4, "warning": 6, "error": 8}[item.level]
		body = J{"title": v["title"], "message": v["message"] + "\n" + v["host"], "priority": prio}
	case "text":
		headers["Content-Type"] = "text/plain; charset=utf-8"
		body = v["title"] + "\n" + v["message"] + "\n" + v["host"]
	default:
		if t := str(hook, "template", ""); t != "" {
			body = renderTemplate(t, v)
		} else {
			body = J{"event": item.event, "level": item.level, "title": v["title"], "message": v["message"],
				"host": v["host"], "time": item.time, "panel_version": panelVersion,
				"instance": nullable(item.instance), "data": item.data}
		}
	}
	var raw []byte
	if s, ok := body.(string); ok {
		raw = []byte(s)
	} else {
		raw = jsonMarshalNoEscape(body)
	}
	if secret := str(hook, "secret", ""); (format == "generic" || format == "text") && secret != "" {
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write(raw)
		headers["X-FRPManager-Signature"] = "sha256=" + hex.EncodeToString(mac.Sum(nil))
	}
	if extra, err := FR.parseWebhookHeaders(str(hook, "headers", "")); err == nil { // refusés à l'enregistrement
		for k, val := range extra {
			headers[k] = val
		}
	}
	return target, raw, headers
}

// webhookSend : un envoi avec ses nouveaux essais → (ok, HTTP ou 0, message court).
func webhookSend(hook map[string]any, item webhookItem) (bool, int, string) {
	target, raw, headers := webhookRequest(hook, item)
	client := &http.Client{Timeout: webhookTimeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	status, msg := 0, ""
	for _, delay := range webhookRetryDelays {
		time.Sleep(delay)
		req, err := http.NewRequest(http.MethodPost, target, bytes.NewReader(raw))
		if err != nil {
			return false, 0, "InvalidURL"
		}
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		resp, err := client.Do(req)
		if err != nil {
			status, msg = 0, requestErrorName(err)
			continue
		}
		resp.Body.Close()
		status = resp.StatusCode
		if status >= 200 && status < 300 {
			return true, status, "OK"
		}
		msg = fmt.Sprintf("HTTP %d", status)
		if status < 500 && status != 429 {
			break // refus définitif : inutile de réessayer
		}
	}
	return false, status, msg
}

// requestErrorName : nom court d'une erreur réseau (comme les exceptions de requests).
func requestErrorName(err error) string {
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() || errors.Is(err, context.DeadlineExceeded) {
		return "Timeout"
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return "ConnectionError"
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return "ConnectionError"
	}
	if strings.Contains(err.Error(), "tls") || strings.Contains(err.Error(), "x509") {
		return "SSLError"
	}
	return "ConnectionError"
}

func webhookRecord(hook map[string]any, item webhookItem, ok bool, status int, msg string) {
	entry := J{"time": item.time, "webhook": hook["id"], "name": str(hook, "name", ""), "event": item.event,
		"ok": ok, "status": status, "msg": msg}
	webhookMu.Lock()
	webhookHistory = append(webhookHistory, entry)
	if len(webhookHistory) > webhookHistoryMax {
		webhookHistory = webhookHistory[len(webhookHistory)-webhookHistoryMax:]
	}
	webhookMu.Unlock()
	if !ok {
		log.Printf("[WARN] Webhook « %s » : %s", str(hook, "name", ""), msg)
	}
}

func webhookMatches(hook map[string]any, item webhookItem) bool {
	events := stringList(hook["events"])
	matched := false
	for _, e := range events {
		if e == "*" || e == item.event {
			matched = true
		}
	}
	if !matched {
		return false
	}
	if only := stringList(hook["instances"]); len(only) > 0 && item.instance != "" {
		found := false
		for _, i := range only {
			if i == item.instance {
				found = true
			}
		}
		if !found {
			return false
		}
	}
	cooldown, _ := pyInt(hook["cooldown"])
	if cooldown > 0 {
		key := pyStr(hook["id"]) + "\x00" + item.event + "\x00" + item.subject
		now := nowFunc()
		webhookMu.Lock()
		defer webhookMu.Unlock()
		if now-webhookRecent[key] < float64(cooldown) {
			return false
		}
		webhookRecent[key] = now
		if len(webhookRecent) > 2000 {
			for k, t := range webhookRecent {
				if now-t > 86400 {
					delete(webhookRecent, k)
				}
			}
		}
	}
	return true
}

func webhookWorker() {
	for item := range webhookQueue {
		func() {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("[WARN] Webhooks : %v", r)
				}
			}()
			for _, hook := range webhooksActive() {
				if webhookMatches(hook, item) {
					ok, status, msg := webhookSend(hook, item)
					webhookRecord(hook, item, ok, status, msg)
				}
			}
		}()
	}
}

// webhookStateNote retient la dernière version annoncée : une mise à jour n'est
// notifiée qu'une fois.
func webhookStateNote(key, value string) bool {
	st := loadState()
	if st[key] == value {
		return false
	}
	st[key] = value
	saveState(st)
	return true
}

func webhookWants(event string) bool {
	for _, w := range webhooksActive() {
		for _, e := range stringList(w["events"]) {
			if e == "*" || e == event {
				return true
			}
		}
	}
	return false
}

func webhookCheckUpdates() {
	l := FR
	if webhookWants("update.panel") {
		pre := panelIsPrerelease(l, false)
		res := githubCached(fmt.Sprintf("panel:%v", pre), func() []string { return fetchPanelLatest(l, pre) }, false)
		ver, link := res[0], res[1]
		if ver != "" && !(pre && inDocker) && versionNewer(ver, panelVersion) && webhookStateNote("notified_panel", ver) {
			webhookEmit("update.panel", [2]string{"Panel " + ver + " disponible", "Panel " + ver + " available"},
				[2]string{"Version installée : " + panelVersion + ". " + link, "Installed version: " + panelVersion + ". " + link},
				ver, J{"current": panelVersion, "latest": ver, "url": link})
		}
	}
	if webhookWants("update.frp") {
		res := githubCached("frp", func() []string {
			v, t, s := fetchLatestVersion(l)
			return []string{v, t, s}
		}, false)
		ver := res[0]
		installed := pyStr(loadState()["installed_version"])
		if !truthy(loadState()["installed_version"]) {
			installed = ""
		}
		if ver != "" && installed != "" && versionNewer(ver, installed) && webhookStateNote("notified_frp", ver) {
			webhookEmit("update.frp", [2]string{"frp " + ver + " disponible", "frp " + ver + " available"},
				[2]string{"Version installée : " + installed + ".", "Installed version: " + installed + "."},
				ver, J{"current": installed, "latest": ver})
		}
	}
}

// webhookInstanceEvents compare l'état des instances au précédent ; un
// changement doit tenir deux relevés d'affilée avant d'être annoncé.
func webhookInstanceEvents(instances map[string]J, seen, pending map[string]bool) {
	for _, iid := range sortedKeys(instances) {
		inst := instances[iid]
		if found, ok := inst["binary_found"].(bool); ok && !found {
			delete(seen, iid)
			delete(pending, iid)
			continue
		}
		running := truthy(mapv(inst, "status")["running"])
		prev, known := seen[iid]
		switch {
		case !known:
			seen[iid] = running
		case running == prev:
			delete(pending, iid)
		default:
			if p, ok := pending[iid]; !ok || p != running {
				pending[iid] = running // premier relevé différent : à confirmer
				continue
			}
			seen[iid] = running
			delete(pending, iid)
			name := str(nicknames(), iid, "")
			if name == "" {
				name = str(inst, "service", iid)
			}
			panelActionMu.Lock()
			byPanel := nowFunc()-panelActions[iid] < 120
			panelActionMu.Unlock()
			src := [2]string{"", ""}
			if byPanel {
				src = [2]string{" (action lancée depuis le panel)", " (action started from the panel)"}
			}
			if running {
				webhookEmitFull("instance.up", [2]string{name + " est démarrée", name + " is running"},
					[2]string{"L'instance tourne." + src[0], "The instance is running." + src[1]}, iid, iid, J{"by_panel": byPanel})
			} else {
				webhookEmitFull("instance.down", [2]string{name + " est arrêtée", name + " is stopped"},
					[2]string{"L'instance ne tourne plus." + src[0], "The instance is no longer running." + src[1]}, iid, iid, J{"by_panel": byPanel})
			}
		}
	}
}

func webhookMonitorLoop() {
	time.Sleep(10 * time.Second)
	seen, pending := map[string]bool{}, map[string]bool{}
	var lastUpdates time.Time
	for {
		func() {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("[WARN] Webhooks : %v", r)
				}
			}()
			if len(webhooksActive()) > 0 {
				webhookInstanceEvents(detectFrp(false), seen, pending)
				if time.Since(lastUpdates) >= webhookUpdateEvery {
					lastUpdates = time.Now()
					webhookCheckUpdates()
				}
			} else {
				clear(seen)
				clear(pending)
			}
		}()
		time.Sleep(webhookMonitorTick)
	}
}

// webhookPublic : un webhook tel que l'interface le voit (l'adresse, souvent
// porteuse d'un jeton, le secret et les en-têtes ne sortent jamais du serveur).
func webhookPublic(w map[string]any) J {
	hint := ""
	if u, err := url.Parse(str(w, "url", "")); err == nil && u.Host != "" {
		hint = u.Scheme + "://" + u.Host + "/…"
	}
	cooldown, _ := pyInt(w["cooldown"])
	events, instances := w["events"], w["instances"]
	if !truthy(events) {
		events = []any{}
	}
	if !truthy(instances) {
		instances = []any{}
	}
	return J{"id": w["id"], "name": str(w, "name", ""), "enabled": boolv(w, "enabled", true),
		"format": str(w, "format", "generic"), "url_hint": hint,
		"has_secret": truthy(w["secret"]), "has_headers": truthy(w["headers"]),
		"chat_id": str(w, "chat_id", ""), "events": events, "instances": instances,
		"lang": str(w, "lang", "fr"), "cooldown": cooldown, "template": str(w, "template", "")}
}

// webhookNormalize : webhook validé ; l'adresse, le secret et les en-têtes laissés
// vides reprennent ceux de l'enregistrement précédent.
func (l Lang) webhookNormalize(raw any, old map[string]any) (map[string]any, error) {
	if old == nil {
		old = map[string]any{}
	}
	r, ok := raw.(map[string]any)
	if !ok {
		return nil, errors.New(l.M("Webhook invalide.", "Invalid webhook."))
	}
	get := func(k string) string {
		if truthy(r[k]) {
			return pyStr(r[k])
		}
		return ""
	}
	name := truncRunes(strings.TrimSpace(get("name")), 64)
	if name == "" {
		return nil, errors.New(l.M("Donnez un nom au webhook.", "Give the webhook a name."))
	}
	format := "generic"
	if truthy(r["format"]) {
		s, isStr := r["format"].(string)
		if !isStr {
			return nil, errors.New(l.M("Format inconnu.", "Unknown format."))
		}
		format = s
	}
	known := false
	for _, f := range webhookFormats {
		if f == format {
			known = true
		}
	}
	if !known {
		return nil, errors.New(l.M("Format inconnu.", "Unknown format."))
	}
	target := strings.TrimSpace(get("url"))
	if target == "" {
		target = str(old, "url", "")
	}
	u, err := url.Parse(target)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || len(target) > 2048 || strings.ContainsAny(target, " \r\n\x00") {
		return nil, errors.New(l.M("« "+name+" » : adresse invalide (http:// ou https:// attendu).", "“"+name+"”: invalid address (http:// or https:// expected)."))
	}
	if format == "ntfy" && strings.Trim(u.Path, "/") == "" {
		return nil, errors.New(l.M("« "+name+" » : ajoutez le sujet ntfy à l'adresse (https://ntfy.sh/mon-sujet).",
			"“"+name+"”: add the ntfy topic to the address (https://ntfy.sh/my-topic)."))
	}
	chatID := truncRunes(strings.TrimSpace(get("chat_id")), 64)
	if format == "telegram" && chatID == "" {
		return nil, errors.New(l.M("« "+name+" » : identifiant de conversation Telegram requis.", "“"+name+"”: Telegram chat ID required."))
	}
	events, isList := r["events"].([]any)
	validEvents := isList && len(events) > 0
	for _, e := range events {
		s, isStr := e.(string)
		if _, known := webhookEvents[s]; !isStr || (s != "*" && !known) {
			validEvents = false
		}
	}
	if !validEvents {
		return nil, errors.New(l.M("« "+name+" » : choisissez au moins un événement.", "“"+name+"”: pick at least one event."))
	}
	instances := []any{}
	if list, ok := r["instances"].([]any); ok {
		for _, i := range list {
			if s := pyStr(i); validInstanceID(s) && len(instances) < 50 {
				instances = append(instances, s)
			}
		}
	}
	lang := "fr"
	if r["lang"] == "en" {
		lang = "en"
	}
	cooldown := 0
	if truthy(r["cooldown"]) {
		var ok bool
		if cooldown, ok = pyInt(r["cooldown"]); !ok || cooldown < 0 || cooldown > 86400 {
			return nil, errors.New(l.M("Délai entre deux envois : 0 à 86 400 secondes.", "Delay between two sends: 0 to 86,400 seconds."))
		}
	}
	secret := ""
	if !truthy(r["secret_clear"]) {
		if secret = get("secret"); secret == "" {
			secret = str(old, "secret", "")
		}
	}
	headers := ""
	if !truthy(r["headers_clear"]) {
		if headers = strings.TrimSpace(get("headers")); headers == "" {
			headers = str(old, "headers", "")
		}
	}
	if _, err := l.parseWebhookHeaders(headers); err != nil {
		return nil, err
	}
	template := ""
	if format == "generic" {
		template = truncRunes(strings.TrimSpace(get("template")), 4000)
	}
	if template != "" {
		filler := map[string]string{}
		for _, k := range webhookTemplateKeys {
			filler[k] = "x"
		}
		var v any
		if json.Unmarshal([]byte(renderTemplate(template, filler)), &v) != nil {
			return nil, errors.New(l.M("« "+name+" » : le modèle n'est pas un JSON valide une fois rempli.",
				"“"+name+"”: the template is not valid JSON once filled in."))
		}
	}
	id := str(old, "id", "")
	if id == "" {
		id = tokenHex(4)
	}
	enabled := true
	if v, ok := r["enabled"]; ok {
		enabled = truthy(v)
	}
	return map[string]any{"id": id, "name": name, "enabled": enabled, "format": format, "url": target,
		"secret": secret, "headers": headers, "chat_id": chatID, "events": events, "instances": instances,
		"lang": lang, "cooldown": cooldown, "template": template}, nil
}

func savedWebhooks() []map[string]any {
	var out []map[string]any
	for _, w := range listv(cfg(), "webhooks") {
		if m, ok := w.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

func init() {
	route("GET /api/webhooks", "api_webhooks_get", perm(P("webhooks.view"), func(c *Ctx) {
		webhookMu.Lock()
		history := make([]J, 0, len(webhookHistory))
		for i := len(webhookHistory) - 1; i >= 0; i-- {
			history = append(history, webhookHistory[i])
		}
		webhookMu.Unlock()
		hooks := []J{}
		for _, w := range savedWebhooks() {
			hooks = append(hooks, webhookPublic(w))
		}
		events := make([]J, len(webhookEventOrder))
		for i, id := range webhookEventOrder {
			events[i] = J{"id": id, "level": webhookEvents[id].level}
		}
		c.JSON(J{"ok": true, "webhooks": hooks, "history": history, "events": events, "formats": webhookFormats})
	}))

	route("POST /api/webhooks", "api_webhooks_save", perm(P("webhooks.manage"), func(c *Ctx) {
		raws, ok := c.Body()["webhooks"].([]any)
		if !ok || len(raws) > webhookMax {
			c.Fail(http.StatusBadRequest, c.M(fmt.Sprintf("%d webhooks au plus.", webhookMax), fmt.Sprintf("%d webhooks at most.", webhookMax)))
			return
		}
		known := map[string]map[string]any{}
		for _, w := range savedWebhooks() {
			known[pyStr(w["id"])] = w
		}
		hooks := make([]any, 0, len(raws))
		views := []J{}
		for _, r := range raws {
			var old map[string]any
			if m, ok := r.(map[string]any); ok {
				old = known[pyStr(m["id"])]
			}
			hook, err := c.Lang.webhookNormalize(r, old)
			if err != nil {
				c.Fail(http.StatusBadRequest, err.Error())
				return
			}
			hooks = append(hooks, hook)
			views = append(views, webhookPublic(hook))
		}
		if _, err := cfgUpdate(Cfg{"webhooks": hooks}); err != nil {
			c.Fail(http.StatusInternalServerError, err.Error())
			return
		}
		c.JSON(J{"ok": true, "webhooks": views, "msg": c.M("Webhooks enregistrés", "Webhooks saved")})
	}))

	// Notification d'essai au webhook décrit (enregistré ou en cours de saisie).
	route("POST /api/webhooks/test", "api_webhooks_test", perm(P("webhooks.manage"), func(c *Ctx) {
		raw, _ := c.Body()["webhook"].(map[string]any)
		if raw == nil {
			raw = map[string]any{}
		}
		var old map[string]any
		for _, w := range savedWebhooks() {
			if pyStr(w["id"]) == pyStr(raw["id"]) {
				old = w
			}
		}
		draft := map[string]any{}
		for k, v := range raw {
			draft[k] = v
		}
		draft["events"] = []any{"*"}
		hook, err := c.Lang.webhookNormalize(draft, old)
		if err != nil {
			c.Fail(http.StatusBadRequest, err.Error())
			return
		}
		item := webhookItem{event: "test", level: "info",
			title:   [2]string{"Notification d'essai", "Test notification"},
			text:    [2]string{"Ce webhook fonctionne. FRP Manager " + panelVersion + ".", "This webhook works. FRP Manager " + panelVersion + "."},
			subject: "test", data: J{"test": true}, time: nowISO()}
		ok, status, msg := webhookSend(hook, item)
		webhookRecord(hook, item, ok, status, msg)
		text := c.M("Échec de l'envoi : "+msg, "Sending failed: "+msg)
		if ok {
			text = c.M("Notification envoyée", "Notification sent")
		}
		c.JSON(J{"ok": ok, "status": status, "msg": text})
	}))
}
