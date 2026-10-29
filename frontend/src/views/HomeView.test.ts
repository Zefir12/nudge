import { flushPromises, mount } from '@vue/test-utils';
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

beforeEach(() => {
  currentUser.value = admin;
  environments.value = null;
  localStorage.clear();
});

afterEach(() => {
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
});
