import { mount } from '@vue/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vite-plus/test';

import type { StatsSample } from '../api/client';
import { stubSockets, type FakeWebSocket } from '../test-utils';
import ContainerStats from './ContainerStats.vue';

let sockets: FakeWebSocket[];

beforeEach(() => {
  sockets = stubSockets();
});

afterEach(() => {
  vi.unstubAllGlobals();
});

const sample = (sec: number, over: Partial<StatsSample> = {}): StatsSample => ({
  time: new Date(Date.UTC(2026, 9, 1, 12, 0, sec)).toISOString(),
  cpu_percent: 12.345,
  memory_usage: 256e6,
  memory_limit: 1e9,
  memory_percent: 25.6,
  network_rx: sec * 2000,
  network_tx: 1000,
  block_read: 4096,
  block_write: 512,
  pids: 7,
  ...over,
});

const mountStats = () => mount(ContainerStats, { props: { env: '1', id: 'abc 123' } });

describe('ContainerStats', () => {
  it('opens the stats stream and shows the latest sample with charts', async () => {
    const wrapper = mountStats();
    expect(sockets).toHaveLength(1);
    expect(new URL(sockets[0]!.url).pathname).toBe('/api/v1/environments/1/containers/abc%20123/stats');
    expect(wrapper.text()).toContain('Waiting for the first sample');

    sockets[0]!.message(sample(1));
    sockets[0]!.message(sample(3, { cpu_percent: 50 }));
    await wrapper.vm.$nextTick();
    const text = wrapper.text();
    expect(text).toContain('50.0%');
    expect(text).toContain('256.0 MB');
    expect(text).toContain('of 1.0 GB (25.6%)');
    // 4000 bytes over 2 seconds.
    expect(text).toContain('2.0 KB/s');
    expect(text).toContain('7 processes');
    expect(wrapper.findAll('svg.sparkline')).toHaveLength(4);
    expect(wrapper.find('polyline').attributes('points')).toBe('0.00,25.54 100.00,15.00');
  });

  it('keeps about a minute of samples', async () => {
    const wrapper = mountStats();
    for (let i = 0; i < 70; i++) sockets[0]!.message(sample(i));
    await wrapper.vm.$nextTick();
    expect(wrapper.find('polyline').attributes('points')!.split(' ')).toHaveLength(60);
  });

  it('shows the end of the stream', async () => {
    const wrapper = mountStats();
    sockets[0]!.message({ done: true });
    await wrapper.vm.$nextTick();
    expect(wrapper.text()).toContain('The container stopped.');

    await wrapper.setProps({ id: 'other' });
    expect(sockets).toHaveLength(2);
    sockets[1]!.message({ error: 'the stream failed' });
    await wrapper.vm.$nextTick();
    expect(wrapper.find('[role=alert]').text()).toBe('the stream failed');
  });

  it('closes the stream when unmounted', () => {
    const wrapper = mountStats();
    wrapper.unmount();
    expect(sockets[0]!.closed).toBe(true);
  });
});
