"""Rendu Markdown minimal pour le site (bibliothèque standard uniquement).

Couvre ce qu'utilisent les pages du dépôt : titres (avec ancres façon GitHub),
paragraphes, listes à puces et numérotées, citations, blocs de code, tableaux,
règles horizontales, liens (y compris par référence), images, gras, italique
et code en ligne. Tout le texte est échappé : aucun HTML brut n'est recopié.
"""

import html
import re

_REF_DEF = re.compile(r"^\[([^\]]+)\]:\s*(\S+)\s*$")


def slugify(text):
    """Ancre d'un titre, comme GitHub : minuscules, ponctuation retirée,
    espaces changés en tirets (les lettres accentuées restent)."""
    text = re.sub(r"`", "", text).strip().lower()
    text = re.sub(r"[^\w\- ]", "", text, flags=re.UNICODE)
    return text.replace(" ", "-")


class Renderer:
    """link_fn(url) → url réécrite (liens entre documents du dépôt)."""

    def __init__(self, link_fn=None):
        self.link_fn = link_fn or (lambda u: u)
        self.refs = {}
        self.headings = []            # [(niveau, texte, ancre)]

    # ── En ligne ───────────────────────────────────────────────────────────
    def inline(self, text):
        codes = []

        def keep_code(m):
            codes.append(f"<code>{html.escape(m.group(1))}</code>")
            return f"\x00{len(codes) - 1}\x00"

        text = re.sub(r"`([^`]+)`", keep_code, text)
        text = html.escape(text, quote=False)

        def image(m):
            return (f'<img src="{html.escape(self.link_fn(html.unescape(m.group(2))))}" '
                    f'alt="{html.escape(m.group(1))}" loading="lazy">')

        def link(m):
            url = self.link_fn(html.unescape(m.group(2)))
            ext = url.startswith("http") and "frp-manager.gogow.fr" not in url
            attrs = ' rel="noopener"' if ext else ""
            return f'<a href="{html.escape(url)}"{attrs}>{m.group(1)}</a>'

        def ref_link(m):
            label, key = m.group(1), (m.group(2) or m.group(1))
            url = self.refs.get(key.lower())
            if not url:
                return m.group(0)
            return f'<a href="{html.escape(self.link_fn(url))}">{label}</a>'

        text = re.sub(r"!\[([^\]]*)\]\(([^)\s]+)\)", image, text)
        text = re.sub(r"\[([^\]]+)\]\(([^)\s]+)\)", link, text)
        text = re.sub(r"\[([^\]]+)\](?:\[([^\]]*)\])?(?![(:])", ref_link, text)
        text = re.sub(r"\*\*(.+?)\*\*", r"<strong>\1</strong>", text)
        text = re.sub(r"(?<![\w*])\*(?!\s)(.+?)(?<!\s)\*(?![\w*])", r"<em>\1</em>", text)
        return re.sub(r"\x00(\d+)\x00", lambda m: codes[int(m.group(1))], text)

    # ── Blocs ──────────────────────────────────────────────────────────────
    def render(self, text):
        lines = []
        for line in text.replace("\r\n", "\n").split("\n"):
            m = _REF_DEF.match(line)
            if m:
                self.refs[m.group(1).lower()] = m.group(2)
            else:
                lines.append(line)
        return self.blocks(lines)

    def blocks(self, lines):
        out, i = [], 0
        while i < len(lines):
            line = lines[i]
            stripped = line.strip()
            if not stripped:
                i += 1
                continue
            if stripped.startswith("```"):
                lang = stripped[3:].strip()
                i += 1
                code = []
                while i < len(lines) and not lines[i].strip().startswith("```"):
                    code.append(lines[i])
                    i += 1
                i += 1
                cls = f' class="language-{html.escape(lang)}"' if lang else ""
                out.append(f'<div class="code"><pre><code{cls}>{html.escape(chr(10).join(code))}</code></pre></div>')
                continue
            m = re.match(r"(#{1,6})\s+(.*?)\s*#*$", stripped)
            if m:
                # Titre déjà lien d'ancre : ses liens ([0.0.51], [texte](url)) deviennent du texte
                level = len(m.group(1))
                title = re.sub(r"\[([^\]]+)\](?:\([^)]*\)|\[[^\]]*\])?", r"\1", m.group(2))
                anchor = slugify(title)
                self.headings.append((level, title, anchor))
                inner = self.inline(title)
                if level == 1:
                    out.append(f"<h1>{inner}</h1>")
                else:
                    out.append(f'<h{level} id="{html.escape(anchor)}"><a class="anchor" href="#{html.escape(anchor)}">'
                               f"{inner}</a></h{level}>")
                i += 1
                continue
            if re.fullmatch(r"-{3,}|\*{3,}", stripped):
                out.append("<hr>")
                i += 1
                continue
            if stripped.startswith(">"):
                quote = []
                while i < len(lines) and lines[i].strip().startswith(">"):
                    quote.append(re.sub(r"^\s*>\s?", "", lines[i]))
                    i += 1
                out.append(f"<blockquote>{Renderer(self.link_fn).blocks(quote)}</blockquote>")
                continue
            if stripped.startswith("|") and i + 1 < len(lines) and re.match(r"^\s*\|[\s:|-]+\|\s*$", lines[i + 1]):
                rows = []
                while i < len(lines) and lines[i].strip().startswith("|"):
                    rows.append(lines[i])
                    i += 1
                out.append(self.table(rows))
                continue
            if re.match(r"^\s*([-*]|\d+\.)\s+", line):
                i = self.list_block(lines, i, out)
                continue
            para = []
            while i < len(lines) and lines[i].strip() and not re.match(
                    r"^\s*(```|#{1,6}\s|>|\||([-*]|\d+\.)\s+)", lines[i]):
                para.append(lines[i].strip())
                i += 1
            out.append(f"<p>{self.inline(' '.join(para))}</p>")
        return "\n".join(out)

    def list_block(self, lines, i, out):
        ordered = bool(re.match(r"^\s*\d+\.", lines[i]))
        items = []
        while i < len(lines):
            m = re.match(r"^\s*([-*]|\d+\.)\s+(.*)", lines[i])
            if m and bool(re.match(r"^\s*\d+\.", lines[i])) == ordered:
                items.append([m.group(2)])
                i += 1
            elif items and lines[i].startswith(("  ", "\t")) and lines[i].strip():
                items[-1].append(lines[i].strip())          # suite de l'élément
                i += 1
            elif items and not lines[i].strip() and i + 1 < len(lines) and lines[i + 1].startswith(("  ", "\t")):
                i += 1
            else:
                break
        tag = "ol" if ordered else "ul"
        body = "".join(f"<li>{self.item(parts)}</li>" for parts in items)
        out.append(f"<{tag}>{body}</{tag}>")
        return i

    def item(self, parts):
        if any(p.startswith("```") for p in parts):
            first, rest = parts[0], parts[1:]
            return self.inline(first) + Renderer(self.link_fn).blocks(rest)
        return self.inline(" ".join(parts))

    def table(self, rows):
        def cells(row):
            return [c.strip() for c in row.strip().strip("|").split("|")]
        head, aligns_row, body = cells(rows[0]), cells(rows[1]), [cells(r) for r in rows[2:]]
        aligns = ["center" if a.startswith(":") and a.endswith(":") else "right" if a.endswith(":") else ""
                  for a in aligns_row]

        def td(tag, text, n):
            style = f' style="text-align:{aligns[n]}"' if n < len(aligns) and aligns[n] else ""
            return f"<{tag}{style}>{self.inline(text)}</{tag}>"
        thead = "".join(td("th", c, n) for n, c in enumerate(head))
        tbody = "".join("<tr>" + "".join(td("td", c, n) for n, c in enumerate(r)) + "</tr>" for r in body)
        return f'<div class="table"><table><thead><tr>{thead}</tr></thead><tbody>{tbody}</tbody></table></div>'


def render(text, link_fn=None):
    """→ (html, titres [(niveau, texte, ancre)])"""
    r = Renderer(link_fn)
    body = r.render(text)
    return body, r.headings
