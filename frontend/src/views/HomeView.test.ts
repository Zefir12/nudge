import { flushPromises, mount } from '@vue/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vite-plus/test';
import { createMemoryHistory } from 'vue-router';

import { createAppRouter } from '../router';
import { currentUser } from '../session';
import { admin, stubApi } from '../test-utils';
import HomeView from './HomeView.vue';

const envs = [
  { id: 1, name: 'local', kind: 'local', online: true, created_at: '', last_seen_at: null },
  { id: 4, name: 'nas', kind: 'agent', online: false, created_at: '', last_seen_at: null },
];

async function mountHome() {
  const router = createAppRouter(createMemoryHistory());
  await router.push('/');
  const wrapper = mount(HomeView, { global: { plugins: [router] } });
  await flushPromises();
  return { wrapper, router };
}

beforeEach(() => {
  currentUser.value = admin;
  localStorage.clear();
});

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('HomeView', () => {
  it.each([
    { name: 'first environment', last: null, want: '/env/1' },
    { name: 'the one used last', last: '4', want: '/env/4' },
    { name: 'first when the last one is gone', last: '9', want: '/env/1' },
  ])('opens the $name', async ({ last, want }) => {
    if (last) localStorage.setItem('nudge.lastEnvironment', last);
    stubApi({ 'GET /environments': { body: envs } });
    const { router } = await mountHome();
    expect(router.currentRoute.value.fullPath).toBe(want);
  });

  it('says when there are no environments', async () => {
    stubApi({ 'GET /environments': { body: [] } });
    const { wrapper, router } = await mountHome();
    expect(router.currentRoute.value.fullPath).toBe('/');
    expect(wrapper.text()).toContain('There are no environments yet.');
  });

  it('shows an error', async () => {
    stubApi({ 'GET /environments': { status: 500, body: { error: 'boom' } } });
    const { wrapper } = await mountHome();
    expect(wrapper.find('[role=alert]').text()).toContain('boom');
  });
});
