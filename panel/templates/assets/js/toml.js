// ── TOML frp : lecture / écriture ─────────────────────────────────────────
// Pas un parseur TOML générique : il couvre la forme des configs frp
// (clés racine, [sections], [[proxies]] / [[visitors]] et leurs sous-tables).
// Chaque tunnel garde la valeur brute de ses clés pour réécrire à l'identique
// celles que l'interface ne gère pas (plugin, healthCheck, subdomain…).

// Un bloc peut être précédé de la ligne « range » d'un modèle de plage de ports
// (voir generateProxy) : elle part avec lui, pas avec la partie connexion.
// A block may be preceded by the "range" line of a port-range template (see
// generateProxy): it goes with the block, not with the connection part.
const RANGE_LINE = String.raw`\{\{-?\s*range\s+\$_,\s*\$v\s*:=\s*parseNumberRangePair\s+"([^"]*)"\s+"([^"]*)"\s*-?\}\}`;
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
    if (rm) { pendingRange = { local: rm[1], remote: rm[2] }; continue; }
    if (END_RE.test(line)) continue;

    if (line === '[[proxies]]' || line === '[[visitors]]') {
      block = { values: {}, raw: {}, order: [] };
      if (pendingRange && line === '[[proxies]]') block.range = pendingRange;
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

export function generateServer(v, name) {
  const L = [`# ${name}.toml — généré par FRP Manager`, ''];
  L.push(`bindAddr = ${str(v.bindAddr)}`);
  L.push(`bindPort = ${v.bindPort}`);
  if (v.proxyBindAddr) L.push(`proxyBindAddr = ${str(v.proxyBindAddr)}`);
  if (v.kcpEnabled) L.push(`kcpBindPort = ${v.kcpBindPort}`);
  if (v.quicEnabled) L.push(`quicBindPort = ${v.quicBindPort}`);
  if (v.vhostHttpEnabled) L.push(`vhostHTTPPort = ${v.vhostHTTPPort}`);
  if (v.vhostHttpsEnabled) L.push(`vhostHTTPSPort = ${v.vhostHTTPSPort}`);
  if (Number(v.maxPortsPerClient) > 0) L.push(`maxPortsPerClient = ${v.maxPortsPerClient}`);

  L.push('', '[auth]', `method = ${str(v.authMethod)}`);
  if (v.authToken) L.push(`token = ${str(v.authToken)}`);

  L.push('', '[log]', `to = ${str(v.logTo)}`, `level = ${str(v.logLevel)}`, `maxDays = ${v.logMaxDays}`);

  if (v.webEnabled) {
    L.push('', '[webServer]', `addr = ${str(v.webAddr)}`, `port = ${v.webPort}`);
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

export function generateClient(v, name) {
  const L = [`# ${name}.toml — généré par FRP Manager`, ''];
  L.push(`serverAddr = ${str(v.serverAddr)}`);
  L.push(`serverPort = ${v.serverPort}`);
  if (v.dnsServer) L.push(`dnsServer = ${str(v.dnsServer)}`);

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
    L.push('', '[webServer]', `addr = ${str(v.webAddr)}`, `port = ${v.webPort}`);
    if (v.webUser) L.push(`user = ${str(v.webUser)}`);
    if (v.webPassword) L.push(`password = ${str(v.webPassword)}`);
  }
  return `${L.join('\n')}\n`;
}

// ── Tunnels ([[proxies]]) & visiteurs ([[visitors]]) ──────────────────────

export const PROXY_TYPES = ['tcp', 'udp', 'http', 'https', 'stcp', 'xtcp'];
export const VISITOR_TYPES = ['stcp', 'xtcp'];
export const hasRemotePort = (type) => type === 'tcp' || type === 'udp';
export const hasDomains = (type) => type === 'http' || type === 'https';
export const hasSecret = (type) => type === 'stcp' || type === 'xtcp';

const PROXY_KEYS = new Set([
  'name', 'type', 'localIP', 'localPort', 'remotePort', 'customDomains', 'secretKey',
  'transport.useCompression', 'transport.useEncryption', 'transport.proxyProtocolVersion',
]);
const VISITOR_KEYS = new Set(['name', 'type', 'serverName', 'secretKey', 'bindAddr', 'bindPort']);

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

/** Tunnel écrit en modèle de plage (voir generateProxy) ?
 *  Tunnel written as a range template (see generateProxy)? */
export const isRanged = (p) => hasRemotePort(p.type) && (isPortRange(p.localPort) || isPortRange(p.remotePort));

/** Plus grand nombre de ports d'une plage : au-delà, frpc crée trop de tunnels.
 *  Largest number of ports in a range: beyond it, frpc creates too many tunnels. */
export const MAX_RANGE_PORTS = 1000;

/**
 * Noms des tunnels que frpc créera pour cette entrée : son nom, ou « nom-<port
 * local> » pour chaque port d'une plage. frpc refuse de démarrer si deux tunnels
 * ont le même nom.
 * Names of the tunnels frpc will create for this entry: its name, or "name-<local
 * port>" for each port of a range. frpc refuses to start if two tunnels share a
 * name.
 */
export function frpcNames(p) {
  if (!isRanged(p)) return [p.name];
  const names = [];
  for (const part of String(p.localPort).replace(/\s+/g, '').split(',')) {
    const [a, b = a] = part.split('-').map(Number);
    for (let port = a; port <= b && names.length < 65535; port += 1) names.push(`${p.name}-${port}`);
  }
  return names;
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
  const range = block.range || null;
  return {
    uid: uid(),
    name: range ? (v.name || '').replace(/-?\{\{\s*\$v\.First\s*\}\}$/, '') : v.name || '',
    type: PROXY_TYPES.includes(v.type) ? v.type : (v.type || 'tcp'),
    localIP: v.localIP || '127.0.0.1',
    localPort: range ? range.local : String(v.localPort || ''),
    // remotePort = 0 : port attribué par le serveur, comme s'il était absent
    remotePort: range ? range.remote : Number(v.remotePort) ? String(v.remotePort) : '',
    domains: (v.customDomains || '').replace(/["'[\]]/g, '').split(',').map((s) => s.trim()).filter(Boolean).join(', '),
    secretKey: v.secretKey || '',
    proxyProtocol: v['transport.proxyProtocolVersion'] || '',
    compression: v['transport.useCompression'] === 'true',
    encryption: v['transport.useEncryption'] === 'true',
    extras: extrasOf(block, PROXY_KEYS),
  };
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
  return {
    uid: uid(),
    name: v.name || '',
    type: v.type || 'stcp',
    serverName: v.serverName || '',
    secretKey: v.secretKey || '',
    bindAddr: v.bindAddr || '127.0.0.1',
    bindPort: v.bindPort || '',
    extras: extrasOf(block, VISITOR_KEYS),
  };
}

export function newVisitor() {
  return { uid: uid(), name: '', type: 'stcp', serverName: '', secretKey: '', bindAddr: '127.0.0.1', bindPort: '', extras: [] };
}

export function generateProxy(p) {
  // Plage de ports : un modèle frpc (parseNumberRangePair) qui crée un tunnel par
  // port, nommé « nom-<port> ». Les deux plages ont le même nombre de ports.
  // Port range: an frpc template (parseNumberRangePair) that creates one tunnel
  // per port, named "name-<port>". Both ranges have the same number of ports.
  const ranged = isRanged(p);
  const L = ranged
    ? [`{{- range $_, $v := parseNumberRangePair "${p.localPort}" "${p.remotePort}" }}`, '[[proxies]]',
      `name = ${str(`${p.name}-{{ $v.First }}`)}`, `type = ${str(p.type)}`]
    : ['[[proxies]]', `name = ${str(p.name)}`, `type = ${str(p.type)}`];
  if (p.localIP) L.push(`localIP = ${str(p.localIP)}`);
  if (ranged) L.push('localPort = {{ $v.First }}');
  else if (p.localPort) L.push(`localPort = ${p.localPort}`);
  if (ranged) L.push('remotePort = {{ $v.Second }}');
  else if (hasRemotePort(p.type) && p.remotePort) L.push(`remotePort = ${p.remotePort}`);
  if (hasDomains(p.type) && p.domains) {
    const list = p.domains.split(',').map((s) => s.trim()).filter(Boolean);
    L.push(`customDomains = [${list.map(str).join(', ')}]`);
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
  const L = ['[[visitors]]', `name = ${str(v.name)}`, `type = ${str(v.type)}`];
  if (v.serverName) L.push(`serverName = ${str(v.serverName)}`);
  if (v.secretKey) L.push(`secretKey = ${str(v.secretKey)}`);
  if (v.bindAddr) L.push(`bindAddr = ${str(v.bindAddr)}`);
  if (v.bindPort) L.push(`bindPort = ${v.bindPort}`);
  for (const [k, raw] of v.extras) L.push(`${k} = ${raw}`);
  return L.join('\n');
}

/** Config frpc complète : connexion d'origine + tunnels + visiteurs. */
export function composeClientConfig(baseText, proxies, visitors) {
  const blocks = [
    ...proxies.map((p) => generateProxy(p)),
    ...visitors.filter((v) => v.name).map(generateVisitor),
  ];
  const base = stripBlocks(baseText);
  return `${[base, ...blocks].filter(Boolean).join('\n\n')}\n`;
}
