// Adresse réelle du client derrière un reverse proxy. Les en-têtes
// X-Forwarded-For / X-Forwarded-Proto ne sont crus que si la connexion vient
// d'une adresse ou d'un réseau listé dans trusted_proxies : sinon n'importe qui
// pourrait se faire passer pour une autre adresse (journaux, limitation de
// débit, pare-feu).
// Real client address behind a reverse proxy. X-Forwarded-For / X-Forwarded-Proto
// are trusted only when the connection comes from an address or network listed in
// trusted_proxies: otherwise anyone could impersonate another address (logs,
// rate limiting, firewall).

package main

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
)

var (
	proxyMu    sync.Mutex
	proxyRaw   string
	proxyCache []netip.Prefix
)

// proxyFields : entrées de trusted_proxies (virgules, espaces ou retours à la
// ligne), vides retirées.
// proxyFields: trusted_proxies entries (commas, spaces or newlines), empties dropped.
func proxyFields(raw string) []string {
	var out []string
	for _, f := range strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == '\n' || r == '\r'
	}) {
		out = append(out, f)
	}
	return out
}

// parseProxyList : réseaux valides de la liste (une adresse seule compte pour
// elle-même) ; les entrées invalides sont ignorées (l'API les refuse déjà).
// parseProxyList: valid networks of the list (a bare address counts for itself);
// invalid entries are ignored (the API already rejects them).
func parseProxyList(raw string) []netip.Prefix {
	out := []netip.Prefix{}
	for _, f := range proxyFields(raw) {
		if p, err := netip.ParsePrefix(f); err == nil {
			out = append(out, p.Masked())
			continue
		}
		if a, err := netip.ParseAddr(f); err == nil {
			out = append(out, netip.PrefixFrom(a.WithZone(""), a.BitLen()))
		}
	}
	return out
}

// trustedProxyPrefixes : liste de confiance courante (relue quand la config
// change, sans reparcours à chaque requête).
// trustedProxyPrefixes: current trusted list (reloaded when the config changes,
// not reparsed on every request).
func trustedProxyPrefixes() []netip.Prefix {
	raw := str(cfg(), "trusted_proxies", "")
	proxyMu.Lock()
	defer proxyMu.Unlock()
	if proxyCache == nil || raw != proxyRaw {
		proxyRaw, proxyCache = raw, parseProxyList(raw)
	}
	return proxyCache
}

// peerAddr : adresse de la connexion TCP (sans port ni zone), invalide si elle
// n'est pas une IP (socket Unix, test).
// peerAddr: TCP connection address (no port, no zone), invalid when not an IP
// (Unix socket, test).
func peerAddr(remoteAddr string) netip.Addr {
	host := remoteAddr
	if h, _, err := net.SplitHostPort(remoteAddr); err == nil {
		host = h
	}
	a, err := netip.ParseAddr(strings.Trim(host, "[]"))
	if err != nil {
		return netip.Addr{}
	}
	return a.WithZone("")
}

func proxyTrusted(a netip.Addr, trusted []netip.Prefix) bool {
	if !a.IsValid() {
		return false
	}
	for _, p := range trusted {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// clientIPFrom : adresse du client. X-Forwarded-For n'est lu que si la connexion
// vient d'un proxy de confiance ; on remonte la liste depuis la droite et on
// s'arrête à la première adresse qui n'est pas un proxy de confiance, car les
// entrées de gauche sont écrites par le client. ""
// si l'adresse de connexion n'est pas une IP.
// clientIPFrom: the client's address. X-Forwarded-For is read only when the
// connection comes from a trusted proxy; the list is walked from the right and
// stops at the first address that is not a trusted proxy, since the entries on
// the left are written by the client. "" when the connection address is not an IP.
func clientIPFrom(remoteAddr, xff string, trusted []netip.Prefix) string {
	peer := peerAddr(remoteAddr)
	if !peer.IsValid() {
		return ""
	}
	if !proxyTrusted(peer, trusted) || strings.TrimSpace(xff) == "" {
		return peer.String()
	}
	ip := peer
	for _, part := range reverse(strings.Split(xff, ",")) {
		hop, err := netip.ParseAddr(strings.TrimSpace(part))
		if err != nil {
			break
		}
		ip = hop.WithZone("")
		if !proxyTrusted(ip, trusted) {
			break
		}
	}
	return ip.String()
}

// requestScheme : schéma vu par le navigateur ; X-Forwarded-Proto n'est cru que
// depuis un proxy de confiance.
// requestScheme: scheme seen by the browser; X-Forwarded-Proto is trusted only
// from a trusted proxy.
func requestScheme(r *http.Request, trusted []netip.Prefix) string {
	if r.TLS != nil {
		return "https"
	}
	if proxyTrusted(peerAddr(r.RemoteAddr), trusted) {
		switch strings.ToLower(strings.TrimSpace(r.Header.Get("X-Forwarded-Proto"))) {
		case "https":
			return "https"
		case "http":
			return "http"
		}
	}
	return "http"
}

func reverse(list []string) []string {
	for i, j := 0, len(list)-1; i < j; i, j = i+1, j-1 {
		list[i], list[j] = list[j], list[i]
	}
	return list
}
