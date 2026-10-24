package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSummarize(t *testing.T) {
	now := time.Date(2026, 10, 25, 6, 0, 0, 0, time.UTC)
	releases := []apiRelease{
		{Tag: "v0.4.0", PublishedAt: "2026-10-24T20:35:35Z", Assets: []apiAsset{
			{Name: "frp-manager.zip", Downloads: 10}, {Name: "frp-manager.zip.sha256", Downloads: 4},
			{Name: "frp-manager_0.4.0_amd64.deb", Downloads: 3}}},
		{Tag: "v0.3.0", Prerelease: true, PublishedAt: "2026-10-24T11:43:00Z", Assets: []apiAsset{{Name: "frp-manager.zip", Downloads: 5}}},
		{Tag: "v0.9.9", Draft: true, PublishedAt: "2026-10-25T00:00:00Z", Assets: []apiAsset{{Name: "x.zip", Downloads: 999}}},
	}
	s := summarize("o/r", apiRepo{Stars: 7, Forks: 2}, releases, now)
	if s.TotalDownloads != 22 || len(s.Releases) != 2 {
		t.Fatalf("total %d, %d releases (brouillon non ignoré ?)", s.TotalDownloads, len(s.Releases))
	}
	if s.Releases[0].Tag != "v0.4.0" || s.Releases[0].ByKind["zip"] != 10 || s.Releases[0].ByKind["deb"] != 3 || s.Releases[0].ByKind["sha256"] != 4 {
		t.Fatalf("release la plus récente : %+v", s.Releases[0])
	}
	if s.GeneratedAt != "2026-10-25T06:00:00Z" || s.Stars != 7 {
		t.Fatalf("en-tête : %+v", s)
	}
}

func TestAddHistory(t *testing.T) {
	s := Stats{GeneratedAt: "2026-10-25T06:00:00Z", Stars: 8, TotalDownloads: 30}
	h := addHistory([]Snapshot{{Date: "2026-10-24", Downloads: 20}}, s)
	if len(h) != 2 || h[1].Date != "2026-10-25" || h[1].Downloads != 30 {
		t.Fatalf("ajout : %+v", h)
	}
	s.TotalDownloads = 31
	h = addHistory(h, s)
	if len(h) != 2 || h[1].Downloads != 31 {
		t.Fatalf("même jour : la ligne doit être remplacée, pas ajoutée : %+v", h)
	}
	var long []Snapshot
	for i := 0; i < maxHistory+50; i++ {
		long = append(long, Snapshot{Date: "2025-01-01"})
	}
	if got := addHistory(long, s); len(got) != maxHistory {
		t.Fatalf("historique non borné : %d", len(got))
	}
}

func TestRun(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/o/r":
			json.NewEncoder(w).Encode(apiRepo{Stars: 3})
		case "/repos/o/r/releases":
			json.NewEncoder(w).Encode([]apiRelease{{Tag: "v1.0.0", PublishedAt: "2026-01-01T00:00:00Z",
				Assets: []apiAsset{{Name: "a.zip", Downloads: 2}}}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	dir := t.TempDir()
	out, hist := filepath.Join(dir, "stats.json"), filepath.Join(dir, "history.json")
	now := time.Date(2026, 10, 25, 6, 0, 0, 0, time.UTC)
	for i := 0; i < 2; i++ { // deux passages le même jour : une seule ligne d'historique
		if err := run("o/r", out, hist, srv.Client(), srv.URL, now); err != nil {
			t.Fatal(err)
		}
	}
	var h []Snapshot
	data, _ := os.ReadFile(hist)
	if err := json.Unmarshal(data, &h); err != nil || len(h) != 1 || h[0].Downloads != 2 || h[0].Stars != 3 {
		t.Fatalf("historique : %s (%v)", data, err)
	}
	if err := run("sans-barre", out, "", srv.Client(), srv.URL, now); err == nil {
		t.Fatal("un dépôt sans « / » doit être refusé")
	}
	if err := run("o/inconnu", out, "", srv.Client(), srv.URL, now); err == nil {
		t.Fatal("une erreur HTTP doit être remontée")
	}
}
