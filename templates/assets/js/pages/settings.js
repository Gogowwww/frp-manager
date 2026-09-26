// ── Réglages du panel : sécurité, accès réseau, préférences ───────────────

import { t, LOCALES, PREVIEW_LOCALES, getLocale, setLocale } from '../i18n.js';
import { api, apiOk } from '../api.js';
import { store } from '../store.js';
import {
  h, button, busy, toast, toastResult, toastError, field, setFieldError, input, select, secretInput,
  segmented, callout, pageHeader, card,
} from '../ui.js';
import { getThemePref, setThemePref } from '../theme.js';

export default {
  id: 'settings',
  title: () => t('nav.settings'),

  async mount(view) {
    const page = h('div', { class: 'page' },
      pageHeader({ title: t('settings.title'), description: t('settings.description') }));
    view.append(page);

    let cfg = {};
    try {
      cfg = (await apiOk('/api/manager/config')).config || {};
    } catch (e) {
      page.append(callout({ type: 'danger', title: t('settings.loadError'), text: e.message }));
    }
    // Sécurité et accès réseau côte à côte, les préférences d'affichage en dessous
    page.append(
      h('div', { class: 'grid-2 card-pair' }, securityCard(cfg), networkCard(cfg)),
      preferencesCard());
  },
};

function securityCard(cfg) {
  const state = cfg.has_password
    ? callout({ type: 'success', iconName: 'shield', text: t('settings.security.protected') })
    : callout({ type: 'danger', title: t('settings.security.unprotectedTitle'), text: t('settings.security.unprotectedText') });

  const username = input({ value: cfg.username || 'admin', autocomplete: 'username' });
  const pass = secretInput({ autocomplete: 'new-password', placeholder: cfg.has_password ? t('settings.security.keepPassword') : '' });
  const confirm = secretInput({ autocomplete: 'new-password' });
  const saveBtn = button(t('settings.security.save'), { variant: 'primary', type: 'submit', form: 'security-form' });

  async function submit(e) {
    e.preventDefault();
    const p1 = pass.input.value;
    const p2 = confirm.input.value;
    setFieldError(confirm.input, p1 && p1 !== p2 ? t('settings.security.mismatch') : '');
    setFieldError(pass.input, p1 && p1.length < 8 ? t('settings.security.tooShort') : '');
    if ((p1 && p1 !== p2) || (p1 && p1.length < 8)) return;
    if (!username.value.trim()) { setFieldError(username, t('validation.required')); return; }
    setFieldError(username, '');
    await busy(saveBtn, async () => {
      try {
        const d = await api('/api/manager/config', { method: 'POST', body: { username: username.value.trim(), new_password: p1 || undefined } });
        if (d.ok) {
          toast(p1 ? t('settings.security.passwordSaved') : t('settings.security.saved'), 'success');
          pass.input.value = '';
          confirm.input.value = '';
          if (p1) {
            store.set({ hasPassword: true });
            state.replaceWith(callout({ type: 'success', iconName: 'shield', text: t('settings.security.protected') }));
          }
        } else toastResult(d);
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

function networkCard(cfg) {
  const host = input({ value: cfg.bind_host || '0.0.0.0', mono: true });
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
    title: t('settings.network.title'), description: t('settings.network.desc'),
    body: h('form', { id: 'network-form', class: 'form-stack', onSubmit: submit, novalidate: true },
      h('div', { class: 'form-grid' },
        field({ label: t('settings.network.host'), hint: t('settings.network.hostHint'), tomlKey: 'bind_host', control: host }),
        field({ label: t('settings.network.port'), tomlKey: 'bind_port', control: port }),
        field({ label: t('settings.network.timeout'), hint: t('settings.network.timeoutHint'), control: timeout })),
      callout({ type: 'neutral', text: t('settings.network.restartNote') })),
    foot: [saveBtn],
  });
}

function preferencesCard() {
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

  return card({
    title: t('settings.prefs.title'), description: t('settings.prefs.desc'),
    body: h('div', { class: 'form-grid-2' },
      h('div', { class: 'field' }, h('span', { class: 'field-label' }, t('settings.prefs.theme')), theme),
      field({ label: t('settings.prefs.language'), hint: codes.length < 2 ? t('settings.prefs.languageSoon')
        : PREVIEW_LOCALES.has(getLocale()) ? t('settings.prefs.languagePreview') : null, control: lang })),
  });
}
