// ── Pare-feu : qui peut se connecter aux ports qu'ouvre frps ──────────────
// Les règles se modifient dans un brouillon local ; la barre du bas les
// applique d'un coup (table nftables remplacée atomiquement côté serveur).

import { t } from '../i18n.js';
import { api, apiOk } from '../api.js';
import {
  h, icon, button, busy, toast, toastResult, toastError, openDialog, confirmDialog,
  field, input, switchControl, switchRow, segmented, callout, emptyState, pageHeader, badge, saveBar,
} from '../ui.js';
import { navigate } from '../router.js';

const S = {
  data: null,
  enabled: false,
  rules: [],
  snapshot: '',
  els: {},
};

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
        h('span', null, rule.sources.length
          ? rule.sources.slice(0, 2).map((s) => s.cidr).join(', ') + (rule.sources.length > 2 ? ` +${rule.sources.length - 2}` : '')
          : t('firewall.noSource')))),
    h('div', { class: 'fw-count', title: t('firewall.countHint') },
      count != null && !state && rule.enabled ? t('firewall.blockedCount', { count }) : ''),
    h('div', { class: 'row-actions' },
      sw,
      button('', { variant: 'ghost', size: 'sm', iconName: 'edit', title: t('common.edit'), onClick: (e) => { e.stopPropagation(); open(); } }),
      button('', { variant: 'ghost-danger', size: 'sm', iconName: 'trash', title: t('common.delete'), onClick: (e) => { e.stopPropagation(); removeRule(rule); } })));
  })));
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

function blockedSection() {
  const body = h('div', null, h('p', { class: 'muted', style: { padding: '16px 20px' } }, t('common.loading')));
  const names = () => Object.fromEntries(S.rules.map((r) => [r.id, ruleTitle(r)]));
  const loadBlocked = async () => {
    try {
      const d = await api('/api/firewall/blocked');
      if (!d.ok) { body.replaceChildren(h('p', { class: 'muted', style: { padding: '16px 20px' } }, d.msg)); return; }
      if (!d.entries.length) {
        body.replaceChildren(h('p', { class: 'muted', style: { padding: '16px 20px' } }, t('firewall.blockedEmpty')));
        return;
      }
      const byId = names();
      body.replaceChildren(h('div', { class: 'fw-table-wrap' }, h('table', { class: 'fw-table' },
        h('thead', null, h('tr', null,
          h('th', null, t('firewall.col.time')), h('th', null, t('firewall.col.source')),
          h('th', null, t('firewall.col.port')), h('th', null, t('firewall.col.rule')))),
        h('tbody', null, d.entries.map((e) => h('tr', null,
          h('td', { class: 'mono muted' }, formatTime(e.time)),
          h('td', { class: 'mono' }, e.src),
          h('td', { class: 'mono' }, e.port ?? '—', h('span', { class: 'muted' }, ` /${e.proto}`)),
          h('td', null, byId[e.rule] || h('span', { class: 'muted' }, t('firewall.deletedRule')))))))));
    } catch (err) { toastError(err); }
  };
  const refresh = button(t('common.refresh'), { size: 'sm', iconName: 'refresh', onClick: () => busy(refresh, loadBlocked) });
  loadBlocked();
  return h('section', null,
    h('div', { class: 'section-head' },
      h('div', null,
        h('h2', { class: 'section-title' }, t('firewall.blockedTitle')),
        h('p', { class: 'section-desc' }, t('firewall.blockedDesc'))),
      refresh),
    h('div', { class: 'card' }, body));
}

function formatTime(iso) {
  const d = new Date(iso.replace(/([+-]\d{2})(\d{2})$/, '$1:$2'));
  return Number.isNaN(d.getTime()) ? iso : d.toLocaleString();
}

// ── Édition ────────────────────────────────────────────────────────────────

function sourcesToText(sources) {
  return sources.map((s) => (s.note ? `${s.cidr}  # ${s.note}` : s.cidr)).join('\n');
}

function textToSources(text) {
  return text.split('\n').map((line) => {
    const [addr, ...note] = line.split('#');
    return { cidr: addr.trim(), note: note.join('#').trim() };
  }).filter((s) => s.cidr);
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
        placeholder: '203.0.113.4  # maison\n198.51.100.0/24  # bureau\n2001:db8::/32',
      });
      sources.value = sourcesToText(draft.sources);
      const addMe = S.data.client_ip && button(t('firewall.addMyIp', { ip: S.data.client_ip }), {
        size: 'sm', iconName: 'plus',
        onClick: () => {
          if (!textToSources(sources.value).some((s) => s.cidr === S.data.client_ip)) {
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
