// ── Primitives d'interface ────────────────────────────────────────────────
// Tout le DOM dynamique passe par h() : jamais d'innerHTML avec des données,
// donc pas d'échappement à gérer à la main.

import { t } from './i18n.js';

const SVG_NS = 'http://www.w3.org/2000/svg';

/** h('div', { class: 'x', onClick: fn, dataset: {…} }, enfants…) */
export function h(tag, attrs, ...children) {
  const el = document.createElement(tag);
  if (attrs) {
    for (const [key, value] of Object.entries(attrs)) {
      if (value == null || value === false) continue;
      if (key === 'class') el.className = value;
      else if (key === 'dataset') Object.assign(el.dataset, value);
      else if (key === 'style' && typeof value === 'object') Object.assign(el.style, value);
      else if (key.startsWith('on') && typeof value === 'function') el.addEventListener(key.slice(2).toLowerCase(), value);
      else if (key in el && typeof value !== 'string') el[key] = value;
      else el.setAttribute(key, value === true ? '' : value);
    }
  }
  append(el, children);
  return el;
}

function append(parent, children) {
  for (const child of children) {
    if (child == null || child === false) continue;
    if (Array.isArray(child)) append(parent, child);
    else parent.append(child instanceof Node ? child : document.createTextNode(String(child)));
  }
}

export function icon(name, cls = '') {
  const svg = document.createElementNS(SVG_NS, 'svg');
  svg.setAttribute('class', `icon ${cls}`.trim());
  svg.setAttribute('aria-hidden', 'true');
  const use = document.createElementNS(SVG_NS, 'use');
  use.setAttribute('href', `#i-${name}`);
  svg.append(use);
  return svg;
}

// ── Boutons ────────────────────────────────────────────────────────────────
export function button(label, { variant = 'secondary', size, iconName, onClick, type = 'button', title, ...rest } = {}) {
  const cls = ['btn', `btn-${variant}`, size && `btn-${size}`, !label && 'btn-icon'].filter(Boolean).join(' ');
  return h('button', { type, class: cls, onClick, title, 'aria-label': !label ? title : null, ...rest },
    iconName && icon(iconName), label);
}

/** Désactive le bouton + spinner le temps de la promesse. */
export async function busy(btn, fn) {
  if (!btn) return fn();
  btn.setAttribute('aria-busy', 'true');
  btn.disabled = true;
  try { return await fn(); } finally {
    btn.removeAttribute('aria-busy');
    btn.disabled = false;
  }
}

// ── Toasts ─────────────────────────────────────────────────────────────────
const TOAST_ICONS = { success: 'check-circle', error: 'alert', info: 'info' };

export function toast(message, type = 'info', duration = 4000) {
  const host = document.getElementById('toasts');
  if (!host || !message) return;
  const el = h('div', { class: `toast toast-${type}`, role: type === 'error' ? 'alert' : 'status' },
    icon(TOAST_ICONS[type] || 'info'), h('div', null, message));
  host.append(el);
  while (host.children.length > 4) host.firstChild.remove();
  setTimeout(() => {
    el.classList.add('is-leaving');
    setTimeout(() => el.remove(), 220);
  }, type === 'error' ? duration + 2000 : duration);
}

/** Toast à partir d'une réponse backend { ok, msg }. */
export function toastResult(d, fallbackOk) {
  if (d && d.ok) toast(d.msg && d.msg !== 'OK' ? d.msg : fallbackOk, 'success');
  else toast((d && d.msg) || t('errors.generic'), 'error');
}

export function toastError(err) {
  toast(err && err.message ? err.message : t('errors.generic'), 'error');
}

// ── Dialogues ──────────────────────────────────────────────────────────────
/**
 * Ouvre un <dialog> modal. Renvoie { el, close(result), result: Promise }.
 * build({ close }) → { body: Node[], foot: Node[] }
 */
export function openDialog({ title, description, size = '', build, onClose }) {
  let resolve;
  const result = new Promise((r) => { resolve = r; });
  const dialog = h('dialog', { class: `dialog ${size ? `dialog-${size}` : ''}`, 'aria-labelledby': 'dlg-title' });
  let returned;
  const close = (value) => { returned = value; dialog.close(); };
  const { body = [], foot = [] } = build({ close, dialog });

  dialog.append(
    h('div', { class: 'dialog-head' },
      h('div', { style: { flex: '1', minWidth: '0' } },
        h('h2', { class: 'dialog-title', id: 'dlg-title' }, title),
        description && h('p', { class: 'dialog-desc' }, description)),
      button('', { variant: 'ghost', size: 'sm', iconName: 'x', title: t('common.close'), onClick: () => close(undefined) })),
    h('div', { class: 'dialog-body' }, body),
    foot.length ? h('div', { class: 'dialog-foot' }, foot) : null,
  );

  dialog.addEventListener('close', () => {
    dialog.remove();
    if (onClose) onClose(returned);
    resolve(returned);
  });
  document.body.append(dialog);
  dialog.showModal();
  const first = dialog.querySelector('[autofocus], .dialog-body input, .dialog-body select, .dialog-foot .btn-primary');
  if (first) first.focus();
  return { el: dialog, close, result };
}

export function confirmDialog({ title, message, confirmLabel = t('common.confirm'), danger = false }) {
  return openDialog({
    title, size: 'sm',
    build: ({ close }) => ({
      body: [h('p', { class: 'muted', style: { color: 'var(--text-2)' } }, message)],
      foot: [
        button(t('common.cancel'), { onClick: () => close(false) }),
        button(confirmLabel, { variant: danger ? 'danger' : 'primary', onClick: () => close(true), autofocus: true }),
      ],
    }),
  }).result.then(Boolean);
}

export function promptDialog({ title, label, hint, value = '', placeholder = '', maxLength, confirmLabel = t('common.save') }) {
  return openDialog({
    title, size: 'sm',
    build: ({ close }) => {
      const input = h('input', { class: 'input', value, placeholder, maxLength, autofocus: true });
      const submit = (e) => { e.preventDefault(); close(input.value.trim()); };
      return {
        body: [h('form', { onSubmit: submit, id: 'prompt-form' }, field({ label, hint, control: input }))],
        foot: [
          button(t('common.cancel'), { onClick: () => close(undefined) }),
          button(confirmLabel, { variant: 'primary', type: 'submit', form: 'prompt-form' }),
        ],
      };
    },
  }).result;
}

// ── Menu déroulant ─────────────────────────────────────────────────────────
let openMenu = null;
function closeOpenMenu() {
  if (!openMenu) return;
  openMenu.menu.remove();
  openMenu.trigger.setAttribute('aria-expanded', 'false');
  openMenu = null;
}
document.addEventListener('click', (e) => {
  if (openMenu && !openMenu.wrap.contains(e.target)) closeOpenMenu();
});
document.addEventListener('keydown', (e) => {
  if (e.key === 'Escape' && openMenu) { const tr = openMenu.trigger; closeOpenMenu(); tr.focus(); }
});

/** items: [{ label, icon, onSelect, danger }] ou 'sep' */
export function menuButton(items, { title = t('common.moreActions') } = {}) {
  const trigger = button('', { variant: 'ghost', iconName: 'more', title, 'aria-haspopup': 'menu', 'aria-expanded': 'false' });
  const wrap = h('div', { class: 'menu-wrap' }, trigger);
  trigger.addEventListener('click', (e) => {
    e.stopPropagation();
    if (openMenu && openMenu.trigger === trigger) { closeOpenMenu(); return; }
    closeOpenMenu();
    const menu = h('div', { class: 'menu', role: 'menu' },
      items.filter(Boolean).map((it) => (it === 'sep'
        ? h('div', { class: 'menu-sep', role: 'separator' })
        : h('button', {
          type: 'button', role: 'menuitem', class: `menu-item${it.danger ? ' is-danger' : ''}`,
          onClick: () => { closeOpenMenu(); it.onSelect(); },
        }, it.icon && icon(it.icon), it.label))));
    menu.addEventListener('keydown', (ev) => {
      const list = [...menu.querySelectorAll('.menu-item')];
      const i = list.indexOf(document.activeElement);
      if (ev.key === 'ArrowDown') { ev.preventDefault(); list[(i + 1) % list.length].focus(); }
      if (ev.key === 'ArrowUp') { ev.preventDefault(); list[(i - 1 + list.length) % list.length].focus(); }
    });
    wrap.append(menu);
    trigger.setAttribute('aria-expanded', 'true');
    openMenu = { wrap, menu, trigger };
    menu.querySelector('.menu-item')?.focus();
  });
  return wrap;
}

// ── Formulaires ────────────────────────────────────────────────────────────
let fieldSeq = 0;

/** Champ libellé : label + contrôle + aide + clé TOML optionnelle. */
export function field({ label, hint, tomlKey, control, optional, error }) {
  const id = control.id || `f-${++fieldSeq}`;
  control.id = id;
  const hintId = hint ? `${id}-hint` : null;
  if (hintId) control.setAttribute('aria-describedby', hintId);
  const errorEl = h('p', { class: 'field-error', hidden: !error }, error || '');
  return h('div', { class: 'field' },
    h('label', { class: 'field-label', for: id },
      label,
      optional && h('span', { class: 'field-optional' }, t('common.optional')),
      tomlKey && h('span', { class: 'field-key', title: t('common.tomlKey') }, tomlKey)),
    control,
    hint && h('p', { class: 'field-hint', id: hintId }, hint),
    errorEl);
}

export function setFieldError(control, message) {
  const err = control.closest('.field')?.querySelector('.field-error');
  control.setAttribute('aria-invalid', message ? 'true' : 'false');
  if (err) { err.textContent = message || ''; err.hidden = !message; }
}

export function input(attrs = {}) {
  return h('input', { class: `input${attrs.mono ? ' input-mono' : ''}`, type: 'text', ...attrs, mono: null });
}

export function select(options, value, attrs = {}) {
  const el = h('select', { class: 'select', ...attrs },
    options.map((o) => {
      const opt = typeof o === 'string' ? { value: o, label: o } : o;
      return h('option', { value: opt.value }, opt.label);
    }));
  if (value != null) el.value = String(value);
  return el;
}

/** Champ mot de passe avec bouton afficher/masquer. */
export function secretInput(attrs = {}) {
  const inp = input({ type: 'password', autocomplete: 'off', mono: true, ...attrs });
  const toggle = button('', {
    iconName: 'eye', title: t('common.show'),
    onClick: () => {
      const show = inp.type === 'password';
      inp.type = show ? 'text' : 'password';
      toggle.querySelector('use').setAttribute('href', show ? '#i-eye-off' : '#i-eye');
      toggle.title = show ? t('common.hide') : t('common.show');
      toggle.setAttribute('aria-label', toggle.title);
    },
  });
  const group = h('div', { class: 'input-group' }, inp, toggle);
  group.input = inp;
  return group;
}

/** Interrupteur accessible (role=switch). */
export function switchControl({ checked = false, disabled = false, label, onChange, id } = {}) {
  const inp = h('input', { type: 'checkbox', role: 'switch', checked, disabled, id, 'aria-label': label });
  if (onChange) inp.addEventListener('change', () => onChange(inp.checked));
  const wrap = h('label', { class: 'switch' }, inp, h('span', { class: 'switch-track' }));
  wrap.input = inp;
  return wrap;
}

export function switchRow({ label, description, checked, disabled, onChange }) {
  const sw = switchControl({ checked, disabled, label, onChange });
  const row = h('div', { class: 'switch-row' },
    h('div', { class: 'switch-text' },
      h('div', { class: 'switch-label' }, label),
      description && h('div', { class: 'switch-desc' }, description)),
    sw);
  row.input = sw.input;
  return row;
}

/** Contrôle segmenté. options: [{ value, label, icon }] */
export function segmented(options, value, onChange, { label } = {}) {
  const wrap = h('div', { class: 'segmented', role: 'radiogroup', 'aria-label': label });
  const buttons = options.map((o) => h('button', {
    type: 'button', role: 'radio', 'aria-checked': String(o.value === value),
    onClick: () => {
      if (wrap.value === o.value) return;
      wrap.value = o.value;
      buttons.forEach((b) => b.setAttribute('aria-checked', String(b.dataset.value === o.value)));
      onChange(o.value);
    },
    dataset: { value: o.value },
  }, o.icon && icon(o.icon), o.label));
  wrap.value = value;
  wrap.append(...buttons);
  return wrap;
}

// ── Blocs ──────────────────────────────────────────────────────────────────
const CALLOUT_ICONS = { info: 'info', warn: 'alert', danger: 'alert', success: 'check-circle', neutral: 'info' };

/** compact : une ligne discrète (ex. rappels « mode Docker »), titre et texte à la suite. */
export function callout({ type = 'info', title, text, actions, iconName, compact = false }) {
  if (compact) {
    return h('div', { class: `callout callout-compact callout-${type}` },
      icon(iconName || CALLOUT_ICONS[type]),
      h('div', { class: 'callout-body' },
        title && h('strong', null, title), title && text ? ' ' : null, text),
      actions && actions.length ? h('div', { class: 'callout-inline-actions' }, actions) : null);
  }
  return h('div', { class: `callout callout-${type}`, role: type === 'danger' ? 'alert' : null },
    icon(iconName || CALLOUT_ICONS[type]),
    h('div', { class: 'callout-body' },
      title && h('div', { class: 'callout-title' }, title),
      text && h('div', { class: 'callout-text' }, text),
      actions && actions.length ? h('div', { class: 'callout-actions' }, actions) : null));
}

export function emptyState({ iconName = 'info', title, text, actions }) {
  return h('div', { class: 'empty' },
    h('div', { class: 'empty-icon' }, icon(iconName, 'icon-lg')),
    h('div', { class: 'empty-title' }, title),
    text && h('p', { class: 'empty-text' }, text),
    actions && actions.length ? h('div', { class: 'empty-actions' }, actions) : null);
}

export function pageHeader({ title, description, actions }) {
  return h('header', { class: 'page-head' },
    h('div', null,
      h('h1', { class: 'page-title', tabindex: '-1' }, title),
      description && h('p', { class: 'page-desc' }, description)),
    actions && actions.length ? h('div', { class: 'page-actions' }, actions) : null);
}

export function card({ title, description, headActions, body, foot, cls = '' }) {
  return h('section', { class: `card ${cls}`.trim() },
    title && h('div', { class: 'card-head' },
      h('div', null,
        h('h2', { class: 'card-title' }, title),
        description && h('p', { class: 'card-desc' }, description)),
      headActions),
    body && h('div', { class: 'card-body' }, body),
    foot && h('div', { class: 'card-foot' }, foot));
}

export function badge(text, variant = '', attrs = {}) {
  return h('span', { class: `badge ${variant ? `badge-${variant}` : ''}`.trim(), ...attrs }, text);
}

/** Barre « modifications non enregistrées », fixée en bas de l'écran. */
export function saveBar({ onDiscard, onSave, onSaveRestart, restartLabel }) {
  const saveBtn = button(t('common.save'), { variant: 'secondary', onClick: () => busy(saveBtn, onSave) });
  const restartBtn = onSaveRestart
    ? button(restartLabel || t('common.saveAndRestart'), { variant: 'primary', onClick: () => busy(restartBtn, onSaveRestart) })
    : null;
  if (!onSaveRestart) saveBtn.className = 'btn btn-primary';
  const bar = h('div', { class: 'savebar', role: 'region', 'aria-label': t('common.unsaved'), hidden: true },
    h('div', { class: 'savebar-text' }, icon('alert'), t('common.unsaved')),
    button(t('common.discard'), { variant: 'ghost', onClick: onDiscard }),
    saveBtn, restartBtn);
  return bar;
}

export function copyButton(text) {
  const btn = button('', {
    variant: 'ghost', size: 'sm', iconName: 'copy', title: t('common.copy'),
    onClick: async () => {
      try { await navigator.clipboard.writeText(text); toast(t('common.copied'), 'success', 1800); }
      catch { toast(t('errors.clipboard'), 'error'); }
    },
  });
  return btn;
}
