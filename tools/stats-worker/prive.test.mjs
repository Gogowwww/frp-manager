import test from 'node:test';
import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import worker from './worker.js';
import { PRIVE_MAX_FAILS, PRIVE_FAIL_WINDOW } from './logic.js';
import { GITHUB_RELEASES } from './render.js';

// Fausse base D1 en mémoire : ne connaît que les requêtes de l'espace privé, et
// échoue sur toute autre (le test le verra si le Worker change de requête).
// In-memory fake D1 database: only knows the private area's queries, and fails on
// any other (the test will notice if the Worker changes a query).
function fakeD1(pings = []) {
  const db = { pings, fails: [] };
  const exec = (sql, args) => {
    if (sql === 'SELECT day, key, n FROM pings ORDER BY day, key') return [...db.pings];
    if (sql === 'SELECT COUNT(*) AS n, MIN(at) AS first FROM fails WHERE at > ?1') {
      const recent = db.fails.filter((at) => at > args[0]);
      return [{ n: recent.length, first: recent.length ? Math.min(...recent) : null }];
    }
    if (sql === 'DELETE FROM fails WHERE at <= ?1') {
      db.fails = db.fails.filter((at) => at > args[0]);
      return [];
    }
    if (sql === 'INSERT INTO fails (at) VALUES (?1)') {
      db.fails.push(args[0]);
      return [];
    }
    throw new Error(`requête inattendue : ${sql}`);
  };
  const statement = (sql, args = []) => ({
    bind: (...values) => statement(sql, values),
    first: async () => exec(sql, args)[0] ?? null,
    all: async () => ({ results: exec(sql, args), success: true }),
    run: async () => { exec(sql, args); return { success: true }; },
    exec: () => exec(sql, args),
  });
  return {
    db,
    prepare: (sql) => statement(sql),
    batch: async (list) => list.map((s) => ({ results: s.exec(), success: true })),
  };
}

const PASSWORD = 'un mot de passe é';
const HASH = createHash('sha256').update(PASSWORD, 'utf8').digest('hex');
const basic = (user, pass) => ({ Authorization: `Basic ${Buffer.from(`${user}:${pass}`, 'utf8').toString('base64')}` });
const ctx = { waitUntil() {} };
const PINGS = [
  { day: new Date().toISOString().slice(0, 10), key: '0.4.1|amd64|apt', n: 3 },
  { day: '2026-01-01', key: '0.3.0|arm64|docker', n: 2 },
];
const RELEASES = [
  { tag_name: 'v0.4.1', prerelease: false, published_at: '2026-10-25T10:00:00Z', assets: [{ name: 'frp-manager.zip', download_count: 40 }] },
];

// Faux GitHub et faux caches.default, remis à zéro par chaque test
// Fake GitHub and fake caches.default, reset by each test
let github;
let cacheStore;
globalThis.fetch = async (url, init) => {
  github.calls.push({ url: String(url), init });
  return github.respond();
};
globalThis.caches = {
  default: {
    match: async (req) => (cacheStore.has(req.url) ? new Response(cacheStore.get(req.url)) : undefined),
    put: async (req, res) => { cacheStore.set(req.url, await res.text()); },
  },
};

function setup(opts = {}) {
  const hash = 'hash' in opts ? opts.hash : HASH;
  github = { calls: [], respond: opts.respond || (() => Response.json(RELEASES)) };
  cacheStore = new Map();
  const STATS = fakeD1([...PINGS]);
  const env = { OLD_HOST: 'frp-manager.gogow.fr', NEW_SITE: 'https://frp-manager.org', PRIVE_SHA256: hash, STATS };
  const call = (path, init = {}) => worker.fetch(new Request(`https://frp-manager.org${path}`, init), env, ctx);
  return { env, call, db: STATS.db };
}

test('espace fermé : 404 sans empreinte valide', async () => {
  for (const hash of [undefined, '', 'abc', 'z'.repeat(64)]) {
    const { call } = setup({ hash });
    for (const path of ['/prive/', '/prive', '/prive/stats.json']) {
      const r = await call(path, { headers: basic('x', PASSWORD) });
      assert.equal(r.status, 404, `${hash} ${path}`);
    }
  }
});

test('401 sans identifiants, sans compter de refus', async () => {
  const { call, db } = setup();
  const r = await call('/prive/');
  assert.equal(r.status, 401);
  assert.equal(r.headers.get('WWW-Authenticate'), 'Basic realm="FRP Manager", charset="UTF-8"');
  assert.equal(r.headers.get('Cache-Control'), 'no-store');
  assert.equal(r.headers.get('X-Robots-Tag'), 'noindex, nofollow');
  assert.equal(db.fails.length, 0);
  assert.equal(github.calls.length, 0);
});

test('401 avec un mauvais mot de passe, refus compté', async () => {
  const { call, db } = setup();
  let r = await call('/prive/', { headers: basic('admin', 'faux') });
  assert.equal(r.status, 401);
  assert.equal(r.headers.get('WWW-Authenticate'), 'Basic realm="FRP Manager", charset="UTF-8"');
  r = await call('/prive/stats.json', { headers: { Authorization: 'Bearer quelque-chose' } });
  assert.equal(r.status, 401);
  assert.equal(db.fails.length, 1);
});

test('200 avec le bon mot de passe, quel que soit l\'identifiant', async () => {
  const { call } = setup();
  for (const user of ['', 'admin', 'n importe qui']) {
    const r = await call('/prive/', { headers: basic(user, PASSWORD) });
    assert.equal(r.status, 200, user);
    assert.equal(r.headers.get('Content-Type'), 'text/html; charset=utf-8');
    assert.equal(r.headers.get('Cache-Control'), 'no-store');
    assert.equal(r.headers.get('X-Robots-Tag'), 'noindex, nofollow');
    const csp = r.headers.get('Content-Security-Policy');
    assert.ok(csp.startsWith("default-src 'none'; style-src 'unsafe-inline'; base-uri 'none'; form-action 'none'"), csp);
    const html = await r.text();
    assert.ok(html.includes('Espace privé'));
    assert.ok(html.includes('v0.4.1'));
  }
  // Une seule requête à GitHub grâce au cache, avec un User-Agent
  // A single request to GitHub thanks to the cache, with a User-Agent
  assert.equal(github.calls.length, 1);
  assert.equal(github.calls[0].url, GITHUB_RELEASES);
  assert.ok(new Headers(github.calls[0].init.headers).get('User-Agent'));
});

test('/prive/stats.json : les mêmes données en JSON', async () => {
  const { call } = setup();
  const r = await call('/prive/stats.json', { headers: basic('x', PASSWORD) });
  assert.equal(r.status, 200);
  assert.equal(r.headers.get('Content-Type'), 'application/json; charset=utf-8');
  assert.equal(r.headers.get('Cache-Control'), 'no-store');
  const d = await r.json();
  assert.equal(d.pings.total, 5);
  assert.equal(d.pings.today, 3);
  assert.deepEqual(d.releases_total, { zip: 40, deb: 0, total: 40 });
  assert.equal(d.releases[0].tag, 'v0.4.1');
});

test('redirection, méthode, chemin inconnu, HEAD', async () => {
  const { call } = setup();
  let r = await call('/prive?x=1');
  assert.equal(r.status, 301);
  assert.equal(r.headers.get('Location'), 'https://frp-manager.org/prive/?x=1');
  r = await call('/prive/', { method: 'POST', headers: basic('x', PASSWORD) });
  assert.equal(r.status, 405);
  assert.equal(r.headers.get('Allow'), 'GET, HEAD');
  r = await call('/prive/autre', { headers: basic('x', PASSWORD) });
  assert.equal(r.status, 404);
  r = await call('/prive/autre');
  assert.equal(r.status, 401);
  r = await call('/prive/', { method: 'HEAD', headers: basic('x', PASSWORD) });
  assert.equal(r.status, 200);
  assert.equal(await r.text(), '');
});

test('GitHub en échec : la page s\'affiche avec un message', async () => {
  const { call } = setup({ respond: () => new Response('{"message":"API rate limit exceeded"}', { status: 403, headers: { 'X-RateLimit-Remaining': '0' } }) });
  let r = await call('/prive/', { headers: basic('x', PASSWORD) });
  assert.equal(r.status, 200);
  let html = await r.text();
  assert.ok(html.includes('Téléchargements indisponibles : limite d&#39;appels de l&#39;API GitHub atteinte'));
  assert.ok(html.includes('Envois des panels'));

  const down = setup({ respond: () => { throw new TypeError('réseau coupé'); } });
  r = await down.call('/prive/', { headers: basic('x', PASSWORD) });
  assert.equal(r.status, 200);
  html = await r.text();
  assert.ok(html.includes('GitHub injoignable (réseau coupé)'));
  r = await down.call('/prive/stats.json', { headers: basic('x', PASSWORD) });
  const d = await r.json();
  assert.equal(d.releases, null);
  assert.equal(d.releases_error, 'GitHub injoignable (réseau coupé)');
  assert.equal(github.calls.length, 1); // échec gardé en cache / failure kept in cache
});

test(`429 après ${PRIVE_MAX_FAILS} refus en ${PRIVE_FAIL_WINDOW / 60} minutes, sans rien vérifier`, async () => {
  const { call, db } = setup();
  for (let i = 0; i < PRIVE_MAX_FAILS; i += 1) {
    const r = await call('/prive/', { headers: basic('x', `faux ${i}`) });
    assert.equal(r.status, 401, `essai ${i}`);
  }
  assert.equal(db.fails.length, PRIVE_MAX_FAILS);
  // Même le bon mot de passe est refusé, et rien n'est plus compté
  // Even the right password is refused, and nothing more is counted
  for (const headers of [basic('x', PASSWORD), basic('x', 'encore faux'), {}]) {
    const r = await call('/prive/', { headers });
    assert.equal(r.status, 429);
    const wait = Number(r.headers.get('Retry-After'));
    assert.ok(wait >= 1 && wait <= PRIVE_FAIL_WINDOW, String(wait));
    assert.equal(r.headers.get('X-Robots-Tag'), 'noindex, nofollow');
  }
  assert.equal(db.fails.length, PRIVE_MAX_FAILS);
  assert.equal(github.calls.length, 0);

  // Les refus sortis de la fenêtre ne comptent plus, et sont effacés au refus suivant
  // Refusals out of the window no longer count, and are deleted on the next refusal
  db.fails = db.fails.map((at) => at - PRIVE_FAIL_WINDOW * 1000);
  let r = await call('/prive/', { headers: basic('x', PASSWORD) });
  assert.equal(r.status, 200);
  r = await call('/prive/', { headers: basic('x', 'faux') });
  assert.equal(r.status, 401);
  assert.equal(db.fails.length, 1);
});
