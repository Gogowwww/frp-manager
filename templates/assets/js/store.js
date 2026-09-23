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

export async function refreshStatus() {
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
