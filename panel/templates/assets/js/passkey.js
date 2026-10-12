// ── Passkeys (WebAuthn) ───────────────────────────────────────────────────
// Le serveur envoie ses options en JSON (octets en base64url) : on les convertit
// pour navigator.credentials, puis la réponse du navigateur dans l'autre sens.
// Page de connexion et Réglages → Mon compte.

function fromB64(s) {
  const b64 = s.replace(/-/g, '+').replace(/_/g, '/');
  return Uint8Array.from(atob(b64.padEnd(Math.ceil(b64.length / 4) * 4, '=')), (c) => c.charCodeAt(0));
}

function toB64(buf) {
  let s = '';
  for (const b of new Uint8Array(buf)) s += String.fromCharCode(b);
  return btoa(s).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
}

const credList = (list) => (list || []).map((c) => ({ ...c, id: fromB64(c.id) }));

/**
 * Pourquoi les passkeys ne sont pas utilisables sur cette page, ou '' :
 * 'insecure' (panel ouvert en http, hors localhost), 'ip' (ouvert par une
 * adresse IP : WebAuthn exige un nom de domaine), 'browser' (navigateur trop ancien).
 */
export function passkeysUnavailable() {
  if (!window.isSecureContext) return 'insecure';
  if (/^[\d.]+$/.test(location.hostname) || location.hostname.includes(':') || location.hostname.startsWith('[')) return 'ip';
  if (!window.PublicKeyCredential || !navigator.credentials) return 'browser';
  return '';
}

/** Suggestion des passkeys dans le champ identifiant (« conditional mediation »). */
export async function passkeyAutofill() {
  if (passkeysUnavailable()) return false;
  try {
    return Boolean(await PublicKeyCredential.isConditionalMediationAvailable?.());
  } catch {
    return false;
  }
}

/** navigator.credentials.create() → réponse au format du serveur. */
export async function createPasskey(publicKey) {
  const cred = await navigator.credentials.create({
    publicKey: {
      ...publicKey,
      challenge: fromB64(publicKey.challenge),
      user: { ...publicKey.user, id: fromB64(publicKey.user.id) },
      excludeCredentials: credList(publicKey.excludeCredentials),
    },
  });
  return {
    id: cred.id, rawId: toB64(cred.rawId), type: cred.type,
    response: {
      clientDataJSON: toB64(cred.response.clientDataJSON),
      attestationObject: toB64(cred.response.attestationObject),
      transports: cred.response.getTransports?.() || [],
    },
  };
}

/** navigator.credentials.get() → réponse au format du serveur (null si rien). */
export async function getPasskey(publicKey, { signal, mediation } = {}) {
  const cred = await navigator.credentials.get({
    publicKey: { ...publicKey, challenge: fromB64(publicKey.challenge), allowCredentials: credList(publicKey.allowCredentials) },
    signal, ...(mediation ? { mediation } : {}),
  });
  if (!cred) return null;
  const r = cred.response;
  return {
    id: cred.id, rawId: toB64(cred.rawId), type: cred.type,
    response: {
      clientDataJSON: toB64(r.clientDataJSON),
      authenticatorData: toB64(r.authenticatorData),
      signature: toB64(r.signature),
      userHandle: r.userHandle ? toB64(r.userHandle) : null,
    },
  };
}
