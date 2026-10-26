import { flushPromises, mount } from '@vue/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vite-plus/test';
import { createMemoryHistory } from 'vue-router';

import { createAppRouter } from '../router';
import { currentUser } from '../session';
import { admin, stubApi } from '../test-utils';
import HomeView from './HomeView.vue';

const envs = [
  { id: 1, name: 'local', kind: 'local', online: true, created_at: '', last_seen_at: null },
  {
    id: 4,
    name: 'nas',
    kind: 'agent',
    online: false,
    created_at: '',
    last_seen_at: null,
    agent: { enrolled: true, version: '0.9.0' },
  },
];

const docker = {
  version: '27.3.1',
  os_type: 'linux',
  architecture: 'x86_64',
  ncpu: 8,
  mem_total: 8 * 1024 ** 3,
  containers: 16,
  containers_running: 14,
  images: 38,
};

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
  it('shows a tile per environment that opens it', async () => {
    stubApi({
      'GET /environments': { body: envs },
      'GET /environments/1': { body: { ...envs[0], docker } },
    });
    const { wrapper, router } = await mountHome();
    expect(router.currentRoute.value.fullPath).toBe('/');
    expect(wrapper.text()).toContain('2 hosts · 1 online');
    const tiles = wrapper.findAll('.tile');
    expect(tiles.map((t) => t.find('h2').text())).toEqual(['local', 'nas']);
    expect(tiles[0]?.attributes('href')).toBe('/env/1');
    expect(tiles[0]?.text()).toContain('14/16');
    expect(tiles[1]?.text()).toContain('Agent not connected');
  });

  it.each([
    { name: 'admins', user: admin, want: true },
    { name: 'other users', user: { ...admin, admin: false }, want: false },
  ])('offers adding an environment to $name', async ({ user, want }) => {
    currentUser.value = user;
    stubApi({ 'GET /environments': { body: [] } });
    const { wrapper } = await mountHome();
    expect(wrapper.find('a[href="/environments"]').exists()).toBe(want);
  });

  it('says when there are no environments', async () => {
    stubApi({ 'GET /environments': { body: [] } });
    const { wrapper } = await mountHome();
    expect(wrapper.text()).toContain('There are no environments yet.');
  });

  it('shows an error', async () => {
    stubApi({ 'GET /environments': { status: 500, body: { error: 'boom' } } });
    const { wrapper } = await mountHome();
    expect(wrapper.find('[role=alert]').text()).toContain('boom');
  });
});
