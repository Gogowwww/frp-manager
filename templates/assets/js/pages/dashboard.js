// ── Tableau de bord : état et pilotage des services frp ───────────────────

import { t } from '../i18n.js';
import { api, post } from '../api.js';
import {
  store, instanceIds, displayName, technicalLabel, statusOf, isDocker, detect, refreshStatus, loadNicknames,
} from '../store.js';
import {
  h, icon, button, busy, toast, toastResult, toastError, confirmDialog, promptDialog, menuButton,
  switchControl, callout, emptyState, pageHeader,
} from '../ui.js';
import { navigate } from '../router.js';

let unsubscribe = null;
let lastSignature = '';

export default {
  id: 'dashboard',
  title: () => t('nav.dashboard'),

  mount(view) {
    const alerts = h('div', { class: 'form-stack' });
    const grid = h('div', { class: 'svc-grid' });
    const detectBtn = button(t('dashboard.redetect'), {
      iconName: 'refresh',
      onClick: () => busy(detectBtn, async () => {
        try { await detect(); toast(t('dashboard.detected'), 'success', 2000); } catch (e) { toastError(e); }
      }),
    });

    view.append(h('div', { class: 'page' },
      pageHeader({ title: t('dashboard.title'), description: t('dashboard.description'), actions: [detectBtn] }),
      alerts,
      grid));

    const render = () => {
      const signature = JSON.stringify([store.instances, store.nicknames, store.hasPassword, store.updates, store.inDocker, store.ready]);
      if (signature === lastSignature) return;
      lastSignature = signature;
      renderAlerts(alerts);
      renderGrid(grid);
    };
    lastSignature = '';
    unsubscribe = store.subscribe(render);
    render();
  },

  unmount() {
    if (unsubscribe) unsubscribe();
    unsubscribe = null;
  },
};

function renderAlerts(host) {
  const items = [];
  if (!store.hasPassword) {
    items.push(callout({
      type: 'danger', title: t('dashboard.noPasswordTitle'), text: t('dashboard.noPasswordText'),
      actions: [button(t('dashboard.setPassword'), { variant: 'primary', size: 'sm', onClick: () => navigate('settings') })],
    }));
  }
  if (store.updates.frp || store.updates.panel) {
    const what = store.updates.frp && store.updates.panel ? 'both' : store.updates.frp ? 'frp' : 'panel';
    items.push(callout({
      type: 'info', iconName: 'download', title: t(`dashboard.update.${what}`),
      actions: [button(t('dashboard.seeUpdates'), { size: 'sm', onClick: () => navigate('updates') })],
    }));
  }
  if (store.inDocker) {
    items.push(callout({ type: 'warn', compact: true, iconName: 'box', title: t('dashboard.dockerTitle'), text: t('dashboard.dockerText') }));
  }
  host.replaceChildren(...items);
  host.hidden = !items.length;
}

function renderGrid(grid) {
  if (!store.ready) {
    grid.replaceChildren(...[1, 2].map(() => h('div', { class: 'skeleton', style: { height: '220px' } })));
    return;
  }
  const ids = instanceIds();
  if (!ids.length) {
    grid.replaceChildren(h('div', { class: 'card', style: { gridColumn: '1 / -1' } }, emptyState({
      iconName: 'server',
      title: t('dashboard.emptyTitle'),
      text: store.inDocker ? t('dashboard.emptyTextDocker') : t('dashboard.emptyText'),
      actions: store.inDocker ? [] : [button(t('dashboard.installFrp'), { variant: 'primary', iconName: 'download', onClick: () => navigate('updates') })],
    })));
    return;
  }
  grid.replaceChildren(...ids.map(serviceCard));
}

function serviceCard(iid) {
  const inst = store.instances[iid];
  const docker = isDocker(inst);
  const status = statusOf(inst);
  const running = status === 'running';

  const statusBadge = {
    running: h('span', { class: 'badge badge-success' }, h('span', { class: 'dot dot-pulse' }), t('status.running')),
    stopped: h('span', { class: 'badge badge-danger' }, h('span', { class: 'dot' }), t('status.stopped')),
    missing: h('span', { class: 'badge' }, h('span', { class: 'dot' }), t('status.missing')),
  }[status];

  const iconName = docker ? 'box' : inst.type === 'frps' ? 'server' : 'laptop';

  // ── Détails ──
  const details = [];
  if (docker) {
    details.push(['dashboard.fields.image', (inst.image || '—').split('/').pop(), inst.image]);
    details.push(['dashboard.fields.container', inst.container_name || iid]);
    if (inst.network_mode) details.push(['dashboard.fields.network', inst.network_mode]);
  } else {
    const cfg = inst.config_path || '';
    details.push(['dashboard.fields.config', cfg ? cfg.replace('/etc/frp/', '') : t('dashboard.noConfig'), cfg, !inst.config_exists]);
    details.push(['dashboard.fields.service', inst.service]);
  }
  const kv = h('dl', { class: 'kv' }, details.flatMap(([label, value, title, bad]) => [
    h('dt', null, t(label)),
    h('dd', { class: 'mono', title: title || value, style: bad ? { color: 'var(--danger-text)' } : null }, value || '—'),
  ]));

  const body = [kv];
  if (status === 'missing') {
    body.unshift(callout({ type: 'warn', text: t('dashboard.binaryMissing', { type: inst.type }) }));
  }

  // Démarrage automatique (systemd uniquement)
  if (!docker && status !== 'missing') {
    const sw = switchControl({
      checked: !!inst.status.enabled,
      label: t('dashboard.autostart'),
      onChange: async (on) => {
        sw.input.disabled = true;
        try {
          const d = await post(`/api/service/${encodeURIComponent(iid)}/${on ? 'enable' : 'disable'}`);
          toastResult(d, t(on ? 'dashboard.autostartOn' : 'dashboard.autostartOff'));
          if (!d.ok) sw.input.checked = !on;
          await refreshStatus();
        } catch (e) { sw.input.checked = !on; toastError(e); }
        sw.input.disabled = false;
      },
    });
    body.push(h('div', { class: 'switch-row' },
      h('div', { class: 'switch-text' },
        h('div', { class: 'switch-label' }, t('dashboard.autostart')),
        h('div', { class: 'switch-desc' }, t('dashboard.autostartHint'))),
      sw));
  }

  // ── Actions ──
  const actions = [];
  if (status !== 'missing') {
    const primary = running
      ? button(t('actions.stop'), { iconName: 'stop', onClick: () => stopService(iid, primary) })
      : button(t('actions.start'), { variant: 'primary', iconName: 'play', onClick: () => serviceAction(iid, 'start', primary) });
    const restart = button(t('actions.restart'), { iconName: 'refresh', onClick: () => serviceAction(iid, 'restart', restart) });
    actions.push(primary, restart);
  }
  actions.push(h('span', { class: 'spacer' }));
  if (inst.type === 'frpc') {
    actions.push(button(t('nav.tunnels'), { variant: 'ghost', size: 'sm', iconName: 'tunnels', onClick: () => navigate('tunnels', { iid }) }));
  }

  const menuItems = [
    { label: t('dashboard.rename'), icon: 'edit', onSelect: () => rename(iid) },
    { label: t('dashboard.viewLogs'), icon: 'logs', onSelect: () => navigate('logs', { iid }) },
  ];
  if (!docker) {
    menuItems.push({ label: t('dashboard.editConfig'), icon: 'config', onSelect: () => navigate('config', { iid }) });
    if (status !== 'missing') {
      menuItems.push('sep', { label: t('dashboard.reload'), icon: 'refresh', onSelect: () => serviceAction(iid, 'reload') });
    }
  }

  return h('article', { class: `card svc${running ? ' is-running' : ''}`, 'aria-label': displayName(iid) },
    h('div', { class: 'svc-head' },
      h('div', { class: 'svc-icon' }, icon(iconName, 'icon-lg')),
      h('div', { class: 'svc-title' },
        h('h2', { class: 'svc-name', title: displayName(iid) }, displayName(iid)),
        h('div', { class: 'svc-meta' }, statusBadge, h('p', { class: 'svc-sub', title: technicalLabel(iid) }, technicalLabel(iid)))),
      menuButton(menuItems)),
    h('div', { class: 'svc-body' }, body),
    h('div', { class: 'svc-actions' }, actions));
}

async function serviceAction(iid, action, btn) {
  await busy(btn, async () => {
    try {
      const d = await post(`/api/service/${encodeURIComponent(iid)}/${action}`);
      toastResult(d, t(`actions.done.${action}`, { name: displayName(iid) }));
    } catch (e) { toastError(e); }
    setTimeout(() => refreshStatus().catch(() => {}), 700);
  });
}

async function stopService(iid, btn) {
  const inst = store.instances[iid];
  const ok = await confirmDialog({
    title: t('dashboard.stopTitle', { name: displayName(iid) }),
    message: t(inst.type === 'frpc' ? 'dashboard.stopTextClient' : 'dashboard.stopTextServer'),
    confirmLabel: t('actions.stop'),
    danger: true,
  });
  if (ok) await serviceAction(iid, 'stop', btn);
}

async function rename(iid) {
  const value = await promptDialog({
    title: t('dashboard.renameTitle'),
    label: t('dashboard.renameLabel'),
    hint: t('dashboard.renameHint', { id: iid }),
    value: store.nicknames[iid] || '',
    placeholder: t('dashboard.renamePlaceholder'),
    maxLength: 64,
  });
  if (value === undefined) return;
  try {
    const d = await api(`/api/nickname/${encodeURIComponent(iid)}`, { method: 'POST', body: { nickname: value } });
    toastResult(d, t('dashboard.renamed'));
    if (d.ok) {
      await loadNicknames();
      store.set({});
    }
  } catch (e) { toastError(e); }
}
