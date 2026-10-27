import { flushPromises, mount } from '@vue/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vite-plus/test';
import { createMemoryHistory } from 'vue-router';

import type { DockerEvent } from '../api/client';
import { createAppRouter } from '../router';
import { stubSockets, type FakeWebSocket } from '../test-utils';
import RecentEvents from './RecentEvents.vue';

const event = (action: string, name: string, type = 'container'): DockerEvent => ({
  time: '2026-10-24T12:00:00Z',
  type,
  action,
  id: `${name}-id`,
  name,
});

const mountPanel = () =>
  mount(RecentEvents, { props: { env: '1' }, global: { plugins: [createAppRouter(createMemoryHistory())] } });

let sockets: FakeWebSocket[];

beforeEach(() => {
  sockets = stubSockets();
});

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('RecentEvents', () => {
  it('shows the latest events, newest first, without exec events', async () => {
    const wrapper = mountPanel();
    expect(wrapper.text()).toContain('No events since the page was opened.');
    expect(wrapper.find('.title a').attributes('href')).toBe('/env/1/events');

    sockets[0]!.message(event('start', 'web'));
    sockets[0]!.message(event('exec_start: sh', 'web'));
    sockets[0]!.message(event('destroy', 'old'));
    await flushPromises();
    const rows = wrapper.findAll('li');
    expect(rows).toHaveLength(2);
    expect(rows[0]!.text()).toContain('destroy');
    expect(rows[0]!.find('a').exists()).toBe(false);
    expect(rows[1]!.find('a').attributes('href')).toBe('/env/1/containers/web-id');
  });

  it('keeps only the last few', async () => {
    const wrapper = mountPanel();
    for (let i = 0; i < 12; i++) sockets[0]!.message(event('start', `c${i}`));
    await flushPromises();
    const names = wrapper.findAll('li a').map((a) => a.text());
    expect(names).toHaveLength(8);
    expect(names[0]).toBe('c11');
  });
});
