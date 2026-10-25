// Worker Cloudflare : récepteur des statistiques anonymes de FRP Manager.
// Cloudflare Worker: receiver of FRP Manager's anonymous statistics.
//
// Routes (wrangler.jsonc) : frp-manager.org/api/ping, frp-manager.org/prive et
// /prive/* (espace privé), et l'ancien nom d'hôte (OLD_HOST) en entier, dont les
// pages sont redirigées vers NEW_SITE. Le reste de frp-manager.org est servi par
// GitHub Pages, derrière Cloudflare.
// Routes (wrangler.jsonc): frp-manager.org/api/ping, frp-manager.org/prive and
// /prive/* (private area), and the whole old host name (OLD_HOST), whose pages are
// redirected to NEW_SITE. The rest of frp-manager.org is served by GitHub Pages,
// behind Cloudflare.
//
// Stockage : base D1 « STATS » (schema.sql), des compteurs par jour. L'adresse du
// visiteur ne sert qu'à limiter les abus : elle est salée et hachée, gardée pour le
// jour même seulement.
// Storage: D1 database "STATS" (schema.sql), daily counters. The visitor's address
// is only used to limit abuse: it is salted and hashed, kept for the same day only.
//
// Secrets : PRIVE_SHA256 (empreinte SHA-256 en hexadécimal du mot de passe de
// /prive/, par exemple « printf '%s' 'mot de passe' | sha256sum » ; sans elle
// l'espace privé répond 404), IP_SALT (sel du hachage des adresses).
// Secrets: PRIVE_SHA256 (hexadecimal SHA-256 hash of the /prive/ password, for
// example "printf '%s' 'password' | sha256sum"; without it the private area answers
// 404), IP_SALT (salt of the address hashing).

import {
  PER_IP_DAY, MAX_PER_DAY, MAX_BODY, PRIVE_MAX_FAILS, PRIVE_FAIL_WINDOW,
  pingKey, dayOf, oldestKept, redirectTarget, sameSecret, privateKey, basicPassword, retryAfter,
} from './logic.js';
import {
  GITHUB_RELEASES, GITHUB_TTL, GITHUB_ERROR_TTL, releasesFrom, statsData, renderPage,
} from './render.js';

const text = (status, body, headers = {}) => new Response(body, { status, headers: { 'Cache-Control': 'no-store', ...headers } });

async function sha256Hex(value) {
  const digest = await crypto.subtle.digest('SHA-256', new TextEncoder().encode(value));
  return [...new Uint8Array(digest)].map((b) => b.toString(16).padStart(2, '0')).join('');
}

async function ipHash(ip, day, salt) {
  const data = new TextEncoder().encode(`${salt}|${day}|${ip}`);
  const digest = await crypto.subtle.digest('SHA-256', data);
  return [...new Uint8Array(digest).slice(0, 8)].map((b) => b.toString(16).padStart(2, '0')).join('');
}

async function ping(request, env, ctx) {
  if (request.method !== 'POST') return text(405, '405 Method Not Allowed', { Allow: 'POST' });
  const raw = await request.text();
  const key = raw.length > MAX_BODY ? null : pingKey(raw);
  if (!key) return text(400, '400 Bad Request');

  const now = new Date();
  const day = dayOf(now);
  const ip = await ipHash(request.headers.get('CF-Connecting-IP') || 'inconnue', day, env.IP_SALT || '');

  // Limite par adresse : le compteur du jour est incrémenté d'abord, de façon atomique
  // Per-address limit: the day's counter is incremented first, atomically
  const seen = await env.STATS.prepare(
    'INSERT INTO ips (day, hash, n) VALUES (?1, ?2, 1) ON CONFLICT (day, hash) DO UPDATE SET n = n + 1 RETURNING n',
  ).bind(day, ip).first();
  if (seen && seen.n > PER_IP_DAY) return text(429, '429 Too Many Requests', { 'Retry-After': '3600' });

  const total = await env.STATS.prepare('SELECT COALESCE(SUM(n), 0) AS total FROM pings WHERE day = ?1').bind(day).first();
  if (total && total.total >= MAX_PER_DAY) return text(429, '429 Too Many Requests', { 'Retry-After': '3600' });

  await env.STATS.prepare(
    'INSERT INTO pings (day, key, n) VALUES (?1, ?2, 1) ON CONFLICT (day, key) DO UPDATE SET n = n + 1',
  ).bind(day, key).run();

  // Ménage : adresses des jours passés, compteurs au-delà de la durée gardée
  // Housekeeping: addresses of past days, counters beyond the kept duration
  ctx.waitUntil(env.STATS.batch([
    env.STATS.prepare('DELETE FROM ips WHERE day < ?1').bind(day),
    env.STATS.prepare('DELETE FROM pings WHERE day < ?1').bind(oldestKept(now)),
  ]));
  return new Response(null, { status: 204, headers: { 'Cache-Control': 'no-store' } });
}

// En-têtes de toute réponse de /prive/ / Headers of every /prive/ response
const ROBOTS = { 'X-Robots-Tag': 'noindex, nofollow' };
const PAGE_HEADERS = {
  ...ROBOTS,
  'Cache-Control': 'no-store',
  'Content-Security-Policy': "default-src 'none'; style-src 'unsafe-inline'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'",
  'X-Content-Type-Options': 'nosniff',
  'Referrer-Policy': 'no-referrer',
  'X-Frame-Options': 'DENY',
};

/**
 * Contrôle d'accès à /prive/ (authentification HTTP Basic, seul le mot de passe
 * compte) : null si la requête porte le bon mot de passe, sinon la réponse à renvoyer
 * (404 espace fermé, 429 trop de refus récents, 401 mot de passe demandé ou refusé).
 * Le compteur des refus est dans D1 (table fails) : un Worker n'a pas de mémoire
 * partagée fiable entre ses instances.
 * Access control of /prive/ (HTTP Basic authentication, only the password counts):
 * null if the request carries the right password, otherwise the response to send
 * (404 closed area, 429 too many recent refusals, 401 password asked or refused). The
 * refusal counter lives in D1 (fails table): a Worker has no reliable memory shared
 * between its instances.
 */
async function privateGate(request, env, key) {
  const now = Date.now();
  const since = now - PRIVE_FAIL_WINDOW * 1000;
  const recent = await env.STATS.prepare('SELECT COUNT(*) AS n, MIN(at) AS first FROM fails WHERE at > ?1').bind(since).first();
  if (recent && recent.n >= PRIVE_MAX_FAILS) {
    return text(429, '429 Too Many Requests', { ...ROBOTS, 'Retry-After': String(retryAfter(recent.first, now)) });
  }
  const password = basicPassword(request.headers.get('Authorization'));
  if (password !== null) {
    if (sameSecret(await sha256Hex(password), key)) return null;
    // Refus compté, et ménage des refus sortis de la fenêtre
    // Refusal counted, and housekeeping of the refusals out of the window
    await env.STATS.batch([
      env.STATS.prepare('DELETE FROM fails WHERE at <= ?1').bind(since),
      env.STATS.prepare('INSERT INTO fails (at) VALUES (?1)').bind(now),
    ]);
  }
  return text(401, '401 Unauthorized', { ...ROBOTS, 'WWW-Authenticate': 'Basic realm="FRP Manager", charset="UTF-8"' });
}

/**
 * Téléchargements des releases GitHub ({releases, error, fetched_at}), gardés 30 min
 * dans caches.default (5 min en cas d'échec) : l'API sans jeton accepte 60 appels par heure.
 * GitHub release downloads ({releases, error, fetched_at}), kept 30 min in
 * caches.default (5 min on failure): the API without a token allows 60 calls an hour.
 */
async function githubReleases(origin) {
  const cache = globalThis.caches?.default;
  const cacheKey = new Request(`${origin}/prive/github-releases`);
  if (cache) {
    const hit = await cache.match(cacheKey);
    if (hit) return hit.json();
  }
  const fetchedAt = new Date().toISOString();
  let result;
  try {
    const r = await fetch(GITHUB_RELEASES, {
      headers: { Accept: 'application/vnd.github+json', 'User-Agent': 'frp-manager-stats-worker' },
      signal: AbortSignal.timeout(10000),
    });
    if (!r.ok) {
      const limited = (r.status === 403 || r.status === 429) && r.headers.get('X-RateLimit-Remaining') === '0';
      result = { releases: null, error: limited ? "limite d'appels de l'API GitHub atteinte" : `GitHub a répondu HTTP ${r.status}` };
    } else {
      const releases = releasesFrom(await r.json());
      result = releases ? { releases, error: '' } : { releases: null, error: 'réponse inattendue de GitHub' };
    }
  } catch (e) {
    result = { releases: null, error: `GitHub injoignable (${e && e.message ? e.message : e})` };
  }
  result.fetched_at = fetchedAt;
  if (cache) {
    await cache.put(cacheKey, new Response(JSON.stringify(result), {
      headers: { 'Content-Type': 'application/json', 'Cache-Control': `max-age=${result.releases ? GITHUB_TTL : GITHUB_ERROR_TTL}` },
    }));
  }
  return result;
}

async function prive(request, env, url) {
  const key = privateKey(env.PRIVE_SHA256);
  if (!key) return text(404, '404 Not Found');
  if (request.method !== 'GET' && request.method !== 'HEAD') {
    return text(405, '405 Method Not Allowed', { ...ROBOTS, Allow: 'GET, HEAD' });
  }
  if (url.pathname === '/prive') return Response.redirect(`${url.origin}/prive/${url.search}`, 301);
  const denied = await privateGate(request, env, key);
  if (denied) return denied;

  const json = url.pathname === '/prive/stats.json';
  if (!json && url.pathname !== '/prive/') return text(404, '404 Not Found', ROBOTS);
  const [{ results }, github] = await Promise.all([
    env.STATS.prepare('SELECT day, key, n FROM pings ORDER BY day, key').all(),
    githubReleases(url.origin),
  ]);
  const data = statsData(results, new Date(), github);
  const body = json ? JSON.stringify(data) : renderPage(data);
  const type = json ? 'application/json; charset=utf-8' : 'text/html; charset=utf-8';
  return new Response(request.method === 'HEAD' ? null : body, { headers: { ...PAGE_HEADERS, 'Content-Type': type } });
}

export default {
  async fetch(request, env, ctx) {
    const url = new URL(request.url);
    const path = url.pathname.replace(/\/+$/, '') || '/';
    if (path === '/api/ping') return ping(request, env, ctx);

    const to = redirectTarget(request.url, request.method, env.OLD_HOST, env.NEW_SITE);
    if (to) return Response.redirect(to, 301);

    if (url.pathname === '/prive' || url.pathname.startsWith('/prive/')) return prive(request, env, url);
    return text(404, '404 Not Found');
  },
};
