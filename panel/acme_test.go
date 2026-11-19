package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestValidSiteDomain(t *testing.T) {
	good := []string{"example.com", "a.b.example.com", "*.example.com", "site-1.example.co"}
	bad := []string{"", "localhost", "example", "a..b", "-bad.example.com", "bad-.example.com",
		"exa mple.com", "example.com.", "*.", "é.example.com"}
	for _, d := range good {
		if !validSiteDomain(d) {
			t.Errorf("validSiteDomain(%q) = false", d)
		}
	}
	for _, d := range bad {
		if validSiteDomain(d) {
			t.Errorf("validSiteDomain(%q) = true", d)
		}
	}
}

func TestAcmeDomains(t *testing.T) {
	got, err := acmeDomains(map[string]any{"domains": " A.Example.com, a.example.com , B.example.com"})
	if err != nil {
		t.Fatalf("domaines refusés : %v", err)
	}
	if strings.Join(got, ",") != "a.example.com,b.example.com" {
		t.Fatalf("domaines = %v", got)
	}
	if _, err := acmeDomains(map[string]any{"domains": []any{"a.example.com", "localhost"}}); err == nil {
		t.Fatal("nom sans point accepté")
	}
	if _, err := acmeDomains(map[string]any{}); err == nil {
		t.Fatal("liste vide acceptée")
	}
}

func TestTxtRecordName(t *testing.T) {
	cases := map[string]string{
		"example.com":   "_acme-challenge.example.com",
		"*.example.com": "_acme-challenge.example.com",
		"a.example.com": "_acme-challenge.a.example.com",
	}
	for in, want := range cases {
		if got := txtRecordName(in); got != want {
			t.Errorf("txtRecordName(%q) = %q, attendu %q", in, got, want)
		}
	}
}

func TestZoneCandidates(t *testing.T) {
	got := zoneCandidates("_acme-challenge.www.example.com")
	want := []string{"example.com", "www.example.com", "_acme-challenge.www.example.com"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("zoneCandidates = %v", got)
	}
}

func TestCloudflareClient(t *testing.T) {
	var created map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/zones":
			if r.URL.Query().Get("name") == "example.com" {
				fmt.Fprint(w, `{"success":true,"result":[{"id":"z1"}]}`)
				return
			}
			fmt.Fprint(w, `{"success":true,"result":[]}`)
		case r.Method == http.MethodPost && r.URL.Path == "/zones/z1/dns_records":
			_ = json.NewDecoder(r.Body).Decode(&created)
			if r.Header.Get("Authorization") != "Bearer tok" {
				w.WriteHeader(http.StatusForbidden)
				fmt.Fprint(w, `{"success":false,"errors":[{"message":"jeton refusé"}]}`)
				return
			}
			fmt.Fprint(w, `{"success":true,"result":{"id":"r1"}}`)
		case r.Method == http.MethodDelete && r.URL.Path == "/zones/z1/dns_records/r1":
			fmt.Fprint(w, `{"success":true,"result":{"id":"r1"}}`)
		default:
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"success":false,"errors":[{"message":"inattendu"}]}`)
		}
	}))
	defer srv.Close()

	cf := cfClient{token: "tok", base: srv.URL}
	zone, err := cf.zoneFor(context.Background(), "_acme-challenge.www.example.com")
	if err != nil || zone != "z1" {
		t.Fatalf("zone = %q, %v", zone, err)
	}
	rec, err := cf.createTXT(context.Background(), zone, "_acme-challenge.www.example.com", "valeur")
	if err != nil || rec != "r1" {
		t.Fatalf("enregistrement = %q, %v", rec, err)
	}
	if created["type"] != "TXT" || created["name"] != "_acme-challenge.www.example.com" || created["content"] != "valeur" {
		t.Fatalf("enregistrement envoyé = %v", created)
	}
	if err := cf.deleteTXT(context.Background(), zone, rec); err != nil {
		t.Fatalf("suppression : %v", err)
	}
	if _, err := cf.zoneFor(context.Background(), "nowhere.test"); err == nil {
		t.Fatal("zone inconnue acceptée")
	}
	bad := cfClient{token: "faux", base: srv.URL}
	if _, err := bad.createTXT(context.Background(), "z1", "n.example.com", "v"); err == nil {
		t.Fatal("jeton refusé par l'API accepté")
	}
}

func TestTxtPresent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") != "application/dns-json" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		fmt.Fprint(w, `{"Status":0,"Answer":[{"data":"\"abc123\""}]}`)
	}))
	defer srv.Close()

	saved := dohURL
	dohURL = srv.URL
	t.Cleanup(func() { dohURL = saved })

	if !txtPresent(context.Background(), "_acme-challenge.example.com", "abc123") {
		t.Fatal("valeur présente non trouvée")
	}
	if txtPresent(context.Background(), "_acme-challenge.example.com", "autre") {
		t.Fatal("valeur absente trouvée")
	}
}
