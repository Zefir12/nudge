import { mount } from '@vue/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vite-plus/test';
import { defineComponent, h, nextTick, ref } from 'vue';

import type { LiveUsage } from '../api/client';
import { stubSockets, type FakeWebSocket } from '../test-utils';
import { useLiveUsage } from './useLiveUsage';

let sockets: FakeWebSocket[];

beforeEach(() => {
  vi.useFakeTimers();
  sockets = stubSockets();
});

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

const sample = (cpu: number): LiveUsage => ({
  time: '2026-10-27T12:00:05Z',
  containers: [],
  skipped: 0,
  total: { cpu_percent: cpu, memory_usage: 1, network_rx: 0, network_tx: 0, block_read: 0, block_write: 0 },
});

function mountLive(env = ref('1')) {
  let live!: ReturnType<typeof useLiveUsage>;
  const wrapper = mount(
    defineComponent({
      setup() {
        live = useLiveUsage(env);
        return () => h('div');
      },
    }),
  );
  return { wrapper, env, live };
}

describe('useLiveUsage', () => {
  it('keeps the latest sample and reconnects when the stream ends', async () => {
    const { wrapper, live } = mountLive();
    expect(sockets[0]!.url).toMatch(/\/api\/v1\/environments\/1\/metrics\/live$/);
    sockets[0]!.message(sample(10));
    sockets[0]!.message(sample(20));
    expect(live.usage.value?.total.cpu_percent).toBe(20);
    sockets[0]!.message({ error: 'the usage stream fell behind; reconnect to resume' });
    expect(live.error.value).toContain('fell behind');
    await vi.advanceTimersByTimeAsync(1000);
    expect(sockets).toHaveLength(2);
    sockets[1]!.message(sample(30));
    expect(live.error.value).toBe('');
    expect(live.usage.value?.total.cpu_percent).toBe(30);
    wrapper.unmount();
  });

  it('starts over when the environment changes and closes on unmount', async () => {
    const { wrapper, env, live } = mountLive();
    sockets[0]!.message(sample(10));
    env.value = '2';
    await nextTick();
    expect(live.usage.value).toBeNull();
    expect(sockets[0]!.closed).toBe(true);
    expect(sockets[1]!.url).toContain('/environments/2/metrics/live');
    env.value = '';
    await nextTick();
    expect(sockets[1]!.closed).toBe(true);
    expect(sockets).toHaveLength(2);
    wrapper.unmount();
  });

  it('closes the stream while the tab is hidden', async () => {
    const state = vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('visible');
    const { wrapper, live } = mountLive();
    sockets[0]!.message(sample(10));
    state.mockReturnValue('hidden');
    document.dispatchEvent(new Event('visibilitychange'));
    await nextTick();
    expect(sockets[0]!.closed).toBe(true);
    expect(live.usage.value?.total.cpu_percent).toBe(10);
    state.mockReturnValue('visible');
    document.dispatchEvent(new Event('visibilitychange'));
    await nextTick();
    expect(sockets).toHaveLength(2);
    wrapper.unmount();
    state.mockRestore();
  });
});
