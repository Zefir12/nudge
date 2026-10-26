import { mount } from '@vue/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vite-plus/test';
import { defineComponent, h, ref } from 'vue';

import type { DockerEvent } from '../api/client';
import { stubSockets, type FakeWebSocket } from '../test-utils';
import { useEvents } from './useEvents';

let sockets: FakeWebSocket[];

beforeEach(() => {
  vi.useFakeTimers();
  sockets = stubSockets();
});

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

function mountEvents(env = ref('1'), types: string[] = []) {
  const got: DockerEvent[] = [];
  const reconnects = { n: 0 };
  let error = ref('');
  const wrapper = mount(
    defineComponent({
      setup() {
        error = useEvents(
          env,
          types,
          (e) => got.push(e),
          () => reconnects.n++,
        ).error;
        return () => h('div');
      },
    }),
  );
  return { wrapper, got, env, reconnects, error: () => error.value };
}

const event: DockerEvent = { time: '2026-10-24T12:00:00Z', type: 'image', action: 'pull', id: 'nginx' };

describe('useEvents', () => {
  it('opens the stream with the types and passes events on', () => {
    const { got } = mountEvents(ref('1'), ['image', 'volume']);
    expect(sockets).toHaveLength(1);
    expect(sockets[0]!.url).toMatch(/\/api\/v1\/environments\/1\/events\?type=image%2Cvolume$/);
    sockets[0]!.message(event);
    expect(got).toEqual([event]);
  });

  it('reconnects with a growing delay, reset by an event', async () => {
    const { error, reconnects } = mountEvents();
    sockets[0]!.message({ error: "the environment's Docker host ended the event stream" });
    expect(error()).toContain('ended the event stream');
    await vi.advanceTimersByTimeAsync(999);
    expect(sockets).toHaveLength(1);
    await vi.advanceTimersByTimeAsync(1);
    expect(sockets).toHaveLength(2);
    expect(reconnects.n).toBe(1);

    sockets[1]!.onclose?.();
    await vi.advanceTimersByTimeAsync(1999);
    expect(sockets).toHaveLength(2);
    await vi.advanceTimersByTimeAsync(1);
    expect(sockets).toHaveLength(3);

    sockets[2]!.message(event);
    expect(error()).toBe('');
    sockets[2]!.onclose?.();
    await vi.advanceTimersByTimeAsync(1000);
    expect(sockets).toHaveLength(4);
  });

  it('follows the environment and stops on unmount', async () => {
    const { wrapper, env } = mountEvents();
    env.value = '2';
    await vi.advanceTimersByTimeAsync(0);
    expect(sockets[0]!.closed).toBe(true);
    expect(sockets[1]!.url).toContain('/environments/2/events');

    sockets[1]!.onclose?.();
    wrapper.unmount();
    await vi.advanceTimersByTimeAsync(60_000);
    expect(sockets).toHaveLength(2);
  });
});
