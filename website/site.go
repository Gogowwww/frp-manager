// Site vitrine : page d'accueil, documentation et démo en ligne.
//
// Site statique bilingue (anglais à la racine, français sous /fr/) :
//   /                  page d'accueil
//   /docs/<page>/      documentation, rendue depuis les fichiers Markdown du dépôt
//                      (docs/, CHANGELOG.md…) : toujours à jour
//   /demo/             la démo (vraie interface, données fictives, demo.go)

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const (
	repo    = "Gogowwww/frp-manager"
	repoURL = "https://github.com/" + repo
	current = ` aria-current="page"`
)

type siteCtx struct {
	root, site, version, assetV, icons string
}

// ── Dates de dernière modification (sitemap, données structurées) ──────────

var lastmodCache = map[string]string{}

// lastmod : date (AAAA-MM-JJ) du dernier commit qui touche ces chemins ; "" sans
// historique complet (clone superficiel : la date ne dirait rien).
func lastmod(root string, paths ...string) string {
	key := strings.Join(paths, "\x00")
	if v, ok := lastmodCache[key]; ok {
		return v
	}
	v := ""
	if out, err := exec.Command("git", "-C", root, "rev-parse", "--is-shallow-repository").Output(); err == nil &&
		strings.TrimSpace(string(out)) != "true" {
		args := append([]string{"-C", root, "log", "-1", "--format=%cs", "--"}, paths...)
		if out, err := exec.Command("git", args...).Output(); err == nil {
			v = strings.TrimSpace(string(out))
		}
	}
	lastmodCache[key] = v
	return v
}

func prefix(lang string) string {
	if lang == "en" {
		return ""
	}
	return "/fr"
}

func docURL(lang, slug string) string {
	if slug == "" {
		return prefix(lang) + "/"
	}
	return prefix(lang) + "/docs/" + slug + "/"
}

func icon(name string, cls ...string) string {
	c := "i"
	if len(cls) > 0 {
		c = cls[0]
	}
	return `<svg class="` + c + `" aria-hidden="true"><use href="#i-` + name + `"/></svg>`
}

func otherLang(lang string) string { return t(lang, "other_lang_code") }

// ── Gabarit commun ──────────────────────────────────────────────────────────

type shellOpts struct {
	section string
	noindex bool
	ld      any
	preload string
}

func shell(lang, pagePath, altPath, title, desc, body string, ctx siteCtx, o shellOpts) string {
	other := otherLang(lang)
	canonical := ctx.site + pagePath
	nav := [][3]string{
		{prefix(lang) + "/#features", t(lang, "nav_features"), "features"},
		{prefix(lang) + "/docs/", t(lang, "nav_docs"), "docs"},
		{"/demo/", t(lang, "nav_demo"), "demo"},
	}
	var navHTML strings.Builder
	for _, n := range nav {
		cur := ""
		if n[2] == o.section {
			cur = current
		}
		navHTML.WriteString(`<a href="` + n[0] + `"` + cur + `>` + n[1] + `</a>`)
	}
	var footerDocs strings.Builder
	for _, d := range docList {
		footerDocs.WriteString(`<li><a href="` + docURL(lang, d.slug) + `">` + esc(docName(lang, d.slug)) + `</a></li>`)
	}
	robots := `<meta name="robots" content="index, follow, max-image-preview:large, max-snippet:-1">`
	if o.noindex {
		robots = `<meta name="robots" content="noindex">`
	}
	imageAlt := t(lang, "og_alt")
	ldTag := ""
	if o.ld != nil {
		ldTag = `<script type="application/ld+json">` + jsonText(o.ld, true) + `</script>`
	}
	fonts := "https://fonts.googleapis.com/css2?family=IBM+Plex+Mono:wght@400;500&family=" +
		"IBM+Plex+Sans:ital,wght@0,400;0,500;0,600;0,700;1,400&display=swap"
	alternates := ""
	if !o.noindex {
		xdef := pagePath
		if lang == "fr" {
			xdef = altPath
		}
		alternates = `<link rel="alternate" hreflang="` + lang + `" href="` + ctx.site + pagePath + "\">\n" +
			`<link rel="alternate" hreflang="` + other + `" href="` + ctx.site + altPath + "\">\n" +
			`<link rel="alternate" hreflang="x-default" href="` + ctx.site + xdef + `">`
	}
	section := o.section
	if section == "" {
		section = "other"
	}
	a := "/assets/" + ctx.assetV
	return `<!DOCTYPE html>
<html lang="` + t(lang, "html_lang") + `">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="color-scheme" content="light dark">
<title>` + esc(title) + `</title>
<meta name="description" content="` + esc(desc) + `">
` + robots + `
<link rel="canonical" href="` + canonical + `">
` + alternates + `
<meta property="og:type" content="website">
<meta property="og:site_name" content="FRP Manager">
<meta property="og:title" content="` + esc(title) + `">
<meta property="og:description" content="` + esc(desc) + `">
<meta property="og:url" content="` + canonical + `">
<meta property="og:image" content="` + ctx.site + `/og.png">
<meta property="og:image:width" content="1280">
<meta property="og:image:height" content="640">
<meta property="og:image:alt" content="` + esc(imageAlt) + `">
<meta property="og:locale" content="` + t(lang, "locale") + `">
<meta property="og:locale:alternate" content="` + t(other, "locale") + `">
<meta name="twitter:card" content="summary_large_image">
<meta name="twitter:title" content="` + esc(title) + `">
<meta name="twitter:description" content="` + esc(desc) + `">
<meta name="twitter:image" content="` + ctx.site + `/og.png">
<meta name="twitter:image:alt" content="` + esc(imageAlt) + `">
<meta name="theme-color" content="#0e9f6e">
<link rel="icon" type="image/svg+xml" href="` + a + `/favicon.svg">
<script>try{var th=localStorage.getItem('frpm.site.theme');if(th==='light'||th==='dark')document.documentElement.dataset.theme=th}catch(e){}</script>
<link rel="preconnect" href="https://fonts.googleapis.com">
<link rel="preconnect" href="https://fonts.gstatic.com" crossorigin>
<link rel="preload" as="style" href="` + fonts + `" onload="this.onload=null;this.rel='stylesheet'">
<noscript><link rel="stylesheet" href="` + fonts + `"></noscript>
<link rel="stylesheet" href="` + a + `/site.css">
` + o.preload + `<script src="` + a + `/site.js" defer></script>
` + ldTag + `
</head>
<body class="page-` + section + `" data-copy="` + esc(t(lang, "copy")) + `" data-copied="` + esc(t(lang, "copied")) + `">
` + ctx.icons + `
<a class="skip" href="#main">` + t(lang, "skip") + `</a>
<header class="top">
  <div class="top-in">
    <a class="brand" href="` + prefix(lang) + `/">` + icon("logo", "brand-mark") + `<span>FRP Manager</span></a>
    <button class="nav-toggle" type="button" aria-expanded="false" aria-controls="nav">` + icon("menu") + `<span class="sr">` + t(lang, "menu") + `</span></button>
    <nav class="nav" id="nav">
      ` + navHTML.String() + `
      <a href="` + repoURL + `" rel="noopener">` + t(lang, "nav_source") + `</a>
      <span class="nav-tools">
        <a class="lang" href="` + altPath + `" hreflang="` + other + `" lang="` + other + `">` + t(lang, "other_lang") + `</a>
        <button class="theme" type="button" title="` + t(lang, "theme") + `" aria-label="` + t(lang, "theme") + `">` + icon("sun", "i i-light") + icon("moon", "i i-dark") + `</button>
      </span>
    </nav>
  </div>
</header>
<main id="main">
` + body + `
</main>
<footer class="foot">
  <div class="foot-in">
    <div class="foot-about">
      <a class="brand" href="` + prefix(lang) + `/">` + icon("logo", "brand-mark") + `<span>FRP Manager</span></a>
      <p>` + t(lang, "footer_about") + `</p>
      <p class="foot-ai">` + t(lang, "footer_ai") + `</p>
      <p class="foot-version">v` + esc(ctx.version) + `</p>
    </div>
    <div>
      <h2>` + t(lang, "footer_project") + `</h2>
      <ul>
        <li><a href="/demo/">` + t(lang, "nav_demo") + `</a></li>
        <li><a href="` + repoURL + `" rel="noopener">` + t(lang, "nav_source") + `</a></li>
        <li><a href="` + repoURL + `/releases" rel="noopener">` + t(lang, "releases") + `</a></li>
        <li><a href="` + repoURL + `/issues" rel="noopener">` + t(lang, "issues") + `</a></li>
        <li><a href="` + repoURL + `/blob/main/LICENSE" rel="noopener">` + t(lang, "license") + `</a></li>
      </ul>
    </div>
    <div>
      <h2>` + t(lang, "footer_docs") + `</h2>
      <ul>` + footerDocs.String() + `</ul>
    </div>
  </div>
</footer>
</body>
</html>
`
}

// ── Données structurées (schema.org) ────────────────────────────────────────

var author = OM{{"@type", "Person"}, {"name", "Gogowwww"}, {"url", "https://github.com/Gogowwww"}}

func homeLD(lang string, ctx siteCtx) OM {
	c, site := texts.Home[lang], ctx.site
	url := site + prefix(lang) + "/"
	img := site + "/assets/" + ctx.assetV + "/img"
	var shots []any
	for _, n := range []string{"dashboard", "ports", "firewall", "logs"} {
		shots = append(shots, img+"/"+n+"-"+lang+".webp")
	}
	var features []any
	for _, m := range c.More {
		features = append(features, m[1])
	}
	for _, r := range c.Rows {
		features = append(features, r.Title)
	}
	return OM{{"@context", "https://schema.org"}, {"@graph", []any{
		OM{{"@type", "SoftwareApplication"}, {"@id", site + "/#software"}, {"name", "FRP Manager"},
			{"description", c.Desc}, {"url", url}, {"inLanguage", lang},
			{"applicationCategory", "DeveloperApplication"},
			{"applicationSubCategory", "Network tunnel and reverse proxy management"},
			{"operatingSystem", "Linux, Docker"}, {"softwareVersion", ctx.version},
			{"image", site + "/og.png"}, {"screenshot", shots}, {"featureList", features},
			{"downloadUrl", repoURL + "/releases/latest"}, {"codeRepository", repoURL},
			{"releaseNotes", site + docURL(lang, "changelog")},
			{"license", "https://www.apache.org/licenses/LICENSE-2.0"}, {"isAccessibleForFree", true},
			{"offers", OM{{"@type", "Offer"}, {"price", "0"}, {"priceCurrency", "EUR"}}},
			{"keywords", "frp, frps, frpc, frp panel, frp dashboard, frp web ui, reverse proxy, tunnel, " +
				"port forwarding, self-hosted, homelab, nftables firewall"},
			{"author", author}, {"sameAs", []any{repoURL}}},
		OM{{"@type", "WebSite"}, {"@id", site + "/#website"}, {"name", "FRP Manager"}, {"url", url},
			{"inLanguage", lang}, {"publisher", author}},
	}}}
}

type crumb struct{ name, path string }

func breadcrumbLD(ctx siteCtx, crumbs []crumb) OM {
	var items []any
	for n, c := range crumbs {
		items = append(items, OM{{"@type", "ListItem"}, {"position", n + 1}, {"name", c.name}, {"item", ctx.site + c.path}})
	}
	return OM{{"@type", "BreadcrumbList"}, {"itemListElement", items}}
}

func crumbsHTML(crumbs []crumb) string {
	var links strings.Builder
	for _, c := range crumbs[:len(crumbs)-1] {
		links.WriteString(`<li><a href="` + c.path + `">` + esc(c.name) + `</a></li>`)
	}
	return `<nav class="crumbs" aria-label="Breadcrumb"><ol>` + links.String() +
		`<li aria-current="page">` + esc(crumbs[len(crumbs)-1].name) + `</li></ol></nav>`
}

// ── Page d'accueil ──────────────────────────────────────────────────────────

func codeBlock(cmd string) string {
	return `<div class="code"><pre><code>` + esc(cmd) + `</code></pre></div>`
}

func stepsHTML(items []step) string {
	var b strings.Builder
	for _, s := range items {
		code := ""
		if s.Cmd != "" {
			code = codeBlock(s.Cmd)
		}
		b.WriteString("<li><p>" + esc(s.Text) + "</p>" + code + "</li>")
	}
	return b.String()
}

func homeBody(lang string, ctx siteCtx) string {
	c := texts.Home[lang]
	img := "/assets/" + ctx.assetV + "/img"
	var route []string
	for n, r := range c.Route {
		if n > 0 {
			route = append(route, `<li class="route-link" aria-hidden="true"><span>`+esc(c.RouteLinks[n-1])+`</span></li>`)
		}
		mono := ""
		if n > 0 {
			mono = " mono"
		}
		route = append(route, `<li class="route-node"><span class="route-ico">`+icon(r[0])+`</span>`+
			`<span><span class="route-addr`+mono+`">`+esc(r[1])+`</span>`+
			`<span class="route-label">`+esc(r[2])+`</span></span></li>`)
	}
	var rows []string
	for n, row := range c.Rows {
		var points strings.Builder
		for _, p := range row.Points {
			points.WriteString("<li>" + icon("check-circle") + "<span>" + p + "</span></li>")
		}
		flip := ""
		if n%2 == 1 {
			flip = " feature-flip"
		}
		rows = append(rows, `<article class="feature`+flip+`">
  <div class="feature-text">
    <h3>`+row.Title+`</h3>
    <p>`+row.Text+`</p>
    <ul class="checks">`+points.String()+`</ul>
  </div>
  <figure class="shot"><img src="`+img+"/"+row.Img+"-"+lang+`.webp" alt="`+esc(row.Alt)+`" loading="lazy" width="1560" height="1170"></figure>
</article>`)
	}
	var more strings.Builder
	for _, m := range c.More {
		more.WriteString(`<div class="more-item">` + icon(m[0]) + "<h3>" + esc(m[1]) + "</h3><p>" + esc(m[2]) + "</p></div>")
	}
	var whyHead, whyRows, sec, faq strings.Builder
	for _, h := range c.WhyHead {
		whyHead.WriteString("<th>" + h + "</th>")
	}
	for _, r := range c.WhyRows {
		whyRows.WriteString(`<tr><th scope="row">` + r[0] + "</th><td>" + r[1] + "</td><td>" + r[2] + "</td></tr>")
	}
	for _, s := range c.SecPoints {
		sec.WriteString("<div><h3>" + esc(s[0]) + "</h3><p>" + esc(s[1]) + "</p></div>")
	}
	for _, f := range c.Faq {
		faq.WriteString("<details><summary>" + esc(f[0]) + "</summary><p>" + f[1] + "</p></details>")
	}
	// Onglets de « Démarrer » : le premier ouvert
	var tabList, tabPanels []string
	for n, tab := range c.Tabs {
		selected, extra, hidden := "true", "", ""
		if n > 0 {
			selected, extra, hidden = "false", ` tabindex="-1"`, " hidden"
		}
		tabList = append(tabList, `        <button role="tab" type="button" aria-selected="`+selected+`" aria-controls="tab-`+tab.ID+
			`" id="tab-`+tab.ID+`-btn"`+extra+`>`+tab.Label+`</button>`)
		tabPanels = append(tabPanels, `      <div class="tab-panel" role="tabpanel" id="tab-`+tab.ID+`" aria-labelledby="tab-`+tab.ID+`-btn"`+hidden+`>
        <ol class="steps">`+stepsHTML(append(append([]step{}, tab.Steps...), c.StepsCommon...))+`</ol>
      </div>`)
	}
	v := esc(ctx.version)
	return `<section class="hero">
  <div class="wrap hero-in">
    <div class="hero-text">
      <p class="hero-version"><a href="` + repoURL + `/releases" rel="noopener">v` + v + `</a></p>
      <h1>` + c.H1 + `</h1>
      <p class="lead">` + c.Lead + `</p>
      <div class="actions">
        <a class="btn btn-primary" href="/demo/">` + icon("play") + c.CtaDemo + `</a>
        <a class="btn" href="#start">` + icon("download") + c.CtaInstall + `</a>
      </div>
      <p class="hero-meta">` + c.Meta + `</p>
    </div>
    <figure class="route">
      <ol>` + strings.Join(route, "") + `</ol>
      <figcaption>` + c.RouteCaption + `</figcaption>
    </figure>
  </div>
  <div class="wrap">
    <figure class="window hero-shot">
      <div class="window-bar" aria-hidden="true"><span></span><span></span><span></span></div>
      <img src="` + img + `/dashboard-` + lang + `.webp" alt="` + esc(c.ShotAlt) + `" width="1920" height="1170" fetchpriority="high">
    </figure>
  </div>
</section>

<section class="section" id="features">
  <div class="wrap">
    <header class="section-head">
      <h2>` + c.FeaturesTitle + `</h2>
      <p>` + c.FeaturesLead + `</p>
    </header>
    ` + strings.Join(rows, "") + `
    <div class="more">` + more.String() + `</div>
  </div>
</section>

<section class="section section-demo" id="demo">
  <div class="wrap">
    <header class="section-head">
      <h2>` + c.DemoTitle + `</h2>
      <p>` + c.DemoLead + `</p>
    </header>
    <div class="window demo-frame" data-src="/demo/">
      <div class="window-bar" aria-hidden="true"><span></span><span></span><span></span></div>
      <button class="demo-launch" type="button">
        <img src="` + img + `/config-` + lang + `.webp" alt="" loading="lazy" width="1920" height="1170">
        <span class="btn btn-primary">` + icon("play") + c.DemoLaunch + `</span>
      </button>
    </div>
    <p class="demo-full"><a href="/demo/">` + icon("external") + c.DemoFull + `</a></p>
  </div>
</section>

<section class="section" id="start">
  <div class="wrap narrow">
    <header class="section-head">
      <h2>` + c.StartTitle + `</h2>
      <p>` + c.StartLead + `</p>
    </header>
    <div class="tabs" data-tabs>
      <div class="tab-list" role="tablist">
` + strings.Join(tabList, "\n") + `
      </div>
` + strings.Join(tabPanels, "\n") + `
    </div>
    <p class="note">` + c.StartMore + `</p>
  </div>
</section>

<section class="section" id="why">
  <div class="wrap narrow">
    <header class="section-head">
      <h2>` + c.WhyTitle + `</h2>
      <p>` + c.WhyLead + `</p>
    </header>
    <div class="table compare"><table><thead><tr>` + whyHead.String() + `</tr></thead><tbody>` + whyRows.String() + `</tbody></table></div>
    <p class="note">` + c.WhyNote + `</p>
  </div>
</section>

<section class="section section-security" id="security">
  <div class="wrap">
    <header class="section-head">
      ` + icon("shield", "i section-ico") + `
      <h2>` + c.SecTitle + `</h2>
      <p>` + c.SecLead + `</p>
    </header>
    <div class="sec-grid">` + sec.String() + `</div>
    <p class="callout">` + icon("alert") + `<span>` + c.SecDocker + `</span></p>
    <p class="note">` + c.SecMore + `</p>
  </div>
</section>

<section class="section" id="faq">
  <div class="wrap narrow">
    <header class="section-head"><h2>` + c.FaqTitle + `</h2></header>
    <div class="faq">` + faq.String() + `</div>
  </div>
</section>

<section class="final">
  <div class="wrap final-in">
    <div>
      <h2>` + c.FinalTitle + `</h2>
      <p>` + c.FinalText + `</p>
    </div>
    <div class="actions">
      <a class="btn btn-primary" href="/demo/">` + icon("play") + c.CtaDemo + `</a>
      <a class="btn" href="` + docURL(lang, "installation") + `">` + icon("download") + c.CtaInstall + `</a>
    </div>
  </div>
</section>`
}

// ── Documentation ───────────────────────────────────────────────────────────

// stripLangSwitch retire la ligne « English · Français » en tête des pages.
func stripLangSwitch(text string) string {
	lines := strings.Split(text, "\n")
	var out []string
	for n, line := range lines {
		if n < 6 && strings.Contains(line, "English") && strings.Contains(line, "Français") {
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

var externalRe = regexp.MustCompile(`^(https?:|mailto:|#)`)

func makeLinkFn(lang, src string) func(string) string {
	base := path.Dir(src)
	return func(url string) string {
		if externalRe.MatchString(url) {
			return url
		}
		p, frag, _ := strings.Cut(url, "#")
		if frag != "" {
			frag = "#" + frag
		}
		name := p[strings.LastIndex(p, "/")+1:]
		if df, ok := texts.DocFiles[name]; ok {
			target := df[0]
			if target == "" {
				target = lang
			}
			return docURL(target, df[1]) + frag
		}
		var parts []string
		for _, s := range strings.Split(base+"/"+p, "/") {
			switch s {
			case "..":
				if len(parts) > 0 {
					parts = parts[:len(parts)-1]
				}
			case "", ".":
			default:
				parts = append(parts, s)
			}
		}
		return repoURL + "/blob/main/" + strings.Join(parts, "/") + frag
	}
}

var titleMarksRe = regexp.MustCompile("[`*]")

func docPageHTML(lang string, d docPage, ctx siteCtx) (string, error) {
	src, contentLang := d.sources[lang], lang
	if src == "" {
		src, contentLang = d.sources["fr"], "fr"
	}
	raw, err := os.ReadFile(filepath.Join(ctx.root, filepath.FromSlash(src)))
	if err != nil {
		return "", err
	}
	body, headings := renderMarkdown(stripLangSwitch(lf(string(raw))), makeLinkFn(lang, src))
	title := docName(lang, d.slug)
	for _, h := range headings {
		if h.level == 1 {
			title = h.text
			break
		}
	}
	title = titleMarksRe.ReplaceAllString(title, "")
	var toc, side strings.Builder
	for _, h := range headings {
		if h.level == 2 || h.level == 3 {
			toc.WriteString(fmt.Sprintf(`<li class="toc-%d"><a href="#%s">%s</a></li>`, h.level, esc(h.anchor), newRenderer(nil).inline(h.text)))
		}
	}
	names := make([]string, len(docList))
	idx := 0
	for n, dd := range docList {
		names[n] = dd.slug
		if dd.slug == d.slug {
			idx = n
		}
		cur := ""
		if dd.slug == d.slug {
			cur = current
		}
		side.WriteString(`<li><a href="` + docURL(lang, dd.slug) + `"` + cur + `>` + esc(docName(lang, dd.slug)) + `</a></li>`)
	}
	notice := ""
	if contentLang != lang {
		notice = `<p class="callout callout-info">` + icon("info") + `<span>` + t(lang, "only_fr") + `</span></p>`
	}
	var pager []string
	if idx > 0 {
		pager = append(pager, `<a class="prev" href="`+docURL(lang, names[idx-1])+`">`+esc(docName(lang, names[idx-1]))+`</a>`)
	}
	if idx+1 < len(names) {
		pager = append(pager, `<a class="next" href="`+docURL(lang, names[idx+1])+`">`+esc(docName(lang, names[idx+1]))+`</a>`)
	}
	crumbs := []crumb{{"FRP Manager", prefix(lang) + "/"}, {t(lang, "docs_title"), prefix(lang) + "/docs/"},
		{docName(lang, d.slug), docURL(lang, d.slug)}}
	article := OM{{"@type", "TechArticle"}, {"headline", title}, {"description", docDesc(lang, d.slug)},
		{"inLanguage", contentLang}, {"url", ctx.site + docURL(lang, d.slug)},
		{"mainEntityOfPage", ctx.site + docURL(lang, d.slug)}, {"author", author}, {"publisher", author},
		{"image", ctx.site + "/og.png"}}
	if m := lastmod(ctx.root, src); m != "" {
		article = append(article, KV{"dateModified", m})
	}
	ld := OM{{"@context", "https://schema.org"}, {"@graph", []any{article, breadcrumbLD(ctx, crumbs)}}}
	tocHTML := ""
	if toc.Len() > 0 {
		tocHTML = `<p class="docs-side-title">` + t(lang, "on_this_page") + `</p><ul>` + toc.String() + `</ul>`
	}
	htmlBody := `<div class="wrap docs">
  <aside class="docs-side">
    <p class="docs-side-title"><a href="` + prefix(lang) + `/docs/">` + t(lang, "docs_title") + `</a></p>
    <ul>` + side.String() + `</ul>
  </aside>
  <article class="prose" lang="` + contentLang + `">
    ` + crumbsHTML(crumbs) + `
    ` + notice + `
    ` + body + `
    <nav class="pager">` + strings.Join(pager, "") + `</nav>
    <p class="edit"><a href="` + repoURL + `/blob/main/` + src + `" rel="noopener">` + icon("edit") + t(lang, "edit") + `</a></p>
  </article>
  <aside class="docs-toc">
    ` + tocHTML + `
  </aside>
</div>`
	alt := docURL(otherLang(lang), d.slug)
	return shell(lang, docURL(lang, d.slug), alt, title+" — FRP Manager", docDesc(lang, d.slug), htmlBody, ctx,
		shellOpts{section: "docs", ld: ld}), nil
}

func docsIndex(lang string, ctx siteCtx) string {
	var items strings.Builder
	for _, d := range docList {
		items.WriteString(`<li><a href="` + docURL(lang, d.slug) + `"><strong>` + esc(docName(lang, d.slug)) + `</strong>` +
			`<span>` + esc(docDesc(lang, d.slug)) + `</span></a></li>`)
	}
	crumbs := []crumb{{"FRP Manager", prefix(lang) + "/"}, {t(lang, "docs_title"), prefix(lang) + "/docs/"}}
	body := `<div class="wrap narrow docs-index">
  ` + crumbsHTML(crumbs) + `
  <h1>` + t(lang, "docs_title") + `</h1>
  <p class="lead">` + t(lang, "docs_intro") + `</p>
  <ul class="doc-list">` + items.String() + `</ul>
</div>`
	ld := OM{{"@context", "https://schema.org"}, {"@graph", []any{breadcrumbLD(ctx, crumbs)}}}
	return shell(lang, prefix(lang)+"/docs/", prefix(otherLang(lang))+"/docs/", t(lang, "docs_page_title"),
		t(lang, "docs_meta"), body, ctx, shellOpts{section: "docs", ld: ld})
}

func notFoundPage(ctx siteCtx) string {
	body := `<div class="wrap narrow notfound">
  <p class="notfound-code">404</p>
  <h1>` + t("en", "notfound_title") + `</h1>
  <p>` + t("en", "notfound_text") + `</p>
  <div class="actions"><a class="btn btn-primary" href="/">` + t("en", "notfound_home") + `</a><a class="btn" href="/docs/">` + t("en", "notfound_docs") + `</a></div>
  <div lang="fr" class="notfound-fr">
    <h2>` + t("fr", "notfound_title") + `</h2>
    <p>` + t("fr", "notfound_text") + `</p>
    <p><a href="/fr/">` + t("fr", "notfound_home") + `</a> · <a href="/fr/docs/">` + t("fr", "notfound_docs") + `</a></p>
  </div>
</div>`
	return shell("en", "/404.html", "/fr/", t("en", "notfound_title")+" — FRP Manager", t("en", "notfound_text"),
		body, ctx, shellOpts{noindex: true})
}

const favicon = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 26 26"><path d="M13 2L23 7.5V18.5L13 24L3 18.5V7.5L13 2Z" stroke="#10b981" stroke-width="1.5" stroke-linejoin="round" fill="#101418"/><path d="M7.5 11h11M16 8.5l2.5 2.5L16 13.5" stroke="#10b981" stroke-width="1.4" stroke-linecap="round" stroke-linejoin="round" fill="none"/><path d="M18.5 15H7.5M10 12.5L7.5 15l2.5 2.5" stroke="#10b981" stroke-width="1.4" stroke-linecap="round" stroke-linejoin="round" fill="none"/></svg>
`

// llmsTxt : résumé du site pour les assistants et moteurs de réponse (convention llms.txt).
func llmsTxt(site string) string {
	var docs []string
	for _, d := range docList {
		docs = append(docs, "- ["+docName("en", d.slug)+"]("+site+docURL("en", d.slug)+"): "+docDesc("en", d.slug))
	}
	return `# FRP Manager

> ` + texts.Home["en"].Desc + `

FRP Manager is a self-hosted web panel for frp, the fast reverse proxy. It drives the official frps (server) and
frpc (client) binaries, systemd services and Docker containers, and keeps their configuration as plain TOML.
Free and open source (Apache 2.0). Linux with systemd, or Docker. English and French.

## Try and install

- [Live demo](` + site + `/demo/): the real interface with sample data, nothing to install
- [Source code and releases](` + repoURL + `): issues, releases, license

## Documentation

` + strings.Join(docs, "\n") + `

## French

- [Accueil](` + site + `/fr/) and [documentation](` + site + `/fr/docs/) in French
`
}

// gitVersion : dernier tag du dépôt (sans le « v »), ou def.
func gitVersion(root, def string) string {
	out, err := exec.Command("git", "-C", root, "describe", "--tags", "--abbrev=0").Output()
	if v := strings.TrimPrefix(strings.TrimSpace(string(out)), "v"); err == nil && v != "" {
		return v
	}
	return def
}

func writeFile(out, rel, text string) error {
	p := filepath.Join(out, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, []byte(text), 0o644)
}

// emptyDir vide un dossier en le gardant (il peut être servi pendant la construction).
func emptyDir(dir string, keep ...string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		skip := false
		for _, k := range keep {
			if e.Name() == k {
				skip = true
			}
		}
		if !skip {
			if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
				return err
			}
		}
	}
	return nil
}

type sitePage struct{ path, alt, mod string }

// buildSite construit le site complet (démo comprise) dans out.
func buildSite(root, out, site, version string) error {
	if err := loadContent(root); err != nil {
		return err
	}
	if err := emptyDir(out); err != nil {
		return err
	}
	// La démo d'abord, dans /demo/ (elle vide son propre dossier)
	if err := buildDemo(root, filepath.Join(out, "demo"), version, site+"/demo", true); err != nil {
		return err
	}

	// Fichiers statiques sous /assets/<empreinte>/ : gardés en cache indéfiniment
	here := filepath.Join(root, "website")
	assets := map[string][]byte{"favicon.svg": []byte(favicon)}
	for _, n := range []string{"site.css", "site.js"} {
		b, err := os.ReadFile(filepath.Join(here, n))
		if err != nil {
			return err
		}
		assets[n] = b
	}
	// Captures prises dans la démo (go run ./website shots), une par page et par langue
	images := map[string][]byte{}
	matches, _ := filepath.Glob(filepath.Join(here, "img", "*.webp"))
	for _, m := range matches {
		b, err := os.ReadFile(m)
		if err != nil {
			return err
		}
		images[filepath.Base(m)] = b
	}
	digest := sha256.New()
	for _, group := range []map[string][]byte{assets, images} {
		names := make([]string, 0, len(group))
		for n := range group {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			digest.Write([]byte(n + "|"))
			digest.Write(group[n])
		}
	}
	assetV := hex.EncodeToString(digest.Sum(nil))[:10]
	adir := filepath.Join(out, "assets", assetV)
	if err := os.MkdirAll(filepath.Join(adir, "img"), 0o755); err != nil {
		return err
	}
	for n, b := range assets {
		if err := os.WriteFile(filepath.Join(adir, n), b, 0o644); err != nil {
			return err
		}
	}
	for n, b := range images {
		if err := os.WriteFile(filepath.Join(adir, "img", n), b, 0o644); err != nil {
			return err
		}
	}
	if err := copyFile(filepath.Join(here, "demo", "og.png"), filepath.Join(out, "og.png")); err != nil {
		return err
	}
	// Vérification de propriété Google Search Console (fichier à la racine)
	google, _ := filepath.Glob(filepath.Join(here, "google*.html"))
	for _, g := range google {
		if err := copyFile(g, filepath.Join(out, filepath.Base(g))); err != nil {
			return err
		}
	}

	icons, err := os.ReadFile(filepath.Join(root, "panel", "templates", "partials", "icons.html"))
	if err != nil {
		return err
	}
	ctx := siteCtx{root: root, site: site, version: version, assetV: assetV, icons: lf(string(icons))}
	var pages []sitePage
	docsMod := ""
	for _, d := range docList {
		for _, src := range d.sources {
			if m := lastmod(root, src); m > docsMod {
				docsMod = m
			}
		}
	}
	siteMod := lastmod(root, "website", "panel/templates")
	for _, lang := range langs {
		other := otherLang(lang)
		c := texts.Home[lang]
		home := shell(lang, prefix(lang)+"/", prefix(other)+"/", c.Title, c.Desc, homeBody(lang, ctx), ctx, shellOpts{
			section: "home", ld: homeLD(lang, ctx),
			preload: `<link rel="preload" as="image" type="image/webp" fetchpriority="high" href="/assets/` + assetV + `/img/dashboard-` + lang + ".webp\">\n"})
		if err := writeFile(out, strings.TrimPrefix(prefix(lang)+"/index.html", "/"), home); err != nil {
			return err
		}
		pages = append(pages, sitePage{prefix(lang) + "/", prefix(other) + "/", siteMod})
		if err := writeFile(out, strings.TrimPrefix(prefix(lang)+"/docs/index.html", "/"), docsIndex(lang, ctx)); err != nil {
			return err
		}
		pages = append(pages, sitePage{prefix(lang) + "/docs/", prefix(other) + "/docs/", docsMod})
		for _, d := range docList {
			page, err := docPageHTML(lang, d, ctx)
			if err != nil {
				return err
			}
			if err := writeFile(out, strings.TrimPrefix(docURL(lang, d.slug), "/")+"index.html", page); err != nil {
				return err
			}
			src := d.sources[lang]
			if src == "" {
				src = d.sources["fr"]
			}
			pages = append(pages, sitePage{docURL(lang, d.slug), docURL(other, d.slug), lastmod(root, src)})
		}
	}
	if err := writeFile(out, "404.html", notFoundPage(ctx)); err != nil {
		return err
	}
	if err := writeFile(out, "robots.txt", "User-agent: *\nAllow: /\n\nSitemap: "+site+"/sitemap.xml\n"); err != nil {
		return err
	}
	var urls strings.Builder
	entry := func(p sitePage) {
		links := ""
		if p.alt != "" {
			en, fr := p.path, p.alt
			if strings.HasPrefix(en, "/fr/") || en == "/fr" {
				en, fr = fr, en
			}
			for _, a := range [][2]string{{"en", en}, {"fr", fr}, {"x-default", en}} {
				links += `<xhtml:link rel="alternate" hreflang="` + a[0] + `" href="` + site + a[1] + `"/>`
			}
		}
		mod := ""
		if p.mod != "" {
			mod = "<lastmod>" + p.mod + "</lastmod>"
		}
		urls.WriteString("  <url><loc>" + site + p.path + "</loc>" + mod + links + "</url>\n")
	}
	for _, p := range pages {
		entry(p)
	}
	entry(sitePage{"/demo/", "", lastmod(root, "panel/templates", "website/demo")})
	if err := writeFile(out, "sitemap.xml", "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n"+
		`<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9" `+
		`xmlns:xhtml="http://www.w3.org/1999/xhtml">`+"\n"+urls.String()+"</urlset>\n"); err != nil {
		return err
	}
	if err := writeFile(out, "llms.txt", llmsTxt(site)); err != nil {
		return err
	}
	// Hébergeurs Apache : pages relues, assets en cache, page 404 du site
	if err := writeFile(out, ".htaccess", "Options -Indexes\nDirectoryIndex index.html\nErrorDocument 404 /404.html\n"+
		"<IfModule mod_headers.c>\n  <FilesMatch \"\\.html$\">\n    Header set Cache-Control \"no-cache\"\n"+
		"  </FilesMatch>\n</IfModule>\n"); err != nil {
		return err
	}
	if err := writeFile(out, "assets/.htaccess", "<IfModule mod_headers.c>\n"+
		"  Header set Cache-Control \"public, max-age=31536000, immutable\"\n</IfModule>\n"); err != nil {
		return err
	}
	if err := writeFile(out, "version.json", jsonText(OM{{"version", version}, {"assets", assetV}}, false)+"\n"); err != nil {
		return err
	}
	n := 0
	filepath.WalkDir(out, func(_ string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			n++
		}
		return nil
	})
	fmt.Printf("[site] v%s -> %s (%d pages, %d fichiers)\n", version, out, len(pages), n)
	return nil
}

func copyFile(src, dst string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return os.WriteFile(dst, b, 0o644)
}
