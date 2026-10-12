// Audit SEO du site construit.
//
// Vérifie, pour chaque page indexable : titre et description (longueur, unicité),
// un seul h1, canonical, hreflang réciproques, Open Graph, images (alt,
// dimensions), données structurées (JSON valide), liens internes, présence dans
// le sitemap. Une erreur fait échouer ; les avertissements (longueurs limites) non.

package main

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"golang.org/x/net/html"
)

const (
	titleMax = 62
	descMin  = 70
	descMax  = 165
)

type seoPage struct {
	title, lang string
	h1          []string
	meta        map[string][]string
	links       []map[string]string
	imgs        []map[string]string
	anchors     []string
	ids         map[string]bool
	jsonld      []string
}

var spacesRe = regexp.MustCompile(`\s+`)

func parsePage(text string) *seoPage {
	p := &seoPage{meta: map[string][]string{}, ids: map[string]bool{}}
	z := html.NewTokenizer(strings.NewReader(text))
	in, buf := "", ""
	for {
		tt := z.Next()
		switch tt {
		case html.ErrorToken:
			return p
		case html.StartTagToken, html.SelfClosingTagToken:
			tok := z.Token()
			a := map[string]string{}
			for _, at := range tok.Attr {
				a[at.Key] = at.Val
			}
			if a["id"] != "" {
				p.ids[a["id"]] = true
			}
			switch tok.Data {
			case "html":
				p.lang = a["lang"]
			case "meta":
				key := a["name"]
				if key == "" {
					key = a["property"]
				}
				if key != "" {
					p.meta[key] = append(p.meta[key], a["content"])
				}
			case "link":
				p.links = append(p.links, a)
			case "img":
				p.imgs = append(p.imgs, a)
			case "a":
				if a["href"] != "" {
					p.anchors = append(p.anchors, a["href"])
				}
			case "script":
				if a["type"] == "application/ld+json" {
					in, buf = "ld", ""
				}
			case "title", "h1":
				in, buf = tok.Data, ""
			}
		case html.TextToken:
			if in != "" {
				buf += string(z.Text())
			}
		case html.EndTagToken:
			name, _ := z.TagName()
			tag := string(name)
			switch {
			case in == "ld" && tag == "script":
				p.jsonld = append(p.jsonld, buf)
			case in == "title" && tag == "title":
				p.title = strings.TrimSpace(buf)
			case in == "h1" && tag == "h1":
				p.h1 = append(p.h1, strings.TrimSpace(spacesRe.ReplaceAllString(buf, " ")))
			default:
				continue
			}
			in = ""
		}
	}
}

func urlToFile(root, p string) string {
	f := filepath.Join(root, filepath.FromSlash(strings.TrimLeft(p, "/")))
	if st, err := os.Stat(f); strings.HasSuffix(p, "/") || (err == nil && st.IsDir()) {
		return filepath.Join(f, "index.html")
	}
	return f
}

type sitemapEntry struct {
	alts    map[string]string
	lastmod string
}

func readSitemap(file string) (map[string]sitemapEntry, error) {
	raw, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	var doc struct {
		URLs []struct {
			Loc     string `xml:"loc"`
			Lastmod string `xml:"lastmod"`
			Links   []struct {
				Hreflang string `xml:"hreflang,attr"`
				Href     string `xml:"href,attr"`
			} `xml:"http://www.w3.org/1999/xhtml link"`
		} `xml:"url"`
	}
	if err := xml.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	out := map[string]sitemapEntry{}
	for _, u := range doc.URLs {
		alts := map[string]string{}
		for _, l := range u.Links {
			alts[l.Hreflang] = l.Href
		}
		out[strings.TrimSpace(u.Loc)] = sitemapEntry{alts, strings.TrimSpace(u.Lastmod)}
	}
	return out, nil
}

// seoCheck → nombre d'erreurs.
func seoCheck(root, site string) (int, error) {
	site = strings.TrimRight(site, "/")
	var errs, warns []string
	errf := func(page, msg string) { errs = append(errs, page+": "+msg) }
	warnf := func(page, msg string) { warns = append(warns, page+": "+msg) }

	sitemap, err := readSitemap(filepath.Join(root, "sitemap.xml"))
	if err != nil {
		return 0, err
	}
	robots, _ := os.ReadFile(filepath.Join(root, "robots.txt"))
	if !strings.Contains(string(robots), "Sitemap: "+site+"/sitemap.xml") {
		errf("robots.txt", "ligne Sitemap absente")
	}
	pages := map[string]*seoPage{}
	var order []string
	filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, ".html") {
			rel, _ := filepath.Rel(root, p)
			order = append(order, filepath.ToSlash(rel))
		}
		return nil
	})
	sortPaths(order)
	for i, rel := range order {
		b, _ := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		order[i] = "/" + rel
		pages["/"+rel] = parsePage(string(b))
	}
	titles, descs := map[string][]string{}, map[string][]string{}
	var titleOrder, descOrder []string
	for _, rel := range order {
		page := pages[rel]
		pth := rel
		if strings.HasSuffix(rel, "/index.html") {
			pth = strings.TrimSuffix(rel, "index.html")
		}
		noindex := strings.Contains(strings.Join(page.meta["robots"], " "), "noindex")
		canon := ""
		for _, l := range page.links {
			if l["rel"] == "canonical" {
				canon = l["href"]
				break
			}
		}
		if strings.HasPrefix(rel, "/demo/v/") || strings.HasPrefix(rel, "/assets/") || rel == "/demo/login.html" ||
			strings.HasPrefix(path.Base(rel), "google") || (noindex && rel != "/404.html") {
			continue
		}
		if page.lang == "" {
			errf(rel, "attribut lang absent de <html>")
		}
		if noindex {
			if _, ok := sitemap[site+pth]; ok {
				errf(rel, "noindex mais présente dans le sitemap")
			}
			continue
		}
		if page.title == "" {
			errf(rel, "titre absent")
		} else if n := utf8.RuneCountInString(page.title); n > titleMax {
			warnf(rel, fmt.Sprintf("titre de %d caractères (> %d) : %s", n, titleMax, page.title))
		}
		if page.title != "" {
			if _, ok := titles[page.title]; !ok {
				titleOrder = append(titleOrder, page.title)
			}
			titles[page.title] = append(titles[page.title], rel)
		}
		desc := ""
		if d := page.meta["description"]; len(d) > 0 {
			desc = d[0]
		}
		if desc == "" {
			errf(rel, "meta description absente")
		} else {
			if n := utf8.RuneCountInString(desc); n < descMin || n > descMax {
				warnf(rel, fmt.Sprintf("description de %d caractères (attendu %d-%d)", n, descMin, descMax))
			}
			if _, ok := descs[desc]; !ok {
				descOrder = append(descOrder, desc)
			}
			descs[desc] = append(descs[desc], rel)
		}
		if strings.HasPrefix(rel, "/demo/") {
			if len(page.h1) > 1 {
				errf(rel, fmt.Sprintf("%d balises h1", len(page.h1)))
			}
		} else if len(page.h1) != 1 {
			errf(rel, fmt.Sprintf("%d balises h1 (une attendue)", len(page.h1)))
		}
		if canon != site+pth {
			errf(rel, fmt.Sprintf("canonical %q au lieu de %s%s", canon, site, pth))
		}
		if _, ok := sitemap[site+pth]; !ok {
			errf(rel, "absente du sitemap")
		}
		for _, key := range []string{"og:title", "og:description", "og:url", "og:image", "twitter:card"} {
			if len(page.meta[key]) == 0 {
				errf(rel, "balise "+key+" absente")
			}
		}
		if og := page.meta["og:url"]; len(og) == 0 || og[0] != site+pth {
			errf(rel, "og:url différente du canonical")
		}
		if !strings.HasPrefix(rel, "/demo/") {
			alts := map[string]string{}
			for _, l := range page.links {
				if l["rel"] == "alternate" && l["hreflang"] != "" {
					alts[l["hreflang"]] = l["href"]
				}
			}
			for _, lang := range []string{"en", "fr", "x-default"} {
				if _, ok := alts[lang]; !ok {
					errf(rel, "hreflang "+lang+" absent")
				}
			}
			for _, lang := range sortedKeys(alts) {
				href := alts[lang]
				u, _ := url.Parse(href)
				other := u.Path
				key := other
				if strings.HasSuffix(other, "/") {
					key = other + "index.html"
				}
				back := pages[key]
				if back == nil {
					errf(rel, "hreflang "+lang+" vers une page introuvable : "+href)
					continue
				}
				backAlts := map[string]string{}
				for _, l := range back.links {
					if l["rel"] == "alternate" {
						backAlts[l["hreflang"]] = l["href"]
					}
				}
				if lang != "x-default" && backAlts[page.lang] != site+pth {
					errf(rel, "hreflang non réciproque avec "+other)
				}
			}
		}
		for _, img := range page.imgs {
			if _, ok := img["alt"]; !ok {
				errf(rel, "image sans alt : "+img["src"])
			}
			if img["width"] == "" || img["height"] == "" {
				warnf(rel, "image sans dimensions (décalage de mise en page) : "+img["src"])
			}
		}
		for _, block := range page.jsonld {
			var data any
			if err := json.Unmarshal([]byte(block), &data); err != nil {
				errf(rel, "JSON-LD invalide : "+err.Error())
				continue
			}
			var nodes []any
			switch d := data.(type) {
			case []any:
				nodes = d
			case map[string]any:
				if g, ok := d["@graph"].([]any); ok {
					nodes = g
				} else {
					nodes = []any{d}
				}
			}
			for _, n := range nodes {
				m, ok := n.(map[string]any)
				if _, has := m["@type"]; !ok || !has {
					errf(rel, "JSON-LD sans @type")
				}
			}
		}
		siteHost := ""
		if su, err := url.Parse(site); err == nil {
			siteHost = su.Host
		}
		for _, href := range page.anchors {
			if strings.HasPrefix(href, "#") || strings.HasPrefix(href, "mailto:") || strings.HasPrefix(href, "javascript:") {
				continue
			}
			u, err := url.Parse(href)
			if err != nil {
				errf(rel, "lien illisible : "+href)
				continue
			}
			target := u.Path
			if u.Scheme == "http" || u.Scheme == "https" {
				if u.Host != siteHost {
					continue
				}
				if target == "" {
					target = "/"
				}
			} else if !strings.HasPrefix(target, "/") {
				target = pth[:strings.LastIndex(pth, "/")] + "/" + target
			}
			file := urlToFile(root, target)
			if _, err := os.Stat(file); err != nil {
				errf(rel, "lien interne cassé : "+href)
			} else if u.Fragment != "" && strings.HasSuffix(file, ".html") {
				r, _ := filepath.Rel(root, file)
				if tgt := pages["/"+filepath.ToSlash(r)]; tgt != nil && !tgt.ids[u.Fragment] {
					errf(rel, "ancre introuvable : "+href)
				}
			}
		}
	}
	for _, g := range []struct {
		what  string
		seen  map[string][]string
		order []string
	}{{"titre", titles, titleOrder}, {"description", descs, descOrder}} {
		for _, text := range g.order {
			if where := g.seen[text]; len(where) > 1 {
				n := min(3, len(where))
				errf(strings.Join(where[:n], ", "), g.what+" en double : "+truncate(text, 70))
			}
		}
	}
	locs := make([]string, 0, len(sitemap))
	for loc := range sitemap {
		locs = append(locs, loc)
	}
	sort.Strings(locs)
	for _, loc := range locs {
		u, _ := url.Parse(loc)
		if _, err := os.Stat(urlToFile(root, u.Path)); err != nil {
			errf("sitemap.xml", "URL sans fichier : "+loc)
		}
		if sitemap[loc].lastmod == "" {
			warnf("sitemap.xml", "lastmod absent : "+loc)
		}
	}
	for _, w := range warns {
		fmt.Println("WARN ", w)
	}
	for _, e := range errs {
		fmt.Println("ERROR", e)
	}
	fmt.Printf("[seo] %d pages HTML, %d URL dans le sitemap : %d erreur(s), %d avertissement(s)\n",
		len(pages), len(sitemap), len(errs), len(warns))
	return len(errs), nil
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
