import { flushPromises, mount } from '@vue/test-utils';
import { afterEach, describe, expect, it, vi } from 'vite-plus/test';
import { createMemoryHistory } from 'vue-router';

import App from './App.vue';
import { createAppRouter } from './router';
import { currentUser } from './session';
import { admin, stubApi, user } from './test-utils';

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
      // Only the sidebar asks: the environment switcher and the version.
      const sidebar = ['/api/v1/environments', '/api/v1/health'];
      expect(fetch.mock.calls.every(([url]) => sidebar.includes(url))).toBe(true);
    },
  );

  it('shows a user the pages open to users', async () => {
    currentUser.value = user;
    stubApi({ 'GET /tokens': { body: [] } });
    const wrapper = await mountApp('/tokens');
    expect(wrapper.text()).not.toContain('Not allowed');
    expect(wrapper.find('main h1').text()).toBe('API tokens');
    expect(wrapper.find('[aria-label="Settings"]').exists()).toBe(false);
  });

  it('opens the settings from the gear for admins', async () => {
    currentUser.value = admin;
    stubApi({
      'GET /tokens': { body: [] },
      'GET /system/update': { body: { version: 'v1.2.0', status: 'current', can_update: true } },
    });
    const wrapper = await mountApp('/tokens');
    expect(wrapper.find('dialog').exists()).toBe(false);
    await wrapper.find('[aria-label="Settings"]').trigger('click');
    await flushPromises();
    expect(wrapper.find('dialog').text()).toContain('v1.2.0');
    await wrapper.find('dialog [aria-label="Close"]').trigger('click');
    expect(wrapper.find('dialog').exists()).toBe(false);
  });
});
