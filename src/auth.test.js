import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import { mount, flushPromises } from '@vue/test-utils';
import { createRouter, createMemoryHistory } from 'vue-router';
import App from './App.vue';
import { routes } from './routes';
import { usePasskey } from './passkeys';
vi.mock('./passkeys', () => ({ passkeysSupported: () => true, usePasskey: vi.fn() }));
let wrapper, authenticated, passwordEnabled;
beforeEach(() => {
  localStorage.clear(); authenticated = false; passwordEnabled = false; usePasskey.mockReset();
  vi.stubGlobal('fetch', vi.fn(async (path, options = {}) => {
    let data;
    if (path === '/api/auth/status') data = { enabled: true, authenticated, password_enabled: passwordEnabled, passkeys_enabled: true };
    else if (path === '/api/config') data = {};
    else if (path === '/api/categories' || path === '/api/videos' || path === '/api/category-migrations') data = [];
    else if (path === '/api/auth/passkeys' && !options.method) data = { credentials: [{ id: 'key1', name: 'Laptop', hostname: 'video.example.com' }], password_enabled: passwordEnabled };
    else if (path === '/api/auth/password') { passwordEnabled = JSON.parse(options.body).enabled; data = { ok: true }; }
    else if (path.startsWith('/api/auth/passkeys/') && options.method === 'DELETE') data = { ok: true };
    else throw new Error(`Unexpected request ${path}`);
    return { ok: true, json: async () => data };
  }));
});
afterEach(() => { wrapper?.unmount(); vi.unstubAllGlobals(); });
async function open() {
  const router = createRouter({ history: createMemoryHistory(), routes });
  await router.push('/'); await router.isReady();
  wrapper = mount(App, { global: { plugins: [router] } }); await flushPromises();
}
it('shows passkey-only login and enters the library after verification', async () => {
  usePasskey.mockImplementation(async () => { authenticated = true; });
  await open(); expect(wrapper.find('#password').exists()).toBe(false);
  await wrapper.find('button').trigger('click'); await flushPromises();
  expect(usePasskey).toHaveBeenCalledWith(expect.any(Function), 'login');
  expect(wrapper.vm.authenticated).toBe(true);
});
it('shows cancellation and remains on login', async () => {
  usePasskey.mockRejectedValue(new Error('Passkey request canceled or timed out.'));
  await open(); await wrapper.find('button').trigger('click'); await flushPromises();
  expect(wrapper.text()).toContain('canceled or timed out'); expect(wrapper.vm.authenticated).toBe(false);
});
it('lists hostnames, adds a named key, verifies and updates password policy', async () => {
  authenticated = true; passwordEnabled = true; usePasskey.mockResolvedValue({ authenticated: true });
  await open(); await wrapper.vm.toggleSecurity(); await flushPromises();
  expect(wrapper.text()).toContain('Laptop — video.example.com');
  wrapper.vm.passkeyName = 'Backup'; await wrapper.vm.addPasskey();
  expect(usePasskey).toHaveBeenCalledWith(expect.any(Function), 'register', 'Backup');
  await wrapper.vm.verifySecurityPasskey(); expect(wrapper.vm.securityMessage).toContain('verified');
  await wrapper.vm.setPasswordLogin(false); expect(wrapper.vm.passwordEnabled).toBe(false);
  await wrapper.vm.removePasskey('key1'); expect(fetch).toHaveBeenCalledWith('/api/auth/passkeys/key1', expect.objectContaining({ method: 'DELETE' }));
});
