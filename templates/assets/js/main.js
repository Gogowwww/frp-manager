// ── Point d'entrée de l'application ───────────────────────────────────────

import { initI18n, applyI18n, t } from './i18n.js';
import { api } from './api.js';
import {
  store, frpcIds, frpsIds, detect, connectStatus, loadNicknames, loadManagerState, checkUpdatesQuietly,
} from './store.js';
import { h, button, openDialog, callout } from './ui.js';
import { registerPage, startRouter, navigate } from './router.js';
import dashboard from './pages/dashboard.js';
import tunnels from './pages/tunnels.js';
import firewall from './pages/firewall.js';
import config from './pages/config.js';
import logs from './pages/logs.js';
import updates from './pages/updates.js';
import settings from './pages/settings.js';

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

  // Navigation : Ports seulement s'il existe un frpc, Pare-feu seulement s'il
  // existe un frps ; pastille de mise à jour
  store.subscribe(() => {
    document.querySelector('[data-route="tunnels"]').hidden = store.ready && !frpcIds().length;
    document.querySelector('[data-route="firewall"]').hidden = !store.ready || !frpsIds().length;
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

  [dashboard, tunnels, firewall, config, logs, updates, settings].forEach(registerPage);

  // Détection d'abord : toutes les pages en dépendent
  await Promise.all([loadNicknames(), loadManagerState()]);
  try { await detect(); } catch { store.set({ ready: true }); }

  await startRouter({ root: document.getElementById('view'), defaultRoute: 'dashboard', onRouteChange });
  showWelcome();
  checkUpdatesQuietly();

  // État des instances poussé en direct (WebSocket), repli sur une vérification périodique
  connectStatus();
}

boot();
