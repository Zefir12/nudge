import { flushPromises, mount } from '@vue/test-utils';
import { afterEach, describe, expect, it, vi } from 'vite-plus/test';

import type { Environment } from '../api/client';
import { stubApi } from '../test-utils';
import EnvironmentSettings from './EnvironmentSettings.vue';
import SelfUpdatePanel from './SelfUpdatePanel.vue';

const local: Environment = {
  id: 1,
  name: 'local',
  kind: 'local',
  online: true,
  created_at: '2026-10-24T12:00:00Z',
  last_seen_at: null,
};
const nas: Environment = {
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
  docker_run: 'docker run -d -e NUDGE_JOIN_TOKEN=nudge_join_secret nudge-agent:latest agent',
  compose: 'services:\n  nudge-agent:\n    environment:\n      NUDGE_JOIN_TOKEN: "nudge_join_secret"\n',
};

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

const mountSettings = (env: Environment) => mount(EnvironmentSettings, { props: { env } });

const button = (wrapper: ReturnType<typeof mountSettings>, text: string) =>
  wrapper.findAll('button').find((b) => b.text() === text);

const calls = (fetch: ReturnType<typeof stubApi>, method: string) =>
  fetch.mock.calls.filter(([, init]) => (init as RequestInit | undefined)?.method === method);

describe('EnvironmentSettings', () => {
  it('has nothing to manage for the local environment', () => {
    const wrapper = mountSettings(local);
    expect(wrapper.findAll('button')).toHaveLength(0);
    expect(wrapper.text()).toContain('no agent to manage');
  });

  it('shows the agent and its actions', () => {
    const wrapper = mountSettings(nas);
    expect(wrapper.find('.facts').text()).toContain('v0.3.0');
    expect(wrapper.findAll('button').map((b) => b.text())).toEqual([
      'New join token',
      'Revoke agent',
      'Remove',
    ]);
  });

  it.each([
    { name: 'an online enrolled agent', env: { ...nas, online: true }, shown: true },
    { name: 'an offline agent', env: nas, shown: false },
    {
      name: 'an agent not enrolled',
      env: { ...nas, online: true, agent: { enrolled: false, version: '' } },
      shown: false,
    },
  ])('offers the version and update for $name: $shown', async ({ env, shown }) => {
    stubApi({
      'GET /environments/2/agent/update': {
        body: { version: 'v0.3.0', image: 'nudge-agent:latest', status: 'current', can_update: true },
      },
    });
    const wrapper = mountSettings(env);
    await flushPromises();
    const panel = wrapper.findComponent(SelfUpdatePanel);
    expect(panel.exists()).toBe(shown);
    if (shown) expect(panel.props('env')).toBe('2');
  });

  it('has nothing to revoke before an agent enrolled', () => {
    const wrapper = mountSettings({ ...nas, agent: { enrolled: false, version: '' } });
    expect(wrapper.find('.facts').text()).toContain('not enrolled');
    expect(button(wrapper, 'Revoke agent')).toBeUndefined();
  });

  it('creates a new join token after confirmation and shows it once', async () => {
    const fetch = stubApi({ 'POST /environments/2/join-tokens': { status: 201, body: joinToken } });
    const confirm = vi.spyOn(window, 'confirm').mockReturnValueOnce(false).mockReturnValueOnce(true);
    const wrapper = mountSettings(nas);

    await button(wrapper, 'New join token')?.trigger('click');
    await flushPromises();
    expect(calls(fetch, 'POST')).toHaveLength(0);
    expect(wrapper.emitted('changed')).toBeUndefined();

    await button(wrapper, 'New join token')?.trigger('click');
    await flushPromises();
    expect(confirm).toHaveBeenCalledTimes(2);
    expect(confirm.mock.calls[1]?.[0]).toContain('keeps working');
    expect(wrapper.find('.command').text()).toBe(joinToken.docker_run);
    expect(wrapper.emitted('changed')).toHaveLength(1);

    await button(wrapper, 'Done')?.trigger('click');
    expect(wrapper.text()).not.toContain('nudge_join_secret');
  });

  it('revokes after confirmation', async () => {
    const fetch = stubApi({ 'DELETE /environments/2/credentials': { status: 204 } });
    vi.spyOn(window, 'confirm').mockReturnValueOnce(false).mockReturnValueOnce(true);
    const wrapper = mountSettings(nas);
    await button(wrapper, 'Revoke agent')?.trigger('click');
    await flushPromises();
    expect(calls(fetch, 'DELETE')).toHaveLength(0);
    await button(wrapper, 'Revoke agent')?.trigger('click');
    await flushPromises();
    expect(calls(fetch, 'DELETE')).toHaveLength(1);
    expect(wrapper.emitted('changed')).toHaveLength(1);
  });

  it('removes after the name is typed', async () => {
    const fetch = stubApi({ 'DELETE /environments/2': { status: 204 } });
    const prompt = vi
      .spyOn(window, 'prompt')
      .mockReturnValueOnce('nope')
      .mockReturnValueOnce(null)
      .mockReturnValueOnce(' nas ');
    const wrapper = mountSettings(nas);

    await button(wrapper, 'Remove')?.trigger('click');
    await flushPromises();
    expect(wrapper.find('[role="alert"]').text()).toContain("didn't match");
    await button(wrapper, 'Remove')?.trigger('click');
    await flushPromises();
    expect(calls(fetch, 'DELETE')).toHaveLength(0);

    await button(wrapper, 'Remove')?.trigger('click');
    await flushPromises();
    expect(prompt).toHaveBeenCalledTimes(3);
    expect(calls(fetch, 'DELETE')).toHaveLength(1);
    expect(wrapper.find('[role="alert"]').exists()).toBe(false);
    expect(wrapper.emitted('changed')).toHaveLength(1);
  });

  it('shows an action error and enables the buttons again', async () => {
    stubApi({
      'DELETE /environments/2/credentials': { status: 409, body: { error: 'not an agent environment' } },
    });
    vi.spyOn(window, 'confirm').mockReturnValue(true);
    const wrapper = mountSettings(nas);
    await button(wrapper, 'Revoke agent')?.trigger('click');
    await flushPromises();
    expect(wrapper.find('[role=alert]').text()).toBe('not an agent environment');
    expect(button(wrapper, 'Revoke agent')?.attributes('disabled')).toBeUndefined();
    // The list is reloaded either way.
    expect(wrapper.emitted('changed')).toHaveLength(1);
  });
});
