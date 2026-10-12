// Démo statique de FRP Manager : la vraie interface (panel/templates/), servie
// sans le panel. demo.js remplace l'API, les WebSocket et les journaux par des
// données fictives, gardées dans l'onglet (sessionStorage).

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Adresses absolues du panel → pages statiques de la démo. Chaque motif doit être
// trouvé : si le code change, la construction échoue au lieu de produire une démo
// qui renvoie vers une page inexistante.
var demoPatches = map[string][][2]string{
	"js/api.js": {{"location.href = '/login';", "location.href = 'login.html';"}},
	"js/main.js": {
		{"location.href = '/login';", "location.href = 'login.html';"},
		// Titre de chaque page : « Pare-feu — FRP Manager · démo » (moteurs de recherche, onglets)
		{"document.title = `${page.title()} — FRP Manager`;", "document.title = `${page.title()} — FRP Manager · Live demo`;"},
	},
}

var loginPatches = [][2]string{{"location.href = '/';", "location.href = './';"}} // /demo/ et pas /demo/index.html

const (
	seoTitle = "FRP Manager live demo: web panel for frp (frps and frpc)"
	seoDesc  = "Try FRP Manager in your browser: a free, open-source web GUI for frp. Tunnels, ports, " +
		"nftables firewall, live logs and updates, with sample data. Nothing to install."
)

// seoHead : balises <head> de la page d'accueil de la démo (partages, moteurs de recherche).
func seoHead(site, version string) string {
	ld := OM{
		{"@context", "https://schema.org"}, {"@type", "SoftwareApplication"}, {"name", "FRP Manager"},
		{"description", seoDesc}, {"applicationCategory", "DeveloperApplication"},
		{"applicationSubCategory", "Network tunnel / reverse proxy management"},
		{"operatingSystem", "Linux"}, {"softwareVersion", version}, {"url", site + "/"},
		{"image", site + "/og.png"}, {"downloadUrl", repoURL + "/releases/latest"}, {"codeRepository", repoURL},
		{"license", "https://www.apache.org/licenses/LICENSE-2.0"}, {"isAccessibleForFree", true},
		{"offers", OM{{"@type", "Offer"}, {"price", "0"}, {"priceCurrency", "EUR"}}},
		{"keywords", "frp, frps, frpc, frp panel, frp dashboard, frp web ui, reverse proxy, tunnel, " +
			"port forwarding, self-hosted, homelab, nftables firewall"},
		{"sameAs", []any{repoURL}},
	}
	e := strings.NewReplacer("&", "&amp;", `"`, "&quot;", "<", "&lt;").Replace
	return strings.Join([]string{
		`<meta name="description" content="` + e(seoDesc) + `">`,
		`<meta name="robots" content="index, follow, max-image-preview:large">`,
		`<link rel="canonical" href="` + site + `/">`,
		`<meta name="theme-color" content="#0e1319">`,
		`<meta property="og:type" content="website">`,
		`<meta property="og:site_name" content="FRP Manager">`,
		`<meta property="og:title" content="` + e(seoTitle) + `">`,
		`<meta property="og:description" content="` + e(seoDesc) + `">`,
		`<meta property="og:url" content="` + site + `/">`,
		`<meta property="og:image" content="` + site + `/og.png">`,
		`<meta property="og:image:width" content="1280">`,
		`<meta property="og:image:height" content="640">`,
		`<meta property="og:image:alt" content="FRP Manager, the self-hosted web panel for frp: firewall page of the live demo">`,
		`<meta property="og:locale" content="en_US">`,
		`<meta property="og:locale:alternate" content="fr_FR">`,
		`<meta name="twitter:card" content="summary_large_image">`,
		`<meta name="twitter:title" content="` + e(seoTitle) + `">`,
		`<meta name="twitter:description" content="` + e(seoDesc) + `">`,
		`<meta name="twitter:image" content="` + site + `/og.png">`,
		`<script type="application/ld+json">` + jsonText(ld, false) + `</script>`,
	}, "\n")
}

// Sans JavaScript (robots qui n'exécutent pas les scripts, aperçus de liens) :
// une vraie présentation au lieu de « nécessite JavaScript ».
const noscript = `<noscript><main style="max-width:760px;margin:0 auto;padding:40px 20px;font-family:system-ui,sans-serif;line-height:1.6">
<h1>FRP Manager — self-hosted web panel for frp</h1>
<p>FRP Manager is a free, open-source web interface (GUI and dashboard) for <a href="https://github.com/fatedier/frp">frp</a>, the fast reverse proxy.
It manages <strong>frps</strong> (the server) and <strong>frpc</strong> (the client) from a browser: start and stop services, open ports and tunnels
(TCP, UDP, HTTP, HTTPS, STCP, XTCP), edit the configuration, follow live logs, filter who can connect with an <strong>nftables firewall</strong>
and update frp in one click. Linux with systemd, or Docker.</p>
<p>This live demo runs the real interface with sample data. <strong>Enable JavaScript</strong> to try it.</p>
<p><a href="` + repoURL + `">Source code, documentation and installation on GitHub</a> ·
<a href="` + repoURL + `/releases/latest">Download the latest release</a></p>
</main></noscript>`

func patchText(text string, pairs [][2]string, name string) (string, error) {
	for _, p := range pairs {
		if !strings.Contains(text, p[0]) {
			return "", fmt.Errorf("[demo] motif introuvable dans %s : %q", name, p[0])
		}
		text = strings.ReplaceAll(text, p[0], p[1])
	}
	return text, nil
}

var (
	preloadLoopRe = regexp.MustCompile(`(?s)\{% for m in preload_modules %\}.*?\{% endfor %\}`)
	assetTagRe    = regexp.MustCompile(`\{\{ asset\('([^']+)'\) \}\}`)
	oidcBlockRe   = regexp.MustCompile(`(?s)\{% if oidc_enabled %\}.*?\{% endif %\}`)
	jinjaLeftRe   = regexp.MustCompile(`\{\{.*?\}\}|\{%.*?%\}`)
)

func renderDemoPage(tpl, base, version string, preload []string, icons, demoTag string) (string, error) {
	text := strings.ReplaceAll(tpl, `{% include "partials/icons.html" %}`, icons)
	var pre strings.Builder
	for _, m := range preload {
		pre.WriteString(`<link rel="modulepreload" href="` + base + "/" + m + "\">\n")
	}
	text = preloadLoopRe.ReplaceAllLiteralString(text, pre.String())
	text = assetTagRe.ReplaceAllString(text, strings.ReplaceAll(base, "$", "$$")+"/$1")
	// Pas d'état intégré : l'interface le demande à l'API (simulée)
	text = strings.ReplaceAll(text, "{{ boot|tojson }}", "null")
	text = strings.ReplaceAll(text, "{{ panel_version }}", version)
	// Ni CSP ni session sur un site statique : pas de nonce, jeton CSRF factice
	text = strings.ReplaceAll(text, ` nonce="{{ csp_nonce() }}"`, "")
	text = strings.ReplaceAll(text, "{{ csrf_token() }}", "demo")
	// Page de connexion : pas de connexion unique dans la démo
	text = oidcBlockRe.ReplaceAllString(text, "")
	text = strings.ReplaceAll(text, "{{ oidc_error }}", "")
	// demo.js avant tout module : fetch et WebSocket sont remplacés avant le premier appel
	text = strings.Replace(text, "</title>", "</title>\n"+demoTag, 1)
	if left := jinjaLeftRe.FindAllString(text, -1); len(left) > 0 {
		return "", fmt.Errorf("[demo] balises de gabarit non traitées : %v", left)
	}
	return text, nil
}

// buildDemo construit la démo dans out ; embedded : placée dans le site, sous /demo/.
func buildDemo(root, out, version, site string, embedded bool) error {
	templates := filepath.Join(root, "panel", "templates")
	demoDir := filepath.Join(root, "website", "demo")
	raw, err := os.ReadFile(filepath.Join(demoDir, "demo.js"))
	if err != nil {
		return err
	}
	demoJS := strings.ReplaceAll(lf(string(raw)), "__PANEL_VERSION__", version)

	// Empreinte du contenu : les assets vont dans v/<empreinte>/, comme sur le vrai
	// panel. Chaque version a ses propres adresses (imports des modules compris).
	var rels []string
	filepath.WalkDir(filepath.Join(templates, "assets"), func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			rel, _ := filepath.Rel(templates, p)
			rels = append(rels, filepath.ToSlash(rel))
		}
		return nil
	})
	sortPaths(rels)
	digest := sha256.New()
	digest.Write([]byte(version + "|" + demoJS))
	for _, rel := range rels {
		b, err := os.ReadFile(filepath.Join(templates, filepath.FromSlash(rel)))
		if err != nil {
			return err
		}
		digest.Write([]byte(rel + "|"))
		digest.Write(b)
	}
	base := "v/" + hex.EncodeToString(digest.Sum(nil))[:10]
	href := "./" + base // un import de module exige un chemin relatif explicite

	if err := emptyDir(out); err != nil {
		return err
	}
	for _, rel := range rels {
		dst := filepath.Join(out, filepath.FromSlash(base), filepath.FromSlash(strings.TrimPrefix(rel, "assets/")))
		if err := copyFile(filepath.Join(templates, filepath.FromSlash(rel)), dst); err != nil {
			return err
		}
	}
	for rel, pairs := range demoPatches {
		f := filepath.Join(out, filepath.FromSlash(base), filepath.FromSlash(rel))
		b, err := os.ReadFile(f)
		if err != nil {
			return err
		}
		text, err := patchText(lf(string(b)), pairs, rel)
		if err != nil {
			return err
		}
		if err := os.WriteFile(f, []byte(text), 0o644); err != nil {
			return err
		}
	}
	if err := os.WriteFile(filepath.Join(out, filepath.FromSlash(base), "demo.js"), []byte(demoJS), 0o644); err != nil {
		return err
	}

	var preload []string
	for _, rel := range rels {
		if strings.HasPrefix(rel, "assets/js/") && strings.HasSuffix(rel, ".js") {
			preload = append(preload, strings.TrimPrefix(rel, "assets/"))
		}
	}
	sortPaths(preload)
	preload = append(preload, "locales/fr.js")
	iconsRaw, err := os.ReadFile(filepath.Join(templates, "partials", "icons.html"))
	if err != nil {
		return err
	}
	icons := lf(string(iconsRaw))
	demoTag := `<script src="` + href + `/demo.js"></script>`
	read := func(name string) (string, error) {
		b, err := os.ReadFile(filepath.Join(templates, name))
		return lf(string(b)), err
	}
	index, err := read("index.html")
	if err != nil {
		return err
	}
	if index, err = patchText(index, [][2]string{
		{`<html lang="fr">`, `<html lang="en">`}, // langue servie par défaut aux visiteurs
		{"<title>FRP Manager</title>", "<title>" + seoTitle + "</title>\n" + seoHead(site, version)},
		{`<noscript><p style="padding:24px">FRP Manager nécessite JavaScript.</p></noscript>`, noscript},
	}, "index.html"); err != nil {
		return err
	}
	page, err := renderDemoPage(index, href, version, preload, icons, demoTag)
	if err != nil {
		return err
	}
	if err := writeFile(out, "index.html", page); err != nil {
		return err
	}
	login, err := read("login.html")
	if err != nil {
		return err
	}
	if login, err = patchText(login, append(append([][2]string{}, loginPatches...),
		[2]string{"<title>", "<meta name=\"robots\" content=\"noindex, follow\">\n<title>"}), "login.html"); err != nil { // page sans intérêt en recherche
		return err
	}
	if page, err = renderDemoPage(login, href, version, preload, icons, demoTag); err != nil {
		return err
	}
	if err := writeFile(out, "login.html", page); err != nil {
		return err
	}
	if err := copyFile(filepath.Join(demoDir, "og.png"), filepath.Join(out, "og.png")); err != nil {
		return err
	}
	// Démo dans le site : robots.txt et sitemap.xml sont ceux du site
	if !embedded {
		if err := writeFile(out, "robots.txt", "User-agent: *\nAllow: /\n\nSitemap: "+site+"/sitemap.xml\n"); err != nil {
			return err
		}
		if err := writeFile(out, "sitemap.xml", "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n"+
			"<urlset xmlns=\"http://www.sitemaps.org/schemas/sitemap/0.9\">\n"+
			"  <url><loc>"+site+"/</loc><changefreq>weekly</changefreq><priority>1.0</priority></url>\n"+
			"</urlset>\n"); err != nil {
			return err
		}
	}
	// Hébergeurs Apache : pas de liste des dossiers, pages HTML toujours relues
	if err := writeFile(out, ".htaccess", "Options -Indexes\nDirectoryIndex index.html\n<IfModule mod_headers.c>\n"+
		"  <FilesMatch \"\\.html$\">\n    Header set Cache-Control \"no-cache\"\n  </FilesMatch>\n</IfModule>\n"); err != nil {
		return err
	}
	if err := writeFile(out, "v/.htaccess", "<IfModule mod_headers.c>\n"+
		"  Header set Cache-Control \"public, max-age=31536000, immutable\"\n</IfModule>\n"); err != nil {
		return err
	}
	if err := writeFile(out, "version.json", jsonText(OM{{"version", version}, {"assets", base}}, false)+"\n"); err != nil {
		return err
	}
	n := 0
	filepath.WalkDir(out, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			n++
		}
		return nil
	})
	fmt.Printf("[demo] v%s -> %s (%d fichiers, assets dans %s/)\n", version, out, n, base)
	return nil
}
