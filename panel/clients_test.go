package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	toml "github.com/pelletier/go-toml/v2"
)

// fakeFrps : API du tableau de bord frps, réduite aux routes lues par la page
// Clients. clients = corps de /api/clients (nil : route absente, comme avant la
// 0.60) ; proxies = corps de /api/proxy/<type>.
// fakeFrps: the frps dashboard API, reduced to the routes the Clients page
// reads. clients = /api/clients body (nil: route missing, as before 0.60);
// proxies = /api/proxy/<type> body.
func fakeFrps(t *testing.T, clients string, proxies map[string]string) map[string]any {
	t.Helper()
	mux := http.NewServeMux()
	if clients != "" {
		mux.HandleFunc("/api/clients", func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprint(w, clients)
		})
	}
	for ptype, body := range proxies {
		mux.HandleFunc("/api/proxy/"+ptype, func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprint(w, body)
		})
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	port, _ := strconv.Atoi(u.Port())
	return map[string]any{"webServer": map[string]any{"addr": "127.0.0.1", "port": int64(port)}}
}

const testClientsJSON = `[{"key":"k1","user":"nas","clientID":"abcdef0123456789","hostname":"nas","clientIP":"203.0.113.7","firstConnectedAt":1700000000,"lastConnectedAt":1700000500,"online":true}]`

const testTCPJSON = `{"proxies":[{"name":"nas-web","user":"nas","clientID":"abcdef0123456789","clientVersion":"0.68.1","status":"online","conf":{"remotePort":20001}}]}`
const testHTTPJSON = `{"proxies":[{"name":"blog","user":"nas","clientID":"abcdef0123456789","status":"online","conf":{"customDomains":["blog.example.org"]}}]}`

func TestFrpsClients(t *testing.T) {
	conf := fakeFrps(t, testClientsJSON, map[string]string{"tcp": testTCPJSON, "http": testHTTPJSON})
	clients, err := frpsClients(conf)
	if err != nil {
		t.Fatalf("frpsClients : %v", err)
	}
	if len(clients) != 1 {
		t.Fatalf("%d clients, attendu 1", len(clients))
	}
	c := clients[0]
	if c.User != "nas" || c.IP != "203.0.113.7" || !c.Online {
		t.Fatalf("client inattendu : %+v", c)
	}
	if c.LastConnected != 1700000500 {
		t.Fatalf("lastConnected = %v", c.LastConnected)
	}
	// Version : /api/clients (frps ancien) ne la donne pas, le tunnel si
	if c.Version != "0.68.1" {
		t.Fatalf("version = %q", c.Version)
	}
	if len(c.Ports) != 2 {
		t.Fatalf("%d ports, attendu 2 : %+v", len(c.Ports), c.Ports)
	}
	// Triés par nom : blog (domaine) avant nas-web (port)
	if c.Ports[0].Name != "blog" || c.Ports[0].Where != "blog.example.org" {
		t.Fatalf("port http : %+v", c.Ports[0])
	}
	if c.Ports[1].Name != "nas-web" || c.Ports[1].Where != "20001" || c.Ports[1].Type != "tcp" {
		t.Fatalf("port tcp : %+v", c.Ports[1])
	}
}

// frps avant la 0.60 : pas de /api/clients, les clients se déduisent des tunnels.
// frps before 0.60: no /api/clients, clients are derived from the tunnels.
func TestFrpsClientsWithoutClientAPI(t *testing.T) {
	conf := fakeFrps(t, "", map[string]string{"tcp": testTCPJSON})
	clients, err := frpsClients(conf)
	if err != nil {
		t.Fatalf("frpsClients : %v", err)
	}
	if len(clients) != 1 || clients[0].User != "nas" || !clients[0].Online {
		t.Fatalf("clients = %+v", clients)
	}
	if len(clients[0].Ports) != 1 || clients[0].Ports[0].Where != "20001" {
		t.Fatalf("ports = %+v", clients[0].Ports)
	}
}

// Sans [webServer], il n'y a pas d'API : la page doit le dire, pas afficher zéro
// client.
// Without [webServer] there is no API: the page must say so, not show zero
// clients.
func TestFrpsClientsNoDashboard(t *testing.T) {
	conf := fakeFrps(t, testClientsJSON, nil)
	delete(conf, "webServer")
	if _, err := frpsClients(conf); err == nil {
		t.Fatal("errNoDashboard attendu")
	}
}

// Le bouton « Activer le tableau de bord » ajoute un bloc [webServer] local, et
// ne touche à rien s'il existe déjà.
// The "Enable the dashboard" button adds a local [webServer] block, and leaves
// an existing one alone.
func TestEnableFrpsDashboard(t *testing.T) {
	in := "bindPort = 7000\nauth.token = \"x\"\n\n[[allowPorts]]\nsingle = 2222\n"
	out, changed, err := enableFrpsDashboard(FR, in, 7501, "")
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	for _, want := range []string{"[webServer]", `addr = "127.0.0.1"`, "port = 7501", `user = "frp-manager"`} {
		if !strings.Contains(out, want) {
			t.Fatalf("bloc sans %q :\n%s", want, out)
		}
	}
	// Le résultat doit se relire, mot de passe compris
	// The result must parse, password included
	var conf map[string]any
	if err := toml.Unmarshal([]byte(out), &conf); err != nil {
		t.Fatalf("TOML invalide : %v\n%s", err, out)
	}
	ws := mapv(conf, "webServer")
	if tomlInt(ws["port"]) != 7501 || str(ws, "password", "") == "" {
		t.Fatalf("webServer = %v", ws)
	}
	if strings.Contains(in, "[webServer]") {
		t.Fatal("la config de départ ne doit pas déjà en avoir")
	}

	again, changed, err := enableFrpsDashboard(FR, out, 7502, "")
	if err != nil || changed || again != out {
		t.Fatalf("déjà configuré : changed=%v err=%v", changed, err)
	}

	// webServer sans port : on demande de le corriger plutôt que d'inventer
	// webServer without a port: ask to fix it rather than guess
	if _, _, err := enableFrpsDashboard(FR, "webServer.addr = \"127.0.0.1\"\n", 7500, ""); err == nil {
		t.Fatal("webServer sans port : erreur attendue")
	}
	// Conteneur sur un réseau Docker : il écoute sur toutes ses interfaces,
	// le panel visant son adresse de conteneur.
	// Container on a Docker network: it listens on all its interfaces, the panel
	// aiming at its container address.
	wide, changed, err := enableFrpsDashboard(FR, in, 7500, "0.0.0.0")
	if err != nil || !changed || !strings.Contains(wide, `addr = "0.0.0.0"`) {
		t.Fatalf("conteneur : changed=%v err=%v\n%s", changed, err, wide)
	}
	if _, _, err := enableFrpsDashboard(FR, "bindPort = [", 7500, ""); err == nil {
		t.Fatal("TOML invalide : erreur attendue")
	}
}

// Deux clients : les connectés d'abord.
// Two clients: connected ones first.
func TestFrpsClientsSortOnlineFirst(t *testing.T) {
	body := `[{"user":"b","clientID":"b1","online":false,"lastConnectedAt":1700000000},` +
		`{"user":"a","clientID":"a1","online":true,"lastConnectedAt":1700000100}]`
	conf := fakeFrps(t, body, nil)
	clients, err := frpsClients(conf)
	if err != nil {
		t.Fatal(err)
	}
	if len(clients) != 2 || clients[0].User != "a" {
		t.Fatalf("ordre : %+v", clients)
	}
}
