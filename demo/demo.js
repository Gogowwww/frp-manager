// ── FRP Manager : démo sans serveur ────────────────────────────────────────
// Chargé avant l'application (script classique dans <head>) : remplace fetch
// pour /api/…, et WebSocket pour /ws/…, par un faux serveur en mémoire.
// L'état (instances, configs, règles du pare-feu…) est gardé dans l'onglet
// (sessionStorage) : il survit à un rechargement ou au changement de langue,
// et repart de zéro avec « Réinitialiser » ou dans un nouvel onglet.
// Construit par demo/build.py ; __PANEL_VERSION__ y est remplacé.

(function () {
  'use strict';

  const PANEL_VERSION = '__PANEL_VERSION__';
  const REPO = 'Gogowwww/frp-manager';
  const STATE_KEY = 'frpm.demo.state';
  const CATALOG_URL = `https://raw.githubusercontent.com/${REPO}/blocklists/blocklists.json`;
  const FRP_LATEST = '0.68.1';
  const FRP_INSTALLED = '0.67.0';
  const PANEL_PORT = 8765;
  const CLIENT_IP = '90.12.34.56';

  // ── Langue : la démo s'ouvre en anglais, sauf navigateur en français ──────
  try {
    if (!localStorage.getItem('frpm.locale')) {
      const nav = String(navigator.language || '').toLowerCase();
      localStorage.setItem('frpm.locale', nav.startsWith('fr') ? 'fr' : 'en');
    }
    // Pas de fenêtre « Avant de commencer » : elle cacherait la démo à l'ouverture
    localStorage.setItem('frpMgrWelcomeSeen', '1');
  } catch { /* stockage indisponible : français par défaut */ }
  const lang = () => {
    try { return localStorage.getItem('frpm.locale') === 'en' ? 'en' : 'fr'; } catch { return 'fr'; }
  };
  const M = (fr, en) => (lang() === 'en' ? en : fr);

  // ── Petits outils ─────────────────────────────────────────────────────────
  const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
  const pick = (arr) => arr[Math.floor(Math.random() * arr.length)];
  const rnd = (a, b) => a + Math.floor(Math.random() * (b - a + 1));
  const hex = (n) => Array.from({ length: n }, () => rnd(0, 15).toString(16)).join('');
  const pad = (n, w = 2) => String(n).padStart(w, '0');
  const now = () => Math.floor(Date.now() / 1000);

  function ipToInt(ip) {
    const p = String(ip).split('.').map(Number);
    if (p.length !== 4 || p.some((x) => !Number.isInteger(x) || x < 0 || x > 255)) return null;
    return ((p[0] << 24) >>> 0) + (p[1] << 16) + (p[2] << 8) + p[3];
  }
  function isIp(text) {
    return ipToInt(text) !== null || /^[0-9a-f:]+$/i.test(text) && text.includes(':');
  }
  function inCidr(ip, cidr) {
    const [net, bits = '32'] = String(cidr).split('/');
    const a = ipToInt(ip);
    const b = ipToInt(net);
    if (a === null || b === null) return ip === net;
    const n = Number(bits);
    if (n === 0) return true;
    const mask = (0xffffffff << (32 - n)) >>> 0;
    return ((a & mask) >>> 0) === ((b & mask) >>> 0);
  }

  // ── Opérateurs (AS) connus de la démo ─────────────────────────────────────
  const ASNS = {
    3215: { holder: 'Orange S.A.', prefixes: ['90.0.0.0/9', '86.192.0.0/10', '2.0.0.0/12', '92.128.0.0/10'], count: 1284 },
    12322: { holder: 'Free SAS', prefixes: ['78.192.0.0/10', '82.64.0.0/14', '88.160.0.0/11'], count: 214 },
    15557: { holder: 'Societe Francaise Du Radiotelephone - SFR SA', prefixes: ['77.192.0.0/10', '93.0.0.0/11'], count: 671 },
    5410: { holder: 'Bouygues Telecom SA', prefixes: ['176.128.0.0/10', '89.80.0.0/12'], count: 310 },
    16276: { holder: 'OVH SAS', prefixes: ['51.68.0.0/16', '51.75.0.0/16', '54.36.0.0/16', '141.94.0.0/16'], count: 412 },
    14061: { holder: 'DigitalOcean, LLC', prefixes: ['159.65.0.0/16', '167.99.0.0/16', '206.189.0.0/16', '64.227.0.0/16'], count: 1034 },
    4134: { holder: 'CHINANET-BACKBONE', prefixes: ['218.92.0.0/16', '61.160.0.0/16', '222.186.0.0/16'], count: 5712 },
    45090: { holder: 'Shenzhen Tencent Computer Systems', prefixes: ['43.128.0.0/10', '49.51.0.0/16'], count: 890 },
    211298: { holder: 'Driftnet Ltd', prefixes: ['87.236.176.0/24', '193.163.125.0/24'], count: 12 },
    202425: { holder: 'IP Volume inc', prefixes: ['80.82.77.0/24', '89.248.165.0/24', '94.102.61.0/24'], count: 64 },
  };
  function asnOf(ip) {
    for (const [asn, e] of Object.entries(ASNS)) {
      if (e.prefixes.some((c) => inCidr(ip, c))) return { asn: Number(asn), holder: e.holder };
    }
    return null;
  }

  // ── Configurations de départ ──────────────────────────────────────────────
  const FRPS_TOML = `# Serveur frp du VPS (démo)
bindAddr = "0.0.0.0"
bindPort = 7000
vhostHTTPPort = 80
vhostHTTPSPort = 443
allowPorts = [{ start = 20000, end = 20100 }, { single = 25565 }, { single = 2222 }, { single = 3389 }]

auth.method = "token"
auth.token = "demo-7f3a91c2e4b8"

log.to = "/var/log/frp/frps.log"
log.level = "info"
log.maxDays = 3

[webServer]
addr = "0.0.0.0"
port = 7500
user = "admin"
password = "demo"
`;

  const FRPC_TOML = `serverAddr = "vps.example.net"
serverPort = 7000

auth.method = "token"
auth.token = "demo-7f3a91c2e4b8"

log.to = "/var/log/frp/frpc.log"
log.level = "info"
log.maxDays = 3

[[proxies]]
name = "minecraft"
type = "tcp"
localIP = "127.0.0.1"
localPort = 25565
remotePort = 25565
transport.proxyProtocolVersion = "v2"

[[proxies]]
name = "ssh"
type = "tcp"
localIP = "127.0.0.1"
localPort = 22
remotePort = 2222

[[proxies]]
name = "blog"
type = "http"
localIP = "127.0.0.1"
localPort = 8080
customDomains = ["blog.example.net"]

[[proxies]]
name = "jellyfin"
type = "https"
localIP = "192.168.1.20"
localPort = 8920
customDomains = ["media.example.net"]

[[proxies]]
name = "rdp-bureau"
type = "stcp"
secretKey = "demo-secret"
localIP = "192.168.1.30"
localPort = 3389
`;

  const FRPC_NAS_TOML = `serverAddr = "vps.example.net"
serverPort = 7000

auth.method = "token"
auth.token = "demo-7f3a91c2e4b8"

[[proxies]]
name = "nas-web"
type = "tcp"
localIP = "127.0.0.1"
localPort = 5000
remotePort = 20001

[[proxies]]
name = "nas-webdav"
type = "tcp"
localIP = "127.0.0.1"
localPort = 5005
remotePort = 20002
transport.useEncryption = true
transport.useCompression = true
`;

  function initialState() {
    const fr = lang() === 'fr';
    return {
      instances: {
        frps: {
          id: 'frps', type: 'frps', source: 'systemd', binary_path: '/usr/local/bin/frps', binary_found: true,
          version: FRP_INSTALLED, config_path: '/etc/frp/frps.toml', config_exists: true,
          service: 'frps', log_path: '/var/log/frp/frps.log', configured: true,
        },
        frpc: {
          id: 'frpc', type: 'frpc', source: 'systemd', binary_path: '/usr/local/bin/frpc', binary_found: true,
          version: FRP_INSTALLED, config_path: '/etc/frp/frpc.toml', config_exists: true,
          service: 'frpc', log_path: '/var/log/frp/frpc.log', configured: true,
        },
        'docker_frpc-nas': {
          id: 'docker_frpc-nas', type: 'frpc', source: 'docker', container_name: 'frpc-nas',
          network_mode: 'host', image: 'snowdreamtech/frpc:0.68.1', binary_path: 'docker:frpc-nas', binary_found: true,
          version: null, config_path: null, config_exists: false, service: 'frpc-nas', log_path: null, configured: true,
        },
      },
      status: {
        frps: { active: 'active', enabled: true, running: true },
        frpc: { active: 'active', enabled: true, running: true },
        'docker_frpc-nas': { active: 'active', enabled: false, running: true },
      },
      configs: { frps: FRPS_TOML, frpc: FRPC_TOML, 'docker_frpc-nas': FRPC_NAS_TOML },
      nicknames: { frps: fr ? 'VPS public' : 'Public VPS', frpc: fr ? 'Serveur maison' : 'Home server' },
      manager: { bind_host: '0.0.0.0', bind_port: PANEL_PORT, username: 'admin', session_timeout: 3600, ssl_enabled: true, has_password: false },
      frp: { installed: FRP_INSTALLED },
      firewall: {
        enabled: true,
        rules: [
          {
            id: 'a1b2c3d4', name: fr ? 'Scanners et clouds' : 'Scanners and clouds', mode: 'block', ports: '*',
            sources: [
              { asn: 14061, note: 'DigitalOcean' },
              { asn: 4134, note: 'Chinanet' },
              { asn: 202425, note: 'IP Volume' },
              { cidr: '45.155.205.0/24', note: fr ? 'scanner' : 'scanner' },
            ],
            enabled: true,
          },
          {
            id: 'e5f6a7b8', name: fr ? 'SSH : maison seulement' : 'SSH: home only', mode: 'allow', ports: '2222',
            sources: [
              { cidr: CLIENT_IP, note: fr ? 'maison' : 'home' },
              { asn: 12322, note: 'Free' },
            ],
            enabled: true,
          },
        ],
      },
      counters: { a1b2c3d4: 1843, e5f6a7b8: 212 },
      blocked: [],
    };
  }

  let S;
  function load() {
    try {
      const raw = sessionStorage.getItem(STATE_KEY);
      if (raw) { S = JSON.parse(raw); return; }
    } catch { /* état illisible : on repart de zéro */ }
    S = initialState();
    S.blocked = seedBlocked();
  }
  function save() {
    try { sessionStorage.setItem(STATE_KEY, JSON.stringify(S)); } catch { /* sans stockage : état en mémoire */ }
  }

  // ── Détection et état ─────────────────────────────────────────────────────
  function detect() {
    const out = {};
    for (const [id, inst] of Object.entries(S.instances)) {
      out[id] = { ...inst, status: { ...(S.status[id] || { active: 'inactive', enabled: false, running: false }) } };
    }
    return out;
  }

  const statusSockets = new Set();
  function pushStatus() {
    save();
    const payload = JSON.stringify({ instances: detect(), in_docker: false });
    statusSockets.forEach((s) => s._message(payload));
  }

  // ── Pare-feu : ports, règles, verdicts ─────────────────────────────────────
  function tomlValues(text) {
    const values = {};
    let section = '';
    for (const raw of String(text || '').split(/\r?\n/)) {
      const line = raw.replace(/#.*$/, '').trim();
      if (!line) continue;
      const sec = line.match(/^\[([^[\]]+)\]$/);
      if (sec) { section = sec[1].trim(); continue; }
      if (line.startsWith('[[')) { section = '#'; continue; }
      const kv = line.match(/^([\w.]+)\s*=\s*(.+)$/);
      if (kv && section !== '#') values[(section ? `${section}.` : '') + kv[1]] = kv[2].trim().replace(/^"(.*)"$/, '$1');
    }
    return values;
  }

  function proxiesOf(text) {
    return String(text || '').split(/^\s*\[\[proxies\]\]\s*$/m).slice(1).map((block) => {
      const body = block.split(/^\s*\[\[visitors\]\]\s*$/m)[0];
      const get = (k) => (body.match(new RegExp(`^\\s*${k}\\s*=\\s*"?([^"\\n#]+)"?`, 'm')) || [])[1];
      return { name: (get('name') || '').trim(), type: (get('type') || 'tcp').trim(), remotePort: Number(get('remotePort')) || null };
    });
  }

  function knownPorts() {
    const known = [];
    const seen = new Set();
    const add = (start, end, proto, label, kind, online = null) => {
      const key = `${start}-${end}-${proto}`;
      if (seen.has(key) || !(start >= 1 && start <= end && end <= 65535)) return;
      seen.add(key);
      known.push({ start, end, proto, label, kind, online });
    };
    for (const [id, inst] of Object.entries(S.instances)) {
      if (inst.type !== 'frps' || !S.status[id]?.running) continue;
      const text = S.configs[id] || '';
      const v = tomlValues(text);
      const bind = Number(v.bindPort) || 7000;
      add(bind, bind, 'tcp', M('Connexion des clients frpc', 'frpc client connections'), 'config');
      for (const [key, proto, fr, en] of [
        ['kcpBindPort', 'udp', 'KCP', 'KCP'], ['quicBindPort', 'udp', 'QUIC', 'QUIC'],
        ['vhostHTTPPort', 'tcp', 'Sites HTTP (vhost)', 'HTTP sites (vhost)'],
        ['vhostHTTPSPort', 'tcp', 'Sites HTTPS (vhost)', 'HTTPS sites (vhost)'],
      ]) {
        if (Number(v[key])) add(Number(v[key]), Number(v[key]), proto, M(fr, en), 'config');
      }
      if (Number(v['webServer.port']) && !['127.0.0.1', 'localhost', '::1'].includes(v['webServer.addr'] || '127.0.0.1')) {
        add(Number(v['webServer.port']), Number(v['webServer.port']), 'tcp', M('Tableau de bord frps', 'frps dashboard'), 'config');
      }
      const allow = (text.match(/^\s*allowPorts\s*=\s*\[(.*)\]\s*$/m) || [])[1] || '';
      for (const m of allow.matchAll(/\{([^}]*)\}/g)) {
        const single = m[1].match(/single\s*=\s*(\d+)/);
        const start = m[1].match(/start\s*=\s*(\d+)/);
        const end = m[1].match(/end\s*=\s*(\d+)/);
        if (single) add(Number(single[1]), Number(single[1]), 'any', M('Port réservé aux clients', 'Port reserved for clients'), 'range');
        else if (start && end) add(Number(start[1]), Number(end[1]), 'any', M('Plage réservée aux clients', 'Range reserved for clients'), 'range');
      }
    }
    if (known.length) {
      // Ports ouverts par les clients frpc de la démo (vus par le tableau de bord frps)
      for (const [id, inst] of Object.entries(S.instances)) {
        if (inst.type !== 'frpc') continue;
        for (const p of proxiesOf(S.configs[id])) {
          if (!p.remotePort || !['tcp', 'udp'].includes(p.type)) continue;
          add(p.remotePort, p.remotePort, p.type, M(`Port « ${p.name} »`, `Port “${p.name}”`), 'proxy', !!S.status[id]?.running);
        }
      }
    }
    return known.sort((a, b) => a.start - b.start || a.end - b.end);
  }

  const hasFrps = () => Object.entries(S.instances).some(([id, i]) => i.type === 'frps' && (S.status[id]?.running || S.status[id]?.enabled));

  function parsePorts(spec) {
    const ranges = [];
    for (const part of String(spec).trim().split(/[,\s]+/).filter(Boolean)) {
      const m = part.match(/^(\d{1,5})(?:-(\d{1,5}))?$/);
      if (!m) throw new Error(M(`« ${part} » n'est pas un port ni une plage (ex. 30000-30010)`, `“${part}” is not a port or a range (e.g. 30000-30010)`));
      const a = Number(m[1]);
      const b = Number(m[2] || m[1]);
      if (!(a >= 1 && a <= b && b <= 65535)) throw new Error(M(`« ${part} » : les ports vont de 1 à 65535, dans l'ordre`, `“${part}”: ports go from 1 to 65535, in order`));
      ranges.push([a, b]);
    }
    if (!ranges.length) throw new Error(M('Indiquez au moins un port', 'Enter at least one port'));
    return ranges;
  }

  function normalizeSource(src, where) {
    const note = String(src.note || '').trim().slice(0, 60);
    const text = src.asn ? `AS${src.asn}` : String(src.cidr || '').trim();
    if (!text) return null;
    const as = text.match(/^AS\s*(\d{1,10})$/i);
    if (as) return { asn: Number(as[1]), note };
    const [addr, bits] = text.split('/');
    const v4 = ipToInt(addr) !== null;
    const okBits = bits === undefined || (/^\d+$/.test(bits) && Number(bits) <= (v4 ? 32 : 128));
    if (!isIp(addr) || !okBits) {
      throw new Error(M(`${where} : « ${text} » n'est ni une adresse IP, ni un réseau (203.0.113.0/24), ni un AS (AS16276)`,
        `${where}: “${text}” is neither an IP address, a network (203.0.113.0/24) nor an AS (AS16276)`));
    }
    return { cidr: bits === undefined || Number(bits) === (v4 ? 32 : 128) ? addr : `${addr}/${bits}`, note };
  }

  function normalizeRule(raw, i) {
    const name = String(raw.name || '').trim().slice(0, 60);
    const where = name ? M(`Règle « ${name} »`, `Rule “${name}”`) : M(`Règle ${i + 1}`, `Rule ${i + 1}`);
    if (!['allow', 'block'].includes(raw.mode)) throw new Error(`${where} : ${M('action inconnue', 'unknown action')}`);
    let ports = String(raw.ports || '').trim();
    if (ports !== '*') {
      let ranges;
      try { ranges = parsePorts(ports); } catch (e) { throw new Error(`${where} : ${e.message}`); }
      if (ranges.length === 1 && ranges[0][0] === PANEL_PORT && ranges[0][1] === PANEL_PORT) {
        throw new Error(M(`${where} : le port du panel (${PANEL_PORT}) n'est jamais filtré ici`, `${where}: the panel port (${PANEL_PORT}) is never filtered here`));
      }
      ports = ranges.map(([a, b]) => (a === b ? String(a) : `${a}-${b}`)).join(', ');
    }
    const sources = (raw.sources || []).map((s) => normalizeSource(s, where)).filter(Boolean);
    const list = String(raw.list || '').trim();
    if (raw.mode === 'block' && !sources.length && !list) {
      throw new Error(M(`${where} : indiquez au moins une adresse à bloquer`, `${where}: enter at least one address to block`));
    }
    return {
      id: String(raw.id || '').replace(/[^0-9a-f]/g, '').slice(0, 12) || hex(8),
      name, mode: raw.mode, ports, sources, ...(list ? { list } : {}), enabled: raw.enabled !== false,
    };
  }

  function ruleRanges(rule, known) {
    const ranges = rule.ports === '*' ? known.map((k) => [k.start, k.end]) : parsePorts(rule.ports);
    const out = [];
    for (const [a, b] of ranges) {
      if (a <= PANEL_PORT && PANEL_PORT <= b) {
        if (a < PANEL_PORT) out.push([a, PANEL_PORT - 1]);
        if (PANEL_PORT < b) out.push([PANEL_PORT + 1, b]);
      } else out.push([a, b]);
    }
    return out;
  }

  function ruleSources(rule) {
    const lst = rule.list ? catalogById(rule.list) : null;
    return [...(rule.sources || []), ...(lst ? lst.sources : [])];
  }

  function ruleMatches(rule, ip) {
    return ruleSources(rule).some((s) => {
      if (s.asn) return (ASNS[s.asn]?.prefixes || []).some((c) => inCidr(ip, c));
      return inCidr(ip, s.cidr);
    });
  }

  function verdicts(ip, rules, known) {
    const blocking = [];
    for (const rule of rules) {
      if (rule.enabled === false) continue;
      let ranges;
      try { ranges = ruleRanges(rule, known); } catch { continue; }
      if ((rule.mode === 'allow') !== ruleMatches(rule, ip)) blocking.push([ranges, rule.name || rule.id]);
    }
    return known.map((k) => ({
      ...k,
      blocked_by: blocking.filter(([ranges]) => ranges.some(([a, b]) => a <= k.end && k.start <= b)).map(([, n]) => n),
    }));
  }

  function asnInfo(rules) {
    const info = {};
    for (const rule of rules) {
      for (const s of ruleSources(rule)) {
        if (!s.asn) continue;
        const e = ASNS[s.asn];
        info[s.asn] = { holder: e ? e.holder : (s.note || `AS${s.asn}`), prefixes: e ? e.count : 8 + (s.asn % 97), fetched: now() - 3600 };
      }
    }
    return info;
  }

  // ── Listes communautaires : le vrai catalogue GitHub, sinon une copie ────
  let catalog = null;
  let catalogFetched = 0;
  let catalogError = null;
  const FALLBACK_CATALOG = [
    { id: 'scanners', name: 'Scanners', mode: 'block', author: 'demo', updated: '2026-09-20',
      description: 'Scanners de ports connus (Censys, Shodan, Driftnet…).',
      sources: ['AS211298  # Driftnet', 'AS202425  # IP Volume', '45.155.205.0/24'] },
    { id: 'fai', name: 'FAI 🇫🇷', mode: 'allow', author: 'Gogow_', updated: '2026-09-25',
      description: 'Autorise seulement les FAI Français.',
      sources: ['AS3215  # Orange France', 'AS15557  # SFR', 'AS12322  # Free SAS', 'AS5410  # Bouygues Telecom'] },
  ];

  function normalizeList(raw) {
    if (!raw || !/^[a-z0-9][a-z0-9-]{0,39}$/.test(String(raw.id || ''))) return null;
    const sources = [];
    for (const item of raw.sources || []) {
      const [text, ...note] = String(item).split('#');
      try {
        const s = normalizeSource({ cidr: text.trim(), note: note.join('#').trim() }, raw.id);
        if (s) sources.push(s);
      } catch { /* entrée illisible : écartée, comme sur le vrai panel */ }
    }
    if (!sources.length) return null;
    return {
      id: raw.id, name: String(raw.name || raw.id).slice(0, 60), description: String(raw.description || '').slice(0, 300),
      author: String(raw.author || '').slice(0, 40), updated: String(raw.updated || '').slice(0, 10),
      mode: raw.mode === 'allow' ? 'allow' : 'block', sources,
    };
  }

  async function getCatalog(fresh) {
    if (catalog && !fresh) return catalog;
    try {
      const r = await realFetch(CATALOG_URL, { cache: fresh ? 'no-cache' : 'default' });
      if (!r.ok) throw new Error(`HTTP ${r.status}`);
      catalog = ((await r.json()).lists || []).map(normalizeList).filter(Boolean);
      catalogFetched = now();
      catalogError = null;
    } catch (e) {
      if (!catalog) catalog = FALLBACK_CATALOG.map(normalizeList).filter(Boolean);
      catalogError = String(e.message || e);
    }
    return catalog;
  }
  const catalogById = (id) => (catalog || FALLBACK_CATALOG.map(normalizeList)).find((l) => l && l.id === id) || null;

  function listsInfo(rules) {
    const info = {};
    for (const r of rules) {
      if (!r.list) continue;
      const l = catalogById(r.list);
      info[r.list] = l ? { name: l.name, author: l.author, updated: l.updated, count: l.sources.length } : null;
    }
    return info;
  }

  // ── Connexions bloquées (simulées) ────────────────────────────────────────
  const ATTACKERS = [
    ['167.99.', 14061], ['159.65.', 14061], ['206.189.', 14061], ['64.227.', 14061],
    ['218.92.0.', 4134], ['222.186.', 4134], ['61.160.', 4134],
    ['80.82.77.', 202425], ['89.248.165.', 202425], ['94.102.61.', 202425],
    ['45.155.205.', null], ['193.163.125.', 211298], ['87.236.176.', 211298],
    ['43.153.', 45090], ['49.51.', 45090], ['51.75.', 16276], ['141.94.', 16276],
  ];
  function randomIp(prefix) {
    const parts = prefix.split('.').filter(Boolean);
    while (parts.length < 4) parts.push(String(rnd(1, 254)));
    return parts.join('.');
  }

  /** Une tentative bloquée plausible avec les règles actuelles, ou null. */
  function fakeAttempt(when) {
    if (!S.firewall.enabled) return null;
    const known = knownPorts();
    if (!known.length) return null;
    for (let tries = 0; tries < 12; tries += 1) {
      const [prefix] = pick(ATTACKERS);
      const ip = randomIp(prefix);
      const k = pick(known);
      const port = k.start === k.end ? k.start : rnd(k.start, k.end);
      const v = verdicts(ip, S.firewall.rules, [{ ...k, start: port, end: port }])[0];
      if (!v.blocked_by.length) continue;
      const rule = S.firewall.rules.find((r) => (r.name || r.id) === v.blocked_by[0]);
      if (!rule) continue;
      return { src: ip, proto: k.proto === 'udp' ? 'udp' : 'tcp', port, rule: rule.id, last: when, as: asnOf(ip) };
    }
    return null;
  }

  function seedBlocked() {
    const list = [];
    let t = now();
    for (let i = 0; i < 18; i += 1) {
      t -= rnd(20, 900);
      const e = fakeAttempt(t);
      if (e) list.push(e);
    }
    return list;
  }

  function blockedPayload() {
    const entries = [...S.blocked].sort((a, b) => b.last - a.last).slice(0, 200);
    return { ok: true, entries, total: S.blocked.length, counters: { ...S.counters }, available: true, now: now() };
  }

  const firewallSockets = new Set();
  function tickFirewall() {
    const e = fakeAttempt(now());
    if (e) {
      const same = S.blocked.find((x) => x.src === e.src && x.port === e.port);
      if (same) same.last = e.last; else S.blocked.unshift(e);
      S.blocked = S.blocked.filter((x) => now() - x.last < 86400).slice(0, 300);
      S.counters[e.rule] = (S.counters[e.rule] || 0) + rnd(1, 4);
      save();
      const payload = JSON.stringify(blockedPayload());
      firewallSockets.forEach((s) => s._message(payload));
    }
    setTimeout(tickFirewall, rnd(2500, 7000));
  }

  // ── Journaux ──────────────────────────────────────────────────────────────
  function stamp(d, iso) {
    const date = `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`;
    const time = `${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`;
    if (iso) {
      const off = -d.getTimezoneOffset();
      return `${date}T${time}${off >= 0 ? '+' : '-'}${pad(Math.floor(Math.abs(off) / 60))}${pad(Math.abs(off) % 60)}`;
    }
    return `${date} ${time}.${pad(d.getMilliseconds(), 3)}`;
  }

  function frpLine(iid, d) {
    const inst = S.instances[iid];
    const run = hex(16);
    const visitor = randomIp(pick(['90.', '86.', '82.64.', '78.', '176.', '93.']));
    if (inst.type === 'frps') {
      const proxy = pick(['minecraft', 'ssh', 'blog', 'jellyfin', 'nas-web', 'nas-webdav']);
      return pick([
        `[I] [proxy/proxy.go:204] [${run}] [${proxy}] get a user connection [${visitor}:${rnd(40000, 65000)}]`,
        `[I] [proxy/proxy.go:204] [${run}] [${proxy}] get a user connection [${visitor}:${rnd(40000, 65000)}]`,
        `[I] [http/server.go:147] [${run}] [blog] http request from [${visitor}] to [blog.example.net/]`,
        `[I] [server/control.go:382] [${run}] new work connection registered`,
        `[I] [server/service.go:586] [${run}] client login info: ip [${randomIp('82.64.')}:${rnd(40000, 65000)}] version [${FRP_INSTALLED}] hostname [] os [linux] arch [amd64]`,
        `[W] [server/control.go:420] [${run}] heartbeat timeout, close control`,
        `[I] [proxy/proxy.go:117] [${run}] [${proxy}] proxy closing`,
      ]);
    }
    const proxy = pick(proxiesOf(S.configs[iid]).map((p) => p.name).filter(Boolean)) || 'ssh';
    return pick([
      `[I] [proxy/proxy_wrapper.go:224] [${run}] [${proxy}] start a new work connection, localAddr: 127.0.0.1:${rnd(40000, 65000)} remoteAddr: ${randomIp('203.0.113.')}:7000`,
      `[I] [proxy/proxy_wrapper.go:224] [${run}] [${proxy}] start a new work connection, localAddr: 127.0.0.1:${rnd(40000, 65000)} remoteAddr: ${randomIp('203.0.113.')}:7000`,
      `[I] [proxy/proxy.go:201] [${run}] [${proxy}] join connections, workConn(l[127.0.0.1:${rnd(40000, 65000)}] r[${randomIp('203.0.113.')}:7000])`,
      `[D] [client/control.go:171] [${run}] send heartbeat to server`,
      `[I] [client/control.go:168] [${run}] [${proxy}] start proxy success`,
      `[W] [proxy/proxy.go:198] [${run}] [${proxy}] connect to local service [127.0.0.1:${rnd(1000, 9000)}] error: dial tcp: connection refused`,
    ]);
  }

  function logLine(iid, source, d = new Date()) {
    const inst = S.instances[iid];
    const text = `${stamp(d)} ${frpLine(iid, d)}`;
    if (inst.source === 'docker' || source === 'file') return text;
    return `${stamp(d, true)} vps ${inst.service}[${iid === 'frps' ? 812 : 1047}]: ${text}`;
  }

  function startupLines(iid, source, d) {
    const inst = S.instances[iid];
    const run = hex(16);
    const wrap = (msg, offset) => {
      const t = new Date(d.getTime() + offset);
      const text = `${stamp(t)} ${msg}`;
      return inst.source === 'docker' || source === 'file' ? text : `${stamp(t, true)} vps ${inst.service}[${iid === 'frps' ? 812 : 1047}]: ${text}`;
    };
    if (inst.type === 'frps') {
      return [
        wrap(`[I] [frps/root.go:105] frps uses config file: /etc/frp/frps.toml`, 0),
        wrap(`[I] [server/service.go:237] frps tcp listen on 0.0.0.0:7000`, 3),
        wrap(`[I] [server/service.go:305] http service listen on 0.0.0.0:80`, 4),
        wrap(`[I] [server/service.go:319] https service listen on 0.0.0.0:443`, 4),
        wrap(`[I] [frps/root.go:114] frps started successfully`, 5),
        wrap(`[I] [server/service.go:359] dashboard listen on 0.0.0.0:7500`, 6),
      ];
    }
    const lines = [
      wrap(`[I] [sub/root.go:142] start frpc service for config file [/etc/frp/frpc.toml]`, 0),
      wrap(`[I] [client/service.go:295] try to connect to server...`, 2),
      wrap(`[I] [client/service.go:287] [${run}] login to server success, get run id [${run}]`, 140),
    ];
    proxiesOf(S.configs[iid]).forEach((p, i) => {
      lines.push(wrap(`[I] [proxy/proxy_manager.go:173] [${run}] proxy added: [${p.name}]`, 141 + i));
    });
    proxiesOf(S.configs[iid]).forEach((p, i) => {
      lines.push(wrap(`[I] [client/control.go:168] [${run}] [${p.name}] start proxy success`, 180 + i * 7));
    });
    return lines;
  }

  function history(iid, source, n) {
    if (!S.instances[iid]) return [];
    const running = S.status[iid]?.running;
    const start = new Date(Date.now() - (n + 5) * 47000);
    const lines = startupLines(iid, source, start);
    let t = start.getTime() + 60000;
    while (lines.length < n) {
      t += rnd(5000, 90000);
      if (t > Date.now()) break;
      lines.push(logLine(iid, source, new Date(t)));
    }
    if (!running) {
      const inst = S.instances[iid];
      lines.push(inst.source === 'docker'
        ? `${stamp(new Date())} [I] [frp] received signal terminated, exiting`
        : `${stamp(new Date(), true)} vps systemd[1]: ${inst.service}.service: Deactivated successfully.`);
    }
    return lines.slice(-n);
  }

  // ── Mises à jour frp (simulées) ───────────────────────────────────────────
  let updateLog = [];
  let updating = false;
  function clock() {
    const d = new Date();
    return `${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`;
  }
  async function runInstall(version, manual) {
    updating = true;
    updateLog = [];
    const log = (m) => updateLog.push(`[${clock()}] ${m}`);
    const running = Object.entries(S.instances).filter(([id, i]) => i.source === 'systemd' && S.status[id]?.running).map(([id]) => id);
    if (!manual) {
      log(`[INFO] Version : v${version} via github`);
      await sleep(600);
      log('[INFO] Tentative : github.com …');
      await sleep(1400);
      log('[OK] Téléchargé depuis github.com');
    }
    await sleep(400);
    log(M('[INFO] Arrêt des services frp …', '[INFO] Stopping frp services …'));
    running.forEach((id) => { S.status[id] = { ...S.status[id], active: 'inactive', running: false }; });
    pushStatus();
    await sleep(700);
    log(M(`[INFO] Stoppés : ${running.join(', ')}`, `[INFO] Stopped: ${running.join(', ')}`));
    log(M('[INFO] Extraction …', '[INFO] Extracting …'));
    await sleep(800);
    log('[INFO] frps → /usr/local/bin/frps');
    log('[INFO] frpc → /usr/local/bin/frpc');
    S.frp.installed = version;
    Object.values(S.instances).forEach((i) => { if (i.source === 'systemd') i.version = version; });
    await sleep(500);
    log(M(`[INFO] Redémarrage : ${running.join(', ')} …`, `[INFO] Restarting: ${running.join(', ')} …`));
    running.forEach((id) => { S.status[id] = { ...S.status[id], active: 'active', running: true }; });
    pushStatus();
    running.forEach((id) => log(`[OK] ${id}`));
    await sleep(300);
    log(M(`[OK] frp ${version} installé.`, `[OK] frp ${version} installed.`));
    updating = false;
  }

  // ── Faux serveur HTTP ─────────────────────────────────────────────────────
  const realFetch = window.fetch.bind(window);
  const unknown = () => [{ ok: false, msg: M('Instance inconnue', 'Unknown instance') }, 404];
  const routes = [];
  const on = (method, pattern, fn) => routes.push([method, pattern, fn]);

  on('POST', /^\/api\/login$/, () => ({ ok: true }));
  on('POST', /^\/api\/logout$/, () => ({ ok: true }));
  on('GET', /^\/api\/detect$/, () => ({ ok: true, instances: detect(), in_docker: false }));
  on('GET', /^\/api\/status$/, () => ({ ok: true, instances: detect(), installed_version: S.frp.installed, last_update_check: new Date().toISOString() }));
  on('GET', /^\/api\/nicknames$/, () => ({ ok: true, nicknames: S.nicknames }));

  on('POST', /^\/api\/nickname\/([^/]+)$/, (m, body) => {
    const nick = String(body.nickname || '').trim().slice(0, 64);
    if (nick) S.nicknames[m[1]] = nick; else delete S.nicknames[m[1]];
    save();
    return { ok: true, msg: M('Surnom mis à jour', 'Nickname updated') };
  });

  on('POST', /^\/api\/service\/([^/]+)\/([a-z]+)$/, async (m) => {
    const [, iid, action] = m;
    const inst = S.instances[iid];
    if (!inst) return unknown();
    const docker = inst.source === 'docker';
    const allowed = docker ? ['start', 'stop', 'restart'] : ['start', 'stop', 'restart', 'reload', 'enable', 'disable'];
    if (!allowed.includes(action)) return [{ ok: false, msg: M('Action invalide', 'Invalid action') }, 400];
    const st = S.status[iid];
    await sleep(350);
    if (action === 'start') Object.assign(st, { active: 'active', running: true });
    if (action === 'stop') Object.assign(st, { active: 'inactive', running: false });
    if (action === 'enable') st.enabled = true;
    if (action === 'disable') st.enabled = false;
    if (action === 'restart') {
      Object.assign(st, { active: 'activating', running: false });
      pushStatus();
      setTimeout(() => { Object.assign(st, { active: 'active', running: true }); pushStatus(); }, 1200);
      return { ok: true, msg: 'OK' };
    }
    pushStatus();
    return { ok: true, msg: 'OK' };
  });

  on('GET', /^\/api\/instance\/([^/]+)\/delete-info$/, (m) => {
    const inst = S.instances[m[1]];
    if (!inst) return unknown();
    if (inst.source !== 'docker') return { ok: true, image: null, config_path: null };
    return { ok: true, image: inst.image, config_path: `/srv/${inst.container_name}/frpc.toml` };
  });

  on('DELETE', /^\/api\/instance\/([^/]+)$/, async (m, body) => {
    const iid = m[1];
    const inst = S.instances[iid];
    if (!inst) return unknown();
    await sleep(500);
    const done = inst.source === 'docker'
      ? [M(`conteneur ${inst.container_name}`, `container ${inst.container_name}`)]
      : [`service ${inst.service}.service`];
    if (inst.source === 'docker' && body.delete_image) done.push(`image ${inst.image}`);
    if (body.delete_config) done.push(inst.config_path || `/srv/${inst.container_name}/frpc.toml`);
    delete S.instances[iid];
    delete S.status[iid];
    delete S.configs[iid];
    delete S.nicknames[iid];
    pushStatus();
    return { ok: true, msg: M(`Supprimé : ${done.join(', ')}`, `Deleted: ${done.join(', ')}`) };
  });

  on('GET', /^\/api\/config\/([^/]+)$/, (m) => {
    const inst = S.instances[m[1]];
    if (!inst) return unknown();
    return { ok: true, content: S.configs[m[1]] || '', exists: true, ...(inst.source === 'docker' ? { docker: true } : {}) };
  });

  on('POST', /^\/api\/config\/([^/]+)$/, async (m, body) => {
    const inst = S.instances[m[1]];
    if (!inst) return unknown();
    await sleep(250);
    S.configs[m[1]] = String(body.content || '');
    if (inst.type === 'frpc') inst.configured = /^\s*serverAddr\s*=\s*"[^"]+"/m.test(S.configs[m[1]]);
    pushStatus();
    const path = inst.config_path || `/srv/${inst.container_name}/frpc.toml`;
    return { ok: true, msg: M(`Sauvegardé : ${path}`, `Saved: ${path}`) };
  });

  on('GET', /^\/api\/logs\/([^/]+)$/, (m, body, url) => {
    if (!S.instances[m[1]]) return [{ ok: false }, 404];
    return { ok: true, content: history(m[1], url.searchParams.get('source'), 200).join('\n') };
  });

  on('GET', /^\/api\/manager\/config$/, () => ({ ok: true, config: { ...S.manager, nicknames: S.nicknames } }));
  on('POST', /^\/api\/manager\/config$/, (m, body) => {
    for (const k of ['bind_host', 'username']) if (k in body) S.manager[k] = String(body[k]).trim();
    for (const k of ['bind_port', 'session_timeout']) if (k in body) S.manager[k] = Number(body[k]);
    if (body.new_password) S.manager.has_password = true;
    save();
    return { ok: true, msg: M('Sauvegardé. Redémarrez frp-manager pour appliquer bind_host/port.', 'Saved. Restart frp-manager to apply bind_host/port.') };
  });

  on('GET', /^\/api\/panel\/version$/, () => ({
    ok: true, current: PANEL_VERSION, latest: PANEL_VERSION, release_url: `https://github.com/${REPO}/releases`,
    update_available: false, prerelease: false, repo: REPO, repo_configured: true, in_docker: false,
  }));
  on('POST', /^\/api\/panel\/update$/, () => ({ ok: false, msg: M('Le panel est déjà à jour.', 'The panel is already up to date.') }));
  on('GET', /^\/api\/panel\/update\/log$/, () => ({ ok: true, lines: [] }));

  on('GET', /^\/api\/update\/check$/, async () => {
    await sleep(400);
    return { ok: true, latest: FRP_LATEST, tag: `v${FRP_LATEST}`, installed: S.frp.installed, source: 'github', update_available: S.frp.installed !== FRP_LATEST };
  });
  on('POST', /^\/api\/update\/install$/, () => {
    if (updating) return { ok: false, msg: M('Mise à jour déjà en cours', 'Update already in progress') };
    runInstall(FRP_LATEST, false);
    return { ok: true };
  });
  on('POST', /^\/api\/update\/upload$/, (m, body) => {
    if (updating) return { ok: false, msg: M('Mise à jour déjà en cours', 'Update already in progress') };
    const version = String((body instanceof FormData && body.get('version')) || 'manual').replace(/^v/, '');
    updateLog = [];
    runInstall(version, true);
    return { ok: true };
  });
  on('GET', /^\/api\/update\/log$/, () => ({ ok: true, lines: updateLog }));
  on('GET', /^\/api\/connectivity$/, async () => {
    await sleep(900);
    return { ok: true, sources: { github: { ok: true, version: `v${FRP_LATEST}` }, 'github.com': { ok: true, version: `v${FRP_LATEST}` } } };
  });

  on('GET', /^\/api\/firewall$/, async () => {
    const known = knownPorts();
    await getCatalog(false).catch(() => {});
    const fw = S.firewall;
    return {
      ok: true, has_frps: hasFrps(), available: true, nft: 'nftables v1.0.9 (Old Doc Yak #3)',
      enabled: fw.enabled, rules: fw.rules, ports: known, active: fw.enabled && fw.rules.some((r) => r.enabled),
      counters: { ...S.counters }, asns: asnInfo(fw.rules), lists: listsInfo(fw.rules),
      client_ip: CLIENT_IP, client_verdicts: fw.enabled ? verdicts(CLIENT_IP, fw.rules, known) : [],
      panel_port: PANEL_PORT, in_docker: false,
    };
  });
  on('POST', /^\/api\/firewall\/install$/, () => ({ ok: true, msg: M('nftables est déjà installé.', 'nftables is already installed.') }));
  on('POST', /^\/api\/firewall$/, async (m, body) => {
    if (!hasFrps()) return [{ ok: false, msg: M('Le pare-feu ne filtre que les ports d\'un frps : aucun frps sur cette machine.', 'The firewall only filters the ports of an frps: no frps on this machine.') }, 400];
    let rules;
    try { rules = (body.rules || []).map(normalizeRule); } catch (e) { return [{ ok: false, msg: e.message }, 400]; }
    await sleep(500);
    S.firewall = { enabled: !!body.enabled, rules };
    rules.forEach((r) => { if (S.counters[r.id] == null) S.counters[r.id] = 0; });
    save();
    return { ok: true, rules, msg: S.firewall.enabled ? M('Pare-feu appliqué', 'Firewall applied') : M('Pare-feu désactivé : plus aucun filtrage', 'Firewall disabled: no filtering anymore') };
  });
  on('POST', /^\/api\/firewall\/test$/, (m, body) => {
    const ip = String(body.ip || '').trim();
    if (!isIp(ip)) return [{ ok: false, msg: M('Adresse IP invalide', 'Invalid IP address') }, 400];
    let rules;
    try { rules = (body.rules || []).map(normalizeRule); } catch (e) { return [{ ok: false, msg: e.message }, 400]; }
    return { ok: true, verdicts: verdicts(ip, rules, knownPorts()) };
  });
  on('GET', /^\/api\/firewall\/lists$/, async (m, body, url) => {
    const lists = await getCatalog(url.searchParams.get('fresh') === '1');
    return {
      ok: true, lists: [...lists].sort((a, b) => a.name.localeCompare(b.name)), fetched: catalogFetched || null,
      error: catalogError, browse_url: `https://github.com/${REPO}/tree/blocklists`,
    };
  });
  on('POST', /^\/api\/firewall\/lists\/publish$/, (m, body) => {
    const name = String(body.name || '').trim().slice(0, 60);
    if (!name) return [{ ok: false, msg: M('Donnez un nom à la liste', 'Give the list a name') }, 400];
    const sources = [];
    const skipped = [];
    for (const s of body.sources || []) {
      try {
        const n = normalizeSource(body.notes === false ? { ...s, note: '' } : s, name);
        if (n) sources.push(n);
      } catch (e) { skipped.push({ entry: s.asn ? `AS${s.asn}` : s.cidr, reason: e.message }); }
    }
    if (!sources.length) return [{ ok: false, msg: M('Aucune adresse publiable', 'No publishable address'), skipped }, 400];
    const id = name.normalize('NFKD').replace(/[^\x00-\x7f]/g, '').toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-|-$/g, '').slice(0, 40) || `liste-${hex(6)}`;
    const entry = {
      id, name, mode: body.mode === 'allow' ? 'allow' : 'block', description: String(body.description || '').trim().slice(0, 300),
      author: String(body.author || '').trim().slice(0, 40), updated: new Date().toISOString().slice(0, 10),
      sources: sources.map((s) => (s.asn ? `AS${s.asn}` : s.cidr) + (s.note ? `  # ${s.note}` : '')),
    };
    // Démo : pas d'issue pré-remplie (le catalogue réel accepte les listes sans relecture)
    return {
      ok: true, entry, json: JSON.stringify(entry, null, 2), issue_url: `https://github.com/${REPO}/tree/blocklists`,
      too_long: false, skipped, update: !!catalogById(id),
    };
  });
  on('GET', /^\/api\/firewall\/blocked$/, () => blockedPayload());

  async function handle(method, url, body) {
    for (const [m, pattern, fn] of routes) {
      if (m !== method) continue;
      const match = url.pathname.match(pattern);
      if (!match) continue;
      await sleep(rnd(60, 180));
      const res = await fn(match, body, url);
      return Array.isArray(res) ? res : [res, 200];
    }
    return [{ ok: false, msg: M('Non disponible dans la démo', 'Not available in the demo') }, 404];
  }

  window.fetch = async function demoFetch(input, init = {}) {
    const url = new URL(typeof input === 'string' ? input : input.url, location.href);
    if (url.origin !== location.origin || !url.pathname.startsWith('/api/')) return realFetch(input, init);
    const method = String(init.method || (typeof input === 'string' ? 'GET' : input.method) || 'GET').toUpperCase();
    let body = {};
    if (init.body instanceof FormData) body = init.body;
    else if (typeof init.body === 'string') { try { body = JSON.parse(init.body); } catch { body = {}; } }
    const [data, status] = await handle(method, url, body || {});
    return new Response(JSON.stringify(data), { status, headers: { 'Content-Type': 'application/json' } });
  };

  // ── Faux WebSocket ────────────────────────────────────────────────────────
  const RealWebSocket = window.WebSocket;

  class DemoSocket extends EventTarget {
    constructor(url) {
      super();
      this.url = String(url);
      this.readyState = 0;
      this.protocol = '';
      this._timer = null;
      setTimeout(() => {
        if (this.readyState !== 0) return;
        this.readyState = 1;
        this._emit('open', new Event('open'));
        this._start();
      }, rnd(40, 120));
    }

    _emit(type, event) {
      const handler = this[`on${type}`];
      if (typeof handler === 'function') handler.call(this, event);
      this.dispatchEvent(event);
    }

    _message(data) {
      if (this.readyState === 1) this._emit('message', new MessageEvent('message', { data }));
    }

    _start() {
      const u = new URL(this.url.replace(/^ws/, 'http'));
      const path = u.pathname;
      if (path.endsWith('/ws/status')) {
        statusSockets.add(this);
        this._message(JSON.stringify({ instances: detect(), in_docker: false }));
      } else if (path.includes('/ws/logs/')) {
        const iid = decodeURIComponent(path.split('/ws/logs/')[1] || '');
        const source = u.searchParams.get('source');
        const n = Number(u.searchParams.get('history') || 50);
        history(iid, source, n).forEach((l) => this._message(l));
        const tick = () => {
          if (this.readyState !== 1) return;
          if (S.instances[iid] && S.status[iid]?.running) this._message(logLine(iid, source));
          this._timer = setTimeout(tick, rnd(900, 4500));
        };
        this._timer = setTimeout(tick, rnd(800, 2000));
      } else if (path.endsWith('/ws/firewall')) {
        firewallSockets.add(this);
        this._message(JSON.stringify(blockedPayload()));
      }
    }

    send() { /* rien à envoyer au faux serveur */ }

    close() {
      if (this.readyState >= 2) return;
      this.readyState = 3;
      clearTimeout(this._timer);
      statusSockets.delete(this);
      firewallSockets.delete(this);
      this._emit('close', new CloseEvent('close', { code: 1000, wasClean: true }));
    }
  }
  DemoSocket.CONNECTING = 0;
  DemoSocket.OPEN = 1;
  DemoSocket.CLOSING = 2;
  DemoSocket.CLOSED = 3;

  window.WebSocket = function WebSocket(url, protocols) {
    return /\/ws\//.test(String(url)) ? new DemoSocket(url) : new RealWebSocket(url, protocols);
  };
  Object.assign(window.WebSocket, { CONNECTING: 0, OPEN: 1, CLOSING: 2, CLOSED: 3 });
  window.WebSocket.prototype = RealWebSocket.prototype;

  // ── Bandeau « démo » ──────────────────────────────────────────────────────
  function banner() {
    const style = document.createElement('style');
    style.textContent = `
      .demo-banner { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; justify-content: center;
        padding: 7px 16px; font-size: 13px; line-height: 1.4; text-align: center;
        background: var(--accent-soft); color: var(--text); border-bottom: 1px solid var(--border); }
      .demo-banner strong { color: var(--accent-text, var(--accent)); }
      .demo-banner a, .demo-banner button { font: inherit; color: var(--accent-text, var(--accent)); background: none;
        border: 0; padding: 0; cursor: pointer; text-decoration: underline; text-underline-offset: 2px; }
      .demo-banner .sep { opacity: .45; }
      body > .demo-banner { position: sticky; top: 0; z-index: 20; }
    `;
    document.head.append(style);
    const bar = document.createElement('div');
    bar.className = 'demo-banner';
    bar.setAttribute('role', 'note');
    const reset = document.createElement('button');
    reset.type = 'button';
    reset.textContent = M('Réinitialiser', 'Reset');
    reset.addEventListener('click', () => {
      try { sessionStorage.removeItem(STATE_KEY); } catch { /* ignoré */ }
      location.reload();
    });
    const gh = document.createElement('a');
    gh.href = `https://github.com/${REPO}`;
    gh.target = '_blank';
    gh.rel = 'noopener';
    gh.textContent = M('Installer FRP Manager', 'Install FRP Manager');
    const text = document.createElement('span');
    text.innerHTML = M('<strong>Démo</strong> : données fictives, rien n\'est réellement modifié.',
      '<strong>Demo</strong>: sample data, nothing is actually changed.');
    const sep = () => Object.assign(document.createElement('span'), { className: 'sep', textContent: '·' });
    bar.append(text, sep(), reset, sep(), gh);
    const main = document.querySelector('.main');
    if (main) main.prepend(bar);
    else document.body.prepend(bar);
  }

  load();
  save();
  setTimeout(tickFirewall, 3000);
  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', banner);
  else banner();
}());
