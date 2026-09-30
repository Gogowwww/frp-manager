// ── Notifications : webhooks vers Discord, Slack, Telegram, ntfy… ─────────
// L'adresse, le secret et les en-têtes ne reviennent jamais du serveur :
// laissés vides dans le formulaire, ils gardent leur valeur enregistrée.

import { t, getLocale } from '../i18n.js';
import { api, apiOk } from '../api.js';
import { instanceIds, displayName } from '../store.js';
import {
  h, button, busy, toast, toastResult, toastError, openDialog, confirmDialog, field, input, select,
  secretInput, segmented, callout, emptyState, pageHeader, card, badge, switchControl,
} from '../ui.js';

const FORMATS = ['discord', 'slack', 'telegram', 'ntfy', 'gotify', 'generic', 'text'];
const PLACEHOLDERS = {
  discord: 'https://discord.com/api/webhooks/…',
  slack: 'https://hooks.slack.com/services/…',
  telegram: 'https://api.telegram.org/bot<TOKEN>',
  ntfy: 'https://ntfy.sh/mon-sujet',
  gotify: 'https://gotify.example.org/message?token=…',
  generic: 'https://example.org/hooks/frp',
  text: 'https://example.org/hooks/frp',
};
const LEVEL_BADGE = { info: '', success: 'success', warning: 'warn', error: 'danger' };
// Événements regroupés dans le formulaire
const GROUPS = [
  ['instances', ['instance.down', 'instance.up']],
  ['security', ['login.locked', 'login.success']],
  ['changes', ['config.saved', 'firewall.saved']],
  ['updates', ['update.panel', 'update.frp', 'update.frp_installed', 'panel.started']],
];
// Modèles de départ : un clic remplit nom, événements et options
const PRESETS = [
  { id: 'outages', events: ['instance.down', 'instance.up'], cooldown: 60 },
  { id: 'security', events: ['login.locked', 'login.success', 'firewall.saved'], cooldown: 0 },
  { id: 'updates', events: ['update.panel', 'update.frp', 'update.frp_installed'], cooldown: 0 },
  { id: 'all', events: ['*'], cooldown: 0 },
];

const S = { hooks: [], events: [], history: [], els: {} };

export default {
  id: 'notifications',
  title: () => t('nav.notifications'),

  async mount(view) {
    const page = h('div', { class: 'page' },
      pageHeader({
        title: t('notifications.title'), description: t('notifications.description'),
        actions: [button(t('notifications.add'), { variant: 'primary', iconName: 'plus', onClick: () => edit(null) })],
      }));
    S.els.list = h('div', { class: 'card' });
    S.els.history = h('div');
    page.append(S.els.list, S.els.history);
    view.append(page);
    try {
      apply(await apiOk('/api/webhooks'));
    } catch (e) {
      S.els.list.replaceChildren(callout({ type: 'danger', title: t('notifications.loadError'), text: e.message }));
    }
  },
};

function apply(d) {
  if (d.webhooks) S.hooks = d.webhooks;
  if (d.events) S.events = d.events;
  if (d.history) S.history = d.history;
  render();
}

async function refreshHistory() {
  try { S.history = (await apiOk('/api/webhooks')).history; renderHistory(); } catch { /* affichage seul */ }
}

const eventLabel = (id) => (id === '*' ? t('notifications.allEvents') : t(`notifications.events.${id}`));
const time = (iso) => new Date(iso).toLocaleString(getLocale());

async function persist(hooks, okMessage) {
  try {
    const d = await api('/api/webhooks', { method: 'POST', body: { webhooks: hooks } });
    if (!d.ok) { toastResult(d); return false; }
    S.hooks = d.webhooks;
    render();
    if (okMessage) toast(okMessage, 'success');
    return true;
  } catch (err) { toastError(err); return false; }
}

function render() {
  const host = S.els.list;
  if (!S.hooks.length) {
    host.replaceChildren(emptyState({
      iconName: 'bell', title: t('notifications.emptyTitle'), text: t('notifications.emptyText'),
      actions: [button(t('notifications.add'), { variant: 'primary', iconName: 'plus', onClick: () => edit(null) })],
    }));
  } else {
    host.replaceChildren(h('ul', { class: 'rows' }, S.hooks.map((hook) => {
      const sw = switchControl({
        checked: hook.enabled, label: t('notifications.enabled'),
        onChange: async (on) => {
          const ok = await persist(S.hooks.map((x) => (x.id === hook.id ? { ...x, enabled: on } : x)));
          if (!ok) sw.input.checked = !on;
        },
      });
      sw.addEventListener('click', (e) => e.stopPropagation());
      const open = () => edit(hook);
      const testBtn = button('', {
        variant: 'ghost', size: 'sm', iconName: 'send', title: t('notifications.test'),
        onClick: (e) => { e.stopPropagation(); busy(testBtn, () => sendTest(hook)); },
      });
      return h('li', {
        class: `row nt-row${hook.enabled ? '' : ' is-off'}`, tabindex: '0', onClick: open,
        onKeydown: (e) => { if (e.key === 'Enter') open(); },
      },
      h('div', { class: 'row-name' }, h('span', null, hook.name), badge(t(`notifications.formats.${hook.format}`))),
      h('div', { class: 'fw-detail' },
        h('span', null, h('small', null, t('notifications.destination')), h('span', { class: 'mono' }, hook.url_hint)),
        h('span', null, h('small', null, t('notifications.eventsLabel')),
          h('span', { title: hook.events.map(eventLabel).join('\n') },
            hook.events.includes('*') ? t('notifications.allEvents') : t('notifications.eventCount', { count: hook.events.length })))),
      h('div', { class: 'row-actions' },
        sw, testBtn,
        button('', { variant: 'ghost', size: 'sm', iconName: 'edit', title: t('common.edit'), onClick: (e) => { e.stopPropagation(); open(); } }),
        button('', { variant: 'ghost-danger', size: 'sm', iconName: 'trash', title: t('common.delete'), onClick: (e) => { e.stopPropagation(); remove(hook); } })));
    })));
  }
  renderHistory();
}

function renderHistory() {
  if (!S.hooks.length && !S.history.length) { S.els.history.replaceChildren(); return; }
  const body = S.history.length
    ? h('div', { class: 'fw-table-wrap' }, h('table', { class: 'fw-table' },
      h('thead', null, h('tr', null, ['time', 'webhook', 'event', 'result'].map((k) => h('th', null, t(`notifications.history.${k}`))))),
      h('tbody', null, S.history.slice(0, 20).map((e) => h('tr', null,
        h('td', null, time(e.time)),
        h('td', null, e.name),
        h('td', null, e.event === 'test' ? t('notifications.history.test') : eventLabel(e.event)),
        h('td', null, badge(e.ok ? t('notifications.history.sent') : t('notifications.history.failed', { detail: e.msg }), e.ok ? 'success' : 'danger')))))))
    : h('p', { class: 'fw-pad muted' }, t('notifications.history.empty'));
  S.els.history.replaceChildren(card({
    title: t('notifications.history.title'), description: t('notifications.history.desc'),
    headActions: button('', { variant: 'ghost', size: 'sm', iconName: 'refresh', title: t('common.refresh'), onClick: refreshHistory }),
    body, cls: 'nt-history',
  }));
}

async function sendTest(hook) {
  try {
    const d = await api('/api/webhooks/test', { method: 'POST', body: { webhook: hook } });
    toast(d.msg, d.ok ? 'success' : 'danger');
  } catch (err) { toastError(err); }
  refreshHistory();
}

async function remove(hook) {
  const ok = await confirmDialog({
    title: t('notifications.deleteTitle', { name: hook.name }), message: t('notifications.deleteText'),
    confirmLabel: t('common.delete'), danger: true,
  });
  if (ok) await persist(S.hooks.filter((x) => x.id !== hook.id), t('notifications.deleted'));
}

async function edit(existing) {
  const isNew = !existing;
  const draft = existing ? { ...existing } : { id: '', name: '', enabled: true, format: 'discord', events: ['instance.down', 'instance.up'], instances: [], lang: getLocale() === 'en' ? 'en' : 'fr', cooldown: 60, template: '', chat_id: '' };
  const formId = 'nt-form';

  await openDialog({
    title: isNew ? t('notifications.newTitle') : t('notifications.editTitle', { name: existing.name }),
    description: t('notifications.editorHint'),
    size: 'lg',
    build: ({ close }) => {
      const name = input({ value: draft.name, maxLength: 64, placeholder: t('notifications.namePlaceholder') });
      const url = input({ mono: true, autocomplete: 'off', spellcheck: 'false' });
      const chat = input({ value: draft.chat_id, mono: true, placeholder: '-1001234567890' });
      const secret = secretInput({ placeholder: existing?.has_secret ? t('notifications.keepSecret') : '' });
      const headers = h('textarea', {
        class: 'input input-mono fw-textarea', rows: 3, spellcheck: 'false',
        placeholder: existing?.has_headers ? t('notifications.keepHeaders') : 'Authorization: Bearer …',
      });
      const template = h('textarea', {
        class: 'input input-mono fw-textarea', rows: 5, spellcheck: 'false',
        placeholder: '{"text": "{{title}} — {{message}}"}',
      });
      template.value = draft.template || '';
      const cooldown = input({ value: draft.cooldown, inputmode: 'numeric', mono: true });
      const lang = segmented([{ value: 'fr', label: 'Français' }, { value: 'en', label: 'English' }], draft.lang, () => {}, { label: t('notifications.lang') });
      const clearSecret = h('input', { type: 'checkbox' });
      const clearHeaders = h('input', { type: 'checkbox' });

      // Cases à cocher des événements
      const checks = new Map();
      const eventBox = (id) => {
        const cb = h('input', { type: 'checkbox', checked: draft.events.includes('*') || draft.events.includes(id) });
        checks.set(id, cb);
        return h('label', { class: 'nt-check' }, cb, h('span', null, eventLabel(id),
          badge(t(`notifications.levels.${S.events.find((e) => e.id === id)?.level || 'info'}`), LEVEL_BADGE[S.events.find((e) => e.id === id)?.level || 'info'])));
      };
      const groups = GROUPS.map(([g, ids]) => h('div', { class: 'nt-group' },
        h('div', { class: 'nt-group-title' }, t(`notifications.groups.${g}`)), ids.map(eventBox)));
      const setEvents = (ids) => checks.forEach((cb, id) => { cb.checked = ids.includes('*') || ids.includes(id); });
      const presets = h('div', { class: 'fw-chips' }, PRESETS.map((p) => h('button', {
        type: 'button', class: 'fw-chip nt-preset',
        onClick: () => { setEvents(p.events); cooldown.value = p.cooldown; if (!name.value.trim()) name.value = t(`notifications.presets.${p.id}`); },
      }, t(`notifications.presets.${p.id}`))));

      // Instances : filtre facultatif pour les événements d'instance
      const ids = instanceIds();
      const instChecks = new Map();
      const instances = ids.length ? h('div', { class: 'nt-group' },
        h('div', { class: 'nt-group-title' }, t('notifications.onlyInstances')),
        ids.map((iid) => {
          const cb = h('input', { type: 'checkbox', checked: draft.instances.includes(iid) });
          instChecks.set(iid, cb);
          return h('label', { class: 'nt-check' }, cb, h('span', null, displayName(iid)));
        }),
        h('p', { class: 'field-hint' }, t('notifications.onlyInstancesHint'))) : null;

      // Format : les champs qui n'ont de sens que pour certains formats
      const format = select(FORMATS.map((f) => ({ value: f, label: t(`notifications.formats.${f}`) })), draft.format);
      const formatHint = h('p', { class: 'field-hint' });
      const urlField = field({ label: t('notifications.url'), control: url, hint: null });
      const chatField = field({ label: t('notifications.chatId'), control: chat, hint: t('notifications.chatIdHint') });
      const secretField = field({ label: t('notifications.secret'), optional: true, control: secret, hint: t('notifications.secretHint') });
      const templateField = field({ label: t('notifications.template'), optional: true, control: template, hint: t('notifications.templateHint') });
      const syncFormat = () => {
        const f = format.value;
        url.placeholder = existing ? t('notifications.keepUrl', { url: existing.url_hint }) : PLACEHOLDERS[f];
        formatHint.textContent = t(`notifications.formatHint.${f}`);
        chatField.hidden = f !== 'telegram';
        secretField.hidden = !['generic', 'text'].includes(f);
        templateField.hidden = f !== 'generic';
      };
      format.addEventListener('change', syncFormat);
      syncFormat();

      const submit = (e) => {
        e.preventDefault();
        const picked = [...checks].filter(([, cb]) => cb.checked).map(([id]) => id);
        if (!name.value.trim()) { toast(t('notifications.nameMissing'), 'danger'); name.focus(); return; }
        if (!picked.length) { toast(t('notifications.eventsMissing'), 'danger'); return; }
        close({
          ...draft, name: name.value.trim(), format: format.value, url: url.value.trim(), chat_id: chat.value.trim(),
          secret: secret.input.value, secret_clear: clearSecret.checked, headers: headers.value, headers_clear: clearHeaders.checked,
          template: template.value, lang: lang.value, cooldown: Number(cooldown.value) || 0,
          events: picked.length === checks.size ? ['*'] : picked,
          instances: [...instChecks].filter(([, cb]) => cb.checked).map(([iid]) => iid),
        });
      };
      const testBtn = button(t('notifications.test'), {
        iconName: 'send',
        onClick: () => busy(testBtn, async () => {
          try {
            const d = await api('/api/webhooks/test', { method: 'POST', body: { webhook: {
              id: draft.id, name: name.value.trim() || '—', format: format.value, url: url.value.trim(),
              chat_id: chat.value.trim(), secret: secret.input.value, headers: headers.value,
              template: template.value, lang: lang.value } } });
            toast(d.msg, d.ok ? 'success' : 'danger');
          } catch (err) { toastError(err); }
          refreshHistory();
        }),
      });
      return {
        body: [h('form', { id: formId, class: 'form-stack', onSubmit: submit, novalidate: true },
          h('div', { class: 'form-grid' },
            field({ label: t('notifications.name'), control: name }),
            h('div', { class: 'field' }, h('label', { class: 'field-label', for: 'nt-format' }, t('notifications.format')),
              Object.assign(format, { id: 'nt-format' }), formatHint)),
          urlField, chatField,
          h('div', { class: 'field' }, h('span', { class: 'field-label' }, t('notifications.eventsLabel')),
            !isNew ? null : h('p', { class: 'field-hint' }, t('notifications.presetsHint')), isNew ? presets : null,
            h('div', { class: 'nt-groups' }, groups), instances),
          h('div', { class: 'form-grid' },
            h('div', { class: 'field' }, h('span', { class: 'field-label' }, t('notifications.lang')), lang),
            field({ label: t('notifications.cooldown'), control: cooldown, hint: t('notifications.cooldownHint') })),
          secretField,
          existing?.has_secret ? h('label', { class: 'nt-check' }, clearSecret, h('span', null, t('notifications.clearSecret'))) : null,
          field({ label: t('notifications.headers'), optional: true, control: headers, hint: t('notifications.headersHint') }),
          existing?.has_headers ? h('label', { class: 'nt-check' }, clearHeaders, h('span', null, t('notifications.clearHeaders'))) : null,
          templateField)],
        foot: [testBtn, h('span', { class: 'spacer' }),
          button(t('common.cancel'), { onClick: () => close(undefined) }),
          button(t('common.save'), { variant: 'primary', type: 'submit', form: formId })],
      };
    },
  }).result.then(async (saved) => {
    if (!saved) return;
    const list = isNew ? [...S.hooks, saved] : S.hooks.map((x) => (x.id === saved.id ? saved : x));
    await persist(list, t('notifications.saved'));
  });
}
