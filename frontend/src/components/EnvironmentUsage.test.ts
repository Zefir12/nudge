import { flushPromises, mount } from '@vue/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vite-plus/test';
import { createMemoryHistory } from 'vue-router';

import type { ContainerUsage } from '../api/client';
import { createAppRouter } from '../router';
import { stubApi, stubSockets, type FakeWebSocket } from '../test-utils';
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
  time: '2026-10-01T12:00:05Z',
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

let sockets: FakeWebSocket[];

beforeEach(() => {
  vi.useFakeTimers();
  sockets = stubSockets();
});

afterEach(() => {
  vi.restoreAllMocks();
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

describe('EnvironmentUsage', () => {
  it('shows totals and running containers, updated by each live sample', async () => {
    const fetch = stubApi({});
    const wrapper = mountUsage();
    expect(sockets[0]!.url).toMatch(/\/environments\/1\/metrics\/live$/);
    expect(wrapper.text()).toContain('Sampling running containers');
    sockets[0]!.message(body([usage('db', 50), usage('web', 180)]));
    await flushPromises();
    expect(wrapper.find('.totals').text()).toContain('150.0% CPU');
    expect(wrapper.find('.totals').text()).toContain('200.0 MB memory');
    const rows = wrapper.findAll('tbody tr');
    expect(rows.map((r) => r.find('a').text())).toEqual(['web', 'db']);
    expect(rows[1]!.find('a').attributes('href')).toBe('/env/1/containers/db-id');
    expect(rows[0]!.text()).toContain('180.0%');
    expect(rows[0]!.find('meter').element.getAttribute('value')).toBe('10');

    sockets[0]!.message(body([usage('db', 90), usage('web', 20)]));
    await flushPromises();
    expect(wrapper.findAll('tbody tr a').map((a) => a.text())).toEqual(['db', 'web']);
    // No polling: the stream is the only source.
    await vi.advanceTimersByTimeAsync(30_000);
    expect(fetch.mock.calls.filter(([url]) => url.endsWith('/stats'))).toHaveLength(0);
    wrapper.unmount();
    expect(sockets[0]!.closed).toBe(true);
  });

  it('shows the busiest containers until all are asked for', async () => {
    const cpu = [5, 90, 1, 40, 70, 3, 60];
    const wrapper = mountUsage();
    sockets[0]!.message(body(cpu.map((c, i) => usage(`c${i}`, c))));
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

  it('asks for one overview when the stream fails before a sample', async () => {
    const fetch = stubApi({ 'GET /environments/1/stats': { body: body([usage('db', 50)]) } });
    const wrapper = mountUsage();
    sockets[0]!.onclose?.();
    await flushPromises();
    expect(wrapper.find('.totals').text()).toContain('150.0% CPU');
    expect(wrapper.find('[role=alert]').exists()).toBe(false);
    sockets.at(-1)!.onclose?.();
    await vi.advanceTimersByTimeAsync(5000);
    expect(fetch.mock.calls.filter(([url]) => url.endsWith('/stats'))).toHaveLength(1);
    wrapper.unmount();
  });

  it('says when nothing runs and shows errors', async () => {
    stubApi({ 'GET /environments/2/stats': { status: 503, body: { error: 'the environment is offline' } } });
    const wrapper = mountUsage();
    sockets[0]!.message(body([], 2));
    await flushPromises();
    expect(wrapper.text()).toContain('2 running containers could not be sampled.');
    expect(wrapper.text()).not.toContain('No running containers.');
    await wrapper.setProps({ env: '2' });
    sockets.at(-1)!.onclose?.();
    await flushPromises();
    expect(wrapper.find('[role=alert]').text()).toBe('the environment is offline');
    wrapper.unmount();
  });
});
