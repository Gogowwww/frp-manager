package main

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestProxiesOnlyChange(t *testing.T) {
	base := "serverAddr = \"frp.example.org\"\nserverPort = 7000\n\n[[proxies]]\nname = \"web\"\nlocalPort = 8080\n"
	// Un tunnel renommé : frpc relit les proxies, rien d'autre n'a bougé.
	// A renamed tunnel: frpc rereads proxies, nothing else changed.
	if !proxiesOnlyChange(base, strings.Replace(base, `"web"`, `"site"`, 1)) {
		t.Fatal("changement de proxy non reconnu")
	}
	// Une valeur de connexion changée : frpc ne la relit pas à chaud.
	// A connection value changed: frpc does not reread it live.
	if proxiesOnlyChange(base, strings.Replace(base, "7000", "7001", 1)) {
		t.Fatal("changement de serverPort pris pour un changement de proxy")
	}
	// Commentaires et ordre des lignes : rien à recharger, mais comparables.
	// Comments and line order: nothing to reload, but comparable.
	if !proxiesOnlyChange(base, "# note\n"+base) {
		t.Fatal("commentaire pris pour un changement")
	}
}

func TestFrpcReload(t *testing.T) {
	var calls int
	var gotAuth bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// frpc rejette POST (405) : le rechargement se demande en GET.
		// frpc rejects POST (405): the reload is asked for with GET.
		if r.Method != http.MethodGet || r.URL.Path != "/api/reload" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		user, pass, ok := r.BasicAuth()
		gotAuth = ok && user == "frp-manager" && pass == "secret"
		if !gotAuth {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		calls++
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	_, port, err := net.SplitHostPort(u.Host)
	if err != nil {
		t.Fatal(err)
	}
	content := fmt.Sprintf("serverAddr = \"frp.example.org\"\n\nwebServer.addr = \"127.0.0.1\"\nwebServer.port = %s\n"+
		"webServer.user = \"frp-manager\"\nwebServer.password = \"secret\"\n", port)

	ok, msg := frpcReload(content)
	if !ok || msg != "" || calls != 1 {
		t.Fatalf("rechargement : ok=%v msg=%q appels=%d", ok, msg, calls)
	}
	if !gotAuth {
		t.Fatal("identifiants de [webServer] non envoyés")
	}

	// Sans [webServer].port : pas d'API à joindre, rien n'est tenté.
	// Without [webServer].port: no API to reach, nothing is attempted.
	ok, msg = frpcReload("serverAddr = \"frp.example.org\"\n")
	if ok || msg != "" {
		t.Fatalf("sans webServer : ok=%v msg=%q", ok, msg)
	}

	// Mauvais mot de passe : l'échec est remonté, pas avalé.
	// Wrong password: the failure is reported, not swallowed.
	ok, msg = frpcReload(strings.Replace(content, `"secret"`, `"faux"`, 1))
	if ok || msg == "" {
		t.Fatalf("mot de passe refusé : ok=%v msg=%q", ok, msg)
	}
}
