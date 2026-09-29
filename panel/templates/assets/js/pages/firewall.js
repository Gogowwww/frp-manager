// ── Pare-feu : qui peut se connecter aux ports qu'ouvre frps ──────────────
// Les règles se modifient dans un brouillon local ; la barre du bas les
// applique d'un coup (table nftables remplacée atomiquement côté serveur).

import { t } from '../i18n.js';
import { api, apiOk } from '../api.js';
import {
  h, icon, button, busy, toast, toastResult, toastError, openDialog, confirmDialog,
  field, input, switchControl, switchRow, segmented, callout, emptyState, pageHeader, card, badge, saveBar, copyButton,
} from '../ui.js';

const POLL_MS = 5000;          // repli sans WebSocket
const AGO_MS = 5000;           // rafraîchit « il y a … » sans rien redemander
const WS_MAX_FAILS = 3;        // WebSocket jamais ouvert (proxy) : on reste en repli
import { navigate } from '../router.js';

const S = {
  data: null,
  enabled: false,
  rules: [],
  snapshot: '',
  live: null,
  catalog: null,        // listes communautaires, une fois le catalogue ouvert
  els: {},
};

function stopLive() {
  if (S.live) S.live.stop();
  S.live = null;
}

const snapshotOf = () => JSON.stringify([S.enabled, S.rules]);
const isDirty = () => S.snapshot !== '' && snapshotOf() !== S.snapshot;

function beforeUnload(e) {
  if (isDirty()) { e.preventDefault(); e.returnValue = ''; }
}

export default {
  id: 'firewall',
  title: () => t('nav.firewall'),

  async mount(view) {
    S.snapshot = '';
    window.addEventListener('beforeunload', beforeUnload);
    S.els.root = h('div', { class: 'page' },
      pageHeader({ title: t('firewall.title'), description: t('firewall.description') }),
      h('div', { class: 'skeleton', style: { height: '160px' } }));
    view.append(S.els.root);
    await load();
  },

  unmount() {
    window.removeEventListener('beforeunload', beforeUnload);
    stopLive();
    S.els = {};
  },

  canLeave() {
    return isDirty() ? confirmDialog({
      title: t('common.leaveTitle'), message: t('common.leaveText'), confirmLabel: t('common.leave'), danger: true,
    }) : true;
  },
};

// ── Chargement ─────────────────────────────────────────────────────────────

async function load() {
  try {
    S.data = await apiOk('/api/firewall');
  } catch (e) {
    toastError(e);
    return;
  }
  S.enabled = S.data.enabled;
  S.rules = S.data.rules.map((r) => ({ ...r, sources: r.sources.map((s) => ({ ...s })) }));
  S.snapshot = snapshotOf();
  build();
}

async function installNft(e) {
  const btn = e.currentTarget;
  await busy(btn, async () => {
    try {
      const d = await apiOk('/api/firewall/install', { method: 'POST' });
      toast(d.msg, 'success');
    } catch (err) {
      toastError(err);
      return;
    }
    await load();
  });
}

// ── Rendu ──────────────────────────────────────────────────────────────────

function build() {
  const root = S.els.root;
  if (!root) return;
  const head = pageHeader({ title: t('firewall.title'), description: t('firewall.description') });
  const d = S.data;

  if (!d.has_frps) {
    root.replaceChildren(head, h('div', { class: 'card' }, emptyState({
      iconName: 'shield', title: t('firewall.noFrpsTitle'), text: t('firewall.noFrpsText'),
      actions: [button(t('nav.dashboard'), { onClick: () => navigate('dashboard') })],
    })));
    return;
  }
  if (!d.available) {
    root.replaceChildren(head, callout({
      type: 'danger', title: t('firewall.noNftTitle'),
      text: t(d.in_docker ? 'firewall.noNftTextDocker' : 'firewall.noNftText', { detail: d.nft }),
      actions: [button(t('firewall.installNft'), { variant: 'primary', iconName: 'download', onClick: installNft })],
    }));
    return;
  }

  S.els.status = h('div', { class: 'card' });
  S.els.rules = h('div', { class: 'card' });
  S.els.ports = h('div', { class: 'card' });
  S.els.savebar = saveBar({ onDiscard: discard, onSave: save, saveLabel: t('firewall.apply') });

  root.replaceChildren(
    head,
    // État et test d'adresse côte à côte, règles et tableaux en pleine largeur
    h('div', { class: 'grid-2 card-pair' }, S.els.status, testSection()),
    h('section', null,
      h('div', { class: 'section-head' },
        h('div', null,
          h('h2', { class: 'section-title' }, t('firewall.rulesTitle')),
          h('p', { class: 'section-desc' }, t('firewall.rulesDesc'))),
        h('div', { class: 'fw-head-actions' },
          button(t('firewall.lists.open'), { iconName: 'globe', onClick: openCatalog }),
          button(t('firewall.addRule'), { variant: 'primary', iconName: 'plus', onClick: () => editRule(null) }))),
      S.els.rules),
    h('section', null,
      h('div', { class: 'section-head' },
        h('div', null,
          h('h2', { class: 'section-title' }, t('firewall.portsTitle')),
          h('p', { class: 'section-desc' }, t('firewall.portsDesc')))),
      S.els.ports),
    blockedSection(),
    S.els.savebar);
  render();
}

function render() {
  if (!S.els.status) return;
  renderStatus();
  renderRules();
  renderPorts();
  S.els.savebar.hidden = !isDirty();
}

function renderStatus() {
  const d = S.data;
  const activeRules = S.rules.filter((r) => r.enabled).length;
  let state;
  if (isDirty()) state = badge(t('firewall.state.pending'), 'warn');
  else if (S.enabled && d.active) state = badge(t('firewall.state.on', { count: activeRules }), 'success');
  else if (S.enabled) state = badge(t('firewall.state.idle'), 'info');
  else state = badge(t('firewall.state.off'));

  const master = switchRow({
    label: t('firewall.enable'),
    description: t('firewall.enableHint'),
    checked: S.enabled,
    onChange: (on) => { S.enabled = on; render(); },
  });

  const you = [];
  if (d.client_ip) {
    const blocked = d.client_verdicts.filter((v) => v.blocked_by.length);
    you.push(h('p', { class: 'fw-you' },
      icon('globe'), t('firewall.yourIp'), ' ', h('span', { class: 'mono' }, d.client_ip),
      blocked.length
        ? h('span', { class: 'fw-you-warn' }, ' — ', t('firewall.youBlocked', { ports: blocked.map(portText).join(', ') }))
        : h('span', { class: 'muted' }, ' — ', t('firewall.youFree'))));
  }

  S.els.status.replaceChildren(
    h('div', { class: 'card-head' }, h('h2', { class: 'card-title' }, icon('shield'), t('firewall.statusTitle')), state),
    h('div', { class: 'card-body form-stack' }, master, ...you));
}

function portText(p) {
  return p.start === p.end ? String(p.start) : `${p.start}-${p.end}`;
}

function parseSpec(spec) {
  return String(spec).split(/[,\s]+/).filter(Boolean).map((part) => {
    const [a, b] = part.split('-').map(Number);
    return [a, b || a];
  });
}

function ruleCovers(rule, port) {
  if (rule.ports === '*') return true;
  return parseSpec(rule.ports).some(([a, b]) => a <= port.end && port.start <= b);
}

function ruleTitle(rule) {
  return rule.name || t(rule.mode === 'allow' ? 'firewall.untitledAllow' : 'firewall.untitledBlock');
}

function renderRules() {
  const host = S.els.rules;
  if (!S.rules.length) {
    host.replaceChildren(emptyState({
      iconName: 'shield', title: t('firewall.emptyTitle'), text: t('firewall.emptyText'),
      actions: [button(t('firewall.addRule'), { variant: 'primary', iconName: 'plus', onClick: () => editRule(null) })],
    }));
    return;
  }
  const saved = JSON.parse(S.snapshot || '[false,[]]')[1];
  host.replaceChildren(h('ul', { class: 'rows' }, S.rules.map((rule) => {
    const before = saved.find((r) => r.id === rule.id);
    const state = !before ? 'new' : JSON.stringify(before) === JSON.stringify(rule) ? '' : 'changed';
    const count = S.data.counters[rule.id];
    const sw = switchControl({
      checked: rule.enabled, label: t('firewall.ruleEnabled'),
      onChange: (on) => { rule.enabled = on; render(); },
    });
    sw.addEventListener('click', (e) => e.stopPropagation());
    const open = () => editRule(rule);
    return h('li', {
      class: `row fw-row${state ? ` is-${state}` : ''}${rule.enabled ? '' : ' is-off'}`,
      dataset: { state: state ? t(`common.state.${state}`) : '' },
      tabindex: '0', onClick: open, onKeydown: (e) => { if (e.key === 'Enter') open(); },
    },
    h('div', { class: 'row-name' }, h('span', null, ruleTitle(rule)),
      badge(t(`firewall.mode.${rule.mode}`), rule.mode === 'allow' ? 'success' : 'danger')),
    h('div', { class: 'fw-detail' },
      h('span', null, h('small', null, t('firewall.portsLabel')),
        rule.ports === '*' ? h('span', null, t('firewall.allPorts')) : h('span', { class: 'mono' }, rule.ports)),
      h('span', null, h('small', null, t('firewall.sourcesLabel')),
        h('span', { title: [rule.list && listText(rule.list), ...rule.sources.map(sourceTitle)].filter(Boolean).join('\n') },
          rule.list ? h('span', { class: 'fw-list-tag' }, icon('globe'), listText(rule.list)) : null,
          rule.list && rule.sources.length ? ' + ' : null,
          rule.sources.length
            ? rule.sources.slice(0, 2).map(sourceLabel).join(', ') + (rule.sources.length > 2 ? ` +${rule.sources.length - 2}` : '')
            : rule.list ? null : t('firewall.noSource')))),
    h('div', { class: 'fw-count', title: t('firewall.countHint'), dataset: { rule: state || !rule.enabled ? '' : rule.id } },
      count != null && !state && rule.enabled ? t('firewall.blockedCount', { count }) : ''),
    h('div', { class: 'row-actions' },
      sw,
      rule.sources.length ? button('', {
        variant: 'ghost', size: 'sm', iconName: 'upload', title: t('firewall.publish.action'),
        onClick: (e) => { e.stopPropagation(); publishRule(rule); },
      }) : null,
      button('', { variant: 'ghost', size: 'sm', iconName: 'edit', title: t('common.edit'), onClick: (e) => { e.stopPropagation(); open(); } }),
      button('', { variant: 'ghost-danger', size: 'sm', iconName: 'trash', title: t('common.delete'), onClick: (e) => { e.stopPropagation(); removeRule(rule); } })));
  })));
}

/** « AS16276 » ou l'adresse, pour une source de règle. */
function sourceLabel(src) {
  return src.asn ? `AS${src.asn}` : src.cidr;
}

function sourceTitle(src) {
  if (!src.asn) return src.note ? `${src.cidr} — ${src.note}` : src.cidr;
  const info = S.data.asns?.[src.asn];
  const what = info ? t('firewall.asInfo', { holder: info.holder || '?', count: info.prefixes }) : t('firewall.asPending');
  return `AS${src.asn} — ${what}${src.note ? ` (${src.note})` : ''}`;
}

/** Compteurs des règles, rafraîchis sans reconstruire la liste. */
function updateCounts(counters) {
  S.data.counters = counters;
  S.els.rules?.querySelectorAll('.fw-count[data-rule]').forEach((el) => {
    const id = el.dataset.rule;
    if (id && counters[id] != null) el.textContent = t('firewall.blockedCount', { count: counters[id] });
  });
}

function renderPorts() {
  const host = S.els.ports;
  const ports = S.data.ports;
  if (!ports.length) {
    host.replaceChildren(h('p', { class: 'muted', style: { padding: '16px 20px' } }, t('firewall.noPorts')));
    return;
  }
  const active = S.enabled ? S.rules.filter((r) => r.enabled) : [];
  host.replaceChildren(h('div', { class: 'fw-table-wrap' }, h('table', { class: 'fw-table' },
    h('thead', null, h('tr', null,
      h('th', null, t('firewall.col.port')), h('th', null, t('firewall.col.use')), h('th', null, t('firewall.col.access')))),
    h('tbody', null, ports.map((p) => {
      const rules = active.filter((r) => ruleCovers(r, p));
      return h('tr', null,
        h('td', { class: 'mono' }, portText(p), p.proto !== 'any' ? h('span', { class: 'muted' }, ` /${p.proto}`) : null),
        h('td', null, p.label,
          p.kind === 'proxy' ? badge(t(p.online ? 'firewall.online' : 'firewall.offline'), p.online ? 'success' : '', { style: { marginLeft: '8px' } }) : null),
        h('td', null, rules.length
          ? h('span', { class: 'fw-filtered' }, icon('shield'), rules.map(ruleTitle).join(', '))
          : h('span', { class: 'muted' }, t('firewall.openToAll'))));
    })))));
}

// ── Tester une adresse ─────────────────────────────────────────────────────

function testSection() {
  const ip = input({ value: S.data.client_ip || '', placeholder: '203.0.113.4', mono: true, 'aria-label': t('firewall.testLabel') });
  const out = h('div', { class: 'fw-test-out' });
  const run = async (e) => {
    e.preventDefault();
    try {
      const d = await api('/api/firewall/test', { method: 'POST', body: { ip: ip.value.trim(), rules: S.enabled ? S.rules : [] } });
      if (!d.ok) { toastResult(d); return; }
      out.replaceChildren(h('ul', { class: 'fw-verdicts' }, d.verdicts.map((v) => h('li', null,
        h('span', { class: `fw-dot ${v.blocked_by.length ? 'is-blocked' : 'is-ok'}` }),
        h('span', { class: 'mono' }, portText(v)), ' ', h('span', { class: 'muted' }, v.label), ' — ',
        v.blocked_by.length ? t('firewall.testBlocked', { rules: v.blocked_by.join(', ') }) : t('firewall.testOk')))));
    } catch (err) { toastError(err); }
  };
  const btn = button(t('firewall.test'), { type: 'submit', form: 'fw-test' });
  return card({
    title: t('firewall.testTitle'), description: t('firewall.testDesc'),
    body: h('div', { class: 'form-stack' },
      h('form', { id: 'fw-test', class: 'fw-test-form', onSubmit: (e) => busy(btn, () => run(e)) }, ip, btn),
      out),
  });
}

// ── Connexions bloquées ────────────────────────────────────────────────────

function ago(seconds) {
  if (seconds < 60) return t('firewall.ago.s', { n: Math.max(1, seconds) });
  if (seconds < 3600) return t('firewall.ago.m', { n: Math.floor(seconds / 60) });
  return t('firewall.ago.h', { n: Math.floor(seconds / 3600) });
}

function blockedSection() {
  stopLive();
  const body = h('div', null, h('p', { class: 'muted fw-pad' }, t('common.loading')));
  const pill = h('span', { class: 'badge badge-success fw-live', hidden: true }, h('span', { class: 'dot dot-pulse' }), t('firewall.live'));
  const note = (text) => body.replaceChildren(h('p', { class: 'muted fw-pad' }, text));
  let last = null;
  let skew = 0;                // horloge du serveur − horloge du navigateur (s)

  const show = () => {
    const d = last;
    if (!d || !body.isConnected) return;
    if (!d.ok) { note(d.msg || t('errors.generic')); return; }
    if (!d.available) { note(t('firewall.blockedUnavailable')); return; }
    if (!d.entries.length) { note(t('firewall.blockedEmpty')); return; }
    const now = Date.now() / 1000 + skew;
    const byId = Object.fromEntries(S.rules.map((r) => [r.id, ruleTitle(r)]));
    body.replaceChildren(
      h('div', { class: 'fw-table-wrap' }, h('table', { class: 'fw-table' },
        h('thead', null, h('tr', null,
          h('th', null, t('firewall.col.last')), h('th', null, t('firewall.col.source')), h('th', null, t('firewall.col.as')),
          h('th', null, t('firewall.col.port')), h('th', null, t('firewall.col.rule')), h('th', { 'aria-label': t('common.moreActions') }))),
        h('tbody', null, d.entries.map((e) => h('tr', null,
          h('td', { class: 'muted', title: new Date(e.last * 1000).toLocaleString() }, ago(Math.round(now - e.last))),
          h('td', { class: 'mono' }, e.src),
          h('td', { class: 'fw-as' }, e.as
            ? [h('span', { class: 'mono' }, `AS${e.as.asn}`), ' ', h('span', { class: 'muted' }, e.as.holder)]
            : h('span', { class: 'muted' }, '—')),
          h('td', { class: 'mono' }, e.port, h('span', { class: 'muted' }, ` /${e.proto}`)),
          h('td', null, byId[e.rule] || h('span', { class: 'muted' }, t('firewall.deletedRule'))),
          h('td', { class: 'fw-cell-menu' }, button('', {
            variant: 'ghost', size: 'sm', iconName: 'shield', title: t('firewall.quickTitle', { ip: e.src }),
            onClick: () => chooseQuickBlock(e),
          }))))))),
      ...(d.total > d.entries.length ? [h('p', { class: 'muted fw-pad' }, t('firewall.blockedMore', { count: d.total }))] : []));
  };

  const receive = (d) => {
    last = d;
    if (d.now) skew = d.now - Date.now() / 1000;
    if (d.counters) updateCounts(d.counters);
    show();
  };

  // ── WebSocket, repli sur une requête toutes les 5 s ──
  let ws = null;
  let pollTimer = null;
  let retryTimer = null;
  let fails = 0;
  let stopped = false;
  const poll = async () => {
    if (document.visibilityState !== 'visible') return;
    try { receive(await api('/api/firewall/blocked')); } catch { /* réessayé au prochain tour */ }
  };
  const startPolling = () => {
    if (!pollTimer) { poll(); pollTimer = setInterval(poll, POLL_MS); }
  };
  const connect = () => {
    if (stopped || !('WebSocket' in window)) { startPolling(); return; }
    const scheme = location.protocol === 'https:' ? 'wss' : 'ws';
    const sock = new WebSocket(`${scheme}://${location.host}/ws/firewall`);
    let opened = false;
    ws = sock;
    sock.onopen = () => {
      opened = true; fails = 0; pill.hidden = false;
      clearInterval(pollTimer); pollTimer = null;
    };
    sock.onmessage = (e) => {
      try {
        const d = JSON.parse(e.data);
        if (!d.keepalive) receive(d);
      } catch { /* message illisible : ignoré */ }
    };
    sock.onclose = () => {
      if (ws !== sock || stopped) return;
      ws = null;
      pill.hidden = true;
      startPolling();
      if (!opened && ++fails >= WS_MAX_FAILS) return;
      retryTimer = setTimeout(connect, Math.min(30000, 1000 * 2 ** fails));
    };
  };
  const agoTimer = setInterval(show, AGO_MS);
  S.live = {
    stop() {
      stopped = true;
      clearInterval(agoTimer); clearInterval(pollTimer); clearTimeout(retryTimer);
      if (ws) { const w = ws; ws = null; w.close(); }
    },
  };
  connect();

  return h('section', null,
    h('div', { class: 'section-head' },
      h('div', null,
        h('h2', { class: 'section-title' }, t('firewall.blockedTitle'), ' ', pill),
        h('p', { class: 'section-desc' }, t('firewall.blockedDesc')))),
    h('div', { class: 'card' }, body));
}

/** Bloquer une adresse de la liste : elle seule, ou tout son opérateur. */
async function chooseQuickBlock(entry) {
  const choice = await openDialog({
    title: t('firewall.quickTitle', { ip: entry.src }),
    description: t('firewall.quickDesc'),
    size: 'sm',
    build: ({ close }) => ({
      body: [h('div', { class: 'form-stack fw-quick-choices' },
        button(t('firewall.quickIp', { ip: entry.src }), { iconName: 'shield', onClick: () => close({ cidr: entry.src }) }),
        entry.as && button(t('firewall.quickAs', { asn: entry.as.asn, holder: entry.as.holder }), {
          iconName: 'shield', onClick: () => close({ asn: entry.as.asn, note: entry.as.holder }),
        }))],
      foot: [button(t('common.cancel'), { onClick: () => close(undefined) })],
    }),
  }).result;
  if (choice) quickBlock(choice);
}

/** Ajoute une adresse ou un AS à la règle « Blocages rapides » (créée au besoin). */
function quickBlock(source) {
  const name = t('firewall.quickRule');
  let rule = S.rules.find((r) => r.mode === 'block' && r.ports === '*' && r.name === name);
  if (!rule) {
    rule = { id: Math.random().toString(16).slice(2, 10), name, mode: 'block', ports: '*', sources: [], enabled: true };
    S.rules.push(rule);
  }
  const label = sourceLabel(source);
  if (!rule.sources.some((x) => sourceLabel(x) === label)) rule.sources.push({ note: '', ...source });
  rule.enabled = true;
  render();
  toast(t('firewall.quickAdded', { what: label, rule: name }), 'info', 6000);
}

// ── Listes communautaires ──────────────────────────────────────────────────
// Catalogue tenu sur GitHub. S'abonner crée une règle « Bloquer » qui suit la
// liste (mise à jour par le serveur) ; publier prépare une issue GitHub.

function listInfo(id) {
  const known = S.data.lists?.[id];
  if (known) return known;
  const lst = S.catalog?.find((l) => l.id === id);
  return lst ? { name: lst.name, author: lst.author, updated: lst.updated, count: lst.sources.length } : null;
}

/** « Scanners connus (120) », ou l'identifiant si la liste est inconnue. */
function listText(id) {
  const info = listInfo(id);
  return info ? t('firewall.lists.tag', { name: info.name, count: info.count }) : t('firewall.lists.unknown', { id });
}

function openCatalog() {
  const body = h('div', { class: 'fw-catalog' }, h('p', { class: 'muted' }, t('common.loading')));
  const status = h('p', { class: 'field-hint' });
  const browse = h('a', { class: 'btn btn-ghost', target: '_blank', rel: 'noopener', hidden: true },
    icon('external'), t('firewall.lists.browse'));
  let close;

  const fill = async (fresh) => {
    let d;
    try {
      d = await apiOk(`/api/firewall/lists${fresh ? '?fresh=1' : ''}`);
    } catch (e) {
      body.replaceChildren(callout({ type: 'danger', text: e.message || t('errors.generic') }));
      return;
    }
    S.catalog = d.lists;
    browse.href = d.browse_url;
    browse.hidden = false;
    status.textContent = d.error
      ? t(d.fetched ? 'firewall.lists.stale' : 'firewall.lists.unreachable', { error: d.error })
      : d.fetched ? t('firewall.lists.fetchedAt', { date: new Date(d.fetched * 1000).toLocaleString() }) : '';
    if (!d.lists.length) {
      body.replaceChildren(emptyState({
        iconName: 'globe', title: t('firewall.lists.emptyTitle'), text: t('firewall.lists.emptyText'),
      }));
      return;
    }
    body.replaceChildren(h('ul', { class: 'fw-catalog-list' }, d.lists.map((lst) => {
      const rule = S.rules.find((r) => r.list === lst.id);
      const entries = h('ul', { class: 'fw-catalog-entries mono', hidden: true },
        lst.sources.slice(0, 500).map((s) => h('li', null, sourceLabel(s), s.note ? h('span', { class: 'muted' }, `  # ${s.note}`) : null)),
        lst.sources.length > 500 ? h('li', { class: 'muted' }, t('firewall.lists.more', { count: lst.sources.length - 500 })) : null);
      const toggle = button(t('firewall.lists.show', { count: lst.sources.length }), {
        variant: 'ghost', size: 'sm', iconName: 'eye',
        onClick: () => { entries.hidden = !entries.hidden; },
      });
      return h('li', { class: 'fw-catalog-item' },
        h('div', { class: 'fw-catalog-head' },
          h('div', null,
            h('strong', null, lst.name), ' ',
            badge(t(`firewall.mode.${lst.mode}`), lst.mode === 'allow' ? 'success' : 'danger'),
            h('div', { class: 'muted fw-catalog-meta' },
              [lst.author && t('firewall.lists.by', { author: lst.author }),
                lst.updated && t('firewall.lists.updated', { date: lst.updated }),
                t('firewall.lists.count', { count: lst.sources.length })].filter(Boolean).join(' · '))),
          rule
            ? badge(t('firewall.lists.subscribed'), 'success')
            : button(t('firewall.lists.subscribe'), { variant: 'primary', size: 'sm', iconName: 'plus', onClick: () => subscribe(lst, close) })),
        lst.description ? h('p', { class: 'fw-catalog-desc' }, lst.description) : null,
        h('div', null, toggle),
        entries);
    })));
  };

  const refresh = button(t('common.refresh'), { iconName: 'refresh', onClick: () => busy(refresh, () => fill(true)) });
  ({ close } = openDialog({
    title: t('firewall.lists.title'),
    description: t('firewall.lists.desc'),
    size: 'lg',
    build: () => ({
      body: [body, status],
      foot: [
        browse,
        button(t('firewall.publish.new'), { iconName: 'upload', onClick: () => { close(undefined); publishRule(); } }),
        refresh,
        button(t('common.close'), { onClick: () => close(undefined) }),
      ],
    }),
  }));
  fill(false);
}

async function subscribe(lst, close) {
  // Liste d'autorisation sur tous les ports frp : tout le reste est coupé,
  // clients frpc compris s'ils n'y figurent pas.
  if (lst.mode === 'allow' && !(await confirmDialog({
    title: t('firewall.lists.allowTitle', { name: lst.name }),
    message: t('firewall.lists.allowText'),
    confirmLabel: t('firewall.lists.subscribe'),
  }))) return;
  S.rules.push({
    id: Math.random().toString(16).slice(2, 10), name: lst.name, mode: lst.mode, ports: '*',
    sources: [], list: lst.id, enabled: true,
  });
  close(undefined);
  render();
  toast(t('firewall.lists.added', { name: lst.name }), 'info', 6000);
}

const AUTHOR_KEY = 'frpm.fw.author';

function savedAuthor() {
  try { return localStorage.getItem(AUTHOR_KEY) || ''; } catch { return ''; }
}

/** Règles dont les adresses peuvent être proposées au catalogue. */
const publishable = () => S.rules.filter((r) => r.sources.length);

/** Proposer une liste au catalogue communautaire : les adresses d'une règle
 *  « Bloquer » (rule), ou saisies ici. */
function publishRule(rule = null) {
  const formId = 'fw-publish-form';
  openDialog({
    title: rule ? t('firewall.publish.title', { name: ruleTitle(rule) }) : t('firewall.publish.titleNew'),
    description: t('firewall.publish.desc'),
    size: 'lg',
    build: () => {
      const sources = h('textarea', {
        class: 'input input-mono fw-textarea', rows: 6, spellcheck: 'false', required: true,
        placeholder: '203.0.113.4  # scanner\n198.51.100.0/24\nAS64500',
      });
      sources.value = sourcesToText(rule?.sources || []);
      const rules = publishable();
      const from = !rule && rules.length ? h('div', { class: 'fw-chips' },
        h('span', { class: 'field-hint' }, t('firewall.publish.fromRule')),
        rules.map((r) => h('button', {
          type: 'button', class: 'fw-chip',
          onClick: () => {
            const have = new Set(textToSources(sources.value).map(sourceLabel));
            const add = r.sources.filter((x) => !have.has(sourceLabel(x)));
            sources.value = [sources.value.trim(), sourcesToText(add)].filter(Boolean).join('\n');
            if (!name.value) name.value = r.name || '';
          },
        }, ruleTitle(r)))) : null;
      const name = input({ value: rule?.name || '', placeholder: t('firewall.publish.namePlaceholder'), maxLength: 60, required: true });
      const author = input({ value: savedAuthor(), placeholder: t('firewall.publish.authorPlaceholder'), maxLength: 40 });
      const desc = h('textarea', {
        class: 'input fw-textarea', rows: 3, maxLength: 300, placeholder: t('firewall.publish.descPlaceholder'),
      });
      const modeHint = h('p', { class: 'field-hint' });
      const mode = segmented(['block', 'allow'].map((v) => ({ value: v, label: t(`firewall.mode.${v}`) })),
        rule?.mode || 'block', (v) => { modeHint.textContent = t(`firewall.publish.modeHint.${v}`); },
        { label: t('firewall.publish.mode') });
      modeHint.textContent = t(`firewall.publish.modeHint.${mode.value}`);
      const notes = switchRow({ label: t('firewall.publish.notes'), description: t('firewall.publish.notesHint'), checked: false });
      const out = h('div', { class: 'form-stack' });

      const prepare = async (e) => {
        e.preventDefault();
        try { localStorage.setItem(AUTHOR_KEY, author.value.trim()); } catch { /* facultatif */ }
        const d = await api('/api/firewall/lists/publish', {
          method: 'POST',
          body: { name: name.value, mode: mode.value, author: author.value, description: desc.value, notes: notes.input.checked, sources: textToSources(sources.value) },
        });
        const skipped = d.skipped?.length ? callout({
          type: 'warn', title: t('firewall.publish.skipped', { count: d.skipped.length }),
          text: h('ul', { class: 'fw-skipped' }, d.skipped.slice(0, 20).map((s) => h('li', null, h('span', { class: 'mono' }, s.entry), ` — ${s.reason}`))),
        }) : null;
        if (!d.ok) { out.replaceChildren(callout({ type: 'danger', text: d.msg }), skipped || ''); return; }
        const open = h('a', {
          class: 'btn btn-primary', href: d.issue_url, target: '_blank', rel: 'noopener',
          onClick: () => {
            // Trop long pour le lien : l'entrée est collée à la main dans l'issue
            if (d.too_long) navigator.clipboard?.writeText(d.json).then(() => toast(t('common.copied'), 'success', 1800)).catch(() => {});
          },
        }, icon('external'), t(d.update ? 'firewall.publish.openUpdate' : 'firewall.publish.open'));
        out.replaceChildren(
          skipped || '',
          callout({ type: 'info', compact: true, text: t(d.too_long ? 'firewall.publish.tooLong' : 'firewall.publish.review') }),
          h('div', { class: 'fw-publish-preview' },
            h('div', { class: 'fw-publish-copy' }, copyButton(d.json)),
            h('pre', { class: 'mono' }, d.json)),
          h('div', null, open));
      };
      const submitBtn = button(t('firewall.publish.prepare'), { variant: 'primary', type: 'submit', form: formId });
      return {
        body: [h('form', { id: formId, class: 'form-stack', onSubmit: (e) => busy(submitBtn, () => prepare(e).catch(toastError)) },
          field({ label: t('firewall.publish.name'), control: name, hint: t('firewall.publish.nameHint') }),
          h('div', { class: 'field' }, h('span', { class: 'field-label' }, t('firewall.publish.mode')), mode, modeHint),
          h('div', { class: 'field' },
            h('label', { class: 'field-label', for: 'fw-publish-sources' }, t('firewall.publish.sources')),
            Object.assign(sources, { id: 'fw-publish-sources' }),
            h('p', { class: 'field-hint' }, t('firewall.sourcesHint')),
            from),
          field({ label: t('firewall.publish.description'), optional: true, control: desc }),
          field({ label: t('firewall.publish.author'), optional: true, control: author, hint: t('firewall.publish.authorHint') }),
          notes),
        out],
        foot: [submitBtn],
      };
    },
  });
}

// ── Édition ────────────────────────────────────────────────────────────────

function sourcesToText(sources) {
  return sources.map((s) => (s.note ? `${sourceLabel(s)}  # ${s.note}` : sourceLabel(s))).join('\n');
}

function textToSources(text) {
  return text.split('\n').map((line) => {
    const [addr, ...note] = line.split('#');
    const value = addr.trim();
    const as = value.match(/^AS\s*(\d+)$/i);
    return as ? { asn: Number(as[1]), note: note.join('#').trim() } : { cidr: value, note: note.join('#').trim() };
  }).filter((s) => s.asn || s.cidr);
}

async function editRule(existing) {
  const draft = existing
    ? { ...existing, sources: existing.sources.map((s) => ({ ...s })) }
    : { id: '', name: '', mode: 'allow', ports: '*', sources: [], enabled: true };
  const formId = 'fw-rule-form';

  const result = await openDialog({
    title: existing ? t('firewall.editTitle', { name: ruleTitle(existing) }) : t('firewall.newTitle'),
    description: t('firewall.editorHint'),
    build: ({ close }) => {
      const name = input({ value: draft.name, placeholder: t('firewall.namePlaceholder'), maxLength: 60 });
      const modeHint = h('p', { class: 'field-hint' });
      const syncMode = () => { modeHint.textContent = t(`firewall.modeHint.${mode.value}`); };
      const mode = segmented(['allow', 'block'].map((v) => ({ value: v, label: t(`firewall.mode.${v}`) })),
        draft.mode, syncMode, { label: t('firewall.action') });

      const ports = input({ value: draft.ports === '*' ? '' : draft.ports, placeholder: '7000, 25565, 30000-30010', mono: true });
      const chips = h('div', { class: 'fw-chips' }, S.data.ports.map((p) => h('button', {
        type: 'button', class: 'fw-chip', title: p.label,
        onClick: () => {
          const cur = ports.value.split(/[,\s]+/).filter(Boolean);
          if (!cur.includes(portText(p))) ports.value = [...cur, portText(p)].join(', ');
        },
      }, portText(p))));
      const portsBox = h('div', { class: 'form-stack', style: { gap: '8px' } }, ports, S.data.ports.length ? chips : null);
      const scope = segmented([
        { value: 'all', label: t('firewall.allPorts') },
        { value: 'some', label: t('firewall.somePorts') },
      ], draft.ports === '*' ? 'all' : 'some', () => { portsBox.hidden = scope.value === 'all'; }, { label: t('firewall.portsLabel') });
      portsBox.hidden = scope.value === 'all';

      const sources = h('textarea', {
        class: 'input input-mono fw-textarea', rows: 6, spellcheck: 'false',
        placeholder: '203.0.113.4  # maison\n198.51.100.0/24  # bureau\nAS16276  # OVH\n2001:db8::/32',
      });
      sources.value = sourcesToText(draft.sources);
      const addMe = S.data.client_ip && button(t('firewall.addMyIp', { ip: S.data.client_ip }), {
        size: 'sm', iconName: 'plus',
        onClick: () => {
          if (!textToSources(sources.value).some((x) => x.cidr === S.data.client_ip)) {
            sources.value = `${sources.value.trim()}\n${S.data.client_ip}  # ${t('firewall.myIpNote')}`.trim();
          }
        },
      });
      syncMode();

      const subscribed = draft.list ? callout({
        type: 'info', iconName: 'globe', compact: true,
        title: t('firewall.lists.ruleUses', { name: listText(draft.list) }),
        text: t('firewall.lists.ruleUsesHint'),
        actions: [button(t('firewall.lists.unsubscribe'), {
          size: 'sm', onClick: (e) => { draft.list = ''; e.currentTarget.closest('.callout').remove(); },
        })],
      }) : null;

      const submit = (e) => {
        e.preventDefault();
        close({
          ...draft,
          name: name.value.trim(),
          mode: mode.value,
          ports: scope.value === 'all' ? '*' : ports.value.trim(),
          sources: textToSources(sources.value),
        });
      };
      return {
        body: [h('form', { id: formId, class: 'form-stack', onSubmit: submit },
          field({ label: t('firewall.name'), optional: true, control: name }),
          h('div', { class: 'field' }, h('span', { class: 'field-label' }, t('firewall.action')), mode, modeHint),
          h('div', { class: 'field' }, h('span', { class: 'field-label' }, t('firewall.portsLabel')), scope, portsBox,
            h('p', { class: 'field-hint' }, t('firewall.portsHint', { port: S.data.panel_port }))),
          subscribed,
          h('div', { class: 'field' },
            h('label', { class: 'field-label', for: 'fw-sources' },
              t(draft.list ? 'firewall.lists.extraSources' : 'firewall.sourcesLabel'),
              draft.list ? h('span', { class: 'field-optional' }, t('common.optional')) : null),
            Object.assign(sources, { id: 'fw-sources' }),
            h('p', { class: 'field-hint' }, t('firewall.sourcesHint')),
            addMe ? h('div', null, addMe) : null))],
        foot: [
          button(t('common.cancel'), { onClick: () => close(undefined) }),
          button(existing ? t('tunnels.apply') : t('tunnels.addToList'), { variant: 'primary', type: 'submit', form: formId }),
        ],
      };
    },
  }).result;
  if (!result) return;
  if (!result.list) delete result.list;
  if (result.mode === 'block' && !result.sources.length && !result.list) {
    toast(t('firewall.blockNeedsSource'), 'error');
    return;
  }
  if (existing) {
    // Désabonnement : « list » absent de result. Les autres clés gardent leur
    // place, sinon la règle paraîtrait modifiée (comparaison en JSON).
    Object.keys(existing).forEach((k) => { if (!(k in result)) delete existing[k]; });
    Object.assign(existing, result);
  }
  else S.rules.push({ ...result, id: Math.random().toString(16).slice(2, 10) });
  render();
}

async function removeRule(rule) {
  const ok = await confirmDialog({
    title: t('firewall.deleteTitle', { name: ruleTitle(rule) }),
    message: t('tunnels.deleteText'), confirmLabel: t('common.delete'), danger: true,
  });
  if (!ok) return;
  S.rules = S.rules.filter((r) => r !== rule);
  render();
}

function discard() {
  S.enabled = S.data.enabled;
  S.rules = S.data.rules.map((r) => ({ ...r, sources: r.sources.map((s) => ({ ...s })) }));
  render();
}

async function save() {
  // Votre propre adresse serait-elle bloquée ? (seulement si le panel la connaît)
  if (S.enabled && S.data.client_ip) {
    try {
      const d = await api('/api/firewall/test', { method: 'POST', body: { ip: S.data.client_ip, rules: S.rules } });
      const blocked = d.ok ? d.verdicts.filter((v) => v.blocked_by.length) : [];
      if (blocked.length) {
        const go = await confirmDialog({
          title: t('firewall.selfBlockTitle'),
          message: t('firewall.selfBlockText', { ip: S.data.client_ip, ports: blocked.map(portText).join(', ') }),
          confirmLabel: t('firewall.apply'), danger: true,
        });
        if (!go) return;
      }
    } catch { /* test indisponible : on laisse le serveur valider */ }
  }
  try {
    const d = await api('/api/firewall', { method: 'POST', body: { enabled: S.enabled, rules: S.rules } });
    toastResult(d, t('firewall.applied'));
    if (d.ok) await load();
  } catch (e) { toastError(e); }
}
