import test from 'node:test';
import assert from 'node:assert/strict';
import {
  escapeHtml, lastDays, percent, compareVersions, summarize, releasesFrom, releaseTotals, statsData,
  formatInt, formatPct, frDate, frDateTime, renderPage,
} from './render.js';
import { KEEP_DAYS } from './logic.js';

const TODAY = '2026-10-25';
const ROWS = [
  { day: '2026-09-20', key: '0.3.0|arm|script', n: 10 }, // hors des 30 jours / outside the 30 days
  { day: '2026-10-01', key: '0.4.1+dev|amd64|apt', n: 4 },
  { day: '2026-10-19', key: '0.4.0|arm64|docker', n: 5 },
  { day: '2026-10-20', key: '0.4.1|amd64|script', n: 2 },
  { day: '2026-10-25', key: '0.4.1|amd64|apt', n: 3 },
  { day: '2026-10-25', key: '0.4.0|arm64|docker', n: 1 },
  { day: '2026-10-24', key: '0.4.1|amd64|apt', n: 0 }, // ignorée / ignored
  { day: '2026-10-24', key: '0.4.1|amd64|apt', n: 'x' }, // ignorée / ignored
];

test('escapeHtml', () => {
  assert.equal(escapeHtml('<a href="x">l\'été & co</a>'), '&lt;a href=&quot;x&quot;&gt;l&#39;été &amp; co&lt;/a&gt;');
  assert.equal(escapeHtml(42), '42');
});

test('lastDays : 30 jours jusqu\'à aujourd\'hui compris, à cheval sur deux mois', () => {
  const d = lastDays(TODAY, 30);
  assert.equal(d.length, 30);
  assert.equal(d[0], '2026-09-26');
  assert.equal(d[29], TODAY);
  assert.deepEqual(lastDays('2026-03-01', 2), ['2026-02-28', '2026-03-01']);
});

test('percent', () => {
  assert.equal(percent(1, 3), 33.3);
  assert.equal(percent(2, 3), 66.7);
  assert.equal(percent(5, 0), 0);
});

test('compareVersions : la plus récente d\'abord', () => {
  const list = ['0.4.0', '0.5.0-rc.1', '0.4.1+dev', 'zzz', '0.5.0', '0.4.1', '0.10.0', '0.5.0-rc.10'];
  assert.deepEqual(list.sort(compareVersions), ['0.10.0', '0.5.0', '0.5.0-rc.10', '0.5.0-rc.1', '0.4.1+dev', '0.4.1', '0.4.0', 'zzz']);
});

test('summarize : totaux, jours manquants, répartitions', () => {
  const s = summarize(ROWS, TODAY);
  assert.equal(s.today, 4);
  assert.equal(s.last7, 11); // 19, 20 et 25 octobre / October 19, 20 and 25
  assert.equal(s.last30, 15);
  assert.equal(s.total, 25);

  assert.equal(s.days30.length, 30);
  assert.deepEqual(s.days30[0], { day: '2026-09-26', n: 0 });
  assert.deepEqual(s.days30[29], { day: TODAY, n: 4 });
  assert.deepEqual(s.days30[28], { day: '2026-10-24', n: 0 });
  assert.equal(s.days30.find((d) => d.day === '2026-10-01').n, 4);
  assert.equal(s.days30.reduce((a, d) => a + d.n, 0), s.last30);

  assert.deepEqual(s.by_version.map((e) => e.value), ['0.4.1+dev', '0.4.1', '0.4.0', '0.3.0']);
  assert.deepEqual(s.by_version[1], { value: '0.4.1', last30: 5, total: 5, pct_last30: 33.3, pct_total: 20 });
  assert.deepEqual(s.by_version[3], { value: '0.3.0', last30: 0, total: 10, pct_last30: 0, pct_total: 40 });
  assert.deepEqual(s.by_arch.map((e) => [e.value, e.total, e.last30]), [['arm', 10, 0], ['amd64', 9, 9], ['arm64', 6, 6]]);
  assert.deepEqual(s.by_install.map((e) => [e.value, e.total]), [['script', 12], ['apt', 7], ['docker', 6]]);
  for (const list of [s.by_version, s.by_arch, s.by_install]) {
    assert.equal(list.reduce((a, e) => a + e.total, 0), s.total);
    assert.ok(Math.abs(list.reduce((a, e) => a + e.pct_total, 0) - 100) < 0.2);
  }
  assert.deepEqual(s.days['2026-10-25'], { '0.4.1|amd64|apt': 3, '0.4.0|arm64|docker': 1 });
  assert.equal(s.days['2026-10-24'], undefined);
});

test('summarize : aucune donnée', () => {
  const s = summarize([], TODAY);
  assert.equal(s.total, 0);
  assert.equal(s.days30.length, 30);
  assert.ok(s.days30.every((d) => d.n === 0));
  assert.deepEqual(s.by_version, []);
});

test('releasesFrom : zip, .deb, le reste ignoré', () => {
  const rels = releasesFrom([
    {
      tag_name: 'v0.4.1', prerelease: false, published_at: '2026-10-25T10:00:00Z',
      assets: [
        { name: 'frp-manager.zip', download_count: 120 },
        { name: 'frp-manager_0.4.1_amd64.deb', download_count: 7 },
        { name: 'frp-manager_0.4.1_arm64.deb', download_count: 3 },
        { name: 'SHA256SUMS', download_count: 50 },
        { name: 'frp-manager.zip.sig', download_count: 9 },
      ],
    },
    { tag_name: 'v0.4.1-rc.1', prerelease: true, published_at: null, assets: [{ name: 'frp-manager.zip', download_count: -4 }] },
    { tag_name: 'v0.4.0' },
    null,
  ]);
  assert.deepEqual(rels, [
    { tag: 'v0.4.1', prerelease: false, published: '2026-10-25T10:00:00Z', zip: 120, deb: 10, total: 130 },
    { tag: 'v0.4.1-rc.1', prerelease: true, published: '', zip: 0, deb: 0, total: 0 },
    { tag: 'v0.4.0', prerelease: false, published: '', zip: 0, deb: 0, total: 0 },
  ]);
  assert.deepEqual(releaseTotals(rels), { zip: 120, deb: 10, total: 130 });
  assert.equal(releasesFrom({ message: 'API rate limit exceeded' }), null);
});

test('mise en forme à la française', () => {
  assert.equal(formatInt(1234567), '1 234 567');
  assert.equal(formatInt(999), '999');
  assert.equal(formatPct(33.3), '33,3 %');
  assert.equal(formatPct(100), '100,0 %');
  assert.equal(frDate('2026-10-05T10:00:00Z'), '05/10/2026');
  assert.equal(frDate(''), '—');
  assert.equal(frDateTime('2026-10-25T14:03:09.123Z'), '25/10/2026 à 14:03 UTC');
});

const NOW = new Date('2026-10-25T14:03:00Z');
const GITHUB = {
  releases: releasesFrom([
    { tag_name: '<script>alert(1)</script>', prerelease: true, published_at: '2026-10-20T00:00:00Z', assets: [{ name: 'frp-manager.zip', download_count: 1500 }] },
    { tag_name: 'v0.4.1', prerelease: false, published_at: '2026-10-25T00:00:00Z', assets: [{ name: 'x_amd64.deb', download_count: 2 }] },
  ]),
  error: '',
  fetched_at: '2026-10-25T14:00:00.000Z',
};

test('statsData : données du JSON', () => {
  const d = statsData(ROWS, NOW, GITHUB);
  assert.equal(d.today, TODAY);
  assert.equal(d.keep_days, KEEP_DAYS);
  assert.equal(d.generated_at, NOW.toISOString());
  assert.equal(d.pings.total, 25);
  assert.equal(d.pings.days, undefined);
  assert.deepEqual(d.days['2026-10-01'], { '0.4.1+dev|amd64|apt': 4 });
  assert.deepEqual(d.releases_total, { zip: 1500, deb: 2, total: 1502 });
  assert.equal(d.releases_error, '');

  const failed = statsData(ROWS, NOW, { releases: null, error: 'GitHub a répondu HTTP 500' });
  assert.equal(failed.releases, null);
  assert.equal(failed.releases_total, null);
  assert.equal(failed.releases_error, 'GitHub a répondu HTTP 500');
});

test('renderPage : sections, échappement, aucune ressource externe', () => {
  const rows = [...ROWS, { day: TODAY, key: '<img src=x onerror=alert(2)>|amd64|apt', n: 1 }];
  const html = renderPage(statsData(rows, NOW, GITHUB));
  assert.ok(html.startsWith('<!doctype html>'));
  assert.ok(html.includes('<html lang="fr">'));
  assert.ok(html.includes('<title>Espace privé — FRP Manager</title>'));
  for (const s of ['Envois des panels', 'Aujourd&#39;hui (en cours)', '7 derniers jours', '30 derniers jours',
    'Par version', 'Par architecture', 'Par méthode d\'installation', 'Téléchargements des releases GitHub',
    'Chiffres anonymes ; les envois ne comptent que les panels dont l\'administrateur a accepté les statistiques.']) {
    assert.ok(html.includes(s), s);
  }
  // Données échappées / Escaped data
  assert.ok(!html.includes('<script'));
  assert.ok(!html.includes('<img'));
  assert.ok(html.includes('&lt;script&gt;alert(1)&lt;/script&gt;'));
  assert.ok(html.includes('&lt;img src=x onerror=alert(2)&gt;'));
  // Ni script, ni ressource externe / No script, no external resource
  assert.ok(!/<link|src=|url\(|@import|https?:\/\//i.test(html.replace(/&lt;img src=x/g, '')));
  // 30 colonnes, dont celle du jour en cours / 30 columns, including the current day's
  assert.equal((html.match(/class="col"/g) || []).length, 30);
  assert.equal((html.match(/class="bar now"/g) || []).length, 1);
  assert.ok(html.includes('pré-release'));
  assert.ok(html.includes('1 502'));
  assert.ok(html.includes('relevés le 25/10/2026 à 14:00 UTC'));
});

test('renderPage : erreur GitHub et aucun envoi', () => {
  const html = renderPage(statsData([], NOW, { releases: null, error: 'limite <atteinte>', fetched_at: NOW.toISOString() }));
  const releases = html.slice(html.indexOf('id="telechargements"'));
  assert.ok(releases.includes('Téléchargements indisponibles : limite &lt;atteinte&gt;'));
  assert.ok(!releases.includes('<table'));
  assert.ok(html.includes('Aucun envoi reçu pour l\'instant'));
  assert.ok(!html.includes('class="col"'));
  assert.ok(html.includes('Envois des panels'));
});
