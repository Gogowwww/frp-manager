// ── Routeur par hash : #/tunnels?iid=frpc ─────────────────────────────────
// Une page = { id, title(), mount(view, params), unmount?(), canLeave?() }.
// canLeave() peut renvoyer une promesse (ex. confirmation « modifications
// non enregistrées ») ; si elle vaut false, la navigation est annulée.

const routes = new Map();
let current = null;
let currentHash = '';
let view = null;
let fallback = 'dashboard';
let onChange = () => {};

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
  const { name, params } = parseHash();
  if (current && current.canLeave && location.hash !== currentHash) {
    const ok = await current.canLeave();
    if (!ok) { history.replaceState(null, '', currentHash); return; }
  }
  const page = routes.get(name) || routes.get(fallback);
  if (current && current.unmount) current.unmount();
  current = page;
  currentHash = location.hash || hrefFor(page.id);
  view.replaceChildren();
  window.scrollTo(0, 0);
  onChange(page);
  await page.mount(view, params);
  view.querySelector('.page-title')?.focus({ preventScroll: true });
}

export function startRouter({ root, defaultRoute, onRouteChange }) {
  view = root;
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
