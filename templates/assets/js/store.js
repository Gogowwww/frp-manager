// ── État partagé ──────────────────────────────────────────────────────────
// Instances frp détectées, surnoms, mode Docker, état des mises à jour.
// Les pages s'abonnent via store.subscribe() et se redessinent au besoin.

import { api } from './api.js';
import { t } from './i18n.js';

const listeners = new Set();

export const store = {
  ready: false,
  instances: {},
  inDocker: false,
  nicknames: {},
  hasPassword: true,
  updates: { frp: false, panel: false },

  set(patch) {
    Object.assign(this, patch);
    listeners.forEach((fn) => fn(this));
  },
  subscribe(fn) {
    listeners.add(fn);
    return () => listeners.delete(fn);
  },
};

/** Identifiants triés : frps d'abord, puis ordre alphabétique. */
export function instanceIds(filter) {
  return Object.keys(store.instances)
    .filter((id) => !filter || filter(store.instances[id], id))
    .sort((a, b) => {
      const ta = store.instances[a].type;
      const tb = store.instances[b].type;
      if (ta !== tb) return ta === 'frps' ? -1 : 1;
      return a.localeCompare(b);
    });
}

export const isDocker = (inst) => inst && inst.source === 'docker';
export const frpcIds = () => instanceIds((i) => i.type === 'frpc');
export const frpsIds = () => instanceIds((i) => i.type === 'frps');

export function displayName(iid) {
  const inst = store.instances[iid];
  return store.nicknames[iid] || (isDocker(inst) ? inst.container_name || iid : iid);
}

/** Libellé technique court : « Client frpc, service systemd, v0.68.1 » */
export function technicalLabel(iid) {
  const inst = store.instances[iid] || {};
  const parts = [t(`instance.type.${inst.type === 'frps' ? 'frps' : 'frpc'}`)];
  parts.push(isDocker(inst) ? t('instance.source.docker') : t('instance.source.systemd'));
  if (inst.version) parts.push(`v${inst.version}`);
  if (store.nicknames[iid]) parts.unshift(iid);
  return parts.join(', ');
}

export function statusOf(inst) {
  if (!inst || (!inst.binary_found && !isDocker(inst))) return 'missing';
  return inst.status && inst.status.running ? 'running' : 'stopped';
}

export async function detect() {
  const d = await api('/api/detect');
  store.set({ instances: d.instances || {}, inDocker: !!d.in_docker, ready: true });
}

// ── État en direct : WebSocket /ws/status, repli sur /api/status ─────────
// Le serveur pousse l'état des instances dès qu'il change (et juste après une
// action). Sans WebSocket, on revient à une vérification toutes les 12 s et on
// retente la connexion avec un délai croissant.

const POLL_MS = 12000;
const MAX_FAILED_OPENINGS = 3;   // proxy qui refuse le WebSocket : on n'insiste pas
let statusSocket = null;
let statusRetries = 0;
let failedOpenings = 0;
let pollTimer = null;

const statusLive = () => !!statusSocket && statusSocket.readyState === WebSocket.OPEN;

function startPolling() {
  if (pollTimer) return;
  pollTimer = setInterval(() => {
    if (document.visibilityState === 'visible') refreshStatus().catch(() => {});
  }, POLL_MS);
}

function stopPolling() {
  clearInterval(pollTimer);
  pollTimer = null;
}

export function connectStatus() {
  if (!('WebSocket' in window)) { startPolling(); return; }
  const scheme = location.protocol === 'https:' ? 'wss' : 'ws';
  const ws = new WebSocket(`${scheme}://${location.host}/ws/status`);
  let opened = false;
  statusSocket = ws;
  ws.onopen = () => { opened = true; statusRetries = 0; failedOpenings = 0; stopPolling(); };
  ws.onmessage = (e) => {
    try {
      const d = JSON.parse(e.data);   // {"keepalive": true} : maintien de connexion, rien à faire
      if (d.instances) store.set({ instances: d.instances });
    } catch { /* message illisible : ignoré */ }
  };
  ws.onclose = () => {
    if (statusSocket !== ws) return;
    statusSocket = null;
    startPolling();
    if (!opened) {
      // Jamais ouvert : le reverse proxy ne laisse sans doute pas passer le
      // WebSocket. Après quelques essais, on reste sur la vérification périodique.
      failedOpenings += 1;
      if (failedOpenings >= MAX_FAILED_OPENINGS) return;
    }
    statusRetries += 1;
    setTimeout(connectStatus, Math.min(30000, 1000 * 2 ** (statusRetries - 1)));
  };
}

/** Rafraîchit l'état à la demande ; inutile quand le WebSocket le pousse déjà. */
export async function refreshStatus() {
  if (statusLive()) return;
  const d = await api('/api/status');
  if (d.ok) store.set({ instances: d.instances || {} });
}

export async function loadNicknames() {
  try {
    const d = await api('/api/nicknames');
    if (d.ok) store.nicknames = d.nicknames || {};
  } catch { /* non bloquant */ }
}

export async function loadManagerState() {
  try {
    const d = await api('/api/manager/config');
    if (d.ok) store.set({ hasPassword: !!d.config.has_password });
  } catch { /* non bloquant */ }
}

/** Vérifie en arrière-plan les mises à jour (pastille dans la navigation). */
export async function checkUpdatesQuietly() {
  const updates = { ...store.updates };
  const tasks = [
    api('/api/panel/version').then((d) => { updates.panel = !!(d.ok && d.update_available); }),
  ];
  if (!store.inDocker) {
    tasks.push(api('/api/update/check').then((d) => { updates.frp = !!(d.ok && d.update_available && d.installed); }));
  }
  await Promise.allSettled(tasks);
  store.set({ updates });
}

/** Attend qu'une instance repasse « running » après un redémarrage. */
export async function waitRunning(iid, { attempts = 15, interval = 1000 } = {}) {
  if (statusLive()) {
    // État poussé par /ws/status : on écoute au lieu d'interroger. On ignore la
    // première seconde, où l'instance peut encore apparaître « running » avant l'arrêt.
    await new Promise((r) => setTimeout(r, interval));
    if (store.instances[iid]?.status?.running) return true;
    return new Promise((resolve) => {
      const timer = setTimeout(() => { unsubscribe(); resolve(false); }, attempts * interval);
      const unsubscribe = store.subscribe((s) => {
        if (s.instances[iid]?.status?.running) { clearTimeout(timer); unsubscribe(); resolve(true); }
      });
    });
  }
  for (let i = 0; i < attempts; i += 1) {
    await new Promise((r) => setTimeout(r, interval));
    try {
      const d = await api('/api/status');
      if (d.ok) {
        store.set({ instances: d.instances || {} });
        if (d.instances?.[iid]?.status?.running) return true;
      }
    } catch { /* la connexion peut sauter pendant le redémarrage */ }
  }
  return false;
}
