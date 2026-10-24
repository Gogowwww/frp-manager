package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// statsReceiver : faux site du projet qui garde les signaux reçus.
// statsReceiver: fake project website that keeps the pings it receives.
func statsReceiver(t *testing.T) (*[]map[string]any, *sync.Mutex) {
	t.Helper()
	var (
		mu   sync.Mutex
		got  []map[string]any
		seen = &got
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var m map[string]any
		if r.Method != http.MethodPost || json.Unmarshal(raw, &m) != nil {
			http.Error(w, "bad", http.StatusBadRequest)
			return
		}
		mu.Lock()
		got = append(got, m)
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)
	savedURL, savedVersion := statsURL, panelVersion
	t.Cleanup(func() { statsURL, panelVersion = savedURL, savedVersion })
	statsURL, panelVersion = srv.URL, "0.4.0"
	return seen, &mu
}

func TestStatsPayloadHasOnlyThreeFields(t *testing.T) {
	setup(t)
	p := statsPayload()
	if len(p) != 3 || p["version"] != panelVersion || p["arch"] != getArch() || p["install"] != "script" {
		t.Fatalf("%v", p)
	}
}

// Rien sans accord ; avec accord, un seul signal par jour, identique à l'aperçu.
// Nothing without consent; with consent, a single ping a day, same as the preview.
func TestSendStatsOnlyWhenEnabledAndOncePerDay(t *testing.T) {
	setup(t)
	got, mu := statsReceiver(t)
	if err := sendStats(); err != nil || len(*got) != 0 {
		t.Fatalf("envoi sans accord : %v %v", err, *got)
	}
	cfgUpdate(Cfg{"anonymous_stats": true})
	if err := sendStats(); err != nil {
		t.Fatal(err)
	}
	if err := sendStats(); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(*got) != 1 {
		t.Fatalf("%d signaux", len(*got))
	}
	want, _ := json.Marshal(statsPayload())
	sent, _ := json.Marshal((*got)[0])
	if string(sent) != string(want) {
		t.Fatalf("envoyé %s, attendu %s", sent, want)
	}
	if str(cfg(), "anonymous_stats_sent", "") != time.Now().UTC().Format("2006-01-02") {
		t.Fatal("jour d'envoi non retenu")
	}
}

func TestSendStatsSkipsDevelopmentBuild(t *testing.T) {
	setup(t)
	got, _ := statsReceiver(t)
	panelVersion = "0.0.0"
	cfgUpdate(Cfg{"anonymous_stats": true})
	if err := sendStats(); err != nil || len(*got) != 0 {
		t.Fatalf("%v %v", err, *got)
	}
}

func TestStatsChoiceAtSetupAndInSettings(t *testing.T) {
	e := setup(t)
	statsReceiver(t)
	c := e.client()
	expectCode(t, c.post("/api/setup", J{"username": "moi", "password": testPassword}), 200)
	if e.savedConfig()["anonymous_stats"] != false {
		t.Fatal("activé sans accord")
	}
	r := c.get("/api/manager/config")
	expectCode(t, r, 200)
	conf, _ := r.json()["config"].(map[string]any)
	if p, _ := conf["anonymous_stats_payload"].(map[string]any); p["install"] != "script" {
		t.Fatalf("aperçu absent : %v", conf)
	}
	expectCode(t, c.post("/api/manager/config", J{"anonymous_stats": true}), 200)
	if e.savedConfig()["anonymous_stats"] != true {
		t.Fatal("accord non enregistré")
	}

	other := setup(t)
	statsReceiver(t)
	expectCode(t, other.client().post("/api/setup", J{"username": "moi", "password": testPassword, "anonymous_stats": true}), 200)
	if other.savedConfig()["anonymous_stats"] != true {
		t.Fatal("case de la configuration initiale ignorée")
	}
}
