// ── Journaux : 200 dernières lignes ou flux en direct (WebSocket, repli SSE) ──

import { t } from '../i18n.js';
import { api } from '../api.js';
import { store, instanceIds, displayName, isDocker } from '../store.js';
import { h, icon, button, busy, select, segmented, emptyState, pageHeader, toast, toastError } from '../ui.js';
import { navigate, replaceParams } from '../router.js';

const MAX_LINES = 2000;
const S = { iid: '', source: 'journal', filter: '', live: null, els: {} };

function levelOf(line) {
  if (/error|ERR\b|\[E\]/i.test(line)) return 'log-error';
  if (/warn|\[W\]/i.test(line)) return 'log-warn';
  if (/start|connect|success|\blogin to server success/i.test(line)) return 'log-ok';
  return '';
}

function lineEl(text) {
  const el = h('span', { class: `log-line ${levelOf(text)}`.trim() }, `${text}\n`);
  if (S.filter && !text.toLowerCase().includes(S.filter)) el.hidden = true;
  return el;
}

export default {
  id: 'logs',
  title: () => t('nav.logs'),

  async mount(view, params) {
    const ids = instanceIds();
    S.iid = ids.includes(params.iid) ? params.iid : ids[0] || '';
    S.filter = '';

    if (!S.iid) {
      view.append(h('div', { class: 'page' },
        pageHeader({ title: t('logs.title'), description: t('logs.description') }),
        h('div', { class: 'card' }, emptyState({
          iconName: 'logs', title: t('logs.emptyTitle'), text: t('logs.emptyText'),
          actions: [button(t('nav.dashboard'), { onClick: () => navigate('dashboard') })],
        }))));
      return;
    }

    const instSel = select(ids.map((id) => ({ value: id, label: displayName(id) })), S.iid, {
      class: 'select select-inline', 'aria-label': t('common.instance'),
    });
    instSel.addEventListener('change', () => { S.iid = instSel.value; replaceParams({ iid: S.iid }); syncSource(); load(); });

    const source = segmented([
      { value: 'journal', label: t('logs.sources.journal') },
      { value: 'file', label: t('logs.sources.file') },
    ], S.source, (v) => {
      S.source = v;
      // En direct : on bascule le flux sur la nouvelle source, sinon on recharge
      if (S.live) { stopLive(); startLive(); } else load();
    }, { label: t('logs.source') });

    const search = h('input', { class: 'input', type: 'search', placeholder: t('logs.filter'), 'aria-label': t('logs.filter') });
    search.addEventListener('input', () => {
      S.filter = search.value.trim().toLowerCase();
      for (const el of S.els.out.children) el.hidden = !!S.filter && !el.textContent.toLowerCase().includes(S.filter);
    });

    const refreshBtn = button(t('common.refresh'), { iconName: 'refresh', onClick: () => busy(refreshBtn, load) });
    const liveBtn = button(t('logs.live'), { iconName: 'radio', onClick: toggleLive });
    const bottomBtn = button('', { iconName: 'arrow-down', title: t('logs.toBottom'), onClick: () => { S.els.out.scrollTop = S.els.out.scrollHeight; } });
    const livePill = h('span', { class: 'live-pill', hidden: true }, h('span', { class: 'dot dot-pulse' }), t('logs.liveOn'));

    S.els = { out: h('pre', { class: 'log-view', tabindex: '0', 'aria-label': t('logs.title'), 'aria-live': 'off' }), source, liveBtn, livePill };

    view.append(h('div', { class: 'page' },
      pageHeader({ title: t('logs.title'), description: t('logs.description'), actions: ids.length > 1 ? [instSel] : [] }),
      h('section', { class: 'card' },
        h('div', { class: 'toolbar' },
          source,
          h('div', { class: 'input-affix' }, icon('search'), search),
          h('span', { class: 'spacer' }),
          livePill, refreshBtn, liveBtn, bottomBtn),
        S.els.out)));

    syncSource();
    await load();
  },

  unmount() {
    stopLive();
    S.els = {};
  },
};

function syncSource() {
  // Conteneur Docker : les journaux viennent du socket Docker, pas de choix
  S.els.source.hidden = isDocker(store.instances[S.iid]);
}

let loadSeq = 0;

async function load() {
  stopLive();
  const out = S.els.out;
  if (!out) return;
  const seq = ++loadSeq;
  out.textContent = t('common.loading');
  const src = isDocker(store.instances[S.iid]) ? 'docker' : S.source;
  try {
    const d = await api(`/api/logs/${encodeURIComponent(S.iid)}?source=${src}`);
    // Réponse obsolète : le direct a été lancé ou un autre chargement est parti entre-temps
    if (seq !== loadSeq || S.live) return;
    const lines = (d.content || '').split('\n');
    out.replaceChildren(...lines.map(lineEl));
    if (!d.content) out.textContent = t('logs.none');
  } catch (e) {
    if (seq !== loadSeq || S.live) return;
    out.textContent = '';
    toastError(e);
  }
  out.scrollTop = out.scrollHeight;
}

const KEEPALIVE = '\u0000';        // message de maintien de connexion du serveur
const MAX_RECONNECTS = 5;

function appendLive(text) {
  const out = S.els.out;
  if (!out || text === KEEPALIVE) return;
  const stick = out.scrollTop + out.clientHeight >= out.scrollHeight - 24;
  out.append(lineEl(text));
  while (out.children.length > MAX_LINES) out.firstChild.remove();
  if (stick) out.scrollTop = out.scrollHeight;
}

/** Source du direct (journal systemd ou fichier ; logs Docker pour un conteneur).
 *  history = lignes déjà écrites à renvoyer : 50 au départ, 0 à la reconnexion. */
function liveQuery(history) {
  const src = isDocker(store.instances[S.iid]) ? 'docker' : S.source;
  return `?source=${encodeURIComponent(src)}&history=${history}`;
}

/**
 * Direct robuste derrière un reverse proxy : WebSocket (wss:// en HTTPS), repli
 * sur SSE s'il ne s'ouvre jamais, reconnexion silencieuse si la connexion tombe
 * (sans renvoyer les lignes déjà affichées), abandon après quelques échecs.
 */
function createLive(iid) {
  const live = { stopped: false, transport: 'WebSocket' in window ? 'websocket' : 'sse', conn: null, failures: 0 };

  const retry = () => {
    if (live.stopped || S.live !== live) return;
    live.failures += 1;
    if (live.failures > MAX_RECONNECTS) { stopLive(); toast(t('logs.liveEnded'), 'info'); return; }
    // Déjà connecté une fois : les lignes précédentes sont affichées, on ne les redemande pas
    setTimeout(() => connect(live.everOpened ? 0 : 50), Math.min(8000, 500 * 2 ** live.failures));
  };

  const connect = (history) => {
    if (live.stopped) return;
    const query = liveQuery(history);
    if (live.transport === 'websocket') {
      const scheme = location.protocol === 'https:' ? 'wss' : 'ws';
      const ws = new WebSocket(`${scheme}://${location.host}/ws/logs/${encodeURIComponent(iid)}${query}`);
      let opened = false;
      live.conn = ws;
      ws.onopen = () => { opened = true; live.failures = 0; live.everOpened = true; };
      ws.onmessage = (e) => appendLive(e.data);
      ws.onclose = () => {
        if (live.stopped || live.conn !== ws) return;
        // Jamais ouvert dès le départ : le proxy bloque le WebSocket → SSE
        if (!opened && !live.everOpened) { live.transport = 'sse'; connect(history); return; }
        retry();
      };
    } else {
      const es = new EventSource(`/api/logs/stream/${encodeURIComponent(iid)}${query}`);
      live.conn = es;
      es.onopen = () => { live.failures = 0; live.everOpened = true; };
      es.onmessage = (e) => appendLive(e.data);
      es.onerror = () => {
        if (live.stopped || live.conn !== es) return;
        // EventSource se reconnecterait seul avec la même URL et renverrait
        // l'historique : on reprend la main pour repartir sans doublons.
        es.close();
        retry();
      };
    }
  };

  live.close = () => { live.stopped = true; if (live.conn) live.conn.close(); };
  live.start = () => connect(50);
  return live;
}

function startLive() {
  loadSeq += 1;   // un chargement encore en cours n'écrasera pas le direct
  S.els.out.replaceChildren();
  S.live = createLive(S.iid);
  S.live.start();
  S.els.livePill.hidden = false;
  S.els.liveBtn.replaceChildren(icon('stop'), t('logs.stopLive'));
}

function toggleLive() {
  if (S.live) stopLive();
  else startLive();
}

function stopLive() {
  if (S.live) { const live = S.live; S.live = null; live.close(); }
  if (S.els.livePill) {
    S.els.livePill.hidden = true;
    S.els.liveBtn.replaceChildren(icon('radio'), t('logs.live'));
  }
}
