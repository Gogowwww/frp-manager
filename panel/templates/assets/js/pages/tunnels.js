// ── Tunnels : [[proxies]] et [[visitors]] d'une instance frpc ─────────────
// Les modifications se font dans un brouillon local ; la barre du bas
// enregistre tout d'un coup (écriture TOML, puis redémarrage si demandé).

import { t } from '../i18n.js';
import { api, apiOk, post } from '../api.js';
import { can, frpcIds, displayName, waitRunning } from '../store.js';
import {
  h, icon, button, toast, toastError, openDialog, confirmDialog,
  field, setFieldError, input, select, secretInput, switchRow, segmented, emptyState,
  pageHeader, badge, saveBar, callout,
} from '../ui.js';
import {
  parseToml, composeClientConfig, proxyFromBlock, visitorFromBlock, newProxy, newVisitor,
  PROXY_TYPES, VISITOR_TYPES, hasRemotePort, hasDomains, hasSecret,
  portSpecCount, isPortRange, isRanged, isVisitorRanged, frpcNames, visitorNames, MAX_RANGE_PORTS, PORT_TOKEN,
  TCP_UDP, pairProxies, splitPair, joinHostPort, fixHostPort, unbracket, unbracketConnection, stripBlocks,
  tunnelNameOk,
} from '../toml.js';
import { navigate, replaceParams } from '../router.js';
import { importCertDialog, acmeCertDialog } from '../certs.js';

const S = {
  iid: '',
  filter: '', // recherche par identifiant d'utilisateur / search by user identifier
  baseText: '',
  serverAddr: '',
  // Identifiant de l'instance (clé frpc « user ») : frp en préfixe les noms de
  // tunnels côté serveur
  // Instance identifier (frpc "user" key): frp prefixes the tunnel names with it
  // on the server side
  instanceUser: '',
  baseFix: false,
  docker: false,
  proxies: [],
  visitors: [],
  snapshot: '',
  status: null, // état des proxys lu sur l'API de frpc (adresse attribuée par frps)
  certs: [], // certificats du panel (Réglages → Accès)
  proxy: null, // réglage « ports HTTPS en reverse proxy avec certificats »
  certsLoaded: false,
  els: {},
};

let statusTimer = null;

// URL de la configuration d'une instance, en lecture comme en écriture
// Configuration URL of an instance, for reading as well as writing
const configURL = (iid) => `/api/config/${encodeURIComponent(iid)}`;

const snapshotOf = () => JSON.stringify([S.proxies, S.visitors]);
// Lecture seule (pas de config.write) : jamais de modification à enregistrer
// baseFix : adresse de la connexion entre crochets, corrigée à l'enregistrement
// baseFix: bracketed connection address, fixed on save
const isDirty = () => can('config.write') && S.snapshot !== '' && (S.baseFix || snapshotOf() !== S.snapshot);

function beforeUnload(e) {
  if (isDirty()) { e.preventDefault(); e.returnValue = ''; }
}

export default {
  id: 'ports',
  title: () => t('nav.tunnels'),
  perm: 'config.read',

  async mount(view, params) {
    const ids = frpcIds();
    S.iid = ids.includes(params.iid) ? params.iid : ids[0] || '';
    S.snapshot = '';
    S.filter = '';
    window.addEventListener('beforeunload', beforeUnload);

    const actions = [];
    if (ids.length > 1) {
      const sel = select(ids.map((id) => ({ value: id, label: displayName(id) })), S.iid, {
        class: 'select select-inline', 'aria-label': t('common.instance'),
      });
      sel.addEventListener('change', async () => {
        if (isDirty() && !(await confirmLeave())) { sel.value = S.iid; return; }
        S.iid = sel.value;
        // Le filtre porte sur les identifiants de l'instance quittée
        // The filter applies to the identifiers of the instance being left
        S.filter = '';
        if (S.els.filter) S.els.filter.value = '';
        replaceParams({ iid: S.iid });
        load();
      });
      actions.push(sel);
    }
    // Export et import TOML de la liste : l'export écrit ce qui est affiché,
    // l'import y ajoute les tunnels du fichier (voir exportToml, importDialog)
    // TOML export and import of the list: the export writes what is displayed,
    // the import adds the file's tunnels to it (see exportToml, importDialog)
    if (S.iid) {
      actions.push(button(t('tunnels.export'), { iconName: 'download', onClick: exportToml }));
      if (can('config.write')) actions.push(button(t('tunnels.import'), { iconName: 'upload', onClick: importDialog }));
    }
    const addBtn = button(t('tunnels.add'), { variant: 'primary', iconName: 'plus', onClick: () => editProxy(null) });
    if (S.iid && can('config.write')) actions.push(addBtn);

    S.els.statusHint = h('div');
    S.els.instanceUser = h('div');
    // Filtre par identifiant : la barre reste en place pendant la frappe, seules
    // les lignes sont redessinées
    // Filter by identifier: the bar stays in place while typing, only the rows
    // are redrawn
    S.els.filter = input({
      type: 'search', placeholder: t('tunnels.filterPlaceholder'), 'aria-label': t('tunnels.filterPlaceholder'),
    });
    S.els.filter.addEventListener('input', () => { S.filter = S.els.filter.value.trim(); renderProxies(); });
    S.els.filterBar = h('div', { class: 'toolbar', hidden: true },
      h('div', { class: 'input-affix' }, icon('search'), S.els.filter));
    S.els.proxyList = h('div');
    S.els.proxies = h('div', { class: 'card' }, S.els.filterBar, S.els.proxyList);
    S.els.visitors = h('div', { class: 'card' });
    S.els.savebar = saveBar({
      onDiscard: discard,
      onSave: () => save(false),
      onSaveRestart: () => save(true),
      restartLabel: t('tunnels.saveRestart'),
    });

    view.append(h('div', { class: 'page' },
      pageHeader({ title: t('tunnels.title'), description: t('tunnels.description'), actions }),
      !can('config.write') && callout({ type: 'neutral', compact: true, iconName: 'eye', title: t('common.readOnly'), text: t('config.readOnly') }),
      S.els.instanceUser,
      S.els.statusHint,
      S.els.proxies,
      h('section', null,
        h('div', { class: 'section-head' },
          h('div', null,
            h('h2', { class: 'section-title' }, t('visitors.title')),
            h('p', { class: 'section-desc' }, t('visitors.description'))),
          S.iid && can('config.write') && button(t('visitors.add'), { iconName: 'plus', onClick: () => editVisitor(null) })),
        S.els.visitors),
      S.els.savebar));

    if (!S.iid) {
      S.els.proxyList.append(emptyState({
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
    clearTimeout(statusTimer);
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

// discarded : après « Annuler », la référence est la config déjà corrigée, pour
// que la barre ne revienne pas sans modification de l'utilisateur
// discarded: after "Discard", the reference is the already fixed config, so the
// bar does not come back without a change by the user
async function load(discarded = false) {
  // Page quittée pendant la requête : plus rien à redessiner
  // Page left during the request: nothing left to redraw
  if (!S.els.proxyList) return;
  // Seul le conteneur des lignes est remplacé : la barre de filtre reste en place
  // Only the rows container is replaced: the filter bar stays in place
  S.els.proxyList.replaceChildren(h('div', { class: 'skeleton', style: { height: '120px', margin: '16px' } }));
  S.baseFix = false;
  try {
    const d = await apiOk(`/api/config/${encodeURIComponent(S.iid)}`);
    S.baseText = d.content || '';
    S.docker = !!d.docker;
    const cfg = parseToml(S.baseText);
    S.serverAddr = cfg.values.serverAddr || '';
    S.instanceUser = cfg.values.user || '';
    const proxies = cfg.proxies.map(proxyFromBlock);
    const visitors = cfg.visitors.map(visitorFromBlock);
    // Paires X-tcp / X-udp écrites par « TCP + UDP » : une seule ligne
    // X-tcp / X-udp pairs written by "TCP + UDP": a single row
    S.proxies = pairProxies(proxies);
    S.visitors = visitors;
    if (!discarded) S.snapshot = snapshotOf();
    // Adresse entre crochets écrite par une ancienne version : corrigée ici, le
    // tunnel apparaît modifié et la barre d'enregistrement s'allume
    // Bracketed address written by an older version: fixed here, the tunnel
    // shows as changed and the save bar lights up
    // Regroupement après la correction : une paire à moitié entre crochets ne
    // fait qu'une entrée, comme après l'enregistrement
    // Grouping after the fix: a pair half between brackets makes a single
    // entry, as after saving
    S.proxies = pairProxies(proxies.map((p) => ({ ...p, localIP: p.localIP && unbracket(p.localIP) })));
    S.visitors = visitors.map((v) => ({ ...v, bindAddr: v.bindAddr && unbracket(v.bindAddr) }));
    // Même chose pour serverAddr, dnsServer et webServer.addr, que l'écriture
    // corrige aussi (composeClientConfig)
    // Same for serverAddr, dnsServer and webServer.addr, which the write also
    // fixes (composeClientConfig)
    S.baseFix = !discarded && unbracketConnection(stripBlocks(S.baseText)) !== stripBlocks(S.baseText);
    if (discarded) S.snapshot = snapshotOf();
  } catch (e) {
    toastError(e);
    S.proxies = [];
    S.visitors = [];
    S.snapshot = snapshotOf();
  }
  render();
  loadStatus();
}

// Port ouvert sans numéro : frps en choisit un à chaque démarrage de frpc
const serverPicked = (p) => hasRemotePort(p.type) && !p.remotePort;

/** État d'un tunnel sur l'API de frpc : avec un identifiant d'instance, frp
 *  préfixe le nom des tunnels (« identifiant.nom »), les deux formes sont vues.
 *  Tunnel status from frpc's API: with an instance identifier frp prefixes the
 *  tunnel names ("identifier.name"), both forms are looked up. */
function liveStatus(name) {
  const st = S.status?.proxies;
  if (!st || !name) return undefined;
  return st[name] ?? (S.instanceUser ? st[`${S.instanceUser}.${name}`] : undefined);
}

/** Adresses attribuées par frps, lues sur l'API de frpc. frpc vient peut-être de
 *  (re)démarrer : un proxy pas encore enregistré est redemandé une fois. */
async function loadStatus(retry = true) {
  clearTimeout(statusTimer);
  const iid = S.iid;
  try {
    const d = await apiOk(`/api/instance/${encodeURIComponent(iid)}/proxies`);
    if (iid !== S.iid || !S.els.proxies) return;
    S.status = d;
  } catch { S.status = null; }
  render();
  const pending = S.status?.reachable && S.proxies.some((p) => serverPicked(p) && !liveStatus(p.name)?.remote_addr);
  if (retry && pending) statusTimer = setTimeout(() => loadStatus(false), 2500);
}

function discard() {
  S.snapshot = '';
  load(true);
}

// ── Rendu ──────────────────────────────────────────────────────────────────

function render() {
  if (!S.els.proxies) return;
  renderInstanceUser();
  renderStatusHint();
  renderProxies();
  renderVisitors();
  S.els.savebar.hidden = !isDirty();
}

/** Identifiant de l'instance : frp en préfixe les noms de tunnels côté serveur,
 *  la page le dit pour que les noms vus dans frps ne surprennent pas.
 *  Instance identifier: frp prefixes the tunnel names with it on the server; the
 *  page says so that the names seen in frps are not a surprise. */
function renderInstanceUser() {
  const host = S.els.instanceUser;
  if (!host) return;
  host.replaceChildren(...(S.instanceUser ? [callout({
    type: 'neutral', compact: true, text: t('tunnels.instanceUser', { user: S.instanceUser }),
  })] : []));
}

function stateOf(item, list) {
  const original = JSON.parse(S.snapshot || '[[],[]]')[list === 'visitors' ? 1 : 0];
  const before = original.find((o) => o.uid === item.uid);
  if (!before) return 'new';
  return JSON.stringify(before) === JSON.stringify(item) ? '' : 'changed';
}

function renderProxies() {
  const host = S.els.proxyList;
  if (!host) return;
  // Le filtre ne s'affiche qu'une fois un identifiant saisi quelque part ; le
  // retirer du dernier tunnel vide aussi la recherche en cours
  // The filter only shows once an identifier has been set somewhere; removing it
  // from the last tunnel also clears the current search
  const anyUser = S.proxies.some((p) => p.user);
  if (!anyUser && S.filter) clearFilter();
  S.els.filterBar.hidden = !anyUser;
  if (!S.proxies.length) {
    host.replaceChildren(emptyState({
      iconName: 'tunnels', title: t('tunnels.emptyTitle'), text: t('tunnels.emptyText'),
      actions: [button(t('tunnels.add'), { variant: 'primary', iconName: 'plus', onClick: () => editProxy(null) })],
    }));
    return;
  }
  const q = S.filter.toLowerCase();
  const shown = q ? S.proxies.filter((p) => (p.user || '').toLowerCase().includes(q)) : S.proxies;
  if (!shown.length) {
    host.replaceChildren(emptyState({
      iconName: 'search', title: t('tunnels.filterEmptyTitle'), text: t('tunnels.filterEmptyText'),
      actions: [button(t('tunnels.filterClear'), { onClick: clearFilter })],
    }));
    return;
  }
  host.replaceChildren(h('ul', { class: 'rows', 'aria-label': t('tunnels.title') }, shown.map(proxyRow)));
}

/** Vide le filtre par identifiant et redessine la liste.
 *  Clears the identifier filter and redraws the list. */
function clearFilter() {
  S.filter = '';
  if (S.els.filter) S.els.filter.value = '';
}

/** Port attribué par le serveur : comment le connaître quand l'API de frpc manque. */
function renderStatusHint() {
  const host = S.els.statusHint;
  const st = S.status;
  // Activer l'API ou redémarrer frpc : config enregistrée, frpc sur cette machine
  const fixable = !S.docker && !isDirty() && can('config.write') && can('instances.control');
  let hint = null;
  if (st && S.proxies.some(serverPicked)) {
    if (!st.admin) {
      const actions = [];
      if (fixable) actions.push(button(t('tunnels.showAssigned'), { size: 'sm', iconName: 'eye', onClick: (e) => showAssigned(e.currentTarget) }));
      hint = callout({ type: 'info', compact: true, text: t('tunnels.randomPortNoAdmin'), actions });
    } else if (!st.reachable) {
      hint = callout({ type: 'warn', compact: true, text: t('tunnels.randomPortUnreachable'),
        actions: fixable ? [button(t('tunnels.restartFrpc'), { size: 'sm', onClick: (e) => { e.currentTarget.disabled = true; restartFrpc().then(() => load()); } })] : [] });
    }
  }
  host.replaceChildren(...(hint ? [hint] : []));
  host.hidden = !hint;
}

/** Active le tableau de bord local de frpc (127.0.0.1, mot de passe aléatoire),
 *  qui donne le port attribué par frps, puis redémarre frpc pour l'ouvrir. */
async function showAssigned(btn) {
  const ok = await openDialog({
    title: t('tunnels.admin.title'),
    build: ({ close }) => ({
      body: [
        h('p', null, t('tunnels.admin.intro')),
        h('ul', { class: 'admin-steps' },
          h('li', null, t('tunnels.admin.listen')),
          h('li', null, t('tunnels.admin.password')),
          h('li', null, t('tunnels.admin.restart'))),
        h('p', { class: 'field-hint' }, t('tunnels.admin.undo')),
      ],
      foot: [
        button(t('common.cancel'), { onClick: () => close(false) }),
        button(t('tunnels.admin.confirm'), { variant: 'primary', onClick: () => close(true), autofocus: true }),
      ],
    }),
  }).result;
  if (!ok) return;
  btn.disabled = true;
  try {
    const d = await post(`/api/instance/${encodeURIComponent(S.iid)}/proxies/admin`);
    if (!d.ok) throw new Error(d.msg || t('errors.generic'));
    await restartFrpc();
  } catch (e) {
    toastError(e);
  }
  await load();
}

async function restartFrpc() {
  toast(t('tunnels.restarting', { name: displayName(S.iid) }), 'info');
  post(`/api/service/${encodeURIComponent(S.iid)}/restart`).catch(() => {});
  const up = await waitRunning(S.iid);
  toast(up ? t('tunnels.restarted', { name: displayName(S.iid) }) : t('tunnels.restartUnknown'), up ? 'success' : 'info');
}

function publicEndpoint(p) {
  if (hasRemotePort(p.type)) {
    if (p.remotePort) return joinHostPort(S.serverAddr || t('tunnels.server'), p.remotePort);
    return fixHostPort(liveStatus(p.name)?.remote_addr) || t('tunnels.randomPort');
  }
  if (hasDomains(p.type)) return p.domains || t('tunnels.noDomain');
  return t('tunnels.privateOnly');
}

/** Adresse publique à copier : seulement une vraie adresse (« IP:port » ou un
 *  domaine unique), jamais un texte de remplacement, une plage ou une liste.
 *  Public address to copy: only a real address ("IP:port" or a single
 *  domain), never a placeholder text, a range or a list. */
function publicCopyValue(p) {
  // Plage, quel que soit le type : plusieurs adresses (ou un domaine à {port})
  // Range, whatever the type: several addresses (or a domain with {port})
  if (isRanged(p)) return '';
  if (hasRemotePort(p.type)) {
    if (p.remotePort) return S.serverAddr ? joinHostPort(S.serverAddr, p.remotePort) : '';
    // frpc peut rendre « :port » sans hôte : on y ajoute l'adresse du serveur
    // frpc may return ":port" without a host: the server address is added
    const live = liveStatus(p.name)?.remote_addr || '';
    // « ::1:6000 » commence aussi par « : » : seul « :port » seul est complété
    // "::1:6000" also starts with ":": only a bare ":port" is completed
    if (/^:\d+$/.test(live)) return S.serverAddr ? joinHostPort(S.serverAddr, live.slice(1)) : '';
    return fixHostPort(live);
  }
  if (hasDomains(p.type) && p.domains && !p.domains.includes(',')) return p.domains;
  return '';
}

/** Adresse cliquable qui la copie ; Entrée et clic ne doivent pas ouvrir la
 *  ligne (le dialogue d'édition) en plus.
 *  Clickable address that copies it; Enter and click must not also open the
 *  row (the edit dialog). */
function copyAddress(text) {
  const label = t('tunnels.copyAddress', { addr: text });
  return h('button', {
    type: 'button', class: 'route-copy', title: label, 'aria-label': label,
    onClick: async (e) => {
      e.stopPropagation();
      try { await navigator.clipboard.writeText(text); toast(t('common.copied'), 'success', 1800); }
      catch { toast(t('errors.clipboard'), 'error'); }
    },
    onKeydown: (e) => e.stopPropagation(),
  }, h('span', { class: 'mono' }, breakable(text)), icon('copy'));
}

/** Adresse découpée pour un retour à la ligne propre : avant « :port », et dans
 *  un nom de domaine après « . », « - » ou « , », dans une IPv6 entre deux
 *  groupes ; une IPv4 reste d'un seul tenant.
 *  Address split for a clean line break: before ":port", in a domain name
 *  after ".", "-" or ",", in an IPv6 between two groups; an IPv4 stays in one
 *  piece. */
function breakable(text) {
  const i = text.lastIndexOf(':');
  const hasPort = i > 0 && /^[\d,-]+$/.test(text.slice(i + 1));
  const host = hasPort ? text.slice(0, i) : text;
  // IPv6 : coupure possible seulement entre deux groupes, après « : » ou « :: »
  // IPv6: a break is only possible between two groups, after ":" or "::"
  const isV6 = host.startsWith('[') || host.includes(':');
  const parts = isV6 ? host.split(/(?<=:)(?=[^:])/)
    : /^[\d.]+$/.test(host) ? [host] : host.split(/(?<=[.,-])/);
  const out = parts.flatMap((s, k) => (k ? [h('wbr'), s] : [s]));
  // Chaque plage de ports reste d'un seul tenant (pas de coupure après le tiret),
  // mais une longue liste peut passer à la ligne après une virgule
  // Each port range stays in one piece (no break after the hyphen), but a long
  // list may wrap after a comma
  if (hasPort) {
    out.push(h('wbr'));
    text.slice(i).split(/(?<=,)/).forEach((r, k) => {
      if (k) out.push(h('wbr'));
      out.push(h('span', { class: 'route-port' }, r));
    });
  }
  return out;
}

// .route mesure la place disponible (requête de conteneur) : trop étroite, les
// deux extrémités passent l'une sous l'autre au lieu d'être tronquées.
// .route measures the available room (container query): when too narrow, the
// two ends go one below the other instead of being truncated.
function route(fromLabel, from, toLabel, to, fromIsText = false, fromCopy = '') {
  return h('div', { class: 'route' }, h('div', { class: 'route-in' },
    h('span', { class: 'route-end' }, h('small', null, fromLabel),
      fromCopy ? copyAddress(fromCopy) : h('span', { class: fromIsText ? 'route-text' : 'mono', title: from }, fromIsText ? from : breakable(from))),
    h('span', { class: 'route-line', 'aria-hidden': 'true' }),
    h('span', { class: 'route-end' }, h('small', null, toLabel), h('span', { class: 'mono', title: to }, breakable(to)))));
}

function proxyRow(p) {
  const flags = [];
  if (isRanged(p)) flags.push(badge(t('tunnels.flags.range', { count: portSpecCount(p.localPort) }), ''));
  if (p.proxyProtocol) flags.push(badge(t('tunnels.flags.proxyProtocol', { v: p.proxyProtocol }), ''));
  if (p.encryption) flags.push(badge(t('tunnels.flags.encrypted'), ''));
  if (p.compression) flags.push(badge(t('tunnels.flags.compressed'), ''));
  // Plage ou TCP + UDP : frpc a plusieurs tunnels, on montre la première erreur
  // Range or TCP + UDP: frpc has several tunnels, the first error is shown
  const names = frpcNames(p);
  const live = names.length > 1
    ? names.map((n) => liveStatus(n)).find((x) => x?.err)
    : liveStatus(p.name);
  if (serverPicked(p) && live?.remote_addr) flags.push(badge(t('tunnels.flags.assigned'), 'info', { title: t('tunnels.flags.assignedHint') }));
  if (live?.err) flags.push(badge(t('tunnels.flags.error'), 'danger', { title: live.err }));

  const state = stateOf(p, 'proxies');
  const open = () => editProxy(p);
  // TCP + UDP : un badge par protocole, les noms réels des tunnels en infobulle
  // TCP + UDP: one badge per protocol, the real tunnel names in the tooltip
  const halves = splitPair(p);
  return h('li', {
    class: `row${state ? ` is-${state}` : ''}`, dataset: { state: state ? t(`common.state.${state}`) : '' },
    tabindex: '0', onClick: open, onKeydown: (e) => { if (e.key === 'Enter') open(); },
  },
  h('div', { class: 'row-name' },
    h('span', { title: halves.length > 1 ? halves.map((x) => x.name).join(', ') : null }, p.name || t('tunnels.unnamed')),
    halves.map((x) => h('span', { class: 'badge badge-mono badge-outline' }, x.type.toUpperCase())),
    p.user ? badge(p.user, 'user', { title: t('tunnels.fields.user') }) : null),
  route(t('tunnels.public'), publicEndpoint(p), t('tunnels.local'), joinHostPort(p.localIP || '127.0.0.1', p.localPort || '?'),
    hasSecret(p.type) || (serverPicked(p) && !live?.remote_addr) || (hasDomains(p.type) && !p.domains), publicCopyValue(p)),
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
    route(t('visitors.shared'), v.serverName || '?', t('visitors.here'), joinHostPort(v.bindAddr || '127.0.0.1', v.bindPort || '?')),
    h('div', { class: 'row-flags' },
      isVisitorRanged(v) ? badge(t('tunnels.flags.range', { count: portSpecCount(v.bindPort) }), '') : null),
    h('div', { class: 'row-actions' },
      button('', { variant: 'ghost', size: 'sm', iconName: 'edit', title: t('common.edit'), onClick: (e) => { e.stopPropagation(); open(); } }),
      button('', { variant: 'ghost-danger', size: 'sm', iconName: 'trash', title: t('common.delete'), onClick: (e) => { e.stopPropagation(); removeVisitor(v); } })));
  })));
}

async function removeProxy(p) {
  // TCP + UDP : la confirmation nomme les deux tunnels supprimés ensemble
  // TCP + UDP: the confirmation names both tunnels deleted together
  const [tcp, udp] = splitPair(p);
  const ok = await confirmDialog({
    title: t('tunnels.deleteTitle', { name: p.name || t('tunnels.unnamed') }),
    message: udp ? t('tunnels.deletePairText', { tcp: tcp.name, udp: udp.name }) : t('tunnels.deleteText'),
    confirmLabel: t('common.delete'), danger: true,
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

/** Choix « TCP + UDP » : deux tunnels, nom-tcp et nom-udp, avec les mêmes ports.
 *  "TCP + UDP" choice: two tunnels, name-tcp and name-udp, with the same ports. */
const BOTH = TCP_UDP;

/**
 * Erreurs des deux champs de port. Un port seul reste un port ; dès qu'un côté est
 * une plage, le port public est obligatoire et les deux côtés ont le même nombre
 * de ports (frpc associe le 1er au 1er, le 2e au 2e…).
 * Errors of the two port fields. A single port stays a port; as soon as one side is
 * a range, the public port is required and both sides have the same number of
 * ports (frpc pairs the 1st with the 1st, the 2nd with the 2nd…).
 */
function portErrors(kind, local, remote) {
  if (!isRanged({ type: kind, localPort: local, remotePort: remote })) {
    return {
      local: validPort(local) ? '' : t('validation.port'),
      remote: !hasRemotePort(kind) || !remote || validPort(remote) ? '' : t('validation.port'),
    };
  }
  const nl = portSpecCount(local);
  const localError = !nl ? t('validation.portSpec') : nl > MAX_RANGE_PORTS ? t('validation.rangeTooBig', { max: MAX_RANGE_PORTS }) : '';
  // http, https, stcp, xtcp : la plage n'est que sur le port local
  // http, https, stcp, xtcp: the range is only on the local port
  if (!hasRemotePort(kind)) return { local: localError, remote: '' };
  const nr = portSpecCount(remote);
  return {
    local: localError,
    remote: !remote ? t('validation.rangeRemote')
      : !nr ? t('validation.portSpec')
        : nl && nr !== nl ? t('validation.rangeCount', { local: nl, remote: nr }) : '',
  };
}

/**
 * Erreur des domaines http/https : une plage exige {port} dans chaque domaine
 * (sinon tous ses tunnels auraient le même et frps refuserait les suivants) ; un
 * port seul n'en veut pas.
 * Error of the http/https domains: a range requires {port} in each domain
 * (otherwise all its tunnels would share it and frps would refuse the next
 * ones); a single port does not want it.
 */
function domainError(kind, ranged, text) {
  if (!hasDomains(kind)) return '';
  const list = text.split(',').map((s) => s.trim()).filter(Boolean);
  if (ranged && !list.length) return t('validation.required');
  if (ranged) return list.some((d) => !d.includes(PORT_TOKEN)) ? t('validation.domainPort', { token: PORT_TOKEN }) : '';
  return list.some((d) => d.includes(PORT_TOKEN)) ? t('validation.domainPortSingle', { token: PORT_TOKEN }) : '';
}

/** Aide sous le port local : ce qu'une plage crée pour ce type de tunnel.
 *  Hint under the local port: what a range creates for this type of tunnel. */
const rangeHintKey = (kind) => (hasRemotePort(kind) ? 'tunnels.fields.rangeHint'
  : hasDomains(kind) ? 'tunnels.fields.rangeHintDomains' : 'tunnels.fields.rangeHintPrivate');

async function editProxy(existing) {
  const draft = existing ? { ...existing, extras: existing.extras } : newProxy();
  // Certificats du panel, lus une fois : ils alimentent le choix du certificat
  // des tunnels HTTPS.
  // Panel certificates, read once: they feed the HTTPS tunnel certificate choice.
  if (!S.certsLoaded) {
    try {
      const d = await api('/api/manager/certs');
      S.certs = d.certs || [];
      S.proxy = d.proxy || null;
    } catch { S.certs = []; S.proxy = null; }
    S.certsLoaded = true;
  }

  openDialog({
    title: existing ? t('tunnels.editTitle', { name: existing.name }) : t('tunnels.newTitle'),
    description: t('tunnels.editorHint'),
    size: 'lg',
    build: ({ close }) => {
      const name = input({ value: draft.name, placeholder: t('tunnels.fields.namePlaceholder'), mono: true, autocomplete: 'off' });
      // Identifiant de l'utilisateur du tunnel : annotation frp, reprise dans la
      // liste et sur le tableau de bord de frps
      // Tunnel user identifier: frp annotation, shown in the list and on the
      // frps dashboard
      const user = input({ value: draft.user, placeholder: t('tunnels.fields.userPlaceholder'), maxlength: '64' });
      const typeHint = h('p', { class: 'field-hint' });
      // « TCP + UDP » à la création, et pour une paire déjà regroupée (elle peut
      // redevenir un seul type) : pas pour un tunnel seul, que cela renommerait
      // "TCP + UDP" on creation, and for an already grouped pair (it can go back
      // to a single type): not for a single tunnel, which this would rename
      const kinds = PROXY_TYPES.flatMap((v) => (v === 'udp' && (!existing || existing.type === BOTH) ? [v, BOTH] : [v]));
      const type = segmented(kinds.map((v) => ({ value: v, label: v === BOTH ? 'TCP + UDP' : v.toUpperCase() })), draft.type, () => sync(), { label: t('tunnels.fields.type') });

      // Pas de clavier numérique : une plage s'écrit avec « - » et « , »
      // No numeric keyboard: a range is written with "-" and ","
      const localIP = input({ value: draft.localIP, placeholder: '127.0.0.1', mono: true });
      const localPort = input({ value: draft.localPort, placeholder: '8080', mono: true });
      const remotePort = input({ value: draft.remotePort, placeholder: '8080', mono: true });
      const rangeHint = h('p', { class: 'field-hint' });
      const domains = input({ value: draft.domains, placeholder: 'app.example.com', mono: true });
      const secret = secretInput({ value: draft.secretKey, placeholder: t('tunnels.fields.secretPlaceholder') });

      // Certificat du site (tunnels HTTPS) : frpc le présente lui-même via le
      // plugin https2http / https2https, frps ne fait que router par SNI.
      // Site certificate (HTTPS tunnels): frpc presents it itself through the
      // https2http / https2https plugin, frps only routes by SNI.
      const certOptions = [
        { value: '', label: t('tunnels.fields.certNone') },
        ...S.certs.map((c) => ({ value: c.id, label: (c.domains || []).join(', ') || c.id })),
      ];
      // Certificat déjà déclaré dans le tunnel mais absent du panel : gardé tel quel
      // Certificate already declared in the tunnel but missing from the panel: kept as is
      if (draft.certCrt && !S.certs.some((c) => c.crt_path === draft.certCrt)) {
        certOptions.push({ value: '__existing', label: draft.certCrt });
      }
      // Réglage « ports HTTPS en reverse proxy avec certificats » : son
      // certificat et son mode sont les valeurs par défaut d'un tunnel nouveau ;
      // un tunnel existant garde ce qu'il déclare.
      // "HTTPS ports as a reverse proxy with certificates" setting: its
      // certificate and mode are the defaults for a new tunnel; an existing
      // tunnel keeps what it declares.
      const proxy = S.proxy && S.proxy.ready ? S.proxy : null;
      const proxyCertID = proxy && S.certs.some((c) => c.id === proxy.cert_id) ? proxy.cert_id : '';
      const certSelect = select(certOptions,
        S.certs.find((c) => c.crt_path === draft.certCrt)?.id || (draft.certCrt ? '__existing' : proxyCertID));
      const certMode = segmented([
        { value: 'https2http', label: t('tunnels.fields.certModeHttp') },
        { value: 'https2https', label: t('tunnels.fields.certModeHttps') },
      ], draft.certPlugin || (proxy ? proxy.mode : '') || 'https2http', () => sync(), { label: t('tunnels.fields.certMode') });
      const certField = field({ label: t('tunnels.fields.cert'),
        hint: S.certs.length ? t('tunnels.fields.certHint') : t('tunnels.fields.certMissing'), control: certSelect });
      const certModeField = field({ label: t('tunnels.fields.certMode'), hint: t('tunnels.fields.certModeHint'), control: certMode });
      certSelect.addEventListener('change', () => sync());

      // Après un import ou une obtention Let’s Encrypt depuis cet écran, la
      // liste et le sélecteur sont rafraîchis, et le nouveau certificat choisi.
      // After an import or a Let’s Encrypt issuance from this screen, the list
      // and the picker are refreshed, and the new certificate selected.
      async function reloadCerts(selectID) {
        try {
          const d = await api('/api/manager/certs');
          S.certs = d.certs || [];
          S.proxy = d.proxy || null;
        } catch { return; }
        const keep = selectID || certSelect.value;
        certSelect.replaceChildren(
          h('option', { value: '' }, t('tunnels.fields.certNone')),
          ...S.certs.map((c) => h('option', { value: c.id }, (c.domains || []).join(', ') || c.id)));
        if (draft.certCrt && !S.certs.some((c) => c.crt_path === draft.certCrt)) {
          certSelect.append(h('option', { value: '__existing' }, draft.certCrt));
        }
        certSelect.value = [...certSelect.options].some((o) => o.value === keep) ? keep : '';
        certField.querySelector('.field-hint').textContent = S.certs.length
          ? t('tunnels.fields.certHint') : t('tunnels.fields.certMissing');
        sync();
      }

      // Bandeau jaune des tunnels HTTPS : un port HTTPS n'est servi en reverse
      // proxy avec certificats que si un certificat est choisi ; le réglage
      // correspondant vit dans Réglages → Proxy inverse HTTPS.
      // Yellow banner for HTTPS tunnels: an HTTPS port is only served as a
      // reverse proxy with certificates when one is chosen; the matching
      // setting lives in Settings → HTTPS reverse proxy.
      const httpsNote = h('div');
      function syncHttpsNote() {
        if (kindOf(type.value) !== 'https') {
          httpsNote.replaceChildren();
          httpsNote.hidden = true;
          return;
        }
        httpsNote.hidden = false;
        // Sans certificat : les deux façons d'en créer un s'ouvrent ici, sans
        // quitter l'éditeur ; les réglages restent accessibles pour choisir
        // le mode par défaut des tunnels.
        // Without a certificate: both ways to create one open here, without
        // leaving the editor; the settings stay reachable to pick the default
        // mode of the tunnels.
        const actions = [];
        if (!certSelect.value) {
          actions.push(
            button(t('tunnels.fields.certAcme'), {
              size: 'sm', variant: 'secondary', iconName: 'shield',
              onClick: () => acmeCertDialog({ onSaved: (id) => reloadCerts(id) }),
            }),
            button(t('tunnels.fields.certAdd'), {
              size: 'sm', variant: 'secondary', iconName: 'upload',
              onClick: () => importCertDialog({ onSaved: (id) => reloadCerts(id) }),
            }));
        }
        actions.push(button(t('tunnels.fields.certProxySettings'), {
          size: 'sm', variant: 'secondary',
          onClick: () => { close(false); navigate('settings', { s: 'proxy' }); },
        }));
        httpsNote.replaceChildren(callout({
          type: 'warn', compact: true,
          text: certSelect.value ? t('tunnels.fields.certProxyOn') : t('tunnels.fields.certProxyOff'),
          actions,
        }));
      }

      const nameField = field({ label: t('tunnels.fields.name'), hint: t('tunnels.fields.nameHint'), tomlKey: 'name', control: name });
      const userField = field({ label: t('tunnels.fields.user'), hint: t('tunnels.fields.userHint'), tomlKey: 'annotations.user', optional: true, control: user });
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

      // TCP + UDP se comporte comme TCP pour les champs affichés
      // TCP + UDP behaves like TCP for the fields shown
      const kindOf = (tp) => (tp === BOTH ? 'tcp' : tp);

      function sync() {
        const tp = type.value;
        const kind = kindOf(tp);
        typeHint.textContent = t(`tunnels.types.${tp}`);
        remoteField.hidden = !hasRemotePort(kind);
        // TCP + UDP : le port public ne peut pas rester vide, l'aide le dit
        // TCP + UDP: the public port cannot stay empty, the hint says so
        const server = S.serverAddr || t('tunnels.server');
        remoteField.querySelector('.field-hint').textContent = tp === BOTH
          ? t('tunnels.fields.remotePortHintBoth', { server })
          : t('tunnels.fields.remotePortHint', { server });
        // Plage pour tous les types : l'aide dit ce qu'elle crée pour celui-ci
        // Range for all types: the hint says what it creates for this one
        rangeHint.textContent = t(rangeHintKey(kind), { token: PORT_TOKEN });
        domainsField.hidden = !hasDomains(kind);
        secretField.hidden = !hasSecret(kind);
        // Certificat : seulement pour un tunnel https, et si le panel en a un
        // Certificate: only for an https tunnel, and when the panel has one
        certField.hidden = kind !== 'https' || !S.certs.length;
        certModeField.hidden = kind !== 'https' || !certSelect.value;
        syncHttpsNote();
        syncNameHint();
        syncDomainsHint();
      }
      // http/https en plage : l'aide des domaines rappelle {port}
      // http/https range: the domains hint recalls {port}
      function syncDomainsHint() {
        const ranged = isRanged({ type: kindOf(type.value), localPort: localPort.value, remotePort: remotePort.value });
        domainsField.querySelector('.field-hint').textContent = ranged
          ? t('tunnels.fields.domainsHintRange', { token: PORT_TOKEN })
          : t('tunnels.fields.domainsHint');
      }
      localPort.addEventListener('input', syncDomainsHint);
      // TCP + UDP : l'aide du nom donne les deux noms que frpc utilisera
      // TCP + UDP: the name hint gives the two names frpc will use
      function syncNameHint() {
        const base = name.value.trim() || t('tunnels.fields.namePlaceholder');
        nameField.querySelector('.field-hint').textContent = type.value === BOTH
          ? t('tunnels.fields.nameHintBoth', { tcp: `${base}-tcp`, udp: `${base}-udp` })
          : t('tunnels.fields.nameHint');
      }
      name.addEventListener('input', syncNameHint);
      sync();

      function submit(e) {
        e.preventDefault();
        const tp = type.value;
        const kind = kindOf(tp);
        const values = {
          name: name.value.trim(),
          // Valeur telle qu'écrite (sans crochets) : « [::1] » ne compte pas comme une modification
          // Value as written (no brackets): "[::1]" does not count as a change
          localIP: unbracket(localIP.value.trim()),
          localPort: localPort.value.replace(/\s+/g, ''),
          remotePort: remotePort.value.replace(/\s+/g, ''),
        };
        // Noms de la liste et noms réels côté frpc (« nom-<port> » pour une
        // plage, nom-tcp et nom-udp pour TCP + UDP) : aucun en double
        // Names in the list and real names on the frpc side ("name-<port>" for
        // a range, name-tcp and name-udp for TCP + UDP): no duplicates
        const others = S.proxies.filter((p) => p.uid !== draft.uid);
        const taken = new Set([...others.map((p) => p.name), ...others.flatMap(frpcNames)]);
        const clash = [values.name, ...frpcNames({ ...values, type: tp })].find((n) => taken.has(n));
        const ports = portErrors(kind, values.localPort, values.remotePort);

        let ok = true;
        const check = (ctrl, msg) => { setFieldError(ctrl, msg); if (msg) ok = false; };
        // Forme du nom (l'aide dit « sans espace ») : seulement pour un nom nouveau
        // ou changé, un ancien nom écrit à la main reste accepté tel quel
        // Name format (the hint says "no spaces"): only for a new or changed
        // name, an old name written by hand stays accepted as is
        const badName = values.name !== existing?.name && !tunnelNameOk(values.name);
        check(name, !values.name ? t('validation.required')
          : badName ? t('validation.nameFormat')
          : clash === values.name ? t('validation.nameTaken')
            : clash ? t('validation.nameClash', { name: clash }) : '');
        check(localIP, !values.localIP ? t('validation.required') : '');
        check(localPort, ports.local);
        // TCP + UDP sans port public : le serveur en tirerait un différent pour chacun
        // TCP + UDP without a public port: the server would pick a different one for each
        check(remotePort, tp === BOTH && !values.remotePort ? t('validation.bothRemote') : ports.remote);
        check(domains, domainError(kind, isRanged({ ...values, type: kind }), domains.value));
        if (!ok) { draftDialogFocusError(); return; }

        // TCP + UDP reste une seule entrée : splitPair écrit ses deux tunnels
        // TCP + UDP stays a single entry: splitPair writes its two tunnels
        // Certificat choisi : chemins du panel, ou ceux déjà écrits dans le tunnel
        // Chosen certificate: panel paths, or those already written in the tunnel
        const chosenCert = tp === 'https' && certSelect.value && certSelect.value !== '__existing'
          ? S.certs.find((c) => c.id === certSelect.value) : null;
        const keepCert = tp === 'https' && certSelect.value === '__existing';
        Object.assign(draft, values, {
          type: tp,
          user: user.value.trim(),
          domains: domains.value.trim(),
          secretKey: secret.input.value,
          proxyProtocol: ppv.value,
          encryption: encryption.input.checked,
          compression: compression.input.checked,
          certPlugin: chosenCert ? (certMode.value || 'https2http') : keepCert ? draft.certPlugin || 'https2http' : '',
          certCrt: chosenCert ? chosenCert.crt_path : keepCert ? draft.certCrt : '',
          certKey: chosenCert ? chosenCert.key_path : keepCert ? draft.certKey : '',
          certHost: chosenCert || keepCert ? (values.localIP || '127.0.0.1') : '',
        });
        if (existing) S.proxies = S.proxies.map((p) => (p.uid === draft.uid ? draft : p));
        else S.proxies = [...S.proxies, draft];
        close(true);
        render();
      }

      const formId = `proxy-form-${draft.uid}`;
      return {
        body: [h('form', { id: formId, class: 'form-stack', onSubmit: submit, novalidate: true },
          nameField,
          userField,
          h('div', { class: 'field' },
            h('span', { class: 'field-label' }, t('tunnels.fields.type'), h('span', { class: 'field-key' }, 'type')),
            type, typeHint),
          h('fieldset', { class: 'fieldset' },
            h('legend', { class: 'fieldset-title' }, t('tunnels.fields.localService')),
            h('div', { class: 'form-grid-2' },
              field({ label: t('tunnels.fields.localIP'), tomlKey: 'localIP', control: localIP }),
              field({ label: t('tunnels.fields.localPort'), tomlKey: 'localPort', control: localPort })),
            h('p', { class: 'field-hint' }, t('tunnels.fields.localHint')),
            rangeHint),
          h('fieldset', { class: 'fieldset' },
            h('legend', { class: 'fieldset-title' }, t('tunnels.fields.publicAccess')),
            httpsNote, remoteField, domainsField, certField, certModeField, secretField),
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
      const serverName = input({ value: draft.serverName, placeholder: t('tunnels.fields.serverNamePlaceholder'), mono: true });
      const secret = secretInput({ value: draft.secretKey, placeholder: t('tunnels.fields.secretPlaceholder') });
      const bindAddr = input({ value: draft.bindAddr, placeholder: '127.0.0.1', mono: true });
      // Pas de clavier numérique : une plage s'écrit avec « - » et « , »
      // No numeric keyboard: a range is written with "-" and ","
      const bindPort = input({ value: draft.bindPort, placeholder: '9001', mono: true });
      // Plage : ports du tunnel visé, s'ils diffèrent des ports locaux
      // Range: ports of the target tunnel, if they differ from the local ports
      const serverPorts = input({ value: draft.serverPorts, placeholder: t('visitors.fields.serverPortsPlaceholder'), mono: true });
      const serverPortsField = field({ label: t('visitors.fields.serverPorts'), hint: t('visitors.fields.serverPortsHint'), optional: true, control: serverPorts });
      const syncRange = () => { serverPortsField.hidden = !isPortRange(bindPort.value); };
      bindPort.addEventListener('input', syncRange);
      syncRange();

      function submit(e) {
        e.preventDefault();
        let ok = true;
        const check = (ctrl, msg) => { setFieldError(ctrl, msg); if (msg) ok = false; };
        const nm = name.value.trim();
        const bp = bindPort.value.replace(/\s+/g, '');
        const ranged = isPortRange(bp);
        const sp = ranged ? serverPorts.value.replace(/\s+/g, '') : '';
        // Noms réels côté frpc (« nom-<port> » pour une plage) : aucun en double
        // Real names on the frpc side ("name-<port>" for a range): no duplicates
        const taken = new Set(S.visitors.filter((v) => v.uid !== draft.uid).flatMap((v) => [v.name, ...visitorNames(v)]));
        const clash = [nm, ...visitorNames({ name: nm, bindPort: bp })].find((n) => taken.has(n));
        check(name, !nm ? t('validation.required')
          : clash === nm ? t('validation.nameTaken')
            : clash ? t('validation.visitorNameClash', { name: clash }) : '');
        check(serverName, !serverName.value.trim() ? t('validation.required') : '');
        // Plage : autant de ports pour le tunnel visé que pour l'écoute locale
        // Range: as many ports for the target tunnel as for the local listener
        const nb = ranged ? portSpecCount(bp) : 0;
        const ns = sp ? portSpecCount(sp) : nb;
        check(bindPort, !ranged ? (validPort(bp) ? '' : t('validation.port'))
          : !nb ? t('validation.portSpec')
            : nb > MAX_RANGE_PORTS ? t('validation.rangeTooBig', { max: MAX_RANGE_PORTS }) : '');
        // Champ facultatif : vide, il reprend les ports locaux, déjà vérifiés
        // Optional field: when empty it reuses the local ports, already checked
        check(serverPorts, !ranged || !sp ? ''
          : !ns ? t('validation.portSpec')
            : nb && ns !== nb ? t('validation.rangeCountVisitor', { local: nb, remote: ns }) : '');
        if (!ok) { draftDialogFocusError(); return; }
        Object.assign(draft, {
          name: nm, type: type.value, serverName: serverName.value.trim(), secretKey: secret.input.value,
          bindAddr: unbracket(bindAddr.value.trim()), bindPort: bp, serverPorts: sp === bp ? '' : sp,
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
          h('p', { class: 'field-hint' }, t('visitors.fields.bindHint')),
          h('p', { class: 'field-hint' }, t('visitors.fields.rangeHint')),
          serverPortsField)],
        foot: [
          button(t('common.cancel'), { onClick: () => close(false) }),
          button(existing ? t('tunnels.apply') : t('tunnels.addToList'), { variant: 'primary', type: 'submit', form: formId }),
        ],
      };
    },
  });
}

// ── Import / export TOML ───────────────────────────────────────────────────

/** Télécharge la configuration frpc telle qu'elle est affichée (brouillon
 *  compris) : tous les paramètres des tunnels, y compris ceux que l'interface
 *  ne gère pas, sont écrits tels quels.
 *  Downloads the frpc configuration as displayed (draft included): every tunnel
 *  parameter, including those the interface does not manage, is written as is. */
function exportToml() {
  const content = composeClientConfig(S.baseText, S.proxies, S.visitors);
  const name = `${(S.iid || 'frpc').replace(/[^\w.-]+/g, '-')}.toml`;
  const url = URL.createObjectURL(new Blob([content], { type: 'application/toml' }));
  const link = h('a', { href: url, download: name });
  document.body.append(link);
  link.click();
  link.remove();
  // Laissez le navigateur démarrer le téléchargement avant de libérer le blob
  // Let the browser start the download before freeing the blob
  setTimeout(() => URL.revokeObjectURL(url), 1000);
  toast(t('tunnels.exported', { name }), 'success');
}

/** Ajoute à la liste les [[proxies]] et [[visitors]] d'un fichier TOML. La
 *  partie connexion de ce fichier n'est pas reprise : elle appartient à
 *  l'instance, et se modifie dans sa configuration.
 *  Adds the [[proxies]] and [[visitors]] of a TOML file to the list. The
 *  connection part of that file is not taken over: it belongs to the instance
 *  and is changed in its configuration. */
function importDialog() {
  const file = h('input', { type: 'file', accept: '.toml,text/plain', hidden: true });
  const chosen = h('p', { class: 'field-hint' }, t('tunnels.importNoFile'));
  const fileBtn = button(t('tunnels.importChoose'), { size: 'sm', iconName: 'upload', onClick: () => file.click() });
  let text = '';
  file.addEventListener('change', async () => {
    const f = file.files && file.files[0];
    text = '';
    chosen.textContent = f ? f.name : t('tunnels.importNoFile');
    if (!f) return;
    try { text = await f.text(); }
    catch { toast(t('tunnels.importReadError'), 'error'); }
  });

  openDialog({
    title: t('tunnels.importTitle'),
    description: t('tunnels.importDesc'),
    build: ({ close }) => {
      function submit() {
        if (!text) { toast(t('tunnels.importNoFile'), 'error'); return; }
        const parsed = parseToml(text);
        const proxies = parsed.proxies.map(proxyFromBlock).filter((p) => p.name);
        const visitors = parsed.visitors.map(visitorFromBlock).filter((v) => v.name);
        // Noms déjà pris, par la liste et par les noms réels de frpc (paires et
        // plages) : un tunnel en double empêcherait frpc de démarrer
        // Names already taken, by the list and by frpc's real names (pairs and
        // ranges): a duplicate tunnel would keep frpc from starting
        const taken = new Set([
          ...S.proxies.flatMap((p) => [p.name, ...frpcNames(p)]),
          ...S.visitors.flatMap((v) => [v.name, ...visitorNames(v)]),
        ]);
        const skipped = [];
        const keep = (item, names) => {
          if (names.some((n) => taken.has(n))) { skipped.push(item.name); return false; }
          names.forEach((n) => taken.add(n));
          return true;
        };
        const addedProxies = pairProxies(proxies).filter((p) => keep(p, [p.name, ...frpcNames(p)]));
        const addedVisitors = visitors.filter((v) => keep(v, [v.name, ...visitorNames(v)]));
        if (!addedProxies.length && !addedVisitors.length) {
          toast(skipped.length ? t('tunnels.importSkipped', { names: skipped.join(', ') }) : t('tunnels.importEmpty'), 'error');
          return;
        }
        S.proxies = [...S.proxies, ...addedProxies];
        S.visitors = [...S.visitors, ...addedVisitors];
        close(true);
        render();
        toast(t('tunnels.importDone', { proxies: addedProxies.length, visitors: addedVisitors.length }), 'success');
        if (skipped.length) toast(t('tunnels.importSkipped', { names: skipped.join(', ') }), 'error');
      }
      return {
        body: [
          h('div', { class: 'row-actions' }, fileBtn, file),
          chosen,
        ],
        foot: [
          button(t('common.cancel'), { onClick: () => close(false) }),
          button(t('tunnels.importConfirm'), { variant: 'primary', onClick: submit, autofocus: true }),
        ],
      };
    },
  });
}

// ── Enregistrement ─────────────────────────────────────────────────────────

async function save(restart) {
  try {
    // Config frpc : on relit la partie connexion au dernier moment
    const current = await api(configURL(S.iid)).catch(() => null);
    const base = current && current.ok ? current.content || '' : S.baseText;
    const content = composeClientConfig(base, S.proxies, S.visitors);
    const d = await post(configURL(S.iid), { content });
    if (!d.ok) { toast(d.msg || t('errors.generic'), 'error'); return; }
    S.baseText = content;
    S.snapshot = snapshotOf();
    toast(d.applied ? t('common.applied') : (d.note || t('tunnels.saved')), d.note ? 'info' : 'success');

    // Redémarrage seulement si le rechargement à chaud n'a pas pu s'appliquer
    // (la réponse peut ne jamais arriver si le panel passe par ce tunnel)
    // Restart only when the live reload could not be applied (the reply may never
    // arrive when the panel is reached through this tunnel)
    if (restart && !d.applied) await restartFrpc();
  } catch (e) {
    toastError(e);
  }
  await load();
}
