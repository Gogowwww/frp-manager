// ── Configuration frps / frpc (hors tunnels) ──────────────────────────────
// Formulaire décrit par un schéma : sections → champs. Chaque champ connaît sa
// clé TOML (lecture) et son nom dans l'objet de valeurs passé au générateur.

import { t, hasKey } from '../i18n.js';
import { apiOk, post } from '../api.js';
import { store, instanceIds, displayName, isDocker, waitRunning } from '../store.js';
import {
  h, icon, button, toast, toastError, confirmDialog, field, input, select, secretInput, switchRow,
  callout, emptyState, pageHeader, card, saveBar,
} from '../ui.js';
import { parseToml, generateServer, generateClient, extractBlocks } from '../toml.js';
import { navigate, replaceParams } from '../router.js';

const LOG_LEVELS = ['trace', 'debug', 'info', 'warn', 'error'];

// kind           : text | number | secret | select | switch
// toggle         : interrupteur de section (activé ⇔ la clé toggle.key existe)
// optionalToggle : champ précédé d'un interrupteur (la clé n'est écrite que s'il est activé)
const SCHEMA = {
  frps: [
    {
      id: 'connection',
      fields: [
        { name: 'bindPort', key: 'bindPort', kind: 'number', def: '7000' },
        { name: 'bindAddr', key: 'bindAddr', kind: 'text', def: '0.0.0.0', placeholder: '0.0.0.0' },
        { name: 'proxyBindAddr', key: 'proxyBindAddr', kind: 'text', def: '', optional: true, placeholder: '203.0.113.10' },
      ],
    },
    {
      id: 'auth',
      fields: [
        { name: 'authMethod', key: 'auth.method', kind: 'select', options: ['token', 'oidc'], def: 'token' },
        { name: 'authToken', key: 'auth.token', kind: 'secret', def: '' },
      ],
    },
    {
      id: 'dashboard',
      toggle: { name: 'webEnabled', key: 'webServer.port' },
      fields: [
        { name: 'webPort', key: 'webServer.port', kind: 'number', def: '7500' },
        { name: 'webAddr', key: 'webServer.addr', kind: 'text', def: '0.0.0.0' },
        { name: 'webUser', key: 'webServer.user', kind: 'text', def: 'admin' },
        { name: 'webPassword', key: 'webServer.password', kind: 'secret', def: '' },
      ],
    },
    {
      id: 'tls', advanced: true,
      toggle: { name: 'tlsEnabled', key: 'transport.tls.certFile' },
      fields: [
        { name: 'tlsCert', key: 'transport.tls.certFile', kind: 'text', def: '', placeholder: '/etc/frp/server.crt' },
        { name: 'tlsKey', key: 'transport.tls.keyFile', kind: 'text', def: '', placeholder: '/etc/frp/server.key' },
        { name: 'tlsCa', key: 'transport.tls.trustedCaFile', kind: 'text', def: '', optional: true, placeholder: '/etc/frp/ca.crt' },
        { name: 'tlsForce', key: 'transport.tls.force', kind: 'switch', def: false },
      ],
    },
    {
      id: 'extraPorts', advanced: true,
      fields: [
        { name: 'kcpBindPort', key: 'kcpBindPort', kind: 'number', def: '7000', optionalToggle: 'kcpEnabled' },
        { name: 'quicBindPort', key: 'quicBindPort', kind: 'number', def: '7002', optionalToggle: 'quicEnabled' },
        { name: 'vhostHTTPPort', key: 'vhostHTTPPort', kind: 'number', def: '80', optionalToggle: 'vhostHttpEnabled' },
        { name: 'vhostHTTPSPort', key: 'vhostHTTPSPort', kind: 'number', def: '443', optionalToggle: 'vhostHttpsEnabled' },
      ],
    },
    {
      id: 'limits', advanced: true,
      fields: [
        { name: 'maxPortsPerClient', key: 'maxPortsPerClient', kind: 'number', def: '0' },
        { name: 'maxPoolCount', key: 'transport.maxPoolCount', kind: 'number', def: '5' },
        { name: 'heartbeatTimeout', key: 'transport.heartbeatTimeout', kind: 'number', def: '90' },
      ],
    },
    {
      id: 'logs', advanced: true,
      fields: [
        { name: 'logTo', key: 'log.to', kind: 'text', def: '/var/log/frp/frps.log' },
        { name: 'logLevel', key: 'log.level', kind: 'select', options: LOG_LEVELS, def: 'info' },
        { name: 'logMaxDays', key: 'log.maxDays', kind: 'number', def: '3' },
      ],
    },
  ],
  frpc: [
    {
      id: 'server',
      fields: [
        { name: 'serverAddr', key: 'serverAddr', kind: 'text', def: '', placeholder: 'vps.example.com' },
        { name: 'serverPort', key: 'serverPort', kind: 'number', def: '7000' },
        { name: 'protocol', key: 'transport.protocol', kind: 'select', options: ['tcp', 'kcp', 'quic', 'websocket', 'wss'], def: 'tcp' },
      ],
    },
    {
      id: 'auth',
      fields: [
        { name: 'authMethod', key: 'auth.method', kind: 'select', options: ['token', 'oidc'], def: 'token' },
        { name: 'authToken', key: 'auth.token', kind: 'secret', def: '' },
      ],
    },
    {
      id: 'tls', advanced: true,
      toggle: { name: 'tlsEnabled', key: 'transport.tls.certFile' },
      fields: [
        { name: 'tlsCert', key: 'transport.tls.certFile', kind: 'text', def: '', placeholder: '/etc/frp/client.crt' },
        { name: 'tlsKey', key: 'transport.tls.keyFile', kind: 'text', def: '', placeholder: '/etc/frp/client.key' },
        { name: 'tlsCa', key: 'transport.tls.trustedCaFile', kind: 'text', def: '', optional: true, placeholder: '/etc/frp/ca.crt' },
        { name: 'tlsServerName', key: 'transport.tls.serverName', kind: 'text', def: '', optional: true, placeholder: 'vps.example.com' },
      ],
    },
    {
      id: 'localDashboard', advanced: true,
      toggle: { name: 'webEnabled', key: 'webServer.port' },
      fields: [
        { name: 'webPort', key: 'webServer.port', kind: 'number', def: '7400' },
        { name: 'webAddr', key: 'webServer.addr', kind: 'text', def: '127.0.0.1' },
        { name: 'webUser', key: 'webServer.user', kind: 'text', def: 'admin' },
        { name: 'webPassword', key: 'webServer.password', kind: 'secret', def: '' },
      ],
    },
    {
      id: 'network', advanced: true,
      fields: [
        { name: 'poolCount', key: 'transport.poolCount', kind: 'number', def: '5' },
        { name: 'heartbeatInterval', key: 'transport.heartbeatInterval', kind: 'number', def: '30' },
        { name: 'dnsServer', key: 'dnsServer', kind: 'text', def: '', optional: true, placeholder: '1.1.1.1' },
        { name: 'proxyURL', key: 'transport.proxyURL', kind: 'text', def: '', optional: true, placeholder: 'http://proxy:8080' },
      ],
    },
    {
      id: 'logs', advanced: true,
      fields: [
        { name: 'logTo', key: 'log.to', kind: 'text', def: '/var/log/frp/frpc.log' },
        { name: 'logLevel', key: 'log.level', kind: 'select', options: LOG_LEVELS, def: 'info' },
        { name: 'logMaxDays', key: 'log.maxDays', kind: 'number', def: '3' },
      ],
    },
  ],
};

const S = { iid: '', type: '', controls: {}, snapshot: '', els: {} };

function values() {
  const v = {};
  for (const [name, ctrl] of Object.entries(S.controls)) {
    v[name] = ctrl.type === 'checkbox' ? ctrl.checked : ctrl.value.trim();
  }
  return v;
}
const isDirty = () => S.snapshot !== '' && JSON.stringify(values()) !== S.snapshot;
function updateSaveBar() { if (S.els.savebar) S.els.savebar.hidden = !isDirty(); }
function beforeUnload(e) { if (isDirty()) { e.preventDefault(); e.returnValue = ''; } }

export default {
  id: 'config',
  title: () => t('nav.config'),

  async mount(view, params) {
    const ids = instanceIds();
    S.iid = ids.includes(params.iid) ? params.iid : ids.find((id) => !isDocker(store.instances[id])) || ids[0] || '';
    S.snapshot = '';
    S.controls = {};
    window.addEventListener('beforeunload', beforeUnload);

    const actions = [];
    if (ids.length > 1) {
      const sel = select(ids.map((id) => ({ value: id, label: displayName(id) })), S.iid, {
        class: 'select select-inline', 'aria-label': t('common.instance'),
      });
      sel.addEventListener('change', async () => {
        if (isDirty() && !(await confirmLeave())) { sel.value = S.iid; return; }
        S.iid = sel.value;
        replaceParams({ iid: S.iid });
        load();
      });
      actions.push(sel);
    }

    S.els.body = h('div', { class: 'form-stack', style: { gap: '20px' } });
    S.els.body.addEventListener('input', updateSaveBar);
    S.els.body.addEventListener('change', updateSaveBar);
    S.els.savebar = saveBar({
      onDiscard: () => { S.snapshot = ''; load(); },
      onSave: () => save(false),
      onSaveRestart: () => save(true),
    });
    view.append(h('div', { class: 'page' },
      pageHeader({ title: t('config.title'), description: t('config.description'), actions }),
      S.els.body, S.els.savebar));

    if (!S.iid) {
      S.els.body.append(h('div', { class: 'card' }, emptyState({
        iconName: 'config', title: t('config.emptyTitle'), text: t('config.emptyText'),
        actions: [button(t('nav.dashboard'), { onClick: () => navigate('dashboard') })],
      })));
      return;
    }
    await load();
  },

  unmount() {
    window.removeEventListener('beforeunload', beforeUnload);
    S.els = {};
  },

  canLeave() { return isDirty() ? confirmLeave() : true; },
};

function confirmLeave() {
  return confirmDialog({ title: t('common.leaveTitle'), message: t('common.leaveText'), confirmLabel: t('common.leave'), danger: true });
}

async function load() {
  const inst = store.instances[S.iid];
  S.type = inst.type === 'frps' ? 'frps' : 'frpc';
  S.controls = {};
  S.els.savebar.hidden = true;

  if (isDocker(inst)) {
    S.snapshot = '';
    S.els.body.replaceChildren(callout({
      type: 'warn', compact: true, iconName: 'box', title: t('config.dockerTitle'), text: t('config.dockerText'),
      actions: inst.type === 'frpc' ? [button(t('nav.tunnels'), { size: 'sm', onClick: () => navigate('tunnels', { iid: S.iid }) })] : [],
    }));
    return;
  }

  S.els.body.replaceChildren(h('div', { class: 'skeleton', style: { height: '240px' } }));
  let data;
  try {
    data = await apiOk(`/api/config/${encodeURIComponent(S.iid)}`);
  } catch (e) {
    S.els.body.replaceChildren(callout({ type: 'danger', title: t('config.loadError'), text: e.message }));
    return;
  }
  const cfg = parseToml(data.content || '').values;

  const top = [];
  if (!data.exists) top.push(callout({ type: 'info', text: t('config.missingFile', { path: inst.config_path || inst.config || '' }) }));
  if (S.type === 'frpc') {
    top.push(callout({
      type: 'neutral', iconName: 'tunnels', text: t('config.tunnelsElsewhere'),
      actions: [button(t('nav.tunnels'), { size: 'sm', onClick: () => navigate('tunnels', { iid: S.iid }) })],
    }));
  }

  const sections = SCHEMA[S.type];
  const basic = sections.filter((s) => !s.advanced).map((s) => sectionCard(s, cfg));
  const advanced = sections.filter((s) => s.advanced).map((s) => sectionBlock(s, cfg));

  const details = h('details', { class: 'card disclosure' },
    h('summary', null, icon('chevron-down'), t('config.advanced'), h('span', { class: 'summary-hint' }, t(`config.advancedHint.${S.type}`))),
    h('div', { class: 'disclosure-body' }, advanced));

  S.els.body.replaceChildren(...top, ...basic, details);
  S.snapshot = JSON.stringify(values());
  updateSaveBar();
}

function sectionTitle(section) {
  return t(`config.sections.${S.type}.${section.id}.title`);
}
function sectionDesc(section) {
  return t(`config.sections.${S.type}.${section.id}.desc`);
}

function buildFields(section, cfg) {
  const fieldEls = [];
  let toggleInput = null;
  if (section.toggle) {
    const row = switchRow({ label: t(`config.toggles.${section.toggle.name}`), checked: !!cfg[section.toggle.key] });
    toggleInput = row.input;
    S.controls[section.toggle.name] = toggleInput;
    fieldEls.push(row);
  }
  const grid = h('div', { class: 'form-grid' });
  for (const f of section.fields) {
    const raw = cfg[f.key];
    const label = t(`config.fields.${f.name}.label`);
    const hintKey = `config.fields.${f.name}.hint`;
    const hint = hasKey(hintKey) ? t(hintKey) : null;

    if (f.kind === 'switch') {
      const row = switchRow({ label, description: hint, checked: raw === 'true' });
      S.controls[f.name] = row.input;
      grid.append(h('div', { class: 'field', style: { justifyContent: 'flex-end' } }, row));
      continue;
    }
    let ctrl;
    let el;
    if (f.kind === 'select') {
      ctrl = select(f.options, raw || f.def);
      el = ctrl;
    } else if (f.kind === 'secret') {
      el = secretInput({ value: raw || f.def, placeholder: f.placeholder || '' });
      ctrl = el.input;
    } else {
      ctrl = input({ value: raw || f.def, placeholder: f.placeholder || '', mono: true, inputmode: f.kind === 'number' ? 'numeric' : null });
      el = ctrl;
    }
    S.controls[f.name] = ctrl;

    if (f.optionalToggle) {
      // Port optionnel : l'interrupteur ajoute/retire la clé du fichier
      const enabled = !!raw;
      const sw = switchRow({ label, description: hint, checked: enabled });
      S.controls[f.optionalToggle] = sw.input;
      ctrl.disabled = !enabled;
      ctrl.setAttribute('aria-label', label);
      sw.input.addEventListener('change', () => { ctrl.disabled = !sw.input.checked; });
      grid.append(h('div', { class: 'field' }, sw, h('div', { class: 'field-inline' }, el, h('span', { class: 'field-key' }, f.key))));
      continue;
    }
    grid.append(field({ label, hint, tomlKey: f.key, control: el, optional: f.optional }));
  }
  fieldEls.push(grid);

  if (toggleInput) {
    const apply = () => {
      grid.hidden = !toggleInput.checked;
    };
    toggleInput.addEventListener('change', apply);
    apply();
  }
  return fieldEls;
}

function sectionCard(section, cfg) {
  return card({ title: sectionTitle(section), description: sectionDesc(section), body: h('div', { class: 'form-stack' }, buildFields(section, cfg)) });
}

function sectionBlock(section, cfg) {
  return h('section', { class: 'form-stack' },
    h('div', null,
      h('h3', { class: 'fieldset-title' }, sectionTitle(section)),
      h('p', { class: 'field-hint' }, sectionDesc(section))),
    buildFields(section, cfg));
}

async function save(restart) {
  const v = values();
  try {
    const content = S.type === 'frps' ? generateServer(v, S.iid) : await withExistingBlocks(generateClient(v, S.iid));
    const d = await post(`/api/config/${encodeURIComponent(S.iid)}`, { content });
    if (!d.ok) { toast(d.msg || t('errors.generic'), 'error'); return; }
    S.snapshot = JSON.stringify(values());
    updateSaveBar();
    toast(t('config.saved'), 'success');
    if (restart) {
      toast(t('tunnels.restarting', { name: displayName(S.iid) }), 'info');
      post(`/api/service/${encodeURIComponent(S.iid)}/restart`).catch(() => {});
      const up = await waitRunning(S.iid);
      toast(up ? t('tunnels.restarted', { name: displayName(S.iid) }) : t('tunnels.restartUnknown'), up ? 'success' : 'info');
    }
  } catch (e) { toastError(e); }
}

/** frpc : conserve les [[proxies]] / [[visitors]] actuels du fichier. */
async function withExistingBlocks(base) {
  try {
    const d = await apiOk(`/api/config/${encodeURIComponent(S.iid)}`);
    const blocks = extractBlocks(d.content || '');
    return blocks ? `${base}\n${blocks}\n` : base;
  } catch {
    throw new Error(t('config.blocksReadError'));
  }
}
