import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vite-plus/test';
import { createMemoryHistory } from 'vue-router';

import { environments } from '../environments';
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

const joinToken = {
  token: 'nudge_join_secret',
  expires_at: '2026-10-25T12:00:00Z',
  docker_run: 'docker run -d -e NUDGE_JOIN_TOKEN=nudge_join_secret nudge-agent:latest agent',
  compose: 'services:\n  nudge-agent:\n    environment:\n      NUDGE_JOIN_TOKEN: "nudge_join_secret"\n',
};

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

const addButtons = (wrapper: Awaited<ReturnType<typeof mountHome>>['wrapper']) =>
  wrapper.findAll('button').filter((b) => b.text().includes('Add environment'));

// Unmounting stops each dashboard's refresh, so one test's listeners don't count in the next.
enableAutoUnmount(afterEach);

beforeEach(() => {
  currentUser.value = admin;
  environments.value = null;
  localStorage.clear();
});

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
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
    expect(tiles[0]?.find('h2 a').attributes('href')).toBe('/env/1');
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
    expect(addButtons(wrapper).length > 0).toBe(want);
  });

  it('adds an environment in a dialog and reloads the tiles', async () => {
    let list: unknown[] = [envs[0]];
    stubApi({
      'GET /environments': () => ({ body: list }),
      'GET /environments/1': { body: { ...envs[0], docker } },
      'POST /environments': () => {
        const created = { ...envs[1], agent: { enrolled: false, version: '' } };
        list = [envs[0], created];
        return { status: 201, body: { ...created, join_token: joinToken } };
      },
    });
    const { wrapper } = await mountHome();
    expect(wrapper.find('dialog').exists()).toBe(false);
    // The tile after the last environment opens the same dialog as the header's button.
    expect(addButtons(wrapper)).toHaveLength(2);
    await addButtons(wrapper)[1]?.trigger('click');
    expect(wrapper.find('dialog').attributes('aria-label')).toBe('Add environment');

    await wrapper.find('dialog input[name=name]').setValue('nas');
    await wrapper.find('dialog form').trigger('submit');
    await flushPromises();
    expect(wrapper.find('dialog .command').text()).toBe(joinToken.docker_run);
    expect(wrapper.findAll('.tile').map((t) => t.find('h2').text())).toEqual(['local', 'nas']);
    expect(environments.value).toHaveLength(2);

    await wrapper
      .findAll('dialog button')
      .find((b) => b.text() === 'Done')
      ?.trigger('click');
    expect(wrapper.find('dialog').exists()).toBe(false);
    expect(wrapper.text()).not.toContain('nudge_join_secret');
  });

  it('drops a removed environment from the tiles', async () => {
    let list: unknown[] = envs;
    stubApi({
      'GET /environments': () => ({ body: list }),
      'GET /environments/1': { body: { ...envs[0], docker } },
      'DELETE /environments/4': () => {
        list = [envs[0]];
        return { status: 204 };
      },
    });
    vi.spyOn(window, 'prompt').mockReturnValue('nas');
    const { wrapper } = await mountHome();
    await wrapper.findAll('.tile')[1]?.find('.gear').trigger('click');
    await wrapper
      .findAll('dialog button')
      .find((b) => b.text() === 'Remove')
      ?.trigger('click');
    await flushPromises();
    expect(wrapper.findAll('.tile').map((t) => t.find('h2').text())).toEqual(['local']);
    expect(wrapper.find('dialog').exists()).toBe(false);
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

  describe('refreshes the list by itself', () => {
    const refreshMs = 30_000;
    const listCalls = (fetch: ReturnType<typeof stubApi>) =>
      fetch.mock.calls.filter(([url, init]) => url === '/api/v1/environments' && !init?.method).length;

    it('shows status, version and added or removed environments without remounting', async () => {
      vi.useFakeTimers();
      const backup = {
        id: 7,
        name: 'backup',
        kind: 'local',
        online: true,
        created_at: '',
        last_seen_at: null,
      };
      let list: unknown[] = envs;
      stubApi({
        'GET /environments': () => ({ body: list }),
        'GET /environments/1': { body: { ...envs[0], docker } },
      });
      const { wrapper } = await mountHome();
      expect(wrapper.text()).toContain('2 hosts · 1 online');
      expect(wrapper.findAll('.tile')[1]?.text()).toContain('agent 0.9.0');
      // A dialog open on a tile stays open through a refresh.
      const local = wrapper.findAll('.tile')[0];
      await local?.find('.gear').trigger('click');
      expect(wrapper.find('dialog').exists()).toBe(true);

      list = [envs[0], { ...envs[1], online: true, agent: { enrolled: true, version: '1.0.0' } }, backup];
      await vi.advanceTimersByTimeAsync(refreshMs);
      expect(wrapper.text()).toContain('3 hosts · 3 online');
      const tiles = wrapper.findAll('.tile');
      expect(tiles.map((t) => t.find('h2').text())).toEqual(['local', 'nas', 'backup']);
      expect(tiles[1]?.text()).toContain('agent 1.0.0');
      expect(tiles[1]?.text()).not.toContain('Agent not connected');
      expect(tiles[0]?.element).toBe(local?.element);
      expect(wrapper.find('dialog').exists()).toBe(true);

      list = [envs[0]];
      await vi.advanceTimersByTimeAsync(refreshMs);
      expect(wrapper.findAll('.tile').map((t) => t.find('h2').text())).toEqual(['local']);
      expect(wrapper.find('dialog').exists()).toBe(true);
    });

    it('keeps the tiles when a refresh fails', async () => {
      vi.useFakeTimers();
      let fail = false;
      stubApi({
        'GET /environments': () => (fail ? { status: 500, body: { error: 'boom' } } : { body: envs }),
        'GET /environments/1': { body: { ...envs[0], docker } },
      });
      const { wrapper } = await mountHome();
      fail = true;
      await vi.advanceTimersByTimeAsync(refreshMs);
      expect(wrapper.findAll('.tile')).toHaveLength(2);
      expect(wrapper.find('[role=alert]').exists()).toBe(false);
    });

    it('skips hidden tabs, refreshes on becoming visible and stops on unmount', async () => {
      vi.useFakeTimers();
      const fetch = stubApi({
        'GET /environments': { body: envs },
        'GET /environments/1': { body: { ...envs[0], docker } },
      });
      const state = vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('visible');
      const { wrapper } = await mountHome();
      expect(listCalls(fetch)).toBe(1);
      await vi.advanceTimersByTimeAsync(refreshMs);
      expect(listCalls(fetch)).toBe(2);

      state.mockReturnValue('hidden');
      await vi.advanceTimersByTimeAsync(refreshMs * 3);
      expect(listCalls(fetch)).toBe(2);
      state.mockReturnValue('visible');
      document.dispatchEvent(new Event('visibilitychange'));
      await flushPromises();
      expect(listCalls(fetch)).toBe(3);

      wrapper.unmount();
      await vi.advanceTimersByTimeAsync(refreshMs * 3);
      document.dispatchEvent(new Event('visibilitychange'));
      await flushPromises();
      expect(listCalls(fetch)).toBe(3);
    });
  });
});
