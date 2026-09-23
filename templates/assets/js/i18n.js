// ── i18n ──────────────────────────────────────────────────────────────────
// Dictionnaires : assets/locales/<code>.js (export default { … } imbriqué).
// Ajouter une langue = créer le fichier + l'ajouter à LOCALES ci-dessous.
//
//   t('nav.dashboard')                       → texte simple
//   t('tunnels.count', { count: 3 })         → pluriel via { one, other }
//   t('dashboard.stopConfirm', { name })     → interpolation {name}
//   <span data-i18n="nav.logs"></span>       → traduit par applyI18n()
//   <input data-i18n-placeholder="…">        → attributs : placeholder, title, aria-label

export const LOCALES = {
  fr: 'Français',
  // en: 'English',
};
const DEFAULT_LOCALE = 'fr';
const STORAGE_KEY = 'frpm.locale';

let locale = DEFAULT_LOCALE;
let dict = {};
let fallback = {};
const warned = new Set();

function lookup(obj, key) {
  return key.split('.').reduce((o, k) => (o == null ? undefined : o[k]), obj);
}

function detectLocale() {
  try {
    const saved = localStorage.getItem(STORAGE_KEY);
    if (saved && LOCALES[saved]) return saved;
  } catch { /* stockage indisponible */ }
  for (const lang of navigator.languages || [navigator.language]) {
    const code = String(lang || '').slice(0, 2).toLowerCase();
    if (LOCALES[code]) return code;
  }
  return DEFAULT_LOCALE;
}

async function loadDict(code) {
  const mod = await import(`../locales/${code}.js`);
  return mod.default;
}

export async function initI18n() {
  locale = detectLocale();
  fallback = await loadDict(DEFAULT_LOCALE);
  dict = locale === DEFAULT_LOCALE ? fallback : await loadDict(locale).catch(() => fallback);
  document.documentElement.lang = locale;
}

export function getLocale() { return locale; }

export function setLocale(code) {
  if (!LOCALES[code]) return;
  try { localStorage.setItem(STORAGE_KEY, code); } catch { /* ignoré */ }
  location.reload();
}

export function hasKey(key) {
  return lookup(dict, key) !== undefined || lookup(fallback, key) !== undefined;
}

export function t(key, params = {}) {
  let value = lookup(dict, key);
  if (value === undefined) value = lookup(fallback, key);
  if (value === undefined) {
    if (!warned.has(key)) { warned.add(key); console.warn(`[i18n] clé manquante : ${key}`); }
    return key;
  }
  if (typeof value === 'object') {
    const rule = new Intl.PluralRules(locale).select(Number(params.count) || 0);
    value = value[rule] ?? value.other ?? '';
  }
  return String(value).replace(/\{(\w+)\}/g, (m, name) => (name in params ? String(params[name]) : m));
}

const ATTRS = ['placeholder', 'title', 'aria-label'];

export function applyI18n(root = document) {
  root.querySelectorAll('[data-i18n]').forEach((el) => { el.textContent = t(el.dataset.i18n); });
  for (const attr of ATTRS) {
    const data = `data-i18n-${attr}`;
    root.querySelectorAll(`[${data}]`).forEach((el) => el.setAttribute(attr, t(el.getAttribute(data))));
  }
}

export const format = {
  number: (n) => new Intl.NumberFormat(locale).format(n),
  time: (d) => new Intl.DateTimeFormat(locale, { timeStyle: 'medium' }).format(d),
};
