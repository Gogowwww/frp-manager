// ── Tableau de bord : état et pilotage des services frp ───────────────────

import { t } from '../i18n.js';
import { api, post } from '../api.js';
import {
  store, can, instanceIds, displayName, technicalLabel, statusOf, isDocker, detect, refreshStatus, loadNicknames,
} from '../store.js';
import {
  h, icon, button, busy, toast, toastResult, toastError, confirmDialog, promptDialog, menuButton,
  switchControl, switchRow, openDialog, callout, emptyState, pageHeader,
} from '../ui.js';
import { navigate } from '../router.js';
import { renderLog } from './updates.js';

let unsubscribe = null;
let lastSignature = '';

export default {
  id: 'dashboard',
  title: () => t('nav.dashboard'),
  perm: 'instances.view',

  mount(view) {
    const alerts = h('div', { class: 'form-stack' });
    const grid = h('div', { class: 'svc-grid' });
    const detectBtn = button(t('dashboard.redetect'), {
      iconName: 'refresh',
      onClick: () => busy(detectBtn, async () => {
        try { await detect(); toast(t('dashboard.detected'), 'success', 2000); } catch (e) { toastError(e); }
      }),
    });

    // Un bouton par service frps/frpc absent (supprimé depuis le tableau de bord)
    const reinstallSlot = h('div', { style: { display: 'contents' } });
    view.append(h('div', { class: 'page' },
      pageHeader({ title: t('dashboard.title'), description: t('dashboard.description'), actions: [reinstallSlot, detectBtn] }),
      alerts,
      grid));

    const render = () => {
      const signature = JSON.stringify([store.instances, store.nicknames, store.hasPassword, store.updates, store.inDocker, store.standalone, store.ready]);
      if (signature === lastSignature) return;
      lastSignature = signature;
      renderAlerts(alerts);
      renderGrid(grid);
      renderReinstall(reinstallSlot);
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
  if (!store.hasPassword && store.me?.owner) {
    items.push(callout({
      type: 'danger', title: t('dashboard.noPasswordTitle'), text: t('dashboard.noPasswordText'),
      actions: [button(t('dashboard.setPassword'), { variant: 'primary', size: 'sm', onClick: () => navigate('settings') })],
    }));
  }
  if ((store.updates.frp || store.updates.panel) && can('updates.view')) {
    const what = store.updates.frp && store.updates.panel ? 'both' : store.updates.frp ? 'frp' : 'panel';
    items.push(callout({
      type: 'info', iconName: 'download', title: t(`dashboard.update.${what}`),
      actions: [button(t('dashboard.seeUpdates'), { size: 'sm', onClick: () => navigate('settings', { s: 'updates' }) })],
    }));
  }
  if (store.standalone) {
    items.push(callout({ type: 'info', compact: true, iconName: 'box', title: t('dashboard.standaloneTitle'), text: t('dashboard.standaloneText') }));
  } else if (store.inDocker) {
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
      text: t(store.standalone ? 'dashboard.emptyTextStandalone' : store.inDocker ? 'dashboard.emptyTextDocker' : 'dashboard.emptyText'),
      actions: store.inDocker || !can('updates.install') ? [] : [button(t('dashboard.installFrp'), { variant: 'primary', iconName: 'download', onClick: () => navigate('settings', { s: 'updates' }) })],
    })));
    return;
  }
  grid.replaceChildren(...ids.map(serviceCard));
}

function renderReinstall(host) {
  // Mode autonome : recréer une instance revient à recréer sa config
  // Standalone mode: creating an instance again means creating its config again
  const standalone = store.standalone;
  const missing = store.ready && (!store.inDocker || standalone) && can('updates.install')
    ? ['frps', 'frpc'].filter((type) => !instanceIds().some((iid) => {
      const inst = store.instances[iid];
      return inst.type === type && !isDocker(inst);
    }))
    : [];
  host.replaceChildren(...missing.map((type) => button(t('dashboard.reinstall', { type }), {
    iconName: 'plus', title: t(standalone ? 'dashboard.reinstallHintStandalone' : 'dashboard.reinstallHint', { type }), onClick: () => reinstall(type),
  })));
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
  if (!docker && status !== 'missing' && can('instances.control')) {
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
        h('div', { class: 'switch-desc' }, t(inst.source === 'standalone' ? 'dashboard.autostartHintStandalone' : 'dashboard.autostartHint'))),
      sw));
  }

  // ── Actions ──
  const actions = [];
  if (status !== 'missing' && can('instances.control')) {
    const primary = running
      ? button(t('actions.stop'), { iconName: 'stop', onClick: () => stopService(iid, primary) })
      : button(t('actions.start'), { variant: 'primary', iconName: 'play', onClick: () => serviceAction(iid, 'start', primary) });
    const restart = button(t('actions.restart'), { iconName: 'refresh', onClick: () => serviceAction(iid, 'restart', restart) });
    actions.push(primary, restart);
  }
  actions.push(h('span', { class: 'spacer' }));
  if (inst.type === 'frpc' && can('config.read')) {
    actions.push(button(t('nav.tunnels'), { variant: 'ghost', size: 'sm', iconName: 'tunnels', onClick: () => navigate('ports', { iid }) }));
  } else if (inst.type === 'frps' && can('firewall.view')) {
    actions.push(button(t('nav.firewall'), { variant: 'ghost', size: 'sm', iconName: 'shield', onClick: () => navigate('firewall') }));
  }

  // Menu limité à ce que le compte connecté a le droit de faire
  const menuItems = [];
  if (can('instances.control')) menuItems.push({ label: t('dashboard.rename'), icon: 'edit', onSelect: () => rename(iid) });
  if (can('logs.view')) menuItems.push({ label: t('dashboard.viewLogs'), icon: 'logs', onSelect: () => navigate('logs', { iid }) });
  if (!docker) {
    if (can('config.read')) menuItems.push({ label: t('dashboard.editConfig'), icon: 'config', onSelect: () => navigate('settings', { s: 'config', iid }) });
    // Lancé par le panel : « Recharger » serait un redémarrage, déjà proposé
    // Run by the panel: "Reload" would be a restart, already offered
    if (status !== 'missing' && can('instances.control') && inst.source !== 'standalone') {
      menuItems.push('sep', { label: t('dashboard.reload'), icon: 'refresh', onSelect: () => serviceAction(iid, 'reload') });
    }
  }
  if (can('instances.delete')) {
    menuItems.push('sep', { label: t('common.delete'), icon: 'trash', danger: true, onSelect: () => deleteInstance(iid) });
  }
  if (menuItems[0] === 'sep') menuItems.shift();

  return h('article', { class: `card svc${running ? ' is-running' : ''}`, 'aria-label': displayName(iid) },
    h('div', { class: 'svc-head' },
      h('div', { class: 'svc-icon' }, icon(iconName, 'icon-lg')),
      h('div', { class: 'svc-title' },
        h('h2', { class: 'svc-name', title: displayName(iid) }, displayName(iid)),
        h('div', { class: 'svc-meta' }, statusBadge, h('p', { class: 'svc-sub', title: technicalLabel(iid) }, technicalLabel(iid)))),
      menuItems.length ? menuButton(menuItems) : null),
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

async function deleteInstance(iid) {
  const inst = store.instances[iid];
  const docker = isDocker(inst);
  const name = displayName(iid);

  // Container : l'image et la config montée ne sont connues que de Docker
  let image = null;
  let configPath = docker ? null : (inst.config_exists ? inst.config_path : null);
  if (docker) {
    try {
      const info = await api(`/api/instance/${encodeURIComponent(iid)}/delete-info`);
      if (!info.ok) { toastResult(info); return; }
      ({ image, config_path: configPath } = info);
    } catch (e) { toastError(e); return; }
  }

  const result = await openDialog({
    title: t('dashboard.deleteTitle', { name }),
    size: 'sm',
    build: ({ close }) => {
      const body = [h('p', { style: { color: 'var(--text-2)' } },
        docker ? t('dashboard.deleteTextDocker', { container: inst.container_name || iid })
          : t(inst.source === 'standalone' ? 'dashboard.deleteTextStandalone' : 'dashboard.deleteTextService', { service: inst.service }))];
      let imageRow = null;
      if (image) {
        imageRow = switchRow({
          label: t('dashboard.deleteImage'),
          description: t('dashboard.deleteImageHint', { image }),
          checked: true,
        });
        body.push(imageRow);
      }
      let cfgRow = null;
      if (configPath) {
        cfgRow = switchRow({
          label: t('dashboard.deleteConfig'),
          description: t('dashboard.deleteConfigHint', { path: configPath }),
        });
        body.push(cfgRow);
      }
      body.push(callout({ type: 'danger', compact: true, text: t('dashboard.deleteWarning') }));
      return {
        body: [h('div', { class: 'form-stack' }, body)],
        foot: [
          button(t('common.cancel'), { onClick: () => close(undefined), autofocus: true }),
          button(t('common.delete'), {
            variant: 'danger', iconName: 'trash',
            onClick: () => close({
              delete_image: !!(imageRow && imageRow.input.checked),
              delete_config: !!(cfgRow && cfgRow.input.checked),
            }),
          }),
        ],
      };
    },
  }).result;
  if (!result) return;
  try {
    const d = await api(`/api/instance/${encodeURIComponent(iid)}`, { method: 'DELETE', body: result });
    toastResult(d, t('dashboard.deleted', { name }));
    if (d.ok) {
      // Retirer la carte tout de suite, sans attendre la nouvelle détection
      const { [iid]: _gone, ...rest } = store.instances;
      store.set({ instances: rest });
      await loadNicknames();
    }
  } catch (e) { toastError(e); }
  await detect().catch(() => {});
}

/** Recrée le service frps ou frpc supprimé (programme retéléchargé s'il manque). */
async function reinstall(type) {
  const ok = await confirmDialog({
    title: t('dashboard.reinstallTitle', { type }),
    message: t(store.standalone ? 'dashboard.reinstallTextStandalone' : 'dashboard.reinstallText', { type }),
    confirmLabel: t('dashboard.reinstall', { type }),
  });
  if (!ok) return;
  try {
    const d = await post(`/api/instance/reinstall/${type}`);
    if (!d.ok) { toastResult(d); return; }
  } catch (e) { toastError(e); return; }

  const term = h('pre', { class: 'term', 'data-empty': t('updates.logEmpty'), 'aria-live': 'polite' });
  openDialog({
    title: t('dashboard.reinstallingTitle', { type }),
    build: ({ close }) => ({ body: [term], foot: [button(t('common.close'), { onClick: () => close(undefined) })] }),
  });
  // Le suivi continue si la fenêtre est fermée : la fin est annoncée par un toast
  const tick = async () => {
    try {
      const r = await api('/api/update/log');
      renderLog(term, r.lines || []);
      if (!r.running) {
        toast(t(r.success ? 'dashboard.reinstalled' : 'updates.installFailed', { type }), r.success ? 'success' : 'error');
        await detect().catch(() => {});
        return;
      }
    } catch { /* on réessaie */ }
    setTimeout(tick, 1100);
  };
  tick();
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
