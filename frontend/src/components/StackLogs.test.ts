import { flushPromises, mount } from '@vue/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vite-plus/test';

import { stubSockets, type FakeWebSocket } from '../test-utils';
import StackLogs from './StackLogs.vue';

let sockets: FakeWebSocket[];

beforeEach(() => {
  sockets = stubSockets();
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

const containers = [
  { id: 'a1', name: 'web-app-1', service: 'app' },
  { id: 'd1', name: 'web-db-1', service: 'db' },
];
const mountLogs = (cs = containers) => mount(StackLogs, { props: { env: '1', containers: cs } });
const frame = () => new Promise<void>((resolve) => requestAnimationFrame(() => resolve()));
const lines = (w: ReturnType<typeof mountLogs>) => w.findAll('.line').map((l) => l.text());

describe('StackLogs', () => {
  it('follows every container and merges the lines by time', async () => {
    const wrapper = mountLogs();
    expect(sockets.map((s) => new URL(s.url).pathname)).toEqual([
      '/api/v1/environments/1/containers/a1/logs',
      '/api/v1/environments/1/containers/d1/logs',
    ]);
    const url = new URL(sockets[0]!.url);
    expect(url.searchParams.get('follow')).toBe('true');
    expect(url.searchParams.get('timestamps')).toBe('true');
    expect(url.searchParams.get('tail')).toBe('200');

    // Each stream sends its tail in a burst.
    sockets[0]!.message({ stream: 'stdout', time: '2026-10-01T12:00:01Z', line: 'app ready' });
    sockets[0]!.message({ stream: 'stdout', time: '2026-10-01T12:00:03Z', line: 'GET /' });
    sockets[1]!.message({ stream: 'stderr', time: '2026-10-01T12:00:00.5Z', line: 'db starting' });
    sockets[1]!.message({ stream: 'stdout', time: '2026-10-01T12:00:02Z', line: 'db ready' });
    await frame();
    expect(lines(wrapper)).toEqual(['dbdb starting', 'appapp ready', 'dbdb ready', 'appGET /']);
    expect(wrapper.findAll('.line')[0]!.classes()).toContain('stderr');

    await wrapper.find('input[type="search"]').setValue('db');
    expect(lines(wrapper)).toEqual(['dbdb starting', 'dbdb ready']);
  });

  it('reconnects only when the containers change', async () => {
    const wrapper = mountLogs();
    await wrapper.setProps({ containers: [...containers].reverse() });
    expect(sockets).toHaveLength(2);
    await wrapper.setProps({ containers: [containers[0]!, { id: 'd2', name: 'web-db-1', service: 'db' }] });
    await flushPromises();
    expect(sockets).toHaveLength(4);
    expect(sockets[0]!.closed && sockets[1]!.closed).toBe(true);
  });

  it('reports streams that end and closes them on unmount', async () => {
    const wrapper = mountLogs();
    sockets[0]!.message({ error: 'container not found' });
    sockets[1]!.message({ done: true });
    await flushPromises();
    expect(wrapper.text()).toContain('web-app-1: container not found');
    expect(wrapper.text()).toContain('Streams ended');
    wrapper.unmount();
    expect(sockets.every((s) => s.closed)).toBe(true);
  });

  it('says when there is nothing to follow', () => {
    const wrapper = mountLogs([]);
    expect(sockets).toHaveLength(0);
    expect(wrapper.text()).toContain('The stack has no containers.');
  });
});
