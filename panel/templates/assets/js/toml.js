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
const BLOCK_RE = new RegExp(String.raw`^(?:${RANGE_LINE}[ \t]*\r?\n)?\[\[(proxies|visitors)\]\]`, 'm');
const RANGE_RE = new RegExp(`^${RANGE_LINE}$`);
const END_RE = /^\{\{-?\s*end\s*-?\}\}$/;

/** Coupe un commentaire de fin de ligne situé hors chaîne. */
function stripComment(value) {
  let quote = null;
  for (let i = 0; i < value.length; i += 1) {
    const c = value[i];
    if (quote) {
      if (c === '\\' && quote === '"') i += 1;
      else if (c === quote) quote = null;
    } else if (c === '"' || c === "'") {
      quote = c;
    } else if (c === '#') {
      return value.slice(0, i).trim();
    }
  }
  return value.trim();
}

function unquote(raw) {
  if (raw.length >= 2 && raw.startsWith('"') && raw.endsWith('"')) {
    return raw.slice(1, -1).replace(/\\(["\\nt])/g, (m, c) => ({ n: '\n', t: '\t' }[c] || c));
  }
  if (raw.length >= 2 && raw.startsWith("'") && raw.endsWith("'")) return raw.slice(1, -1);
  return raw;
}

export function str(value) {
  return `"${String(value).replace(/\\/g, '\\\\').replace(/"/g, '\\"')}"`;
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

  for (const rawLine of String(text || '').split(/\r?\n/)) {
    const line = rawLine.trim();
    if (!line || line.startsWith('#')) continue;

    // Modèle de plage de ports : « range » devant le bloc, « end » derrière
    // Port-range template: "range" in front of the block, "end" behind it
    const rm = RANGE_RE.exec(line);
    // Une seule plage (parseNumberRange) : pas de second côté
    // A single range (parseNumberRange): no second side
    if (rm) { pendingRange = rm[3] !== undefined ? { local: rm[3], remote: '' } : { local: rm[1], remote: rm[2] }; continue; }
    if (END_RE.test(line)) continue;

    if (line === '[[proxies]]' || line === '[[visitors]]') {
      block = { values: {}, raw: {}, order: [] };
      if (pendingRange) block.range = pendingRange;
      pendingRange = null;
      out[line === '[[proxies]]' ? 'proxies' : 'visitors'].push(block);
      prefix = '';
      continue;
    }

    if (line.startsWith('[')) {
      const name = line.replace(/^\[+|\]+.*$/g, '').trim();
      const sub = name.match(/^(?:proxies|visitors)\.(.+)$/);
      if (block && sub) { prefix = `${sub[1]}.`; continue; }
      block = null;
      prefix = '';
      section = name;
      continue;
    }

    const eq = line.indexOf('=');
    if (eq < 0) continue;
    const key = line.slice(0, eq).trim();
    const raw = stripComment(line.slice(eq + 1));
    const value = unquote(raw);

    if (block) {
      const k = prefix + key;
      if (!(k in block.raw)) block.order.push(k);
      block.values[k] = value;
      block.raw[k] = raw;
    } else {
      out.values[section ? `${section}.${key}` : key] = value;
    }
  }
  return out;
}

/** Partie « connexion » de la config, sans les [[proxies]] / [[visitors]]. */
export function stripBlocks(text) {
  const m = BLOCK_RE.exec(text);
  return (m ? text.slice(0, m.index) : text).trimEnd();
}

/** Uniquement les blocs [[proxies]] / [[visitors]] (texte brut). */
export function extractBlocks(text) {
  const m = BLOCK_RE.exec(text);
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
  return String(text || '').split('\n').map((line) => {
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

let uidSeq = 0;
const uid = () => `u${++uidSeq}`;

function extrasOf(block, known) {
  return block.order.filter((k) => !known.has(k)).map((k) => [k, block.raw[k]]);
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
    localPort: range ? range.local : String(v.localPort || ''),
    // remotePort = 0 : port attribué par le serveur, comme s'il était absent
    remotePort: range ? range.remote : Number(v.remotePort) ? String(v.remotePort) : '',
    domains: range ? domains.replace(new RegExp(LOOP_FIRST, 'g'), PORT_TOKEN) : domains,
    secretKey: v.secretKey || '',
    proxyProtocol: v['transport.proxyProtocolVersion'] || '',
    compression: v['transport.useCompression'] === 'true',
    encryption: v['transport.useEncryption'] === 'true',
    extras: extrasOf(block, PROXY_KEYS),
  };
}

/** Les deux tunnels d'une entrée « TCP + UDP » (nom-tcp, nom-udp), sinon l'entrée.
 *  The two tunnels of a "TCP + UDP" entry (name-tcp, name-udp), otherwise the entry. */
export function splitPair(p) {
  if (p.type !== TCP_UDP) return [p];
  return [{ ...p, name: `${p.name}-tcp`, type: 'tcp' }, { ...p, name: `${p.name}-udp`, type: 'udp' }];
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
  const rest = (p) => JSON.stringify({ ...p, uid: '', name: '', type: '' });
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
    out.push({ ...tcp, name: tcp.name.slice(0, -'-tcp'.length), type: TCP_UDP });
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
    domains: '', secretKey: '', proxyProtocol: '', compression: false, encryption: false, extras: [],
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
    bindPort: range ? range.local : v.bindPort || '',
    serverPorts: range && range.remote && range.remote !== range.local ? range.remote : '',
    extras: extrasOf(block, VISITOR_KEYS),
  };
}

export function newVisitor() {
  return { uid: uid(), name: '', type: 'stcp', serverName: '', secretKey: '', bindAddr: '127.0.0.1', bindPort: '', serverPorts: '', extras: [] };
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
  return L.join('\n');
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
  return L.join('\n');
}

/** Config frpc complète : connexion d'origine + tunnels + visiteurs. */
export function composeClientConfig(baseText, proxies, visitors) {
  const blocks = [
    ...proxies.flatMap(splitPair).map((p) => generateProxy(p)),
    ...visitors.filter((v) => v.name).map(generateVisitor),
  ];
  // Adresses de la connexion entre crochets (ancienne version) : frpc ne
  // démarrerait pas, elles sont corrigées à chaque écriture
  // Bracketed connection addresses (older version): frpc would not start, they
  // are fixed on every write
  const base = unbracketConnection(stripBlocks(baseText));
  return `${[base, ...blocks].filter(Boolean).join('\n\n')}\n`;
}
