// Espace privé (/prive/) : agrégations, mise en forme et rendu HTML du tableau de bord,
// sans entrée-sortie (le Worker lit D1 et GitHub puis appelle ces fonctions).
// Private area (/prive/): aggregations, formatting and HTML rendering of the dashboard,
// with no I/O (the Worker reads D1 and GitHub, then calls these functions).
//
// La page est du HTML statique généré ici : aucun script, aucune ressource externe,
// tout ce qui vient des données (versions, tags GitHub, messages d'erreur) est échappé.
// The page is static HTML generated here: no script, no external resource, everything
// that comes from the data (versions, GitHub tags, error messages) is escaped.

import { KEEP_DAYS, dayOf } from './logic.js';

export const GITHUB_RELEASES = 'https://api.github.com/repos/Gogowwww/frp-manager/releases?per_page=100';
export const GITHUB_TTL = 1800; // réponse gardée 30 min (60 appels par heure sans jeton) / kept 30 min (60 calls an hour without a token)
export const GITHUB_ERROR_TTL = 300; // échec gardé 5 min / failure kept 5 min
export const CHART_DAYS = 30;

/** Échappe une valeur pour le HTML (texte et attributs). / Escapes a value for HTML (text and attributes). */
export function escapeHtml(value) {
  return String(value).replace(/[&<>"']/g, (c) => ({
    '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;',
  }[c]));
}
const esc = escapeHtml;

/** Les `count` derniers jours UTC jusqu'à `today` compris, du plus ancien au plus récent. / The last `count` UTC days up to `today` included, oldest first. */
export function lastDays(today, count) {
  const end = new Date(`${today}T00:00:00Z`).getTime();
  const out = [];
  for (let back = count - 1; back >= 0; back -= 1) out.push(dayOf(new Date(end - back * 86400000)));
  return out;
}

/** Part de `n` dans `total`, en pourcentage arrondi au dixième (0 si total nul). / Share of `n` in `total`, as a percentage rounded to a tenth (0 if total is zero). */
export function percent(n, total) {
  return total > 0 ? Math.round((n / total) * 1000) / 10 : 0;
}

function versionRank(v) {
  const m = /^([0-9]+)\.([0-9]+)\.([0-9]+)(?:-([^+]*))?(\+dev)?$/.exec(v);
  if (!m) return null;
  // Un instantané « X.Y.Z+dev » suit la release X.Y.Z, une pré-release la précède
  // A "X.Y.Z+dev" snapshot comes after release X.Y.Z, a pre-release comes before it
  return { nums: [Number(m[1]), Number(m[2]), Number(m[3])], stage: m[5] ? 2 : (m[4] ? 0 : 1), pre: m[4] || '' };
}

/** Tri des versions, la plus récente d'abord (les valeurs illisibles à la fin). / Version sort, newest first (unreadable values last). */
export function compareVersions(a, b) {
  const x = versionRank(a);
  const y = versionRank(b);
  if (!x || !y) {
    if (x || y) return x ? -1 : 1;
    return a < b ? -1 : (a > b ? 1 : 0);
  }
  for (let i = 0; i < 3; i += 1) if (x.nums[i] !== y.nums[i]) return y.nums[i] - x.nums[i];
  if (x.stage !== y.stage) return y.stage - x.stage;
  return y.pre.localeCompare(x.pre, 'en', { numeric: true });
}

/**
 * Agrège les compteurs D1 (lignes {day, key, n}, clé « version|arch|install ») :
 * aujourd'hui, 7 et 30 derniers jours (aujourd'hui compris), total, série des 30 jours
 * (un jour sans envoi vaut 0) et répartitions par version, architecture et installation.
 * Aggregates the D1 counters (rows {day, key, n}, key "version|arch|install"): today,
 * last 7 and 30 days (today included), total, 30-day series (a day without pings counts
 * as 0) and breakdowns by version, architecture and install method.
 */
export function summarize(rows, today) {
  const window30 = lastDays(today, CHART_DAYS);
  const in30 = new Set(window30);
  const in7 = new Set(window30.slice(-7));
  const perDay = new Map();
  const dims = [new Map(), new Map(), new Map()];
  const days = {};
  let total = 0;
  let last30 = 0;
  let last7 = 0;
  let todayCount = 0;
  for (const row of rows || []) {
    const n = Number(row.n);
    if (!Number.isFinite(n) || n <= 0) continue;
    const day = String(row.day);
    const key = String(row.key);
    (days[day] ||= {})[key] = n;
    total += n;
    perDay.set(day, (perDay.get(day) || 0) + n);
    const recent = in30.has(day);
    if (recent) last30 += n;
    if (in7.has(day)) last7 += n;
    if (day === today) todayCount += n;
    const parts = key.split('|');
    dims.forEach((m, i) => {
      const value = parts[i] || 'inconnue';
      const e = m.get(value) || { value, last30: 0, total: 0 };
      e.total += n;
      if (recent) e.last30 += n;
      m.set(value, e);
    });
  }
  const byCount = (a, b) => b.total - a.total || b.last30 - a.last30 || (a.value < b.value ? -1 : 1);
  const breakdown = (m, sort) => [...m.values()]
    .map((e) => ({ ...e, pct_last30: percent(e.last30, last30), pct_total: percent(e.total, total) }))
    .sort(sort);
  return {
    today: todayCount,
    last7,
    last30,
    total,
    days30: window30.map((day) => ({ day, n: perDay.get(day) || 0 })),
    by_version: breakdown(dims[0], (a, b) => compareVersions(a.value, b.value)),
    by_arch: breakdown(dims[1], byCount),
    by_install: breakdown(dims[2], byCount),
    days,
  };
}

const downloads = (v) => (Number.isSafeInteger(v) && v > 0 ? v : 0);

/**
 * Releases de la réponse de l'API GitHub : tag, pré-release, date, téléchargements de
 * frp-manager.zip et des .deb. null si la réponse n'est pas une liste.
 * Releases from the GitHub API response: tag, pre-release, date, downloads of
 * frp-manager.zip and of the .deb files. null if the response is not a list.
 */
export function releasesFrom(list) {
  if (!Array.isArray(list)) return null;
  return list.filter((r) => r && typeof r === 'object').map((r) => {
    let zip = 0;
    let deb = 0;
    for (const a of Array.isArray(r.assets) ? r.assets : []) {
      if (!a || typeof a.name !== 'string') continue;
      if (a.name === 'frp-manager.zip') zip += downloads(a.download_count);
      else if (a.name.endsWith('.deb')) deb += downloads(a.download_count);
    }
    return {
      tag: String(r.tag_name ?? ''),
      prerelease: r.prerelease === true,
      published: typeof r.published_at === 'string' ? r.published_at : '',
      zip,
      deb,
      total: zip + deb,
    };
  });
}

/** Totaux des téléchargements. / Download totals. */
export function releaseTotals(releases) {
  return (releases || []).reduce(
    (t, r) => ({ zip: t.zip + r.zip, deb: t.deb + r.deb, total: t.total + r.total }),
    { zip: 0, deb: 0, total: 0 },
  );
}

/**
 * Données du tableau de bord, servies telles quelles par /prive/stats.json et rendues
 * par renderPage. `github` : {releases, error, fetched_at} (releases null en cas d'échec).
 * `days` et `keep_days` gardent le format de l'ancien /stats.json.
 * Dashboard data, served as is by /prive/stats.json and rendered by renderPage.
 * `github`: {releases, error, fetched_at} (releases null on failure). `days` and
 * `keep_days` keep the format of the former /stats.json.
 */
export function statsData(rows, now, github = {}) {
  const today = dayOf(now);
  const { days, ...pings } = summarize(rows, today);
  const releases = Array.isArray(github.releases) ? github.releases : null;
  return {
    generated_at: now.toISOString(),
    today,
    keep_days: KEEP_DAYS,
    pings,
    releases,
    releases_total: releases ? releaseTotals(releases) : null,
    releases_error: releases ? '' : String(github.error || 'données GitHub absentes'),
    releases_fetched_at: typeof github.fetched_at === 'string' ? github.fetched_at : '',
    days,
  };
}

// Mise en forme à la française : espace fine insécable entre les milliers, virgule
// décimale, espace insécable avant « % ».
// French formatting: narrow no-break space between thousands, decimal comma, no-break
// space before "%".
export function formatInt(n) {
  return String(Math.trunc(Number(n) || 0)).replace(/\B(?=([0-9]{3})+(?![0-9]))/g, ' ');
}
export function formatPct(p) {
  return `${(Number(p) || 0).toFixed(1).replace('.', ',')} %`;
}
/** « 2026-10-25… » → « 25/10/2026 », « — » si illisible. / "2026-10-25…" → "25/10/2026", "—" if unreadable. */
export function frDate(iso) {
  const m = /^([0-9]{4})-([0-9]{2})-([0-9]{2})/.exec(iso || '');
  return m ? `${m[3]}/${m[2]}/${m[1]}` : '—';
}
/** « 2026-10-25T14:03… » → « 25/10/2026 à 14:03 UTC ». / "2026-10-25T14:03…" → "25/10/2026 à 14:03 UTC". */
export function frDateTime(iso) {
  const m = /^([0-9]{4})-([0-9]{2})-([0-9]{2})T([0-9]{2}):([0-9]{2})/.exec(iso || '');
  return m ? `${m[3]}/${m[2]}/${m[1]} à ${m[4]}:${m[5]} UTC` : '—';
}
const shortDay = (day) => `${day.slice(8, 10)}/${day.slice(5, 7)}`;
const sends = (n) => `${formatInt(n)} envoi${n > 1 ? 's' : ''}`;

// Cellule déjà en HTML (balisage écrit ici, jamais une donnée) ; toute autre cellule est échappée
// Cell already in HTML (markup written here, never data); any other cell is escaped
const markup = (html) => ({ html });
const cellHtml = (c) => (c && typeof c === 'object' && 'html' in c ? c.html : esc(c));

function table(columns, rows, foot) {
  const cls = (i) => {
    const names = [columns[i].num && 'num', columns[i].cls].filter(Boolean).join(' ');
    return names ? ` class="${names}"` : '';
  };
  const line = (r) => `<tr>${r.map((c, i) => `<td${cls(i)}>${cellHtml(c)}</td>`).join('')}</tr>`;
  return `<div class="scroll"><table>
<thead><tr>${columns.map((c, i) => `<th scope="col"${cls(i)}>${esc(c.label)}</th>`).join('')}</tr></thead>
<tbody>${rows.map(line).join('\n')}</tbody>${foot ? `\n<tfoot>${line(foot)}</tfoot>` : ''}
</table></div>`;
}

function breakdownTable(label, list) {
  if (!list.length) return '<p class="muted">Aucun envoi.</p>';
  return table(
    [{ label }, { label: '30 jours', num: true }, { label: 'Part', num: true }, { label: 'Total', num: true }, { label: 'Part', num: true }],
    list.map((e) => [e.value, formatInt(e.last30), formatPct(e.pct_last30), formatInt(e.total), formatPct(e.pct_total)]),
  );
}

function dayChart(days30, today) {
  const max = Math.max(1, ...days30.map((d) => d.n));
  const first = days30[0].day;
  const last = days30[days30.length - 1].day;
  const cols = days30.map(({ day, n }) => {
    const now = day === today;
    const height = n > 0 ? Math.max(2, Math.round((n / max) * 1000) / 10) : 0;
    const tip = `${shortDay(day)} : ${sends(n)}${now ? ' (en cours)' : ''}`;
    const bar = n > 0 ? `<span class="bar${now ? ' now' : ''}" style="height:${height}%"></span>` : '';
    return `<span class="col" title="${esc(tip)}">${bar}</span>`;
  }).join('');
  // Repères tous les 7 jours en partant d'aujourd'hui, alignés sur leur colonne
  // Ticks every 7 days counting back from today, aligned on their column
  const ticks = days30.map(({ day }, i) => `<span>${(days30.length - 1 - i) % 7 === 0 ? `<b>${esc(shortDay(day))}</b>` : ''}</span>`).join('');
  return `<figure class="chart">
<div class="ymax" aria-hidden="true"><span>${formatInt(max)}</span></div>
<div class="cols" role="img" aria-label="${esc(`Envois par jour du ${frDate(first)} au ${frDate(last)} : au plus ${sends(max)} par jour`)}">${cols}</div>
<div class="ticks" aria-hidden="true">${ticks}</div>
<figcaption>Envois par jour, ${esc(frDate(first))} – ${esc(frDate(last))} (UTC). La dernière barre, atténuée, est la journée en cours.</figcaption>
</figure>
<details><summary>Chiffres jour par jour</summary>
${table([{ label: 'Jour' }, { label: 'Envois', num: true }], days30.slice().reverse().map(({ day, n }) => [day === today ? `${frDate(day)} (en cours)` : frDate(day), formatInt(n)]))}
</details>`;
}

function releasesSection(data) {
  if (!data.releases) {
    return `<p class="error" role="status">Téléchargements indisponibles : ${esc(data.releases_error)}. Réessayez plus tard.</p>`;
  }
  if (!data.releases.length) return '<p class="muted">Aucune release publiée.</p>';
  const t = data.releases_total;
  const kind = (r) => markup(r.prerelease ? '<span class="tag pre">pré-release</span>' : '<span class="tag">définitive</span>');
  // Sur un écran étroit, la colonne Type est masquée : « pré-release » passe sous le tag
  // On a narrow screen, the Type column is hidden: "pré-release" moves under the tag
  const tag = (r) => markup(`${esc(r.tag)}${r.prerelease ? ' <span class="tag pre narrow">pré-release</span>' : ''}`);
  return table(
    [{ label: 'Version' }, { label: 'Type', cls: 'wide' }, { label: 'Date' }, { label: 'zip', num: true }, { label: '.deb', num: true }, { label: 'Total', num: true }],
    data.releases.map((r) => [tag(r), kind(r), frDate(r.published), formatInt(r.zip), formatInt(r.deb), formatInt(r.total)]),
    ['Total', '', '', formatInt(t.zip), formatInt(t.deb), formatInt(t.total)],
  ) + (data.releases_fetched_at ? `\n<p class="meta">Chiffres de GitHub relevés le ${esc(frDateTime(data.releases_fetched_at))}, gardés 30 minutes.</p>` : '');
}

const STYLE = `
:root{color-scheme:light dark;--bg:#f9f9f7;--surface:#fcfcfb;--ink:#0b0b0b;--ink-2:#52514e;--ink-3:#686762;--line:#e1e0d9;--axis:#c3c2b7;--accent:#2a78d6;--accent-now:#9ec5f4;--error-bg:#fdecec;--error-ink:#8f1f1f;--tag-bg:#e7f0fb;--tag-ink:#1c5cab}
@media (prefers-color-scheme:dark){:root{--bg:#0d0d0d;--surface:#1a1a19;--ink:#f0efec;--ink-2:#c3c2b7;--ink-3:#a3a29b;--line:#2c2c2a;--axis:#383835;--accent:#3987e5;--accent-now:#1c5cab;--error-bg:#3a1717;--error-ink:#f3b4b4;--tag-bg:#18304f;--tag-ink:#b7d3f6}}
*{box-sizing:border-box}
html{-webkit-text-size-adjust:100%}
body{margin:0;background:var(--bg);color:var(--ink);font:16px/1.5 system-ui,-apple-system,"Segoe UI",Roboto,sans-serif}
main{max-width:960px;margin:0 auto;padding:28px 16px 40px}
h1{font-size:28px;line-height:1.2;margin:0 0 8px}
h2{font-size:21px;margin:40px 0 8px;padding-top:20px;border-top:1px solid var(--line)}
h3{font-size:16px;margin:28px 0 8px}
p{margin:8px 0}
a{color:var(--accent)}
.eyebrow{margin:0;color:var(--ink-3);font-size:13px;font-weight:600;letter-spacing:.04em;text-transform:uppercase}
.muted,.meta,figcaption{color:var(--ink-2)}
.meta,figcaption{font-size:13px}
.note{margin:16px 0;padding:10px 14px;border-left:3px solid var(--accent);background:var(--surface);border-radius:0 6px 6px 0}
.tiles{display:grid;grid-template-columns:repeat(auto-fit,minmax(150px,1fr));gap:12px;margin:16px 0}
.tile{background:var(--surface);border:1px solid var(--line);border-radius:8px;padding:12px 14px}
.tile span{display:block}
.tile .label{color:var(--ink-2);font-size:13px}
.tile .value{font-size:28px;font-weight:650;line-height:1.25}
.chart{margin:12px 0 4px;padding:14px 14px 10px;background:var(--surface);border:1px solid var(--line);border-radius:8px}
.ymax{border-bottom:1px dashed var(--line);font-size:12px;color:var(--ink-3);font-variant-numeric:tabular-nums;line-height:1.4}
.cols{display:flex;align-items:flex-end;gap:2px;height:150px;border-bottom:1px solid var(--axis)}
.col{flex:1 1 0;min-width:0;height:100%;display:flex;align-items:flex-end}
.bar{display:block;width:100%;background:var(--accent);border-radius:3px 3px 0 0}
.bar.now{background:var(--accent-now)}
.ticks{display:flex;gap:2px;height:20px;font-size:12px;color:var(--ink-3)}
.ticks span{flex:1 1 0;min-width:0;position:relative}
.ticks b{position:absolute;top:3px;left:50%;transform:translateX(-50%);font-weight:400;white-space:nowrap}
.ticks span:last-child b{left:auto;right:0;transform:none}
.ticks span:first-child b{left:0;transform:none}
figcaption{margin-top:6px}
details{margin:8px 0}
summary{cursor:pointer;color:var(--accent);font-size:14px}
.grid2{display:grid;grid-template-columns:repeat(auto-fit,minmax(280px,1fr));gap:0 24px}
.scroll{overflow-x:auto;margin:8px 0}
table{width:100%;border-collapse:collapse;font-size:14px}
th,td{padding:6px 8px;border-bottom:1px solid var(--line);text-align:left;white-space:nowrap}
th{color:var(--ink-2);font-weight:600;border-bottom-color:var(--axis)}
td:first-child{white-space:normal;overflow-wrap:anywhere}
.num{text-align:right;font-variant-numeric:tabular-nums}
tfoot td{font-weight:650;border-top:1px solid var(--axis);border-bottom:0}
.tag{display:inline-block;padding:0 7px;border-radius:9px;font-size:12px;background:var(--tag-bg);color:var(--tag-ink)}
.tag.pre{background:transparent;color:var(--ink-2);box-shadow:inset 0 0 0 1px var(--axis)}
.narrow{display:none}
.error{padding:10px 14px;border-radius:6px;background:var(--error-bg);color:var(--error-ink)}
footer{margin-top:40px;padding-top:16px;border-top:1px solid var(--line);font-size:13px;color:var(--ink-2)}
@media (max-width:480px){main{padding:20px 12px 32px}th,td{padding:6px 5px}.tile .value{font-size:24px}.wide{display:none}.tag.narrow{display:table;margin-top:2px}}
`;

/** Page HTML complète du tableau de bord. / Full HTML page of the dashboard. */
export function renderPage(data) {
  const p = data.pings;
  const tile = (label, n) => `<div class="tile"><span class="label">${esc(label)}</span><span class="value">${formatInt(n)}</span></div>`;
  const panels = p.total > 0
    ? `${dayChart(p.days30, data.today)}
<h3>Par version</h3>
${breakdownTable('Version', p.by_version)}
<div class="grid2">
<div><h3>Par architecture</h3>
${breakdownTable('Architecture', p.by_arch)}</div>
<div><h3>Par méthode d'installation</h3>
${breakdownTable('Installation', p.by_install)}</div>
</div>`
    : '<p class="muted">Aucun envoi reçu pour l\'instant : un panel n\'envoie rien tant que son administrateur n\'a pas accepté les statistiques anonymes.</p>';
  return `<!doctype html>
<html lang="fr">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="robots" content="noindex, nofollow">
<meta name="color-scheme" content="light dark">
<title>Espace privé — FRP Manager</title>
<style>${STYLE}</style>
</head>
<body>
<main>
<header>
<p class="eyebrow">FRP Manager</p>
<h1>Espace privé</h1>
<p class="note">Chiffres anonymes ; les envois ne comptent que les panels dont l'administrateur a accepté les statistiques.</p>
<p class="meta">Page générée le ${esc(frDateTime(data.generated_at))}. Jours en UTC.</p>
</header>

<section aria-labelledby="envois">
<h2 id="envois">Envois des panels</h2>
<p class="muted">Un panel envoie au plus un signal par jour (version, architecture, méthode d'installation) : le nombre d'un jour donne à peu près celui des panels actifs ce jour-là. Les compteurs sont gardés ${formatInt(data.keep_days)} jours, sans adresse IP ni identifiant.</p>
<div class="tiles">
${tile('Aujourd\'hui (en cours)', p.today)}
${tile('7 derniers jours', p.last7)}
${tile('30 derniers jours', p.last30)}
${tile(`Total (${formatInt(data.keep_days)} jours)`, p.total)}
</div>
${panels}
</section>

<section aria-labelledby="telechargements">
<h2 id="telechargements">Téléchargements des releases GitHub</h2>
<p class="muted">Compteurs de GitHub : chaque mise à jour d'un panel par son bouton télécharge <code>frp-manager.zip</code> ; les <code>.deb</code> sont ceux téléchargés depuis GitHub, pas depuis le dépôt apt. Un téléchargement n'est pas une installation active, et les images Docker n'ont pas de compteur public.</p>
${releasesSection(data)}
</section>

<footer>Les mêmes données en JSON : <a href="stats.json">stats.json</a>.</footer>
</main>
</body>
</html>
`;
}
