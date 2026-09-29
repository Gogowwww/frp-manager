// ── Thème clair / sombre ──────────────────────────────────────────────────
// 'auto' suit le système ; un petit script inline dans le <head> applique la
// préférence avant le premier rendu pour éviter le flash.

const KEY = 'frpm.theme';

export function getThemePref() {
  try { return localStorage.getItem(KEY) || 'auto'; } catch { return 'auto'; }
}

export function setThemePref(pref) {
  try { localStorage.setItem(KEY, pref); } catch { /* préférence non mémorisée */ }
  applyTheme(pref);
}

export function applyTheme(pref = getThemePref()) {
  const root = document.documentElement;
  if (pref === 'light' || pref === 'dark') root.dataset.theme = pref;
  else delete root.dataset.theme;
}
