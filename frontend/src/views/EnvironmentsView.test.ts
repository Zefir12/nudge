import { flushPromises, mount } from '@vue/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vite-plus/test';
import { createMemoryHistory } from 'vue-router';

import { environments } from '../environments';
import { createAppRouter } from '../router';
import { currentUser } from '../session';
import { admin, stubApi } from '../test-utils';
import EnvironmentsView from './EnvironmentsView.vue';

const local = {
  id: 1,
  name: 'local',
  kind: 'local',
  online: true,
  created_at: '2026-10-24T12:00:00Z',
  last_seen_at: null,
};
const nas = {
  id: 2,
  name: 'nas',
  kind: 'agent',
  online: false,
  created_at: '2026-10-24T12:00:00Z',
  last_seen_at: '2026-10-24T13:00:00Z',
  agent: { enrolled: true, version: 'v0.3.0' },
};
const joinToken = {
  token: 'nudge_join_secret',
  expires_at: '2026-10-25T12:00:00Z',
  docker_run:
    'docker run -d --name nudge-agent -e NUDGE_JOIN_TOKEN=nudge_join_secret nudge-agent:latest agent',
  compose: 'services:\n  nudge-agent:\n    environment:\n      NUDGE_JOIN_TOKEN: "nudge_join_secret"\n',
};

beforeEach(() => {
  currentUser.value = admin;
  environments.value = null;
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

async function mountView() {
  const router = createAppRouter(createMemoryHistory());
  await router.push('/environments');
  const wrapper = mount(EnvironmentsView, { global: { plugins: [router] } });
  await flushPromises();
  return wrapper;
}

const button = (wrapper: Awaited<ReturnType<typeof mountView>>, text: string) =>
  wrapper.findAll('button').find((b) => b.text() === text);

describe('EnvironmentsView', () => {
  it('lists environments with their status', async () => {
    stubApi({ 'GET /environments': { body: [local, nas] } });
    const wrapper = await mountView();
    const rows = wrapper.findAll('tbody tr');
    expect(rows).toHaveLength(2);
    expect(rows[0]?.text()).toContain('online');
    expect(rows[0]?.find('button').exists()).toBe(false);
    expect(rows[1]?.text()).toContain('offline');
    expect(rows[1]?.text()).toContain('last seen');
    expect(rows[1]?.text()).toContain('v0.3.0');
  });

  it('adds an environment and shows its agent command once', async () => {
    let list: unknown[] = [local];
    stubApi({
      'GET /environments': () => ({ body: list }),
      'POST /environments': (init) => {
        expect(JSON.parse(String(init?.body))).toEqual({ name: 'nas', kind: 'agent' });
        const created = {
          ...nas,
          online: false,
          last_seen_at: null,
          agent: { enrolled: false, version: '' },
        };
        list = [local, created];
        return { status: 201, body: { ...created, join_token: joinToken } };
      },
    });
    const wrapper = await mountView();
    await wrapper.find('input[name=name]').setValue(' nas ');
    await wrapper.find('form').trigger('submit');
    await flushPromises();
    expect(wrapper.find('.command').text()).toBe(joinToken.docker_run);
    expect(wrapper.findAll('tbody tr')[1]?.text()).toContain('waiting for agent');
    // Not enrolled yet: nothing to revoke.
    expect(button(wrapper, 'Revoke')).toBeUndefined();
    // The shared list (the switcher's) is refreshed.
    expect(environments.value).toHaveLength(2);

    await button(wrapper, 'Done')?.trigger('click');
    expect(wrapper.text()).not.toContain('nudge_join_secret');
  });

  it('shows a create error', async () => {
    stubApi({
      'GET /environments': { body: [local] },
      'POST /environments': { status: 409, body: { error: 'an environment with that name already exists' } },
    });
    const wrapper = await mountView();
    await wrapper.find('input[name=name]').setValue('local');
    await wrapper.find('form').trigger('submit');
    await flushPromises();
    expect(wrapper.find('[role=alert]').text()).toBe('an environment with that name already exists');
  });

  it('creates a new join token after confirmation', async () => {
    const fetch = stubApi({
      'GET /environments': { body: [local, nas] },
      'POST /environments/2/join-tokens': { status: 201, body: joinToken },
    });
    const confirm = vi.spyOn(window, 'confirm').mockReturnValueOnce(false).mockReturnValueOnce(true);
    const wrapper = await mountView();

    await button(wrapper, 'New join token')?.trigger('click');
    await flushPromises();
    expect(
      fetch.mock.calls.some(([url, init]) => init?.method === 'POST' && String(url).includes('join-tokens')),
    ).toBe(false);

    await button(wrapper, 'New join token')?.trigger('click');
    await flushPromises();
    expect(confirm).toHaveBeenCalledTimes(2);
    expect(wrapper.find('.command').text()).toBe(joinToken.docker_run);
  });

  it('revokes after confirmation', async () => {
    let list: unknown[] = [local, nas];
    stubApi({
      'GET /environments': () => ({ body: list }),
      'DELETE /environments/2/credentials': () => {
        list = [local, { ...nas, agent: { enrolled: false, version: 'v0.3.0' } }];
        return { status: 204 };
      },
    });
    vi.spyOn(window, 'confirm').mockReturnValue(true);
    const wrapper = await mountView();
    await button(wrapper, 'Revoke')?.trigger('click');
    await flushPromises();
    expect(button(wrapper, 'Revoke')).toBeUndefined();
    expect(wrapper.findAll('tbody tr')[1]?.text()).toContain('waiting for agent');
  });

  it('removes after the name is typed', async () => {
    let list: unknown[] = [local, nas];
    const fetch = stubApi({
      'GET /environments': () => ({ body: list }),
      'DELETE /environments/2': () => {
        list = [local];
        return { status: 204 };
      },
    });
    const prompt = vi
      .spyOn(window, 'prompt')
      .mockReturnValueOnce('nope')
      .mockReturnValueOnce(null)
      .mockReturnValueOnce('nas');
    const wrapper = await mountView();
    expect(wrapper.findAll('tbody tr')[0]?.text()).not.toContain('Remove');

    await button(wrapper, 'Remove')?.trigger('click');
    await flushPromises();
    expect(wrapper.find('[role="alert"]').text()).toContain("didn't match");
    await button(wrapper, 'Remove')?.trigger('click');
    await flushPromises();
    const deletes = () =>
      fetch.mock.calls.filter(([, init]) => (init as RequestInit | undefined)?.method === 'DELETE');
    expect(deletes()).toHaveLength(0);

    await button(wrapper, 'Remove')?.trigger('click');
    await flushPromises();
    expect(prompt).toHaveBeenCalledTimes(3);
    expect(deletes()).toHaveLength(1);
    expect(wrapper.findAll('tbody tr')).toHaveLength(1);
    expect(wrapper.find('[role="alert"]').exists()).toBe(false);
  });

  it('hides Remove from non-admins', async () => {
    currentUser.value = { ...admin, admin: false };
    stubApi({ 'GET /environments': { body: [local, nas] } });
    const wrapper = await mountView();
    expect(button(wrapper, 'Remove')).toBeUndefined();
  });

  it('keeps an action error when the reload fails too', async () => {
    let fail = false;
    stubApi({
      'GET /environments': () =>
        fail ? { status: 500, body: { error: 'internal server error' } } : { body: [local, nas] },
      'DELETE /environments/2/credentials': () => {
        fail = true;
        return { status: 409, body: { error: 'not an agent environment' } };
      },
    });
    vi.spyOn(window, 'confirm').mockReturnValue(true);
    const wrapper = await mountView();
    await button(wrapper, 'Revoke')?.trigger('click');
    await flushPromises();
    expect(wrapper.find('[role=alert]').text()).toBe('not an agent environment');
    expect(button(wrapper, 'Revoke')?.attributes('disabled')).toBeUndefined();
  });

  it('shows a load error', async () => {
    stubApi({ 'GET /environments': { status: 500, body: { error: 'internal server error' } } });
    const wrapper = await mountView();
    expect(wrapper.find('[role=alert]').text()).toBe('internal server error');
  });
});
