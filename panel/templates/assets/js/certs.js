// ── Certificats des sites : dialogues partagés ─────────────────────────────
// Import d'une paire PEM, et obtention Let’s Encrypt (DNS-01 par l’API
// Cloudflare, ou en créant les enregistrements TXT chez n’importe quel
// fournisseur DNS). Utilisés par Réglages → Proxy inverse HTTPS et par
// l’éditeur de tunnel HTTPS.
// Site certificates: shared dialogs. PEM pair import, and Let’s Encrypt
// issuance (DNS-01 through the Cloudflare API, or by creating the TXT records
// at any DNS provider). Used by Settings → HTTPS reverse proxy and by the
// HTTPS tunnel editor.

import { t } from './i18n.js';
import { api } from './api.js';
import {
  h, button, busy, toast, toastResult, toastError, openDialog, field, setFieldError,
  input, secretInput, switchRow, segmented, callout,
} from './ui.js';

/** Champ fichier qui remplit la zone de texte PEM à côté. */
function filePicker(area) {
  const file = h('input', { type: 'file', accept: '.pem,.crt,.cer,.key,.txt', hidden: true });
  file.addEventListener('change', async () => {
    const f = file.files && file.files[0];
    if (!f) return;
    try { area.value = await f.text(); setFieldError(area, ''); }
    catch { toast(t('settings.network.siteCertFileRead'), 'error'); }
  });
  return h('div', { class: 'row-actions' },
    button(t('settings.network.siteCertChooseFile'), { size: 'sm', iconName: 'upload', onClick: () => file.click() }), file);
}

/** Import d’un certificat et de sa clé privée. onSaved(id). */
export function importCertDialog({ onSaved } = {}) {
  const name = input({ placeholder: t('settings.network.siteCertNamePlaceholder'), mono: true });
  const certArea = h('textarea', { class: 'input fw-textarea', rows: 7, spellcheck: 'false', placeholder: '-----BEGIN CERTIFICATE-----' });
  const keyArea = h('textarea', { class: 'input fw-textarea', rows: 7, spellcheck: 'false', placeholder: '-----BEGIN PRIVATE KEY-----' });
  const saveBtn = button(t('settings.network.siteCertAdd'), { variant: 'primary', type: 'submit', form: 'site-cert-form' });

  openDialog({
    title: t('settings.network.siteCertImportTitle'),
    description: t('settings.network.siteCertImportDesc'),
    build: ({ close }) => {
      async function submit(e) {
        e.preventDefault();
        const cert = certArea.value.trim();
        const key = keyArea.value.trim();
        setFieldError(certArea, cert ? '' : t('settings.network.siteCertMissing'));
        setFieldError(keyArea, key ? '' : t('settings.network.siteCertMissing'));
        if (!cert || !key) return;
        await busy(saveBtn, async () => {
          try {
            const d = await api('/api/manager/certs', {
              method: 'POST',
              body: { name: name.value.trim(), cert, key },
            });
            if (!d.ok) { toastResult(d); return; }
            toast(d.msg || t('settings.network.siteCertSaved'), 'success');
            close(d.id);
          } catch (err) { toastError(err); }
        });
      }
      return {
        body: [h('form', { id: 'site-cert-form', class: 'form-stack', onSubmit: submit, novalidate: true },
          field({ label: t('settings.network.siteCertName'), hint: t('settings.network.siteCertNameHint'), control: name }),
          field({ label: t('settings.network.siteCertCertLabel'), hint: t('settings.network.siteCertCertHint'), control: certArea }),
          filePicker(certArea),
          field({ label: t('settings.network.siteCertKeyLabel'), hint: t('settings.network.siteCertKeyHint'), control: keyArea }),
          filePicker(keyArea))],
        foot: [button(t('common.cancel'), { onClick: () => close() }), saveBtn],
      };
    },
    onClose: (id) => { if (typeof id === 'string') onSaved?.(id); },
  });
}

/**
 * Obtention Let’s Encrypt : API Cloudflare, ou validation manuelle (le panel
 * affiche les enregistrements TXT à créer chez le fournisseur DNS).
 * onSaved(id).
 */
export function acmeCertDialog({ state = {}, onSaved } = {}) {
  const formId = 'acme-cert-form';
  const domains = input({ placeholder: t('settings.network.acmeDomainsPlaceholder'), mono: true });
  const token = secretInput({ placeholder: t('settings.network.acmeTokenPlaceholder') });
  const staging = switchRow({
    label: t('settings.network.acmeStaging'),
    description: t('settings.network.acmeStagingDesc'),
    checked: state.staging === true,
  });
  const mode = segmented([
    { value: 'cloudflare', label: t('settings.network.acmeModeCloudflare') },
    { value: 'manual', label: t('settings.network.acmeModeManual') },
  ], 'cloudflare', () => syncMode(), { label: t('settings.network.acmeMode') });
  const modeHint = h('p', { class: 'field-hint' });
  const tokenField = field({
    label: t('settings.network.acmeToken'),
    hint: t('settings.network.acmeTokenHint'),
    control: token.input,
  });
  const goBtn = button(t('settings.network.acmeIssue'), { variant: 'primary', type: 'submit', form: formId });

  function syncMode() {
    const manual = mode.value === 'manual';
    tokenField.hidden = manual;
    modeHint.textContent = t(manual ? 'settings.network.acmeManualModeHint' : 'settings.network.acmeCloudflareModeHint');
  }
  syncMode();

  openDialog({
    title: t('settings.network.acmeTitle'),
    build: ({ close }) => {
      async function submit(e) {
        e.preventDefault();
        const list = domains.value.trim();
        setFieldError(domains, list ? '' : t('settings.network.acmeNoDomains'));
        if (mode.value === 'cloudflare') {
          setFieldError(token.input, token.input.value.trim() ? '' : t('settings.network.acmeNoToken'));
        }
        if (!list || (mode.value === 'cloudflare' && !token.input.value.trim())) return;
        await busy(goBtn, async () => {
          try {
            if (mode.value === 'manual') {
              const d = await api('/api/manager/certs/acme/manual', {
                method: 'POST',
                body: { domains: list, staging: staging.input.checked },
              });
              if (!d.ok) { toastResult(d); return; }
              close();
              manualRecordsDialog(d, onSaved);
              return;
            }
            const d = await api('/api/manager/certs/acme', {
              method: 'POST',
              body: { domains: list, token: token.input.value.trim(), staging: staging.input.checked },
            });
            if (!d.ok) { toastResult(d); return; }
            toast(d.msg || t('settings.network.siteCertSaved'), 'success');
            close(d.id);
          } catch (err) { toastError(err); }
        });
      }
      return {
        body: [h('form', { id: formId, class: 'form-stack', onSubmit: submit, novalidate: true },
          field({ label: t('settings.network.acmeDomains'), hint: t('settings.network.acmeDomainsHint'), control: domains }),
          h('div', { class: 'field' },
            h('span', { class: 'field-label' }, t('settings.network.acmeMode')), mode, modeHint),
          tokenField,
          staging)],
        foot: [button(t('common.cancel'), { onClick: () => close() }), goBtn],
      };
    },
    onClose: (id) => { if (typeof id === 'string') onSaved?.(id); },
  });
}

/** Enregistrements TXT à créer à la main, puis validation de l’ordre. */
function manualRecordsDialog(order, onSaved) {
  const formId = 'acme-manual-form';
  const goBtn = button(t('settings.network.acmeManualValidate'), { variant: 'primary', type: 'submit', form: formId });

  function copy(text) {
    navigator.clipboard?.writeText(text)
      .then(() => toast(t('common.copied'), 'success', 1800))
      .catch(() => toast(t('errors.clipboard'), 'error'));
  }

  openDialog({
    title: t('settings.network.acmeManualTitle'),
    description: t('settings.network.acmeManualDesc'),
    size: 'lg',
    build: ({ close }) => {
      const rows = (order.records || []).map((r) => h('div', {
        class: 'form-stack', style: { gap: '4px', paddingTop: '8px', borderTop: '1px solid rgba(128,128,128,.25)' },
      },
      h('span', { class: 'field-label' }, r.domain || r.name),
      h('div', { class: 'row-actions' },
        h('code', { class: 'mono', style: { overflowWrap: 'anywhere' } }, `${r.name}  TXT  ${r.value}`),
        button(t('common.copy'), {
          variant: 'ghost', size: 'sm', iconName: 'copy',
          onClick: () => copy(`${r.name}\n${r.value}`),
        }))));

      async function submit(e) {
        e.preventDefault();
        await busy(goBtn, async () => {
          try {
            const d = await api(`/api/manager/certs/acme/manual/${encodeURIComponent(order.id)}`, {
              method: 'POST',
              body: {},
            });
            if (!d.ok) { toastResult(d); return; }
            toast(d.msg || t('settings.network.siteCertSaved'), 'success');
            close(d.id);
          } catch (err) { toastError(err); }
        });
      }

      return {
        body: [h('form', { id: formId, class: 'form-stack', onSubmit: submit, novalidate: true },
          callout({ type: 'info', compact: true, text: t('settings.network.acmeManualWait') }),
          rows)],
        foot: [button(t('common.cancel'), { onClick: () => close() }), goBtn],
      };
    },
    onClose: (id) => { if (typeof id === 'string') onSaved?.(id); },
  });
}
