import test from 'node:test';
import assert from 'node:assert/strict';
import {
  countedVersion, pingKey, dayOf, oldestKept, redirectTarget, sameSecret, KEEP_DAYS, MAX_BODY,
} from './logic.js';
import worker from './worker.js';

test('countedVersion regroupe les instantanés de développement', () => {
  assert.equal(countedVersion('0.4.1'), '0.4.1');
  assert.equal(countedVersion('0.4.1+dev.22.g7352f82'), '0.4.1+dev');
  assert.equal(countedVersion('0.4.1+autre'), '0.4.1');
  assert.equal(countedVersion('0.5.0-rc.1'), '0.5.0-rc.1');
});

test('pingKey accepte un envoi valide', () => {
  assert.equal(pingKey('{"version":"0.4.1","arch":"amd64","install":"apt"}'), '0.4.1|amd64|apt');
  assert.equal(pingKey('{"version":"0.4.1+dev.3.gabc","arch":"arm64","install":"docker"}'), '0.4.1+dev|arm64|docker');
});

test('pingKey refuse tout le reste', () => {
  const bad = [
    '', 'pas du json', 'null', '[]', '"x"',
    '{"version":"x","arch":"amd64","install":"apt"}',
    '{"version":"0.4.1","arch":"mips","install":"apt"}',
    '{"version":"0.4.1","arch":"amd64","install":"snap"}',
    '{"version":"0.4.1","arch":"amd64"}',
    '{"version":4,"arch":"amd64","install":"apt"}',
    `{"version":"0.4.1","arch":"amd64","install":"apt","pad":"${'a'.repeat(MAX_BODY)}"}`,
  ];
  for (const b of bad) assert.equal(pingKey(b), null, b.slice(0, 40));
  assert.equal(pingKey(undefined), null);
});

test('dayOf et oldestKept', () => {
  const d = new Date('2026-10-25T23:59:59Z');
  assert.equal(dayOf(d), '2026-10-25');
  assert.equal(oldestKept(d), dayOf(new Date(d.getTime() - KEEP_DAYS * 86400000)));
});

test('redirectTarget : seulement l\'ancien nom d\'hôte, en GET et HEAD, jamais /api/ping', () => {
  const old = 'frp-manager.gogow.fr';
  const site = 'https://frp-manager.org/';
  assert.equal(redirectTarget('https://frp-manager.gogow.fr/docs/install/?a=1', 'GET', old, site), 'https://frp-manager.org/docs/install/?a=1');
  assert.equal(redirectTarget('https://FRP-Manager.gogow.fr./', 'HEAD', old, site), 'https://frp-manager.org/');
  assert.equal(redirectTarget('https://frp-manager.gogow.fr/api/ping', 'GET', old, site), null);
  assert.equal(redirectTarget('https://frp-manager.gogow.fr/api/ping/', 'GET', old, site), null);
  assert.equal(redirectTarget('https://frp-manager.gogow.fr/x', 'POST', old, site), null);
  assert.equal(redirectTarget('https://frp-manager.org/x', 'GET', old, site), null);
});

test('sameSecret', () => {
  assert.equal(sameSecret('abc', 'abc'), true);
  assert.equal(sameSecret('abc', 'abd'), false);
  assert.equal(sameSecret('abc', 'abcd'), false);
  assert.equal(sameSecret('', ''), true);
});

// Chemins du Worker qui ne touchent pas à la base
// Worker paths that do not touch the database
const env = { OLD_HOST: 'frp-manager.gogow.fr', NEW_SITE: 'https://frp-manager.org', STATS_TOKEN: '' };
const ctx = { waitUntil() {} };
const call = (url, init) => worker.fetch(new Request(url, init), env, ctx);

test('Worker : redirection, méthode, corps, route cachée', async () => {
  let r = await call('https://frp-manager.gogow.fr/docs/');
  assert.equal(r.status, 301);
  assert.equal(r.headers.get('Location'), 'https://frp-manager.org/docs/');
  r = await call('https://frp-manager.org/api/ping');
  assert.equal(r.status, 405);
  assert.equal(r.headers.get('Allow'), 'POST');
  r = await call('https://frp-manager.org/api/ping', { method: 'POST', body: '{"version":"x"}' });
  assert.equal(r.status, 400);
  r = await call('https://frp-manager.gogow.fr/api/ping', { method: 'POST', body: 'nope' });
  assert.equal(r.status, 400);
  r = await call('https://frp-manager.org/stats.json');
  assert.equal(r.status, 404);
  r = await call('https://frp-manager.org/autre');
  assert.equal(r.status, 404);
});

test('Worker : /stats.json demande le jeton', async () => {
  const withToken = { ...env, STATS_TOKEN: 'secret' };
  let r = await worker.fetch(new Request('https://frp-manager.org/stats.json'), withToken, ctx);
  assert.equal(r.status, 401);
  r = await worker.fetch(new Request('https://frp-manager.org/stats.json', { headers: { Authorization: 'Bearer faux' } }), withToken, ctx);
  assert.equal(r.status, 401);
});
