// Rendu Markdown minimal pour le site.
//
// Couvre ce qu'utilisent les pages du dépôt : titres (avec ancres façon GitHub),
// paragraphes, listes à puces et numérotées, citations, blocs de code, tableaux,
// règles horizontales, liens (y compris par référence), images, gras, italique
// et code en ligne. Tout le texte est échappé : aucun HTML brut n'est recopié.

package main

import (
	"fmt"
	"html"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

type heading struct {
	level        int
	text, anchor string
}

type renderer struct {
	linkFn   func(string) string
	refs     map[string]string
	headings []heading
}

func newRenderer(linkFn func(string) string) *renderer {
	if linkFn == nil {
		linkFn = func(u string) string { return u }
	}
	return &renderer{linkFn: linkFn, refs: map[string]string{}}
}

var (
	refDefRe    = regexp.MustCompile(`^\[([^\]]+)\]:\s*(\S+)\s*$`)
	slugDropRe  = regexp.MustCompile(`[^\p{L}\p{N}_\- ]`)
	codeSpanRe  = regexp.MustCompile("`([^`]+)`")
	imageRe     = regexp.MustCompile(`!\[([^\]]*)\]\(([^)\s]+)\)`)
	linkRe      = regexp.MustCompile(`\[([^\]]+)\]\(([^)\s]+)\)`)
	strongRe    = regexp.MustCompile(`\*\*(.+?)\*\*`)
	placeRe     = regexp.MustCompile("\x00(\\d+)\x00")
	headingRe   = regexp.MustCompile(`^(#{1,6})\s+(.*?)\s*#*$`)
	headLinkRe  = regexp.MustCompile(`\[([^\]]+)\](?:\([^)]*\)|\[[^\]]*\])?`)
	hrRe        = regexp.MustCompile(`^(?:-{3,}|\*{3,})$`)
	quoteRe     = regexp.MustCompile(`^\s*>\s?`)
	tableSepRe  = regexp.MustCompile(`^\s*\|[\s:|-]+\|\s*$`)
	listItemRe  = regexp.MustCompile(`^\s*([-*]|\d+\.)\s+(.*)`)
	listStartRe = regexp.MustCompile(`^\s*([-*]|\d+\.)\s+`)
	orderedRe   = regexp.MustCompile(`^\s*\d+\.`)
	paraStopRe  = regexp.MustCompile("^\\s*(```|#{1,6}\\s|>|\\||([-*]|\\d+\\.)\\s+)")
)

// slugify : ancre d'un titre, comme GitHub (minuscules, ponctuation retirée,
// espaces changés en tirets, lettres accentuées gardées).
func slugify(text string) string {
	text = strings.ToLower(strings.TrimSpace(strings.ReplaceAll(text, "`", "")))
	return strings.ReplaceAll(slugDropRe.ReplaceAllString(text, ""), " ", "-")
}

func isWord(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsNumber(r)
}

// emphasis : *texte* → <em>texte</em>, avec les règles de l'expression
// (?<![\w*])\*(?!\s)(.+?)(?<!\s)\*(?![\w*]).
func emphasis(s string) string {
	rs := []rune(s)
	var b strings.Builder
	i := 0
	for i < len(rs) {
		if rs[i] == '*' && (i == 0 || !(isWord(rs[i-1]) || rs[i-1] == '*')) &&
			i+1 < len(rs) && !unicode.IsSpace(rs[i+1]) {
			end := -1
			for j := i + 2; j < len(rs); j++ {
				if rs[j] == '\n' {
					break
				}
				if rs[j] == '*' && !unicode.IsSpace(rs[j-1]) && (j+1 == len(rs) || !(isWord(rs[j+1]) || rs[j+1] == '*')) {
					end = j
					break
				}
			}
			if end > 0 {
				b.WriteString("<em>" + string(rs[i+1:end]) + "</em>")
				i = end + 1
				continue
			}
		}
		b.WriteRune(rs[i])
		i++
	}
	return b.String()
}

// refLinks : [texte][clé] ou [texte] vers une référence définie en bas de page,
// avec les règles de \[([^\]]+)\](?:\[([^\]]*)\])?(?![(:]).
func (r *renderer) refLinks(s string) string {
	var b strings.Builder
	i := 0
	for i < len(s) {
		if s[i] != '[' {
			b.WriteByte(s[i])
			i++
			continue
		}
		close := strings.IndexByte(s[i+1:], ']')
		if close <= 0 {
			b.WriteByte(s[i])
			i++
			continue
		}
		label := s[i+1 : i+1+close]
		after := i + 1 + close + 1 // juste après le premier ]
		next := func(p int) byte {
			if p < len(s) {
				return s[p]
			}
			return 0
		}
		end, key, ok := 0, "", false
		if next(after) == '[' {
			if c2 := strings.IndexByte(s[after+1:], ']'); c2 >= 0 {
				e := after + 1 + c2 + 1
				if n := next(e); n != '(' && n != ':' {
					end, key, ok = e, s[after+1:after+1+c2], true
				}
			}
		}
		if !ok {
			if n := next(after); n != '(' && n != ':' {
				end, key, ok = after, "", true
			}
		}
		if !ok {
			b.WriteByte(s[i])
			i++
			continue
		}
		if key == "" {
			key = label
		}
		if url, found := r.refs[strings.ToLower(key)]; found && url != "" {
			b.WriteString(`<a href="` + esc(r.linkFn(url)) + `">` + label + "</a>")
		} else {
			b.WriteString(s[i:end])
		}
		i = end
	}
	return b.String()
}

func (r *renderer) inline(text string) string {
	var codes []string
	text = codeSpanRe.ReplaceAllStringFunc(text, func(m string) string {
		codes = append(codes, "<code>"+esc(codeSpanRe.FindStringSubmatch(m)[1])+"</code>")
		return "\x00" + strconv.Itoa(len(codes)-1) + "\x00"
	})
	text = escText(text)
	text = imageRe.ReplaceAllStringFunc(text, func(m string) string {
		g := imageRe.FindStringSubmatch(m)
		return `<img src="` + esc(r.linkFn(html.UnescapeString(g[2]))) + `" alt="` + esc(g[1]) + `" loading="lazy">`
	})
	text = linkRe.ReplaceAllStringFunc(text, func(m string) string {
		g := linkRe.FindStringSubmatch(m)
		url := r.linkFn(html.UnescapeString(g[2]))
		attrs := ""
		if strings.HasPrefix(url, "http") && !strings.Contains(url, "frp-manager.gogow.fr") {
			attrs = ` rel="noopener"`
		}
		return `<a href="` + esc(url) + `"` + attrs + ">" + g[1] + "</a>"
	})
	text = r.refLinks(text)
	text = strongRe.ReplaceAllString(text, "<strong>$1</strong>")
	text = emphasis(text)
	return placeRe.ReplaceAllStringFunc(text, func(m string) string {
		n, _ := strconv.Atoi(placeRe.FindStringSubmatch(m)[1])
		return codes[n]
	})
}

// render → (HTML, titres).
func (r *renderer) render(text string) string {
	var lines []string
	for _, line := range strings.Split(lf(text), "\n") {
		if m := refDefRe.FindStringSubmatch(line); m != nil {
			r.refs[strings.ToLower(m[1])] = m[2]
		} else {
			lines = append(lines, line)
		}
	}
	return r.blocks(lines)
}

func hasIndent(line string) bool {
	return strings.HasPrefix(line, "  ") || strings.HasPrefix(line, "\t")
}

func (r *renderer) blocks(lines []string) string {
	var out []string
	i := 0
	for i < len(lines) {
		line := lines[i]
		stripped := strings.TrimSpace(line)
		switch {
		case stripped == "":
			i++
			continue
		case strings.HasPrefix(stripped, "```"):
			lang := strings.TrimSpace(stripped[3:])
			i++
			var code []string
			for i < len(lines) && !strings.HasPrefix(strings.TrimSpace(lines[i]), "```") {
				code = append(code, lines[i])
				i++
			}
			i++
			cls := ""
			if lang != "" {
				cls = ` class="language-` + esc(lang) + `"`
			}
			out = append(out, `<div class="code"><pre><code`+cls+">"+esc(strings.Join(code, "\n"))+"</code></pre></div>")
			continue
		}
		if m := headingRe.FindStringSubmatch(stripped); m != nil {
			// Titre déjà lien d'ancre : ses liens ([0.0.51], [texte](url)) deviennent du texte
			level := len(m[1])
			title := headLinkRe.ReplaceAllString(m[2], "$1")
			anchor := slugify(title)
			r.headings = append(r.headings, heading{level, title, anchor})
			inner := r.inline(title)
			if level == 1 {
				out = append(out, "<h1>"+inner+"</h1>")
			} else {
				out = append(out, fmt.Sprintf(`<h%d id="%s"><a class="anchor" href="#%s">%s</a></h%d>`, level, esc(anchor), esc(anchor), inner, level))
			}
			i++
			continue
		}
		if hrRe.MatchString(stripped) {
			out = append(out, "<hr>")
			i++
			continue
		}
		if strings.HasPrefix(stripped, ">") {
			var quote []string
			for i < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[i]), ">") {
				quote = append(quote, quoteRe.ReplaceAllString(lines[i], ""))
				i++
			}
			out = append(out, "<blockquote>"+newRenderer(r.linkFn).blocks(quote)+"</blockquote>")
			continue
		}
		if strings.HasPrefix(stripped, "|") && i+1 < len(lines) && tableSepRe.MatchString(lines[i+1]) {
			var rows []string
			for i < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[i]), "|") {
				rows = append(rows, lines[i])
				i++
			}
			out = append(out, r.table(rows))
			continue
		}
		if listStartRe.MatchString(line) {
			var block string
			i, block = r.listBlock(lines, i)
			out = append(out, block)
			continue
		}
		var para []string
		for i < len(lines) && strings.TrimSpace(lines[i]) != "" && !paraStopRe.MatchString(lines[i]) {
			para = append(para, strings.TrimSpace(lines[i]))
			i++
		}
		out = append(out, "<p>"+r.inline(strings.Join(para, " "))+"</p>")
	}
	return strings.Join(out, "\n")
}

func (r *renderer) listBlock(lines []string, i int) (int, string) {
	ordered := orderedRe.MatchString(lines[i])
	var items [][]string
	for i < len(lines) {
		m := listItemRe.FindStringSubmatch(lines[i])
		switch {
		case m != nil && orderedRe.MatchString(lines[i]) == ordered:
			items = append(items, []string{m[2]})
			i++
		case len(items) > 0 && hasIndent(lines[i]) && strings.TrimSpace(lines[i]) != "":
			items[len(items)-1] = append(items[len(items)-1], strings.TrimSpace(lines[i])) // suite de l'élément
			i++
		case len(items) > 0 && strings.TrimSpace(lines[i]) == "" && i+1 < len(lines) && hasIndent(lines[i+1]):
			i++
		default:
			goto done
		}
	}
done:
	tag := "ul"
	if ordered {
		tag = "ol"
	}
	var b strings.Builder
	for _, parts := range items {
		b.WriteString("<li>" + r.item(parts) + "</li>")
	}
	return i, "<" + tag + ">" + b.String() + "</" + tag + ">"
}

func (r *renderer) item(parts []string) string {
	for _, p := range parts {
		if strings.HasPrefix(p, "```") {
			return r.inline(parts[0]) + newRenderer(r.linkFn).blocks(parts[1:])
		}
	}
	return r.inline(strings.Join(parts, " "))
}

func (r *renderer) table(rows []string) string {
	cells := func(row string) []string {
		parts := strings.Split(strings.Trim(strings.TrimSpace(row), "|"), "|")
		for i := range parts {
			parts[i] = strings.TrimSpace(parts[i])
		}
		return parts
	}
	head, alignRow := cells(rows[0]), cells(rows[1])
	aligns := make([]string, len(alignRow))
	for n, a := range alignRow {
		switch {
		case strings.HasPrefix(a, ":") && strings.HasSuffix(a, ":"):
			aligns[n] = "center"
		case strings.HasSuffix(a, ":"):
			aligns[n] = "right"
		}
	}
	td := func(tag, text string, n int) string {
		style := ""
		if n < len(aligns) && aligns[n] != "" {
			style = ` style="text-align:` + aligns[n] + `"`
		}
		return "<" + tag + style + ">" + r.inline(text) + "</" + tag + ">"
	}
	var thead, tbody strings.Builder
	for n, c := range head {
		thead.WriteString(td("th", c, n))
	}
	for _, row := range rows[2:] {
		tbody.WriteString("<tr>")
		for n, c := range cells(row) {
			tbody.WriteString(td("td", c, n))
		}
		tbody.WriteString("</tr>")
	}
	return `<div class="table"><table><thead><tr>` + thead.String() + "</tr></thead><tbody>" + tbody.String() + "</tbody></table></div>"
}

// renderMarkdown → (HTML, titres).
func renderMarkdown(text string, linkFn func(string) string) (string, []heading) {
	r := newRenderer(linkFn)
	return r.render(text), r.headings
}
