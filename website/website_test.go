package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInlineMarkdown(t *testing.T) {
	r := newRenderer(nil)
	r.refs["0.0.51"] = "https://example.org/v51"
	for in, want := range map[string]string{
		"un *mot* ici":                          "un <em>mot</em> ici",
		"**gras** et `code <x>`":                "<strong>gras</strong> et <code>code &lt;x&gt;</code>",
		"a*b*c reste":                           "a*b*c reste",
		"* pas une liste ici":                   "* pas une liste ici",
		"[lien](https://github.com/x)":          `<a href="https://github.com/x" rel="noopener">lien</a>`,
		"[site](https://frp-manager.gogow.fr/)": `<a href="https://frp-manager.gogow.fr/">site</a>`,
		"voir [0.0.51]":                         `voir <a href="https://example.org/v51">0.0.51</a>`,
		"[inconnu] reste":                       "[inconnu] reste",
		"a & b < c":                             "a &amp; b &lt; c",
		"![logo](img.png)":                      `<img src="img.png" alt="logo" loading="lazy">`,
	} {
		if got := r.inline(in); got != want {
			t.Errorf("%q\n  obtenu  %q\n  attendu %q", in, got, want)
		}
	}
}

func TestBlocksMarkdown(t *testing.T) {
	md := "# Titre\n\n## [0.0.56] - 2026-10-11\n\nTexte *ici*.\n\n- un\n- deux\n  suite\n\n1. a\n2. b\n\n> cité\n\n| A | B |\n|---|--:|\n| 1 | 2 |\n\n```bash\necho \"x\"\n```\n\n---\n\n[0.0.56]: https://example.org\n"
	html, heads := renderMarkdown(md, nil)
	for _, want := range []string{
		"<h1>Titre</h1>",
		`<h2 id="0056---2026-10-11"><a class="anchor" href="#0056---2026-10-11">0.0.56 - 2026-10-11</a></h2>`,
		"<p>Texte <em>ici</em>.</p>",
		"<ul><li>un</li><li>deux suite</li></ul>",
		"<ol><li>a</li><li>b</li></ol>",
		"<blockquote><p>cité</p></blockquote>",
		`<td style="text-align:right">2</td>`,
		`<code class="language-bash">echo &quot;x&quot;</code>`,
		"<hr>",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("absent : %s\n%s", want, html)
		}
	}
	if len(heads) != 2 || heads[1].anchor != "0056---2026-10-11" {
		t.Fatalf("%v", heads)
	}
	if slugify("Accéder au panel depuis un autre poste") != "accéder-au-panel-depuis-un-autre-poste" {
		t.Fatal(slugify("Accéder au panel depuis un autre poste"))
	}
}

func TestPyJSON(t *testing.T) {
	v := OM{{"b", 1}, {"a", []any{"é", true, nil}}, {"c", "x\"<y>\n"}}
	if got := jsonText(v, true); got != `{"b":1,"a":["é",true,null],"c":"x\"<y>\n"}` {
		t.Fatal(got)
	}
	if got := jsonText(OM{{"v", "1"}, {"w", "2"}}, false); got != `{"v": "1", "w": "2"}` {
		t.Fatal(got)
	}
	if esc(`a'b"<&>`) != "a&#x27;b&quot;&lt;&amp;&gt;" {
		t.Fatal(esc(`a'b"<&>`))
	}
}

// Construction complète, puis audit SEO : aucune erreur.
func TestBuildSiteAndSEO(t *testing.T) {
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
	if err := buildSite(root, out, "https://frp-manager.gogow.fr", "0.0.0-test"); err != nil {
		t.Fatal(err)
	}
	n, err := seoCheck(out, "https://frp-manager.gogow.fr")
	if err != nil || n != 0 {
		t.Fatalf("audit SEO : %d erreur(s) (%v)", n, err)
	}
	home, _ := os.ReadFile(filepath.Join(out, "index.html"))
	for _, want := range []string{`id="tab-apt"`, "frp-manager --reset-password", "/demo/"} {
		if !strings.Contains(string(home), want) {
			t.Errorf("accueil sans %q", want)
		}
	}
	for _, f := range []string{"fr/docs/installation/index.html", "demo/index.html", "demo/login.html", "sitemap.xml", "llms.txt", "404.html"} {
		if _, err := os.Stat(filepath.Join(out, filepath.FromSlash(f))); err != nil {
			t.Errorf("%s absent", f)
		}
	}
}
