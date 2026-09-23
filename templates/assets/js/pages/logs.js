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
    ], S.source, (v) => { S.source = v; load(); }, { label: t('logs.source') });

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

function appendLive(text) {
  const out = S.els.out;
  if (!out) return;
  const stick = out.scrollTop + out.clientHeight >= out.scrollHeight - 24;
  out.append(lineEl(text));
  while (out.children.length > MAX_LINES) out.firstChild.remove();
  if (stick) out.scrollTop = out.scrollHeight;
}

/** WebSocket (wss:// en HTTPS) ; s'il ne s'ouvre pas, repli sur le flux SSE. */
function openWebSocket(iid) {
  const scheme = location.protocol === 'https:' ? 'wss' : 'ws';
  const ws = new WebSocket(`${scheme}://${location.host}/ws/logs/${encodeURIComponent(iid)}`);
  let opened = false;
  const live = { close: () => ws.close(), transport: 'websocket' };
  ws.onopen = () => { opened = true; };
  ws.onmessage = (e) => appendLive(e.data);
  ws.onclose = () => {
    if (S.live !== live) return;               // arrêt demandé par l'utilisateur
    if (!opened) { S.live = openEventSource(iid); return; }
    stopLive();
    toast(t('logs.liveEnded'), 'info');
  };
  return live;
}

function openEventSource(iid) {
  const es = new EventSource(`/api/logs/stream/${encodeURIComponent(iid)}`);
  const live = { close: () => es.close(), transport: 'sse' };
  es.onmessage = (e) => appendLive(e.data);
  es.onerror = () => {
    if (S.live === live && es.readyState === EventSource.CLOSED) stopLive();
  };
  return live;
}

function toggleLive() {
  if (S.live) { stopLive(); return; }
  loadSeq += 1;   // un chargement encore en cours n'écrasera pas le direct
  S.els.out.replaceChildren();
  S.live = 'WebSocket' in window ? openWebSocket(S.iid) : openEventSource(S.iid);
  S.els.livePill.hidden = false;
  S.els.liveBtn.replaceChildren(icon('stop'), t('logs.stopLive'));
}

function stopLive() {
  if (S.live) { const live = S.live; S.live = null; live.close(); }
  if (S.els.livePill) {
    S.els.livePill.hidden = true;
    S.els.liveBtn.replaceChildren(icon('radio'), t('logs.live'));
  }
}
