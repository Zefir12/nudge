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

const body = (containers: ContainerUsage[], skipped = 0) => ({
  containers,
  skipped,
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
  vi.restoreAllMocks();
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
    expect(rows.map((r) => r.find('a').text())).toEqual(['web', 'db']);
    expect(rows[1]!.find('a').attributes('href')).toBe('/env/1/containers/db-id');
    expect(rows[0]!.text()).toContain('180.0%');
    expect(rows[0]!.find('meter').element.getAttribute('value')).toBe('10');

    const statsCalls = () => fetch.mock.calls.filter(([url]) => url.endsWith('/stats')).length;
    expect(statsCalls()).toBe(1);
    await vi.advanceTimersByTimeAsync(10_000);
    expect(statsCalls()).toBe(2);
    wrapper.unmount();
    await vi.advanceTimersByTimeAsync(30_000);
    expect(statsCalls()).toBe(2);
  });

  it('shows the busiest containers until all are asked for', async () => {
    const cpu = [5, 90, 1, 40, 70, 3, 60];
    stubApi({
      'GET /environments/1/stats': { body: body(cpu.map((c, i) => usage(`c${i}`, c))) },
    });
    const wrapper = mountUsage();
    await flushPromises();
    const names = () => wrapper.findAll('tbody tr a').map((a) => a.text());
    expect(names()).toEqual(['c1', 'c4', 'c6', 'c3', 'c0']);
    const toggle = wrapper.find('button.all');
    expect(toggle.text()).toBe('Show all 7');
    await toggle.trigger('click');
    expect(names()).toHaveLength(7);
    expect(toggle.text()).toBe('Show the busiest 5');
    wrapper.unmount();
  });

  it('aborts the request in flight when unmounted', async () => {
    const fetch = stubApi({ 'GET /environments/1/stats': { body: body([]) } });
    const wrapper = mountUsage();
    const call = fetch.mock.calls.find(([url]) => url.endsWith('/stats'))!;
    wrapper.unmount();
    expect((call[1] as RequestInit).signal!.aborted).toBe(true);
  });

  it('pauses while the tab is hidden', async () => {
    const fetch = stubApi({ 'GET /environments/1/stats': { body: body([]) } });
    vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('hidden');
    const wrapper = mountUsage();
    await vi.advanceTimersByTimeAsync(30_000);
    expect(fetch.mock.calls.filter(([url]) => url.endsWith('/stats'))).toHaveLength(0);
    wrapper.unmount();
  });

  it('says when nothing runs and shows errors', async () => {
    stubApi({
      'GET /environments/1/stats': { body: body([], 2) },
      'GET /environments/2/stats': { status: 503, body: { error: 'the environment is offline' } },
    });
    const wrapper = mountUsage();
    await flushPromises();
    expect(wrapper.text()).toContain('2 running containers could not be sampled.');
    expect(wrapper.text()).not.toContain('No running containers.');
    await wrapper.setProps({ env: '2' });
    await flushPromises();
    expect(wrapper.find('[role=alert]').text()).toBe('the environment is offline');
  });
});
