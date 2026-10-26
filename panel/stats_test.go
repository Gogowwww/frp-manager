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

// waitStatsSent : attend la fin de l'envoi lancé en arrière-plan (go sendStats())
// par le panel : il écrit dans la configuration partagée, donc doit finir
// avant que le test ne recrée un environnement ou ne se termine.
// waitStatsSent: waits for the background send (go sendStats()) started by the
// panel to finish: it writes to the shared configuration, so it must be over
// before the test builds another environment or ends.
func waitStatsSent(t *testing.T, got *[]map[string]any, mu *sync.Mutex) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		mu.Lock()
		n := len(*got)
		mu.Unlock()
		if n > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("envoi des statistiques jamais reçu")
		}
		time.Sleep(10 * time.Millisecond)
	}
	// sendStats garde statsMu jusqu'à l'enregistrement du jour d'envoi
	// sendStats holds statsMu until the send day is saved
	statsMu.Lock()
	statsMu.Unlock() //nolint:staticcheck // barrière : attendre la fin / barrier: wait for the end
}

func TestStatsChoiceAtSetupAndInSettings(t *testing.T) {
	e := setup(t)
	got, mu := statsReceiver(t)
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
	waitStatsSent(t, got, mu)

	other := setup(t)
	got2, mu2 := statsReceiver(t)
	expectCode(t, other.client().post("/api/setup", J{"username": "moi", "password": testPassword, "anonymous_stats": true}), 200)
	defer waitStatsSent(t, got2, mu2)
	if other.savedConfig()["anonymous_stats"] != true {
		t.Fatal("case de la configuration initiale ignorée")
	}
}

// L'adresse par défaut est celle du projet, en HTTPS : ce que disent la doc et SECURITY.
// The default address is the project's, over HTTPS: what the docs and SECURITY say.
func TestStatsDefaultURL(t *testing.T) {
	if defaultStatsURL != "https://frp-manager.org/api/ping" {
		t.Fatalf("adresse par défaut inattendue : %s", defaultStatsURL)
	}
}
