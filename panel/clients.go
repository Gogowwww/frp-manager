// ── Clients : les frpc connectés à un frps, et les ports que chacun héberge ──
// Clients: the frpc connected to one frps, and the ports each one hosts.
//
// Tout vient de l'API du tableau de bord frps ([webServer] de sa configuration) :
// /api/clients donne les connexions (adresse, hôte, état, dates), et
// /api/proxy/<type> les tunnels de chacun (nom, port public ou domaine).
// Everything comes from the frps dashboard API ([webServer] in its config):
// /api/clients gives the connections (address, host, state, dates), and
// /api/proxy/<type> the tunnels of each one (name, public port or domain).

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"time"
)

// errNoDashboard : frps sans [webServer], donc sans API à interroger.
// errNoDashboard: frps without [webServer], so no API to query.
var errNoDashboard = errors.New("no dashboard")

// frpsProxyTypes : les types de tunnels que frps sait lister.
// frpsProxyTypes: the tunnel types frps can list.
var frpsProxyTypes = []string{"tcp", "udp", "http", "https", "tcpmux", "stcp", "xtcp", "sudp"}

type clientPort struct {
	Name   string `json:"name"`
	Type   string `json:"type"`
	Where  string `json:"where"` // port public ou domaine, "" s'il n'y en a pas / public port or domain, "" if none
	Status string `json:"status"`
}

type frpsClient struct {
	User           string       `json:"user"`
	ClientID       string       `json:"clientID"`
	Hostname       string       `json:"hostname"`
	IP             string       `json:"ip"`
	Version        string       `json:"version"`
	Protocol       string       `json:"protocol"`
	Online         bool         `json:"online"`
	FirstConnected int64        `json:"firstConnected"`
	LastConnected  int64        `json:"lastConnected"`
	Ports          []clientPort `json:"ports"`
}

// clientProxy : un tunnel et le client qui l'a ouvert.
// clientProxy: a tunnel and the client that opened it.
type clientProxy struct {
	user, clientID, version string
	port                    clientPort
}

// frpsAPIGet : document JSON de l'API du tableau de bord frps.
// frpsAPIGet: JSON document from the frps dashboard API.
func frpsAPIGet(conf map[string]any, path string) (any, error) {
	ws := mapv(conf, "webServer")
	port := tomlInt(ws["port"])
	if port == 0 {
		return nil, errNoDashboard
	}
	addr := str(ws, "addr", "")
	if addr == "" || addr == "0.0.0.0" || addr == "::" {
		addr = "127.0.0.1"
	}
	req, err := http.NewRequest(http.MethodGet, "http://"+urlHostPort(addr, port)+path, nil)
	if err != nil {
		return nil, err
	}
	if user := str(ws, "user", ""); user != "" {
		req.SetBasicAuth(user, str(ws, "password", ""))
	}
	resp, err := (&http.Client{Timeout: 3 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("%s : HTTP %d", path, resp.StatusCode)
	}
	var body any
	dec := json.NewDecoder(io.LimitReader(resp.Body, 8<<20))
	dec.UseNumber()
	if err := dec.Decode(&body); err != nil {
		return nil, err
	}
	return body, nil
}

// proxyWhere : adresse publique du tunnel — numéro de port pour tcp et udp,
// domaine pour http, https et tcpmux ; vide pour stcp, xtcp et sudp, que le
// visiteur joint de client à client.
// proxyWhere: the tunnel's public address — port number for tcp and udp,
// domain for http, https and tcpmux; empty for stcp, xtcp and sudp, which the
// visitor reaches client to client.
func proxyWhere(ptype string, conf map[string]any) string {
	switch ptype {
	case "tcp", "udp":
		// pyInt : le port arrive en json.Number, pas en int64 comme dans un TOML
		// pyInt: the port arrives as a json.Number, not as int64 like in TOML
		if p, ok := pyInt(conf["remotePort"]); ok && p != 0 {
			return strconv.Itoa(p)
		}
	case "http", "https", "tcpmux":
		for _, d := range listv(conf, "customDomains") {
			if s, ok := d.(string); ok && s != "" {
				return s
			}
		}
		if s := str(conf, "subDomain", ""); s != "" {
			return s
		}
	}
	return ""
}

// frpsProxies : tous les tunnels que frps connaît, attribués à leur client.
// frpsProxies: every tunnel frps knows about, attributed to its client.
func frpsProxies(conf map[string]any) []clientProxy {
	var out []clientProxy
	for _, ptype := range frpsProxyTypes {
		body, err := frpsAPIGet(conf, "/api/proxy/"+ptype)
		if err != nil {
			continue
		}
		b, ok := body.(map[string]any)
		if !ok {
			continue
		}
		for _, item := range listv(b, "proxies") {
			p, _ := item.(map[string]any)
			if p == nil {
				continue
			}
			out = append(out, clientProxy{
				user:     str(p, "user", ""),
				clientID: str(p, "clientID", ""),
				version:  str(p, "clientVersion", ""),
				port: clientPort{
					Name:   str(p, "name", ""),
					Type:   ptype,
					Where:  proxyWhere(ptype, mapv(p, "conf")),
					Status: str(p, "status", ""),
				},
			})
		}
	}
	return out
}

// sameClient : un tunnel appartient-il à ce client ? L'identifiant de connexion
// est unique ; à défaut (frps ancien), le nom d'utilisateur fait foi.
// sameClient: does a tunnel belong to this client? The connection identifier is
// unique; failing that (older frps), the user name decides.
func sameClient(c frpsClient, p clientProxy) bool {
	if c.ClientID != "" && p.clientID != "" {
		return c.ClientID == p.clientID
	}
	return c.ClientID == "" && p.clientID == "" && c.User == p.user
}

func portsOf(c frpsClient, proxies []clientProxy) []clientPort {
	ports := []clientPort{}
	for _, p := range proxies {
		if sameClient(c, p) {
			ports = append(ports, p.port)
		}
	}
	sort.SliceStable(ports, func(i, j int) bool {
		if ports[i].Name != ports[j].Name {
			return ports[i].Name < ports[j].Name
		}
		return ports[i].Type < ports[j].Type
	})
	return ports
}

// clientsFromProxies : repli pour un frps sans /api/clients (avant la 0.60) :
// les clients sont ceux qui ont un tunnel, sans adresse ni date.
// clientsFromProxies: fallback for an frps without /api/clients (before 0.60):
// the clients are those with a tunnel, without address or dates.
func clientsFromProxies(proxies []clientProxy) []frpsClient {
	var out []frpsClient
	seen := map[string]bool{}
	for _, p := range proxies {
		key := p.clientID
		if key == "" {
			key = "user:" + p.user
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		c := frpsClient{
			User: p.user, ClientID: p.clientID, Version: p.version,
			Online: true, Ports: []clientPort{},
		}
		c.Ports = portsOf(c, proxies)
		out = append(out, c)
	}
	return out
}

// frpsClients : clients connus de frps (connectés et déjà vus), avec leurs
// ports. errNoDashboard si frps n'expose pas de tableau de bord.
// frpsClients: clients known to frps (connected and seen before), with their
// ports. errNoDashboard if frps does not expose a dashboard.
func frpsClients(conf map[string]any) ([]frpsClient, error) {
	body, err := frpsAPIGet(conf, "/api/clients")
	if err != nil && errors.Is(err, errNoDashboard) {
		return nil, err
	}
	proxies := frpsProxies(conf)
	if err != nil {
		// API /api/clients absente : on se rabat sur les tunnels
		// /api/clients missing: fall back to the tunnels
		if len(proxies) == 0 {
			return nil, err
		}
		return clientsFromProxies(proxies), nil
	}
	raw, _ := body.([]any)
	if b, ok := body.(map[string]any); ok {
		raw = listv(b, "clients")
	}
	clients := make([]frpsClient, 0, len(raw))
	for _, item := range raw {
		m, _ := item.(map[string]any)
		if m == nil {
			continue
		}
		first, _ := pyInt(m["firstConnectedAt"])
		last, _ := pyInt(m["lastConnectedAt"])
		c := frpsClient{
			User:           str(m, "user", ""),
			ClientID:       str(m, "clientID", ""),
			Hostname:       str(m, "hostname", ""),
			IP:             str(m, "clientIP", ""),
			Version:        str(m, "version", ""),
			Protocol:       str(m, "wireProtocol", ""),
			Online:         truthy(m["online"]),
			FirstConnected: int64(first),
			LastConnected:  int64(last),
		}
		c.Ports = portsOf(c, proxies)
		// Version : celle du client, sinon celle qu'annoncent ses tunnels
		// Version: the client's own, otherwise the one its tunnels report
		if c.Version == "" {
			for _, p := range proxies {
				if p.version != "" && sameClient(c, p) {
					c.Version = p.version
					break
				}
			}
		}
		clients = append(clients, c)
	}
	sort.SliceStable(clients, func(i, j int) bool {
		a, b := clients[i], clients[j]
		if a.Online != b.Online {
			return a.Online
		}
		if a.User != b.User {
			return a.User < b.User
		}
		return a.ClientID < b.ClientID
	})
	return clients, nil
}
