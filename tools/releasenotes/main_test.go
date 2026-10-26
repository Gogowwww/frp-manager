package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUnwrap(t *testing.T) {
	in := "- **Un** : texte coupé\n  à 80 colonnes.\n  - sous-élément\n    suite\n- Deux\n\nParagraphe\nsuite.\n\n```bash\necho a\necho b\n```\napres\n| A |\n| B |"
	want := "- **Un** : texte coupé à 80 colonnes.\n  - sous-élément suite\n- Deux\n\nParagraphe suite.\n\n```bash\necho a\necho b\n```\napres\n| A |\n| B |"
	if got := unwrap(in); got != want {
		t.Fatalf("obtenu :\n%s\nattendu :\n%s", got, want)
	}
}

func TestNotes(t *testing.T) {
	dir := t.TempDir()
	fr := "# Journal\n\n## [Non publié]\n\n### Ajouté\n\n- Pas encore.\n\n## [0.0.58] - 2026-10-12\n\n" +
		"Le panel passe en Go.\n\nPré-release.\n\n### Modifié\n\n- **Go** : réécrit\n  entièrement.\n\n" +
		"### Ajouté\n\n- **apt** : dépôt.\n\n## [0.0.57] - 2026-10-12\n\n### Modifié\n\n- Ancien.\n\n" +
		"[0.0.58]: https://example.org\n"
	en := "# Changelog\n\n## [Unreleased]\n\n## [0.0.58] - 2026-10-12\n\nThe panel moves to Go.\n\n" +
		"Pre-release, then final release.\n\n### Changed\n\n- **Go**: rewritten.\n\n### Added\n\n- **apt**: repository.\n\n" +
		"## [0.0.57] - 2026-10-12\n"
	// Les CHANGELOG vivent dans docs/, comme dans le dépôt.
	// The CHANGELOGs live in docs/, as in the repository.
	os.Mkdir(filepath.Join(dir, "docs"), 0o755)
	os.WriteFile(filepath.Join(dir, "docs", "CHANGELOG.fr.md"), []byte(fr), 0o644)
	os.WriteFile(filepath.Join(dir, "docs", "CHANGELOG.md"), []byte(en), 0o644)
	name, body, err := notes(dir, "o/r", "v0.0.58")
	if err != nil {
		t.Fatal(err)
	}
	if name != "v0.0.58 — Le panel passe en Go" {
		t.Fatal(name)
	}
	order := []string{"Le panel passe en Go.", "## Nouveautés", "- **apt** : dépôt.", "## Changements",
		"- **Go** : réécrit entièrement.", "## Mettre à jour", ":0.0.58", "compare/v0.0.57...v0.0.58",
		"## 🇬🇧 English", "The panel moves to Go.", "### New", "### Changes", "### Updating"}
	pos := 0
	for _, w := range order {
		i := strings.Index(body[pos:], w)
		if i < 0 {
			t.Fatalf("%q absent ou mal placé :\n%s", w, body)
		}
		pos += i
	}
	for _, absent := range []string{"Pré-release", "Pre-release", "Pas encore", "Ancien", "example.org"} {
		if strings.Contains(body, absent) {
			t.Errorf("%q ne devrait pas figurer", absent)
		}
	}
	if _, _, err := notes(dir, "o/r", "v9.9.9"); err == nil {
		t.Error("version absente acceptée")
	}
	if _, _, err := notes(dir, "o/r", "latest"); err == nil {
		t.Error("tag invalide accepté")
	}
}
