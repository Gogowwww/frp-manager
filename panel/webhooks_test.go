// Notifications webhook : validation, formats, secrets, événements.

package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

var testHook = J{"name": "Alertes", "format": "discord", "url": "https://discord.com/api/webhooks/1/secret-token",
	"events": []any{"instance.down"}}

func hookWith(patch J) J {
	h := J{}
	for k, v := range testHook {
		h[k] = v
	}
	for k, v := range patch {
		h[k] = v
	}
	return h
}

type sentRequest struct {
	path    string
	body    []byte
	headers http.Header
}

// receiver : destination factice qui répond status et note chaque envoi.
func receiver(t *testing.T, status int) (*httptest.Server, *[]sentRequest, *sync.Mutex) {
	var mu sync.Mutex
	var got []sentRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		got = append(got, sentRequest{r.URL.RequestURI(), b, r.Header.Clone()})
		mu.Unlock()
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	saved := webhookRetryDelays
	webhookRetryDelays = []time.Duration{0, 0, 0}
	t.Cleanup(func() { webhookRetryDelays = saved })
	return srv, &got, &mu
}

func testItem(event string, patch func(*webhookItem)) webhookItem {
	it := webhookItem{event: event, level: "error", title: [2]string{"Titre", "Title"}, text: [2]string{"Texte", "Text"},
		subject: "frps", instance: "frps", data: J{}, time: "2026-09-30T10:00:00+02:00"}
	if patch != nil {
		patch(&it)
	}
	return it
}

func mustNormalize(t *testing.T, raw J) map[string]any {
	t.Helper()
	h, err := FR.webhookNormalize(raw, nil)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func drainQueue() []webhookItem {
	var out []webhookItem
	for {
		select {
		case it := <-webhookQueue:
			out = append(out, it)
		default:
			return out
		}
	}
}

func TestWebhookSaveHidesURLAndSecret(t *testing.T) {
	e := setup(t)
	c := e.owner()
	r := c.post("/api/webhooks", J{"webhooks": []any{hookWith(J{"secret": "s3cret"})}})
	expectCode(t, r, 200)
	public := r.json()["webhooks"].([]any)[0].(map[string]any)
	if public["url_hint"] != "https://discord.com/…" || public["has_secret"] != true {
		t.Fatalf("%v", public)
	}
	for _, body := range []string{r.text(), c.get("/api/webhooks").text()} {
		if strings.Contains(body, "secret-token") || strings.Contains(body, "s3cret") {
			t.Fatal("secret exposé")
		}
	}
	if e.savedConfig()["webhooks"].([]any)[0].(map[string]any)["url"] != testHook["url"] {
		t.Fatal("adresse non enregistrée")
	}
}

func TestWebhookBlankURLAndSecretKeepPrevious(t *testing.T) {
	e := setup(t)
	c := e.owner()
	id := c.post("/api/webhooks", J{"webhooks": []any{hookWith(J{"secret": "s3cret"})}}).json()["webhooks"].([]any)[0].(map[string]any)["id"]
	c.post("/api/webhooks", J{"webhooks": []any{hookWith(J{"id": id, "url": "", "name": "Renommé"})}})
	saved := e.savedConfig()["webhooks"].([]any)[0].(map[string]any)
	if saved["id"] != id || saved["name"] != "Renommé" || saved["url"] != testHook["url"] || saved["secret"] != "s3cret" {
		t.Fatalf("%v", saved)
	}
	c.post("/api/webhooks", J{"webhooks": []any{hookWith(J{"id": id, "secret_clear": true})}})
	if e.savedConfig()["webhooks"].([]any)[0].(map[string]any)["secret"] != "" {
		t.Fatal("secret gardé")
	}
}

func TestInvalidWebhookRefused(t *testing.T) {
	e := setup(t)
	c := e.owner()
	for _, patch := range []J{
		{"name": ""}, {"url": "ftp://x/y"}, {"url": "https://"}, {"url": "https://a b"}, {"format": "sms"},
		{"events": []any{}}, {"events": []any{"nimporte.quoi"}}, {"cooldown": -1}, {"cooldown": "abc"},
		{"headers": "pas-un-en-tete"}, {"format": "telegram"}, {"format": "ntfy", "url": "https://ntfy.sh/"},
		{"format": "generic", "template": `{"a": `}, {"format": "generic", "template": "pas du json"},
	} {
		r := c.post("/api/webhooks", J{"webhooks": []any{hookWith(patch)}})
		if r.code != 400 || r.json()["ok"] != false {
			t.Errorf("%v accepté (%d)", patch, r.code)
		}
	}
	if hooks, ok := e.savedConfig()["webhooks"].([]any); ok && len(hooks) > 0 {
		t.Fatal("webhook enregistré")
	}
}

func TestWebhooksRequireLogin(t *testing.T) {
	e := setup(t)
	e.setPassword(testPassword, "admin", false)
	c := e.client()
	expectCode(t, c.get("/api/webhooks"), 401)
	expectCode(t, c.post("/api/webhooks/test", J{"webhook": testHook}), 401)
}

func TestGenericPayloadAndSignature(t *testing.T) {
	setup(t)
	srv, got, _ := receiver(t, 204)
	hook := mustNormalize(t, J{"name": "n", "url": srv.URL + "/hook", "events": []any{"*"}, "secret": "cle", "headers": "Authorization: Bearer abc"})
	if ok, status, _ := webhookSend(hook, testItem("instance.down", nil)); !ok || status != 204 {
		t.Fatalf("%v %d", ok, status)
	}
	call := (*got)[0]
	var body map[string]any
	json.Unmarshal(call.body, &body)
	if body["event"] != "instance.down" || body["title"] != "Titre" || body["instance"] != "frps" {
		t.Fatalf("%v", body)
	}
	mac := hmac.New(sha256.New, []byte("cle"))
	mac.Write(call.body)
	if call.headers.Get("X-FRPManager-Signature") != "sha256="+hex.EncodeToString(mac.Sum(nil)) {
		t.Fatal("signature")
	}
	if call.headers.Get("Authorization") != "Bearer abc" || call.headers.Get("X-FRPManager-Event") != "instance.down" {
		t.Fatal("en-têtes")
	}
}

func TestWebhookRedirectNotFollowed(t *testing.T) {
	setup(t)
	target, got, _ := receiver(t, 204)
	redirect := httptest.NewServer(http.RedirectHandler(target.URL+"/ailleurs", http.StatusFound))
	defer redirect.Close()
	hook := mustNormalize(t, J{"name": "n", "url": redirect.URL + "/h", "events": []any{"*"}})
	if ok, status, _ := webhookSend(hook, testItem("instance.down", nil)); ok || status != 302 || len(*got) != 0 {
		t.Fatalf("redirection suivie (%v %d)", ok, status)
	}
}

func TestWebhookLanguage(t *testing.T) {
	setup(t)
	srv, got, _ := receiver(t, 204)
	webhookSend(mustNormalize(t, J{"name": "n", "url": srv.URL + "/h", "events": []any{"*"}, "lang": "en"}), testItem("instance.down", nil))
	var body map[string]any
	json.Unmarshal((*got)[0].body, &body)
	if body["title"] != "Title" {
		t.Fatal(body["title"])
	}
}

func TestTemplateEscapesValues(t *testing.T) {
	setup(t)
	srv, got, _ := receiver(t, 204)
	hook := mustNormalize(t, J{"name": "n", "url": srv.URL + "/h", "events": []any{"*"}, "template": `{"content": "{{title}}: {{message}}"}`})
	webhookSend(hook, testItem("instance.down", func(it *webhookItem) { it.text = [2]string{"dit \"bonjour\"\nligne 2", "x"} }))
	var body map[string]any
	if err := json.Unmarshal((*got)[0].body, &body); err != nil || body["content"] != "Titre: dit \"bonjour\"\nligne 2" {
		t.Fatalf("%v %s", err, (*got)[0].body)
	}
}

func TestWebhookFormats(t *testing.T) {
	setup(t)
	build := func(format, u string, extra J) (string, map[string]any) {
		raw := J{"name": "n", "format": format, "url": u, "events": []any{"*"}}
		for k, v := range extra {
			raw[k] = v
		}
		target, body, _ := webhookRequest(mustNormalize(t, raw), testItem("instance.down", nil))
		var m map[string]any
		json.Unmarshal(body, &m)
		return target, m
	}
	_, b := build("discord", "https://discord.com/api/webhooks/1/t", nil)
	embed := b["embeds"].([]any)[0].(map[string]any)
	if embed["title"] != "Titre" || embed["color"] != float64(0xEF4444) {
		t.Fatalf("discord %v", embed)
	}
	if _, b = build("slack", "https://hooks.slack.com/services/A/B/C", nil); b["attachments"].([]any)[0].(map[string]any)["text"] != "Texte" {
		t.Fatal("slack")
	}
	if u, b := build("telegram", "https://api.telegram.org/bot123:ABC", J{"chat_id": "-100"}); !strings.HasSuffix(u, "/bot123:ABC/sendMessage") || b["chat_id"] != "-100" {
		t.Fatal("telegram")
	}
	if u, b := build("ntfy", "https://ntfy.sh/mon-sujet", nil); u != "https://ntfy.sh/" || b["topic"] != "mon-sujet" || b["priority"] != float64(5) {
		t.Fatalf("ntfy %s %v", u, b)
	}
	if u, b := build("gotify", "https://gotify.lan/message?token=T", nil); u != "https://gotify.lan/message?token=T" || b["priority"] != float64(8) {
		t.Fatal("gotify")
	}
}

func TestTelegramEscapesHTML(t *testing.T) {
	setup(t)
	hook := mustNormalize(t, J{"name": "n", "format": "telegram", "url": "https://api.telegram.org/bot1:A", "chat_id": "1", "events": []any{"*"}})
	_, raw, _ := webhookRequest(hook, testItem("instance.down", func(it *webhookItem) { it.text = [2]string{"<b>x</b> & y", ""} }))
	var b map[string]any
	json.Unmarshal(raw, &b)
	if !strings.Contains(b["text"].(string), "&lt;b&gt;x&lt;/b&gt; &amp; y") {
		t.Fatal(b["text"])
	}
}

func TestWebhookRetriesThenGivesUp(t *testing.T) {
	setup(t)
	srv, got, _ := receiver(t, 503)
	ok, status, msg := webhookSend(mustNormalize(t, hookWith(J{"url": srv.URL + "/x"})), testItem("instance.down", nil))
	if ok || status != 503 || msg != "HTTP 503" || len(*got) != len(webhookRetryDelays) {
		t.Fatalf("%v %d %s (%d essais)", ok, status, msg, len(*got))
	}
}

func TestWebhookClientErrorNotRetried(t *testing.T) {
	setup(t)
	srv, got, _ := receiver(t, 404)
	if ok, status, _ := webhookSend(mustNormalize(t, hookWith(J{"url": srv.URL + "/x"})), testItem("instance.down", nil)); ok || status != 404 || len(*got) != 1 {
		t.Fatalf("%d essais", len(*got))
	}
}

func TestWebhookMatching(t *testing.T) {
	setup(t)
	hook := map[string]any{"id": "a", "events": []any{"instance.down"}, "instances": []any{"frps"}, "cooldown": float64(60)}
	if webhookMatches(hook, testItem("instance.up", nil)) {
		t.Fatal("événement")
	}
	if webhookMatches(hook, testItem("instance.down", func(it *webhookItem) { it.instance = "frpc" })) {
		t.Fatal("instance")
	}
	if !webhookMatches(hook, testItem("instance.down", nil)) {
		t.Fatal("premier envoi")
	}
	if webhookMatches(hook, testItem("instance.down", nil)) {
		t.Fatal("délai")
	}
	if !webhookMatches(hook, testItem("instance.down", func(it *webhookItem) { it.subject = "autre" })) {
		t.Fatal("autre sujet")
	}
	if !webhookMatches(map[string]any{"id": "b", "events": []any{"*"}}, testItem("login.success", func(it *webhookItem) { it.instance = "" })) {
		t.Fatal("*")
	}
}

func TestWebhookTestEndpoint(t *testing.T) {
	e := setup(t)
	srv, got, _ := receiver(t, 204)
	c := e.owner()
	r := c.post("/api/webhooks/test", J{"webhook": hookWith(J{"url": srv.URL + "/x"})})
	if r.json()["ok"] != true || len(*got) != 1 {
		t.Fatalf("%s", r.text())
	}
	if c.get("/api/webhooks").json()["history"].([]any)[0].(map[string]any)["event"] != "test" {
		t.Fatal("historique")
	}
	expectCode(t, c.post("/api/webhooks/test", J{"webhook": hookWith(J{"url": "nope"})}), 400)
}

func TestNoActiveWebhookQueuesNothing(t *testing.T) {
	setup(t)
	drainQueue()
	webhookEmit("instance.down", [2]string{"a", "a"}, [2]string{"b", "b"}, "", nil)
	if len(drainQueue()) != 0 {
		t.Fatal("mis en file sans webhook")
	}
	cfgUpdate(Cfg{"webhooks": []any{mustNormalize(t, testHook)}})
	webhookEmit("instance.down", [2]string{"a", "a"}, [2]string{"b", "b"}, "", nil)
	webhookEmit("evenement.inconnu", [2]string{"a", "a"}, [2]string{"b", "b"}, "", nil)
	if q := drainQueue(); len(q) != 1 || q[0].event != "instance.down" {
		t.Fatalf("%v", q)
	}
}

func TestInstanceChangeNeedsTwoReadings(t *testing.T) {
	setup(t)
	cfgUpdate(Cfg{"webhooks": []any{mustNormalize(t, hookWith(J{"events": []any{"*"}}))}})
	drainQueue()
	seen, pending := map[string]bool{}, map[string]bool{}
	state := func(running bool) map[string]J {
		return map[string]J{"frps": {"binary_found": true, "service": "frps", "status": J{"running": running}}}
	}
	for _, running := range []bool{true, false, true, false, false} { // panne d'un seul relevé, puis panne réelle
		webhookInstanceEvents(state(running), seen, pending)
	}
	var events []string
	for _, it := range drainQueue() {
		events = append(events, it.event)
	}
	if !reflect.DeepEqual(events, []string{"instance.down"}) {
		t.Fatalf("%v", events)
	}
}

func TestLoginLockoutEmits(t *testing.T) {
	e := setup(t)
	e.setPassword(testPassword, "admin", false)
	cfgUpdate(Cfg{"webhooks": []any{mustNormalize(t, hookWith(J{"events": []any{"*"}}))}})
	drainQueue()
	c := e.client()
	for i := 0; i < loginFreeAttempts; i++ {
		c.login("admin", "faux")
	}
	q := drainQueue()
	if len(q) != 1 || q[0].event != "login.locked" || q[0].data["failures"] != loginFreeAttempts {
		t.Fatalf("%v", q)
	}
}
