// Worker Cloudflare : récepteur des statistiques anonymes de FRP Manager.
// Cloudflare Worker: receiver of FRP Manager's anonymous statistics.
//
// Routes (wrangler.jsonc) : frp-manager.org/api/ping, et l'ancien nom d'hôte
// (OLD_HOST) en entier, dont les pages sont redirigées vers NEW_SITE. Le reste de
// frp-manager.org est servi par GitHub Pages, derrière Cloudflare.
// Routes (wrangler.jsonc): frp-manager.org/api/ping, and the whole old host name
// (OLD_HOST), whose pages are redirected to NEW_SITE. The rest of frp-manager.org
// is served by GitHub Pages, behind Cloudflare.
//
// Stockage : base D1 « STATS » (schema.sql), des compteurs par jour. L'adresse du
// visiteur ne sert qu'à limiter les abus : elle est salée et hachée, gardée pour le
// jour même seulement.
// Storage: D1 database "STATS" (schema.sql), daily counters. The visitor's address
// is only used to limit abuse: it is salted and hashed, kept for the same day only.
//
// Secrets : STATS_TOKEN (lecture de /stats.json ; sans lui la route n'existe pas),
// IP_SALT (sel du hachage des adresses).
// Secrets: STATS_TOKEN (read /stats.json; without it the route does not exist),
// IP_SALT (salt of the address hashing).

import {
  KEEP_DAYS, PER_IP_DAY, MAX_PER_DAY, MAX_BODY,
  pingKey, dayOf, oldestKept, redirectTarget, sameSecret,
} from './logic.js';

const text = (status, body, headers = {}) => new Response(body, { status, headers: { 'Cache-Control': 'no-store', ...headers } });

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

async function stats(request, env) {
  const got = (request.headers.get('Authorization') || '').replace(/^Bearer /, '');
  if (!sameSecret(got, env.STATS_TOKEN)) {
    return text(401, '401 Unauthorized', { 'WWW-Authenticate': 'Bearer realm="frp-manager-stats"' });
  }
  const { results } = await env.STATS.prepare('SELECT day, key, n FROM pings ORDER BY day, key').all();
  const days = {};
  for (const r of results) (days[r.day] ||= {})[r.key] = r.n;
  return new Response(JSON.stringify({ days, today: dayOf(new Date()), keep_days: KEEP_DAYS }), {
    headers: { 'Content-Type': 'application/json', 'Cache-Control': 'no-store' },
  });
}

export default {
  async fetch(request, env, ctx) {
    const url = new URL(request.url);
    const path = url.pathname.replace(/\/+$/, '') || '/';
    if (path === '/api/ping') return ping(request, env, ctx);

    const to = redirectTarget(request.url, request.method, env.OLD_HOST, env.NEW_SITE);
    if (to) return Response.redirect(to, 301);

    if (path === '/stats.json' && env.STATS_TOKEN) return stats(request, env);
    return text(404, '404 Not Found');
  },
};
