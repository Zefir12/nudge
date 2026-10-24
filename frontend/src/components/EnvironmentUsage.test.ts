import { flushPromises, mount } from '@vue/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vite-plus/test';
import { createMemoryHistory } from 'vue-router';

import type { ContainerUsage } from '../api/client';
import { createAppRouter } from '../router';
import { stubApi } from '../test-utils';
import EnvironmentUsage from './EnvironmentUsage.vue';

const usage = (name: string, cpu: number): ContainerUsage => ({
  id: `${name}-id`,
  name,
  time: '2026-10-01T12:00:00Z',
  cpu_percent: cpu,
  memory_usage: 100e6,
  memory_limit: 1e9,
  memory_percent: 10,
  network_rx: 1000,
  network_tx: 2000,
  block_read: 0,
  block_write: 0,
  pids: 1,
});

const body = (containers: ContainerUsage[]) => ({
  containers,
  total: {
    cpu_percent: 150,
    memory_usage: 200e6,
    network_rx: 2000,
    network_tx: 4000,
    block_read: 0,
    block_write: 0,
  },
});

const mountUsage = (env = '1') =>
  mount(EnvironmentUsage, { props: { env }, global: { plugins: [createAppRouter(createMemoryHistory())] } });

beforeEach(() => {
  vi.useFakeTimers();
});

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

describe('EnvironmentUsage', () => {
  it('shows totals and running containers, and refreshes', async () => {
    const fetch = stubApi({
      'GET /environments/1/stats': { body: body([usage('db', 50), usage('web', 180)]) },
    });
    const wrapper = mountUsage();
    await flushPromises();
    expect(wrapper.find('.totals').text()).toContain('150.0% CPU');
    expect(wrapper.find('.totals').text()).toContain('200.0 MB memory');
    const rows = wrapper.findAll('tbody tr');
    expect(rows.map((r) => r.find('a').text())).toEqual(['db', 'web']);
    expect(rows[0]!.find('a').attributes('href')).toBe('/env/1/containers/db-id');
    // A meter can't go past its max; the text keeps the real value.
    expect(rows[1]!.find('meter').element.getAttribute('value')).toBe('100');
    expect(rows[1]!.text()).toContain('180.0%');

    const statsCalls = () => fetch.mock.calls.filter(([url]) => url.endsWith('/stats')).length;
    expect(statsCalls()).toBe(1);
    await vi.advanceTimersByTimeAsync(10_000);
    expect(statsCalls()).toBe(2);
    wrapper.unmount();
    await vi.advanceTimersByTimeAsync(30_000);
    expect(statsCalls()).toBe(2);
  });

  it('says when nothing runs and shows errors', async () => {
    stubApi({
      'GET /environments/1/stats': { body: body([]) },
      'GET /environments/2/stats': { status: 503, body: { error: 'the environment is offline' } },
    });
    const wrapper = mountUsage();
    await flushPromises();
    expect(wrapper.text()).toContain('No running containers.');
    await wrapper.setProps({ env: '2' });
    await flushPromises();
    expect(wrapper.find('[role=alert]').text()).toBe('the environment is offline');
  });
});
