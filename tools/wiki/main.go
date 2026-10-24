// Wiki GitHub du projet, tiré de la documentation du dépôt (README, docs/,
// CHANGELOG), en anglais et en français : la même documentation que sur le
// site, lisible depuis l'onglet Wiki de GitHub.
//
//	go run ./tools/wiki [-dir dépôt] [-out dossier] [-push]
//
// Produit un fichier par page (Home.md et Accueil.md pour les README, une page
// par document), la barre latérale et le pied de page. Les liens entre documents
// deviennent des liens entre pages du wiki ; les autres fichiers du dépôt
// pointent vers GitHub (images : raw.githubusercontent.com, branche main).
//
// -push : clone le wiki (https://github.com/<repo>.wiki.git, jeton $GH_TOKEN),
// remplace son contenu et pousse s'il a changé. Lancé par github.yml et
// promote.yml après la publication du main public, depuis le commit publié.

package main

import (
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

// page : un document du dépôt, en anglais et en français, et ses titres dans
// le wiki (le nom du fichier de la page en découle).
type page struct {
	en, fr           string
	enTitle, frTitle string
}

var pages = []page{
	{"README.md", "README.fr.md", "Home", "Accueil"},
	{"docs/installation.md", "docs/installation.fr.md", "Installation and configuration", "Installation et configuration"},
	{"docs/SECURITY.md", "docs/SECURITY.fr.md", "Security policy", "Politique de sécurité"},
	{"docs/CONTRIBUTING.md", "docs/CONTRIBUTING.fr.md", "Contributing", "Contribuer"},
	{"docs/CODE_OF_CONDUCT.md", "docs/CODE_OF_CONDUCT.fr.md", "Code of conduct", "Code de conduite"},
	{"CHANGELOG.md", "CHANGELOG.fr.md", "Changelog", "Journal des modifications"},
}

// pageName : nom de la page dans le wiki (nom du fichier, sans .md) ; GitHub
// affiche les tirets comme des espaces.
func pageName(title string) string {
	return strings.Join(strings.Fields(strings.ReplaceAll(title, ":", "")), "-")
}

var (
	linkRe    = regexp.MustCompile(`\]\(([^)\s]+)\)`)
	refDefRe  = regexp.MustCompile(`^(\s*\[[^\]]+\]:\s+)(\S+)(.*)$`)
	absRe     = regexp.MustCompile(`^([a-z][a-z0-9+.-]*:|#|/)`)
	imageExts = map[string]bool{".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true, ".svg": true}
)

// wiki : documents du dépôt → pages du wiki.
type wiki struct {
	repo   string            // Gogowwww/frp-manager
	byFile map[string]string // fichier du dépôt → page
}

func newWiki(repo string) *wiki {
	w := &wiki{repo: repo, byFile: map[string]string{}}
	for _, p := range pages {
		w.byFile[p.en] = pageName(p.enTitle)
		w.byFile[p.fr] = pageName(p.frTitle)
	}
	return w
}

// target : lien d'un document (src) réécrit pour le wiki.
func (w *wiki) target(src, link string) string {
	if absRe.MatchString(link) {
		return link
	}
	p, frag, _ := strings.Cut(link, "#")
	if frag != "" {
		frag = "#" + frag
	}
	file := path.Clean(path.Join(path.Dir(src), p))
	if strings.HasPrefix(file, "../") || file == ".." {
		return link
	}
	if name, ok := w.byFile[file]; ok {
		return url.PathEscape(name) + frag
	}
	if imageExts[strings.ToLower(path.Ext(file))] {
		return "https://raw.githubusercontent.com/" + w.repo + "/main/" + file
	}
	return "https://github.com/" + w.repo + "/blob/main/" + file + frag
}

// convert : document Markdown → page du wiki. Le titre de premier niveau des
// documents est retiré (le wiki affiche déjà le nom de la page), sauf sur les
// pages d'accueil ; les blocs de code sont laissés tels quels.
func (w *wiki) convert(src, text string, keepTitle bool) string {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	var out []string
	code, titled := false, keepTitle
	for _, l := range lines {
		if strings.HasPrefix(strings.TrimSpace(l), "```") {
			code = !code
			out = append(out, l)
			continue
		}
		if code {
			out = append(out, l)
			continue
		}
		if !titled && strings.HasPrefix(l, "# ") {
			titled = true
			continue
		}
		if m := refDefRe.FindStringSubmatch(l); m != nil {
			out = append(out, m[1]+w.target(src, m[2])+m[3])
			continue
		}
		out = append(out, linkRe.ReplaceAllStringFunc(l, func(s string) string {
			return "](" + w.target(src, s[2:len(s)-1]) + ")"
		}))
	}
	return strings.TrimLeft(strings.Join(out, "\n"), "\n")
}

// build : fichiers du wiki (nom → contenu), lus dans le dépôt dir.
func build(dir, repo string) (map[string]string, error) {
	w := newWiki(repo)
	files := map[string]string{}
	var side [2]strings.Builder
	side[0].WriteString("**English**\n\n")
	side[1].WriteString("**Français**\n\n")
	for i, p := range pages {
		for j, d := range [][2]string{{p.en, p.enTitle}, {p.fr, p.frTitle}} {
			raw, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(d[0])))
			if err != nil {
				return nil, err
			}
			name := pageName(d[1])
			files[name+".md"] = w.convert(d[0], string(raw), i == 0)
			fmt.Fprintf(&side[j], "- [%s](%s)\n", d[1], url.PathEscape(name))
		}
	}
	files["_Sidebar.md"] = side[0].String() + "\n" + side[1].String()
	files["_Footer.md"] = fmt.Sprintf("Generated from the repository's documentation ([docs/](https://github.com/%[1]s/tree/main/docs)): "+
		"changes go through pull requests. Also on [frp-manager.gogow.fr](https://frp-manager.gogow.fr/docs/).\n\n"+
		"Généré depuis la documentation du dépôt ([docs/](https://github.com/%[1]s/tree/main/docs)) : "+
		"les modifications passent par des pull requests. Aussi sur [frp-manager.gogow.fr](https://frp-manager.gogow.fr/fr/docs/).\n", repo)
	return files, nil
}

// write : remplace le contenu de out (sauf .git) par les fichiers du wiki.
func write(out string, files map[string]string) error {
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}
	entries, err := os.ReadDir(out)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.Name() != ".git" {
			if err := os.RemoveAll(filepath.Join(out, e.Name())); err != nil {
				return err
			}
		}
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(out, name), []byte(body), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// push : clone le wiki dans out, y écrit les pages et pousse s'il a changé.
func push(out, repo string, files map[string]string) error {
	token := strings.TrimSpace(os.Getenv("GH_TOKEN"))
	if token == "" {
		return errors.New("$GH_TOKEN manquant (jeton GitHub avec le scope repo)")
	}
	auth := "http.https://github.com/.extraheader=AUTHORIZATION: basic " +
		base64.StdEncoding.EncodeToString([]byte("x-access-token:"+token))
	git := func(args ...string) error {
		cmd := exec.Command("git", append([]string{"-c", auth}, args...)...)
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		return cmd.Run()
	}
	os.RemoveAll(out)
	if err := git("clone", "--quiet", "https://github.com/"+repo+".wiki.git", out); err != nil {
		return fmt.Errorf("clone du wiki impossible : il n'existe qu'une fois sa première page créée sur GitHub " +
			"(onglet Wiki → Create the first page)")
	}
	if err := write(out, files); err != nil {
		return err
	}
	if err := git("-C", out, "add", "-A"); err != nil {
		return err
	}
	if exec.Command("git", "-C", out, "diff", "--cached", "--quiet").Run() == nil {
		fmt.Println("Wiki inchangé")
		return nil
	}
	if err := git("-C", out, "-c", "user.name=FRP Manager", "-c", "user.email=frp-manager@users.noreply.github.com",
		"commit", "--quiet", "-m", "Wiki : documentation du dépôt"); err != nil {
		return err
	}
	if err := git("-C", out, "push", "--quiet", "origin", "HEAD"); err != nil {
		return err
	}
	fmt.Printf("Wiki publié : https://github.com/%s/wiki (%d pages)\n", repo, len(files)-2)
	return nil
}

func main() {
	dir := flag.String("dir", ".", "racine du dépôt")
	out := flag.String("out", "wiki", "dossier du wiki")
	repo := flag.String("repo", "Gogowwww/frp-manager", "dépôt GitHub")
	doPush := flag.Bool("push", false, "cloner le wiki GitHub dans -out, le mettre à jour et le pousser ($GH_TOKEN)")
	flag.Parse()
	files, err := build(*dir, *repo)
	if err == nil {
		if *doPush {
			err = push(*out, *repo, files)
		} else if err = write(*out, files); err == nil {
			fmt.Printf("%d pages -> %s\n", len(files)-2, *out)
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
