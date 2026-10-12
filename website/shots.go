// Captures d'écran du site, prises dans la démo avec un navigateur sans fenêtre.
//
// Le site doit être servi (go run ./website build, puis go run ./website serve
// dist-site). Produit website/img/<page>-<langue>.webp (panel en thème sombre,
// sans le bandeau de la démo). À relancer quand l'interface change ; les images
// sont versionnées. Demande Chrome, Chromium ou Edge.

package main

import (
	"flag"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/HugoSmits86/nativewebp"
)

var (
	shotPages = []string{"dashboard", "ports", "firewall", "logs", "config"}
	// Captures des fonctionnalités : sans la barre latérale du panel (240 px CSS),
	// qui se répète d'une image à l'autre et rendrait le contenu illisible en petit
	cropSidebar = map[string]bool{"ports": true, "firewall": true, "logs": true}
	browsers    = []string{
		`C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe`,
		`C:\Program Files\Google\Chrome\Application\chrome.exe`,
		"chromium", "chromium-browser", "google-chrome", "microsoft-edge",
	}
)

const shotScale = 1.5

func findBrowser(given string) (string, error) {
	cands := browsers
	if given != "" {
		cands = []string{given}
	}
	for _, c := range cands {
		if fileExists(c) {
			return c, nil
		}
		if p, err := exec.LookPath(c); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("aucun navigateur Chrome/Chromium/Edge trouvé (--browser)")
}

func runShots(args []string) error {
	fs := flag.NewFlagSet("shots", flag.ExitOnError)
	url := fs.String("url", "http://127.0.0.1:18231", "adresse du site servi")
	given := fs.String("browser", "", "navigateur")
	width := fs.Int("width", 1280, "largeur (px CSS)")
	height := fs.Int("height", 780, "hauteur (px CSS)")
	fs.Parse(args)
	root, err := findRoot()
	if err != nil {
		return err
	}
	browser, err := findBrowser(*given)
	if err != nil {
		return err
	}
	for _, l := range [][2]string{{"en", "en-US"}, {"fr", "fr-FR"}} {
		// Un profil neuf par langue : la démo mémorise la langue dans localStorage
		profile, err := os.MkdirTemp("", "frpm-shots-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(profile)
		for _, page := range shotPages {
			pngFile := filepath.Join(profile, page+".png")
			cmd := exec.Command(browser, "--headless=new", "--disable-gpu", "--hide-scrollbars", "--no-first-run",
				"--user-data-dir="+filepath.Join(profile, "profil"), "--lang="+l[1], "--accept-lang="+l[1],
				"--force-dark-mode", "--blink-settings=preferredColorScheme=0",
				fmt.Sprintf("--window-size=%d,%d", *width, *height), fmt.Sprintf("--force-device-scale-factor=%v", shotScale),
				"--virtual-time-budget=6000", "--screenshot="+pngFile, *url+"/demo/?shot#/"+page)
			if out, err := cmd.CombinedOutput(); err != nil {
				return fmt.Errorf("capture %s (%s) : %v\n%s", page, l[0], err, out)
			}
			if err := toWebP(pngFile, filepath.Join(root, "website", "img", page+"-"+l[0]+".webp"), cropSidebar[page]); err != nil {
				return err
			}
		}
	}
	return nil
}

// toWebP : PNG → WebP sans perte (nativewebp), barre latérale retirée au besoin.
func toWebP(src, dst string, crop bool) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	img, err := png.Decode(f)
	f.Close()
	if err != nil {
		return err
	}
	b := img.Bounds()
	if crop {
		b.Min.X += int(240 * shotScale)
	}
	rgba := image.NewNRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(rgba, rgba.Bounds(), img, b.Min, draw.Src)
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if err := nativewebp.Encode(out, rgba, nil); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	st, _ := os.Stat(dst)
	fmt.Printf("%s: %d Ko\n", filepath.Base(dst), st.Size()/1024)
	return nil
}
