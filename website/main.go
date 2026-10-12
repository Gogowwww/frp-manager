// Site de FRP Manager : page d'accueil, documentation et démo en ligne.
//
//	go run ./website build [--out dist-site] [--version 0.0.60] [--site-url https://frp-manager.gogow.fr]
//	                       [--bundle dossier] [--zip site.zip]
//	go run ./website demo  [--out dist-demo] [--version …] [--site-url …] [--bundle …] [--zip …]
//	go run ./website seo dist-site [--site-url …]
//	go run ./website serve [dossier]              (défaut : site/ à côté du programme, ou $DEMO_SITE)
//	go run ./website shots [--url http://127.0.0.1:18231] [--browser chemin]
//
// --bundle et --zip produisent l'application à héberger : site/, le serveur
// (programme Go, amd64 et arm64) et app.py qui le lance, pour un hébergement qui
// démarre « python3 app.py » (cloudpod Webstrator, egg Python Pterodactyl).
// C'est ce que publie .forgejo/workflows/demo.yml dans le dépôt frp-manager-demo.

package main

import (
	"archive/zip"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
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
	bundle := fs.String("bundle", "", "application à héberger : site/ + serveur + app.py (contenu remplacé)")
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
		if err := makeBundle(root, *out, *bundle); err != nil {
			return err
		}
		fmt.Printf("[%s] application -> %s (lancer : python3 app.py, ou ./frp-manager-site-linux-amd64 serve site)\n", cmd, *bundle)
	}
	if *zipOut != "" {
		tmp, err := os.MkdirTemp("", "frpm-site-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(tmp)
		if err := makeBundle(root, *out, tmp); err != nil {
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

// app.py de l'application hébergée : l'hébergement lance « python3 app.py »,
// qui passe la main au serveur Go de la bonne architecture.
const bundleAppPy = `#!/usr/bin/env python3
"""Démarre le serveur du site FRP Manager : le programme Go placé à côté de ce
fichier sert site/ (port $PORT ou $SERVER_PORT, 8080 par défaut). L'hébergement
lance « python3 app.py » : ce fichier ne fait que passer la main."""

import os
import platform

HERE = os.path.dirname(os.path.abspath(__file__))
ARCH = {"x86_64": "amd64", "amd64": "amd64", "aarch64": "arm64", "arm64": "arm64"}.get(platform.machine().lower(), "amd64")
EXE = os.path.join(HERE, "frp-manager-site-linux-" + ARCH)
os.chmod(EXE, 0o755)
os.execv(EXE, [EXE, "serve", os.path.join(HERE, "site")])
`

// makeBundle : site/ (copie de out), app.py et le serveur compilé pour Linux.
func makeBundle(root, out, bundle string) error {
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
	if err := os.WriteFile(filepath.Join(bundle, "app.py"), []byte(bundleAppPy), 0o755); err != nil {
		return err
	}
	for _, arch := range []string{"amd64", "arm64"} {
		build := exec.Command("go", "build", "-trimpath", "-ldflags", "-s -w",
			"-o", filepath.Join(bundle, "frp-manager-site-linux-"+arch), "./website")
		build.Dir = root
		build.Env = append(os.Environ(), "GOOS=linux", "GOARCH="+arch, "CGO_ENABLED=0")
		build.Stdout, build.Stderr = os.Stdout, os.Stderr
		if err := build.Run(); err != nil {
			return fmt.Errorf("compilation du serveur (%s) : %w", arch, err)
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
		mode := os.FileMode(0o644)
		if strings.HasPrefix(filepath.Base(p), "frp-manager-site-") || filepath.Base(p) == "app.py" {
			mode = 0o755
		}
		h.SetMode(mode)
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
