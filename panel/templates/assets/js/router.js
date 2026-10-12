// ── Routeur par hash : #/ports?iid=frpc ───────────────────────────────────
// Une page = { id, title(), mount(view, params), unmount?(), canLeave?(), perm? }.
// perm : permission requise ; sans elle, la page n'est pas ouverte (première page
// permise à la place).
// canLeave() peut renvoyer une promesse (ex. confirmation « modifications
// non enregistrées ») ; si elle vaut false, la navigation est annulée.

const routes = new Map();
// Anciennes adresses encore valides (favoris) → [page actuelle, paramètres]
const ALIASES = {
  tunnels: ['ports'],
  notifications: ['settings', { s: 'notifications' }],
  updates: ['settings', { s: 'updates' }],
  users: ['settings', { s: 'users' }],
};
let current = null;
let currentHash = '';
let view = null;
let fallback = 'dashboard';
let onChange = () => {};
let allowed = () => true;

export function registerPage(page) { routes.set(page.id, page); }

export function parseHash(hash = location.hash) {
  const [path, query = ''] = hash.replace(/^#\/?/, '').split('?');
  return { name: path || fallback, params: Object.fromEntries(new URLSearchParams(query)) };
}

export function hrefFor(name, params = {}) {
  const q = new URLSearchParams(params).toString();
  return `#/${name}${q ? `?${q}` : ''}`;
}

export function navigate(name, params = {}) {
  const href = hrefFor(name, params);
  if (location.hash === href) render();
  else location.hash = href;
}

/** Met à jour les paramètres de la page courante sans la recharger. */
export function replaceParams(params) {
  const { name } = parseHash();
  currentHash = hrefFor(name, params);
  history.replaceState(null, '', currentHash);
}

export function currentPage() { return current; }

async function render() {
  let { name, params } = parseHash();
  if (ALIASES[name]) {
    const [to, extra] = ALIASES[name];
    name = to;
    params = { ...params, ...extra };
    history.replaceState(null, '', hrefFor(name, params));
  }
  if (current && current.canLeave && location.hash !== currentHash) {
    const ok = await current.canLeave();
    if (!ok) { history.replaceState(null, '', currentHash); return; }
  }
  let page = routes.get(name) || routes.get(fallback);
  if (!allowed(page)) {
    page = [routes.get(fallback), ...routes.values()].find(allowed) || routes.get(fallback);
    history.replaceState(null, '', hrefFor(page.id));
  }
  if (current && current.unmount) current.unmount();
  current = page;
  currentHash = location.hash || hrefFor(page.id);
  view.replaceChildren();
  window.scrollTo(0, 0);
  onChange(page);
  await page.mount(view, params);
  view.querySelector('.page-title')?.focus({ preventScroll: true });
}

export function startRouter({ root, defaultRoute, onRouteChange, canOpen }) {
  view = root;
  allowed = canOpen || allowed;
  fallback = defaultRoute;
  onChange = onRouteChange || onChange;
  window.addEventListener('hashchange', render);
  return render();
}

/** Re-monte la page courante (ex. après un changement de données global). */
export function remount() {
  if (current && current.unmount) current.unmount();
  view.replaceChildren();
  return current && current.mount(view, parseHash().params);
}
