// ── Client HTTP ───────────────────────────────────────────────────────────
// Le backend répond toujours { ok, msg?, … }, y compris avec un code 4xx/5xx.
//   api()   → renvoie le JSON tel quel (l'appelant lit d.ok)
//   apiOk() → lève ApiError si d.ok est faux

import { t } from './i18n.js';

export class ApiError extends Error {
  constructor(message, data = null) {
    super(message);
    this.name = 'ApiError';
    this.data = data;
  }
}

export async function api(path, { method = 'GET', body, form, signal } = {}) {
  const init = { method, headers: { Accept: 'application/json' }, signal, credentials: 'same-origin' };
  if (form) {
    init.body = form;
  } else if (body !== undefined) {
    init.headers['Content-Type'] = 'application/json';
    init.body = JSON.stringify(body);
  }

  let res;
  try {
    res = await fetch(path, init);
  } catch (err) {
    if (err.name === 'AbortError') throw err;
    throw new ApiError(t('errors.network'));
  }

  if (res.status === 401 && !path.startsWith('/api/login')) {
    location.href = '/login';
    throw new ApiError(t('errors.unauthenticated'));
  }

  try {
    return await res.json();
  } catch {
    throw new ApiError(t('errors.badResponse', { status: res.status }));
  }
}

export async function apiOk(path, opts) {
  const data = await api(path, opts);
  if (!data || !data.ok) throw new ApiError((data && data.msg) || t('errors.generic'), data);
  return data;
}

export const post = (path, body) => api(path, { method: 'POST', body });
