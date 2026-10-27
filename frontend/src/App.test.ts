import { flushPromises, mount } from '@vue/test-utils';
import { afterEach, describe, expect, it, vi } from 'vite-plus/test';
import { createMemoryHistory } from 'vue-router';

import App from './App.vue';
import { createAppRouter } from './router';
import { currentUser } from './session';
import { stubApi, user } from './test-utils';

afterEach(() => {
  vi.unstubAllGlobals();
  currentUser.value = null;
});

async function mountApp(path: string) {
  const router = createAppRouter(createMemoryHistory());
  await router.push(path);
  const wrapper = mount(App, { global: { plugins: [router] } });
  await flushPromises();
  return wrapper;
}

describe('App', () => {
  it.each(['/registries', '/env/1/images', '/env/1/stacks', '/env/1/containers/new'])(
    "shows a user 'not allowed' on the admin page %s without asking the API",
    async (path) => {
      currentUser.value = user;
      const fetch = stubApi({ 'GET /environments': { body: [] } });
      const wrapper = await mountApp(path);
      expect(wrapper.find('main [role="alert"]').text()).toContain('Not allowed');
      // Only the sidebar's environment switcher asks.
      expect(fetch.mock.calls.every(([url]) => url === '/api/v1/environments')).toBe(true);
    },
  );

  it('shows a user the pages open to users', async () => {
    currentUser.value = user;
    stubApi({ 'GET /tokens': { body: [] } });
    const wrapper = await mountApp('/tokens');
    expect(wrapper.text()).not.toContain('Not allowed');
    expect(wrapper.find('main h1').text()).toBe('API tokens');
  });
});
