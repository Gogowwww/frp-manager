package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTarget(t *testing.T) {
	w := newWiki("o/r")
	for src, cases := range map[string]map[string]string{
		"README.md": {
			"docs/installation.md#install-with-apt": "Installation-and-configuration#install-with-apt",
			"README.fr.md":                          "Accueil",
			"docs/docker.fr.md":                     "Docker-privil%C3%A8ges-et-risques",
			"docs/img/dashboard-en.png":             "https://raw.githubusercontent.com/o/r/main/docs/img/dashboard-en.png",
			"LICENSE":                               "https://github.com/o/r/blob/main/LICENSE",
			"https://example.org/a":                 "https://example.org/a",
			"#features":                             "#features",
		},
		"docs/installation.md": {
			"../CHANGELOG.md":   "Changelog",
			"SECURITY.md":       "Security-policy",
			"../LICENSE":        "https://github.com/o/r/blob/main/LICENSE",
			"../../ailleurs.md": "../../ailleurs.md",
		},
	} {
		for in, want := range cases {
			if got := w.target(src, in); got != want {
				t.Errorf("%s : %q -> %q, attendu %q", src, in, got, want)
			}
		}
	}
}

func TestConvert(t *testing.T) {
	w := newWiki("o/r")
	in := "# Installation\n\n**English** · [Français](installation.fr.md)\n\n" +
		"See [Docker](docker.md#risks) and [![badge](https://img/x.svg)](../LICENSE).\n\n" +
		"```bash\necho '[a](b.md)'\n```\n\n[ref]: SECURITY.md\n"
	got := w.convert("docs/installation.md", in, false)
	want := "**English** · [Français](Installation-et-configuration)\n\n" +
		"See [Docker](Docker-privileges-and-risks#risks) and [![badge](https://img/x.svg)](https://github.com/o/r/blob/main/LICENSE).\n\n" +
		"```bash\necho '[a](b.md)'\n```\n\n[ref]: Security-policy\n"
	if got != want {
		t.Fatalf("obtenu :\n%s\nattendu :\n%s", got, want)
	}
	if !strings.HasPrefix(w.convert("README.md", "# FRP Manager\n", true), "# FRP Manager") {
		t.Error("titre de la page d'accueil retiré")
	}
}

// Toutes les pages du dépôt réel, sans lien relatif restant vers un fichier.
func TestBuildRepo(t *testing.T) {
	files, err := build(filepath.Join("..", ".."), "Gogowwww/frp-manager")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2*len(pages)+2 {
		t.Fatalf("%d fichiers", len(files))
	}
	for name, body := range files {
		for _, bad := range []string{"](docs/", "](../", ".md)", ".md#"} {
			if strings.Contains(body, bad) {
				t.Errorf("%s : lien non converti (%q)", name, bad)
			}
		}
	}
	out := t.TempDir()
	os.MkdirAll(filepath.Join(out, ".git"), 0o755)
	os.WriteFile(filepath.Join(out, "Ancienne-page.md"), []byte("x"), 0o644)
	if err := write(out, files); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(out, "Ancienne-page.md")); err == nil {
		t.Error("ancienne page gardée")
	}
	if _, err := os.Stat(filepath.Join(out, ".git")); err != nil {
		t.Error(".git supprimé")
	}
}
