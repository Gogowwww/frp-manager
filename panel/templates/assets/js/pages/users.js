// ── Utilisateurs : comptes, permissions (ACL) et connexion unique OIDC ────
// Le serveur vérifie tout (y compris qu'on n'accorde pas plus que ses propres
// droits) ; cette page ne fait que présenter les choix.

import { t, getLocale } from '../i18n.js';
import { api, apiOk } from '../api.js';
import { store, displayName } from '../store.js';
import {
  h, button, busy, toast, toastResult, toastError, openDialog, confirmDialog, field, setFieldError, input, select,
  secretInput, segmented, switchControl, switchRow, callout, emptyState, pageHeader, card, badge, copyButton,
} from '../ui.js';

const ROLE_BADGE = { admin: 'danger', operator: 'warn', viewer: '', custom: 'info' };
const S = { data: null, els: {} };

export default {
  id: 'users',
  title: () => t('nav.users'),
  perm: 'users.manage',

  async mount(view) {
    const addBtn = button(t('users.add'), { variant: 'primary', iconName: 'plus', onClick: () => edit(null) });
    S.els.list = h('div', { class: 'card' }, h('div', { class: 'skeleton', style: { height: '120px' } }));
    S.els.oidc = h('div');
    view.append(h('div', { class: 'page' },
      pageHeader({ title: t('users.title'), description: t('users.description'), actions: [addBtn] }),
      S.els.list, S.els.oidc));
    await reload();
    if (S.data?.can_configure_oidc) loadOidc();
  },

  unmount() { S.els = {}; },
};

async function reload() {
  try {
    S.data = await apiOk('/api/users');
    renderList();
  } catch (e) {
    S.els.list?.replaceChildren(callout({ type: 'danger', title: t('users.loadError'), text: e.message }));
  }
}

const roleLabel = (role) => t(`users.roles.${role}`);
const permLabel = (id) => t(`users.perms.${id.replace('.', '_')}`);
const when = (iso) => (iso ? new Date(iso).toLocaleString(getLocale()) : t('users.never'));
const instName = (iid) => (store.instances[iid] ? displayName(iid) : iid);

function renderList() {
  const host = S.els.list;
  if (!host) return;
  const users = S.data.users;
  if (!users.length) {
    host.replaceChildren(emptyState({ iconName: 'shield', title: t('users.emptyTitle') }));
    return;
  }
  host.replaceChildren(h('ul', { class: 'rows' }, users.map((u) => {
    const editable = !u.owner && u.id !== S.data.me;
    const sw = switchControl({
      checked: u.enabled, disabled: !editable, label: t('users.enabled'),
      onChange: async (on) => {
        if (!(await save(u.id, { enabled: on }, on ? t('users.enabledOn') : t('users.enabledOff')))) sw.input.checked = !on;
      },
    });
    sw.addEventListener('click', (e) => e.stopPropagation());
    const open = () => { if (editable) edit(u); };
    const badges = [badge(roleLabel(u.role), ROLE_BADGE[u.role])];
    if (u.owner) badges.push(badge(t('users.owner'), 'success'));
    if (u.id === S.data.me) badges.push(badge(t('users.you')));
    if (u.source === 'oidc') badges.push(badge(u.linked ? 'OIDC' : t('users.oidcPending'), u.linked ? 'info' : 'warn'));
    return h('li', {
      class: `row us-row${u.enabled ? '' : ' is-off'}`, tabindex: editable ? '0' : null, onClick: open,
      onKeydown: (e) => { if (e.key === 'Enter') open(); },
    },
    h('div', { class: 'row-name' }, h('span', null, u.display_name ? `${u.username} · ${u.display_name}` : u.username), badges),
    h('div', { class: 'fw-detail' },
      h('span', null, h('small', null, t('users.instancesLabel')),
        h('span', null, u.instances === '*' ? t('users.allInstances') : u.instances.map(instName).join(', ') || '—')),
      h('span', null, h('small', null, t('users.lastLogin')), h('span', null, when(u.last_login)))),
    h('div', { class: 'row-actions' },
      sw,
      editable && button('', { variant: 'ghost', size: 'sm', iconName: 'edit', title: t('common.edit'), onClick: (e) => { e.stopPropagation(); open(); } }),
      editable && button('', { variant: 'ghost-danger', size: 'sm', iconName: 'trash', title: t('common.delete'), onClick: (e) => { e.stopPropagation(); remove(u); } })));
  })));
}

async function save(uid, body, okMessage) {
  try {
    const d = await api(`/api/users/${encodeURIComponent(uid)}`, { method: 'PUT', body });
    if (!d.ok) { toastResult(d); return false; }
    toast(okMessage || d.msg, 'success');
    await reload();
    return true;
  } catch (err) { toastError(err); return false; }
}

async function remove(u) {
  const ok = await confirmDialog({
    title: t('users.deleteTitle', { name: u.username }), message: t('users.deleteText'),
    confirmLabel: t('common.delete'), danger: true,
  });
  if (!ok) return;
  try {
    const d = await api(`/api/users/${encodeURIComponent(u.id)}`, { method: 'DELETE' });
    toastResult(d);
    if (d.ok) await reload();
  } catch (err) { toastError(err); }
}

// ── Éditeur d'un compte ──────────────────────────────────────────────────
/** Permissions (et celles qu'elles impliquent) : même calcul que le serveur. */
function expand(perms) {
  const out = new Set();
  const todo = [...perms];
  while (todo.length) {
    const p = todo.pop();
    if (!out.has(p)) { out.add(p); todo.push(...(S.data.implies[p] || [])); }
  }
  return out;
}

function edit(existing) {
  const isNew = !existing;
  const draft = existing
    ? { ...existing, permissions: [...existing.permissions], instances: existing.instances }
    : { source: 'local', username: '', email: '', role: 'viewer', permissions: [], instances: '*', enabled: true };
  if (draft.role !== 'custom' && !isNew) draft.permissions = [];
  const formId = 'us-form';

  openDialog({
    title: isNew ? t('users.newTitle') : t('users.editTitle', { name: existing.username }),
    description: t('users.editorHint'),
    size: 'lg',
    build: ({ close }) => {
      const username = input({ value: draft.username, maxLength: 64, autocomplete: 'off', spellcheck: 'false' });
      const password = secretInput({ autocomplete: 'new-password', placeholder: isNew ? '' : t('users.keepPassword') });
      const email = input({ value: draft.email || '', type: 'email', autocomplete: 'off', spellcheck: 'false', placeholder: 'prenom.nom@exemple.fr' });
      const passwordField = field({ label: isNew ? t('users.password') : t('users.newPassword'), hint: t('users.passwordHint'), control: password });
      const emailField = field({ label: t('users.email'), hint: t('users.emailHint'), control: email });
      email.disabled = !isNew && existing.linked;

      const source = segmented([
        { value: 'local', label: t('users.sourceLocal') },
        { value: 'oidc', label: 'OpenID Connect' },
      ], draft.source, (v) => { draft.source = v; syncSource(); }, { label: t('users.source') });
      const syncSource = () => {
        passwordField.hidden = draft.source !== 'local';
        emailField.hidden = draft.source !== 'oidc';
      };

      const role = select(['admin', 'operator', 'viewer', 'custom'].map((r) => ({ value: r, label: roleLabel(r) })), draft.role);
      const roleHint = h('p', { class: 'field-hint' });

      // Cases des permissions, regroupées par domaine (instances, config…)
      const boxes = new Map();
      const groups = [];
      for (const p of S.data.permissions) {
        const group = p.id.split('.')[0];
        if (!groups.length || groups.at(-1).group !== group) groups.push({ group, perms: [] });
        groups.at(-1).perms.push(p);
      }
      const permGrid = h('div', { class: 'perm-grid' }, groups.map(({ group, perms }) => [
        h('div', { class: 'perm-group-title' }, t(`users.permGroups.${group}`)),
        perms.map((p) => {
          const cb = h('input', { type: 'checkbox', checked: draft.permissions.includes(p.id) });
          cb.addEventListener('change', () => {
            draft.permissions = [...boxes].filter(([, b]) => b.cb.checked && !b.implied).map(([id]) => id);
            syncPerms();
          });
          const item = h('label', { class: 'perm-item' }, cb,
            h('span', null, permLabel(p.id), ' ', p.sensitive && badge(t('users.sensitive'), 'warn'), h('br'), h('code', null, p.id)));
          boxes.set(p.id, { cb, item, scoped: p.scoped, implied: false });
          return item;
        }),
      ]));

      // Instances : toutes, ou une sélection
      const allIds = [...new Set([...S.data.instances, ...(Array.isArray(draft.instances) ? draft.instances : [])])];
      const scopeAll = draft.instances === '*';
      const scope = segmented([
        { value: 'all', label: t('users.allInstances') },
        { value: 'some', label: t('users.someInstances') },
      ], scopeAll ? 'all' : 'some', () => syncPerms(), { label: t('users.instancesLabel') });
      const instBoxes = new Map(allIds.map((iid) => [iid, h('input', {
        type: 'checkbox', checked: !scopeAll && draft.instances.includes(iid),
      })]));
      const instList = h('div', { class: 'inst-list' }, allIds.length
        ? allIds.map((iid) => h('label', { class: 'nt-check' }, instBoxes.get(iid), h('span', null, instName(iid))))
        : h('span', { class: 'muted' }, t('users.noInstances')));
      const scopeNote = callout({ type: 'neutral', compact: true, text: t('users.scopeNote') });
      const scopeBlock = h('div', { class: 'field' },
        h('span', { class: 'field-label' }, t('users.instancesLabel')), scope, instList, scopeNote);

      function syncPerms() {
        const r = role.value;
        const custom = r === 'custom';
        const restricted = scope.value === 'some' && r !== 'admin';
        const granted = r === 'admin' ? new Set(S.data.permissions.map((p) => p.id))
          : custom ? expand(draft.permissions) : new Set(S.data.roles[r]);
        const chosen = new Set(draft.permissions);
        for (const [id, b] of boxes) {
          b.implied = custom && granted.has(id) && !chosen.has(id);
          b.cb.checked = granted.has(id) && !(restricted && !b.scoped);
          b.cb.disabled = !custom || b.implied || (restricted && !b.scoped);
          b.item.classList.toggle('is-implied', b.implied || (restricted && !b.scoped));
        }
        roleHint.textContent = t(`users.roleHints.${r}`);
        scopeBlock.hidden = r === 'admin';
        instList.hidden = scope.value !== 'some';
        scopeNote.hidden = scope.value !== 'some';
      }
      // Passer d'un rôle prêt à l'emploi à « Personnalisé » part de ses permissions
      let prevRole = role.value;
      role.addEventListener('change', () => {
        if (role.value === 'custom' && prevRole !== 'custom' && !draft.permissions.length) {
          draft.permissions = prevRole === 'admin' ? S.data.permissions.map((p) => p.id) : [...S.data.roles[prevRole]];
        }
        prevRole = role.value;
        syncPerms();
      });

      const enabled = switchRow({ label: t('users.enabled'), description: t('users.enabledDesc'), checked: draft.enabled });

      async function submit(e) {
        e.preventDefault();
        const body = {
          username: username.value.trim(), role: role.value, enabled: enabled.input.checked,
          permissions: role.value === 'custom' ? draft.permissions : [],
          instances: scope.value === 'all' || role.value === 'admin' ? '*' : [...instBoxes].filter(([, cb]) => cb.checked).map(([iid]) => iid),
        };
        setFieldError(username, body.username ? '' : t('validation.required'));
        if (!body.username) return;
        if (draft.source === 'local' && (isNew || password.input.value)) {
          const short = password.input.value.length < 12;
          setFieldError(password.input, short ? t('settings.security.tooShort') : '');
          if (short) return;
          body.password = password.input.value;
        }
        if (draft.source === 'oidc' && !email.disabled) {
          setFieldError(email, email.value.trim() ? '' : t('validation.required'));
          if (!email.value.trim()) return;
          body.email = email.value.trim();
        }
        if (Array.isArray(body.instances) && !body.instances.length) { toast(t('users.pickInstance'), 'error'); return; }
        await busy(saveBtn, async () => {
          try {
            const d = isNew
              ? await api('/api/users', { method: 'POST', body: { ...body, source: draft.source } })
              : await api(`/api/users/${encodeURIComponent(existing.id)}`, { method: 'PUT', body });
            if (!d.ok) { toastResult(d); return; }
            toast(d.msg, 'success');
            close(true);
            await reload();
          } catch (err) { toastError(err); }
        });
      }

      const saveBtn = button(isNew ? t('users.create') : t('common.save'), { variant: 'primary', type: 'submit', form: formId });
      syncSource();
      syncPerms();
      return {
        body: [h('form', { id: formId, class: 'form-stack', onSubmit: submit, novalidate: true },
          isNew && h('div', { class: 'field' }, h('span', { class: 'field-label' }, t('users.source')), source),
          h('div', { class: 'form-grid' },
            field({ label: t('users.username'), control: username }),
            passwordField, emailField),
          h('div', { class: 'field' }, h('label', { class: 'field-label', for: 'us-role' }, t('users.role')), Object.assign(role, { id: 'us-role' }), roleHint),
          permGrid,
          scopeBlock,
          enabled)],
        foot: [button(t('common.cancel'), { onClick: () => close(false) }), saveBtn],
      };
    },
  });
}

// ── Connexion unique (OpenID Connect) ────────────────────────────────────
async function loadOidc() {
  try {
    const d = await apiOk('/api/oidc');
    S.els.oidc?.replaceChildren(oidcCard(d.oidc, d.redirect_uri));
  } catch (e) {
    S.els.oidc?.replaceChildren(callout({ type: 'danger', title: t('users.oidc.loadError'), text: e.message }));
  }
}

function oidcCard(cfg, defaultRedirect) {
  const enabled = switchRow({ label: t('users.oidc.enable'), description: t('users.oidc.enableDesc'), checked: cfg.enabled });
  const issuer = input({ value: cfg.issuer, mono: true, spellcheck: 'false', placeholder: 'https://auth.exemple.fr/realms/maison' });
  const clientId = input({ value: cfg.client_id, mono: true, spellcheck: 'false' });
  const secret = secretInput({ placeholder: cfg.has_secret ? t('users.oidc.keepSecret') : t('users.oidc.noSecret') });
  const scopes = input({ value: cfg.scopes, mono: true, spellcheck: 'false' });
  const label = input({ value: cfg.label, maxLength: 40, placeholder: 'Keycloak, Authentik, Google…' });
  const redirect = input({ value: cfg.redirect_uri, mono: true, spellcheck: 'false', placeholder: defaultRedirect });
  const usernameClaim = input({ value: cfg.username_claim, mono: true, spellcheck: 'false' });
  const groupsClaim = input({ value: cfg.groups_claim, mono: true, spellcheck: 'false' });
  const autoCreate = switchRow({ label: t('users.oidc.autoCreate'), description: t('users.oidc.autoCreateDesc'), checked: cfg.auto_create });
  const defaultRole = select(['viewer', 'operator'].map((r) => ({ value: r, label: roleLabel(r) })), cfg.default_role);
  const domains = input({ value: (cfg.allowed_domains || []).join(', '), mono: true, spellcheck: 'false', placeholder: 'exemple.fr' });

  // Correspondance groupe du fournisseur → rôle
  const rows = h('div', { class: 'kv-rows' });
  const addRow = (group = '', r = 'viewer') => {
    const g = input({ value: group, mono: true, spellcheck: 'false', placeholder: 'frp-admins', 'aria-label': t('users.oidc.group') });
    const sel = select(['admin', 'operator', 'viewer'].map((x) => ({ value: x, label: roleLabel(x) })), r, { 'aria-label': t('users.role') });
    const row = h('div', { class: 'kv-row' }, g, sel,
      button('', { variant: 'ghost-danger', size: 'sm', iconName: 'trash', title: t('common.delete'), onClick: () => row.remove() }));
    row.read = () => [g.value.trim(), sel.value];
    rows.append(row);
  };
  Object.entries(cfg.group_roles || {}).forEach(([g, r]) => addRow(g, r));

  const testBtn = button(t('users.oidc.test'), { iconName: 'globe', onClick: () => busy(testBtn, async () => {
    try {
      const d = await api('/api/oidc/test', { method: 'POST', body: { issuer: issuer.value.trim() } });
      toast(d.msg, d.ok ? 'success' : 'error');
    } catch (err) { toastError(err); }
  }) });
  const saveBtn = button(t('common.save'), { variant: 'primary', type: 'submit', form: 'oidc-form' });

  async function submit(e) {
    e.preventDefault();
    const groupRoles = {};
    for (const row of rows.children) {
      const [g, r] = row.read();
      if (g) groupRoles[g] = r;
    }
    const body = {
      enabled: enabled.input.checked, issuer: issuer.value.trim(), client_id: clientId.value.trim(),
      scopes: scopes.value.trim(), label: label.value.trim(), redirect_uri: redirect.value.trim(),
      username_claim: usernameClaim.value.trim() || 'preferred_username', groups_claim: groupsClaim.value.trim() || 'groups',
      auto_create: autoCreate.input.checked, default_role: defaultRole.value,
      allowed_domains: domains.value, group_roles: groupRoles,
    };
    if (secret.input.value) body.client_secret = secret.input.value;
    await busy(saveBtn, async () => {
      try {
        const d = await api('/api/oidc', { method: 'POST', body });
        toastResult(d);
        if (d.ok) {
          secret.input.value = '';
          secret.input.placeholder = d.oidc.has_secret ? t('users.oidc.keepSecret') : t('users.oidc.noSecret');
          S.data.oidc = d.oidc.enabled;
        }
      } catch (err) { toastError(err); }
    });
  }

  return card({
    title: t('users.oidc.title'), description: t('users.oidc.desc'),
    body: h('form', { id: 'oidc-form', class: 'form-stack', onSubmit: submit, novalidate: true },
      enabled,
      h('div', { class: 'form-grid' },
        field({ label: t('users.oidc.issuer'), hint: t('users.oidc.issuerHint'), control: issuer }),
        field({ label: t('users.oidc.clientId'), control: clientId }),
        field({ label: t('users.oidc.clientSecret'), hint: t('users.oidc.clientSecretHint'), control: secret })),
      h('div', { class: 'field' },
        h('span', { class: 'field-label' }, t('users.oidc.redirect')),
        h('div', { class: 'input-group' }, redirect, copyButton(redirect.value || defaultRedirect)),
        h('p', { class: 'field-hint' }, t('users.oidc.redirectHint'))),
      h('div', { class: 'form-grid' },
        field({ label: t('users.oidc.label'), hint: t('users.oidc.labelHint'), control: label, optional: true }),
        field({ label: t('users.oidc.scopes'), control: scopes }),
        field({ label: t('users.oidc.usernameClaim'), control: usernameClaim })),
      autoCreate,
      h('div', { class: 'form-grid' },
        field({ label: t('users.oidc.defaultRole'), hint: t('users.oidc.defaultRoleHint'), control: defaultRole }),
        field({ label: t('users.oidc.domains'), hint: t('users.oidc.domainsHint'), control: domains, optional: true }),
        field({ label: t('users.oidc.groupsClaim'), control: groupsClaim })),
      h('div', { class: 'field' },
        h('span', { class: 'field-label' }, t('users.oidc.groupRoles')),
        rows,
        h('div', null, button(t('users.oidc.addGroup'), { size: 'sm', iconName: 'plus', onClick: () => addRow() })),
        h('p', { class: 'field-hint' }, t('users.oidc.groupRolesHint')))),
    foot: [testBtn, saveBtn],
  });
}
