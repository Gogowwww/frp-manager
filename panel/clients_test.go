package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
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
