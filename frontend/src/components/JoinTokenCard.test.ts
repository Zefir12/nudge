import { flushPromises, mount } from '@vue/test-utils';
import { afterEach, describe, expect, it, vi } from 'vite-plus/test';

import type { JoinToken } from '../api/client';
import JoinTokenCard from './JoinTokenCard.vue';

const joinToken: JoinToken = {
  token: 'nudge_join_secret',
  expires_at: '2026-10-25T12:00:00Z',
  docker_run: 'docker run -d -e NUDGE_JOIN_TOKEN=nudge_join_secret nudge-agent:latest agent',
  compose: 'services:\n  nudge-agent:\n    environment:\n      NUDGE_JOIN_TOKEN: "nudge_join_secret"\n',
};

function mountCard() {
  return mount(JoinTokenCard, { props: { name: 'nas', joinToken } });
}

function button(wrapper: ReturnType<typeof mountCard>, label: string) {
  return wrapper.findAll('button').find((b) => b.text() === label);
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('JoinTokenCard', () => {
  it('shows the command and the compose file', () => {
    const wrapper = mountCard();
    expect(wrapper.find('.command').text()).toBe(joinToken.docker_run);
    // A <pre> keeps the compose file's indentation.
    expect(wrapper.find('pre.compose').element.textContent).toBe(joinToken.compose);
  });

  it.each([
    { label: 'Copy command', want: joinToken.docker_run },
    { label: 'Copy compose file', want: joinToken.compose },
  ])('$label copies its snippet', async ({ label, want }) => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    vi.stubGlobal('navigator', { clipboard: { writeText } });
    const wrapper = mountCard();

    await button(wrapper, label)?.trigger('click');
    await flushPromises();
    expect(writeText).toHaveBeenCalledWith(want);
    // Only the copied snippet's button says so.
    expect(wrapper.findAll('button').filter((b) => b.text() === 'Copied')).toHaveLength(1);
    expect(button(wrapper, label)).toBeUndefined();
  });

  it('says when copying is not available', async () => {
    vi.stubGlobal('navigator', {});
    const wrapper = mountCard();
    await button(wrapper, 'Copy compose file')?.trigger('click');
    await flushPromises();
    expect(wrapper.text()).toContain('Copying is not available here');
    expect(button(wrapper, 'Copy compose file')).toBeDefined();
  });

  it('emits done', async () => {
    const wrapper = mountCard();
    await button(wrapper, 'Done')?.trigger('click');
    expect(wrapper.emitted('done')).toHaveLength(1);
  });
});
