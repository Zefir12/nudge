import { flushPromises, mount } from '@vue/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vite-plus/test';
import { createMemoryHistory } from 'vue-router';

import { setUnauthorizedHandler } from '../api/client';
import { createAppRouter } from '../router';
import { currentUser } from '../session';
import { admin, stubApi } from '../test-utils';
import LoginView from './LoginView.vue';

beforeEach(() => {
  currentUser.value = null;
});

afterEach(() => {
  vi.unstubAllGlobals();
});

async function mountAt(path: string) {
  const router = createAppRouter(createMemoryHistory());
  await router.push(path);
  const wrapper = mount(LoginView, { global: { plugins: [router] } });
  return { router, wrapper };
}

async function submit(wrapper: ReturnType<typeof mount>, username: string, password: string) {
  await wrapper.find('input[name=username]').setValue(username);
  await wrapper.find('input[name=password]').setValue(password);
  await wrapper.find('form').trigger('submit');
  await flushPromises();
}

describe('LoginView', () => {
  it('logs in and follows the redirect', async () => {
    const fetch = stubApi({
      'GET /setup': { body: { needed: false } },
      'POST /auth/login': { body: admin },
    });
    const { router, wrapper } = await mountAt('/login?redirect=/tokens');
    await submit(wrapper, ' admin ', 'longenough');
    const call = fetch.mock.calls.find(([url]) => url === '/api/v1/auth/login');
    expect(JSON.parse(String(call?.[1]?.body))).toEqual({ username: 'admin', password: 'longenough' });
    expect(currentUser.value).toEqual(admin);
    expect(router.currentRoute.value.fullPath).toBe('/tokens');
  });

  it('ignores a redirect to another site', async () => {
    stubApi({ 'GET /setup': { body: { needed: false } }, 'POST /auth/login': { body: admin } });
    const { router, wrapper } = await mountAt('/login?redirect=//evil.example');
    await submit(wrapper, 'admin', 'longenough');
    expect(router.currentRoute.value.fullPath).toBe('/');
  });

  it('shows a wrong password without redirecting', async () => {
    const onUnauthorized = vi.fn();
    setUnauthorizedHandler(onUnauthorized);
    stubApi({
      'GET /setup': { body: { needed: false } },
      'POST /auth/login': { status: 401, body: { error: 'invalid username or password' } },
    });
    const { router, wrapper } = await mountAt('/login');
    await submit(wrapper, 'admin', 'wrong-password');
    expect(wrapper.find('[role=alert]').text()).toBe('invalid username or password');
    expect(router.currentRoute.value.name).toBe('login');
    expect(onUnauthorized).not.toHaveBeenCalled();
    setUnauthorizedHandler(null);
  });
});

describe('LoginView sign-in methods', () => {
  it('offers SSO next to the password form', async () => {
    stubApi({
      'GET /setup': { body: { needed: false } },
      'GET /auth/methods': { body: { password: true, oidc: { name: 'Authestra' } } },
    });
    const { wrapper } = await mountAt('/login?redirect=/env/local/stacks');
    await flushPromises();
    const sso = wrapper.find('a.sso');
    expect(sso.text()).toBe('Log in with Authestra');
    expect(sso.attributes('href')).toBe('/api/v1/auth/oidc/login?redirect=%2Fenv%2Flocal%2Fstacks');
    expect(wrapper.find('form').exists()).toBe(true);
  });

  it('hides the password form when password login is off', async () => {
    stubApi({
      'GET /setup': { body: { needed: false } },
      'GET /auth/methods': { body: { password: false, oidc: { name: 'SSO' } } },
    });
    const { wrapper } = await mountAt('/login?redirect=//evil.example');
    await flushPromises();
    expect(wrapper.find('form').exists()).toBe(false);
    expect(wrapper.find('a.sso').attributes('href')).toBe('/api/v1/auth/oidc/login?redirect=%2F');
  });

  it('shows the password form for the break-glass admin with ?password', async () => {
    stubApi({
      'GET /setup': { body: { needed: false } },
      'GET /auth/methods': { body: { password: false, oidc: { name: 'SSO' } } },
    });
    const { wrapper } = await mountAt('/login?password');
    await flushPromises();
    expect(wrapper.find('form').exists()).toBe(true);
  });

  it('shows a generic error after a failed SSO login', async () => {
    stubApi({
      'GET /setup': { body: { needed: false } },
      'GET /auth/methods': { body: { password: true, oidc: { name: 'SSO' } } },
    });
    const { wrapper } = await mountAt('/login?error=sso');
    await flushPromises();
    expect(wrapper.find('[role=alert]').text()).toContain('Single sign-on failed');
  });

  it('keeps the password form when the methods fail to load', async () => {
    stubApi({ 'GET /setup': { body: { needed: false } } });
    const { wrapper } = await mountAt('/login');
    await flushPromises();
    expect(wrapper.find('form').exists()).toBe(true);
    expect(wrapper.find('a.sso').exists()).toBe(false);
  });
});
