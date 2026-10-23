import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { usePasskey, passkeysSupported } from './passkeys';

const bytes = new Uint8Array([1, 2, 3]).buffer;
let get, create;
beforeEach(() => {
  get = vi.fn(); create = vi.fn();
  vi.stubGlobal('isSecureContext', true);
  vi.stubGlobal('PublicKeyCredential', function () {});
  vi.stubGlobal('navigator', { credentials: { get, create } });
});
afterEach(() => vi.unstubAllGlobals());
const credential = () => ({ id: 'AQID', rawId: bytes, type: 'public-key',
  response: { clientDataJSON: bytes, authenticatorData: bytes, signature: bytes, userHandle: bytes },
  getClientExtensionResults: () => ({}) });
describe('passkey browser integration', () => {
  it('decodes assertion options and serializes signed responses', async () => {
    get.mockResolvedValue(credential());
    const api = vi.fn().mockResolvedValueOnce({ publicKey: { challenge: 'AQID', allowCredentials: [{ id: 'AQID', type: 'public-key' }] } }).mockResolvedValueOnce({ authenticated: true });
    await expect(usePasskey(api, 'login')).resolves.toEqual({ authenticated: true });
    expect(Array.from(get.mock.calls[0][0].publicKey.challenge)).toEqual([1, 2, 3]);
    expect(JSON.parse(api.mock.calls[1][1].body).response.signature).toBe('AQID');
    expect(api.mock.calls[1][0]).toBe('/api/auth/passkeys/login/finish');
  });
  it('registers named resident credentials and preserves transports', async () => {
    const c = credential(); c.response = { clientDataJSON: bytes, attestationObject: bytes, getTransports: () => ['internal'] };
    create.mockResolvedValue(c);
    const api = vi.fn().mockResolvedValueOnce({ publicKey: { challenge: 'AQID', user: { id: 'AQID' }, excludeCredentials: [{ id: 'AQID' }] } }).mockResolvedValueOnce({ authenticated: true });
    await usePasskey(api, 'register', 'Laptop');
    expect(JSON.parse(api.mock.calls[0][1].body)).toEqual({ name: 'Laptop' });
    expect(Array.from(create.mock.calls[0][0].publicKey.user.id)).toEqual([1, 2, 3]);
    expect(JSON.parse(api.mock.calls[1][1].body).response.transports).toEqual(['internal']);
  });
  it('explains unsupported contexts before calling the server', async () => {
    vi.stubGlobal('isSecureContext', false);
    expect(passkeysSupported()).toBe(false);
    const api = vi.fn(); await expect(usePasskey(api, 'login')).rejects.toThrow('HTTPS or localhost'); expect(api).not.toHaveBeenCalled();
  });
  it('handles cancellation without finishing a challenge', async () => {
    get.mockRejectedValue(new DOMException('Canceled', 'NotAllowedError'));
    const api = vi.fn().mockResolvedValue({ publicKey: { challenge: 'AQID' } });
    await expect(usePasskey(api, 'login')).rejects.toThrow('canceled or timed out'); expect(api).toHaveBeenCalledTimes(1);
  });
});
