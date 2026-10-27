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
  it("shows a user 'not allowed' on an admin page without asking the API", async () => {
    currentUser.value = user;
    const fetch = stubApi({});
    const wrapper = await mountApp('/registries');
    expect(wrapper.find('main [role="alert"]').text()).toContain('Not allowed');
    expect(fetch).not.toHaveBeenCalled();
  });

  it('shows a user the pages open to users', async () => {
    currentUser.value = user;
    stubApi({ 'GET /tokens': { body: [] } });
    const wrapper = await mountApp('/tokens');
    expect(wrapper.text()).not.toContain('Not allowed');
    expect(wrapper.find('main h1').text()).toBe('API tokens');
  });
});
