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
import { importCertDialog, acmeCertDialog } from '../certs.js';

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
    host.append(sessionsCard());
  },
};

const DISPLAY_NAME_MAX = 64;

/** Identifiant, nom affiché, rôle et instances du compte connecté. */
function profileCard() {
  const me = store.me || {};
  const role = t(`users.roles.${me.role || 'viewer'}`);
  const cell = (label, value) => h('div', { class: 'version-cell' }, h('dt', null, label), h('dd', { title: value }, value));
  // Compte OpenID Connect : le nom vient du fournisseur d'identité et revient à
  // chaque connexion, il n'est donc pas modifiable ici.
  // OpenID Connect account: the name comes from the identity provider and is
  // restored on every sign-in, so it cannot be edited here.
  const oidc = me.source === 'oidc';
  const name = input({ value: me.display_name || '', maxlength: DISPLAY_NAME_MAX, autocomplete: 'nickname' });
  const saveBtn = button(t('common.save'), { variant: 'primary', type: 'submit', form: 'profile-form' });

  async function submit(e) {
    e.preventDefault();
    await busy(saveBtn, async () => {
      try {
        const d = await api('/api/me', { method: 'PUT', body: { display_name: name.value.trim() } });
        if (!d.ok) { toastResult(d); return; }
        toast(d.msg, 'success');
        store.set({ me: { ...store.me, ...d.user } });
        name.value = d.user.display_name || '';
      } catch (err) { toastError(err); }
    });
  }

  return card({
    title: t('settings.account.profile'), description: t('settings.account.profileDesc'),
    body: [
      h('dl', { class: 'versions profile-cells' },
        cell(t('settings.account.username'), oidc && me.display_name ? `${me.username} · ${me.display_name}` : me.username || '—'),
        cell(t('settings.account.role'), me.owner ? `${role} · ${t('users.owner')}` : role),
        cell(t('settings.account.instances'), me.instances === '*' || !me.instances ? t('users.allInstances') : me.instances.join(', '))),
      !oidc && h('form', { id: 'profile-form', class: 'form-stack', style: { marginTop: 'var(--space-4)' }, onSubmit: submit, novalidate: true },
        field({ label: t('settings.account.displayName'), hint: t('settings.account.displayNameHint'), control: name })),
    ],
    foot: oidc ? null : [saveBtn],
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

/** Nom lisible d'un navigateur d'après son User-Agent : « Chrome · Windows ». */
function deviceName(ua) {
  const browser = [['Edg/', 'Edge'], ['OPR/', 'Opera'], ['Firefox/', 'Firefox'], ['Chrome/', 'Chrome'], ['Safari/', 'Safari']]
    .find(([k]) => ua.includes(k));
  const system = [['Windows', 'Windows'], ['Android', 'Android'], ['iPhone', 'iOS'], ['iPad', 'iPadOS'],
    ['Mac OS X', 'macOS'], ['Linux', 'Linux']].find(([k]) => ua.includes(k));
  if (!browser && !system) return t('settings.account.unknownDevice');
  return [browser?.[1], system?.[1]].filter(Boolean).join(' · ');
}

/** Sessions ouvertes du compte : appareils connectés, révocation de l'un d'eux. */
function sessionsCard() {
  const list = h('div', null, h('p', { class: 'muted' }, t('common.loading')));
  const closeBtn = button(t('settings.account.closeOthers'), { iconName: 'logout', onClick: closeOthers });
  const when = (ts) => (ts ? new Date(ts * 1000).toLocaleString(getLocale()) : t('users.never'));
  let items = [];

  async function load() {
    try {
      const d = await apiOk('/api/me/sessions');
      items = d.sessions || [];
      render();
    } catch (e) {
      list.replaceChildren(callout({ type: 'danger', title: t('settings.loadError'), text: e.message }));
    }
  }

  function render() {
    closeBtn.disabled = items.length < 2;
    if (!items.length) {
      list.replaceChildren(h('p', { class: 'muted' }, t('settings.account.sessionsEmpty')));
      return;
    }
    list.replaceChildren(h('ul', { class: 'rows' }, items.map((s) => {
      const device = deviceName(s.ua || '');
      return h('li', { class: 'row us-row' },
        h('div', { class: 'row-name' }, h('span', { title: s.ua || '' }, device),
          s.current && badge(t('settings.account.thisDevice'), 'success')),
        h('div', { class: 'fw-detail' },
          h('span', null, h('small', null, t('settings.account.address')), h('span', { title: s.ip || '' }, s.ip || '—')),
          h('span', null, h('small', null, t('settings.account.lastSeen')), h('span', null, when(s.last_seen)))),
        h('div', { class: 'row-actions' },
          !s.current && button('', { variant: 'ghost-danger', size: 'sm', iconName: 'logout',
            title: t('settings.account.revoke'), onClick: () => revoke(s, device) })));
    })));
  }

  async function revoke(s, device) {
    const ok = await confirmDialog({
      title: t('settings.account.revokeTitle'), message: t('settings.account.revokeText', { device }),
      confirmLabel: t('settings.account.revoke'), danger: true,
    });
    if (!ok) return;
    try {
      const d = await api(`/api/me/sessions/${encodeURIComponent(s.id)}`, { method: 'DELETE' });
      toastResult(d);
      if (d.ok) load();
    } catch (err) { toastError(err); }
  }

  async function closeOthers() {
    const ok = await confirmDialog({
      title: t('settings.account.closeOthersTitle'), message: t('settings.account.closeOthersText'),
      confirmLabel: t('settings.account.closeOthers'), danger: true,
    });
    if (!ok) return;
    try {
      const d = await api('/api/me/sessions', { method: 'DELETE' });
      toastResult(d);
      if (d.ok) load();
    } catch (err) { toastError(err); }
  }

  load();
  return card({
    title: t('settings.account.sessions'), description: t('settings.account.sessionsDesc'),
    body: list, foot: [closeBtn],
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
    host.append(listenCard(cfg), downloadsCard(cfg), statsCard(cfg));
  },
};

function listenCard(cfg) {
  // FRP_MANAGER_HOST (docker-compose.yml) fixe l'adresse réelle : le champ
  // l'affiche et reste désactivé, sinon il n'aurait aucun effet.
  // FRP_MANAGER_HOST (docker-compose.yml) sets the real address: the field
  // shows it and stays disabled, otherwise it would have no effect.
  const envHost = cfg.bind_host_env === true;
  const host = input({ value: cfg.bind_host || '0.0.0.0', mono: true });
  host.disabled = envHost;
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
        field({ label: t('settings.network.host'),
          hint: envHost ? t('settings.network.hostEnvHint', { host: cfg.bind_host }) : t('settings.network.hostHint'),
          tomlKey: 'bind_host', control: host }),
        field({ label: t('settings.network.port'), tomlKey: 'bind_port', control: port }),
        field({ label: t('settings.network.timeout'), hint: t('settings.network.timeoutHint'), control: timeout })),
      callout({ type: 'neutral', compact: true, text: t('settings.network.restartNote') })),
    foot: [saveBtn],
  });
}

// Certificats des sites : paires certificat/clé utilisées par les tunnels HTTPS
// de frpc (plugin https2http / https2https). frps ne termine pas le TLS des
// sites : le certificat vit donc sur la machine du tunnel. La carte est
// affichée dans la section Proxy inverse HTTPS, qui choisit l'un d'eux.
// Site certificates: certificate/key pairs used by frpc HTTPS tunnels
// (https2http / https2https plugin). frps does not terminate site TLS: the
// certificate therefore lives on the tunnel machine. The card is shown in the
// HTTPS reverse proxy section, which picks one of them.
function siteCertsCard({ onChange } = {}) {
  const list = h('div', { class: 'form-stack' }, h('p', { class: 'muted' }, t('common.loading')));
  const note = h('div', { class: 'form-stack' });
  const refreshBtn = button('', { variant: 'ghost', size: 'sm', iconName: 'refresh', title: t('settings.network.siteCertRefresh'), onClick: () => load() });
  const importBtn = button(t('settings.network.siteCertAdd'), { variant: 'primary', iconName: 'upload', onClick: importCert });
  const acmeBtn = button(t('settings.network.siteCertAcme'), { variant: 'secondary', iconName: 'shield', onClick: acme });
  let certs = [];
  let acmeState = {};

  const when = (iso) => (iso ? new Date(iso).toLocaleDateString(getLocale(), { year: 'numeric', month: 'short', day: 'numeric' }) : '—');

  async function load() {
    try {
      const d = await apiOk('/api/manager/certs');
      certs = d.certs || [];
      acmeState = d.acme || {};
      render();
      onChange?.(certs);
    } catch (e) {
      list.replaceChildren(callout({ type: 'danger', title: t('settings.loadError'), text: e.message }));
    }
  }

  function render() {
    const rows = certs.map((c) => {
      const soon = !c.expired && c.expires_in_days <= 30;
      return h('div', { class: 'form-stack', style: { padding: '10px 0', borderTop: '1px solid rgba(128,128,128,.25)' } },
        h('div', { class: 'row-actions' },
          h('strong', { class: 'mono' }, (c.domains || []).join(', ') || c.id),
          c.expired ? badge(t('settings.network.siteCertExpired'), 'danger')
            : soon ? badge(t('settings.network.siteCertExpires', { days: c.expires_in_days }), 'warn')
              : badge(t('settings.network.siteCertValid', { days: c.expires_in_days }), 'success'),
          c.managed ? badge(t('settings.network.siteCertManaged'), 'info') : null,
          button('', {
            variant: 'ghost-danger', size: 'sm', iconName: 'trash', title: t('common.delete'),
            onClick: () => remove(c),
          })),
        h('p', { class: 'field-hint' },
          `${t('settings.network.siteCertIssuer')} : ${c.issuer || '—'} — ${t('settings.network.siteCertValidity')} : ${when(c.not_before)} → ${when(c.not_after)}`),
        h('p', { class: 'field-hint mono' }, c.crt_path || ''));
    });
    list.replaceChildren(...(rows.length ? rows : [h('p', { class: 'muted' }, t('settings.network.siteCertEmpty'))]));
    note.replaceChildren(...(acmeState.configured ? [h('p', { class: 'field-hint' }, t('settings.network.acmeRenewNote'))] : []));
  }

  async function remove(c) {
    const ok = await confirmDialog({
      title: t('settings.network.siteCertDeleteTitle'),
      message: t('settings.network.siteCertDeleteText'),
      confirmLabel: t('common.delete'),
    });
    if (!ok) return;
    try {
      const d = await api(`/api/manager/certs/${encodeURIComponent(c.id)}`, { method: 'DELETE' });
      if (!d.ok) { toastResult(d); return; }
      toast(t('settings.network.siteCertDeleted'), 'success');
      load();
    } catch (err) { toastError(err); }
  }

  // Dialogues partagés avec l'éditeur de tunnel HTTPS (voir certs.js).
  // Dialogs shared with the HTTPS tunnel editor (see certs.js).
  function importCert() { importCertDialog({ onSaved: () => load() }); }
  function acme() { acmeCertDialog({ state: acmeState, onSaved: () => load() }); }

  load();
  return card({
    title: t('settings.network.siteCertTitle'),
    description: t('settings.network.siteCertDesc'),
    headActions: refreshBtn,
    body: h('div', { class: 'form-stack' }, list, note),
    foot: [importBtn, acmeBtn],
  });
}

// HSTS du panel, et adresses des proxys dont les en-têtes X-Forwarded-For /
// X-Forwarded-Proto sont crus : carte de la section Proxy inverse HTTPS.
// The panel's HSTS, and the proxies whose X-Forwarded-For / X-Forwarded-Proto
// headers are trusted: a card of the HTTPS reverse proxy section.
function httpsCard(cfg) {
  const proxies = input({ value: cfg.trusted_proxies || '', mono: true, placeholder: '10.0.0.0/8, 192.168.1.10' });
  const saveBtn = button(t('common.save'), { variant: 'primary', type: 'submit', form: 'https-form' });

  async function submit(e) {
    e.preventDefault();
    await busy(saveBtn, async () => {
      try {
        const d = await api('/api/manager/config', { method: 'POST', body: { trusted_proxies: proxies.value.trim() } });
        if (d.ok) toast(t('settings.network.saved'), 'success');
        else toastResult(d);
      } catch (err) { toastError(err); }
    });
  }

  return card({
    title: t('settings.network.httpsTitle'), description: t('settings.network.httpsDesc'),
    body: h('div', { class: 'form-stack' },
      switchRow({
        label: t('settings.network.hsts'),
        description: t('settings.network.hstsDesc'),
        checked: cfg.hsts === true,
        onChange: async (checked) => {
          try {
            const d = await api('/api/manager/config', { method: 'POST', body: { hsts: checked } });
            if (d.ok) toast(t('settings.network.saved'), 'success');
            else toastResult(d);
          } catch (err) { toastError(err); }
        },
      }),
      h('form', { id: 'https-form', class: 'form-stack', onSubmit: submit, novalidate: true },
        field({ label: t('settings.network.proxies'), hint: t('settings.network.proxiesHint'), tomlKey: 'trusted_proxies', control: proxies })),
      callout({ type: 'neutral', compact: true, text: t('settings.network.proxiesNote') })),
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

// Statistiques anonymes : désactivées par défaut, contenu exact affiché
// Anonymous statistics: off by default, exact content shown
function statsCard(cfg) {
  return card({
    title: t('settings.network.statsTitle'),
    body: h('div', { class: 'form-stack' },
      switchRow({
        label: t('settings.network.stats'),
        description: t('settings.network.statsDesc'),
        checked: cfg.anonymous_stats === true,
        onChange: async (checked) => {
          try {
            const d = await api('/api/manager/config', { method: 'POST', body: { anonymous_stats: checked } });
            if (d.ok) toast(t(checked ? 'settings.network.statsOn' : 'settings.network.statsOff'), 'success');
            else toastResult(d);
          } catch (err) { toastError(err); }
        },
      }),
      h('div', { class: 'field' },
        h('span', { class: 'field-label' }, t('settings.network.statsPayload')),
        h('div', { class: 'code-block' }, h('code', null, JSON.stringify(cfg.anonymous_stats_payload || {}))))),
  });
}

// ── Proxy inverse HTTPS ────────────────────────────────────────────────────

// Ports HTTPS servis en reverse proxy avec certificats : le certificat et le
// mode choisis ici sont repris par défaut par chaque tunnel HTTPS (frpc
// présente le certificat lui-même, frps ne fait que router par SNI).
// HTTPS ports served as a reverse proxy with certificates: the certificate and
// mode chosen here are reused by default by every HTTPS tunnel (frpc presents
// the certificate itself, frps only routes by SNI).
const httpsProxy = {
  id: 'proxy',
  icon: 'shield',
  title: () => t('settings.httpsProxy.title'),
  description: () => t('settings.httpsProxy.desc'),
  visible: () => can('settings.manage'),
  async mount(host) {
    let cfg;
    try {
      cfg = (await apiOk('/api/manager/config')).config || {};
    } catch (e) {
      host.append(callout({ type: 'danger', title: t('settings.loadError'), text: e.message }));
      return;
    }
    // Le certificat se choisit parmi ceux listés juste en dessous : chaque
    // rechargement de la liste rafraîchit le sélecteur sans perdre un choix en
    // cours. HSTS et proxys de confiance du panel se règlent ici aussi.
    // The certificate is picked among the ones listed just below: each reload
    // of the list refreshes the picker without losing an in-progress choice.
    // The panel's HSTS and trusted proxies are set here as well.
    const proxy = httpsProxyCard(cfg);
    host.append(proxy.el, siteCertsCard({ onChange: proxy.setCerts }), httpsCard(cfg));
  },
};

function httpsProxyCard(cfg) {
  // Chaque réglage s'enregistre seul : le bandeau des tunnels lit ensuite la
  // réponse de /api/manager/certs (« proxy »).
  // Each setting saves on its own: the tunnel banner then reads the "proxy"
  // field of /api/manager/certs.
  const save = async (body) => {
    try {
      const d = await api('/api/manager/config', { method: 'POST', body });
      if (!d.ok) { toastResult(d); return false; }
      Object.assign(cfg, body);
      toast(t('settings.httpsProxy.saved'), 'success');
      return true;
    } catch (err) { toastError(err); return false; }
  };

  const enabled = switchRow({
    label: t('settings.httpsProxy.enable'),
    description: t('settings.httpsProxy.enableDesc'),
    checked: cfg.https_proxy_cert === true,
    onChange: (checked) => save({ https_proxy_cert: checked }),
  });

  // Le sélecteur est rempli par setCerts(), avec les certificats des sites
  // affichés en dessous : un import ou une suppression le met à jour.
  // The picker is filled by setCerts(), with the site certificates shown
  // below: an import or a deletion updates it.
  const cert = select([], '');
  const certHint = h('p', { class: 'field-hint' });
  const certField = field({ label: t('settings.httpsProxy.cert'), control: cert });
  // L'indication reste au-dessus de l'emplacement du message d'erreur.
  // The hint stays above the error message slot.
  certField.insertBefore(certHint, certField.querySelector('.field-error'));

  function setCerts(items) {
    const list = items || [];
    const keep = cert.value;
    cert.replaceChildren(
      h('option', { value: '' }, t('settings.httpsProxy.noCert')),
      ...list.map((c) => h('option', { value: c.id }, (c.domains || []).join(', ') || c.id)));
    const wanted = keep || cfg.https_proxy_cert_id || '';
    cert.value = list.some((c) => c.id === wanted) ? wanted : '';
    certHint.textContent = list.length ? t('settings.httpsProxy.certHint') : t('settings.httpsProxy.certMissing');
  }

  const mode = segmented([
    { value: 'https2http', label: t('tunnels.fields.certModeHttp') },
    { value: 'https2https', label: t('tunnels.fields.certModeHttps') },
  ], cfg.https_proxy_cert_mode === 'https2https' ? 'https2https' : 'https2http', () => {},
  { label: t('settings.httpsProxy.mode') });
  const saveBtn = button(t('common.save'), { variant: 'primary', type: 'submit', form: 'https-proxy-form' });

  async function submit(e) {
    e.preventDefault();
    await busy(saveBtn, () => save({ https_proxy_cert_id: cert.value, https_proxy_cert_mode: mode.value }));
  }

  const el = card({
    title: t('settings.httpsProxy.cardTitle'), description: t('settings.httpsProxy.cardDesc'),
    body: h('div', { class: 'form-stack' },
      enabled,
      h('form', { id: 'https-proxy-form', class: 'form-stack', onSubmit: submit, novalidate: true },
        certField,
        field({ label: t('settings.httpsProxy.mode'), hint: t('settings.httpsProxy.modeHint'), control: mode })),
      callout({ type: 'neutral', compact: true, text: t('settings.httpsProxy.note') })),
    foot: [saveBtn],
  });
  return { el, setCerts };
}

// ── Page ───────────────────────────────────────────────────────────────────

const GROUPS = [
  ['personal', [account, appearance]],
  ['frp', [config, httpsProxy]],
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
