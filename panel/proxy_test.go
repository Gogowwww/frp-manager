package main

import (
	"crypto/tls"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
)

func TestParseProxyList(t *testing.T) {
	list := parseProxyList("10.0.0.0/8, 192.168.1.10\n2001:db8::/32,,nawak")
	if len(list) != 3 {
		t.Fatalf("%d réseau(x) reconnu(s), attendu 3 : %v", len(list), list)
	}
	if got := list[1].String(); got != "192.168.1.10/32" {
		t.Fatalf("adresse seule : %q", got)
	}
	if len(parseProxyList("")) != 0 {
		t.Fatal("liste vide non vide")
	}
}

func TestClientIPFrom(t *testing.T) {
	cases := []struct {
		name, peer, xff string
		trust           []string
		want            string
	}{
		{"sans proxy de confiance", "192.0.2.10:5000", "203.0.113.9", nil, "192.0.2.10"},
		{"proxy non listé", "192.0.2.10:5000", "203.0.113.9", []string{"10.0.0.0/8"}, "192.0.2.10"},
		{"proxy listé, sans en-tête", "10.0.0.5:5000", "", []string{"10.0.0.0/8"}, "10.0.0.5"},
		{"un relais", "10.0.0.5:5000", "203.0.113.9", []string{"10.0.0.0/8"}, "203.0.113.9"},
		// Le client a écrit « 1.2.3.4 » à gauche : la dernière entrée non fiable
		// (celle ajoutée par le proxy) est retenue.
		// The client wrote "1.2.3.4" on the left: the last untrusted entry (added
		// by the proxy) is kept.
		{"entrée forgée à gauche", "10.0.0.5:5000", "1.2.3.4, 10.0.0.7, 203.0.113.9", []string{"10.0.0.0/8"}, "203.0.113.9"},
		{"deux relais de confiance", "10.0.0.5:5000", "203.0.113.9, 10.0.0.7", []string{"10.0.0.0/8"}, "203.0.113.9"},
		{"en-tête illisible", "10.0.0.5:5000", "nawak", []string{"10.0.0.0/8"}, "10.0.0.5"},
	}
	for _, tc := range cases {
		var trust []netip.Prefix
		if tc.trust != nil {
			trust = parseProxyList(strings.Join(tc.trust, ","))
		}
		if got := clientIPFrom(tc.peer, tc.xff, trust); got != tc.want {
			t.Errorf("%s : %q, attendu %q", tc.name, got, tc.want)
		}
	}
}

func TestRequestScheme(t *testing.T) {
	trusted := parseProxyList("127.0.0.1/32")
	r := httptest.NewRequest("GET", "http://panel.example.org/login", nil)
	r.RemoteAddr = "127.0.0.1:9000"
	r.Header.Set("X-Forwarded-Proto", "https")
	if got := requestScheme(r, trusted); got != "https" {
		t.Fatalf("proxy de confiance : %q", got)
	}
	if got := requestScheme(r, nil); got != "http" {
		t.Fatalf("proxy non listé : %q", got)
	}
	r.TLS = &tls.ConnectionState{}
	if got := requestScheme(r, nil); got != "https" {
		t.Fatalf("TLS direct : %q", got)
	}
}
