// ── Pare-feu : qui peut se connecter aux ports qu'ouvre frps ──────────────
// Les règles se modifient dans un brouillon local ; la barre du bas les
// applique d'un coup (table nftables remplacée atomiquement côté serveur).

import { t } from '../i18n.js';
import { api, apiOk } from '../api.js';
import {
  h, icon, button, busy, toast, toastResult, toastError, openDialog, confirmDialog,
  field, input, switchControl, switchRow, segmented, callout, emptyState, pageHeader, badge, saveBar,
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
    }));
    return;
  }

  S.els.status = h('div', { class: 'card' });
  S.els.rules = h('div', { class: 'card' });
  S.els.ports = h('div', { class: 'card' });
  S.els.savebar = saveBar({ onDiscard: discard, onSave: save, saveLabel: t('firewall.apply') });

  root.replaceChildren(
    head,
    S.els.status,
    h('section', null,
      h('div', { class: 'section-head' },
        h('div', null,
          h('h2', { class: 'section-title' }, t('firewall.rulesTitle')),
          h('p', { class: 'section-desc' }, t('firewall.rulesDesc'))),
        button(t('firewall.addRule'), { variant: 'primary', iconName: 'plus', onClick: () => editRule(null) })),
      S.els.rules),
    h('section', null,
      h('div', { class: 'section-head' },
        h('div', null,
          h('h2', { class: 'section-title' }, t('firewall.portsTitle')),
          h('p', { class: 'section-desc' }, t('firewall.portsDesc')))),
      S.els.ports),
    testSection(),
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

  S.els.status.replaceChildren(h('div', { class: 'card-body form-stack' },
    h('div', { class: 'fw-status' }, icon('shield', 'icon-lg'), h('div', null, state)),
    master,
    ...you));
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
        h('span', { title: rule.sources.map(sourceTitle).join('\n') }, rule.sources.length
          ? rule.sources.slice(0, 2).map(sourceLabel).join(', ') + (rule.sources.length > 2 ? ` +${rule.sources.length - 2}` : '')
          : t('firewall.noSource')))),
    h('div', { class: 'fw-count', title: t('firewall.countHint'), dataset: { rule: state || !rule.enabled ? '' : rule.id } },
      count != null && !state && rule.enabled ? t('firewall.blockedCount', { count }) : ''),
    h('div', { class: 'row-actions' },
      sw,
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
  return h('section', null,
    h('div', { class: 'section-head' },
      h('div', null,
        h('h2', { class: 'section-title' }, t('firewall.testTitle')),
        h('p', { class: 'section-desc' }, t('firewall.testDesc')))),
    h('div', { class: 'card' }, h('div', { class: 'card-body form-stack' },
      h('form', { id: 'fw-test', class: 'fw-test-form', onSubmit: (e) => busy(btn, () => run(e)) }, ip, btn),
      out)));
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
      body: [h('div', { class: 'form-stack', style: { gap: '8px' } },
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
          h('div', { class: 'field' },
            h('label', { class: 'field-label', for: 'fw-sources' }, t('firewall.sourcesLabel')),
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
  if (result.mode === 'block' && !result.sources.length) {
    toast(t('firewall.blockNeedsSource'), 'error');
    return;
  }
  if (existing) Object.assign(existing, result);
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
