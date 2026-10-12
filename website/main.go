// Site de FRP Manager : page d'accueil, documentation et démo en ligne.
//
//	go run ./website build [--out dist-site] [--version 0.0.60] [--site-url https://frp-manager.gogow.fr]
//	                       [--bundle dossier] [--zip site.zip]
//	go run ./website demo  [--out dist-demo] [--version …] [--site-url …] [--bundle …] [--zip …]
//	go run ./website seo dist-site [--site-url …]
//	go run ./website serve [dossier]              (défaut : site/ à côté du programme, ou $DEMO_SITE)
//	go run ./website shots [--url http://127.0.0.1:18231] [--browser chemin]
//
// --bundle et --zip produisent l'application à héberger : site/ et un module Go
// autonome qui le sert, lancé par « go run . » (cloudpod Webstrator, egg Go
// Pterodactyl) ou compilé par « go build ». C'est ce que publie
// .forgejo/workflows/demo.yml dans le dépôt frp-manager-demo.

package main

import (
	"archive/zip"
	_ "embed"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	cmd, args := os.Args[1], os.Args[2:]
	var err error
	switch cmd {
	case "build", "demo":
		err = runBuild(cmd, args)
	case "seo":
		err = runSEO(args)
	case "serve":
		dir := os.Getenv("DEMO_SITE")
		if len(args) > 0 {
			dir = args[0]
		}
		if dir == "" {
			exe, _ := os.Executable()
			dir = filepath.Join(filepath.Dir(exe), "site")
		}
		err = serveSite(dir)
	case "shots":
		err = runShots(args)
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage : go run ./website build|demo|seo|serve|shots [options]")
	os.Exit(2)
}

// findRoot : racine du dépôt (dossier qui contient go.mod et website/).
func findRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if fileExists(filepath.Join(dir, "go.mod")) && fileExists(filepath.Join(dir, "website", "content.json")) {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("racine du dépôt introuvable (lancer depuis le dépôt frp-manager)")
		}
		dir = parent
	}
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }

func runBuild(cmd string, args []string) error {
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	out := fs.String("out", "", "dossier produit")
	version := fs.String("version", "", "version affichée (défaut : dernier tag)")
	siteURL := fs.String("site-url", "https://frp-manager.gogow.fr", "adresse publique")
	bundle := fs.String("bundle", "", "application à héberger : site/ + module Go qui le sert (contenu remplacé)")
	zipOut := fs.String("zip", "", "même application, en archive")
	fs.Parse(args)
	root, err := findRoot()
	if err != nil {
		return err
	}
	site := strings.TrimRight(*siteURL, "/")
	if *out == "" {
		*out = filepath.Join(root, "dist-"+map[string]string{"build": "site", "demo": "demo"}[cmd])
	}
	ver := *version
	if ver == "" {
		ver = gitVersion(root, map[string]string{"build": "dev", "demo": "demo"}[cmd])
	}
	ver = strings.TrimPrefix(ver, "v")
	if cmd == "build" {
		err = buildSite(root, *out, site, ver)
	} else {
		err = buildDemo(root, *out, ver, site, false)
	}
	if err != nil {
		return err
	}
	if *bundle != "" {
		if err := makeBundle(*out, *bundle); err != nil {
			return err
		}
		fmt.Printf("[%s] application -> %s (lancer : go run main.go)\n", cmd, *bundle)
	}
	if *zipOut != "" {
		tmp, err := os.MkdirTemp("", "frpm-site-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(tmp)
		if err := makeBundle(*out, tmp); err != nil {
			return err
		}
		if err := zipDir(tmp, *zipOut); err != nil {
			return err
		}
		fmt.Printf("[%s] archive -> %s\n", cmd, *zipOut)
	}
	return nil
}

func runSEO(args []string) error {
	fs := flag.NewFlagSet("seo", flag.ExitOnError)
	siteURL := fs.String("site-url", "https://frp-manager.gogow.fr", "adresse publique")
	var dir string
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		dir, args = args[0], args[1:]
	}
	fs.Parse(args)
	if dir == "" {
		dir = fs.Arg(0)
	}
	if dir == "" {
		return errors.New("usage : go run ./website seo <dossier du site>")
	}
	n, err := seoCheck(dir, *siteURL)
	if err != nil {
		return err
	}
	if n > 0 {
		os.Exit(1)
	}
	return nil
}

// Application hébergée : un module Go autonome (bibliothèque standard
// uniquement) qui sert site/, en un seul fichier main.go : l'hébergement peut
// lancer « go run main.go » comme « go run . » ou « go build ». Le serveur est
// serve.go, le même fichier que celui de ce programme.

//go:embed serve.go
var serveSource string

const bundleGoMod = "module frp-manager-site\n\ngo 1.22\n"

const bundleHead = `// Site de FRP Manager (page d'accueil, documentation, démo en ligne), servi
// depuis le dossier site/ : port $PORT, ou $SERVER_PORT, 8080 par défaut.
//
//	go run main.go      (ou go run .)
//	go build -o frp-manager-site . && ./frp-manager-site [dossier]
//
// Construit automatiquement depuis le dépôt frp-manager (go run ./website build --bundle).

`

const bundleMainFunc = `
func main() {
	dir := os.Getenv("DEMO_SITE")
	if len(os.Args) > 1 {
		dir = os.Args[1]
	}
	if dir == "" {
		dir = findSite()
	}
	if err := serveSite(dir); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// findSite : site/ à côté du programme, sinon dans le dossier courant (« go run »
// compile le programme dans un dossier temporaire).
func findSite() string {
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Join(filepath.Dir(exe), "site")
		if _, err := os.Stat(filepath.Join(dir, "index.html")); err == nil {
			return dir
		}
	}
	return "site"
}
`

// bundleMain : main.go de l'application, serve.go compris (sans son commentaire d'en-tête).
func bundleMain() string {
	src := strings.ReplaceAll(serveSource, "\r\n", "\n")
	return bundleHead + src[strings.Index(src, "package main"):] + bundleMainFunc
}

// makeBundle : site/ (copie de out) et le module Go qui le sert.
func makeBundle(out, bundle string) error {
	if err := emptyDir(bundle, ".git"); err != nil {
		return err
	}
	err := filepath.WalkDir(out, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(out, p)
		return copyFile(p, filepath.Join(bundle, "site", rel))
	})
	if err != nil {
		return err
	}
	for name, body := range map[string]string{"go.mod": bundleGoMod, "main.go": bundleMain()} {
		if err := os.WriteFile(filepath.Join(bundle, name), []byte(body), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func zipDir(dir, target string) error {
	f, err := os.Create(target)
	if err != nil {
		return err
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	err = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		h := &zip.FileHeader{Name: filepath.ToSlash(rel), Method: zip.Deflate}
		h.SetMode(0o644)
		w, err := zw.CreateHeader(h)
		if err != nil {
			return err
		}
		src, err := os.Open(p)
		if err != nil {
			return err
		}
		defer src.Close()
		_, err = io.Copy(w, src)
		return err
	})
	if err != nil {
		return err
	}
	return zw.Close()
}
