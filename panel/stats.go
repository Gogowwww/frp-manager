// Statistiques anonymes, sur accord de l'administrateur (anonymous_stats,
// désactivé par défaut). Une fois par jour au plus, le panel envoie au site du
// projet trois informations : sa version, son architecture et sa méthode
// d'installation. Aucun identifiant, aucun nom, aucune adresse : le site ne
// garde que des compteurs par jour, sans les adresses IP.
// Anonymous statistics, with the administrator's consent (anonymous_stats,
// off by default). At most once a day, the panel sends the project website
// three facts: its version, its architecture and its install method. No
// identifier, no name, no address: the website only keeps daily counters,
// without IP addresses.

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sync"
	"time"
)

// statsURL : adresse qui reçoit le signal ; FRP_MANAGER_STATS_URL la remplace
// (tests, site de test).
// statsURL: address that receives the ping; FRP_MANAGER_STATS_URL overrides it
// (tests, test website).
var statsURL = func() string {
	if u := os.Getenv("FRP_MANAGER_STATS_URL"); u != "" {
		return u
	}
	return defaultStatsURL
}()

// defaultStatsURL : le site du projet. L'ancienne adresse (frp-manager.gogow.fr)
// répond toujours, pour les panels déjà installés.
// defaultStatsURL: the project website. The old address (frp-manager.gogow.fr)
// still answers, for panels that are already installed.
const defaultStatsURL = "https://frp-manager.org/api/ping"

var statsMu sync.Mutex // un envoi à la fois / one send at a time

// installMethod : docker, apt ou script (install.sh, ou lancement à la main).
// installMethod: docker, apt or script (install.sh, or started by hand).
func installMethod() string {
	switch {
	case inDocker:
		return "docker"
	case installedByApt():
		return "apt"
	}
	return "script"
}

// statsPayload : tout ce qui est envoyé, rien d'autre (affiché tel quel dans
// Réglages).
// statsPayload: everything that is sent, nothing else (shown as is in
// Settings).
func statsPayload() J {
	return J{"version": panelVersion, "arch": getArch(), "install": installMethod()}
}

func statsEnabled() bool { return boolv(cfg(), "anonymous_stats", false) }

// sendStats : envoie le signal du jour s'il est activé et pas encore parti
// aujourd'hui (jour UTC). Une version de développement (0.0.0) n'envoie rien.
// sendStats: sends today's ping if it is on and not sent yet today (UTC day).
// A development build (0.0.0) sends nothing.
func sendStats() error {
	statsMu.Lock()
	defer statsMu.Unlock()
	today := time.Now().UTC().Format("2006-01-02")
	if !statsEnabled() || panelVersion == "0.0.0" || str(cfg(), "anonymous_stats_sent", "") == today {
		return nil
	}
	body, _ := json.Marshal(statsPayload())
	req, err := http.NewRequest(http.MethodPost, statsURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "frp-manager")
	client := &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	_, err = cfgUpdate(Cfg{"anonymous_stats_sent": today})
	return err
}

// statsLoop : premier essai 10 minutes après le démarrage, puis toutes les
// heures (un échec réseau est retenté l'heure suivante).
// statsLoop: first try 10 minutes after start, then every hour (a network
// failure is retried the next hour).
func statsLoop() {
	time.Sleep(10 * time.Minute)
	for {
		_ = sendStats()
		time.Sleep(time.Hour)
	}
}
