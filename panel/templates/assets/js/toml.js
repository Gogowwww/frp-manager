// ── TOML frp : lecture / écriture ─────────────────────────────────────────
// Pas un parseur TOML générique : il couvre la forme des configs frp
// (clés racine, [sections], [[proxies]] / [[visitors]] et leurs sous-tables).
// Chaque tunnel garde la valeur brute de ses clés pour réécrire à l'identique
// celles que l'interface ne gère pas (plugin, healthCheck, subdomain…).

// Un bloc peut être précédé de la ligne « range » d'un modèle de plage de ports
// (voir generateProxy) : elle part avec lui, pas avec la partie connexion.
// A block may be preceded by the "range" line of a port-range template (see
// generateProxy): it goes with the block, not with the connection part.
// Deux formes : parseNumberRangePair (deux plages, $v.First et $v.Second) ou
// parseNumberRange (une seule plage, $v est le port).
// Two forms: parseNumberRangePair (two ranges, $v.First and $v.Second) or
// parseNumberRange (a single range, $v is the port).
const RANGE_LINE = String.raw`\{\{-?\s*range\s+\$_,\s*\$v\s*:=\s*(?:parseNumberRangePair\s+"([^"]*)"\s+"([^"]*)"|parseNumberRange\s+"([^"]*)")\s*-?\}\}`;
// Port de la boucle dans le bloc : « {{ $v }} », ou « {{ $v.First }} » / « {{ $v.Second }} »
// Loop port inside the block: "{{ $v }}", or "{{ $v.First }}" / "{{ $v.Second }}"
const LOOP_FIRST = String.raw`\{\{\s*\$v(?:\.First)?\s*\}\}`;
const LOOP_SECOND = String.raw`\{\{\s*\$v(?:\.Second)?\s*\}\}`;
// Entre la ligne « range » et l'en-tête : commentaire de fin de ligne, lignes
// vides ou commentées, espaces dans les crochets
// Between the "range" line and the header: end-of-line comment, blank or
// commented lines, spaces inside the brackets
const BLOCK_RE = new RegExp(String.raw`^[ \t]*(?:${RANGE_LINE}[ \t]*(?:#[^\r\n]*)?\r?\n(?:[ \t]*(?:#[^\r\n]*)?\r?\n)*[ \t]*)?\[\[[ \t]*(proxies|visitors)[ \t]*\]\]`, 'm');
const RANGE_RE = new RegExp(`^${RANGE_LINE}$`);
const END_RE = /^\{\{-?\s*end\s*-?\}\}$/;

/** Coupe un commentaire de fin de ligne situé hors chaîne. */
function stripComment(value) {
  return splitComment(value)[0];
}

/** [valeur, commentaire de fin de ligne] / [value, end-of-line comment] */
function splitComment(value) {
  let quote = null;
  for (let i = 0; i < value.length; i += 1) {
    const c = value[i];
    if (quote) {
      if (c === '\\' && quote === '"') i += 1;
      else if (c === quote) quote = null;
    } else if (c === '"' || c === "'") {
      quote = c;
    } else if (c === '#') {
      return [value.slice(0, i).trim(), value.slice(i).trim()];
    }
  }
  return [value.trim(), ''];
}

/** Position du « = » d'une ligne « clé = valeur », hors guillemets (une clé entre
 *  guillemets peut en contenir : "a=b" = 1), sinon -1.
 *  Position of the "=" of a "key = value" line, outside quotes (a quoted key may
 *  contain one: "a=b" = 1), otherwise -1. */
function keyEnd(line) {
  let quote = null;
  for (let i = 0; i < line.length; i += 1) {
    const c = line[i];
    if (quote) {
      if (c === '\\' && quote === '"') i += 1;
      else if (c === quote) quote = null;
    } else if (c === '"' || c === "'") quote = c;
    else if (c === '#') return -1;
    else if (c === '=') return i;
  }
  return -1;
}

/**
 * Remet les commentaires d'un bloc (notes : clé → { before, eol }) autour de
 * ses lignes « clé = valeur ».
 * Puts the comments of a block (notes: key → { before, eol }) back around its
 * "key = value" lines.
 */
function annotate(lines, notes) {
  if (!notes) return lines;
  const out = [];
  for (const line of lines) {
    const eq = keyEnd(line);
    const isHead = line === '[[proxies]]' || line === '[[visitors]]';
    const n = isHead ? notes['[[]]'] : (eq > 0 && !/^[{[]/.test(line) ? notes[line.slice(0, eq).trim()] : null);
    if (n && n.before) out.push(...n.before);
    out.push(n && n.eol ? `${line} ${n.eol}` : line);
  }
  return out;
}

/** Échappements d'une chaîne TOML entre guillemets doubles (\n, \t, \", \\, \uXXXX…) ;
 *  dans une chaîne multi-lignes, « \ » en fin de ligne retire le saut de ligne et
 *  les blancs qui suivent.
 *  Escapes of a double-quoted TOML string (\n, \t, \", \\, \uXXXX…); in a
 *  multi-line string, "\" at the end of a line removes the line break and the
 *  whitespace that follows. */
function unescapeBasic(s) {
  return s.replace(/\\(?:([btnfr"\\])|u([0-9a-fA-F]{4})|U([0-9a-fA-F]{8})|[ \t]*\n\s*)/g, (m, c, u4, u8) => {
    if (c) return { b: '\b', t: '\t', n: '\n', f: '\f', r: '\r' }[c] || c;
    if (u4 || u8) return String.fromCodePoint(parseInt(u4 || u8, 16));
    return '';
  });
}

/** Valeur d'une chaîne multi-lignes brute (""" ou ''', lignes jointes par \n), ou
 *  null si raw n'en est pas une : le saut de ligne qui suit l'ouverture ne compte
 *  pas, la fermeture peut être suivie d'un ou deux guillemets du contenu.
 *  Value of a raw multi-line string (""" or ''', lines joined with \n), or null
 *  if raw is not one: the line break right after the opening does not count, the
 *  closing may be followed by one or two quotes of the content. */
function multilineValue(raw) {
  const d = raw.slice(0, 3);
  if (d !== '"""' && d !== "'''") return null;
  const body = raw.slice(3);
  for (let i = 0; i + 3 <= body.length; i += 1) {
    if (d === '"""' && body[i] === '\\') { i += 1; continue; }
    if (!body.startsWith(d, i)) continue;
    let end = i;
    while (end - i < 2 && body[end + 3] === d[0]) end += 1;
    const text = body.slice(0, end).replace(/^\n/, '');
    return d === '"""' ? unescapeBasic(text) : text;
  }
  return null;
}

function unquote(raw) {
  const ml = multilineValue(raw);
  if (ml !== null) return ml;
  if (raw.length >= 2 && raw.startsWith('"') && raw.endsWith('"')) return unescapeBasic(raw.slice(1, -1));
  if (raw.length >= 2 && raw.startsWith("'") && raw.endsWith("'")) return raw.slice(1, -1);
  return raw;
}

// ── Normalisation du texte avant lecture ──────────────────────────────────
// BOM UTF-8 retiré (frpc le refuse), fins de ligne CR seul ramenées à LF, forme
// en ligne « proxies = [ { … } ] » convertie en blocs [[proxies]].
// Text normalization before reading: UTF-8 BOM removed (frpc rejects it), lone
// CR line endings turned into LF, inline form "proxies = [ { … } ]" converted
// into [[proxies]] blocks.

/** Fin du tableau ou de la table commençant en start ('[' ou '{'), sinon -1. */
function scanEnd(s, start) {
  let depth = 0;
  let quote = null;
  for (let i = start; i < s.length; i += 1) {
    const c = s[i];
    if (quote) {
      if (c === '\\' && quote === '"') i += 1;
      else if (c === quote) quote = null;
    } else if (c === '"' || c === "'") quote = c;
    else if (c === '#') { while (i < s.length && s[i] !== '\n') i += 1; }
    else if (c === '[' || c === '{') depth += 1;
    else if (c === ']' || c === '}') { depth -= 1; if (depth === 0) return i + 1; }
  }
  return -1;
}

/**
 * Coupe sur les virgules du premier niveau (hors chaînes et commentaires).
 * Renvoie { text, notes } : les commentaires lus dans chaque élément.
 * Splits on the top-level commas (outside strings and comments). Returns
 * { text, notes }: the comments read inside each element.
 */
function splitTop(s) {
  const out = [];
  let depth = 0;
  let quote = null;
  let cur = '';
  let notes = [];
  const push = () => { out.push({ text: cur.trim(), notes }); cur = ''; notes = []; };
  for (let i = 0; i < s.length; i += 1) {
    const c = s[i];
    if (quote) {
      cur += c;
      if (c === '\\' && quote === '"') { i += 1; cur += s[i] || ''; } else if (c === quote) quote = null;
    } else if (c === '"' || c === "'") { quote = c; cur += c; }
    else if (c === '#') {
      const from = i;
      while (i < s.length && s[i] !== '\n') i += 1;
      notes.push(s.slice(from, i).trim());
    }
    else if (c === '[' || c === '{') { depth += 1; cur += c; }
    else if (c === ']' || c === '}') { depth -= 1; cur += c; }
    else if (c === ',' && depth === 0) push();
    else cur += c;
  }
  push();
  return out.filter((x) => x.text || x.notes.length);
}

/** Lignes « clé = valeur » d'une table en ligne ; les sous-tables deviennent « a.b = … ». */
function inlineLines(body, prefix = '') {
  const lines = [];
  for (const { text: part } of splitTop(body)) {
    const eq = keyEnd(part);
    if (eq < 0) continue;
    const key = part.slice(0, eq).trim().replace(/^(["'])(.*)\1$/, '$2');
    const val = part.slice(eq + 1).trim();
    if (val.startsWith('{') && val.endsWith('}')) lines.push(...inlineLines(val.slice(1, -1), `${prefix}${key}.`));
    else lines.push(`${prefix}${key} = ${val.replace(/\s*\n\s*/g, ' ')}`);
  }
  return lines;
}

/** Lignes (début) des chaînes sur plusieurs lignes : on n'y cherche rien. */
/** Start offsets of the lines inside multi-line strings: nothing is looked for there. */
function multilineStringLines(text) {
  const inside = new Set();
  let delim = '';
  let off = 0;
  for (const line of text.split('\n')) {
    if (delim) {
      inside.add(off);
      if (line.includes(delim)) delim = '';
    } else if (!/^\s*#/.test(line)) {
      // Une ligne de commentaire n'ouvre pas de chaîne
      // A comment line opens no string
      const eq = keyEnd(line);
      const v = eq >= 0 ? line.slice(eq + 1).trim() : '';
      const d = v.startsWith('"""') ? '"""' : v.startsWith("'''") ? "'''" : '';
      if (d && !v.slice(3).includes(d)) delim = d;
    }
    off += line.length + 1;
  }
  return inside;
}

/** Premier en-tête [[proxies]] / [[visitors]] (avec sa ligne « range ») hors
 *  chaîne multi-lignes, comme BLOCK_RE.exec, sinon null.
 *  First [[proxies]] / [[visitors]] header (with its "range" line) outside
 *  multi-line strings, like BLOCK_RE.exec, otherwise null. */
function findBlock(text) {
  const re = new RegExp(BLOCK_RE.source, 'gm');
  let skip = null;
  let m;
  while ((m = re.exec(text))) {
    if (!skip) skip = multilineStringLines(text);
    // En-tête (ou ligne « range ») dans une chaîne : on cherche à la ligne suivante
    // Header (or "range" line) inside a string: the search goes on at the next line
    const head = text.lastIndexOf('\n', m.index + m[0].length - 1) + 1;
    if (!skip.has(m.index) && !skip.has(head)) return m;
    re.lastIndex = m.index + 1;
  }
  return null;
}

/** « proxies = [ { … }, { … } ] » → blocs [[proxies]] ajoutés à la fin du texte. */
function expandInline(text) {
  const re = /^[ \t]*["']?(proxies|visitors)["']?[ \t]*=[ \t]*\[/gm;
  const extra = [];
  let result = '';
  let pos = 0;
  let found = false;
  let skip = null;
  let m;
  while ((m = re.exec(text))) {
    if (!skip) skip = multilineStringLines(text);
    if (skip.has(m.index)) continue;
    const open = m.index + m[0].length - 1;
    const end = scanEnd(text, open);
    if (end < 0) continue;
    found = true;
    for (const el of splitTop(text.slice(open + 1, end - 1))) {
      // Commentaire sans élément (après la dernière virgule) : gardé tel quel
      // Comment with no element (after the last comma): kept as is
      if (!el.text) { extra.push(el.notes.join('\n')); continue; }
      if (!(el.text.startsWith('{') && el.text.endsWith('}'))) continue;
      extra.push(`${el.notes.join('\n')}${el.notes.length ? '\n' : ''}[[${m[1]}]]\n${inlineLines(el.text.slice(1, -1)).join('\n')}`);
    }
    result += text.slice(pos, m.index);
    pos = end;
    re.lastIndex = end;
  }
  if (!found) return text;
  const rest = `${(result + text.slice(pos)).replace(/\s+$/, '')}\n`;
  return extra.length ? `${rest}\n${extra.join('\n\n')}\n` : rest;
}

function prep(text) {
  return expandInline(String(text || '').replace(/^\uFEFF/, '').replace(/\r(?!\n)/g, '\n'));
}

// Chaîne TOML sur une ligne : les sauts de ligne et autres caractères de contrôle
// sont échappés (une valeur lue dans une chaîne multi-lignes en a)
// One-line TOML string: line breaks and other control characters are escaped (a
// value read from a multi-line string has some)
export function str(value) {
  return `"${String(value).replace(/[\\"\u0000-\u001f\u007f]/g, (c) => ({
    '\\': '\\\\', '"': '\\"', '\b': '\\b', '\t': '\\t', '\n': '\\n', '\f': '\\f', '\r': '\\r',
  }[c] || `\\u${c.charCodeAt(0).toString(16).padStart(4, '0')}`))}"`;
}

/**
 * → { values: { 'auth.token': '…', … }, proxies: [Block], visitors: [Block] }
 *   Block = { values: {clé: valeur}, raw: {clé: brut}, order: [clés] }
 */
export function parseToml(text) {
  const out = { values: {}, proxies: [], visitors: [] };
  let block = null;
  let prefix = '';
  let section = '';
  let pendingRange = null;
  // Commentaires de ligne entière entre deux blocs : ils partent avec le bloc
  // qui suit (ceux d'avant le premier bloc restent dans la partie connexion,
  // ceux de la fin dans out.trailing). Ceux du milieu d'un bloc sont perdus.
  // Whole-line comments between two blocks: they go with the next block
  // (those before the first block stay in the connection part, those at the
  // end in out.trailing). Those in the middle of a block are lost.
  let comments = [];
  let seenBlock = false;
  // Une ligne de contenu a déjà été lue hors d'un bloc : les commentaires qui
  // suivent, juste avant le premier bloc, partent avec lui
  // A content line was already read outside a block: the comments that follow,
  // right before the first block, go with it
  let anyContent = false;

  const allLines = prep(text).split(/\r?\n/);
  for (let li = 0; li < allLines.length; li += 1) {
    const rawLine = allLines[li];
    const line = rawLine.trim();
    if (line.startsWith('#') && (seenBlock || anyContent)) comments.push(line);
    if (!line || line.startsWith('#')) continue;

    // Modèle de plage de ports : « range » devant le bloc, « end » derrière
    // Port-range template: "range" in front of the block, "end" behind it
    const [lineCode, lineNote] = line.startsWith('{{') ? splitComment(line) : [line, ''];
    const rm = RANGE_RE.exec(lineCode);
    if ((rm || END_RE.test(lineCode)) && lineNote) comments.push(lineNote);
    // Une seule plage (parseNumberRange) : pas de second côté
    // A single range (parseNumberRange): no second side
    if (rm) { pendingRange = rm[3] !== undefined ? { local: rm[3], remote: '' } : { local: rm[1], remote: rm[2] }; continue; }
    if (END_RE.test(lineCode)) continue;

    // En-tête de bloc, avec un éventuel commentaire de fin de ligne et des
    // espaces dans les crochets
    // Block header, with a possible end-of-line comment and spaces inside the
    // brackets
    const [headRaw, headEol] = line.startsWith('[[') ? splitComment(line) : [line, ''];
    const head = line.startsWith('[[') ? headRaw.replace(/\s+/g, '') : headRaw;
    if (head === '[[proxies]]' || head === '[[visitors]]') {
      block = { values: {}, raw: {}, order: [], comments, notes: {} };
      if (headEol) block.notes['[[]]'] = { before: [], eol: headEol };
      comments = [];
      seenBlock = true;
      if (pendingRange) block.range = pendingRange;
      pendingRange = null;
      out[head === '[[proxies]]' ? 'proxies' : 'visitors'].push(block);
      prefix = '';
      continue;
    }
    // Ligne de contenu : les commentaires lus avant sont dans un bloc
    // Content line: the comments read before are inside a block
    const pending = comments;
    comments = [];

    if (line.startsWith('[')) {
      if (!(block && /^\[+\s*(?:proxies|visitors)\s*\./.test(line))) anyContent = true;
      const name = line.replace(/^\[+|\]+.*$/g, '').trim();
      const sub = name.match(/^(?:proxies|visitors)\s*\.\s*(.+)$/);
      // Sous-table d'un tunnel : ses commentaires passent à la première clé
      // Sub-table of a tunnel: its comments move to the first key
      if (block && sub) {
        prefix = `${sub[1]}.`;
        const subEol = splitComment(line)[1];
        comments = subEol ? [...pending, subEol] : pending;
        continue;
      }
      block = null;
      prefix = '';
      section = name;
      continue;
    }

    const eq = keyEnd(line);
    if (eq < 0) continue;
    // Clé entre guillemets : "name" = "a" se lit comme name = "a"
    // Quoted key: "name" = "a" reads as name = "a"
    const key = line.slice(0, eq).trim().replace(/^(["'])([\w.-]+)\1$/, '$2');
    let [raw, eol] = splitComment(line.slice(eq + 1));
    // Chaîne sur plusieurs lignes (""" ou ''') : gardée telle quelle jusqu'au
    // délimiteur de fin
    // Multi-line string (""" or '''): kept as is up to the closing delimiter
    const rest0 = line.slice(eq + 1).trim();
    const delim = rest0.startsWith('"""') ? '"""' : rest0.startsWith("'''") ? "'''" : '';
    if (delim && !rest0.slice(3).includes(delim)) {
      const parts = [rest0];
      while (li + 1 < allLines.length) {
        li += 1;
        parts.push(allLines[li]);
        if (allLines[li].includes(delim)) break;
      }
      raw = parts.join('\n');
      eol = '';
    }
    // Commentaires à l'intérieur d'un tableau : remontés au-dessus de la clé
    // Comments inside an array: moved above the key
    const inner = [];
    // Tableau sur plusieurs lignes : toutes les lignes jusqu'au crochet fermant
    // Multi-line array: all the lines up to the closing bracket
    if (raw.startsWith('[') && bracketDepth(raw) > 0) {
      const parts = [raw];
      while (li + 1 < allLines.length && bracketDepth(parts.join('\n')) > 0) {
        li += 1;
        const [more, note] = splitComment(allLines[li]);
        if (more) parts.push(more);
        if (note) inner.push(note);
      }
      raw = parts.join('\n');
      // Le commentaire d'ouverture remonte avec les autres : stable dès la
      // première sauvegarde
      // The opening comment moves up with the others: stable from the first save
      if (eol) { inner.unshift(eol); eol = ''; }
    }
    const value = unquote(raw);

    if (block) {
      const k = prefix + key;
      if (!(k in block.raw)) block.order.push(k);
      block.values[k] = value;
      block.raw[k] = raw;
      // Commentaires du bloc : ceux d'avant la clé et celui de fin de ligne
      // Block comments: those before the key and the end-of-line one
      if (pending.length || inner.length || eol) block.notes[k] = { before: [...pending, ...inner], eol };
    } else {
      anyContent = true;
      out.values[section ? `${section}.${key}` : key] = value;
    }
  }
  out.trailing = comments;
  return out;
}

/** Partie « connexion » de la config, sans les [[proxies]] / [[visitors]]. */
export function stripBlocks(text) {
  // BOM UTF-8 au début : frpc le refuse, l'enregistrement le retire
  // UTF-8 BOM at the start: frpc rejects it, saving removes it
  text = prep(text);
  const m = findBlock(text);
  const base = (m ? text.slice(0, m.index) : text).trimEnd();
  return m ? dropTrailingComments(base) : base;
}

/**
 * Commentaires collés au premier bloc : ils partent avec lui (sauf s'il n'y
 * a que des commentaires avant, comme l'en-tête du fichier).
 * Comments stuck to the first block: they go with it (unless there are only
 * comments before, like the file header).
 */
function dropTrailingComments(base) {
  const lines = base.split(/\r?\n/);
  if (!lines.some((l) => l.trim() && !l.trim().startsWith('#'))) return base;
  // Une ligne en « # » à l'intérieur d'une chaîne multi-lignes n'est pas un commentaire
  // A "#" line inside a multi-line string is not a comment
  const inside = multilineStringLines(base);
  const starts = [];
  let off = 0;
  for (const l of base.split('\n')) { starts.push(off); off += l.length + 1; }
  const isNote = (i) => !inside.has(starts[i]) && (!lines[i].trim() || lines[i].trim().startsWith('#'));
  while (lines.length && isNote(lines.length - 1)) lines.pop();
  return lines.join(/\r\n/.test(base) ? '\r\n' : '\n');
}

/** Profondeur de crochets ouverts hors chaîne / open brackets depth outside strings */
function bracketDepth(s) {
  let depth = 0;
  let quote = null;
  for (let i = 0; i < s.length; i += 1) {
    const c = s[i];
    if (quote) {
      if (c === '\\' && quote === '"') i += 1;
      else if (c === quote) quote = null;
    } else if (c === '"' || c === "'") quote = c;
    else if (c === '[') depth += 1;
    else if (c === ']') depth -= 1;
  }
  return depth;
}

/**
 * Tableaux [xxx] placés APRÈS le premier [[proxies]] / [[visitors]] (fichier
 * écrit à la main) : ils ne sont pas des tunnels, on les garde tels quels.
 * Tables [xxx] placed AFTER the first [[proxies]] / [[visitors]] (hand-written
 * file): they are not tunnels, they are kept as they are.
 */
export function tablesAfterBlocks(text) {
  const out = [];
  let seen = false;
  let inTable = false;
  let buf = [];
  let above = [];
  let arrDepth = 0;
  let strDelim = '';
  // Nombre de lignes de buf jusqu'à la dernière ligne de chaîne multi-lignes :
  // celles-là ne sont jamais retirées (un « # » y est du texte, pas un commentaire)
  // Number of buf lines up to the last multi-line string line: those are never
  // removed (a "#" there is text, not a comment)
  let pinned = 0;
  // Fin de buf : lignes vides, commentaires (et « range » si withRange) retirés
  // End of buf: blank lines, comments (and "range" if withRange) removed
  const trimBuf = (withRange) => {
    while (buf.length > pinned) {
      const l = buf[buf.length - 1].trim();
      if (!l || l.startsWith('#') || (withRange && RANGE_RE.test(splitComment(l)[0]))) buf.pop(); else break;
    }
  };
  const flush = () => {
    // Commentaires et « range » juste devant un bloc : ils sont à ce bloc
    // Comments and "range" right before a block: they belong to that block
    trimBuf(true);
    if (buf.length) out.push(buf.join('\n'));
    buf = [];
    pinned = 0;
  };
  for (const raw of prep(text).split(/\r?\n/)) {
    const line = raw.trim();
    // Suite d'un tableau sur plusieurs lignes : pas un en-tête de tableau TOML
    // Continuation of a multi-line array: not a TOML table header
    if (arrDepth > 0) {
      arrDepth = Math.max(0, arrDepth + bracketDepth(splitComment(line)[0]));
      if (inTable) buf.push(raw);
      continue;
    }
    // Suite d'une chaîne sur plusieurs lignes
    // Continuation of a multi-line string
    if (strDelim) {
      if (raw.includes(strDelim)) strDelim = '';
      if (inTable) { buf.push(raw); pinned = buf.length; }
      continue;
    }
    const eq = line.startsWith('[') ? -1 : keyEnd(line);
    if (eq >= 0) {
      const v0 = line.slice(eq + 1).trim();
      const d0 = v0.startsWith('"""') ? '"""' : v0.startsWith("'''") ? "'''" : '';
      if (d0 && !v0.slice(3).includes(d0)) strDelim = d0;
      // Ouverture d'une chaîne : ses crochets ne comptent pas comme un tableau
      // Opening of a string: its brackets do not count as an array
      else arrDepth = Math.max(0, bracketDepth(splitComment(line.slice(eq + 1))[0]));
    }
    const head = line.startsWith('[[') ? splitComment(line)[0].replace(/\s+/g, '') : line;
    if (head === '[[proxies]]' || head === '[[visitors]]') {
      if (inTable) flush();
      inTable = false;
      seen = true;
      continue;
    }
    if (!seen) continue;
    if (line.startsWith('[') && !/^\[+\s*(?:proxies|visitors)\s*\./.test(line)) {
      // Les commentaires juste au-dessus d'un tableau partent avec lui
      // The comments right above a table go with it
      if (!inTable) buf = [...above];
      inTable = true;
    }
    if (inTable) buf.push(raw);
    else if (line.startsWith('#')) above.push(raw);
    else if (line) above = [];
  }
  if (inTable) {
    // Fin de fichier : on garde aussi les commentaires de la fin dans trailing
    // End of file: the trailing comments stay in trailing
    trimBuf(false);
    if (buf.length) out.push(buf.join('\n'));
  }
  return out.join('\n\n');
}

/** Uniquement les blocs [[proxies]] / [[visitors]] (texte brut). */
export function extractBlocks(text) {
  text = prep(text);
  const m = findBlock(text);
  return m ? text.slice(m.index).trim() : '';
}

// ── Config serveur / client ───────────────────────────────────────────────
// v = valeurs du formulaire (chaînes déjà nettoyées, booléens pour les cases).

// generatedBy : fin de l'en-tête dans la langue de l'interface ; le début
// « # <nom>.toml » ne change pas (le serveur le repère à la copie d'une instance).
// generatedBy: end of the header in the interface language; the "# <name>.toml"
// start does not change (the server spots it when an instance is copied).
export function generateServer(v, name, generatedBy = 'generated by FRP Manager') {
  const L = [`# ${name}.toml — ${generatedBy}`, ''];
  L.push(`bindAddr = ${str(unbracket(v.bindAddr))}`);
  L.push(`bindPort = ${v.bindPort}`);
  if (v.proxyBindAddr) L.push(`proxyBindAddr = ${str(unbracket(v.proxyBindAddr))}`);
  if (v.kcpEnabled) L.push(`kcpBindPort = ${v.kcpBindPort}`);
  if (v.quicEnabled) L.push(`quicBindPort = ${v.quicBindPort}`);
  if (v.vhostHttpEnabled) L.push(`vhostHTTPPort = ${v.vhostHTTPPort}`);
  if (v.vhostHttpsEnabled) L.push(`vhostHTTPSPort = ${v.vhostHTTPSPort}`);
  if (Number(v.maxPortsPerClient) > 0) L.push(`maxPortsPerClient = ${v.maxPortsPerClient}`);

  L.push('', '[auth]', `method = ${str(v.authMethod)}`);
  if (v.authToken) L.push(`token = ${str(v.authToken)}`);

  L.push('', '[log]', `to = ${str(v.logTo)}`, `level = ${str(v.logLevel)}`, `maxDays = ${v.logMaxDays}`);

  if (v.webEnabled) {
    L.push('', '[webServer]', `addr = ${str(unbracket(v.webAddr))}`, `port = ${v.webPort}`);
    if (v.webUser) L.push(`user = ${str(v.webUser)}`);
    if (v.webPassword) L.push(`password = ${str(v.webPassword)}`);
  }
  if (v.tlsEnabled) {
    L.push('', '[transport.tls]', `certFile = ${str(v.tlsCert)}`, `keyFile = ${str(v.tlsKey)}`);
    if (v.tlsCa) L.push(`trustedCaFile = ${str(v.tlsCa)}`);
    if (v.tlsForce) L.push('force = true');
  }
  const pool = Number(v.maxPoolCount);
  const hbt = Number(v.heartbeatTimeout);
  if (pool !== 5 || hbt !== 90) {
    L.push('', '[transport]', `maxPoolCount = ${pool}`, `heartbeatTimeout = ${hbt}`);
  }
  return `${L.join('\n')}\n`;
}

export function generateClient(v, name, generatedBy = 'generated by FRP Manager') {
  const L = [`# ${name}.toml — ${generatedBy}`, ''];
  L.push(`serverAddr = ${str(unbracket(v.serverAddr))}`);
  L.push(`serverPort = ${v.serverPort}`);
  if (v.dnsServer) L.push(`dnsServer = ${str(unbracket(v.dnsServer))}`);

  L.push('', '[auth]', `method = ${str(v.authMethod)}`);
  if (v.authToken) L.push(`token = ${str(v.authToken)}`);

  L.push('', '[log]', `to = ${str(v.logTo)}`, `level = ${str(v.logLevel)}`, `maxDays = ${v.logMaxDays}`);

  const pool = Number(v.poolCount);
  const hbi = Number(v.heartbeatInterval);
  if (v.protocol !== 'tcp' || v.proxyURL || pool !== 5 || hbi !== 30 || v.tlsEnabled) {
    L.push('', '[transport]');
    if (v.protocol !== 'tcp') L.push(`protocol = ${str(v.protocol)}`);
    if (v.proxyURL) L.push(`proxyURL = ${str(v.proxyURL)}`);
    if (pool !== 5) L.push(`poolCount = ${pool}`);
    if (hbi !== 30) L.push(`heartbeatInterval = ${hbi}`);
  }
  if (v.tlsEnabled) {
    L.push('', '[transport.tls]', `certFile = ${str(v.tlsCert)}`, `keyFile = ${str(v.tlsKey)}`);
    if (v.tlsCa) L.push(`trustedCaFile = ${str(v.tlsCa)}`);
    if (v.tlsServerName) L.push(`serverName = ${str(v.tlsServerName)}`);
  }
  if (v.webEnabled) {
    L.push('', '[webServer]', `addr = ${str(unbracket(v.webAddr))}`, `port = ${v.webPort}`);
    if (v.webUser) L.push(`user = ${str(v.webUser)}`);
    if (v.webPassword) L.push(`password = ${str(v.webPassword)}`);
  }
  return `${L.join('\n')}\n`;
}

// ── Formulaire Configuration : fusion dans le fichier existant ────────────
// Seules les clés modifiées dans le formulaire changent, à leur place ; le reste
// du fichier (clés inconnues, commentaires, tables, modèles, tunnels) ne bouge pas.
// Configuration form: merge into the existing file. Only the keys changed in the
// form change, in place; the rest of the file (unknown keys, comments, tables,
// templates, tunnels) does not move.

/** Chemin d'une clé ou d'un nom de table (« a . "b" » → « a.b ») ; un segment entre
 *  guillemets qui n'est pas un simple nom garde ses guillemets.
 *  Path of a key or table name ("a . "b"" → "a.b"); a quoted segment that is not
 *  a plain name keeps its quotes. */
function keyPath(s) {
  const segs = [];
  let cur = '';
  let quote = null;
  for (let i = 0; i < s.length; i += 1) {
    const c = s[i];
    if (quote) {
      cur += c;
      if (c === '\\' && quote === '"') { i += 1; cur += s[i] || ''; } else if (c === quote) quote = null;
    } else if (c === '"' || c === "'") { quote = c; cur += c; }
    else if (c === '.') { segs.push(cur); cur = ''; }
    else cur += c;
  }
  segs.push(cur);
  return segs.map((x) => x.trim().replace(/^(["'])([\w-]+)\1$/, '$2')).join('.');
}

/** Crochets et accolades ouverts hors chaîne, jusqu'au commentaire.
 *  Open brackets and braces outside strings, up to the comment. */
function openDepth(s) {
  let depth = 0;
  let quote = null;
  for (let i = 0; i < s.length; i += 1) {
    const c = s[i];
    if (quote) {
      if (c === '\\' && quote === '"') i += 1;
      else if (c === quote) quote = null;
    } else if (c === '"' || c === "'") quote = c;
    else if (c === '#') break;
    else if (c === '[' || c === '{') depth += 1;
    else if (c === ']' || c === '}') depth -= 1;
  }
  return depth;
}

/** Fin (index après le délimiteur) d'une chaîne multi-lignes commençant en 0, sinon -1.
 *  End (index after the delimiter) of a multi-line string starting at 0, otherwise -1. */
function multilineClose(raw) {
  const d = raw.slice(0, 3);
  for (let i = 3; i + 3 <= raw.length; i += 1) {
    if (d === '"""' && raw[i] === '\\') { i += 1; continue; }
    if (!raw.startsWith(d, i)) continue;
    let end = i;
    while (end - i < 2 && raw[end + 3] === d[0]) end += 1;
    return end + 3;
  }
  return -1;
}

/**
 * Lignes de la partie connexion : entrées « clé = valeur » (clé complète, table,
 * première et dernière ligne) et en-têtes de table, hors blocs [[proxies]] /
 * [[visitors]], chaînes multi-lignes, tableaux sur plusieurs lignes et lignes de
 * modèle {{ }}.
 * Lines of the connection part: "key = value" entries (full key, table, first and
 * last line) and table headers, outside [[proxies]] / [[visitors]] blocks,
 * multi-line strings, multi-line arrays and {{ }} template lines.
 */
function scanConnection(lines) {
  const entries = [];
  const headers = [];
  const heads = [];
  let firstBlock = -1;
  let table = '';
  let inBlock = false;
  for (let i = 0; i < lines.length; i += 1) {
    const line = lines[i];
    const tr = line.trim();
    if (!tr || tr.startsWith('#') || tr.startsWith('{{')) continue;
    if (tr.startsWith('[')) {
      const code = splitComment(tr)[0];
      const array = code.startsWith('[[');
      const name = keyPath(code.replace(/^\[\[?|\]\]?$/g, ''));
      heads.push(i);
      if (array && (name === 'proxies' || name === 'visitors')) {
        inBlock = true;
        if (firstBlock < 0) firstBlock = i;
        continue;
      }
      // Sous-table d'un tunnel ([proxies.plugin]) : toujours dans le bloc
      // Sub-table of a tunnel ([proxies.plugin]): still inside the block
      if (inBlock && /^(?:proxies|visitors)\./.test(name)) continue;
      inBlock = false;
      table = array ? null : name;
      headers.push({ name, array, line: i });
      continue;
    }
    const eq = keyEnd(line);
    if (eq < 0) continue;
    const rest = line.slice(eq + 1).trim();
    const d = rest.startsWith('"""') ? '"""' : rest.startsWith("'''") ? "'''" : '';
    let end = i;
    if (d && !rest.slice(3).includes(d)) {
      while (end + 1 < lines.length) { end += 1; if (lines[end].includes(d)) break; }
    } else if (!d) {
      let depth = openDepth(rest);
      while (depth > 0 && end + 1 < lines.length) { end += 1; depth += openDepth(lines[end]); }
    }
    // Clés d'un [[tableau]] inconnu : jamais visées (table null)
    // Keys of an unknown [[array]]: never targeted (null table)
    if (!inBlock && table !== null) {
      const k = keyPath(line.slice(0, eq));
      entries.push({ key: table ? `${table}.${k}` : k, table, start: i, end, eq });
    }
    i = end;
  }
  return { entries, headers, heads, firstBlock };
}

/** Clé complète → valeur brute des lignes d'une config générée (generateServer / generateClient).
 *  Full key → raw value of the lines of a generated config (generateServer / generateClient). */
function generatedRaws(text) {
  const out = new Map();
  let section = '';
  for (const line of text.split('\n')) {
    const tr = line.trim();
    if (!tr || tr.startsWith('#')) continue;
    if (tr.startsWith('[')) { section = tr.slice(1, -1); continue; }
    const eq = keyEnd(tr);
    if (eq > 0) out.set(`${section ? `${section}.` : ''}${tr.slice(0, eq).trim()}`, splitComment(tr.slice(eq + 1))[0]);
  }
  return out;
}

/** [valeur brute, commentaire de fin] d'une entrée (ses lignes jointes par \n).
 *  [raw value, end comment] of an entry (its lines joined with \n). */
function entryValue(text, eq) {
  const rest = text.slice(eq + 1).trim();
  if (rest.startsWith('"""') || rest.startsWith("'''")) {
    const close = multilineClose(rest);
    if (close > 0) return [rest.slice(0, close), splitComment(rest.slice(close))[1]];
  }
  return splitComment(rest);
}

/** Nombre de segments communs au début de deux clés / leading segments shared by two keys */
function sharedSegs(a, b) {
  const x = a.split('.');
  const y = b.split('.');
  let n = 0;
  while (n < x.length && n < y.length && x[n] === y[n]) n += 1;
  return n;
}

/**
 * Fusionne une config générée dans le texte d'origine, pour les seules clés de
 * touched (clés complètes, « webServer.user ») : valeur remplacée à sa place
 * (toutes ses formes : table, clé pointée, entre guillemets ; la ligne reste telle
 * quelle si la valeur ne change pas), ligne retirée si la clé n'est plus générée,
 * ajoutée près des clés de sa table sinon (ou dans une nouvelle table, avant les
 * tunnels). Une table vidée par la fusion disparaît. Une clé rangée dans une table
 * en ligne (auth = { … }) n'est pas ajoutée.
 * Merges a generated config into the original text, for the keys of touched only
 * (full keys, "webServer.user"): value replaced in place (all its forms: table,
 * dotted key, quoted; the line stays as is if the value does not change), line
 * removed if the key is no longer generated, otherwise added near the keys of
 * its table (or in a new table, before the tunnels). A table emptied by the
 * merge disappears. A key stored in an inline table (auth = { … }) is not added.
 */
export function mergeConnection(text, generated, touched) {
  const src = String(text || '').replace(/^\uFEFF/, '').replace(/\r(?!\n)/g, '\n');
  // Fins de ligne du fichier d'origine : CRLF partout s'il en avait
  // Line endings of the original file: CRLF everywhere if it had some
  const crlf = /\r\n/.test(src);
  const lines = src.split(/\r?\n/);
  const n = lines.length;
  const { entries, headers, heads, firstBlock } = scanConnection(lines);
  const want = generatedRaws(generated);
  const touch = new Set(touched);
  const keys = [...[...want.keys()].filter((k) => touch.has(k)), ...[...touch].filter((k) => !want.has(k))];
  const drop = new Set();
  const put = new Map();
  // before : index de ligne → lignes ajoutées devant elle (n = à la fin)
  // before: line index → lines added in front of it (n = at the end)
  const before = new Map();
  const add = (q, ...ls) => { if (!before.has(q)) before.set(q, []); before.get(q).push(...ls); };
  const newTables = new Map();
  const isNote = (l) => { const x = l.trim(); return x.startsWith('#') || x.startsWith('{{'); };
  // Devant une ligne : en remontant les commentaires et modèles collés au-dessus
  // In front of a line: going up over the comments and templates stuck above it
  const above = (q) => { while (q > 0 && isNote(lines[q - 1])) q -= 1; return q; };
  const limit = firstBlock >= 0 ? firstBlock : n;
  const firstHead = Math.min(limit, ...headers.map((h) => h.line));
  // Fichier sans en-tête de table écrit en clés pointées (auth.token = …) : on
  // garde ce style plutôt que d'ajouter une table
  // File without table headers written with dotted keys (auth.token = …): that
  // style is kept rather than adding a table
  const dottedStyle = !headers.some((h) => !h.array) && entries.some((e) => e.key.includes('.'));
  const rootAdds = [];

  for (const key of keys) {
    const raw = want.get(key);
    const found = entries.filter((e) => e.key === key);
    if (found.length) {
      for (const e of found) {
        if (raw === undefined) {
          for (let j = e.start; j <= e.end; j += 1) drop.add(j);
          continue;
        }
        const old = lines.slice(e.start, e.end + 1).join('\n');
        const [oldRaw, eol] = entryValue(old, e.eq);
        if (unquote(oldRaw) === unquote(raw)) continue;
        const ws = /^[ \t]*/.exec(old.slice(e.eq + 1))[0];
        put.set(e.start, `${old.slice(0, e.eq + 1)}${ws}${raw}${eol ? ` ${eol}` : ''}`);
        for (let j = e.start + 1; j <= e.end; j += 1) drop.add(j);
      }
      continue;
    }
    if (raw === undefined) continue;
    // Clé à l'intérieur d'une valeur (table en ligne) : on n'y touche pas
    // Key inside a value (inline table): left alone
    if (entries.some((e) => key.startsWith(`${e.key}.`))) continue;
    const segs = key.split('.');
    const sec = segs.slice(0, -1).join('.');
    // Table d'accueil : l'en-tête le plus long qui contient la clé, sinon la racine
    // Home table: the longest header that contains the key, otherwise the root
    let home = '';
    for (const h of headers) {
      if (!h.array && (sec === h.name || sec.startsWith(`${h.name}.`)) && h.name.length > home.length) home = h.name;
    }
    const inHome = entries.filter((e) => e.table === home);
    const child = home ? `${home}.${key.slice(home.length + 1).split('.')[0]}` : segs[0];
    if (sec && sec !== home && !dottedStyle && !inHome.some((e) => e.key.startsWith(`${child}.`))) {
      // Ni table ni clé pointée pour cette section : nouvelle table [sec]
      // Neither a table nor a dotted key for this section: new table [sec]
      if (!newTables.has(sec)) newTables.set(sec, []);
      newTables.get(sec).push(`${segs[segs.length - 1]} = ${raw}`);
      continue;
    }
    const line = `${home ? key.slice(home.length + 1) : key} = ${raw}`;
    // Après la dernière clé de la table qui partage le plus de segments avec elle
    // After the last key of the table sharing the most segments with it
    let best = null;
    for (const e of inHome) if (!best || sharedSegs(e.key, key) >= sharedSegs(best.key, key)) best = e;
    if (best) add(best.end + 1, line);
    else if (home) add(headers.find((h) => h.name === home && !h.array).line + 1, line);
    else rootAdds.push(line);
  }

  // Racine sans aucune clé : devant la première table (et ses commentaires), sinon
  // après la dernière ligne non vide
  // Root without any key: in front of the first table (and its comments),
  // otherwise after the last non-blank line
  if (rootAdds.length) {
    let q = firstHead;
    if (q < n) q = above(q);
    else while (q > 0 && !lines[q - 1].trim()) q -= 1;
    add(q, ...rootAdds, ...(q < n && lines[q].trim() ? [''] : []));
  }

  if (newTables.size) {
    // Avant le premier tunnel (et ses commentaires), sinon à la fin du fichier
    // Before the first tunnel (and its comments), otherwise at the end of the file
    let q = limit;
    while (q > 0 && (!lines[q - 1].trim() || isNote(lines[q - 1]))) q -= 1;
    const ls = [];
    for (const [sec, body] of newTables) ls.push(...(ls.length || q > 0 ? [''] : []), `[${sec}]`, ...body);
    if (q < n && lines[q].trim()) ls.push('');
    add(q, ...ls);
  }

  // Table vidée par la fusion (plus que des lignes vides) : en-tête retiré
  // Table emptied by the merge (nothing but blank lines left): header removed
  for (const h of headers) {
    const stop = Math.min(n, ...heads.filter((x) => x > h.line));
    const region = [];
    for (let j = h.line + 1; j < stop; j += 1) region.push(j);
    const had = entries.some((e) => e.start > h.line && e.start < stop);
    const empty = region.every((j) => drop.has(j) || !lines[j].trim());
    const fed = region.some((j) => before.has(j)) || before.has(stop);
    if (had && empty && !fed && !h.array) {
      drop.add(h.line);
      // Une seule ligne vide reste entre ce qui précède et ce qui suit
      // A single blank line stays between what comes before and after
      const gapBefore = h.line > 0 && !lines[h.line - 1].trim();
      const blanks = region.filter((j) => !drop.has(j));
      for (const j of region) drop.add(j);
      if (stop === n) {
        // Fin du fichier : le saut de ligne final reste, la ligne vide d'avant part
        // End of file: the final line break stays, the blank line before goes
        if (lines[n - 1] === '') drop.delete(n - 1);
        if (gapBefore) drop.add(h.line - 1);
      } else if (!gapBefore && blanks.length) drop.delete(blanks[blanks.length - 1]);
    }
  }

  const out = [];
  for (let i = 0; i <= n; i += 1) {
    if (before.has(i)) out.push(...before.get(i));
    if (i === n) break;
    if (!drop.has(i)) out.push(put.has(i) ? put.get(i) : lines[i]);
  }
  return out.join(crlf ? '\r\n' : '\n');
}

// ── Tunnels ([[proxies]]) & visiteurs ([[visitors]]) ──────────────────────

export const PROXY_TYPES = ['tcp', 'udp', 'http', 'https', 'stcp', 'xtcp'];
export const VISITOR_TYPES = ['stcp', 'xtcp'];
/** « TCP + UDP » : une entrée de la liste pour deux tunnels, nom-tcp et nom-udp,
 *  avec les mêmes ports et les mêmes options (voir pairProxies et splitPair).
 *  "TCP + UDP": one list entry for two tunnels, name-tcp and name-udp, with the
 *  same ports and the same options (see pairProxies and splitPair). */
export const TCP_UDP = 'both';
export const hasRemotePort = (type) => type === 'tcp' || type === 'udp' || type === TCP_UDP;
export const hasDomains = (type) => type === 'http' || type === 'https';
export const hasSecret = (type) => type === 'stcp' || type === 'xtcp';

const PROXY_KEYS = new Set([
  'name', 'type', 'localIP', 'localPort', 'remotePort', 'customDomains', 'secretKey',
  'transport.useCompression', 'transport.useEncryption', 'transport.proxyProtocolVersion',
]);
const VISITOR_KEYS = new Set(['name', 'type', 'serverName', 'secretKey', 'bindAddr', 'bindPort']);

/** Adresse IP sans les crochets d'une IPv6 (« [::1] » devient « ::1 ») : frp ajoute
 *  lui-même les crochets, une adresse déjà entourée casse la connexion.
 *  IP address without the brackets of an IPv6 ("[::1]" becomes "::1"): frp adds
 *  the brackets itself, an already bracketed address breaks the connection. */
export function unbracket(addr) {
  // Valeur absente : chaîne vide, jamais « null » ou « undefined » écrit dans le fichier
  // Missing value: empty string, never "null" or "undefined" written to the file
  if (addr == null) return '';
  const m = /^\[(.*)\]$/.exec(String(addr).trim());
  return m ? m[1] : String(addr);
}

/** Partie connexion d'une config frpc avec serverAddr, dnsServer et webServer.addr
 *  sans crochets (fichier écrit par une ancienne version) : seules ces lignes
 *  changent, le reste du texte (commentaires, ordre, autres clés) reste tel quel.
 *  Connection part of an frpc config with serverAddr, dnsServer and webServer.addr
 *  without brackets (file written by an older version): only those lines change,
 *  the rest of the text (comments, order, other keys) stays as is. */
export function unbracketConnection(text) {
  let section = '';
  text = String(text || '');
  // Lignes d'une chaîne multi-lignes : ni en-tête ni clé, gardées telles quelles
  // Lines of a multi-line string: neither header nor key, kept as they are
  const inside = multilineStringLines(text);
  let off = 0;
  return text.split('\n').map((line) => {
    const at = off;
    off += line.length + 1;
    if (inside.has(at)) return line;
    const head = line.trim();
    // Les blocs [[proxies]] / [[visitors]] ne sont pas concernés
    // The [[proxies]] / [[visitors]] blocks are not concerned
    if (section === null) return line;
    if (head.startsWith('[[')) { section = null; return line; }
    if (head.startsWith('[')) { section = head.replace(/^\[+|\]+.*$/g, '').trim(); return line; }
    const m = /^(\s*)([\w.]+)(\s*=\s*)(["'])\[([^\]"']*)\]\4([\s\S]*)$/.exec(line);
    if (!m) return line;
    const key = section ? `${section}.${m[2]}` : m[2];
    if (!['serverAddr', 'dnsServer', 'webServer.addr'].includes(key)) return line;
    return `${m[1]}${m[2]}${m[3]}${m[4]}${m[5]}${m[4]}${m[6]}`;
  }).join('\n');
}

/** « hôte:port » utilisable tel quel : une IPv6 (avec ou sans zone, « fe80::1%eth0 »)
 *  va entre crochets, sauf si elle y est déjà ; IPv4 et noms de domaine inchangés.
 *  "host:port" usable as is: an IPv6 (with or without a zone, "fe80::1%eth0")
 *  goes between brackets, unless it already is; IPv4 and domain names unchanged. */
export function joinHostPort(host, port) {
  const text = String(host);
  return text.includes(':') && !text.startsWith('[') ? `[${text}]:${port}` : `${text}:${port}`;
}

/** Adresse « hôte:port » rendue par frpc : il colle l'adresse du serveur devant
 *  « :port », sans crochets pour une IPv6 (« 2001:db8::1:6000 »). Plusieurs « : »
 *  sans crochet, c'est une IPv6 suivie du port : on les ajoute.
 *  "host:port" address returned by frpc: it puts the server address before
 *  ":port", without brackets for an IPv6 ("2001:db8::1:6000"). Several ":"
 *  without a bracket means an IPv6 followed by the port: they are added. */
export function fixHostPort(addr) {
  const text = String(addr || '');
  const m = /^([^[\]]*:[^[\]]*):(\d+)$/.exec(text);
  return m && m[1] ? joinHostPort(m[1], m[2]) : text;
}

/** Un port (« 8080 ») ou une liste de ports et de plages (« 6000-6006,6007 »).
 *  A port ("8080") or a list of ports and ranges ("6000-6006,6007"). */
const PORT_SPEC_RE = /^\d{1,5}(-\d{1,5})?(,\d{1,5}(-\d{1,5})?)*$/;

/**
 * Nombre de ports d'une saisie (« 30000-30010 » → 11), ou 0 si elle est invalide :
 * ports de 1 à 65535, plages dans l'ordre.
 * Number of ports of an entry ("30000-30010" → 11), or 0 if it is invalid: ports
 * from 1 to 65535, ranges in order.
 */
export function portSpecCount(spec) {
  const text = String(spec || '').replace(/\s+/g, '');
  if (!PORT_SPEC_RE.test(text)) return 0;
  let total = 0;
  for (const part of text.split(',')) {
    const [a, b = a] = part.split('-').map(Number);
    if (a < 1 || b > 65535 || a > b) return 0;
    total += b - a + 1;
  }
  return total;
}

/** Plage (ou liste) de ports plutôt qu'un port seul ?
 *  A range (or list) of ports rather than a single port? */
export const isPortRange = (spec) => /[-,]/.test(String(spec || ''));

/** Tunnel écrit en modèle de plage (voir generateProxy) ? Tous les types : plage
 *  sur le port local, et sur le port public pour tcp et udp.
 *  Tunnel written as a range template (see generateProxy)? All types: range on
 *  the local port, and on the public port for tcp and udp. */
export const isRanged = (p) => isPortRange(p.localPort) || (hasRemotePort(p.type) && isPortRange(p.remotePort));

/** Visiteur écrit en modèle de plage (voir generateVisitor) : plage sur le port local.
 *  Visitor written as a range template (see generateVisitor): range on the local port. */
export const isVisitorRanged = (v) => isPortRange(v.bindPort);

/** Plus grand nombre de ports d'une plage : au-delà, frpc crée trop de tunnels.
 *  Largest number of ports in a range: beyond it, frpc creates too many tunnels. */
export const MAX_RANGE_PORTS = 1000;

/** Repère du port dans un domaine d'une plage http/https (« app-{port}.example.com ») :
 *  chaque tunnel a besoin de son propre domaine.
 *  Port marker in a domain of an http/https range ("app-{port}.example.com"):
 *  each tunnel needs its own domain. */
export const PORT_TOKEN = '{port}';

/** « nom-<port> » pour chaque port d'une saisie (« 6000-6002,6007 »).
 *  "name-<port>" for each port of an entry ("6000-6002,6007"). */
function rangeNames(base, spec) {
  const names = [];
  for (const part of String(spec).replace(/\s+/g, '').split(',')) {
    const [a, b = a] = part.split('-').map(Number);
    for (let port = a; port <= b && names.length < 65535; port += 1) names.push(`${base}-${port}`);
  }
  return names;
}

/**
 * Noms des tunnels que frpc créera pour cette entrée : son nom, ou « nom-<port
 * local> » pour chaque port d'une plage. frpc refuse de démarrer si deux tunnels
 * ont le même nom.
 * Names of the tunnels frpc will create for this entry: its name, or "name-<local
 * port>" for each port of a range. frpc refuses to start if two tunnels share a
 * name.
 */
export function frpcNames(p) {
  if (p.type === TCP_UDP) return splitPair(p).flatMap(frpcNames);
  return isRanged(p) ? rangeNames(p.name, p.localPort) : [p.name];
}

/** Noms des visiteurs que frpc créera : son nom, ou « nom-<port local> » par port
 *  d'une plage ; frpc refuse deux visiteurs du même nom.
 *  Names of the visitors frpc will create: its name, or "name-<local port>" per
 *  port of a range; frpc refuses two visitors with the same name. */
export function visitorNames(v) {
  return isVisitorRanged(v) ? rangeNames(v.name, v.bindPort) : [v.name];
}

/** Nom de tunnel saisi : ni espace ni caractère de contrôle, 64 caractères au
 *  plus. frp en accepte davantage : la règle ne vaut que pour un nom nouveau.
 *  Typed tunnel name: no spaces or control characters, 64 characters at most.
 *  frp accepts more: the rule only applies to a new name. */
export function tunnelNameOk(name) {
  return [...name].length <= 64 && !/[\s\p{Cc}]/u.test(name);
}

let uidSeq = 0;
const uid = () => `u${++uidSeq}`;

function extrasOf(block, known) {
  return block.order.filter((k) => !known.has(k)).map((k) => [k, block.raw[k]]);
}

/** Entier TOML avec tirets bas (« 6_000 ») sans les tirets bas, sinon le texte. */
/** TOML integer with underscores ("6_000") without them, otherwise the text. */
function intText(v) {
  const t = String(v || '');
  return /^\d+(?:_\d+)*$/.test(t) ? t.replace(/_/g, '') : t;
}

export function proxyFromBlock(block) {
  const v = block.values;
  // Bloc d'un modèle de plage : les ports sont ceux de la ligne « range », le nom
  // perd son suffixe « -{{ $v.First }} »
  // Block of a range template: the ports are those of the "range" line, the name
  // loses its "-{{ $v.First }}" suffix
  // (« -{{ $v }} » pour une seule plage) ; dans les domaines, le port redevient {port}
  // ("-{{ $v }}" for a single range); in the domains, the port becomes {port} again
  const range = block.range || null;
  const domains = (v.customDomains || '').replace(/["'[\]]/g, '').split(',').map((s) => s.trim()).filter(Boolean).join(', ');
  return {
    uid: uid(),
    name: range ? (v.name || '').replace(new RegExp(`-?${LOOP_FIRST}$`), '') : v.name || '',
    type: PROXY_TYPES.includes(v.type) ? v.type : (v.type || 'tcp'),
    localIP: v.localIP || '127.0.0.1',
    localPort: range ? range.local : intText(v.localPort),
    // remotePort = 0 : port attribué par le serveur, comme s'il était absent
    remotePort: range ? range.remote : Number(intText(v.remotePort)) ? intText(v.remotePort) : '',
    domains: range ? domains.replace(new RegExp(LOOP_FIRST, 'g'), PORT_TOKEN) : domains,
    secretKey: v.secretKey || '',
    proxyProtocol: v['transport.proxyProtocolVersion'] || '',
    compression: v['transport.useCompression'] === 'true',
    encryption: v['transport.useEncryption'] === 'true',
    extras: extrasOf(block, PROXY_KEYS),
    comments: block.comments || [],
    notes: block.notes || {},
  };
}

/** Les deux tunnels d'une entrée « TCP + UDP » (nom-tcp, nom-udp), sinon l'entrée.
 *  The two tunnels of a "TCP + UDP" entry (name-tcp, name-udp), otherwise the entry. */
export function splitPair(p) {
  if (p.type !== TCP_UDP) return [p];
  // Chacun retrouve ses commentaires (udpComments : ceux du tunnel udp)
  // Each one gets its comments back (udpComments: those of the udp tunnel)
  const { udpComments = [], udpNotes, ...q } = p;
  return [{ ...q, name: `${p.name}-tcp`, type: 'tcp' }, { ...q, name: `${p.name}-udp`, type: 'udp', comments: udpComments, notes: udpNotes || q.notes }];
}

/**
 * Regroupe en une entrée « TCP + UDP » les paires qu'elle écrit : un tunnel tcp
 * « X-tcp » et un tunnel udp « X-udp » identiques pour tout le reste (adresse,
 * ports, port public renseigné, options, réglages avancés). Il suffit d'une
 * différence pour qu'ils restent deux entrées. L'entrée prend la place du premier
 * des deux ; à l'enregistrement, splitPair les réécrit l'un après l'autre.
 * Groups into one "TCP + UDP" entry the pairs it writes: a tcp tunnel "X-tcp" and
 * a udp tunnel "X-udp" identical in everything else (address, ports, public port
 * set, options, advanced settings). A single difference keeps them as two entries.
 * The entry takes the place of the first of the two; on save, splitPair writes
 * them back one after the other.
 */
export function pairProxies(proxies) {
  // Les commentaires n'empêchent pas le regroupement : ceux des deux sont gardés
  // Comments do not prevent grouping: those of both are kept
  const rest = (p) => JSON.stringify({ ...p, uid: '', name: '', type: '', comments: [], udpComments: [], notes: {}, udpNotes: {} });
  const mates = new Map();
  for (const p of proxies) {
    const base = p.type === 'tcp' && p.remotePort ? /^(.+)-tcp$/.exec(p.name)?.[1] : null;
    if (!base || mates.has(p)) continue;
    const q = proxies.find((x) => x.type === 'udp' && x.name === `${base}-udp` && !mates.has(x) && rest(x) === rest(p));
    if (q) { mates.set(p, q); mates.set(q, p); }
  }
  const out = [];
  const done = new Set();
  for (const p of proxies) {
    if (done.has(p)) continue;
    const q = mates.get(p);
    if (!q) { out.push(p); continue; }
    done.add(q);
    const tcp = p.type === 'tcp' ? p : q;
    const udp = tcp === p ? q : p;
    out.push({ ...tcp, name: tcp.name.slice(0, -'-tcp'.length), type: TCP_UDP, udpComments: udp.comments || [], udpNotes: udp.notes || {} });
  }
  return out;
}

/** Port tiré au hasard dans 10000–32767 : loin du 7000 que visent les robots,
 * sous la plage des ports éphémères de Linux (même tirage qu'install.sh). */
export function randomPort() {
  const n = new Uint16Array(1);
  crypto.getRandomValues(n);
  return String(10000 + (n[0] % 22768));
}

export function newProxy() {
  return {
    uid: uid(), name: '', type: 'tcp', localIP: '127.0.0.1', localPort: '', remotePort: '',
    domains: '', secretKey: '', proxyProtocol: '', compression: false, encryption: false, extras: [], comments: [],
  };
}

export function visitorFromBlock(block) {
  const v = block.values;
  // Bloc d'un modèle de plage : ports locaux et ports du tunnel visé (vides
  // s'ils sont les mêmes) lus sur la ligne « range », noms sans leur suffixe
  // Block of a range template: local ports and ports of the target tunnel
  // (empty when they are the same) read from the "range" line, names without
  // their suffix
  const range = block.range || null;
  return {
    uid: uid(),
    name: range ? (v.name || '').replace(new RegExp(`-?${LOOP_FIRST}$`), '') : v.name || '',
    type: v.type || 'stcp',
    serverName: range ? (v.serverName || '').replace(new RegExp(`-?${LOOP_SECOND}$`), '') : v.serverName || '',
    secretKey: v.secretKey || '',
    bindAddr: v.bindAddr || '127.0.0.1',
    bindPort: range ? range.local : intText(v.bindPort),
    serverPorts: range && range.remote && range.remote !== range.local ? range.remote : '',
    extras: extrasOf(block, VISITOR_KEYS),
    comments: block.comments || [],
    notes: block.notes || {},
  };
}

export function newVisitor() {
  return { uid: uid(), name: '', type: 'stcp', serverName: '', secretKey: '', bindAddr: '127.0.0.1', bindPort: '', serverPorts: '', extras: [], comments: [] };
}

export function generateProxy(p) {
  // Plage de ports : un modèle frpc (parseNumberRangePair) qui crée un tunnel par
  // port, nommé « nom-<port> ». Les deux plages ont le même nombre de ports.
  // Port range: an frpc template (parseNumberRangePair) that creates one tunnel
  // per port, named "name-<port>". Both ranges have the same number of ports.
  // Sans port public (http, https, stcp, xtcp), une seule plage : parseNumberRange,
  // et {port} dans un domaine devient le port de chaque tunnel.
  // Without a public port (http, https, stcp, xtcp), a single range:
  // parseNumberRange, and {port} in a domain becomes each tunnel's port.
  const ranged = isRanged(p);
  const pair = ranged && hasRemotePort(p.type);
  const port = pair ? '{{ $v.First }}' : '{{ $v }}';
  const head = pair
    ? `{{- range $_, $v := parseNumberRangePair "${p.localPort}" "${p.remotePort}" }}`
    : `{{- range $_, $v := parseNumberRange "${p.localPort}" }}`;
  const L = ranged
    ? [head, '[[proxies]]', `name = ${str(`${p.name}-${port}`)}`, `type = ${str(p.type)}`]
    : ['[[proxies]]', `name = ${str(p.name)}`, `type = ${str(p.type)}`];
  if (p.localIP) L.push(`localIP = ${str(unbracket(p.localIP))}`);
  if (ranged) L.push(`localPort = ${port}`);
  else if (p.localPort) L.push(`localPort = ${p.localPort}`);
  if (pair) L.push('remotePort = {{ $v.Second }}');
  else if (hasRemotePort(p.type) && p.remotePort) L.push(`remotePort = ${p.remotePort}`);
  if (hasDomains(p.type) && p.domains) {
    const list = p.domains.split(',').map((s) => s.trim()).filter(Boolean)
      .map((d) => (ranged ? str(d).split(PORT_TOKEN).join(port) : str(d)));
    L.push(`customDomains = [${list.join(', ')}]`);
  }
  if (hasSecret(p.type) && p.secretKey) L.push(`secretKey = ${str(p.secretKey)}`);
  if (p.compression) L.push('transport.useCompression = true');
  if (p.encryption) L.push('transport.useEncryption = true');
  if (p.proxyProtocol) L.push(`transport.proxyProtocolVersion = ${str(p.proxyProtocol)}`);
  for (const [k, raw] of p.extras) L.push(`${k} = ${raw}`);
  if (ranged) L.push('{{- end }}');
  return [...(p.comments || []), ...annotate(L, p.notes)].join('\n');
}

export function generateVisitor(v) {
  // Plage de ports : un visiteur par port local, nommé « nom-<port> », qui
  // rejoint « tunnel-<port> », le tunnel de même rang d'une plage stcp/xtcp
  // (ports du tunnel visé : les mêmes, sauf s'ils sont donnés).
  // Port range: one visitor per local port, named "name-<port>", which joins
  // "tunnel-<port>", the tunnel of the same rank in an stcp/xtcp range (ports of
  // the target tunnel: the same, unless they are given).
  const ranged = isVisitorRanged(v);
  const L = ranged
    ? [`{{- range $_, $v := parseNumberRangePair "${v.bindPort}" "${v.serverPorts || v.bindPort}" }}`, '[[visitors]]',
      `name = ${str(`${v.name}-{{ $v.First }}`)}`, `type = ${str(v.type)}`]
    : ['[[visitors]]', `name = ${str(v.name)}`, `type = ${str(v.type)}`];
  if (v.serverName) L.push(`serverName = ${str(ranged ? `${v.serverName}-{{ $v.Second }}` : v.serverName)}`);
  if (v.secretKey) L.push(`secretKey = ${str(v.secretKey)}`);
  if (v.bindAddr) L.push(`bindAddr = ${str(unbracket(v.bindAddr))}`);
  if (ranged) L.push('bindPort = {{ $v.First }}');
  else if (v.bindPort) L.push(`bindPort = ${v.bindPort}`);
  for (const [k, raw] of v.extras) L.push(`${k} = ${raw}`);
  if (ranged) L.push('{{- end }}');
  return [...(v.comments || []), ...annotate(L, v.notes)].join('\n');
}

/** Config frpc complète : connexion d'origine + tunnels + visiteurs. */
export function composeClientConfig(baseText, proxies, visitors) {
  baseText = prep(baseText);
  const blocks = [
    ...proxies.flatMap(splitPair).map((p) => generateProxy(p)),
    ...visitors.filter((v) => v.name).map(generateVisitor),
  ];
  // Adresses de la connexion entre crochets (ancienne version) : frpc ne
  // démarrerait pas, elles sont corrigées à chaque écriture
  // Bracketed connection addresses (older version): frpc would not start, they
  // are fixed on every write
  let base = unbracketConnection(stripBlocks(baseText));
  // Commentaires de la fin du fichier, après le dernier bloc : gardés à la fin
  // Comments at the end of the file, after the last block: kept at the end
  let trailingLines = parseToml(baseText).trailing;
  if (!findBlock(baseText)) {
    // Fichier sans aucun tunnel : ses commentaires de fin sont dans la partie
    // connexion ; avec un premier tunnel ils passent devant lui, une seule fois
    // File with no tunnel: its end comments are in the connection part; with a
    // first tunnel they move in front of it, only once
    if (blocks.length && trailingLines.length) {
      base = dropTrailingComments(base);
      blocks[0] = [...trailingLines, blocks[0]].join('\n');
    }
    trailingLines = [];
  }
  const trailing = trailingLines.join('\n');
  // Tableaux écrits après les tunnels : repris après la connexion
  // Tables written after the tunnels: carried over after the connection part
  const tables = tablesAfterBlocks(baseText);
  const text = `${[base, tables, ...blocks, trailing].filter(Boolean).join('\n\n')}\n`;
  // Fins de ligne du fichier d'origine : CRLF partout s'il en avait
  // Line endings of the original file: CRLF everywhere if it had some
  return /\r\n/.test(baseText) ? text.replace(/\r?\n/g, '\r\n') : text;
}
