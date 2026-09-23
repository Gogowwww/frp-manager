// ── TOML frp : lecture / écriture ─────────────────────────────────────────
// Pas un parseur TOML générique : il couvre la forme des configs frp
// (clés racine, [sections], [[proxies]] / [[visitors]] et leurs sous-tables).
// Chaque tunnel garde la valeur brute de ses clés pour réécrire à l'identique
// celles que l'interface ne gère pas (plugin, healthCheck, subdomain…).

const BLOCK_RE = /^\[\[(proxies|visitors)\]\]/m;

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

  for (const rawLine of String(text || '').split(/\r?\n/)) {
    const line = rawLine.trim();
    if (!line || line.startsWith('#')) continue;

    if (line === '[[proxies]]' || line === '[[visitors]]') {
      block = { values: {}, raw: {}, order: [] };
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
export const supportsRealIp = (type) => type === 'tcp' || type === 'udp';

const PROXY_KEYS = new Set([
  'name', 'type', 'localIP', 'localPort', 'remotePort', 'customDomains', 'secretKey',
  'transport.useCompression', 'transport.useEncryption', 'transport.proxyProtocolVersion',
]);
const VISITOR_KEYS = new Set(['name', 'type', 'serverName', 'secretKey', 'bindAddr', 'bindPort']);

let uidSeq = 0;
const uid = () => `u${++uidSeq}`;

function extrasOf(block, known) {
  return block.order.filter((k) => !known.has(k)).map((k) => [k, block.raw[k]]);
}

/** mm = relais go-mmproxy existant pour ce tunnel (affiche la vraie cible). */
export function proxyFromBlock(block, mm) {
  const v = block.values;
  return {
    uid: uid(),
    name: v.name || '',
    type: PROXY_TYPES.includes(v.type) ? v.type : (v.type || 'tcp'),
    localIP: mm ? mm.target_ip : (v.localIP || '127.0.0.1'),
    localPort: String(mm ? mm.target_port : (v.localPort || '')),
    remotePort: v.remotePort || '',
    domains: (v.customDomains || '').replace(/["'[\]]/g, '').split(',').map((s) => s.trim()).filter(Boolean).join(', '),
    secretKey: v.secretKey || '',
    proxyProtocol: v['transport.proxyProtocolVersion'] || '',
    compression: v['transport.useCompression'] === 'true',
    encryption: v['transport.useEncryption'] === 'true',
    realIp: !!mm,
    extras: extrasOf(block, PROXY_KEYS),
  };
}

export function newProxy() {
  return {
    uid: uid(), name: '', type: 'tcp', localIP: '127.0.0.1', localPort: '', remotePort: '',
    domains: '', secretKey: '', proxyProtocol: '', compression: false, encryption: false, realIp: false, extras: [],
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

/** relayPorts = { nomTunnel: portRelais } renvoyé par /api/mmproxy/sync. */
export function generateProxy(p, relayPorts = {}) {
  const L = ['[[proxies]]', `name = ${str(p.name)}`, `type = ${str(p.type)}`];
  const relay = supportsRealIp(p.type) && p.realIp && relayPorts[p.name];
  if (relay) {
    // frpc pointe vers le relais go-mmproxy, qui transmet au vrai service
    L.push('localIP = "127.0.0.1"');
    L.push(`localPort = ${relay} # relais go-mmproxy vers ${p.localIP || '127.0.0.1'}:${p.localPort}`);
  } else {
    if (p.localIP) L.push(`localIP = ${str(p.localIP)}`);
    if (p.localPort) L.push(`localPort = ${p.localPort}`);
  }
  if (hasRemotePort(p.type) && p.remotePort) L.push(`remotePort = ${p.remotePort}`);
  if (hasDomains(p.type) && p.domains) {
    const list = p.domains.split(',').map((s) => s.trim()).filter(Boolean);
    L.push(`customDomains = [${list.map(str).join(', ')}]`);
  }
  if (hasSecret(p.type) && p.secretKey) L.push(`secretKey = ${str(p.secretKey)}`);
  if (p.compression) L.push('transport.useCompression = true');
  if (p.encryption) L.push('transport.useEncryption = true');
  if (relay) L.push('transport.proxyProtocolVersion = "v2"');
  else if (p.proxyProtocol) L.push(`transport.proxyProtocolVersion = ${str(p.proxyProtocol)}`);
  for (const [k, raw] of p.extras) L.push(`${k} = ${raw}`);
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
export function composeClientConfig(baseText, proxies, visitors, relayPorts) {
  const blocks = [
    ...proxies.map((p) => generateProxy(p, relayPorts)),
    ...visitors.filter((v) => v.name).map(generateVisitor),
  ];
  const base = stripBlocks(baseText);
  return `${[base, ...blocks].filter(Boolean).join('\n\n')}\n`;
}
