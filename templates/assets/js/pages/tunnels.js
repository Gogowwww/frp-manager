// ── Tunnels : [[proxies]] et [[visitors]] d'une instance frpc ─────────────
// Les modifications se font dans un brouillon local ; la barre du bas
// enregistre tout d'un coup (écriture TOML, puis redémarrage si demandé).

import { t } from '../i18n.js';
import { api, apiOk, post } from '../api.js';
import { frpcIds, displayName, waitRunning } from '../store.js';
import {
  h, icon, button, toast, toastError, openDialog, confirmDialog,
  field, setFieldError, input, select, secretInput, switchRow, segmented, emptyState,
  pageHeader, badge, saveBar,
} from '../ui.js';
import {
  parseToml, composeClientConfig, proxyFromBlock, visitorFromBlock, newProxy, newVisitor,
  PROXY_TYPES, VISITOR_TYPES, hasRemotePort, hasDomains, hasSecret,
} from '../toml.js';
import { navigate, replaceParams } from '../router.js';

const S = {
  iid: '',
  baseText: '',
  serverAddr: '',
  proxies: [],
  visitors: [],
  snapshot: '',
  els: {},
};

const snapshotOf = () => JSON.stringify([S.proxies, S.visitors]);
const isDirty = () => S.snapshot !== '' && snapshotOf() !== S.snapshot;

function beforeUnload(e) {
  if (isDirty()) { e.preventDefault(); e.returnValue = ''; }
}

export default {
  id: 'tunnels',
  title: () => t('nav.tunnels'),

  async mount(view, params) {
    const ids = frpcIds();
    S.iid = ids.includes(params.iid) ? params.iid : ids[0] || '';
    S.snapshot = '';
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
    const addBtn = button(t('tunnels.add'), { variant: 'primary', iconName: 'plus', onClick: () => editProxy(null) });
    if (S.iid) actions.push(addBtn);

    S.els.proxies = h('div', { class: 'card' });
    S.els.visitors = h('div', { class: 'card' });
    S.els.savebar = saveBar({
      onDiscard: discard,
      onSave: () => save(false),
      onSaveRestart: () => save(true),
      restartLabel: t('tunnels.saveRestart'),
    });

    view.append(h('div', { class: 'page' },
      pageHeader({ title: t('tunnels.title'), description: t('tunnels.description'), actions }),
      S.els.proxies,
      h('section', null,
        h('div', { class: 'section-head' },
          h('div', null,
            h('h2', { class: 'section-title' }, t('visitors.title')),
            h('p', { class: 'section-desc' }, t('visitors.description'))),
          S.iid && button(t('visitors.add'), { iconName: 'plus', onClick: () => editVisitor(null) })),
        S.els.visitors),
      S.els.savebar));

    if (!S.iid) {
      S.els.proxies.append(emptyState({
        iconName: 'laptop', title: t('tunnels.noClientTitle'), text: t('tunnels.noClientText'),
        actions: [button(t('nav.dashboard'), { onClick: () => navigate('dashboard') })],
      }));
      S.els.visitors.closest('section').hidden = true;
      return;
    }
    await load();
  },

  unmount() {
    window.removeEventListener('beforeunload', beforeUnload);
    S.els = {};
  },

  canLeave() {
    return isDirty() ? confirmLeave() : true;
  },
};

function confirmLeave() {
  return confirmDialog({
    title: t('common.leaveTitle'), message: t('common.leaveText'), confirmLabel: t('common.leave'), danger: true,
  });
}

// ── Chargement ─────────────────────────────────────────────────────────────

async function load() {
  S.els.proxies.replaceChildren(h('div', { class: 'skeleton', style: { height: '120px', margin: '16px' } }));
  try {
    const d = await apiOk(`/api/config/${encodeURIComponent(S.iid)}`);
    S.baseText = d.content || '';
    const cfg = parseToml(S.baseText);
    S.serverAddr = cfg.values.serverAddr || '';
    S.proxies = cfg.proxies.map(proxyFromBlock);
    S.visitors = cfg.visitors.map(visitorFromBlock);
    S.snapshot = snapshotOf();
  } catch (e) {
    toastError(e);
    S.proxies = [];
    S.visitors = [];
    S.snapshot = snapshotOf();
  }
  render();
}

function discard() {
  S.snapshot = '';
  load();
}

// ── Rendu ──────────────────────────────────────────────────────────────────

function render() {
  if (!S.els.proxies) return;
  renderProxies();
  renderVisitors();
  S.els.savebar.hidden = !isDirty();
}

function stateOf(item, list) {
  const original = JSON.parse(S.snapshot || '[[],[]]')[list === 'visitors' ? 1 : 0];
  const before = original.find((o) => o.uid === item.uid);
  if (!before) return 'new';
  return JSON.stringify(before) === JSON.stringify(item) ? '' : 'changed';
}

function renderProxies() {
  const host = S.els.proxies;
  if (!S.proxies.length) {
    host.replaceChildren(emptyState({
      iconName: 'tunnels', title: t('tunnels.emptyTitle'), text: t('tunnels.emptyText'),
      actions: [button(t('tunnels.add'), { variant: 'primary', iconName: 'plus', onClick: () => editProxy(null) })],
    }));
    return;
  }
  host.replaceChildren(h('ul', { class: 'rows', 'aria-label': t('tunnels.title') }, S.proxies.map(proxyRow)));
}

function publicEndpoint(p) {
  if (hasRemotePort(p.type)) {
    return p.remotePort ? `${S.serverAddr || t('tunnels.server')}:${p.remotePort}` : t('tunnels.randomPort');
  }
  if (hasDomains(p.type)) return p.domains || t('tunnels.noDomain');
  return t('tunnels.privateOnly');
}

function route(fromLabel, from, toLabel, to, fromIsText = false) {
  return h('div', { class: 'route' },
    h('span', { class: 'route-end' }, h('small', null, fromLabel), h('span', { class: fromIsText ? 'route-text' : 'mono', title: from }, from)),
    h('span', { class: 'route-line', 'aria-hidden': 'true' }),
    h('span', { class: 'route-end' }, h('small', null, toLabel), h('span', { class: 'mono' }, to)));
}

function proxyRow(p) {
  const flags = [];
  if (p.proxyProtocol) flags.push(badge(t('tunnels.flags.proxyProtocol', { v: p.proxyProtocol }), ''));
  if (p.encryption) flags.push(badge(t('tunnels.flags.encrypted'), ''));
  if (p.compression) flags.push(badge(t('tunnels.flags.compressed'), ''));

  const state = stateOf(p, 'proxies');
  const open = () => editProxy(p);
  return h('li', {
    class: `row${state ? ` is-${state}` : ''}`, dataset: { state: state ? t(`common.state.${state}`) : '' },
    tabindex: '0', onClick: open, onKeydown: (e) => { if (e.key === 'Enter') open(); },
  },
  h('div', { class: 'row-name' }, h('span', null, p.name || t('tunnels.unnamed')), h('span', { class: 'badge badge-mono badge-outline' }, p.type.toUpperCase())),
  route(t('tunnels.public'), publicEndpoint(p), t('tunnels.local'), `${p.localIP || '127.0.0.1'}:${p.localPort || '?'}`,
    hasSecret(p.type) || (hasRemotePort(p.type) && !p.remotePort) || (hasDomains(p.type) && !p.domains)),
  h('div', { class: 'row-flags' }, flags),
  h('div', { class: 'row-actions' },
    button('', { variant: 'ghost', size: 'sm', iconName: 'edit', title: t('common.edit'), onClick: (e) => { e.stopPropagation(); open(); } }),
    button('', { variant: 'ghost-danger', size: 'sm', iconName: 'trash', title: t('common.delete'), onClick: (e) => { e.stopPropagation(); removeProxy(p); } })));
}

function renderVisitors() {
  const host = S.els.visitors;
  if (!host) return;
  if (!S.visitors.length) {
    host.replaceChildren(h('p', { class: 'muted', style: { padding: '16px 20px' } }, t('visitors.empty')));
    return;
  }
  host.replaceChildren(h('ul', { class: 'rows' }, S.visitors.map((v) => {
    const state = stateOf(v, 'visitors');
    const open = () => editVisitor(v);
    return h('li', {
      class: `row${state ? ` is-${state}` : ''}`, dataset: { state: state ? t(`common.state.${state}`) : '' },
      tabindex: '0', onClick: open, onKeydown: (e) => { if (e.key === 'Enter') open(); },
    },
    h('div', { class: 'row-name' }, h('span', null, v.name || t('tunnels.unnamed')), h('span', { class: 'badge badge-mono badge-outline' }, v.type.toUpperCase())),
    route(t('visitors.shared'), v.serverName || '?', t('visitors.here'), `${v.bindAddr || '127.0.0.1'}:${v.bindPort || '?'}`),
    h('div', { class: 'row-flags' }),
    h('div', { class: 'row-actions' },
      button('', { variant: 'ghost', size: 'sm', iconName: 'edit', title: t('common.edit'), onClick: (e) => { e.stopPropagation(); open(); } }),
      button('', { variant: 'ghost-danger', size: 'sm', iconName: 'trash', title: t('common.delete'), onClick: (e) => { e.stopPropagation(); removeVisitor(v); } })));
  })));
}

async function removeProxy(p) {
  const ok = await confirmDialog({
    title: t('tunnels.deleteTitle', { name: p.name || t('tunnels.unnamed') }),
    message: t('tunnels.deleteText'), confirmLabel: t('common.delete'), danger: true,
  });
  if (!ok) return;
  S.proxies = S.proxies.filter((x) => x.uid !== p.uid);
  render();
}

async function removeVisitor(v) {
  const ok = await confirmDialog({
    title: t('visitors.deleteTitle', { name: v.name || t('tunnels.unnamed') }),
    message: t('tunnels.deleteText'), confirmLabel: t('common.delete'), danger: true,
  });
  if (!ok) return;
  S.visitors = S.visitors.filter((x) => x.uid !== v.uid);
  render();
}

// ── Éditeur de tunnel ──────────────────────────────────────────────────────

const PORT_RE = /^\d{1,5}$/;
const validPort = (v) => PORT_RE.test(v) && +v >= 1 && +v <= 65535;

function editProxy(existing) {
  const draft = existing ? { ...existing, extras: existing.extras } : newProxy();

  openDialog({
    title: existing ? t('tunnels.editTitle', { name: existing.name }) : t('tunnels.newTitle'),
    description: t('tunnels.editorHint'),
    size: 'lg',
    build: ({ close }) => {
      const name = input({ value: draft.name, placeholder: t('tunnels.fields.namePlaceholder'), mono: true, autocomplete: 'off' });
      const typeHint = h('p', { class: 'field-hint' });
      const type = segmented(PROXY_TYPES.map((v) => ({ value: v, label: v.toUpperCase() })), draft.type, () => sync(), { label: t('tunnels.fields.type') });

      const localIP = input({ value: draft.localIP, placeholder: '127.0.0.1', mono: true });
      const localPort = input({ value: draft.localPort, placeholder: '8080', inputmode: 'numeric', mono: true });
      const remotePort = input({ value: draft.remotePort, placeholder: '8080', inputmode: 'numeric', mono: true });
      const domains = input({ value: draft.domains, placeholder: 'app.example.com', mono: true });
      const secret = secretInput({ value: draft.secretKey, placeholder: t('tunnels.fields.secretPlaceholder') });

      const remoteField = field({ label: t('tunnels.fields.remotePort'), hint: t('tunnels.fields.remotePortHint', { server: S.serverAddr || t('tunnels.server') }), tomlKey: 'remotePort', control: remotePort });
      const domainsField = field({ label: t('tunnels.fields.domains'), hint: t('tunnels.fields.domainsHint'), tomlKey: 'customDomains', control: domains });
      const secretField = field({ label: t('tunnels.fields.secret'), hint: t('tunnels.fields.secretHint'), tomlKey: 'secretKey', control: secret });

      // Options
      const ppv = select([
        { value: '', label: t('tunnels.fields.ppOff') },
        { value: 'v1', label: 'v1' },
        { value: 'v2', label: t('tunnels.fields.ppV2') },
      ], draft.proxyProtocol);
      const ppField = field({ label: t('tunnels.fields.proxyProtocol'), hint: t('tunnels.fields.proxyProtocolHint'), tomlKey: 'transport.proxyProtocolVersion', control: ppv });
      const encryption = switchRow({ label: t('tunnels.fields.encryption'), description: t('tunnels.fields.encryptionHint'), checked: draft.encryption });
      const compression = switchRow({ label: t('tunnels.fields.compression'), description: t('tunnels.fields.compressionHint'), checked: draft.compression });

      function sync() {
        const tp = type.value;
        typeHint.textContent = t(`tunnels.types.${tp}`);
        remoteField.hidden = !hasRemotePort(tp);
        domainsField.hidden = !hasDomains(tp);
        secretField.hidden = !hasSecret(tp);
      }
      sync();

      function submit(e) {
        e.preventDefault();
        const tp = type.value;
        const values = {
          name: name.value.trim(),
          localIP: localIP.value.trim(),
          localPort: localPort.value.trim(),
          remotePort: remotePort.value.trim(),
        };

        let ok = true;
        const check = (ctrl, msg) => { setFieldError(ctrl, msg); if (msg) ok = false; };
        check(name, !values.name ? t('validation.required')
          : S.proxies.some((p) => p.uid !== draft.uid && p.name === values.name) ? t('validation.nameTaken') : '');
        check(localIP, !values.localIP ? t('validation.required') : '');
        check(localPort, !validPort(values.localPort) ? t('validation.port') : '');
        check(remotePort, hasRemotePort(tp) && values.remotePort && !validPort(values.remotePort) ? t('validation.port') : '');
        if (!ok) { draftDialogFocusError(); return; }

        Object.assign(draft, values, {
          type: tp,
          domains: domains.value.trim(),
          secretKey: secret.input.value,
          proxyProtocol: ppv.value,
          encryption: encryption.input.checked,
          compression: compression.input.checked,
        });
        if (existing) S.proxies = S.proxies.map((p) => (p.uid === draft.uid ? draft : p));
        else S.proxies = [...S.proxies, draft];
        close(true);
        render();
      }

      const formId = `proxy-form-${draft.uid}`;
      return {
        body: [h('form', { id: formId, class: 'form-stack', onSubmit: submit, novalidate: true },
          field({ label: t('tunnels.fields.name'), hint: t('tunnels.fields.nameHint'), tomlKey: 'name', control: name }),
          h('div', { class: 'field' },
            h('span', { class: 'field-label' }, t('tunnels.fields.type'), h('span', { class: 'field-key' }, 'type')),
            type, typeHint),
          h('fieldset', { class: 'fieldset' },
            h('legend', { class: 'fieldset-title' }, t('tunnels.fields.localService')),
            h('div', { class: 'form-grid-2' },
              field({ label: t('tunnels.fields.localIP'), tomlKey: 'localIP', control: localIP }),
              field({ label: t('tunnels.fields.localPort'), tomlKey: 'localPort', control: localPort })),
            h('p', { class: 'field-hint' }, t('tunnels.fields.localHint'))),
          h('fieldset', { class: 'fieldset' },
            h('legend', { class: 'fieldset-title' }, t('tunnels.fields.publicAccess')),
            remoteField, domainsField, secretField),
          h('details', { class: 'fieldset disclosure-inline', open: draft.encryption || draft.compression || !!draft.proxyProtocol || null },
            h('summary', null, icon('chevron-down'), t('tunnels.fields.options')),
            h('div', { class: 'form-stack', style: { paddingTop: '12px' } }, ppField, encryption, compression)),
          draft.extras.length ? h('p', { class: 'field-hint' }, t('tunnels.extrasKept', { keys: draft.extras.map(([k]) => k).join(', ') })) : null)],
        foot: [
          button(t('common.cancel'), { onClick: () => close(false) }),
          button(existing ? t('tunnels.apply') : t('tunnels.addToList'), { variant: 'primary', type: 'submit', form: formId }),
        ],
      };
    },
  });
}

function draftDialogFocusError() {
  document.querySelector('dialog [aria-invalid="true"]')?.focus();
}

// ── Éditeur de visiteur ────────────────────────────────────────────────────

function editVisitor(existing) {
  const draft = existing ? { ...existing } : newVisitor();
  openDialog({
    title: existing ? t('visitors.editTitle', { name: existing.name }) : t('visitors.newTitle'),
    description: t('visitors.editorHint'),
    build: ({ close }) => {
      const name = input({ value: draft.name, placeholder: t('visitors.fields.namePlaceholder'), mono: true });
      const type = segmented(VISITOR_TYPES.map((v) => ({ value: v, label: v.toUpperCase() })), draft.type, () => {
        typeHint.textContent = t(`tunnels.types.${type.value}`);
      }, { label: t('tunnels.fields.type') });
      const typeHint = h('p', { class: 'field-hint' }, t(`tunnels.types.${draft.type}`));
      const serverName = input({ value: draft.serverName, placeholder: 'service-partage', mono: true });
      const secret = secretInput({ value: draft.secretKey, placeholder: t('tunnels.fields.secretPlaceholder') });
      const bindAddr = input({ value: draft.bindAddr, placeholder: '127.0.0.1', mono: true });
      const bindPort = input({ value: draft.bindPort, placeholder: '9001', inputmode: 'numeric', mono: true });

      function submit(e) {
        e.preventDefault();
        let ok = true;
        const check = (ctrl, msg) => { setFieldError(ctrl, msg); if (msg) ok = false; };
        const nm = name.value.trim();
        check(name, !nm ? t('validation.required')
          : S.visitors.some((v) => v.uid !== draft.uid && v.name === nm) ? t('validation.nameTaken') : '');
        check(serverName, !serverName.value.trim() ? t('validation.required') : '');
        check(bindPort, !validPort(bindPort.value.trim()) ? t('validation.port') : '');
        if (!ok) { draftDialogFocusError(); return; }
        Object.assign(draft, {
          name: nm, type: type.value, serverName: serverName.value.trim(), secretKey: secret.input.value,
          bindAddr: bindAddr.value.trim(), bindPort: bindPort.value.trim(),
        });
        if (existing) S.visitors = S.visitors.map((v) => (v.uid === draft.uid ? draft : v));
        else S.visitors = [...S.visitors, draft];
        close(true);
        render();
      }

      const formId = `visitor-form-${draft.uid}`;
      return {
        body: [h('form', { id: formId, class: 'form-stack', onSubmit: submit, novalidate: true },
          field({ label: t('tunnels.fields.name'), tomlKey: 'name', control: name }),
          h('div', { class: 'field' }, h('span', { class: 'field-label' }, t('tunnels.fields.type'), h('span', { class: 'field-key' }, 'type')), type, typeHint),
          field({ label: t('visitors.fields.serverName'), hint: t('visitors.fields.serverNameHint'), tomlKey: 'serverName', control: serverName }),
          field({ label: t('tunnels.fields.secret'), hint: t('visitors.fields.secretHint'), tomlKey: 'secretKey', control: secret }),
          h('div', { class: 'form-grid-2' },
            field({ label: t('visitors.fields.bindAddr'), tomlKey: 'bindAddr', control: bindAddr }),
            field({ label: t('visitors.fields.bindPort'), tomlKey: 'bindPort', control: bindPort })),
          h('p', { class: 'field-hint' }, t('visitors.fields.bindHint')))],
        foot: [
          button(t('common.cancel'), { onClick: () => close(false) }),
          button(existing ? t('tunnels.apply') : t('tunnels.addToList'), { variant: 'primary', type: 'submit', form: formId }),
        ],
      };
    },
  });
}

// ── Enregistrement ─────────────────────────────────────────────────────────

async function save(restart) {
  try {
    // Config frpc : on relit la partie connexion au dernier moment
    const current = await api(`/api/config/${encodeURIComponent(S.iid)}`).catch(() => null);
    const base = current && current.ok ? current.content || '' : S.baseText;
    const content = composeClientConfig(base, S.proxies, S.visitors);
    const d = await post(`/api/config/${encodeURIComponent(S.iid)}`, { content });
    if (!d.ok) { toast(d.msg || t('errors.generic'), 'error'); return; }
    S.baseText = content;
    S.snapshot = snapshotOf();
    toast(t('tunnels.saved'), 'success');

    // Redémarrage (la réponse peut ne jamais arriver si le panel passe par ce tunnel)
    if (restart) {
      toast(t('tunnels.restarting', { name: displayName(S.iid) }), 'info');
      post(`/api/service/${encodeURIComponent(S.iid)}/restart`).catch(() => {});
      const up = await waitRunning(S.iid);
      toast(up ? t('tunnels.restarted', { name: displayName(S.iid) }) : t('tunnels.restartUnknown'), up ? 'success' : 'info');
    }
  } catch (e) {
    toastError(e);
  }
  await load();
}
