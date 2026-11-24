// ── Clients : les frpc connectés à un frps, et les ports qu'ils hébergent ──
// Une ligne par client : nom d'utilisateur frp, adresse, version, état, et les
// ports publics de ses tunnels. Rafraîchi toutes les dix secondes.

import { t } from '../i18n.js';
import { api } from '../api.js';
import { instanceIds, displayName, can } from '../store.js';
import {
  h, button, busy, select, badge, emptyState, pageHeader, card, callout, toast, toastError, confirmDialog,
} from '../ui.js';

const FRPS_KEY = 'frpm.clients.frps';
const POLL_MS = 10000;

const S = { iid: '', timer: null, els: {} };

/** Nom lisible d'un client : son utilisateur frp, sinon son hôte, sinon son identifiant. */
function clientName(c) {
  return c.user || c.hostname || c.clientID || c.ip || '—';
}

/** Identifiant de connexion abrégé (il fait une vingtaine de caractères). */
const shortID = (id) => (id.length > 14 ? `${id.slice(0, 14)}…` : id);

function ago(seconds) {
  if (seconds < 60) return t('clients.ago.s', { n: Math.max(0, seconds) });
  if (seconds < 3600) return t('clients.ago.m', { n: Math.floor(seconds / 60) });
  if (seconds < 86400) return t('clients.ago.h', { n: Math.floor(seconds / 3600) });
  return t('clients.ago.d', { n: Math.floor(seconds / 86400) });
}

function stateBadge(c) {
  return c.online
    ? h('span', { class: 'client-state' }, h('span', { class: 'dot' }), badge(t('clients.online'), 'success'))
    : badge(t('clients.offline'));
}

/** Ports hébergés : « 2022 · tcp », ou le domaine pour http et https. */
function portsCell(c) {
  const ports = c.ports || [];
  if (!ports.length) return h('span', { class: 'muted' }, t('clients.noPorts'));
  return h('div', { class: 'client-ports' },
    ports.map((p) => h('span', {
      class: `badge ${p.status === 'online' ? 'badge-info' : ''}`.trim(),
      title: p.where ? `${p.name} · ${p.type}` : `${p.name} · ${t('clients.privatePort')}`,
    },
      h('span', { class: 'mono' }, p.where || p.name),
      h('span', { class: 'muted' }, ` ${p.type}`))));
}

function table(frps) {
  const clients = frps.clients || [];
  if (!clients.length) {
    return h('div', { class: 'card' }, emptyState({
      iconName: 'laptop', title: t('clients.emptyTitle'), text: t('clients.emptyText'),
    }));
  }
  const now = Date.now() / 1000;
  const online = clients.filter((c) => c.online).length;
  const head = ['client', 'address', 'version', 'ports', 'last'].map((key) => h('th', null, t(`clients.col.${key}`)));

  const row = (c) => {
    const name = clientName(c);
    const last = c.lastConnected
      ? h('td', { class: 'muted', title: new Date(c.lastConnected * 1000).toLocaleString() }, ago(Math.round(now - c.lastConnected)))
      : h('td', { class: 'muted' }, '—');
    return h('tr', null,
      h('td', null,
        h('div', { class: 'client-name' }, name),
        // L'identifiant et l'hôte en second : sauf s'ils ont servi de nom
        // Identifier and host underneath: unless they were used as the name
        h('div', { class: 'client-id' },
          c.clientID && c.clientID !== name ? h('span', { class: 'mono', title: c.clientID }, shortID(c.clientID)) : '',
          c.hostname && c.hostname !== name ? h('span', { class: 'muted' }, ` · ${c.hostname}`) : '')),
      h('td', null,
        h('span', { class: 'mono' }, c.ip || '—'),
        h('div', null, stateBadge(c))),
      h('td', null,
        c.version ? h('span', { class: 'mono' }, `v${c.version}`) : h('span', { class: 'muted' }, '—'),
        c.protocol ? h('div', null, badge(t('clients.protocol', { protocol: c.protocol }))) : ''),
      h('td', null, portsCell(c)),
      last);
  };

  return h('section', { class: 'card' },
    h('div', { class: 'card-head' },
      h('div', null,
        h('h2', { class: 'card-title' }, t('clients.list')),
        h('p', { class: 'card-desc' }, t('clients.countLine', { online, total: clients.length })))),
    h('div', { class: 'fw-table-wrap' },
      h('table', { class: 'fw-table client-table' },
        h('thead', null, h('tr', null, head)),
        h('tbody', null, clients.map(row)))));
}

function body(frps) {
  if (!frps) {
    return h('div', { class: 'card' }, emptyState({
      iconName: 'laptop', title: t('clients.noFrpsTitle'), text: t('clients.noFrpsText'),
    }));
  }
  if (!frps.reachable) {
    // Tableau de bord absent : on propose de l'ajouter, plutôt que de laisser
    // l'utilisateur chercher où écrire le bloc dans la config.
    // Missing dashboard: offer to add it, instead of leaving the user to find
    // where the block goes in the config.
    const enable = frps.needs_dashboard && can('config.write') ? dashboardButton() : null;
    return card({
      title: displayName(frps.iid),
      body: callout({
        type: 'warn', title: t('clients.unavailableTitle'), text: frps.error || t('errors.generic'),
        actions: enable ? [enable] : null,
      }),
    });
  }
  return table(frps);
}

export default {
  id: 'clients',
  title: () => t('nav.clients'),
  perm: 'ports.view',

  async mount(view, params) {
    const ids = instanceIds((i) => i.type === 'frps');
    let stored = '';
    try { stored = localStorage.getItem(FRPS_KEY) || ''; } catch { /* ignoré */ }
    S.iid = ids.includes(params.iid) ? params.iid : (ids.includes(stored) ? stored : ids[0] || '');

    if (!ids.length) {
      view.append(h('div', { class: 'page' },
        pageHeader({ title: t('clients.title'), description: t('clients.description') }),
        h('div', { class: 'card' }, emptyState({ iconName: 'laptop', title: t('clients.noFrpsTitle'), text: t('clients.noFrpsText') }))));
      return;
    }

    const instSel = select(ids.map((id) => ({ value: id, label: displayName(id) })), S.iid, {
      class: 'select select-inline', 'aria-label': t('common.instance'),
    });
    instSel.addEventListener('change', () => {
      S.iid = instSel.value;
      try { localStorage.setItem(FRPS_KEY, S.iid); } catch { /* ignoré */ }
      render();
      load();
    });

    const refresh = button(t('common.refresh'), { iconName: 'refresh', onClick: () => busy(refresh, load) });
    S.els = { body: h('div', { class: 'client-body' }, h('p', { class: 'muted' }, t('common.loading'))) };

    view.append(h('div', { class: 'page' },
      pageHeader({
        title: t('clients.title'), description: t('clients.description'),
        actions: [ids.length > 1 ? instSel : null, refresh].filter(Boolean),
      }),
      S.els.body));

    await load();
    S.timer = setInterval(load, POLL_MS);
  },

  unmount() {
    clearInterval(S.timer);
    S.timer = null;
    S.els = {};
  },
};

/** Bouton d'activation : il porte lui-même la référence passée à busy(), le
 *  gestionnaire de clic ne recevant que l'événement. */
function dashboardButton() {
  const btn = button(t('clients.enable'), { variant: 'primary', iconName: 'shield' });
  btn.addEventListener('click', () => enableDashboard(btn));
  return btn;
}

/** Ajoute le bloc [webServer] local à la config du frps, puis propose de le
 *  redémarrer (il ne relit pas sa configuration à chaud). */
async function enableDashboard(btn) {
  // L'attente ne couvre que l'écriture : le bouton reprend son libellé avant
  // la question du redémarrage, sinon il tourne pendant que l'utilisateur lit.
  // Waiting covers the write only: the button gets its label back before the
  // restart question, instead of spinning while the user reads.
  let port = 0;
  let host = '127.0.0.1';
  await busy(btn, async () => {
    try {
      const d = await api(`/api/clients/dashboard/${encodeURIComponent(S.iid)}`, { method: 'POST' });
      if (!d.ok) {
        toast(d.msg || t('errors.generic'), 'error');
        return;
      }
      if (!d.changed) {
        toast(t('clients.enableAlready'), 'info');
        return;
      }
      port = d.port || 0;
      host = d.addr || host;
    } catch (e) {
      toastError(e);
    }
  });
  if (!port) return;

  toast(t('clients.enabled', { host, port }));
  const restart = await confirmDialog({
    title: t('clients.restartTitle'), message: t('clients.restartText', { host, port }), confirmLabel: t('clients.restart'),
  });
  if (!restart) return;
  try {
    await api(`/api/service/${encodeURIComponent(S.iid)}/restart`, { method: 'POST' });
    toast(t('clients.restarting'), 'info');
    setTimeout(load, 4000);
  } catch (e) {
    toastError(e);
  }
}

function render() {
  if (!S.els.body) return;
  S.els.body.replaceChildren(body(S.frps));
}

let loadSeq = 0;

async function load() {
  if (!S.iid) return;
  const seq = ++loadSeq;
  try {
    const d = await api('/api/clients');
    if (seq !== loadSeq || !S.els.body) return;
    S.frps = (d.frps || []).find((f) => f.iid === S.iid) || null;
    render();
  } catch (e) {
    if (seq === loadSeq) toastError(e);
  }
}
