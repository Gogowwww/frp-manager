// ── Mises à jour : frp (binaires) et panel FRP Manager ────────────────────

import { t } from '../i18n.js';
import { api } from '../api.js';
import { store, refreshStatus } from '../store.js';
import {
  h, icon, button, busy, toast, toastError, field, input, callout, pageHeader, card, confirmDialog, copyButton,
} from '../ui.js';

const DOCKER_PULL = 'docker pull ghcr.io/gogowwww/frp-manager:latest';
const timers = new Set();
const later = (fn, ms) => { const id = setTimeout(() => { timers.delete(id); fn(); }, ms); timers.add(id); };

export default {
  id: 'updates',
  title: () => t('nav.updates'),

  mount(view) {
    const page = h('div', { class: 'page' },
      pageHeader({ title: t('updates.title'), description: t('updates.description') }));
    view.append(page);

    if (store.inDocker) {
      page.append(callout({ type: 'warn', compact: true, iconName: 'box', title: t('updates.dockerTitle'), text: t('updates.dockerText') }));
    } else {
      page.append(frpCard(), manualCard(), connectivityCard());
    }
    page.append(panelCard());
  },

  unmount() {
    timers.forEach(clearTimeout);
    timers.clear();
  },
};

// ── Blocs communs ──────────────────────────────────────────────────────────

function versionCells() {
  const cell = (label) => {
    const dd = h('dd', null, '—');
    return { el: h('div', { class: 'version-cell' }, h('dt', null, label), dd), dd };
  };
  const installed = cell(t('updates.installed'));
  const latest = cell(t('updates.latest'));
  const status = cell(t('updates.status'));
  status.dd.style.fontFamily = 'var(--font-sans)';
  status.dd.style.fontSize = '14px';
  return { installed, latest, status, el: h('dl', { class: 'versions' }, installed.el, latest.el, status.el) };
}

function setStatus(cell, kind, text) {
  cell.dd.replaceChildren(h('span', { class: `badge badge-${kind}` }, text));
}

function logClass(line) {
  if (line.includes('[OK]')) return 'log-ok';
  if (line.includes('[ERROR]')) return 'log-error';
  if (line.includes('[WARN]')) return 'log-warn';
  return '';
}

function renderLog(term, lines) {
  term.replaceChildren(...lines.map((l) => h('span', { class: `log-line ${logClass(l)}`.trim() }, `${l}\n`)));
  term.scrollTop = term.scrollHeight;
}

// ── frp ────────────────────────────────────────────────────────────────────

let frpLog = null;

function frpCard() {
  const cells = versionCells();
  const term = h('pre', { class: 'term', 'data-empty': t('updates.logEmpty'), 'aria-live': 'polite' });
  const logWrap = h('div', { class: 'field', hidden: true }, h('span', { class: 'field-label' }, t('updates.log')), term);
  frpLog = { term, wrap: logWrap };

  const checkBtn = button(t('updates.check'), { iconName: 'refresh', onClick: () => busy(checkBtn, () => check(true)) });
  const installBtn = button(t('updates.install'), { variant: 'primary', iconName: 'download', onClick: install });
  installBtn.hidden = true;

  /** fresh : redemander à GitHub (bouton « Vérifier »), sinon réponse gardée 10 min. */
  async function check(fresh = false) {
    cells.installed.dd.textContent = '…';
    cells.latest.dd.textContent = '…';
    setStatus(cells.status, '', t('updates.checking'));
    try {
      const d = await api(`/api/update/check${fresh ? '?fresh=1' : ''}`);
      if (!d.ok) {
        cells.installed.dd.textContent = '—';
        cells.latest.dd.textContent = '—';
        setStatus(cells.status, 'danger', d.msg || t('updates.unreachable'));
        return;
      }
      cells.installed.dd.textContent = d.installed ? `v${d.installed}` : t('updates.notInstalled');
      cells.latest.dd.textContent = `v${d.latest}`;
      cells.latest.dd.title = t('updates.source', { source: d.source || '—' });
      if (d.update_available) {
        setStatus(cells.status, 'warn', d.installed ? t('updates.available') : t('updates.notInstalled'));
        installBtn.hidden = false;
        installBtn.replaceChildren(icon('download'), d.installed ? t('updates.installVersion', { v: d.latest }) : t('updates.installFrp', { v: d.latest }));
      } else {
        setStatus(cells.status, 'success', t('updates.upToDate'));
        installBtn.hidden = true;
      }
      store.set({ updates: { ...store.updates, frp: !!(d.update_available && d.installed) } });
    } catch (e) {
      setStatus(cells.status, 'danger', e.message);
    }
  }

  async function install() {
    const ok = await confirmDialog({ title: t('updates.installConfirmTitle'), message: t('updates.installConfirmText'), confirmLabel: t('updates.install') });
    if (!ok) return;
    await busy(installBtn, async () => {
      try {
        const d = await api('/api/update/install', { method: 'POST' });
        if (!d.ok) { toast(d.msg, 'error'); return; }
        toast(t('updates.installing'), 'info');
        await pollFrpLog(() => check(true));
      } catch (e) { toastError(e); }
    });
  }

  later(check, 0);
  return card({
    title: t('updates.frpTitle'), description: t('updates.frpDesc'),
    body: h('div', { class: 'form-stack' }, cells.el, logWrap),
    foot: [checkBtn, installBtn],
  });
}

/** Suit le journal d'installation frp jusqu'à [OK] / [ERROR]. */
function pollFrpLog(onDone) {
  frpLog.wrap.hidden = false;
  return new Promise((resolve) => {
    const tick = async () => {
      if (!frpLog || !frpLog.term.isConnected) { resolve(); return; }
      try {
        const d = await api('/api/update/log');
        const lines = d.lines || [];
        renderLog(frpLog.term, lines);
        const last = lines[lines.length - 1] || '';
        if (last.includes('[OK]') || last.includes('[ERROR]')) {
          toast(last.includes('[OK]') ? t('updates.installDone') : t('updates.installFailed'), last.includes('[OK]') ? 'success' : 'error');
          refreshStatus().catch(() => {});
          if (onDone) onDone();
          resolve();
          return;
        }
      } catch { /* on réessaie */ }
      later(tick, 1100);
    };
    tick();
  });
}

function manualCard() {
  const version = input({ placeholder: '0.68.1', mono: true, style: { maxWidth: '160px' } });
  const file = h('input', { type: 'file', class: 'input', accept: '.tar.gz,.gz' });
  const uploadBtn = button(t('updates.manualInstall'), { variant: 'primary', iconName: 'upload', onClick: () => busy(uploadBtn, upload) });

  async function upload() {
    const f = file.files[0];
    const v = version.value.trim();
    if (!f) { toast(t('updates.manualNoFile'), 'error'); return; }
    if (!v) { toast(t('updates.manualNoVersion'), 'error'); version.focus(); return; }
    const form = new FormData();
    form.append('file', f);
    form.append('version', v);
    try {
      const d = await api('/api/update/upload', { method: 'POST', form });
      if (!d.ok) { toast(d.msg, 'error'); return; }
      toast(t('updates.installing'), 'info');
      await pollFrpLog();
    } catch (e) { toastError(e); }
  }

  const releases = h('a', { href: 'https://github.com/fatedier/frp/releases', target: '_blank', rel: 'noopener' }, 'github.com/fatedier/frp/releases');
  return h('details', { class: 'card disclosure' },
    h('summary', null, icon('chevron-down'), t('updates.manualTitle'), h('span', { class: 'summary-hint' }, t('updates.manualHint'))),
    h('div', { class: 'disclosure-body' },
      h('p', { class: 'field-hint', style: { fontSize: '13.5px' } }, t('updates.manualText1'), ' ', releases, t('updates.manualText2')),
      h('div', { class: 'form-grid', style: { gridTemplateColumns: 'minmax(0,160px) minmax(0,1fr)' } },
        field({ label: t('updates.manualVersion'), control: version }),
        field({ label: t('updates.manualFile'), control: file })),
      h('div', null, uploadBtn)));
}

function connectivityCard() {
  const grid = h('div', { class: 'source-grid' });
  const testBtn = button(t('updates.connTest'), { iconName: 'globe', onClick: () => busy(testBtn, test) });

  async function test() {
    grid.replaceChildren(h('p', { class: 'muted' }, t('updates.connTesting')));
    try {
      const d = await api('/api/connectivity');
      const entries = Object.entries(d.sources || {});
      if (!entries.length) { grid.replaceChildren(h('p', { class: 'muted' }, t('updates.connNone'))); return; }
      grid.replaceChildren(...entries.map(([name, s]) => h('div', { class: `source ${s.ok ? 'ok' : 'ko'}` },
        icon(s.ok ? 'check-circle' : 'alert'),
        h('div', { style: { minWidth: '0' } },
          h('div', { class: 'source-name' }, name),
          h('div', { class: 'source-meta' }, s.ok ? s.version : (s.error || t('updates.unreachable')))))));
    } catch (e) { grid.replaceChildren(); toastError(e); }
  }

  return h('details', { class: 'card disclosure' },
    h('summary', null, icon('chevron-down'), t('updates.connTitle'), h('span', { class: 'summary-hint' }, t('updates.connHint'))),
    h('div', { class: 'disclosure-body' }, h('div', null, testBtn), grid));
}

// ── Panel ──────────────────────────────────────────────────────────────────

function panelCard() {
  const cells = versionCells();
  const term = h('pre', { class: 'term', 'data-empty': t('updates.logEmpty'), 'aria-live': 'polite' });
  const logWrap = h('div', { class: 'field', hidden: true }, h('span', { class: 'field-label' }, t('updates.log')), term);
  const extra = h('div', { class: 'form-stack' });

  const checkBtn = button(t('updates.check'), { iconName: 'refresh', onClick: () => busy(checkBtn, () => check(true)) });
  const updateBtn = button(t('updates.panelUpdate'), { variant: 'primary', iconName: 'download', onClick: update });
  const releaseLink = h('a', { class: 'btn btn-ghost', target: '_blank', rel: 'noopener', hidden: true }, icon('external'), t('updates.releaseNotes'));
  updateBtn.hidden = true;

  /** fresh : redemander à GitHub (bouton « Vérifier »), sinon réponse gardée 10 min. */
  async function check(fresh = false) {
    cells.installed.dd.textContent = '…';
    cells.latest.dd.textContent = '…';
    setStatus(cells.status, '', t('updates.checking'));
    try {
      const d = await api(`/api/panel/version${fresh ? '?fresh=1' : ''}`);
      if (!d.ok) { setStatus(cells.status, 'danger', t('errors.generic')); return; }
      cells.installed.dd.textContent = `v${d.current}`;
      extra.replaceChildren();
      if (!d.repo_configured) {
        cells.latest.dd.textContent = '—';
        setStatus(cells.status, '', t('updates.repoMissing'));
        return;
      }
      if (!d.latest) {
        cells.latest.dd.textContent = '—';
        setStatus(cells.status, '', t('updates.unreachable'));
        return;
      }
      cells.latest.dd.textContent = `v${d.latest}`;
      if (d.prerelease && d.in_docker) {
        // Pré-release sous Docker : la mise à jour passe par l'image, rien à proposer ici
        setStatus(cells.status, 'info', t('updates.prerelease'));
        extra.append(callout({ type: 'info', compact: true, text: t('updates.prereleaseDockerText') }));
        releaseLink.hidden = true;
        updateBtn.hidden = true;
      } else if (d.update_available) {
        // Installation en pré-release : elle suit aussi les pré-releases suivantes
        if (d.prerelease) extra.append(callout({ type: 'info', compact: true, text: t('updates.prereleaseChannel') }));
        setStatus(cells.status, 'warn', t(d.prerelease ? 'updates.prereleaseAvailable' : 'updates.available'));
        releaseLink.href = d.release_url || `https://github.com/${d.repo}/releases`;
        releaseLink.hidden = false;
        updateBtn.hidden = !!d.in_docker;
        if (d.in_docker) {
          extra.append(
            callout({ type: 'warn', compact: true, iconName: 'box', title: t('updates.panelDockerTitle'), text: t('updates.panelDockerText') }),
            h('div', { class: 'code-block' }, h('code', null, DOCKER_PULL), copyButton(DOCKER_PULL)));
        }
      } else {
        if (d.prerelease) extra.append(callout({ type: 'info', compact: true, text: t('updates.prereleaseChannel') }));
        setStatus(cells.status, d.prerelease ? 'info' : 'success', t(d.prerelease ? 'updates.prereleaseUpToDate' : 'updates.upToDate'));
        releaseLink.hidden = true;
        updateBtn.hidden = true;
      }
      store.set({ updates: { ...store.updates, panel: !!d.update_available } });
    } catch (e) { setStatus(cells.status, 'danger', e.message); }
  }

  async function update() {
    const ok = await confirmDialog({ title: t('updates.panelConfirmTitle'), message: t('updates.panelConfirmText'), confirmLabel: t('updates.panelUpdate') });
    if (!ok) return;
    updateBtn.setAttribute('aria-busy', 'true');
    updateBtn.disabled = true;
    try {
      const d = await api('/api/panel/update', { method: 'POST' });
      if (!d.ok) {
        toast(d.msg, 'error');
        updateBtn.removeAttribute('aria-busy');
        updateBtn.disabled = false;
        return;
      }
      toast(t('updates.panelUpdating'), 'info');
      logWrap.hidden = false;
      const tick = async () => {
        if (!term.isConnected) return;
        try {
          const r = await api('/api/panel/update/log');
          const lines = r.lines || [];
          renderLog(term, lines);
          const last = lines[lines.length - 1] || '';
          if (last.includes('Redémarrage')) {
            toast(t('updates.panelRestarting'), 'info');
            later(() => location.reload(), 4000);
            return;
          }
          if (last.includes('[ERROR]')) {
            updateBtn.removeAttribute('aria-busy');
            updateBtn.disabled = false;
            return;
          }
        } catch { /* le panel redémarre peut-être déjà */ }
        later(tick, 1000);
      };
      tick();
    } catch (e) {
      toastError(e);
      updateBtn.removeAttribute('aria-busy');
      updateBtn.disabled = false;
    }
  }

  later(check, 0);
  return card({
    title: t('updates.panelTitle'), description: t('updates.panelDesc'),
    body: h('div', { class: 'form-stack' }, cells.el, extra, logWrap),
    foot: [releaseLink, checkBtn, updateBtn],
  });
}
