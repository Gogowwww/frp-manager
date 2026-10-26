// Notes d'une release GitHub, tirées des deux CHANGELOG (français puis anglais),
// dans la forme des releases précédentes : titre, résumé, sections, « Mettre à
// jour », lien de comparaison, puis la version anglaise.
//
// Les CHANGELOG sont lus dans docs/ (docs/CHANGELOG.fr.md, docs/CHANGELOG.md).
// The CHANGELOGs are read from docs/ (docs/CHANGELOG.fr.md, docs/CHANGELOG.md).
//
//	go run ./tools/releasenotes [-dir dépôt] [-repo Gogowwww/frp-manager] [-json] v0.0.58
//
// Le titre et l'introduction viennent du premier paragraphe de la section de la
// version (« Le panel passe en Go : … ») ; les paragraphes d'état
// (« Pré-release. ») sont laissés de côté, GitHub affichant déjà ce statut.
// -json : {"name": …, "body": …}, pour l'API GitHub (release.yml, promote.yml).

package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	versionHead = regexp.MustCompile(`^## \[(\d+\.\d+\.\d+)\]`)
	tagRe       = regexp.MustCompile(`^v?(\d+\.\d+\.\d+)$`)
)

// changelogDir : dossier des deux CHANGELOG, relatif à la racine du dépôt.
// changelogDir: folder of both CHANGELOGs, relative to the repository root.
const changelogDir = "docs"

// section : une version du CHANGELOG.
type section struct {
	summary string
	parts   []part // sous-sections (### …), dans l'ordre du fichier
	prev    string // version précédente (pour le lien de comparaison)
}

type part struct {
	title, text string
}

// parseSection : section de la version dans un CHANGELOG.
func parseSection(changelog, version string) (*section, error) {
	lines := strings.Split(strings.ReplaceAll(changelog, "\r\n", "\n"), "\n")
	start := -1
	for i, l := range lines {
		if m := versionHead.FindStringSubmatch(l); m != nil && m[1] == version {
			start = i
			break
		}
	}
	if start < 0 {
		return nil, fmt.Errorf("version %s absente du CHANGELOG", version)
	}
	sec := &section{}
	end := len(lines)
	for i := start + 1; i < len(lines); i++ {
		if strings.HasPrefix(lines[i], "## ") || strings.HasPrefix(lines[i], "[") && strings.Contains(lines[i], "]: http") {
			end = i
			if m := versionHead.FindStringSubmatch(lines[i]); m != nil {
				sec.prev = m[1]
			}
			break
		}
	}
	var intro []string
	var cur *part
	for _, l := range lines[start+1 : end] {
		if t, ok := strings.CutPrefix(l, "### "); ok {
			sec.parts = append(sec.parts, part{title: strings.TrimSpace(t)})
			cur = &sec.parts[len(sec.parts)-1]
			continue
		}
		if cur == nil {
			intro = append(intro, l)
		} else {
			cur.text += l + "\n"
		}
	}
	for i := range sec.parts {
		sec.parts[i].text = unwrap(strings.Trim(sec.parts[i].text, "\n"))
	}
	for _, para := range strings.Split(strings.Join(intro, "\n"), "\n\n") {
		para = strings.Join(strings.Fields(para), " ")
		if para == "" || strings.HasPrefix(para, "Pré-release") || strings.HasPrefix(para, "Pre-release") {
			continue
		}
		sec.summary = para
		break
	}
	return sec, nil
}

// blockStart : ligne qui commence un bloc markdown (élément de liste, tableau,
// citation, titre, bloc de code) ; les autres prolongent la ligne précédente.
var blockStart = regexp.MustCompile("^\\s*([-*+] |\\d+[.)] |\\||>|#|```)")

// unwrap : lignes recollées (le CHANGELOG est coupé à 80 colonnes ; GitHub
// affiche chaque retour à la ligne d'une note de release). Blocs de code intacts.
func unwrap(text string) string {
	var out []string
	code := false
	for _, l := range strings.Split(text, "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), "```") {
			code = !code
			out = append(out, l)
			continue
		}
		n := len(out)
		if code || l == "" || n == 0 || out[n-1] == "" || blockStart.MatchString(l) ||
			strings.HasPrefix(strings.TrimSpace(out[n-1]), "```") {
			out = append(out, l)
			continue
		}
		out[n-1] += " " + strings.TrimSpace(l)
	}
	return strings.Join(out, "\n")
}

// Titres des sous-sections dans les notes, dans cet ordre ; un titre inconnu
// est gardé tel quel, après.
var (
	partsFR = [][2]string{{"Ajouté", "Nouveautés"}, {"Modifié", "Changements"}, {"Corrigé", "Corrections"},
		{"Retiré", "Retiré"}, {"Obsolète", "Obsolète"}, {"Sécurité", "Sécurité"}}
	partsEN = [][2]string{{"Added", "New"}, {"Changed", "Changes"}, {"Fixed", "Fixes"},
		{"Removed", "Removed"}, {"Deprecated", "Deprecated"}, {"Security", "Security"}}
)

func writeParts(b *strings.Builder, sec *section, names [][2]string, level string) {
	done := map[string]bool{}
	for _, n := range names {
		for _, p := range sec.parts {
			if p.title == n[0] && p.text != "" {
				fmt.Fprintf(b, "%s %s\n\n%s\n\n", level, n[1], p.text)
				done[p.title] = true
			}
		}
	}
	for _, p := range sec.parts {
		if !done[p.title] && p.text != "" {
			fmt.Fprintf(b, "%s %s\n\n%s\n\n", level, p.title, p.text)
		}
	}
}

func notes(dir, repo, tag string) (name, body string, err error) {
	m := tagRe.FindStringSubmatch(tag)
	if m == nil {
		return "", "", fmt.Errorf("tag invalide : %s (attendu vX.Y.Z)", tag)
	}
	version := m[1]
	read := func(f string) (*section, error) {
		raw, err := os.ReadFile(filepath.Join(dir, changelogDir, f))
		if err != nil {
			return nil, err
		}
		s, err := parseSection(string(raw), version)
		if err != nil {
			return nil, fmt.Errorf("%s : %w", f, err)
		}
		return s, nil
	}
	fr, err := read("CHANGELOG.fr.md")
	if err != nil {
		return "", "", err
	}
	en, err := read("CHANGELOG.md")
	if err != nil {
		return "", "", err
	}
	if len(fr.parts) == 0 && len(en.parts) == 0 {
		return "", "", errors.New("section vide dans les deux CHANGELOG")
	}

	name = "v" + version
	if fr.summary != "" {
		name += " — " + strings.TrimSuffix(fr.summary, ".")
	}
	var b strings.Builder
	if fr.summary != "" {
		b.WriteString(fr.summary + "\n\n")
	}
	writeParts(&b, fr, partsFR, "##")
	fmt.Fprintf(&b, "## Mettre à jour\n\n"+
		"- **Docker** : `docker compose pull && docker compose up -d` (image `:latest`, ou `:%[1]s` pour la figer ; "+
		"une pré-release n'existe qu'en `:%[1]s`).\n"+
		"- **Paquet apt** : `sudo apt update && sudo apt upgrade` (distribution `stable` ; `testing` reçoit les pré-releases).\n"+
		"- **Installation par script** : bouton « Mettre à jour » du panel, ou `sudo bash install.sh` depuis la nouvelle archive.\n\n",
		version)
	if fr.prev != "" {
		fmt.Fprintf(&b, "**Full Changelog**: https://github.com/%s/compare/v%s...v%s\n\n", repo, fr.prev, version)
	}
	b.WriteString("---\n\n## 🇬🇧 English\n\n")
	if en.summary != "" {
		b.WriteString(en.summary + "\n\n")
	}
	writeParts(&b, en, partsEN, "###")
	fmt.Fprintf(&b, "### Updating\n\n"+
		"- **Docker**: `docker compose pull && docker compose up -d` (`:latest` image, or `:%[1]s` to pin it; "+
		"a pre-release only exists as `:%[1]s`).\n"+
		"- **apt package**: `sudo apt update && sudo apt upgrade` (`stable` distribution; `testing` gets pre-releases).\n"+
		"- **Install script**: the panel's \"Update\" button, or `sudo bash install.sh` from the new archive.\n",
		version)
	return name, b.String(), nil
}

func main() {
	dir := flag.String("dir", ".", "racine du dépôt (docs/CHANGELOG.md, docs/CHANGELOG.fr.md)")
	repo := flag.String("repo", "Gogowwww/frp-manager", "dépôt GitHub (lien de comparaison)")
	asJSON := flag.Bool("json", false, `sortie {"name": …, "body": …}`)
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage : go run ./tools/releasenotes [-dir dépôt] [-repo o/r] [-json] vX.Y.Z")
	}
	flag.Parse()
	if flag.NArg() != 1 {
		flag.Usage()
		os.Exit(2)
	}
	name, body, err := notes(*dir, *repo, flag.Arg(0))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetEscapeHTML(false)
		enc.Encode(map[string]string{"name": name, "body": body})
		return
	}
	fmt.Printf("%s\n\n%s", name, body)
}
