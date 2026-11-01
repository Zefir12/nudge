import { flushPromises, mount } from '@vue/test-utils';
import { afterEach, describe, expect, it, vi } from 'vite-plus/test';

import { stubApi } from '../test-utils';
import AddEnvironment from './AddEnvironment.vue';

const joinToken = {
  token: 'nudge_join_secret',
  expires_at: '2026-10-25T12:00:00Z',
  docker_run: 'docker run -d -e NUDGE_JOIN_TOKEN=nudge_join_secret nudge-agent:latest agent',
  compose: 'services:\n  nudge-agent:\n    environment:\n      NUDGE_JOIN_TOKEN: "nudge_join_secret"\n',
};

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('AddEnvironment', () => {
  it('adds an environment and shows its agent command once', async () => {
    stubApi({
      'POST /environments': (init) => {
        expect(JSON.parse(String(init?.body))).toEqual({ name: 'nas', kind: 'agent' });
        return {
          status: 201,
          body: {
            id: 2,
            name: 'nas',
            kind: 'agent',
            online: false,
            created_at: '',
            last_seen_at: null,
            agent: { enrolled: false, version: '' },
            join_token: joinToken,
          },
        };
      },
    });
    const wrapper = mount(AddEnvironment);
    await wrapper.find('input[name=name]').setValue(' nas ');
    await wrapper.find('form').trigger('submit');
    await flushPromises();
    expect(wrapper.emitted('created')).toHaveLength(1);
    expect(wrapper.find('form').exists()).toBe(false);
    expect(wrapper.find('.command').text()).toBe(joinToken.docker_run);
    expect(wrapper.text()).toContain('Start the agent for "nas"');

    await wrapper
      .findAll('button')
      .find((b) => b.text() === 'Done')
      ?.trigger('click');
    expect(wrapper.emitted('done')).toHaveLength(1);
  });

  it('shows a create error', async () => {
    stubApi({
      'POST /environments': { status: 409, body: { error: 'an environment with that name already exists' } },
    });
    const wrapper = mount(AddEnvironment);
    await wrapper.find('input[name=name]').setValue('local');
    await wrapper.find('form').trigger('submit');
    await flushPromises();
    expect(wrapper.find('[role=alert]').text()).toBe('an environment with that name already exists');
    expect(wrapper.emitted('created')).toBeUndefined();
    expect(wrapper.find('button[type=submit]').attributes('disabled')).toBeUndefined();
  });
});
