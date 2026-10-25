// Règles pures du récepteur de statistiques (sans entrée-sortie), pour les tester.
// Pure rules of the statistics receiver (no I/O), so they can be tested.
//
// Un panel dont l'administrateur l'a accepté envoie une fois par jour
// {"version", "arch", "install"} en POST sur /api/ping. Seuls des compteurs par
// jour sont gardés : ni adresse IP, ni identifiant, ni heure.
// A panel whose administrator agreed sends {"version", "arch", "install"} with
// POST to /api/ping once a day. Only daily counters are kept: no IP address, no
// identifier, no time.

export const KEEP_DAYS = 400; // jours gardés / days kept
export const PER_IP_DAY = 30; // signaux par adresse et par jour / pings per address per day
export const MAX_PER_DAY = 5000; // signaux acceptés par jour, tous confondus / accepted pings per day, all together
export const MAX_BODY = 1024; // octets / bytes

const VERSION_RE = /^[0-9]{1,4}\.[0-9]{1,4}\.[0-9]{1,4}(-[0-9A-Za-z.]{1,20})?(\+[0-9A-Za-z.-]{1,60})?$/;
const ARCHS = new Set(['amd64', 'arm64', 'arm']);
const INSTALLS = new Set(['script', 'apt', 'docker']);

/**
 * Version telle que comptée : les instantanés de développement (+dev.<n>.g<commit>)
 * sont regroupés sous « X.Y.Z+dev », toute autre métadonnée de build est ignorée.
 * Version as counted: development snapshots (+dev.<n>.g<commit>) are grouped under
 * "X.Y.Z+dev", any other build metadata is dropped.
 */
export function countedVersion(v) {
  const i = v.indexOf('+');
  if (i < 0) return v;
  return v.slice(i + 1).startsWith('dev') ? `${v.slice(0, i)}+dev` : v.slice(0, i);
}

/**
 * Lit le corps d'un envoi : renvoie la clé du compteur « version|arch|install », ou
 * null si le corps est invalide (trop gros, pas du JSON, valeur inconnue).
 * Reads the body of a ping: returns the counter key "version|arch|install", or null
 * if the body is invalid (too big, not JSON, unknown value).
 */
export function pingKey(text) {
  if (typeof text !== 'string' || text.length > MAX_BODY) return null;
  let body;
  try { body = JSON.parse(text); } catch { return null; }
  if (!body || typeof body !== 'object' || Array.isArray(body)) return null;
  const { version, arch, install } = body;
  if (typeof version !== 'string' || !VERSION_RE.test(version)) return null;
  if (!ARCHS.has(arch) || !INSTALLS.has(install)) return null;
  return `${countedVersion(version)}|${arch}|${install}`;
}

/** Jour UTC « AAAA-MM-JJ » d'une date. / UTC day "YYYY-MM-DD" of a date. */
export const dayOf = (date) => date.toISOString().slice(0, 10);

/** Premier jour encore gardé. / First day still kept. */
export function oldestKept(date) {
  return dayOf(new Date(date.getTime() - KEEP_DAYS * 86400000));
}

/**
 * Où rediriger une requête faite sur l'ancien nom d'hôte (null : ne pas rediriger).
 * Seuls GET et HEAD sont redirigés, jamais /api/ping (un POST deviendrait un GET).
 * Where to redirect a request made on the old host name (null: do not redirect).
 * Only GET and HEAD are redirected, never /api/ping (a POST would become a GET).
 */
export function redirectTarget(url, method, oldHost, newSite) {
  const u = new URL(url);
  if (u.hostname.toLowerCase().replace(/\.$/, '') !== oldHost.toLowerCase()) return null;
  if (method !== 'GET' && method !== 'HEAD') return null;
  if (u.pathname.replace(/\/+$/, '') === '/api/ping') return null;
  return `${newSite.replace(/\/+$/, '')}${u.pathname}${u.search}`;
}

/** Comparaison de deux chaînes sans fuite de temps. / Constant-time comparison of two strings. */
export function sameSecret(a, b) {
  const x = new TextEncoder().encode(a);
  const y = new TextEncoder().encode(b);
  let diff = x.length ^ y.length;
  for (let i = 0; i < Math.max(x.length, y.length); i += 1) diff |= (x[i] || 0) ^ (y[i] || 0);
  return diff === 0;
}

// Espace privé (/prive/) : au-delà de PRIVE_MAX_FAILS mots de passe refusés en
// PRIVE_FAIL_WINDOW secondes, tous visiteurs confondus, il répond 429 sans rien vérifier.
// Private area (/prive/): beyond PRIVE_MAX_FAILS refused passwords within
// PRIVE_FAIL_WINDOW seconds, all visitors together, it answers 429 without checking.
export const PRIVE_MAX_FAILS = 10;
export const PRIVE_FAIL_WINDOW = 600; // secondes / seconds

/**
 * Empreinte SHA-256 du mot de passe de /prive/ (64 caractères hexadécimaux, rendus en
 * minuscules), ou null si elle est absente ou invalide : l'espace est alors fermé.
 * SHA-256 hash of the /prive/ password (64 hexadecimal characters, lowercased), or null
 * if it is missing or invalid: the area is then closed.
 */
export function privateKey(hex) {
  if (typeof hex !== 'string') return null;
  const h = hex.trim().toLowerCase();
  return /^[0-9a-f]{64}$/.test(h) ? h : null;
}

/**
 * Mot de passe d'un en-tête « Authorization: Basic … » (l'identifiant est ignoré), ou
 * null si l'en-tête manque ou est mal formé (base64 ou UTF-8 invalide, pas de « : »).
 * Password of an "Authorization: Basic …" header (the user name is ignored), or null if
 * the header is missing or malformed (invalid base64 or UTF-8, no ":").
 */
export function basicPassword(header) {
  const m = /^Basic +([A-Za-z0-9+/]+={0,2}) *$/i.exec(typeof header === 'string' ? header : '');
  if (!m) return null;
  let text;
  try {
    const bytes = Uint8Array.from(atob(m[1]), (c) => c.charCodeAt(0));
    text = new TextDecoder('utf-8', { fatal: true }).decode(bytes);
  } catch {
    return null;
  }
  const i = text.indexOf(':');
  return i < 0 ? null : text.slice(i + 1);
}

/**
 * Secondes à attendre (Retry-After) quand l'espace privé est bloqué : jusqu'à ce que le
 * plus ancien refus de la fenêtre en sorte, au moins 1.
 * Seconds to wait (Retry-After) while the private area is blocked: until the oldest
 * refusal of the window leaves it, at least 1.
 */
export function retryAfter(firstFailMs, nowMs) {
  const first = typeof firstFailMs === 'number' ? firstFailMs : NaN;
  const left = Math.ceil((first + PRIVE_FAIL_WINDOW * 1000 - nowMs) / 1000);
  return Number.isFinite(left) ? Math.min(PRIVE_FAIL_WINDOW, Math.max(1, left)) : PRIVE_FAIL_WINDOW;
}
