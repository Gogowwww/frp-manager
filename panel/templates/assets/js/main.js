// ── Point d'entrée de l'application ───────────────────────────────────────

import { initI18n, applyI18n, t } from './i18n.js';
import { api } from './api.js';
import {
  store, can, usedFrpcIds, usedFrpsIds, detect, loadBoot, connectStatus, loadNicknames, loadManagerState, loadMe,
  checkUpdatesQuietly,
} from './store.js';
import { h, button, openDialog, callout } from './ui.js';
import { registerPage, startRouter, navigate } from './router.js';
import dashboard from './pages/dashboard.js';
import tunnels from './pages/tunnels.js';
import firewall from './pages/firewall.js';
import logs from './pages/logs.js';
import settings from './pages/settings.js';

// Configuration, notifications, mises à jour, comptes et connexion unique sont
// des sections de Réglages
const PAGES = [dashboard, tunnels, firewall, logs, settings];
const canOpen = (page) => !page.perm || can(page.perm);

const WELCOME_KEY = 'frpMgrWelcomeSeen';

function setupShell() {
  const app = document.getElementById('app');
  const closeNav = () => app.classList.remove('nav-open');

  document.getElementById('nav-toggle').addEventListener('click', () => app.classList.toggle('nav-open'));
  document.getElementById('sidebar-backdrop').addEventListener('click', closeNav);
  document.querySelectorAll('.nav-link[data-route]').forEach((a) => a.addEventListener('click', closeNav));
  document.addEventListener('keydown', (e) => { if (e.key === 'Escape') closeNav(); });

  document.getElementById('logout').addEventListener('click', async () => {
    try { await api('/api/logout', { method: 'POST' }); } finally { location.href = '/login'; }
  });

  // Navigation : pages permises au compte connecté ; Ports seulement s'il existe
  // un frpc réellement utilisé, Pare-feu seulement s'il existe un frps réellement
  // utilisé ; pastille de mise à jour
  // État calculé une fois par lien : il n'est réécrit que s'il change
  // State computed once per link: it is only rewritten when it changes
  const inUse = {
    ports: () => store.ready && usedFrpcIds().length > 0,
    firewall: () => store.ready && usedFrpsIds().length > 0,
  };
  store.subscribe(() => {
    PAGES.forEach((page) => {
      const link = document.querySelector(`[data-route="${page.id}"]`);
      const hidden = !canOpen(page) || (page.id in inUse && !inUse[page.id]());
      if (link && link.hidden !== hidden) link.hidden = hidden;
    });
    const who = document.getElementById('sidebar-user');
    who.hidden = !store.me;
    if (store.me) {
      who.textContent = store.me.display_name || store.me.username;
      who.title = t(`users.roles.${store.me.role}`);
    }
    const upd = store.updates.frp || store.updates.panel;
    document.getElementById('updates-badge').hidden = !upd;
  });
}

function onRouteChange(page) {
  document.querySelectorAll('.nav-link[data-route]').forEach((a) => {
    if (a.dataset.route === page.id) a.setAttribute('aria-current', 'page');
    else a.removeAttribute('aria-current');
  });
  document.title = `${page.title()} — FRP Manager`;
}

function showWelcome() {
  try { if (localStorage.getItem(WELCOME_KEY)) return; } catch { return; }
  openDialog({
    title: t('welcome.title'),
    build: ({ close }) => ({
      body: [
        callout({ type: 'warn', title: t('welcome.warnTitle'), text: t('welcome.warnText') }),
        h('ul', { class: 'welcome-list' },
          ['welcome.tip1', 'welcome.tip2', 'welcome.tip3', 'welcome.tip4'].map((k) => h('li', null, t(k)))),
      ],
      foot: [
        button(t('welcome.later'), { variant: 'ghost', onClick: () => close('later') }),
        !store.hasPassword && button(t('welcome.setPassword'), { onClick: () => { close('ok'); navigate('settings'); } }),
        button(t('welcome.ok'), { variant: 'primary', onClick: () => close('ok') }),
      ].filter(Boolean),
    }),
    onClose: (result) => {
      if (result === 'ok') { try { localStorage.setItem(WELCOME_KEY, '1'); } catch { /* ignoré */ } }
    },
  });
}

async function boot() {
  await initI18n();
  applyI18n();
  setupShell();

  PAGES.forEach(registerPage);

  // Détection d'abord : toutes les pages en dépendent. Elle arrive avec la
  // page ; sinon (ancienne page en cache), on la demande à l'API.
  if (!loadBoot() || !store.me) {
    await Promise.all([loadNicknames(), loadManagerState(), loadMe()]);
    try { await detect(); } catch { store.set({ ready: true }); }
  }

  await startRouter({ root: document.getElementById('view'), defaultRoute: 'dashboard', onRouteChange, canOpen });
  if (store.me?.owner) showWelcome();
  checkUpdatesQuietly();

  // État des instances poussé en direct (WebSocket), repli sur une vérification périodique
  if (can('instances.view')) connectStatus();
}

boot();
