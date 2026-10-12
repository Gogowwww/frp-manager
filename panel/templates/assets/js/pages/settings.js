// ── Réglages : tous les réglages du panel, regroupés par section ──────────
// Menu des sections à gauche (rangée défilante sur mobile), section ouverte à
// droite : #/settings?s=users. Chaque section ne s'affiche qu'aux comptes qui
// ont la permission correspondante (le serveur vérifie de toute façon).
// Une section = { id, icon, title(), description(), visible?(), actions?(),
// badge?(), mount(host, { params, actions }), unmount?(), canLeave?() } ;
// actions est le conteneur des boutons de l'en-tête, params ceux de l'adresse.

import { t, LOCALES, PREVIEW_LOCALES, getLocale, setLocale } from '../i18n.js';
import { api, apiOk } from '../api.js';
import { store, can } from '../store.js';
import { hrefFor, replaceParams } from '../router.js';
import {
  h, icon, button, busy, toast, toastResult, toastError, field, setFieldError, input, select, secretInput,
  segmented, callout, pageHeader, card, switchRow, badge, openDialog, confirmDialog, promptDialog,
} from '../ui.js';
import { passkeysUnavailable, createPasskey } from '../passkey.js';
import { getThemePref, setThemePref } from '../theme.js';
import notifications from './notifications.js';
import updates from './updates.js';
import { users, sso } from './users.js';
import config from './config.js';

const PASSWORD_MIN = 12;

// ── Mon compte ─────────────────────────────────────────────────────────────

const account = {
  id: 'account',
  icon: 'user',
  title: () => t('settings.account.title'),
  description: () => t(store.me?.source === 'oidc' ? 'settings.account.oidcDesc' : 'settings.account.desc'),
  mount(host) {
    host.append(profileCard(), store.me?.owner ? ownerCard() : passwordCard());
    if (store.me?.source !== 'oidc') host.append(passkeysCard());
  },
};

/** Identifiant, rôle et instances du compte connecté. */
function profileCard() {
  const me = store.me || {};
  const role = t(`users.roles.${me.role || 'viewer'}`);
  const cell = (label, value) => h('div', { class: 'version-cell' }, h('dt', null, label), h('dd', { title: value }, value));
  return card({
    title: t('settings.account.profile'),
    body: h('dl', { class: 'versions profile-cells' },
      cell(t('settings.account.username'), me.display_name ? `${me.username} · ${me.display_name}` : me.username || '—'),
      cell(t('settings.account.role'), me.owner ? `${role} · ${t('users.owner')}` : role),
      cell(t('settings.account.instances'), me.instances === '*' || !me.instances ? t('users.allInstances') : me.instances.join(', '))),
  });
}

/** Propriétaire : identifiant et mot de passe du panel (pas d'ancien mot de passe
 * demandé : c'est le compte de la configuration initiale). */
function ownerCard() {
  const status = () => (store.hasPassword
    ? callout({ type: 'success', compact: true, iconName: 'shield', text: t('settings.security.protected') })
    : callout({ type: 'danger', title: t('settings.security.unprotectedTitle'), text: t('settings.security.unprotectedText') }));
  let state = status();
  const username = input({ value: store.me?.username || 'admin', autocomplete: 'username' });
  const pass = secretInput({ autocomplete: 'new-password', placeholder: store.hasPassword ? t('settings.security.keepPassword') : '' });
  const confirm = secretInput({ autocomplete: 'new-password' });
  const saveBtn = button(t('settings.security.save'), { variant: 'primary', type: 'submit', form: 'security-form' });

  async function submit(e) {
    e.preventDefault();
    const p1 = pass.input.value;
    const p2 = confirm.input.value;
    setFieldError(confirm.input, p1 && p1 !== p2 ? t('settings.security.mismatch') : '');
    setFieldError(pass.input, p1 && p1.length < PASSWORD_MIN ? t('settings.security.tooShort') : '');
    if ((p1 && p1 !== p2) || (p1 && p1.length < PASSWORD_MIN)) return;
    if (!username.value.trim()) { setFieldError(username, t('validation.required')); return; }
    setFieldError(username, '');
    await busy(saveBtn, async () => {
      try {
        const d = await api('/api/manager/config', { method: 'POST', body: { username: username.value.trim(), new_password: p1 || undefined } });
        if (!d.ok) { toastResult(d); return; }
        toast(p1 ? t('settings.security.passwordSaved') : t('settings.security.saved'), 'success');
        pass.input.value = '';
        confirm.input.value = '';
        store.set({ me: { ...store.me, username: username.value.trim() }, ...(p1 ? { hasPassword: true } : {}) });
        const next = status();
        state.replaceWith(next);
        state = next;
      } catch (err) { toastError(err); }
    });
  }

  return card({
    title: t('settings.security.title'), description: t('settings.security.desc'),
    body: h('form', { id: 'security-form', class: 'form-stack', onSubmit: submit, novalidate: true },
      state,
      h('div', { class: 'form-grid' },
        field({ label: t('settings.security.username'), control: username }),
        field({ label: t('settings.security.newPassword'), hint: t('settings.security.passwordHint'), control: pass }),
        field({ label: t('settings.security.confirm'), control: confirm }))),
    foot: [saveBtn],
  });
}

/** Autre compte local : son propre mot de passe (l'actuel est redemandé). */
function passwordCard() {
  if (store.me?.source === 'oidc') {
    return callout({ type: 'neutral', iconName: 'key', text: t('settings.account.oidcNote') });
  }
  const current = secretInput({ autocomplete: 'current-password' });
  const pass = secretInput({ autocomplete: 'new-password' });
  const confirm = secretInput({ autocomplete: 'new-password' });
  const saveBtn = button(t('settings.account.change'), { variant: 'primary', type: 'submit', form: 'account-form' });

  async function submit(e) {
    e.preventDefault();
    const p1 = pass.input.value;
    setFieldError(pass.input, p1.length < PASSWORD_MIN ? t('settings.security.tooShort') : '');
    setFieldError(confirm.input, p1 !== confirm.input.value ? t('settings.security.mismatch') : '');
    if (p1.length < PASSWORD_MIN || p1 !== confirm.input.value) return;
    await busy(saveBtn, async () => {
      try {
        const d = await api('/api/me/password', { method: 'POST', body: { current_password: current.input.value, new_password: p1 } });
        toastResult(d);
        if (d.ok) [current, pass, confirm].forEach((x) => { x.input.value = ''; });
      } catch (err) { toastError(err); }
    });
  }

  return card({
    title: t('settings.account.password'),
    body: h('form', { id: 'account-form', class: 'form-stack', onSubmit: submit, novalidate: true },
      h('div', { class: 'form-grid' },
        field({ label: t('settings.account.current'), control: current }),
        field({ label: t('settings.security.newPassword'), hint: t('settings.security.passwordHint'), control: pass }),
        field({ label: t('settings.security.confirm'), control: confirm }))),
    foot: [saveBtn],
  });
}

/** Passkeys du compte : connexion sans mot de passe, déverrouillée par
 * l'appareil (empreinte, visage, code PIN). Ajout : mot de passe redemandé. */
function passkeysCard() {
  const reason = passkeysUnavailable();
  const list = h('div', null, h('p', { class: 'muted' }, t('common.loading')));
  const addBtn = button(t('settings.passkeys.add'), { variant: 'primary', iconName: 'plus', onClick: add });
  addBtn.disabled = true;
  const when = (iso) => (iso ? new Date(iso).toLocaleString(getLocale()) : t('users.never'));
  let items = [];
  let max = 20;

  async function load() {
    try {
      const d = await apiOk('/api/me/passkeys');
      items = d.passkeys || [];
      max = d.max || max;
      render();
    } catch (e) {
      list.replaceChildren(callout({ type: 'danger', title: t('settings.loadError'), text: e.message }));
    }
  }

  function render() {
    addBtn.disabled = Boolean(reason) || items.length >= max;
    if (!items.length) {
      list.replaceChildren(h('p', { class: 'muted' }, t('settings.passkeys.empty')));
      return;
    }
    list.replaceChildren(h('ul', { class: 'rows' }, items.map((pk) => h('li', { class: 'row us-row' },
      h('div', { class: 'row-name' }, h('span', { title: pk.rp_id }, pk.name),
        pk.synced && badge(t('settings.passkeys.synced'), 'info')),
      h('div', { class: 'fw-detail' },
        h('span', null, h('small', null, t('settings.passkeys.created')), h('span', null, when(pk.created))),
        h('span', null, h('small', null, t('settings.passkeys.lastUsed')), h('span', null, when(pk.last_used)))),
      h('div', { class: 'row-actions' },
        button('', { variant: 'ghost', size: 'sm', iconName: 'edit', title: t('settings.passkeys.rename'), onClick: () => rename(pk) }),
        button('', { variant: 'ghost-danger', size: 'sm', iconName: 'trash', title: t('common.delete'), onClick: () => remove(pk) }))))));
  }

  async function add() {
    const added = await openDialog({
      title: t('settings.passkeys.addTitle'),
      description: t('settings.passkeys.addDesc'),
      size: 'sm',
      build: ({ close }) => {
        const pw = secretInput({ autocomplete: 'current-password' });
        const name = input({ maxLength: 64, placeholder: t('settings.passkeys.namePlaceholder') });
        const go = button(t('settings.passkeys.continue'), { variant: 'primary', type: 'submit', form: 'passkey-form' });
        async function submit(e) {
          e.preventDefault();
          setFieldError(pw.input, pw.input.value ? '' : t('validation.required'));
          if (!pw.input.value) return;
          await busy(go, async () => {
            try {
              const o = await api('/api/me/passkeys/options', { method: 'POST', body: { current_password: pw.input.value } });
              if (!o.ok) { setFieldError(pw.input, o.msg || t('errors.generic')); return; }
              let cred;
              try {
                cred = await createPasskey(o.publicKey);
              } catch (err) {
                const key = { InvalidStateError: 'already', NotAllowedError: 'cancelled', SecurityError: 'refused' }[err?.name] || 'failed';
                toast(t(`settings.passkeys.${key}`), 'error');
                return;
              }
              const d = await api('/api/me/passkeys', { method: 'POST', body: { name: name.value.trim(), credential: cred } });
              if (!d.ok) { toastResult(d); return; }
              toast(d.msg, 'success');
              close(true);
            } catch (err) { toastError(err); }
          });
        }
        return {
          body: [h('form', { id: 'passkey-form', class: 'form-stack', onSubmit: submit, novalidate: true },
            field({ label: t('settings.account.current'), control: pw }),
            field({ label: t('settings.passkeys.name'), hint: t('settings.passkeys.nameHint'), optional: true, control: name }))],
          foot: [button(t('common.cancel'), { onClick: () => close(false) }), go],
        };
      },
    }).result;
    if (added) load();
  }

  async function rename(pk) {
    const name = await promptDialog({ title: t('settings.passkeys.renameTitle'), label: t('settings.passkeys.name'), value: pk.name, maxLength: 64 });
    if (!name || name === pk.name) return;
    try {
      const d = await api(`/api/me/passkeys/${encodeURIComponent(pk.id)}`, { method: 'PUT', body: { name } });
      toastResult(d);
      if (d.ok) load();
    } catch (err) { toastError(err); }
  }

  async function remove(pk) {
    const ok = await confirmDialog({
      title: t('settings.passkeys.deleteTitle', { name: pk.name }), message: t('settings.passkeys.deleteText'),
      confirmLabel: t('common.delete'), danger: true,
    });
    if (!ok) return;
    try {
      const d = await api(`/api/me/passkeys/${encodeURIComponent(pk.id)}`, { method: 'DELETE' });
      toastResult(d);
      if (d.ok) load();
    } catch (err) { toastError(err); }
  }

  load();
  return card({
    title: t('settings.passkeys.title'), description: t('settings.passkeys.desc'),
    body: [reason && callout({ type: 'neutral', compact: true, iconName: 'info', text: t(`settings.passkeys.unavailable.${reason}`) }), list],
    foot: [addBtn],
  });
}

// ── Apparence ──────────────────────────────────────────────────────────────

const appearance = {
  id: 'appearance',
  icon: 'palette',
  title: () => t('settings.prefs.title'),
  description: () => t('settings.prefs.desc'),
  mount(host) {
    const theme = segmented([
      { value: 'auto', label: t('settings.prefs.themeAuto'), icon: 'monitor' },
      { value: 'light', label: t('settings.prefs.themeLight'), icon: 'sun' },
      { value: 'dark', label: t('settings.prefs.themeDark'), icon: 'moon' },
    ], getThemePref(), setThemePref, { label: t('settings.prefs.theme') });

    const codes = Object.keys(LOCALES);
    const langLabel = (c) => (PREVIEW_LOCALES.has(c) ? `${LOCALES[c]} (${t('settings.prefs.preview')})` : LOCALES[c]);
    const lang = select(codes.map((c) => ({ value: c, label: langLabel(c) })), getLocale(), { class: 'select select-inline' });
    lang.disabled = codes.length < 2;
    lang.addEventListener('change', () => setLocale(lang.value));

    host.append(card({
      body: h('div', { class: 'form-grid-2' },
        h('div', { class: 'field' }, h('span', { class: 'field-label' }, t('settings.prefs.theme')), theme),
        field({ label: t('settings.prefs.language'), hint: codes.length < 2 ? t('settings.prefs.languageSoon')
          : PREVIEW_LOCALES.has(getLocale()) ? t('settings.prefs.languagePreview') : null, control: lang })),
    }));
  },
};

// ── Accès au panel ─────────────────────────────────────────────────────────

const access = {
  id: 'access',
  icon: 'server',
  title: () => t('settings.network.title'),
  description: () => t('settings.network.desc'),
  visible: () => can('settings.manage'),
  async mount(host) {
    let cfg;
    try {
      cfg = (await apiOk('/api/manager/config')).config || {};
    } catch (e) {
      host.append(callout({ type: 'danger', title: t('settings.loadError'), text: e.message }));
      return;
    }
    host.append(listenCard(cfg), downloadsCard(cfg));
  },
};

function listenCard(cfg) {
  const host = input({ value: cfg.bind_host || '127.0.0.1', mono: true });
  const port = input({ value: cfg.bind_port || 8765, inputmode: 'numeric', mono: true });
  const timeout = input({ value: Math.round((cfg.session_timeout || 3600) / 60), inputmode: 'numeric', mono: true });
  const saveBtn = button(t('common.save'), { variant: 'primary', type: 'submit', form: 'network-form' });

  async function submit(e) {
    e.preventDefault();
    const p = Number(port.value);
    const minutes = Number(timeout.value);
    setFieldError(port, Number.isInteger(p) && p >= 1 && p <= 65535 ? '' : t('validation.port'));
    setFieldError(timeout, Number.isFinite(minutes) && minutes >= 5 ? '' : t('settings.network.timeoutMin'));
    if (!(Number.isInteger(p) && p >= 1 && p <= 65535) || !(minutes >= 5)) return;
    await busy(saveBtn, async () => {
      try {
        const d = await api('/api/manager/config', {
          method: 'POST',
          body: { bind_host: host.value.trim(), bind_port: p, session_timeout: Math.round(minutes * 60) },
        });
        if (d.ok) toast(t('settings.network.saved'), 'success');
        else toastResult(d);
      } catch (err) { toastError(err); }
    });
  }

  return card({
    title: t('settings.network.listenTitle'), description: t('settings.network.listenDesc'),
    body: h('form', { id: 'network-form', class: 'form-stack', onSubmit: submit, novalidate: true },
      h('div', { class: 'form-grid' },
        field({ label: t('settings.network.host'), hint: t('settings.network.hostHint'), tomlKey: 'bind_host', control: host }),
        field({ label: t('settings.network.port'), tomlKey: 'bind_port', control: port }),
        field({ label: t('settings.network.timeout'), hint: t('settings.network.timeoutHint'), control: timeout })),
      callout({ type: 'neutral', compact: true, text: t('settings.network.restartNote') })),
    foot: [saveBtn],
  });
}

function downloadsCard(cfg) {
  return card({
    title: t('settings.network.downloadsTitle'),
    body: switchRow({
      label: t('settings.network.mirrors'),
      description: t('settings.network.mirrorsDesc'),
      checked: cfg.download_mirrors !== false,
      onChange: async (checked) => {
        try {
          const d = await api('/api/manager/config', { method: 'POST', body: { download_mirrors: checked } });
          if (d.ok) toast(t('settings.network.mirrorsSaved'), 'success');
          else toastResult(d);
        } catch (err) { toastError(err); }
      },
    }),
  });
}

// ── Page ───────────────────────────────────────────────────────────────────

const GROUPS = [
  ['personal', [account, appearance]],
  ['frp', [config]],
  ['access', [access, users, sso]],
  ['system', [notifications, updates]],
];

let active = null;
let unsubscribe = null;

export default {
  id: 'settings',
  title: () => t('nav.settings'),

  async mount(view, params = {}) {
    const groups = GROUPS
      .map(([id, list]) => [id, list.filter((s) => !s.visible || s.visible())])
      .filter(([, list]) => list.length);
    const sections = groups.flatMap(([, list]) => list);
    const section = sections.find((s) => s.id === params.s) || sections[0];
    // Section absente ou interdite à ce compte : l'adresse suit la section ouverte
    if (params.s && params.s !== section.id) replaceParams({ s: section.id });

    // Menu des sections ; pastille sur Mises à jour quand une version sort
    const dots = new Map();
    const nav = h('nav', { class: 'settings-nav', 'aria-label': t('settings.sections') },
      groups.map(([id, list]) => h('div', { class: 'settings-nav-group' },
        h('div', { class: 'settings-nav-title' }, t(`settings.groups.${id}`)),
        list.map((s) => {
          const dot = s.badge ? h('span', { class: 'nav-badge', title: t('nav.updateAvailable'), hidden: !s.badge() }) : null;
          if (dot) dots.set(s, dot);
          return h('a', {
            class: 'settings-nav-link', href: hrefFor('settings', { s: s.id }),
            'aria-current': s === section ? 'page' : null,
          }, icon(s.icon), h('span', null, s.title()), dot);
        }))));
    unsubscribe = store.subscribe(() => dots.forEach((dot, s) => { dot.hidden = !s.badge(); }));

    const actions = h('div', { class: 'page-actions' }, section.actions ? section.actions() : []);
    const body = h('div', { class: 'settings-body' });
    const pane = h('section', { class: 'settings-pane', 'aria-labelledby': 'settings-pane-title' },
      h('header', { class: 'settings-pane-head' },
        h('div', null,
          h('h2', { class: 'settings-pane-title', id: 'settings-pane-title' }, section.title()),
          h('p', { class: 'settings-pane-desc' }, section.description())),
        actions),
      body);

    view.append(h('div', { class: 'page' },
      pageHeader({ title: t('settings.title'), description: t('settings.description') }),
      h('div', { class: 'settings-layout' }, nav, pane)));
    // Sur mobile, la section ouverte reste visible dans la rangée défilante
    nav.querySelector('[aria-current]')?.scrollIntoView({ block: 'nearest', inline: 'center' });

    active = section;
    await section.mount(body, { params, actions });
  },

  // Modifications non enregistrées dans la section ouverte (Configuration)
  canLeave() { return active?.canLeave ? active.canLeave() : true; },

  unmount() {
    active?.unmount?.();
    active = null;
    unsubscribe?.();
    unsubscribe = null;
  },
};
