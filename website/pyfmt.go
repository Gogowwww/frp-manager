// Formats de sortie stables : échappement HTML, JSON à clés ordonnées, tri des
// chemins. Les pages produites restent identiques d'une construction à l'autre
// (et à celles des anciennes versions du générateur).

package main

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

// esc échappe un texte pour du HTML (contenu et attributs) : & < > " '.
func esc(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&#x27;").Replace(s)
}

// escText échappe un texte hors attribut : & < > seulement.
func escText(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

// ── JSON à clés ordonnées ───────────────────────────────────────────────────

// KV : une clé et sa valeur ; OM : objet JSON dont l'ordre des clés est gardé.
type KV struct {
	K string
	V any
}
type OM []KV

// jsonText : JSON compact (separators=(",", ":")) ou avec espaces (", ", ": "),
// caractères non ASCII écrits tels quels.
func jsonText(v any, compact bool) string {
	var b strings.Builder
	writeJSON(&b, v, compact)
	return b.String()
}

func writeJSON(b *strings.Builder, v any, compact bool) {
	item, kv := ", ", ": "
	if compact {
		item, kv = ",", ":"
	}
	switch x := v.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		b.WriteString(strconv.FormatBool(x))
	case int:
		b.WriteString(strconv.Itoa(x))
	case int64:
		b.WriteString(strconv.FormatInt(x, 10))
	case float64:
		if x == math.Trunc(x) {
			b.WriteString(strconv.FormatFloat(x, 'f', 1, 64))
		} else {
			b.WriteString(strconv.FormatFloat(x, 'g', -1, 64))
		}
	case string:
		writeJSONString(b, x)
	case OM:
		b.WriteByte('{')
		for i, e := range x {
			if i > 0 {
				b.WriteString(item)
			}
			writeJSONString(b, e.K)
			b.WriteString(kv)
			writeJSON(b, e.V, compact)
		}
		b.WriteByte('}')
	case []any:
		b.WriteByte('[')
		for i, e := range x {
			if i > 0 {
				b.WriteString(item)
			}
			writeJSON(b, e, compact)
		}
		b.WriteByte(']')
	case []string:
		b.WriteByte('[')
		for i, e := range x {
			if i > 0 {
				b.WriteString(item)
			}
			writeJSONString(b, e)
		}
		b.WriteByte(']')
	default:
		panic(fmt.Sprintf("jsonText : type %T", v))
	}
}

func writeJSONString(b *strings.Builder, s string) {
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		default:
			if r < 0x20 {
				fmt.Fprintf(b, `\u%04x`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
}

// ── Chemins ─────────────────────────────────────────────────────────────────

// sortPaths trie des chemins « a/b/c » segment par segment (comme pathlib).
func sortPaths(paths []string) {
	sort.Slice(paths, func(i, j int) bool {
		a, b := strings.Split(paths[i], "/"), strings.Split(paths[j], "/")
		for k := 0; k < len(a) && k < len(b); k++ {
			if a[k] != b[k] {
				return a[k] < b[k]
			}
		}
		return len(a) < len(b)
	})
}

// lf : fins de ligne Unix (fichiers texte lus depuis un dépôt cloné sous Windows).
func lf(s string) string { return strings.ReplaceAll(s, "\r\n", "\n") }
