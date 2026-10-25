import test from 'node:test';
import assert from 'node:assert/strict';
import {
  countedVersion, pingKey, dayOf, oldestKept, redirectTarget, sameSecret, privateKey, basicPassword, retryAfter,
  KEEP_DAYS, MAX_BODY, PRIVE_FAIL_WINDOW,
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

test('privateKey : 64 caractères hexadécimaux, sinon espace fermé', () => {
  const h = 'AB'.repeat(32);
  assert.equal(privateKey(h), 'ab'.repeat(32));
  assert.equal(privateKey(` ${h}\n`), 'ab'.repeat(32));
  for (const bad of [undefined, null, '', 'abc', 'g'.repeat(64), 'a'.repeat(63), 'a'.repeat(65)]) {
    assert.equal(privateKey(bad), null, String(bad));
  }
});

test('basicPassword : seul le mot de passe compte', () => {
  const basic = (s) => `Basic ${Buffer.from(s, 'utf8').toString('base64')}`;
  assert.equal(basicPassword(basic('moi:secret')), 'secret');
  assert.equal(basicPassword(basic(':secret')), 'secret');
  assert.equal(basicPassword(basic('x:a:b')), 'a:b');
  assert.equal(basicPassword(basic('x:')), '');
  assert.equal(basicPassword(basic('x:mot de passe é')), 'mot de passe é');
  assert.equal(basicPassword(`basic ${Buffer.from('x:y').toString('base64')}`), 'y');
  for (const bad of [undefined, '', 'Bearer abc', 'Basic', 'Basic %%%', basic('sans-deux-points'), `Basic ${Buffer.from([0x78, 0x3a, 0xff]).toString('base64')}`]) {
    assert.equal(basicPassword(bad), null, String(bad));
  }
});

test('retryAfter : jusqu\'à la sortie du plus ancien refus, entre 1 et la fenêtre', () => {
  const now = 1_800_000_000_000;
  assert.equal(retryAfter(now, now), PRIVE_FAIL_WINDOW);
  assert.equal(retryAfter(now - 540_000, now), 60);
  assert.equal(retryAfter(now - 599_999, now), 1);
  assert.equal(retryAfter(now - 700_000, now), 1);
  assert.equal(retryAfter(null, now), PRIVE_FAIL_WINDOW);
  assert.equal(retryAfter(undefined, now), PRIVE_FAIL_WINDOW);
});

// Chemins du Worker qui ne touchent pas à la base
// Worker paths that do not touch the database
const env = { OLD_HOST: 'frp-manager.gogow.fr', NEW_SITE: 'https://frp-manager.org' };
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
  r = await call('https://frp-manager.gogow.fr/prive/');
  assert.equal(r.status, 301);
  assert.equal(r.headers.get('Location'), 'https://frp-manager.org/prive/');
});
