const decode = value => Uint8Array.from(atob(value.replace(/-/g, '+').replace(/_/g, '/')), c => c.charCodeAt(0));
const encode = value => btoa(String.fromCharCode(...new Uint8Array(value))).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');

export function passkeysSupported() {
  return Boolean(globalThis.isSecureContext && globalThis.PublicKeyCredential && navigator.credentials);
}

export async function usePasskey(api, operation, name = '') {
  if (!passkeysSupported()) throw new Error('Passkeys require a supported browser on HTTPS or localhost.');
  const path = `/api/auth/passkeys/${operation}`;
  const options = await api(`${path}/begin`, { method: 'POST', body: JSON.stringify({ name }) });
  const publicKey = options.publicKey;
  publicKey.challenge = decode(publicKey.challenge);
  if (publicKey.user) publicKey.user.id = decode(publicKey.user.id);
  for (const field of ['excludeCredentials', 'allowCredentials']) {
    if (publicKey[field]) publicKey[field] = publicKey[field].map(c => ({ ...c, id: decode(c.id) }));
  }
  let credential;
  try {
    credential = await navigator.credentials[operation === 'register' ? 'create' : 'get']({ publicKey });
  } catch (error) {
    if (error.name === 'NotAllowedError' || error.name === 'AbortError') throw new Error('Passkey request canceled or timed out. You can try again.');
    throw new Error('Your device could not complete the passkey request. Try another device or browser.');
  }
  if (!credential) throw new Error('No passkey was selected.');
  const response = {};
  for (const key of ['clientDataJSON', 'attestationObject', 'authenticatorData', 'signature', 'userHandle']) {
    if (credential.response[key] != null) response[key] = encode(credential.response[key]);
  }
  if (credential.response.getTransports) response.transports = credential.response.getTransports();
  return api(`${path}/finish`, {
    method: 'POST', body: JSON.stringify({ id: credential.id, rawId: encode(credential.rawId), type: credential.type,
      authenticatorAttachment: credential.authenticatorAttachment, response,
      clientExtensionResults: credential.getClientExtensionResults() }),
  });
}
